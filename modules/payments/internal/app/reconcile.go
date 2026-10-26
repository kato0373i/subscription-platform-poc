package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/postgres"
	"subscription-platform/modules/payments/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/domain"
)

// ReconcileDailyJobName は日次照合（毎日 03:00 JST、前日分。§8.3）。
const ReconcileDailyJobName = "payments-reconcile-daily"

type reconcileJob struct{ s *Service }

func (s *Service) ReconcileDailyJob() scheduler.Job { return reconcileJob{s: s} }
func (reconcileJob) Name() string                   { return ReconcileDailyJobName }

// reconcileCursor は platform.job_cursors に保存する位置（P-X-7）。
type reconcileCursor struct {
	ProviderCursor string `json:"provider_cursor"`
	Pages          int    `json:"pages"`
}

// Run は as_of の前日（JST）の決済を、SettlementLister を実装した代行ごとに突き合わせる。
// 1 ページ分の不整合の保存とカーソルの保存は同じトランザクション。完了済みの代行は再実行しても何もしない。
func (j reconcileJob) Run(ctx context.Context, run scheduler.Run) (scheduler.Stats, error) {
	s := j.s
	ctx = system(ctx, ReconcileDailyJobName)
	asOf := run.AsOf
	if asOf.IsZero() {
		asOf = s.d.Clock.Today()
	}
	day := asOf.AddDays(-1)
	from, to := day.StartJST(), day.AddDays(1).StartJST()
	var found, pages int64
	for _, p := range s.d.Registry.All() {
		lister, ok := provider.Capability[provider.SettlementLister](p)
		if !ok {
			continue
		}
		name := string(p.Key().Provider)
		scope := day.String() + ":" + name
		state, err := run.Cursors.Load(ctx, scope)
		if err != nil {
			return nil, err
		}
		if state.Completed {
			continue
		}
		var cur reconcileCursor
		if len(state.Cursor) > 0 {
			_ = json.Unmarshal(state.Cursor, &cur)
		}
		// 最後のページまで保存済みで完了の前に落ちた場合は、代行の一覧を読み直さない
		for cur.Pages == 0 || cur.ProviderCursor != "" {
			items, next, err := lister.ListSettlements(ctx, from, to, cur.ProviderCursor, s.d.ReconcilePageSize)
			if err != nil {
				return nil, err
			}
			n, err := s.reconcilePage(ctx, run, scope, day, name, items, reconcileCursor{ProviderCursor: next, Pages: cur.Pages + 1}, next == "")
			if err != nil {
				return nil, err
			}
			found += n
			pages++
			if next == "" {
				break
			}
			cur = reconcileCursor{ProviderCursor: next, Pages: cur.Pages + 1}
		}
		n, err := s.reconcileLocalSucceeded(ctx, run, scope, day, name, p, from, to)
		if err != nil {
			return nil, err
		}
		found += n
	}
	return scheduler.Stats{"discrepancies": found, "pages": pages, "business_date": day.String()}, nil
}

// reconcilePage は代行の 1 ページを自社の試行と突き合わせる。
func (s *Service) reconcilePage(ctx context.Context, run scheduler.Run, scope string, day sk.Date, providerName string, items []provider.Settlement, next reconcileCursor, last bool) (int64, error) {
	var found int64
	err := s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		refs := make([]string, len(items))
		for i, it := range items {
			refs[i] = it.ProviderOrderRef
		}
		local, err := repo.AttemptsByOrderRefs(ctx, refs)
		if err != nil {
			return err
		}
		for _, it := range items {
			a := local[it.ProviderOrderRef]
			var d *postgres.Discrepancy
			switch {
			case a == nil:
				d = &postgres.Discrepancy{Kind: "orphan_provider_charge", Detail: map[string]any{"provider_status": it.Status, "amount": it.Amount.Int64()}}
			case it.Status == provider.OutcomeSucceeded && a.State().Status != c.AttemptSucceeded:
				d = &postgres.Discrepancy{Kind: "provider_succeeded_local_not", Detail: map[string]any{"local_status": a.State().Status}}
			case it.Amount.Int64() != a.State().Amount.Int64():
				d = &postgres.Discrepancy{Kind: "amount_mismatch", Detail: map[string]any{"local": a.State().Amount.Int64(), "provider": it.Amount.Int64()}}
			}
			if d == nil {
				continue
			}
			d.BusinessDate, d.Provider, d.ProviderOrderRef = day, providerName, it.ProviderOrderRef
			if a != nil {
				st := a.State()
				d.AttemptID, d.TenantID = &st.ID, &st.TenantID
			}
			created, err := repo.InsertDiscrepancy(ctx, *d, s.d.Clock.Now())
			if err != nil {
				return err
			}
			if created {
				found++
				observability.Logger("payments.reconcile").LogAttrs(ctx, slog.LevelError, "payments.reconciliation_discrepancy",
					slog.String("kind", d.Kind), slog.String("provider_order_ref", d.ProviderOrderRef))
			}
		}
		b, _ := json.Marshal(next)
		return run.Cursors.Save(ctx, tx, scope, b)
	})
	return found, err
}

