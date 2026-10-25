package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"subscription-platform/internal/platform/messaging"
	sk "subscription-platform/internal/sharedkernel"
	billing "subscription-platform/modules/billing/contracts"
	"subscription-platform/modules/orchestration/internal/adapter/postgres"
	"subscription-platform/modules/orchestration/internal/domain"
	payments "subscription-platform/modules/payments/contracts"
	subscription "subscription-platform/modules/subscription/contracts"
	tenant "subscription-platform/modules/tenant/contracts"
)

// 購読するイベント（§5）。
const (
	EventCollectionDue        = billing.EventInvoiceCollectionDue
	EventInvoicePaid          = billing.EventInvoicePaid
	EventInvoicePaymentFailed = billing.EventInvoicePaymentFailed
	EventInvoiceUncollectible = billing.EventInvoiceMarkedUncollectible
	EventPaymentMethodChanged = subscription.EventPaymentMethodChanged
	EventSubscriptionCanceled = subscription.EventCanceled
	EventTenantSuspended      = tenant.EventTenantSuspended
)

// HandleEvent は orc-events のイベントを振り分ける。
func (s *Service) HandleEvent(ctx context.Context, env sk.EventEnvelope) error {
	ctx = unscoped(ctx)
	switch env.EventType {
	case EventInvoicePaid, EventInvoicePaymentFailed, EventInvoiceUncollectible:
		return s.onInvoiceResult(ctx, env)
	case EventPaymentMethodChanged:
		return s.onPaymentMethodChanged(ctx, env)
	case EventSubscriptionCanceled:
		return s.onCanceled(ctx, env)
	case EventTenantSuspended:
		return s.onTenantSuspended(ctx, env)
	}
	return nil
}

type invoicePayload struct {
	InvoiceID      sk.InvoiceID      `json:"invoice_id"`
	SubscriptionID sk.SubscriptionID `json:"subscription_id"`
}

// onInvoiceResult は invoice_paid / payment_failed / marked_uncollectible（§4.3 の結果のイベント）。
func (s *Service) onInvoiceResult(ctx context.Context, env sk.EventEnvelope) error {
	pl, err := sk.DecodePayload[invoicePayload](env)
	if err != nil {
		return messaging.Permanent(err)
	}
	sub, err := s.d.Subscription.Get(ctx, pl.SubscriptionID)
	if err != nil {
		return err
	}
	d := domain.DecideOnInvoiceEvent(domain.InvoiceEvent(env.EventType), domain.SubStatus(sub.Status))
	attrs := []slog.Attr{slog.String("subscription_id", sub.ID.String()), slog.String("invoice_id", pl.InvoiceID.String()), slog.String("event", env.EventType)}
	var cmdErr error
	switch d.Command {
	case domain.CmdActivate:
		_, cmdErr = conflictRetry(ctx, func() (subscription.SubscriptionView, error) {
			return s.d.Subscription.Activate(ctx, sub.ID, pl.InvoiceID)
		})
		if cmdErr == nil {
			cmdErr = s.confirmCoupon(ctx, sub.TenantID, subscriptionRedemption(sub))
		}
		if cmdErr == nil {
			cmdErr = s.suspendIfTenantSuspended(ctx, sub)
		}
	case domain.CmdConfirmRenewal:
		_, cmdErr = conflictRetry(ctx, func() (subscription.SubscriptionView, error) {
			return s.d.Subscription.ConfirmRenewal(ctx, sub.ID, pl.InvoiceID)
		})
	case domain.CmdRecover:
		_, cmdErr = conflictRetry(ctx, func() (subscription.SubscriptionView, error) {
			return s.d.Subscription.Recover(ctx, sub.ID, pl.InvoiceID)
		})
	case domain.CmdMarkPastDue:
		_, cmdErr = conflictRetry(ctx, func() (subscription.SubscriptionView, error) {
			return s.d.Subscription.MarkPastDue(ctx, sub.ID, pl.InvoiceID)
		})
	case domain.CmdCancelDunning:
		_, cmdErr = conflictRetry(ctx, func() (subscription.SubscriptionView, error) {
			return s.d.Subscription.Cancel(ctx, sub.ID, subscription.CancelByDunning)
		})
	case domain.CmdCompensateSignup:
		p, perr := s.processForInvoice(ctx, sub.TenantID, sub.ID, pl.InvoiceID)
		if perr != nil {
			return perr
		}
		if p != nil {
			s.compensateProcess(ctx, p, sub.ID, sub.TenantID, subscriptionRedemption(sub))
			return nil
		}
		s.compensate(ctx, sub.ID, sub.TenantID, subscriptionRedemption(sub))
	case domain.CmdAlertPaidAfterCancel:
		// P-BIL-3・P-SUB-5：無効化の要求があった請求の成功。契約は解約済みのまま、返金は運用
		alert(ctx, "paid_after_cancel", attrs...)
	case domain.CmdAlertPaidAfterFailed:
		alert(ctx, "paid_after_signup_failed", attrs...)
	case domain.CmdNone:
	}
	if invalidTransition(cmdErr) {
		warn(ctx, "orchestration.command_skipped", cmdErr, attrs...)
		cmdErr = nil
	}
	if cmdErr != nil {
		return cmdErr
	}
	p, err := s.processForInvoice(ctx, sub.TenantID, sub.ID, pl.InvoiceID)
	if err != nil {
		return err
	}
	return s.complete(ctx, p, d.Outcome)
}

