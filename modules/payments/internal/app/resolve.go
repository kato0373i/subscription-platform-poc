package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/postgres"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/domain"
)

const (
	resolveBatch     = 200
	notFoundGrace    = 10 * time.Minute // not_found を「届かなかった」と確定するまでの猶予（§8.1）
	staleUnknown     = 24 * time.Hour   // これを超えた unknown はアラートを出して手動確定に回す（P-PAY-2）
	processingStuck  = 30 * time.Second
	pendingRecheckAt = time.Hour
)

// ResolveUnknownAttempts は結果不明・取り残し・確定待ちの試行を代行に照会して確定する（§8.1）。
func (s *Service) ResolveUnknownAttempts(ctx context.Context, olderThan time.Duration) (c.ResolveSummary, error) {
	if olderThan <= 0 {
		olderThan = 30 * time.Second
	}
	ctx = system(ctx, "resolve_unknown")
	now := s.d.Clock.Now()
	var ids []sk.PaymentAttemptID
	if err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		ids, err = postgres.New(tx).ListUnresolved(ctx, now.Add(-olderThan), now.Add(-processingStuck), now.Add(-pendingRecheckAt), resolveBatch)
		return err
	}); err != nil {
		return c.ResolveSummary{}, err
	}
	var sum c.ResolveSummary
	for _, id := range ids {
		if err := s.resolveOne(ctx, id, &sum); err != nil {
			observability.Logger("payments.resolve").LogAttrs(ctx, slog.LevelWarn, "payments.resolve_failed", slog.String("attempt_id", id.String()), observability.Err(err))
		}
	}
	return sum, nil
}

// resolveOne は 1 件を別のトランザクションで照会・確定する（SKIP LOCKED で、重なった実行が同じ試行を二重に照会しない）。
func (s *Service) resolveOne(ctx context.Context, id sk.PaymentAttemptID, sum *c.ResolveSummary) error {
	return s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		a, err := repo.GetAttempt(ctx, id, postgres.SkipLocked)
		if errors.Is(err, postgres.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		st := a.State()
		if st.Status.Final() || st.ProviderOrderRef == nil {
			return nil
		}
		p, err := s.providerFor(st)
		if err != nil {
			return err
		}
		out, err := p.GetStatus(ctx, provider.StatusInput{ProviderOrderRef: *st.ProviderOrderRef, TenantShopID: st.TenantShopID})
		if errors.Is(err, provider.ErrUnavailable) {
			return nil // ブレーカーが開いている。次の回に回す
		}
		if err != nil {
			out = provider.Outcome{Status: provider.OutcomeUnknown}
		}
		sum.Checked++
		now := s.d.Clock.Now()
		from := st.Status
		a.RecordCheck(now)
		switch out.Status {
		case provider.OutcomeNotFound:
			if now.Sub(st.RequestedAt) > notFoundGrace {
				out = provider.Outcome{Status: provider.OutcomeFailed, FailureCode: domain.FailNotProcessed, Retryable: true}
			}
		case provider.OutcomeUnknown:
			if now.Sub(st.RequestedAt) > staleUnknown {
				observability.Logger("payments.resolve").LogAttrs(ctx, slog.LevelError, "payments.unknown_stale",
					slog.String("attempt_id", st.ID.String()), slog.Duration("age", now.Sub(st.RequestedAt)))
			}
		case provider.OutcomeSucceeded, provider.OutcomeFailed, provider.OutcomePendingAsync:
		}
		if _, _, err := applyOutcome(a, out, domain.ViaReconcile, s.d.Clock); err != nil {
			return err
		}
		switch a.State().Status {
		case c.AttemptSucceeded:
			sum.Succeeded++
		case c.AttemptFailed:
			sum.Failed++
		case c.AttemptPendingAsync:
			sum.Pending++
		case c.AttemptUnknown, c.AttemptProcessing:
			sum.StillUnknown++
		}
		return s.save(ctx, tx, repo, a, from)
	})
}

// ResolveUnknownJobName は結果不明の照合ジョブ（1 分ごと。Platform の規約に合わせた名前。P-IMP-15）。
const ResolveUnknownJobName = "payments-resolve-unknown"

