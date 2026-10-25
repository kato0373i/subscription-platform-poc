// Package app は reporting のアプリ層：イベントの購読で集計表を更新し（設計 §6）、ダッシュボードの値を返す（§5）。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/inbox"
	"subscription-platform/internal/platform/messaging"
	sk "subscription-platform/internal/sharedkernel"
	billing "subscription-platform/modules/billing/contracts"
	catalog "subscription-platform/modules/catalog/contracts"
	subscription "subscription-platform/modules/subscription/contracts"
	"subscription-platform/modules/reporting/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/reporting/internal/domain"
)

// Deps はアプリ層の依存。
type Deps struct {
	DB      *db.ModuleDB
	Clock   sk.Clock
	Catalog catalog.Service
}

// Service は reporting のアプリ層。
type Service struct{ d Deps }

// New は Service を作る。
func New(d Deps) *Service { return &Service{d: d} }

// ProjectionEventTypes は購読するイベント（deploy/messaging.yaml の reporting-projection と一致させる）。
var ProjectionEventTypes = []string{
	subscription.EventStarted, subscription.EventRenewed, subscription.EventRecovered, subscription.EventPlanChanged,
	subscription.EventPastDue, subscription.EventCanceled, billing.EventInvoicePaid,
}

// 検証のエラー。
var (
	ErrInvalidMonth   = errors.New("reporting: month out of range")
	ErrInvalidAmount  = errors.New("reporting: goal amount out of range")
	ErrVersionConflict = errors.New("reporting: version conflict")
)

type snapshotJSON struct {
	SubscriptionID sk.SubscriptionID `json:"subscription_id"`
	Status         string            `json:"status"`
	PriceVersionID string            `json:"price_version_id"`
	Interval       string            `json:"interval"`
	Quantity       int               `json:"quantity"`
	TrialEnd       *sk.Date          `json:"trial_end"`
	CancelReason   *string           `json:"cancel_reason"`
	Version        int               `json:"version"`
}

func parseSnapshot(env sk.EventEnvelope) (*snapshotJSON, error) {
	var p struct {
		Subscription *snapshotJSON `json:"subscription"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil, fmt.Errorf("reporting: payload: %w", err)
	}
	if p.Subscription == nil {
		return nil, fmt.Errorf("reporting: %s has no subscription snapshot", env.EventType)
	}
	return p.Subscription, nil
}

type amountKey struct{}

// ProjectionHandler は reporting-projection の購読（設計 §6）。価格の取得はトランザクションの外で先に行う。
func (s *Service) ProjectionHandler(consumer string) messaging.Handler {
	inner := inbox.Wrap(s.d.DB, consumer, s.project)
	return func(ctx context.Context, env sk.EventEnvelope) error {
		if env.EventType != billing.EventInvoicePaid && env.TenantID != nil {
			snap, err := parseSnapshot(env)
			if err != nil {
				return messaging.Permanent(err)
			}
			amount, err := s.priceAmount(ctx, *env.TenantID, snap.PriceVersionID)
			if err != nil {
				return err
			}
			ctx = context.WithValue(ctx, amountKey{}, amount)
		}
		return inner(ctx, env)
	}
}

// priceAmount は価格バージョンの金額（写しになければ catalog から取得して写す。ADR-R03）。
func (s *Service) priceAmount(ctx context.Context, tenant sk.TenantID, pv string) (int64, error) {
	id, err := uuid.Parse(pv)
	if err != nil {
		return 0, messaging.Permanent(fmt.Errorf("reporting: price_version_id: %w", err))
	}
	var amount int64
	found := false
	if err := s.d.DB.ReadTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
		a, err := sqlcgen.New(tx).GetPriceAmount(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		amount, found = a, err == nil
		return err
	}); err != nil {
		return 0, err
	}
	if found {
		return amount, nil
	}
	q, err := s.d.Catalog.QuotePrice(db.ClearScope(ctx), tenant, sk.IDFromUUID[catalog.PriceVersionTag](id), nil)
	if errors.Is(err, catalog.ErrPriceVersionNotFound) {
		return 0, messaging.Permanent(err)
	}
	if err != nil {
		return 0, fmt.Errorf("reporting: quote price: %w", err) // ack せずに再配信
	}
	amount = q.Amount.Int64()
	err = s.d.DB.InTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
		return sqlcgen.New(tx).InsertPriceAmount(ctx, sqlcgen.InsertPriceAmountParams{PriceVersionID: id, TenantID: tenant.UUID(), Amount: amount, CreatedAt: s.d.Clock.Now()})
	})
	return amount, err
}

// project は 1 つのイベントを集計表に反映する（Inbox のトランザクションの中）。
func (s *Service) project(ctx context.Context, tx db.Tx, env sk.EventEnvelope) error {
	q := sqlcgen.New(tx)
	tenant := env.TenantID.UUID()
	day := sk.DateOf(env.OccurredAt)
	if env.EventType == billing.EventInvoicePaid {
		var p struct {
			Amount int64  `json:"amount"`
			Kind   string `json:"kind"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return messaging.Permanent(err)
		}
		return s.addDaily(ctx, q, tenant, day, domain.Revenue(p.Amount, p.Kind))
	}
	snap, err := parseSnapshot(env)
	if err != nil {
		return messaging.Permanent(err)
	}
	amount, _ := ctx.Value(amountKey{}).(int64)
	sid := snap.SubscriptionID.UUID()
	var old domain.Fact
	row, err := q.GetFactForUpdate(ctx, sid)
	switch {
	case err == nil:
		old = factOf(row)
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	if old.Exists() && snap.Version <= old.LastVersion {
		return nil // 順序の入れ替わり：古い版は無視する（要件 NFR-003）
	}
	reason := ""
	if snap.CancelReason != nil {
		reason = *snap.CancelReason
	}
	next := old.Next(domain.Snapshot{
		Status: snap.Status, PriceVersionID: snap.PriceVersionID, Interval: snap.Interval, Quantity: snap.Quantity, TrialEnd: snap.TrialEnd, CancelReason: reason, Version: snap.Version,
	}, amount)
	pv, err := uuid.Parse(next.PriceVersionID)
	if err != nil {
		return messaging.Permanent(err)
	}
	if err := q.UpsertFact(ctx, sqlcgen.UpsertFactParams{
		SubscriptionID: sid, TenantID: tenant, Status: next.Status, Mrr: next.MRR, PriceVersionID: pv, BillingInterval: next.Interval,
		Quantity: int32(next.Quantity), StartedInTrial: next.StartedInTrial, HasBeenPaying: next.HasBeenPaying, TrialEnd: next.TrialEnd,
		LastVersion: int32(next.LastVersion), UpdatedAt: s.d.Clock.Now(),
	}); err != nil {
		return err
	}
	return s.addDaily(ctx, q, tenant, day, domain.Classify(old, next, env.EventType, reason))
}