// suspendIfTenantSuspended は停止中の事業者の契約を期間終了で解約する（結果不明の申込が停止の後に確定した場合。C-09・AC-ORC-038）。
func (s *Service) suspendIfTenantSuspended(ctx context.Context, sub subscription.SubscriptionView) error {
	info, err := s.d.Tenant.GetTenant(ctx, sub.TenantID)
	if err != nil || info.Status != tenant.TenantSuspended {
		return err
	}
	_, err = conflictRetry(ctx, func() (subscription.SystemCancellationResult, error) {
		return s.d.Subscription.ScheduleSystemCancellation(ctx, sub.ID, subscription.CancelByTenantSuspended)
	})
	return err
}

type pmChangedPayload struct {
	SubscriptionID sk.SubscriptionID   `json:"subscription_id"`
	TenantID       sk.TenantID         `json:"tenant_id"`
	Status         subscription.Status `json:"status"`
}

// onPaymentMethodChanged は支払い遅延中の支払方法の変更 → 即時の再決済（§4.4）。
func (s *Service) onPaymentMethodChanged(ctx context.Context, env sk.EventEnvelope) error {
	pl, err := sk.DecodePayload[pmChangedPayload](env)
	if err != nil {
		return messaging.Permanent(err)
	}
	if pl.Status != subscription.StatusPastDue {
		return nil
	}
	inv, err := s.d.Billing.GetOpenInvoice(ctx, pl.SubscriptionID)
	if err != nil || inv == nil {
		return err
	}
	id := inv.ID
	p := &postgres.Process{TenantID: pl.TenantID, SubscriptionID: pl.SubscriptionID, InvoiceID: &id, Kind: domain.KindRecovery, State: domain.StateInvoiceIssued, CorrelationID: correlation(ctx)}
	if _, err := s.insertProcess(ctx, p); err != nil {
		if errors.Is(err, errActiveProcessOnSubscription) {
			return messaging.RetryAfter(err, retryDelay) // 進行中の回収が終わってから即時の再決済（C-03）
		}
		return err
	}
	_, err = s.d.Billing.RequestImmediateCollection(ctx, inv.ID, billing.CollectionRequest{Reason: billing.ReasonImmediate, RequestedBy: "customer"})
	switch {
	case errors.Is(err, billing.ErrCollectionInProgress):
		_ = s.complete(ctx, p, domain.OutcomeSkipped)
		return messaging.RetryAfter(err, retryDelay)
	case errors.Is(err, billing.ErrInvoiceNotCollectible), errors.Is(err, billing.ErrInvoiceNotFound):
		return s.complete(ctx, p, domain.OutcomeSkipped)
	case err != nil:
		_ = s.complete(ctx, p, domain.OutcomeSkipped)
		return err
	}
	return nil // collection_due（新しい collection_seq）を §4.3 で処理する
}

type canceledPayload struct {
	Subscription struct {
		SubscriptionID sk.SubscriptionID `json:"subscription_id"`
	} `json:"subscription"`
}

// onCanceled は解約（理由に関係なく）→ 請求の無効化（§4.6、P-ORC-1）。
func (s *Service) onCanceled(ctx context.Context, env sk.EventEnvelope) error {
	pl, err := sk.DecodePayload[canceledPayload](env)
	if err != nil {
		return messaging.Permanent(err)
	}
	_, err = s.d.Billing.VoidOpenInvoices(ctx, pl.Subscription.SubscriptionID, billing.VoidSubscriptionCanceled)
	return err
}

// onTenantSuspended は一括解約のジョブを作る（source_event_id で冪等。§4.7）。
func (s *Service) onTenantSuspended(ctx context.Context, env sk.EventEnvelope) error {
	pl, err := sk.DecodePayload[tenant.TenantSuspendedPayload](env)
	if err != nil {
		return messaging.Permanent(err)
	}
	return s.createBulkJob(ctx, pl.TenantID, env.EventID.UUID())
}

// ---- 回収（§4.3 collection_due） ----

type duePayload struct {
	InvoiceID      sk.InvoiceID      `json:"invoice_id"`
	SubscriptionID sk.SubscriptionID `json:"subscription_id"`
	TenantID       sk.TenantID       `json:"tenant_id"`
	Amount         int64             `json:"amount"`
	CollectionSeq  int               `json:"collection_seq"`
}

