package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	catalogcontracts "subscription-platform/modules/catalog/contracts"
	c "subscription-platform/modules/subscription/contracts"
	"subscription-platform/modules/subscription/internal/adapter/postgres"
	"subscription-platform/modules/subscription/internal/domain"
)

// me / console / tenant の API のユースケース。スコープは router が設定する（me は consumer_self、console・tenant は tenant_isolation）。
// RLS で見えない契約は ErrNotFound（他人・他テナントの契約も 404）。

// List は一覧（作成日時の新しい順）。
func (s *Service) List(ctx context.Context, f postgres.Filter, after *postgres.Cursor, limit int) ([]c.SubscriptionView, bool, error) {
	var out []c.SubscriptionView
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		xs, err := postgres.New(tx).List(ctx, f, after, limit+1)
		for _, x := range xs {
			out = append(out, x.View())
		}
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// Find は 1 件。
func (s *Service) Find(ctx context.Context, id sk.SubscriptionID) (c.SubscriptionView, error) {
	var v c.SubscriptionView
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		x, err := postgres.New(tx).Get(ctx, id)
		if err != nil {
			return mapNotFound(err)
		}
		v = x.View()
		return nil
	})
	return v, err
}

// SchedulePlanChange は契約者のプラン変更の予約（§7.2）。変更先は catalog の GetPlanSnapshot で検証する。
func (s *Service) SchedulePlanChange(ctx context.Context, id sk.SubscriptionID, planID sk.PlanID) (c.SubscriptionView, error) {
	cur, err := s.Find(ctx, id)
	if err != nil {
		return c.SubscriptionView{}, err
	}
	snap, err := s.d.Catalog.GetPlanSnapshot(ctx, cur.TenantID, planID)
	switch {
	case errors.Is(err, catalogcontracts.ErrPlanNotFound):
		return c.SubscriptionView{}, &c.PlanChangeInvalidError{Reason: "plan_unavailable"}
	case err != nil:
		return c.SubscriptionView{}, err
	}
	target := domain.PlanTarget{PlanID: snap.PlanID, ProductID: snap.ProductID, PriceVersionID: snap.PriceVersionID, Interval: snap.Interval}
	x, err := s.mutate(ctx, id, func(x *domain.Subscription) error {
		st := x.State()
		// 判定の順序：終端・状態 → 解約予約 → 別の商品 → 同じプラン → 非公開（予約中と同じプランは ○）
		if st.Status.Terminal() || st.Cancellation != nil || target.ProductID != st.ProductID || target.PlanID == st.PlanID {
			return x.SchedulePlanChange(target, s.d.Clock.Today())
		}
		if pc := st.PendingPlanChange; pc != nil && pc.PlanID == target.PlanID {
			return nil
		}
		if !snap.Published {
			if st.Status == c.StatusTrialing || st.Status == c.StatusActive {
				return &c.PlanChangeInvalidError{Reason: "plan_unavailable"}
			}
		}
		return x.SchedulePlanChange(target, s.d.Clock.Today())
	})
	return viewOf(x), err
}

func (s *Service) RevokePlanChange(ctx context.Context, id sk.SubscriptionID) (c.SubscriptionView, error) {
	x, err := s.mutate(ctx, id, func(x *domain.Subscription) error { return x.RevokePlanChange() })
	return viewOf(x), err
}

// ScheduleCancellation は契約者の解約予約。支払い遅延中は即時解約（D-03。請求の無効化は Orchestration。P-ORC-1）。
func (s *Service) ScheduleCancellation(ctx context.Context, id sk.SubscriptionID) (c.SubscriptionView, error) {
	x, err := s.mutate(ctx, id, func(x *domain.Subscription) error { return x.ScheduleCancellation(s.d.Clock.Now()) })
	return viewOf(x), err
}

func (s *Service) RevokeCancellation(ctx context.Context, id sk.SubscriptionID) (c.SubscriptionView, error) {
	x, err := s.mutate(ctx, id, func(x *domain.Subscription) error { return x.RevokeCancellation() })
	return viewOf(x), err
}

// Entitlement は 1 商品の利用権。
type Entitlement struct {
	ProductID       sk.ProductID
	Entitled        bool
	SubscriptionIDs []sk.SubscriptionID
}

// ErrCustomerNotFound は当テナントにその cus_ の契約が 1 件もない（他テナントの cus_ も同じ）。
var ErrCustomerNotFound = errors.New("subscription: tenant customer not found")

// Entitlements は事業者 API の利用権の照会（§7.2）。entitled = 利用権のある契約が 1 件以上。
func (s *Service) Entitlements(ctx context.Context, cus string, product *uuid.UUID) (bool, []Entitlement, error) {
	var rows []postgres.EntitlementRow
	var exists bool
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		var err error
		if exists, err = repo.TenantCustomerHasSubscriptions(ctx, cus); err != nil || !exists {
			return err
		}
		rows, err = repo.ListByTenantCustomer(ctx, cus, product)
		return err
	})
	if err != nil {
		return false, nil, err
	}
	if !exists {
		return false, nil, ErrCustomerNotFound
	}
	var out []Entitlement
	idx := map[sk.ProductID]int{}
	entitledAny := false
	for _, r := range rows {
		i, ok := idx[r.ProductID]
		if !ok {
			i = len(out)
			idx[r.ProductID] = i
			out = append(out, Entitlement{ProductID: r.ProductID, SubscriptionIDs: []sk.SubscriptionID{}})
		}
		if r.Status.Entitled() {
			out[i].Entitled = true
			out[i].SubscriptionIDs = append(out[i].SubscriptionIDs, r.SubscriptionID)
			entitledAny = true
		}
	}
	return entitledAny, out, nil
}
