// Package app は subscription モジュールのユースケース（contracts.Service・me / console / tenant の API・トライアル終了予告のジョブ）。
package app

import (
	"context"
	"errors"
	"fmt"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/outbox"
	sk "subscription-platform/internal/sharedkernel"
	catalogcontracts "subscription-platform/modules/catalog/contracts"
	c "subscription-platform/modules/subscription/contracts"
	"subscription-platform/modules/subscription/internal/adapter/postgres"
	"subscription-platform/modules/subscription/internal/domain"
)

// Deps はユースケースの依存。
type Deps struct {
	DB      *db.ModuleDB
	Outbox  *outbox.Writer
	Clock   sk.Clock
	Catalog catalogcontracts.Service // プラン変更先の検証（GetPlanSnapshot。ID の検証のみ）
}

// Service はユースケースの入口。
type Service struct{ d Deps }

func New(d Deps) *Service { return &Service{d: d} }

var _ c.Service = (*Service)(nil)

const maxAttempts = 3 // 楽観ロックの再試行（§5）

func mapNotFound(err error) error {
	if errors.Is(err, postgres.ErrNotFound) {
		return c.ErrNotFound
	}
	return err
}

// tenantOf は契約の事業者を引く（ジョブ・Orchestration は契約 ID だけを持つので、BYPASSRLS のロールで読む。規約 M5）。
func (s *Service) tenantOf(ctx context.Context, id sk.SubscriptionID) (sk.TenantID, error) {
	var t sk.TenantID
	err := s.d.DB.ReadTx(db.WithSystemScope(ctx, "subscription_lookup"), func(ctx context.Context, tx db.Tx) error {
		x, err := postgres.New(tx).Get(ctx, id)
		if err != nil {
			return mapNotFound(err)
		}
		t = x.State().TenantID
		return nil
	})
	return t, err
}

// mutate は ctx のスコープで契約を読み、fn を適用して保存する。楽観ロックの衝突は読み直して再適用する（最大 3 回）。
// 記録されたイベントは同じトランザクションで Outbox に書く。
func (s *Service) mutate(ctx context.Context, id sk.SubscriptionID, fn func(x *domain.Subscription) error) (*domain.Subscription, error) {
	var out *domain.Subscription
	for attempt := 1; ; attempt++ {
		err := s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
			repo := postgres.New(tx)
			x, err := repo.Get(ctx, id)
			if err != nil {
				return mapNotFound(err)
			}
			from := x.State().Status
			if err := fn(x); err != nil {
				return err
			}
			out = x
			events := x.Events()
			if len(events) == 0 && !x.Dirty() {
				return nil // 冪等な再実行（○）。保存もしない
			}
			if err := repo.Update(ctx, x, from, s.d.Clock.Now()); err != nil {
				return err
			}
			x.MarkSaved()
			return s.appendEvents(ctx, tx, x, events)
		})
		if errors.Is(err, c.ErrVersionConflict) && attempt < maxAttempts {
			continue
		}
		if err != nil {
			return nil, err
		}
		return out, nil
	}
}

// appendEvents はイベントを Outbox に書く。aggregate_version は保存後の version（同じトランザクションの複数のイベントは同じ値）。
func (s *Service) appendEvents(ctx context.Context, tx db.Tx, x *domain.Subscription, events []domain.Event) error {
	st := x.State()
	version := int64(st.Version)
	for _, e := range events {
		payload := map[string]any{}
		switch e.Type {
		case c.EventSignupFailed, c.EventPaymentMethodChanged:
			// 01 の最小項目（スナップショットなし）＋ tenant_id（§3.1）
			payload["subscription_id"] = st.ID
			payload["tenant_id"] = st.TenantID
		default:
			payload["subscription"] = snapshot(st, int(version))
		}
		for k, v := range e.Extra {
			payload[k] = v
		}
		env, err := sk.NewEnvelope(e.Type, "subscription", st.ID.UUID(), version, &st.TenantID, s.d.Clock.Now(), payload)
		if err != nil {
			return err
		}
		if err := s.d.Outbox.Append(ctx, tx, env); err != nil {
			return fmt.Errorf("subscription: outbox: %w", err)
		}
	}
	return nil
}

// snapshot はイベントの payload の subscription（§3.1）。
func snapshot(st domain.State, version int) map[string]any {
	m := map[string]any{
		"subscription_id": st.ID, "tenant_id": st.TenantID, "customer_account_id": st.CustomerAccountID,
		"product_id": st.ProductID, "plan_id": st.PlanID, "price_version_id": st.PriceVersionID, "interval": st.Interval, "quantity": st.Quantity,
		"status":            st.Status,
		"current_period":    map[string]any{"start": st.CurrentPeriod.Start, "end": st.CurrentPeriod.End},
		"next_renewal_date": st.NextRenewalDate, "trial_end": st.TrialEnd,
		"cancel_at_period_end": st.Cancellation != nil, "cancellation": nil, "pending_plan_id": nil, "pending_plan_change": nil,
		"renewal_in_progress": st.PendingRenewal != nil, "entitled": st.Status.Entitled(),
		"cancel_reason": st.CancelReason, "canceled_at": st.CanceledAt, "version": version,
	}
	if cn := st.Cancellation; cn != nil {
		m["cancellation"] = map[string]any{"source": cn.Source, "reason": cn.Reason, "effective_date": cn.EffectiveDate}
	}
	if pc := st.PendingPlanChange; pc != nil {
		m["pending_plan_id"] = pc.PlanID
		m["pending_plan_change"] = map[string]any{"plan_id": pc.PlanID, "effective_date": pc.EffectiveDate}
	}
	return m
}
