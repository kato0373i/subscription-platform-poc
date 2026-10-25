package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/integration/internal/adapter/postgres"
	"subscription-platform/modules/integration/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/integration/internal/domain"
)

func listEndpointsParams(limit int, afterCreated *time.Time, afterID *domain.EndpointID) sqlcgen.ListEndpointsParams {
	p := sqlcgen.ListEndpointsParams{Limit: int32(limit), AfterCreated: afterCreated}
	if afterID != nil {
		u := afterID.UUID()
		p.AfterID = &u
	}
	return p
}

// DeliveryView は配信の一覧・取得の 1 件（§7.1 の Delivery）。
type DeliveryView struct {
	Delivery       *domain.Delivery
	EventType      string
	EventCreatedAt time.Time // 外部イベントの created_at（内部イベントの発生日時）
}

// DeliveryDetail は取得の応答（試行と送った JSON を含む）。
type DeliveryDetail struct {
	DeliveryView
	Attempts []sqlcgen.IntegrationWebhookDeliveryAttempt
	Payload  string
}

// DeliveryFilter は一覧の絞り込み。
type DeliveryFilter struct {
	EndpointID    *uuid.UUID
	Status        *string
	EventID       *string
	EventType     *string
	BeforeCreated *time.Time
	BeforeID      *uuid.UUID
	Limit         int
}

// DeliveryPage は一覧の 1 ページ（新しい順）。
type DeliveryPage struct {
	Data    []DeliveryView
	HasMore bool
}

// ListDeliveries は事業者の配信の一覧（console は tenant スコープ、ops は system スコープで tenant を指定する）。
func (s *Service) ListDeliveries(ctx context.Context, scope Scope, f DeliveryFilter) (DeliveryPage, error) {
	var page DeliveryPage
	err := s.d.DB.ReadTx(scope.ctx(ctx), func(ctx context.Context, tx db.Tx) error {
		p := sqlcgen.ListDeliveriesParams{
			Limit: int32(f.Limit + 1), EndpointID: f.EndpointID, Status: f.Status, EventID: f.EventID, EventType: f.EventType,
			BeforeCreated: f.BeforeCreated, BeforeID: f.BeforeID,
		}
		if scope.Ops {
			u := scope.Tenant.UUID()
			p.OnlyTenant = &u
		}
		rows, err := sqlcgen.New(tx).ListDeliveries(ctx, p)
		if err != nil {
			return err
		}
		for i, row := range rows {
			if i == f.Limit {
				page.HasMore = true
				break
			}
			d := postgres.DeliveryOf(sqlcgen.IntegrationWebhookDelivery{
				ID: row.ID, TenantID: row.TenantID, EndpointID: row.EndpointID, WebhookEventID: row.WebhookEventID, Trigger: row.Trigger,
				RedeliveryOf: row.RedeliveryOf, Status: row.Status, AttemptCount: row.AttemptCount, NextAttemptAt: row.NextAttemptAt,
				FirstAttemptedAt: row.FirstAttemptedAt, LastAttemptedAt: row.LastAttemptedAt, DeadlineAt: row.DeadlineAt, LastStatusCode: row.LastStatusCode,
				LastError: row.LastError, CompletedAt: row.CompletedAt, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			})
			page.Data = append(page.Data, DeliveryView{Delivery: d, EventType: row.EventType, EventCreatedAt: row.EventOccurredAt})
		}
		return nil
	})
	return page, err
}

// Scope は console（tenant スコープ）か ops（system スコープ＋対象の事業者）か。
type Scope struct {
	Tenant sk.TenantID
	Ops    bool
}

func (sc Scope) ctx(ctx context.Context) context.Context {
	if sc.Ops {
		return db.WithSystemScope(db.ClearScope(ctx), "ops webhook deliveries")
	}
	return db.WithTenantScope(ctx, sc.Tenant)
}

// GetDelivery は試行と送った JSON を含めて返す。
func (s *Service) GetDelivery(ctx context.Context, t sk.TenantID, id domain.DeliveryID) (DeliveryDetail, error) {
	var out DeliveryDetail
	err := s.d.DB.ReadTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		r := s.repo(tx)
		d, err := r.GetDelivery(ctx, id)
		if err != nil {
			return err
		}
		ev, err := r.Q.GetWebhookEvent(ctx, d.EventID)
		if err != nil {
			return err
		}
		attempts, err := r.Q.ListAttempts(ctx, d.ID.UUID())
		if err != nil {
			return err
		}
		out = DeliveryDetail{DeliveryView: DeliveryView{Delivery: d, EventType: ev.EventType, EventCreatedAt: ev.OccurredAt}, Attempts: attempts, Payload: ev.Payload}
		return nil
	})
	if errors.Is(err, postgres.ErrNotFound) {
		return out, ErrNotFound
	}
	return out, err
}

// Redeliver は手動再送：同じ外部イベントで manual の新しい配信を作る（配信ループが 1 秒以内に送る）。
func (s *Service) Redeliver(ctx context.Context, scope Scope, id domain.DeliveryID) (DeliveryView, error) {
	var out DeliveryView
	err := s.d.DB.InTx(scope.ctx(ctx), func(ctx context.Context, tx db.Tx) error {
		r := s.repo(tx)
		src, err := r.GetDelivery(ctx, id)
		if err != nil {
			return err
		}
		if scope.Ops {
			scope.Tenant = src.TenantID
		}
		ep, err := r.GetEndpoint(ctx, src.EndpointID, false)
		if err != nil {
			return err
		}
		if !ep.Deliverable() {
			return ErrEndpointUnavailable
		}
		ev, err := r.Q.GetWebhookEvent(ctx, src.EventID)
		if err != nil {
			return err
		}
		nd := domain.NewManualDelivery(src, s.d.Clock.Now())
		if err := r.InsertDelivery(ctx, nd); err != nil {
			if db.IsUniqueViolation(err) {
				return ErrDeliveryInProgress // E6：同じ送信先・同じイベントに pending がある
			}
			return err
		}
		out = DeliveryView{Delivery: nd, EventType: ev.EventType, EventCreatedAt: ev.OccurredAt}
		return nil
	})
	if errors.Is(err, postgres.ErrNotFound) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	opts := []auditctx.Option{}
	if scope.Ops {
		opts = append(opts, auditctx.WithTenant(out.Delivery.TenantID))
	}
	auditctx.Annotate(ctx, "webhook_delivery.redeliver", auditctx.Target{Type: "webhook_delivery", ID: id.String()},
		map[string]any{"delivery_id": id.String()}, map[string]any{"delivery_id": out.Delivery.ID.String()}, opts...)
	return out, nil
}