// HandleCollectionDue は orc-collection の billing.invoice_collection_due。
func (s *Service) HandleCollectionDue(ctx context.Context, env sk.EventEnvelope) error {
	if env.EventType != EventCollectionDue {
		return nil
	}
	ctx = unscoped(ctx)
	pl, err := sk.DecodePayload[duePayload](env)
	if err != nil {
		return messaging.Permanent(err)
	}
	amount, err := sk.NewMoney(pl.Amount)
	if err != nil {
		return messaging.Permanent(err)
	}
	// ① 請求の進行中のプロセス
	var p *postgres.Process
	if err := s.tx(ctx, pl.TenantID, func(ctx context.Context, r *postgres.Repo) error {
		var err error
		p, err = r.ActiveByInvoice(ctx, pl.InvoiceID)
		return err
	}); err != nil {
		return err
	}
	switch domain.DecideOnCollectionDue(p.Active(), pl.CollectionSeq) {
	case domain.DueIgnore:
		return nil
	case domain.DueCreateRetry:
		if p, err = s.retryProcess(ctx, pl); err != nil {
			return err
		}
	case domain.DueCollect, domain.DueRecollect, domain.DueAdvance:
	}
	// ② 契約・請求がまだ回収の対象か
	sub, err := s.d.Subscription.Get(ctx, pl.SubscriptionID)
	if err != nil {
		return err
	}
	if sub.Status.Terminal() {
		return s.complete(ctx, p, domain.OutcomeSkipped)
	}
	open, err := s.d.Billing.GetOpenInvoice(ctx, pl.SubscriptionID)
	if err != nil {
		return err
	}
	if open == nil || open.ID != pl.InvoiceID {
		return s.complete(ctx, p, domain.OutcomeSkipped)
	}
	// ③ 回収（直前に collecting・collection_seq を保存）
	info, err := s.d.Tenant.GetTenant(ctx, sub.TenantID)
	if err != nil {
		return err
	}
	key, err := domain.CollectKey(pl.InvoiceID, pl.CollectionSeq)
	if err != nil {
		return messaging.Permanent(err)
	}
	seq := pl.CollectionSeq
	p.State, p.CollectionSeq = domain.StateCollecting, &seq
	if err := s.save(ctx, p); err != nil {
		return err // 楽観ロックの衝突は再配信で読み直す
	}
	res, err := s.d.Payments.Collect(ctx, payments.CollectCmd{
		TenantID: sub.TenantID, TenantShopID: shopID(info), CustomerAccountID: sub.CustomerAccountID,
		Payable: payments.PayableRef{Type: payments.PayableInvoice, ID: pl.InvoiceID.String()}, Amount: amount, PaymentMethodID: sub.PaymentMethodID, IdempotencyKey: key,
	})
	// ④ 応答
	var inProgress *payments.AttemptInProgressError
	var alreadyPaid *payments.PayableAlreadyPaidError
	switch {
	case errors.Is(err, payments.ErrProviderUnavailable):
		// 試行がないので段数を消費しない。Billing のリース切れで同じ collection_seq が再送される（P-PAY-4）
		p.AttemptStatus = strp("provider_unavailable")
		return s.save(ctx, p)
	case errors.As(err, &inProgress):
		res = payments.AttemptResult{AttemptID: inProgress.AttemptID, Status: payments.AttemptProcessing}
	case errors.As(err, &alreadyPaid):
		res = payments.AttemptResult{AttemptID: alreadyPaid.AttemptID, Status: payments.AttemptSucceeded}
	case err != nil:
		p.LastError = errString(err)
		_ = s.save(ctx, p)
		return err
	}
	a := res.AttemptID.UUID()
	p.State, p.AttemptID, p.AttemptStatus, p.LastError = domain.StateAwaitingOutcome, &a, strp(string(res.Status)), nil
	if err := s.save(ctx, p); err != nil && !errors.Is(err, postgres.ErrVersionConflict) {
		return err // 衝突は結果のイベントが先に閉じた場合なので無視する
	}
	return nil
}

// retryProcess は請求の進行中プロセスがないときの retry プロセス。契約に別の進行中プロセスがあれば、
// 請求 ID がまだ記録されていない同じ契約の更新のプロセスなら引き継ぎ、それ以外は 60 秒後に再配信する。
func (s *Service) retryProcess(ctx context.Context, pl duePayload) (*postgres.Process, error) {
	id := pl.InvoiceID
	p := &postgres.Process{TenantID: pl.TenantID, SubscriptionID: pl.SubscriptionID, InvoiceID: &id, Kind: domain.KindRetry, State: domain.StateInvoiceIssued, CorrelationID: correlation(ctx)}
	existing, err := s.insertProcess(ctx, p)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, errActiveProcessOnSubscription) || existing == nil {
		return nil, err
	}
	if existing.InvoiceID == nil && existing.Kind == domain.KindRenewal {
		existing.InvoiceID, existing.State = &id, domain.StateInvoiceIssued
		if err := s.save(ctx, existing); err != nil {
			return nil, err
		}
		return existing, nil
	}
	return nil, messaging.RetryAfter(fmt.Errorf("%w: %s", errActiveProcessOnSubscription, existing.ID), retryDelay)
}

// ProcessesOf は契約のプロセス（テストと調査用）。
func (s *Service) ProcessesOf(ctx context.Context, t sk.TenantID, sub sk.SubscriptionID) ([]*postgres.Process, error) {
	var out []*postgres.Process
	err := s.tx(ctx, t, func(ctx context.Context, r *postgres.Repo) error {
		var err error
		out, err = r.BySubscription(ctx, sub)
		return err
	})
	return out, err
}
