package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/inbox"
	"subscription-platform/internal/platform/messaging"
	sk "subscription-platform/internal/sharedkernel"
	customer "subscription-platform/modules/customer/contracts"
	"subscription-platform/modules/integration/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/integration/internal/domain"
)

type customerIDKey struct{}

// WebhookHandler は integration-webhook の購読（§5.2）。他モジュールの呼び出し（ResolveTenantCustomerID）はトランザクションの外で先に行い、
// Inbox の記録と外部イベント・配信の作成は 1 つのトランザクションで行う（接続を握ったまま別の接続を待たないため）。
func (s *Service) WebhookHandler(consumer string) messaging.Handler {
	inner := inbox.Wrap(s.d.DB, consumer, s.handleWebhook)
	return func(ctx context.Context, env sk.EventEnvelope) error {
		ext, ok := domain.ExternalType(env)
		if !ok || env.TenantID == nil {
			return inner(ctx, env) // 対象外：Inbox だけ記録して ack（E12）
		}
		var n int
		if err := s.d.DB.ReadTx(db.WithTenantScope(ctx, *env.TenantID), func(ctx context.Context, tx db.Tx) error {
			eps, err := sqlcgen.New(tx).SubscribedEndpoints(ctx, ext)
			n = len(eps)
			return err
		}); err != nil {
			return err
		}
		if n > 0 {
			cid, err := domain.CustomerAccountOf(env)
			if err != nil {
				return messaging.Permanent(err)
			}
			if cid != nil {
				cus, err := s.d.Customer.ResolveTenantCustomerID(db.ClearScope(ctx), *env.TenantID, *cid)
				if err != nil {
					return fmt.Errorf("integration: resolve tenant customer id: %w", err) // E2：ack せずに再配信
				}
				ctx = context.WithValue(ctx, customerIDKey{}, string(cus))
			}
		}
		return inner(ctx, env)
	}
}

