package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/orchestration/internal/adapter/postgres"
	"subscription-platform/modules/orchestration/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/orchestration/internal/domain"
	subscription "subscription-platform/modules/subscription/contracts"
)

// ---- 一括解約（§4.7） ----

func (s *Service) createBulkJob(ctx context.Context, t sk.TenantID, sourceEventID uuid.UUID) error {
	return s.tx(ctx, t, func(ctx context.Context, r *postgres.Repo) error {
		_, err := r.Q.InsertBulkJob(ctx, sqlcgen.InsertBulkJobParams{ID: uuid.Must(uuid.NewV7()), TenantID: t.UUID(), SourceEventID: sourceEventID, StartedAt: s.now()})
		return err // 同じ停止（source_event_id）・同じ事業者の running は DO NOTHING（AC-ORC-037）
	})
}

type bulkJob struct{ s *Service }

func (s *Service) BulkCancellationJob() scheduler.Job { return bulkJob{s: s} }

func (bulkJob) Name() string { return BulkCancellationJob }

// bulkLimit は 1 回の実行で処理する上限（PoC の目安。残りは次の毎分の実行で続ける）。
const bulkLimit = 1000

func (j bulkJob) Run(ctx context.Context, _ scheduler.Run) (scheduler.Stats, error) {
	s := j.s
	ctx = observability.WithActor(ctx, sk.Actor{Type: "system", ID: BulkCancellationJob})
	sys := system(ctx, BulkCancellationJob)
	var job sqlcgen.OrchestrationBulkCancellationJob
	err := s.d.DB.InTx(sys, func(ctx context.Context, tx db.Tx) error {
		q := sqlcgen.New(tx)
		now := s.now()
		var err error
		job, err = q.PickRunningBulkJob(ctx, &now)
		if err != nil {
			return postgres.NotFound(err)
		}
		lease := now.Add(5 * time.Minute)
		return q.LeaseBulkJob(ctx, sqlcgen.LeaseBulkJobParams{ID: job.ID, LeaseUntil: &lease})
	})
	if errors.Is(err, postgres.ErrNotFound) {
		return scheduler.Stats{"processed": int64(0)}, nil
	}
	if err != nil {
		return nil, err
	}
	t := sk.IDFromUUID[sk.TenantTag](job.TenantID)
	done := 0
	for done < bulkLimit && job.Status == "running" {
		cursor := ""
		if job.Cursor != nil {
			cursor = *job.Cursor
		}
		page, err := s.d.Subscription.ListOpenByTenant(ctx, t, cursor, s.d.BulkPageSize)
		if err != nil {
			return nil, err
		}
		for i, ref := range page.Items {
			if h := s.d.Hooks.AfterBulkSubscriber; h != nil {
				if err := h(ctx, i); err != nil {
					return nil, err
				}
			}
			s.bulkOne(ctx, &job, ref)
		}
		done += len(page.Items)
		switch {
		case page.NextCursor != "":
			job.Cursor = &page.NextCursor
		case job.SweepRound == 1:
			// 停止と並行して作られた契約を拾うため、もう 1 周する（2 周目は冪等な命令の再送）
			job.SweepRound, job.Cursor = 2, nil
		default:
			job.Status, job.Cursor = "completed", nil
			now := s.now()
			job.CompletedAt = &now
		}
		if err := s.saveBulkJob(sys, &job); err != nil {
			return nil, err
		}
	}
	return scheduler.Stats{"job_id": job.ID.String(), "status": job.Status, "processed": int64(done)}, nil
}