type resolveJob struct{ s *Service }

func (s *Service) ResolveUnknownJob() scheduler.Job { return resolveJob{s: s} }
func (resolveJob) Name() string                     { return ResolveUnknownJobName }
func (j resolveJob) Run(ctx context.Context, _ scheduler.Run) (scheduler.Stats, error) {
	ctx = observability.WithActor(ctx, sk.Actor{Type: "system", ID: ResolveUnknownJobName})
	sum, err := j.s.ResolveUnknownAttempts(ctx, 30*time.Second)
	return scheduler.Stats{"checked": sum.Checked, "succeeded": sum.Succeeded, "failed": sum.Failed, "still_unknown": sum.StillUnknown, "pending": sum.Pending}, err
}

// ---- ops ----

// GetAttempt は ops の試行の照会（全項目）。
func (s *Service) GetAttempt(ctx context.Context, id sk.PaymentAttemptID) (domain.AttemptState, error) {
	var st domain.AttemptState
	err := s.d.DB.ReadTx(system(ctx, "ops_attempt"), func(ctx context.Context, tx db.Tx) error {
		a, err := postgres.New(tx).GetAttempt(ctx, id, postgres.NoLock)
		if err != nil {
			return mapNotFound(err, c.ErrAttemptNotFound)
		}
		st = a.State()
		return nil
	})
	return st, err
}

// ResolveInput は手動確定の入力（§7.6）。
type ResolveInput struct {
	Succeeded   bool
	FailureCode domain.FailureCode
	Retryable   bool
	Note        string
	Force       bool
	Operator    string
}

// ResolveManually は運営者の手動確定。確定の前に代行に 1 回照会し、指定と食い違えば ErrOutcomeMismatch（force で上書き）。
func (s *Service) ResolveManually(ctx context.Context, id sk.PaymentAttemptID, in ResolveInput) (domain.AttemptState, error) {
	var st domain.AttemptState
	var before c.AttemptStatus
	changed := false
	err := s.d.DB.InTx(system(ctx, "ops_resolve"), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		a, err := repo.GetAttempt(ctx, id, postgres.ForUpdate)
		if err != nil {
			return mapNotFound(err, c.ErrAttemptNotFound)
		}
		before = a.State().Status
		if !a.State().Status.Final() && a.ManualResolvable(s.d.Clock.Now()) && a.State().ProviderOrderRef != nil && !in.Force {
			if p, err := s.providerFor(a.State()); err == nil {
				out, err := p.GetStatus(ctx, provider.StatusInput{ProviderOrderRef: *a.State().ProviderOrderRef, TenantShopID: a.State().TenantShopID})
				if err == nil && (out.Status == provider.OutcomeSucceeded || out.Status == provider.OutcomeFailed) &&
					(out.Status == provider.OutcomeSucceeded) != in.Succeeded {
					return c.ErrOutcomeMismatch
				}
			}
		}
		err = a.ResolveManually(in.Succeeded, in.FailureCode, in.Retryable, in.Operator, in.Note, s.d.Clock.Now())
		if errors.Is(err, domain.ErrSameOutcome) {
			st = a.State()
			return nil
		}
		if err != nil {
			return err
		}
		changed = true
		st = a.State()
		return s.save(ctx, tx, repo, a, before)
	})
	if errors.Is(err, postgres.ErrSucceededExists) || isSucceededConflict(err) {
		return domain.AttemptState{}, s.blockingError(system(ctx, "ops_resolve"), st.Payable)
	}
	if err != nil {
		return domain.AttemptState{}, err
	}
	if changed {
		st.Version++
		auditctx.Annotate(ctx, "payment_attempt.resolve", auditctx.Target{Type: "payment_attempt", ID: id.String()},
			map[string]any{"status": string(before)}, map[string]any{"status": string(st.Status), "note": in.Note},
			auditctx.WithTenant(st.TenantID))
	}
	return st, nil
}

// isSucceededConflict は成功済みの別試行がある請求を succeeded にしようとして、部分ユニークに当たったか。
func isSucceededConflict(err error) bool { return err != nil && db.IsUniqueViolation(err) }
