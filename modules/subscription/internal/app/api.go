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

// PlanOption はプラン変更の候補（consumer-mypage §5.1）。
type PlanOption struct {
	PlanID        sk.PlanID
	Name          string
	Interval      sk.BillingInterval
	Amount        sk.Money
	EffectiveDate sk.Date
}

// PlanChangeOptions は同じ商品の公開中のプランのうち、現在のプラン以外（適用日つき）。
// 予約できない状態（支払い遅延・解約の予約中・終了）なら空。
func (s *Service) PlanChangeOptions(ctx context.Context, id sk.SubscriptionID) ([]PlanOption, error) {
	var x *domain.Subscription
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		x, err = postgres.New(tx).Get(ctx, id)
		return mapNotFound(err)
	})
	if err != nil {
		return nil, err
	}
	st := x.State()
	if (st.Status != c.StatusTrialing && st.Status != c.StatusActive) || st.Cancellation != nil || st.NextRenewalDate == nil {
		return []PlanOption{}, nil
	}
	plans, err := s.d.Catalog.ListPurchasablePlans(db.ClearScope(ctx), st.TenantID, st.ProductID)
	if err != nil {
		return nil, err
	}
	eff := x.PlanChangeDate(sk.DateOf(s.d.Clock.Now()))
	out := []PlanOption{}
	for _, p := range plans {
		if p.PlanID == st.PlanID {
			continue
		}
		out = append(out, PlanOption{PlanID: p.PlanID, Name: p.Name, Interval: p.Interval, Amount: p.Amount, EffectiveDate: eff})
	}
	return out, nil
}

// Display は契約の表示用の値（consumer-mypage ADR-M01）。
type Display struct {
	TenantName  string   `json:"tenant_name"`
	ProductName string   `json:"product_name"`
	PlanName    string   `json:"plan_name"`
	UnitAmount  int64  `json:"unit_amount"`
	Amount      int64  `json:"amount"`
}

// Displays は契約ごとの表示用の値。プラン・価格バージョンごとに 1 回だけ catalog を呼ぶ。取れなかった契約は含めない（画面は「—」）。
func (s *Service) Displays(ctx context.Context, views []c.SubscriptionView) map[sk.SubscriptionID]Display {
	type planKey struct {
		t sk.TenantID
		p sk.PlanID
	}
	descs := map[planKey]*catalogcontracts.PlanDescription{}
	prices := map[sk.PriceVersionID]*int64{}
	cctx := db.ClearScope(ctx)
	out := map[sk.SubscriptionID]Display{}
	for _, v := range views {
		k := planKey{v.TenantID, v.PlanID}
		d, ok := descs[k]
		if !ok {
			if got, err := s.d.Catalog.DescribePlan(cctx, v.TenantID, v.PlanID); err == nil {
				d = &got
			}
			descs[k] = d
		}
		pr, ok := prices[v.PriceVersionID]
		if !ok {
			if q, err := s.d.Catalog.QuotePrice(cctx, v.TenantID, v.PriceVersionID, nil); err == nil {
				a := q.Amount.Int64()
				pr = &a
			}
			prices[v.PriceVersionID] = pr
		}
		if d == nil || pr == nil {
			continue
		}
		out[v.ID] = Display{TenantName: d.TenantName, ProductName: d.ProductName, PlanName: d.PlanName, UnitAmount: *pr, Amount: *pr * int64(max(v.Quantity, 1))}
	}
	return out
}