// bulkOne は 1 件を処理する（3 回まで再試行し、それでも失敗したら failures に記録して先に進む）。
// 件数は bulk_cancellation_items に初めて入った契約だけ数える（途中で落ちた後の再処理・2 周目で二重に数えない）。
func (s *Service) bulkOne(ctx context.Context, job *sqlcgen.OrchestrationBulkCancellationJob, ref subscription.SubscriptionRef) {
	act := domain.DecideBulk(domain.SubStatus(ref.Status))
	var err error
	var kind string
	for attempt := 0; attempt < 3; attempt++ {
		kind, err = s.bulkCommand(ctx, act, ref.SubscriptionID)
		if err == nil || invalidTransition(err) {
			break
		}
	}
	if invalidTransition(err) {
		kind, err = "skipped", nil
	}
	_ = s.tx(ctx, ref.TenantID, func(ctx context.Context, r *postgres.Repo) error {
		if err != nil {
			if ferr := r.Q.InsertBulkFailure(ctx, sqlcgen.InsertBulkFailureParams{JobID: job.ID, TenantID: ref.TenantID.UUID(), SubscriptionID: ref.SubscriptionID.UUID(), Error: err.Error(), OccurredAt: s.now()}); ferr != nil {
				return ferr
			}
			return r.Q.CountBulkFailure(ctx, job.ID)
		}
		n, ierr := r.Q.InsertBulkItem(ctx, sqlcgen.InsertBulkItemParams{JobID: job.ID, TenantID: ref.TenantID.UUID(), SubscriptionID: ref.SubscriptionID.UUID(), Result: kind, ProcessedAt: s.now()})
		if ierr != nil || n == 0 {
			return ierr
		}
		return r.Q.CountBulkResult(ctx, sqlcgen.CountBulkResultParams{Result: kind, ID: job.ID})
	})
	if err != nil {
		warn(ctx, "orchestration.bulk_cancellation_failed", err, slog.String("subscription_id", ref.SubscriptionID.String()))
	}
}

func (s *Service) bulkCommand(ctx context.Context, act domain.BulkAction, sub sk.SubscriptionID) (string, error) {
	switch act {
	case domain.BulkCancelNow:
		_, err := conflictRetry(ctx, func() (subscription.SubscriptionView, error) {
			return s.d.Subscription.Cancel(ctx, sub, subscription.CancelByTenantSuspended)
		})
		return "canceled", err // 請求の無効化は subscription.canceled のハンドラ（§4.6）
	case domain.BulkSchedule:
		res, err := conflictRetry(ctx, func() (subscription.SystemCancellationResult, error) {
			return s.d.Subscription.ScheduleSystemCancellation(ctx, sub, subscription.CancelByTenantSuspended)
		})
		switch res.Kind {
		case subscription.SystemCancellationScheduled:
			return "scheduled", err
		case subscription.SystemCanceledImmediately:
			return "canceled", err
		case subscription.SystemCancellationNoop:
			// 予約済み：途中で落ちる前にこのジョブが予約した場合も「期間終了で解約」に数える（items で二重計上は防ぐ）
			if cn := res.Subscription.Cancellation; cn != nil && cn.Reason == subscription.CancelByTenantSuspended {
				return "scheduled", err
			}
		}
		return "skipped", err
	case domain.BulkSkip:
	}
	return "skipped", nil
}

func (s *Service) saveBulkJob(ctx context.Context, j *sqlcgen.OrchestrationBulkCancellationJob) error {
	return s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		var lease *time.Time
		if j.Status == "running" {
			l := s.now().Add(5 * time.Minute)
			lease = &l
		}
		return sqlcgen.New(tx).SaveBulkJobProgress(ctx, sqlcgen.SaveBulkJobProgressParams{
			ID: j.ID, Status: j.Status, SweepRound: j.SweepRound, Cursor: j.Cursor, LeaseUntil: lease, CompletedAt: j.CompletedAt,
		})
	})
}

// ---- process-sweeper（§8 の再開ルール） ----

type sweeperJob struct{ s *Service }

func (s *Service) SweeperJob() scheduler.Job { return sweeperJob{s: s} }

func (sweeperJob) Name() string { return SweeperJobName }

// maxResume を超えたら failed にしてアラート（ops が対処する）。
const maxResume = 5