func factOf(r sqlcgen.ReportingSubscriptionFact) domain.Fact {
	return domain.Fact{
		Status: r.Status, MRR: r.Mrr, PriceVersionID: r.PriceVersionID.String(), Interval: r.BillingInterval, Quantity: int(r.Quantity),
		StartedInTrial: r.StartedInTrial, HasBeenPaying: r.HasBeenPaying, TrialEnd: r.TrialEnd, LastVersion: int(r.LastVersion),
	}
}

func (s *Service) addDaily(ctx context.Context, q *sqlcgen.Queries, tenant uuid.UUID, day sk.Date, d domain.Delta) error {
	if d.IsZero() {
		return nil
	}
	return q.AddDailyMetrics(ctx, sqlcgen.AddDailyMetricsParams{
		TenantID: tenant, Day: day, MrrDelta: d.MRRDelta, PayingDelta: int32(d.PayingDelta), TrialingDelta: int32(d.TrialingDelta), PastDueDelta: int32(d.PastDueDelta),
		NewMrr: d.NewMRR, ExpansionMrr: d.ExpansionMRR, ContractionMrr: d.ContractionMRR, ChurnedMrr: d.ChurnedMRR,
		NewSubscriptions: int32(d.NewSubscriptions), CanceledTotal: int32(d.CanceledTotal), CanceledPaying: int32(d.CanceledPaying),
		CanceledCustomer: int32(d.CanceledCustomer), CanceledDunning: int32(d.CanceledDunning), CanceledTenant: int32(d.CanceledTenant),
		TrialConverted: int32(d.TrialConverted), TrialLost: int32(d.TrialLost), Recovered: int32(d.Recovered), DunningLost: int32(d.DunningLost),
		RevenueInitial: d.RevenueInitial, RevenueRenewal: d.RevenueRenewal, UpdatedAt: s.d.Clock.Now(),
	})
}

func deltaOf(r sqlcgen.ReportingDailyMetric) domain.Delta {
	return domain.Delta{
		MRRDelta: r.MrrDelta, NewMRR: r.NewMrr, ExpansionMRR: r.ExpansionMrr, ContractionMRR: r.ContractionMrr, ChurnedMRR: r.ChurnedMrr,
		PayingDelta: int(r.PayingDelta), TrialingDelta: int(r.TrialingDelta), PastDueDelta: int(r.PastDueDelta),
		NewSubscriptions: int(r.NewSubscriptions), CanceledTotal: int(r.CanceledTotal), CanceledPaying: int(r.CanceledPaying),
		CanceledCustomer: int(r.CanceledCustomer), CanceledDunning: int(r.CanceledDunning), CanceledTenant: int(r.CanceledTenant),
		TrialConverted: int(r.TrialConverted), TrialLost: int(r.TrialLost), Recovered: int(r.Recovered), DunningLost: int(r.DunningLost),
		RevenueInitial: r.RevenueInitial, RevenueRenewal: r.RevenueRenewal,
	}
}

// Goal は月の MRR の目標。
type Goal struct {
	Amount  int64
	Version int
}

