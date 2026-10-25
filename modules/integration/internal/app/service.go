// Package app は integration のユースケース：送信先の管理（§7.1）、購読処理（§5）、配信ループ（§8.1）、メールの送信（§8.3）。
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	customer "subscription-platform/modules/customer/contracts"
	"subscription-platform/modules/integration/internal/adapter/mail"
	"subscription-platform/modules/integration/internal/adapter/postgres"
	"subscription-platform/modules/integration/internal/adapter/secret"
	"subscription-platform/modules/integration/internal/adapter/webhook"
	"subscription-platform/modules/integration/internal/domain"
	tenant "subscription-platform/modules/tenant/contracts"
)

// URLs はメールのリンク（§5.3）。
type URLs struct {
	PaymentMethodUpdate string // {subscription_id} を置き換える
	Mypage              string
	Console             string
}

// Deps はユースケースの依存。
type Deps struct {
	DB          *db.ModuleDB
	Clock       sk.Clock
	Cipher      secret.Cipher
	Customer    customer.Service
	Tenant      tenant.Service
	Mailer      mail.Mailer
	Sender      *webhook.Sender
	Insecure    domain.InsecureHosts
	Schedule    domain.Schedule
	Breaker     domain.BreakerConfig
	Parallelism int
	URLs        URLs
	// Resolve は登録時の名前解決（U5）。nil なら net.DefaultResolver。
	Resolve domain.Resolver
	// TaskID は配信ループのリースの owner（プロセスごとに一意）。
	TaskID string
}

// Service は integration のユースケース。
type Service struct{ d Deps }