// reconcileLocalSucceeded は自社は succeeded なのに代行が成功していない試行を探し、同じトランザクションで完了にする。
func (s *Service) reconcileLocalSucceeded(ctx context.Context, run scheduler.Run, scope string, day sk.Date, providerName string, p provider.PaymentProvider, from, to time.Time) (int64, error) {
	var found int64
	err := s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		attempts, err := repo.AttemptsInRange(ctx, providerName, from, to)
		if err != nil {
			return err
		}
		for _, a := range attempts {
			st := a.State()
			if st.Status != c.AttemptSucceeded || st.ProviderOrderRef == nil {
				continue
			}
			out, err := p.GetStatus(ctx, provider.StatusInput{ProviderOrderRef: *st.ProviderOrderRef, TenantShopID: st.TenantShopID})
			if err != nil || out.Status == provider.OutcomeSucceeded || out.Status == provider.OutcomeUnknown {
				continue
			}
			created, err := repo.InsertDiscrepancy(ctx, postgres.Discrepancy{
				BusinessDate: day, Provider: providerName, Kind: "local_succeeded_provider_not", AttemptID: &st.ID, TenantID: &st.TenantID,
				ProviderOrderRef: *st.ProviderOrderRef, Detail: map[string]any{"provider_status": out.Status},
			}, s.d.Clock.Now())
			if err != nil {
				return err
			}
			if created {
				found++
			}
		}
		return run.Cursors.Complete(ctx, tx, scope)
	})
	if err != nil {
		return 0, fmt.Errorf("payments: reconcile local: %w", err)
	}
	return found, nil
}

// Reports は ops の照合結果の一覧。
func (s *Service) Reports(ctx context.Context, date *sk.Date, status *string, after *uuid.UUID, limit int) ([]sqlcgen.PaymentsReconciliationDiscrepancy, bool, error) {
	var rows []sqlcgen.PaymentsReconciliationDiscrepancy
	err := s.d.DB.ReadTx(system(ctx, "ops_reports"), func(ctx context.Context, tx db.Tx) error {
		var err error
		rows, err = postgres.New(tx).ListDiscrepancies(ctx, date, status, after, limit+1)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if len(rows) > limit {
		return rows[:limit], true, nil
	}
	return rows, false, nil
}

// ErrDiscrepancyNotFound は照合の不整合がない。
var ErrDiscrepancyNotFound = errors.New("payments: discrepancy not found")

// ErrInvalidNote は対応のメモが空か長すぎる。
var ErrInvalidNote = errors.New("payments: note must be 1-1000 characters")

// AcknowledgeDiscrepancy は照合の不整合を対応済みにする（operator-console FR-007）。既に対応済みなら何もしない。
// メモと運営者は監査ログに残す（ADR-O02）。
func (s *Service) AcknowledgeDiscrepancy(ctx context.Context, id uuid.UUID, note string) (sqlcgen.PaymentsReconciliationDiscrepancy, error) {
	note = strings.TrimSpace(note)
	if n := utf8.RuneCountInString(note); n == 0 || n > 1000 {
		return sqlcgen.PaymentsReconciliationDiscrepancy{}, ErrInvalidNote
	}
	var row sqlcgen.PaymentsReconciliationDiscrepancy
	var changed bool
	err := s.d.DB.InTx(system(ctx, "ops_acknowledge"), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		var err error
		if changed, err = repo.AcknowledgeDiscrepancy(ctx, id); err != nil {
			return mapNotFound(err, ErrDiscrepancyNotFound)
		}
		row, err = sqlcgen.New(tx).GetDiscrepancy(ctx, id)
		return err
	})
	if err != nil {
		return row, err
	}
	if changed {
		var opts []auditctx.Option
		if row.TenantID != nil {
			opts = append(opts, auditctx.WithTenant(sk.IDFromUUID[sk.TenantTag](*row.TenantID)))
		}
		auditctx.Annotate(ctx, "reconciliation.acknowledge", auditctx.Target{Type: "reconciliation_discrepancy", ID: id.String()},
			map[string]any{"status": "open"}, map[string]any{"status": "acknowledged", "note": note}, opts...)
	}
	return row, nil
}

// UnresolvedStatuses は確定していない試行の状態。
var UnresolvedStatuses = []string{"unknown", "processing", "pending_async"}

// UnresolvedAttempts は確定していない試行（依頼から olderThan 以上たったもの）を古い順に読む（operator-console FR-006）。
func (s *Service) UnresolvedAttempts(ctx context.Context, statuses []string, olderThan time.Duration, after *postgres.AttemptCursor, limit int) ([]domain.AttemptState, bool, error) {
	if len(statuses) == 0 {
		statuses = UnresolvedStatuses
	}
	var out []domain.AttemptState
	err := s.d.DB.ReadTx(system(ctx, "ops_unresolved"), func(ctx context.Context, tx db.Tx) error {
		rows, err := postgres.New(tx).ListUnresolvedAttempts(ctx, statuses, s.d.Clock.Now().Add(-olderThan), after, limit+1)
		if err != nil {
			return err
		}
		for _, a := range rows {
			out = append(out, a.State())
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}