// OverviewResult はダッシュボードの値（設計 §5.1）。
type OverviewResult struct {
	domain.Overview
	AsOf           *time.Time
	Goal           *Goal
	PastDue        int64
	TrialsEnding7d int64
}

// MaxPastMonths は選べる過去の月の数（要件 FR-005）。
const MaxPastMonths = 24

// Today は業務時刻の今日（JST）。
func (s *Service) Today() sk.Date { return sk.DateOf(s.d.Clock.Now()) }

// Overview は month（nil なら当月）のダッシュボードの値。
func (s *Service) Overview(ctx context.Context, tenant sk.TenantID, month *domain.Month) (OverviewResult, error) {
	today := s.Today()
	cur := domain.MonthOf(today)
	m := cur
	if month != nil {
		m = *month
	}
	if cur.Before(m) || m.Before(cur.Add(-MaxPastMonths)) {
		return OverviewResult{}, ErrInvalidMonth
	}
	var out OverviewResult
	err := s.d.DB.ReadTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
		q := sqlcgen.New(tx)
		rows, err := q.ListDailyMetrics(ctx, today)
		if err != nil {
			return err
		}
		days := make([]domain.Day, len(rows))
		for i, r := range rows {
			days[i] = domain.Day{Date: r.Day, Delta: deltaOf(r)}
		}
		out.Overview = domain.Compute(days, m, today)
		last, err := q.LastUpdated(ctx)
		if err != nil {
			return err
		}
		if last.After(time.Unix(0, 0)) {
			out.AsOf = &last
		}
		if out.PastDue, err = q.CountPastDue(ctx); err != nil {
			return err
		}
		limit := today.AddDays(7)
		if out.TrialsEnding7d, err = q.CountTrialsEnding(ctx, &limit); err != nil {
			return err
		}
		g, err := q.GetGoal(ctx, m.First)
		switch {
		case err == nil:
			out.Goal = &Goal{Amount: g.Amount, Version: int(g.Version)}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		return nil
	})
	return out, err
}

// MaxGoalAmount は目標の上限（要件 FR-002）。
const MaxGoalAmount = 1_000_000_000

// goalMonthOK は目標を設定できる月か（過去 24 か月から 12 か月先まで）。
func (s *Service) goalMonthOK(m domain.Month) bool {
	cur := domain.MonthOf(s.Today())
	return !m.Before(cur.Add(-MaxPastMonths)) && !cur.Add(12).Before(m)
}

// PutGoal は月の目標を設定する。expected は If-Match（未設定の月は 0）。
func (s *Service) PutGoal(ctx context.Context, tenant sk.TenantID, m domain.Month, amount int64, actor string, expected *int) (Goal, error) {
	if !s.goalMonthOK(m) {
		return Goal{}, ErrInvalidMonth
	}
	if amount < 1 || amount > MaxGoalAmount {
		return Goal{}, ErrInvalidAmount
	}
	var out Goal
	var before map[string]any
	err := s.d.DB.InTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
		q := sqlcgen.New(tx)
		now := s.d.Clock.Now()
		cur, err := q.GetGoal(ctx, m.First)
		if err == nil {
			before = map[string]any{"amount": cur.Amount}
		}
		if errors.Is(err, pgx.ErrNoRows) {
			if expected != nil && *expected != 0 {
				return ErrVersionConflict
			}
			out = Goal{Amount: amount, Version: 1}
			return q.InsertGoal(ctx, sqlcgen.InsertGoalParams{TenantID: tenant.UUID(), Month: m.First, Amount: amount, UpdatedAt: now, UpdatedBy: actor})
		}
		if err != nil {
			return err
		}
		var exp *int32
		if expected != nil {
			e := int32(*expected)
			exp = &e
		}
		n, err := q.UpdateGoal(ctx, sqlcgen.UpdateGoalParams{Month: m.First, Amount: amount, UpdatedAt: now, UpdatedBy: actor, Expected: exp})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrVersionConflict
		}
		out = Goal{Amount: amount, Version: int(cur.Version) + 1}
		return nil
	})
	if err == nil {
		auditctx.Annotate(ctx, "mrr_goal.update", auditctx.Target{Type: "mrr_goal", ID: m.String()}, before, map[string]any{"amount": amount}, auditctx.WithTenant(tenant))
	}
	return out, err
}

// DeleteGoal は月の目標を消す（なくても成功）。
func (s *Service) DeleteGoal(ctx context.Context, tenant sk.TenantID, m domain.Month) error {
	if !s.goalMonthOK(m) {
		return ErrInvalidMonth
	}
	if err := s.d.DB.InTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
		return sqlcgen.New(tx).DeleteGoal(ctx, m.First)
	}); err != nil {
		return err
	}
	auditctx.Annotate(ctx, "mrr_goal.delete", auditctx.Target{Type: "mrr_goal", ID: m.String()}, nil, nil, auditctx.WithTenant(tenant))
	return nil
}