// New はユースケースを組み立てる。
func New(d Deps) *Service {
	if d.Resolve == nil {
		d.Resolve = func(host string) ([]netip.Addr, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if d.TaskID == "" {
		d.TaskID = sk.NewID[sk.EventTag]().String()
	}
	if d.Parallelism < 1 {
		d.Parallelism = 1
	}
	return &Service{d: d}
}

// エラー（http アダプターが Problem にする）。
var (
	ErrNotFound            = errors.New("integration: not found")
	ErrConflict            = errors.New("integration: version conflict")
	ErrEndpointLimit       = errors.New("integration: webhook endpoint limit exceeded")
	ErrDeliveryInProgress  = errors.New("integration: webhook delivery in progress")
	ErrEndpointUnavailable = errors.New("integration: webhook endpoint unavailable")
)

// ValidationError は入力の検証の失敗（errors[].code を持つ）。
type ValidationError struct{ Field, Code string }

func (e *ValidationError) Error() string { return fmt.Sprintf("integration: %s: %s", e.Field, e.Code) }

func (s *Service) repo(tx db.Tx) *postgres.Repo { return postgres.New(tx, s.d.Cipher) }

func validationOf(err error) error {
	var ue *domain.URLError
	switch {
	case errors.As(err, &ue):
		return &ValidationError{Field: "url", Code: ue.Code}
	case errors.Is(err, domain.ErrEventTypesEmpty):
		return &ValidationError{Field: "event_types", Code: "event_types_empty"}
	case errors.Is(err, domain.ErrEventTypeUnknown):
		return &ValidationError{Field: "event_types", Code: "event_type_unknown"}
	case errors.Is(err, domain.ErrDescriptionLength):
		return &ValidationError{Field: "description", Code: "too_long"}
	case errors.Is(err, domain.ErrGracePeriod):
		return &ValidationError{Field: "grace_period_hours", Code: "out_of_range"}
	}
	return err
}

// ---- 送信先（§7.1） ----

// CreateEndpointInput は作成の入力。
type CreateEndpointInput struct {
	URL         string
	EventTypes  []string
	Description string
	Enabled     bool
}

// CreateEndpoint は送信先を作り、平文の鍵を一度だけ返す。
func (s *Service) CreateEndpoint(ctx context.Context, t sk.TenantID, in CreateEndpointInput) (*domain.Endpoint, domain.Secret, error) {
	if err := domain.ValidateURL(in.URL, s.d.Insecure, s.d.Resolve); err != nil {
		return nil, "", validationOf(err)
	}
	ep, err := domain.NewEndpoint(t, in.URL, in.EventTypes, in.Description, in.Enabled, s.d.Clock.Now())
	if err != nil {
		return nil, "", validationOf(err)
	}
	err = s.d.DB.InTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		r := s.repo(tx)
		// 同じ事業者の同時作成で上限を超えないように、事業者ごとのアドバイザリーロックで直列にする
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('integration.endpoints:' || $1::text, 0))`, t.String()); err != nil {
			return err
		}
		n, err := r.Q.CountActiveEndpoints(ctx, t.UUID())
		if err != nil {
			return err
		}
		if n >= domain.MaxEndpointsPerTenant {
			return ErrEndpointLimit
		}
		return r.InsertEndpoint(ctx, ep)
	})
	if err != nil {
		return nil, "", err
	}
	auditctx.Annotate(ctx, "webhook_endpoint.create", auditctx.Target{Type: "webhook_endpoint", ID: ep.ID.String()}, nil, endpointAudit(ep))
	return ep, ep.Current, nil
}

func endpointAudit(ep *domain.Endpoint) map[string]any {
	return map[string]any{"url": ep.URL, "event_types": ep.EventTypes, "status": ep.Status}
}

// GetEndpoint は削除済みを not_found にする。
func (s *Service) GetEndpoint(ctx context.Context, t sk.TenantID, id domain.EndpointID) (*domain.Endpoint, error) {
	var ep *domain.Endpoint
	err := s.d.DB.ReadTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		var err error
		ep, err = s.repo(tx).GetEndpoint(ctx, id, false)
		return err
	})
	if errors.Is(err, postgres.ErrNotFound) || (err == nil && ep.DeletedAt != nil) {
		return nil, ErrNotFound
	}
	return ep, err
}

// EndpointPage は一覧の 1 ページ。
type EndpointPage struct {
	Data    []*domain.Endpoint
	HasMore bool
}

// ListEndpoints は作成順（削除済みを除く）。after は前のページの最後の送信先。
func (s *Service) ListEndpoints(ctx context.Context, t sk.TenantID, limit int, afterCreated *time.Time, afterID *domain.EndpointID) (EndpointPage, error) {
	var page EndpointPage
	err := s.d.DB.ReadTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		r := s.repo(tx)
		p := listEndpointsParams(limit+1, afterCreated, afterID)
		rows, err := r.Q.ListEndpoints(ctx, p)
		if err != nil {
			return err
		}
		for i, row := range rows {
			if i == limit {
				page.HasMore = true
				break
			}
			ep, err := r.EndpointOf(row)
			if err != nil {
				return err
			}
			page.Data = append(page.Data, ep)
		}
		return nil
	})
	return page, err
}

// UpdateEndpoint は部分更新（If-Match は任意の楽観ロック）。
func (s *Service) UpdateEndpoint(ctx context.Context, t sk.TenantID, id domain.EndpointID, p domain.Patch, ifMatch *int) (*domain.Endpoint, error) {
	if p.URL != nil {
		if err := domain.ValidateURL(*p.URL, s.d.Insecure, s.d.Resolve); err != nil {
			return nil, validationOf(err)
		}
	}
	var ep *domain.Endpoint
	var before map[string]any
	err := s.d.DB.InTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		r := s.repo(tx)
		var err error
		if ep, err = s.loadLive(ctx, r, id); err != nil {
			return err
		}
		if ifMatch != nil && *ifMatch != ep.Version {
			return ErrConflict
		}
		before = endpointAudit(ep)
		if err := ep.Update(p, s.d.Clock.Now()); err != nil {
			return validationOf(err)
		}
		return s.saveEndpoint(ctx, r, ep)
	})
	if err != nil {
		return nil, err
	}
	auditctx.Annotate(ctx, "webhook_endpoint.update", auditctx.Target{Type: "webhook_endpoint", ID: id.String()}, before, endpointAudit(ep))
	return ep, nil
}

// DeleteEndpoint は論理削除する（pending の配信は配信ループが abandoned にする）。
func (s *Service) DeleteEndpoint(ctx context.Context, t sk.TenantID, id domain.EndpointID) error {
	var before map[string]any
	err := s.d.DB.InTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		r := s.repo(tx)
		ep, err := s.loadLive(ctx, r, id)
		if err != nil {
			return err
		}
		before = endpointAudit(ep)
		ep.Delete(s.d.Clock.Now())
		return s.saveEndpoint(ctx, r, ep)
	})
	if err != nil {
		return err
	}
	auditctx.Annotate(ctx, "webhook_endpoint.delete", auditctx.Target{Type: "webhook_endpoint", ID: id.String()}, before, map[string]any{"deleted": true})
	return nil
}

// RotateSecret は鍵をローテーションし、新しい平文の鍵と前の鍵の期限を返す（監査には期限だけ）。
func (s *Service) RotateSecret(ctx context.Context, t sk.TenantID, id domain.EndpointID, graceHours int) (domain.Secret, *time.Time, error) {
	var ep *domain.Endpoint
	err := s.d.DB.InTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		r := s.repo(tx)
		var err error
		if ep, err = s.loadLive(ctx, r, id); err != nil {
			return err
		}
		if err := ep.RotateSecret(s.d.Clock.Now(), graceHours); err != nil {
			return validationOf(err)
		}
		return s.saveEndpoint(ctx, r, ep)
	})
	if err != nil {
		return "", nil, err
	}
	auditctx.Annotate(ctx, "webhook_endpoint.rotate_secret", auditctx.Target{Type: "webhook_endpoint", ID: id.String()}, nil,
		map[string]any{"previous_expires_at": ep.PreviousExpiresAt})
	return ep.Current, ep.PreviousExpiresAt, nil
}

func (s *Service) loadLive(ctx context.Context, r *postgres.Repo, id domain.EndpointID) (*domain.Endpoint, error) {
	ep, err := r.GetEndpoint(ctx, id, true)
	if errors.Is(err, postgres.ErrNotFound) || (err == nil && ep.DeletedAt != nil) {
		return nil, ErrNotFound
	}
	return ep, err
}

func (s *Service) saveEndpoint(ctx context.Context, r *postgres.Repo, ep *domain.Endpoint) error {
	if err := r.SaveEndpoint(ctx, ep); err != nil {
		if errors.Is(err, postgres.ErrVersionConflict) {
			return ErrConflict
		}
		return err
	}
	return nil
}