func (j sweeperJob) Run(ctx context.Context, _ scheduler.Run) (scheduler.Stats, error) {
	s := j.s
	ctx = observability.WithActor(ctx, sk.Actor{Type: "system", ID: SweeperJobName})
	sys := system(ctx, SweeperJobName)
	now := s.now()
	var stalled []*postgres.Process
	var sagas []saga
	err := s.d.DB.ReadTx(sys, func(ctx context.Context, tx db.Tx) error {
		r := postgres.New(tx)
		var err error
		if stalled, err = r.Stalled(ctx, now.Add(-s.d.StalledAfter), 500); err != nil {
			return err
		}
		sagas, err = r.Q.ListStaleSagas(ctx, sqlcgen.ListStaleSagasParams{UpdatedAt: now.Add(-s.d.SagaStaleAfter), Limit: 500})
		return err
	})
	if err != nil {
		return nil, err
	}
	var resumed, failed, sagaDone int64
	for _, p := range stalled {
		ok, err := s.sweepProcess(ctx, p)
		if err != nil {
			warn(ctx, "orchestration.sweep_failed", err, slog.String("process_id", p.ID.String()))
			continue
		}
		if ok {
			resumed++
		}
		if p.State == domain.StateFailed {
			failed++
		}
	}
	for i := range sagas {
		if err := s.sweepSaga(ctx, &sagas[i]); err != nil {
			warn(ctx, "orchestration.sweep_saga_failed", err, slog.String("idempotency_key", sagas[i].IdempotencyKey))
			continue
		}
		sagaDone++
	}
	return scheduler.Stats{"resumed": resumed, "failed": failed, "sagas": sagaDone}, nil
}

// sweepProcess は止まったプロセスを再開する。再開したら true。
func (s *Service) sweepProcess(ctx context.Context, p *postgres.Process) (bool, error) {
	if p.ResumeCount >= maxResume {
		p.State = domain.StateFailed
		alert(ctx, "orchestration.process_failed", slog.String("process_id", p.ID.String()), slog.String("kind", string(p.Kind)), slog.String("state", string(p.State)))
		return false, s.save(ctx, p)
	}
	switch {
	case p.Kind == domain.KindRenewal && (p.State == domain.StateStarted || p.State == domain.StateRenewalBegun):
		p.ResumeCount++
		if err := s.save(ctx, p); err != nil {
			return false, err
		}
		m, err := s.renewalMessage(ctx, RenewalMessage{SubscriptionID: p.SubscriptionID, TenantID: p.TenantID, RunDate: *p.RunDate, BillingRunID: p.BillingRunID},
			fmt.Sprintf("%s:%s:resume:%d", p.SubscriptionID, p.RunDate, p.ResumeCount))
		if err != nil {
			return false, err
		}
		return true, s.d.SendRenewal(ctx, []messaging.FIFOMessage{m})
	case p.Kind == domain.KindSignup && (p.State == domain.StateInvoiceIssued || p.State == domain.StateCollecting):
		p.ResumeCount++
		if err := s.save(ctx, p); err != nil {
			return false, err
		}
		sub, err := s.d.Subscription.Get(ctx, p.SubscriptionID)
		if err != nil {
			return false, err
		}
		if sub.Status != subscription.StatusPendingInitial {
			return true, s.complete(ctx, p, domain.OutcomeSkipped)
		}
		inv, err := s.d.Billing.GetInvoice(ctx, *p.InvoiceID)
		if err != nil {
			return false, err
		}
		info, err := s.d.Tenant.GetTenant(ctx, p.TenantID)
		if err != nil {
			return false, err
		}
		_, err = s.collectSignup(ctx, sub, inv, info, subscriptionRedemption(sub)) // 同じ回収キー invoice:{id}:1
		return true, err
	case p.State == domain.StateCompensating:
		s.compensate(ctx, p.SubscriptionID, p.TenantID, nil)
		sub, err := s.d.Subscription.Get(ctx, p.SubscriptionID)
		if err == nil {
			s.releaseCoupon(ctx, p.TenantID, subscriptionRedemption(sub))
		}
		return true, s.complete(ctx, p, domain.OutcomeSignupFailed)
	}
	// それ以外は Billing のリース切れで同じ collection_seq が再送されて再開する。24 時間を超えたらアラート
	if s.now().Sub(p.UpdatedAt) > 24*time.Hour {
		alert(ctx, "orchestration.process_stalled", slog.String("process_id", p.ID.String()), slog.String("kind", string(p.Kind)), slog.String("state", string(p.State)))
	}
	return false, nil
}