func (s *Service) handleWebhook(ctx context.Context, tx db.Tx, env sk.EventEnvelope) error {
	ext, ok := domain.ExternalType(env)
	if !ok {
		return nil
	}
	r := s.repo(tx)
	eps, err := r.SubscribedEndpoints(ctx, ext)
	if err != nil {
		return err
	}
	if len(eps) == 0 {
		return nil // 過去のイベントを後から作った送信先に送ることはしない
	}
	cus, _ := ctx.Value(customerIDKey{}).(string)
	if cid, _ := domain.CustomerAccountOf(env); cid != nil && cus == "" {
		return errors.New("integration: tenant customer id is not resolved")
	}
	evtID := domain.NewEventID()
	body, err := domain.BuildPayload(evtID, ext, env, cus)
	if err != nil {
		return messaging.Permanent(err)
	}
	now := s.d.Clock.Now()
	n, err := r.Q.InsertWebhookEvent(ctx, sqlcgen.InsertWebhookEventParams{
		ID: evtID, TenantID: env.TenantID.UUID(), SourceEventID: env.EventID.UUID(), EventType: ext, AggregateType: env.AggregateType,
		AggregateID: env.AggregateID, AggregateVersion: int32(env.AggregateVersion), OccurredAt: env.OccurredAt, Payload: string(body), CreatedAt: now,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return nil // E1：Inbox をすり抜けても外部イベントは 1 つ
	}
	for _, ep := range eps {
		if err := r.InsertDelivery(ctx, domain.NewAutoDelivery(ep, evtID, now)); err != nil {
			return err
		}
	}
	return nil
}

type emailIDsKey struct{}

// EmailHandler は integration-email の購読（§5.3）。行を作ってコミットした直後に、同じ起動の中でそのメールを送る（§8.3）。
func (s *Service) EmailHandler(consumer string) messaging.Handler {
	inner := inbox.Wrap(s.d.DB, consumer, s.handleEmail, inbox.AllowSystemScope("customer.withdrawn"))
	return func(ctx context.Context, env sk.EventEnvelope) error {
		ids := &[]uuid.UUID{}
		if err := inner(context.WithValue(ctx, emailIDsKey{}, ids), env); err != nil {
			return err
		}
		for _, id := range *ids {
			// 送信の失敗は行に記録して再送ループに任せる。SQS のメッセージは成功として扱う
			if err := s.SendEmail(db.ClearScope(ctx), id); err != nil {
				s.logEmailError(ctx, id, err)
			}
		}
		return nil
	}
}

func (s *Service) handleEmail(ctx context.Context, tx db.Tx, env sk.EventEnvelope) error {
	q := sqlcgen.New(tx)
	now := s.d.Clock.Now()
	switch env.EventType {
	case customer.EventCustomerWithdrawn:
		var p customer.CustomerWithdrawnPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return messaging.Permanent(err)
		}
		_, err := q.SkipPendingEmailsForCustomer(ctx, sqlcgen.SkipPendingEmailsForCustomerParams{CustomerAccountID: uuidPtr(p.CustomerAccountID.UUID()), UpdatedAt: now})
		return err
	case "billing.invoice_issued":
		var p struct {
			InvoiceID      sk.InvoiceID      `json:"invoice_id"`
			SubscriptionID sk.SubscriptionID `json:"subscription_id"`
			Total          int64             `json:"total"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return messaging.Permanent(err)
		}
		return q.UpsertInvoiceSummary(ctx, sqlcgen.UpsertInvoiceSummaryParams{
			InvoiceID: p.InvoiceID.UUID(), TenantID: env.TenantID.UUID(), SubscriptionID: p.SubscriptionID.UUID(), Total: int32(p.Total), UpdatedAt: now,
		})
	case "billing.invoice_payment_failed":
		var p struct {
			InvoiceID        sk.InvoiceID `json:"invoice_id"`
			NextCollectionAt *time.Time   `json:"next_collection_at"`
			CollectionMode   string       `json:"collection_mode"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return messaging.Permanent(err)
		}
		if p.CollectionMode == "caller_managed" {
			return nil // 申込の決済失敗はメールも投影も作らない（P-BIL-5）
		}
		return q.UpdateInvoiceNextCollection(ctx, sqlcgen.UpdateInvoiceNextCollectionParams{InvoiceID: p.InvoiceID.UUID(), NextCollectionAt: p.NextCollectionAt, UpdatedAt: now})
	}
	plan, err := domain.PlanEmail(env)
	if err != nil {
		return messaging.Permanent(err)
	}
	if plan == nil {
		return nil
	}
	src := env.EventID.UUID()
	id, inserted, err := s.insertEmail(ctx, q, plan, &src, now)
	if err != nil || !inserted {
		return err
	}
	if ids, ok := ctx.Value(emailIDsKey{}).(*[]uuid.UUID); ok {
		*ids = append(*ids, id)
	}
	return nil
}

// insertEmail は行を作る（dedupe_key で 1 つ。locked_until = now + 60 秒で再送ループから隠し、コミットの直後に送る）。
func (s *Service) insertEmail(ctx context.Context, q *sqlcgen.Queries, plan *domain.EmailPlan, src *uuid.UUID, now time.Time) (uuid.UUID, bool, error) {
	vars, err := json.Marshal(plan.Variables)
	if err != nil {
		return uuid.Nil, false, err
	}
	id := uuid.Must(uuid.NewV7())
	lock := now.Add(emailLease)
	p := sqlcgen.InsertEmailParams{
		ID: id, TenantID: plan.TenantID.UUID(), TemplateKey: plan.TemplateKey, DedupeKey: plan.DedupeKey, RecipientType: string(plan.Recipient),
		SourceEventID: src, Variables: vars, NextAttemptAt: &now, LockedUntil: &lock,
	}
	if plan.CustomerID != nil {
		p.CustomerAccountID = uuidPtr(plan.CustomerID.UUID())
	}
	if plan.SubscriptionID != nil {
		p.SubscriptionID = uuidPtr(plan.SubscriptionID.UUID())
	}
	if plan.InvoiceID != nil {
		p.InvoiceID = uuidPtr(plan.InvoiceID.UUID())
	}
	if plan.EndpointID != nil {
		p.EndpointID = uuidPtr(plan.EndpointID.UUID())
	}
	n, err := q.InsertEmail(ctx, p)
	if err != nil {
		return uuid.Nil, false, err
	}
	return id, n == 1, nil
}

func uuidPtr(u uuid.UUID) *uuid.UUID { return &u }
