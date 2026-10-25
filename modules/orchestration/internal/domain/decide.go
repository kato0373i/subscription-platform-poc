// Package domain は Orchestration の判定表（orchestration.md §3・§4）。DB も外部も使わない純粋な関数だけを置く。
// 業務ルール（状態遷移・金額・リトライ間隔・回収要求の連番）は各集約が持ち、ここでは「どの順番で何を呼ぶか」だけを決める。
package domain

import (
	"errors"
	"fmt"

	sk "subscription-platform/internal/sharedkernel"
)

// ---- プロセス（§3.1） ----

// Kind はプロセスの種類。
type Kind string

const (
	KindSignup   Kind = "signup"
	KindRenewal  Kind = "renewal"
	KindRetry    Kind = "retry"
	KindRecovery Kind = "recovery"
)

// State はプロセスの段階。
type State string

const (
	StateStarted         State = "started"
	StateRenewalBegun    State = "renewal_begun"
	StateInvoiceIssued   State = "invoice_issued"
	StateCollecting      State = "collecting"
	StateAwaitingOutcome State = "awaiting_outcome"
	StateCompensating    State = "compensating"
	StateCompleted       State = "completed"
	StateFailed          State = "failed"
)

// Terminal は終端か。
func (s State) Terminal() bool { return s == StateCompleted || s == StateFailed }

// Outcome は completed のときの結果。
type Outcome string

const (
	OutcomePaid          Outcome = "paid"
	OutcomePaymentFailed Outcome = "payment_failed"
	OutcomeUncollectible Outcome = "uncollectible"
	OutcomeEnded         Outcome = "ended"
	OutcomeSignupFailed  Outcome = "signup_failed"
	OutcomeSkipped       Outcome = "skipped"
)

// ErrInvalidSeq は collection_seq が 1 未満。
var ErrInvalidSeq = errors.New("orchestration: collection_seq must be >= 1")

// CollectKey は回収キー invoice:{invoice_id}:{collection_seq}（P-X-3。AC-ORC-053）。
func CollectKey(invoiceID sk.InvoiceID, seq int) (string, error) {
	if seq < 1 || invoiceID.IsZero() {
		return "", fmt.Errorf("%w: %d", ErrInvalidSeq, seq)
	}
	return fmt.Sprintf("invoice:%s:%d", invoiceID, seq), nil
}

// ---- collection_due（§4.3 ①。AC-ORC-052） ----

// DueAction は collection_due を受けたときの動作。
type DueAction string

const (
	DueCreateRetry DueAction = "create_retry" // 請求の進行中プロセスがない → retry プロセスを作って回収
	DueCollect     DueAction = "collect"      // started・invoice_issued のプロセスを使って回収（seq を保存）
	DueRecollect   DueAction = "recollect"    // 同じ seq：同じ回収キーで Collect を再呼び出し（Payments が冪等）
	DueAdvance     DueAction = "advance"      // 大きい seq：seq を更新して新しいキーで回収
	DueIgnore      DueAction = "ignore"       // 小さい seq：古い要求なので何もしない
)

// ActiveProcess は判定に使うプロセスの要約。
type ActiveProcess struct {
	State         State
	CollectionSeq *int
}

// DecideOnCollectionDue は請求の進行中プロセス（なければ nil）と届いた collection_seq から動作を決める。
func DecideOnCollectionDue(p *ActiveProcess, seq int) DueAction {
	switch {
	case p == nil:
		return DueCreateRetry
	case p.CollectionSeq == nil:
		return DueCollect
	case seq == *p.CollectionSeq:
		return DueRecollect
	case seq > *p.CollectionSeq:
		return DueAdvance
	default:
		return DueIgnore
	}
}

// ---- 結果のイベント（§4.3。AC-ORC-024〜026・028） ----

// InvoiceEvent は購読する billing の結果のイベント。
type InvoiceEvent string

const (
	EventInvoicePaid          InvoiceEvent = "billing.invoice_paid"
	EventInvoicePaymentFailed InvoiceEvent = "billing.invoice_payment_failed"
	EventInvoiceUncollectible InvoiceEvent = "billing.invoice_marked_uncollectible"
)

// SubStatus は契約の状態（subscription/contracts.Status と同じ値。domain は他モジュールに依存しない）。
type SubStatus string

const (
	SubPendingInitial SubStatus = "pending_initial"
	SubTrialing       SubStatus = "trialing"
	SubActive         SubStatus = "active"
	SubPastDue        SubStatus = "past_due"
	SubCanceled       SubStatus = "canceled"
	SubSignupFailed   SubStatus = "signup_failed"
)

// Command は契約への命令（または補償・アラート）。
type Command string