// sweepSaga は in_progress のまま 5 分を超えたサガを閉じる（§4.1「途中で落ちたとき」）。
func (s *Service) sweepSaga(ctx context.Context, g *saga) error {
	subID := sk.IDFromUUID[sk.SubscriptionTag](g.SubscriptionID)
	var redemption *sk.RedemptionID
	if g.RedemptionID != nil {
		r := sk.IDFromUUID[sk.RedemptionTag](*g.RedemptionID)
		redemption = &r
	}
	var t sk.TenantID
	if g.PlanTenantID != nil {
		t = sk.IDFromUUID[sk.TenantTag](*g.PlanTenantID)
	}
	sub, err := s.d.Subscription.Get(ctx, subID)
	if errors.Is(err, subscription.ErrNotFound) {
		if !t.IsZero() {
			s.releaseCoupon(ctx, t, redemption)
		}
		s.finishSaga(ctx, g, domain.SagaAborted, 0, "")
		return nil
	}
	if err != nil {
		return err
	}
	switch sub.Status {
	case subscription.StatusTrialing, subscription.StatusActive, subscription.StatusPastDue:
		s.finishSaga(ctx, g, domain.SagaCompleted, 201, "")
	case subscription.StatusSignupFailed, subscription.StatusCanceled:
		s.compensate(ctx, sub.ID, sub.TenantID, redemption)
		s.finishSaga(ctx, g, domain.SagaCompleted, 422, "payment_declined:generic")
	case subscription.StatusPendingInitial:
		inv, err := s.d.Billing.GetOpenInvoice(ctx, sub.ID)
		if err != nil {
			return err
		}
		if inv == nil {
			s.compensate(ctx, sub.ID, sub.TenantID, redemption)
			s.finishSaga(ctx, g, domain.SagaAborted, 0, "")
			return nil
		}
		info, err := s.d.Tenant.GetTenant(ctx, sub.TenantID)
		if err != nil {
			return err
		}
		out, err := s.collectSignup(ctx, sub, *inv, info, redemption)
		if err != nil {
			return err
		}
		switch out.kind {
		case signupPaid:
			s.finishSaga(ctx, g, domain.SagaCompleted, 201, "")
		case signupDeclined:
			s.finishSaga(ctx, g, domain.SagaCompleted, 422, "payment_declined:"+out.category)
		case signupProviderDown:
			s.finishSaga(ctx, g, domain.SagaAborted, 0, "")
		case signupPending:
			g.Status = string(domain.SagaPendingOutcome)
			return s.updateSaga(ctx, g)
		}
	}
	return nil
}

// ---- checkout-saga-gc ----

type sagaGCJob struct{ s *Service }

func (s *Service) SagaGCJob() scheduler.Job { return sagaGCJob{s: s} }

func (sagaGCJob) Name() string { return SagaGCJobName }

func (j sagaGCJob) Run(ctx context.Context, _ scheduler.Run) (scheduler.Stats, error) {
	var n int64
	err := j.s.d.DB.InTx(system(ctx, SagaGCJobName), func(ctx context.Context, tx db.Tx) error {
		var err error
		n, err = sqlcgen.New(tx).DeleteOldSagas(ctx, j.s.now().Add(-30*24*time.Hour))
		return err
	})
	return scheduler.Stats{"deleted": n}, err
}
