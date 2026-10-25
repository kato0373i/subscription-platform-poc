// Package postgres は integration スキーマのリポジトリ。送信先の鍵はここで暗号化・復号する（ドメインは平文で扱う）。
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/integration/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/integration/internal/adapter/secret"
	"subscription-platform/modules/integration/internal/domain"
)

var (
	ErrNotFound        = errors.New("integration: row not found")
	ErrVersionConflict = errors.New("integration: version conflict")
)

// Repo は 1 トランザクション分のリポジトリ。
type Repo struct {
	Q      *sqlcgen.Queries
	cipher secret.Cipher
}

func New(tx db.Tx, c secret.Cipher) *Repo { return &Repo{Q: sqlcgen.New(tx), cipher: c} }

// NotFound は sqlc の ErrNoRows を ErrNotFound にする。
func NotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (r *Repo) encrypt(s domain.Secret) (string, []byte, error) {
	id, ct, err := r.cipher.Encrypt([]byte(s))
	if err != nil {
		return "", nil, fmt.Errorf("integration: encrypt secret: %w", err)
	}
	return id, ct, nil
}

func (r *Repo) decrypt(keyID string, ct []byte) (domain.Secret, error) {
	p, err := r.cipher.Decrypt(keyID, ct)
	if err != nil {
		return "", fmt.Errorf("integration: decrypt secret: %w", err)
	}
	return domain.Secret(p), nil
}