const (
	CmdNone                 Command = "none"
	CmdActivate             Command = "activate"          // → ConfirmCouponRedemption（停止中の事業者なら ScheduleSystemCancellation）
	CmdConfirmRenewal       Command = "confirm_renewal"   //
	CmdRecover              Command = "recover"           //
	CmdMarkPastDue          Command = "mark_past_due"     //
	CmdCancelDunning        Command = "cancel_dunning"    //
	CmdCompensateSignup     Command = "compensate_signup" // MarkSignupFailed → VoidOpenInvoices(signup_failed) → ReleaseCouponRedemption
	CmdAlertPaidAfterCancel Command = "alert_paid_after_cancel"
	CmdAlertPaidAfterFailed Command = "alert_paid_after_signup_failed"
)

// Decision は結果のイベントの判定。
type Decision struct {
	Command Command
	Outcome Outcome // プロセスを閉じるときの outcome
}

// DecideOnInvoiceEvent はイベント × 契約状態 → 命令（§4.3 の表）。
func DecideOnInvoiceEvent(ev InvoiceEvent, st SubStatus) Decision {
	switch ev {
	case EventInvoicePaid:
		switch st {
		case SubPendingInitial:
			return Decision{CmdActivate, OutcomePaid}
		case SubTrialing, SubActive:
			return Decision{CmdConfirmRenewal, OutcomePaid}
		case SubPastDue:
			return Decision{CmdRecover, OutcomePaid}
		case SubCanceled:
			return Decision{CmdAlertPaidAfterCancel, OutcomePaid}
		case SubSignupFailed:
			return Decision{CmdAlertPaidAfterFailed, OutcomePaid}
		}
	case EventInvoicePaymentFailed:
		switch st {
		case SubPendingInitial:
			return Decision{CmdCompensateSignup, OutcomeSignupFailed}
		case SubTrialing, SubActive:
			return Decision{CmdMarkPastDue, OutcomePaymentFailed}
		case SubPastDue, SubCanceled, SubSignupFailed:
			return Decision{CmdNone, OutcomePaymentFailed}
		}
	case EventInvoiceUncollectible:
		switch st {
		case SubTrialing, SubActive, SubPastDue:
			return Decision{CmdCancelDunning, OutcomeUncollectible}
		case SubPendingInitial:
			return Decision{CmdCompensateSignup, OutcomeSignupFailed}
		case SubCanceled, SubSignupFailed:
			return Decision{CmdNone, OutcomeUncollectible}
		}
	}
	return Decision{CmdNone, OutcomeSkipped}
}

// ---- 申込の再実行（§3.4・§4.1。AC-ORC-054） ----

// SagaStatus はサガの状態。
type SagaStatus string

const (
	SagaInProgress     SagaStatus = "in_progress"
	SagaPendingOutcome SagaStatus = "pending_outcome"
	SagaCompleted      SagaStatus = "completed"
	SagaAborted        SagaStatus = "aborted"
)

// ReplayAction は同じキーで再実行されたときの動作。
type ReplayAction string

const (
	ReplayResume      ReplayAction = "resume"       // step から再開（同じ subscription_id・回収キー）
	ReplayRespond202  ReplayAction = "respond_202"  // pending_initial のまま
	ReplayRespond201  ReplayAction = "respond_201"  // active / trialing
	ReplayDeclined    ReplayAction = "respond_422"  // signup_failed（payment_declined）
	ReplayStartOver   ReplayAction = "start_over"   // aborted：新しい subscription_id で最初から
	ReplayStoredReply ReplayAction = "stored_reply" // completed：保存した要約から同じ応答
)

// DecideCheckoutReplay はサガの状態と（pending_outcome のときの）契約の状態から動作を決める。
func DecideCheckoutReplay(saga SagaStatus, sub SubStatus) ReplayAction {
	switch saga {
	case SagaInProgress:
		return ReplayResume
	case SagaAborted:
		return ReplayStartOver
	case SagaCompleted:
		return ReplayStoredReply
	case SagaPendingOutcome:
		switch sub {
		case SubTrialing, SubActive, SubPastDue:
			return ReplayRespond201
		case SubSignupFailed, SubCanceled:
			return ReplayDeclined
		case SubPendingInitial:
		}
	}
	return ReplayRespond202
}

// ---- 一括解約（§4.7） ----

// BulkAction は一括解約の 1 件の動作。
type BulkAction string

const (
	BulkCancelNow BulkAction = "cancel"   // past_due → Cancel(tenant_suspended)
	BulkSchedule  BulkAction = "schedule" // trialing・active → ScheduleSystemCancellation(tenant_suspended)
	BulkSkip      BulkAction = "skip"     // pending_initial（確定時に invoice_paid で処理）・終端
)

// DecideBulk は契約の状態から一括解約の動作を決める。
func DecideBulk(st SubStatus) BulkAction {
	switch st {
	case SubPastDue:
		return BulkCancelNow
	case SubTrialing, SubActive:
		return BulkSchedule
	case SubPendingInitial, SubCanceled, SubSignupFailed:
	}
	return BulkSkip
}