// EndpointOf は行を送信先にする（鍵を復号する）。
func (r *Repo) EndpointOf(row sqlcgen.IntegrationWebhookEndpoint) (*domain.Endpoint, error) {
	cur, err := r.decrypt(row.SecretKeyID, row.SecretCurrentCiphertext)
	if err != nil {
		return nil, err
	}
	ep := &domain.Endpoint{
		ID: sk.IDFromUUID[domain.EndpointTag](row.ID), TenantID: sk.IDFromUUID[sk.TenantTag](row.TenantID), URL: row.Url, Description: row.Description,
		EventTypes: row.EventTypes, Status: domain.EndpointStatus(row.Status), APIVersion: row.ApiVersion, Current: cur,
		PreviousExpiresAt: row.PreviousExpiresAt, RotatedAt: row.SecretRotatedAt, DeletedAt: row.DeletedAt, Version: int(row.Version),
		BreakerFailures: int(row.BreakerConsecutiveFailures), BreakerOpenUntil: row.BreakerOpenUntil, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.SecretPreviousCiphertext != nil {
		prev, err := r.decrypt(row.SecretKeyID, row.SecretPreviousCiphertext)
		if err != nil {
			return nil, err
		}
		ep.Previous = &prev
	}
	return ep, nil
}

func (r *Repo) GetEndpoint(ctx context.Context, id domain.EndpointID, forUpdate bool) (*domain.Endpoint, error) {
	get := r.Q.GetEndpoint
	if forUpdate {
		get = r.Q.GetEndpointForUpdate
	}
	row, err := get(ctx, id.UUID())
	if err != nil {
		return nil, NotFound(err)
	}
	return r.EndpointOf(row)
}

func (r *Repo) InsertEndpoint(ctx context.Context, ep *domain.Endpoint) error {
	keyID, ct, err := r.encrypt(ep.Current)
	if err != nil {
		return err
	}
	return r.Q.InsertEndpoint(ctx, sqlcgen.InsertEndpointParams{
		ID: ep.ID.UUID(), TenantID: ep.TenantID.UUID(), Url: ep.URL, Description: ep.Description, EventTypes: ep.EventTypes, Status: string(ep.Status),
		ApiVersion: ep.APIVersion, SecretKeyID: keyID, SecretCurrentCiphertext: ct, CreatedAt: ep.CreatedAt,
	})
}

// SaveEndpoint は事業者の編集を保存する（version の楽観ロック。鍵は毎回暗号化し直す）。
func (r *Repo) SaveEndpoint(ctx context.Context, ep *domain.Endpoint) error {
	keyID, cur, err := r.encrypt(ep.Current)
	if err != nil {
		return err
	}
	var prev []byte
	if ep.Previous != nil {
		if _, prev, err = r.cipher.Encrypt([]byte(*ep.Previous)); err != nil {
			return fmt.Errorf("integration: encrypt secret: %w", err)
		}
	}
	n, err := r.Q.UpdateEndpoint(ctx, sqlcgen.UpdateEndpointParams{
		ID: ep.ID.UUID(), Url: ep.URL, Description: ep.Description, EventTypes: ep.EventTypes, Status: string(ep.Status), SecretKeyID: keyID,
		SecretCurrentCiphertext: cur, SecretPreviousCiphertext: prev, PreviousExpiresAt: ep.PreviousExpiresAt, SecretRotatedAt: ep.RotatedAt,
		DeletedAt: ep.DeletedAt, UpdatedAt: ep.UpdatedAt, Version: int32(ep.Version),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrVersionConflict
	}
	ep.Version++
	return nil
}

// SaveBreaker はブレーカーだけを保存する（version は上げない。§8.6）。
func (r *Repo) SaveBreaker(ctx context.Context, ep *domain.Endpoint) error {
	return r.Q.UpdateBreaker(ctx, sqlcgen.UpdateBreakerParams{ID: ep.ID.UUID(), BreakerConsecutiveFailures: int32(ep.BreakerFailures), BreakerOpenUntil: ep.BreakerOpenUntil})
}

func (r *Repo) SubscribedEndpoints(ctx context.Context, extType string) ([]*domain.Endpoint, error) {
	rows, err := r.Q.SubscribedEndpoints(ctx, extType)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Endpoint, 0, len(rows))
	for _, row := range rows {
		ep, err := r.EndpointOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, nil
}

// ---- 配信 ----

func DeliveryOf(row sqlcgen.IntegrationWebhookDelivery) *domain.Delivery {
	d := &domain.Delivery{
		ID: sk.IDFromUUID[domain.DeliveryTag](row.ID), TenantID: sk.IDFromUUID[sk.TenantTag](row.TenantID), EndpointID: sk.IDFromUUID[domain.EndpointTag](row.EndpointID),
		EventID: row.WebhookEventID, Trigger: domain.Trigger(row.Trigger), Status: domain.DeliveryStatus(row.Status), AttemptCount: int(row.AttemptCount),
		NextAttemptAt: row.NextAttemptAt, FirstAttemptedAt: row.FirstAttemptedAt, LastAttemptedAt: row.LastAttemptedAt, DeadlineAt: row.DeadlineAt,
		CompletedAt: row.CompletedAt, Version: int(row.Version), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.RedeliveryOf != nil {
		id := sk.IDFromUUID[domain.DeliveryTag](*row.RedeliveryOf)
		d.RedeliveryOf = &id
	}
	if row.LastStatusCode != nil {
		c := int(*row.LastStatusCode)
		d.LastStatusCode = &c
	}
	if row.LastError != nil {
		d.LastError = *row.LastError
	}
	return d
}

func (r *Repo) GetDelivery(ctx context.Context, id domain.DeliveryID) (*domain.Delivery, error) {
	row, err := r.Q.GetDelivery(ctx, id.UUID())
	if err != nil {
		return nil, NotFound(err)
	}
	return DeliveryOf(row), nil
}

func (r *Repo) InsertDelivery(ctx context.Context, d *domain.Delivery) error {
	var redo *uuid.UUID
	if d.RedeliveryOf != nil {
		u := d.RedeliveryOf.UUID()
		redo = &u
	}
	return r.Q.InsertDelivery(ctx, sqlcgen.InsertDeliveryParams{
		ID: d.ID.UUID(), TenantID: d.TenantID.UUID(), EndpointID: d.EndpointID.UUID(), WebhookEventID: d.EventID, Trigger: string(d.Trigger),
		RedeliveryOf: redo, Status: string(d.Status), NextAttemptAt: d.NextAttemptAt, CreatedAt: d.CreatedAt,
	})
}

// SaveDelivery は状態を保存する。d.Version はドメインで進めた後の値なので、保存前の値で照合する。
func (r *Repo) SaveDelivery(ctx context.Context, d *domain.Delivery, loadedVersion int) error {
	var code *int32
	if d.LastStatusCode != nil {
		c := int32(*d.LastStatusCode)
		code = &c
	}
	var lastErr *string
	if d.LastError != "" {
		lastErr = &d.LastError
	}
	n, err := r.Q.UpdateDelivery(ctx, sqlcgen.UpdateDeliveryParams{
		ID: d.ID.UUID(), Status: string(d.Status), AttemptCount: int32(d.AttemptCount), NextAttemptAt: d.NextAttemptAt, FirstAttemptedAt: d.FirstAttemptedAt,
		LastAttemptedAt: d.LastAttemptedAt, DeadlineAt: d.DeadlineAt, LastStatusCode: code, LastError: lastErr, CompletedAt: d.CompletedAt,
		UpdatedAt: d.UpdatedAt, Version: int32(loadedVersion),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrVersionConflict
	}
	return nil
}

func (r *Repo) InsertAttempt(ctx context.Context, d *domain.Delivery, res domain.AttemptResult) error {
	var code *int32
	if res.StatusCode != nil {
		c := int32(*res.StatusCode)
		code = &c
	}
	var kind, excerpt *string
	if res.ErrorKind != "" {
		k := string(res.ErrorKind)
		kind = &k
	}
	if res.Excerpt != "" {
		excerpt = &res.Excerpt
	}
	return r.Q.InsertAttempt(ctx, sqlcgen.InsertAttemptParams{
		ID: uuid.Must(uuid.NewV7()), TenantID: d.TenantID.UUID(), DeliveryID: d.ID.UUID(), AttemptNo: int32(d.AttemptCount), RequestedAt: res.RequestedAt,
		DurationMs: int32(res.Duration / time.Millisecond), StatusCode: code, ErrorKind: kind, ResponseExcerpt: excerpt,
	})
}
