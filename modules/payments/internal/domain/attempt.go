// Package domain は payments モジュールの集約（PaymentMethod・PaymentAttempt）と値（payments.md §3）。
// 決済代行ごとの分岐を持たない（代行の違いはアダプターが吸収する。AC-PAY-031）。
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
)

// FailureCode は代行に依存しない失敗の種類（§3.4）。
type FailureCode string

const (
	FailCardDeclined             FailureCode = "card_declined"
	FailInsufficientFunds        FailureCode = "insufficient_funds"
	FailCardExpired              FailureCode = "card_expired"
	FailInvalidCard              FailureCode = "invalid_card"
	FailPaymentMethodUnavailable FailureCode = "payment_method_unavailable"
	FailMethodReentryRequired    FailureCode = "method_reentry_required"
	FailNotProcessed             FailureCode = "not_processed"
	FailProcessingError          FailureCode = "processing_error"
	FailAsyncExpired             FailureCode = "async_expired"
	FailManual                   FailureCode = "manual"
)

// DefaultRetryable は既定の retryable（manual は指定値）。
func (f FailureCode) DefaultRetryable() bool {
	switch f {
	case FailCardDeclined, FailInsufficientFunds, FailPaymentMethodUnavailable, FailNotProcessed, FailProcessingError:
		return true
	case FailCardExpired, FailInvalidCard, FailMethodReentryRequired, FailAsyncExpired, FailManual:
	}
	return false
}

// Known は定義済みのコードか。
func (f FailureCode) Known() bool {
	switch f {
	case FailCardDeclined, FailInsufficientFunds, FailCardExpired, FailInvalidCard, FailPaymentMethodUnavailable,
		FailMethodReentryRequired, FailNotProcessed, FailProcessingError, FailAsyncExpired, FailManual:
		return true
	}
	return false
}

// ConsumerCategory は契約者に返す分類（card_expired / insufficient_funds / generic）。
func (f FailureCode) ConsumerCategory() string {
	switch f {
	case FailCardExpired:
		return "card_expired"
	case FailInsufficientFunds:
		return "insufficient_funds"
	case FailCardDeclined, FailInvalidCard, FailPaymentMethodUnavailable, FailMethodReentryRequired,
		FailNotProcessed, FailProcessingError, FailAsyncExpired, FailManual:
	}
	return "generic"
}

// Resolution は結果が確定した経路。
type Resolution string

const (
	ViaSync      Resolution = "sync"
	ViaWebhook   Resolution = "webhook"
	ViaReconcile Resolution = "reconcile"
	ViaManual    Resolution = "manual"
)

// ---- 冪等キー（§3.3） ----

var keyPattern = regexp.MustCompile(`^invoice:([0-9a-f-]{36}):([1-9][0-9]{0,4})$`)

// ParseIdempotencyKey は `invoice:{invoice_id}:{collection_seq}` を検証し、連番を返す。invoice_id は payable と一致すること（AC-PAY-034）。
func ParseIdempotencyKey(key string, payable c.PayableRef) (int, error) {
	m := keyPattern.FindStringSubmatch(key)
	if m == nil || payable.Type != c.PayableInvoice || m[1] != payable.ID {
		return 0, fmt.Errorf("%w: %q", c.ErrInvalidIdempotencyKey, key)
	}
	seq, _ := strconv.Atoi(m[2])
	return seq, nil
}

// RequestHash は同じキーで中身が違うかを判定する値（tenant・payable・金額・支払方法・契約者）。
func RequestHash(cmd c.CollectCmd) string {
	h := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s|%d|%s|%s", cmd.TenantID, cmd.Payable.Type, cmd.Payable.ID, cmd.Amount.Int64(), cmd.PaymentMethodID, cmd.CustomerAccountID))
	return hex.EncodeToString(h[:])
}

// ValidAmount は 1〜1,000,000 円か（D-08 の上限に揃える）。
func ValidAmount(m sk.Money) bool { return m.Int64() >= 1 && m.Int64() <= 1_000_000 }

// ---- PaymentAttempt ----

// AttemptState は PaymentAttempt の永続化される状態。
type AttemptState struct {
	ID                sk.PaymentAttemptID
	TenantID          sk.TenantID
	Payable           c.PayableRef
	CustomerAccountID sk.CustomerAccountID
	PaymentMethodID   sk.PaymentMethodID
	MethodType        c.MethodType
	Provider          string
	TenantShopID      *string
	Amount            sk.Money
	IdempotencyKey    string
	CollectionSeq     int
	RequestHash       string
	ProviderOrderRef  *string
	Status            c.AttemptStatus
	FailureCode       *FailureCode
	Retryable         *bool
	ProviderRawCode   *string
	Resolution        *Resolution
	ResolvedBy        *string
	ResolveNote       *string
	CheckCount        int
	LastCheckedAt     *time.Time
	RequestedAt       time.Time
	FinalizedAt       *time.Time
	Version           int
}

// Event は記録したイベントの種類（payload はアプリ層が状態から作る）。
type Event struct{ Type string }

// Attempt は 1 回の決済の試行（集約ルート）。
type Attempt struct {
	s      AttemptState
	events []Event
}

func RestoreAttempt(s AttemptState) *Attempt { return &Attempt{s: s} }

// NewAttempt は送信前の試行（processing。イベントなし）。
func NewAttempt(s AttemptState) *Attempt {
	s.Status, s.Version = c.AttemptProcessing, 1
	return &Attempt{s: s}
}

func (a *Attempt) State() AttemptState { return a.s }

func (a *Attempt) Events() []Event {
	ev := a.events
	a.events = nil
	return ev
}

// ErrSameOutcome は同じ状態への再適用（何もしない）。呼び出し元はエラーとして扱わない。
var ErrSameOutcome = errors.New("payments: same outcome")

// finalCheck は終端への再適用を判定する。同じ状態なら ErrSameOutcome（失敗コードが違っても先の結果を残す）、違う終端なら ErrAttemptFinalized。
func (a *Attempt) finalCheck(target c.AttemptStatus) error {
	if !a.s.Status.Final() {
		return nil
	}
	if a.s.Status == target {
		return ErrSameOutcome
	}
	return c.ErrAttemptFinalized
}

// Succeed は succeeded にする。
func (a *Attempt) Succeed(at time.Time, via Resolution) error {
	if err := a.finalCheck(c.AttemptSucceeded); err != nil {
		return err
	}
	a.s.Status, a.s.FinalizedAt, a.s.Resolution = c.AttemptSucceeded, &at, &via
	a.events = append(a.events, Event{Type: c.EventPaymentSucceeded})
	return nil
}

// Fail は failed にする。
func (a *Attempt) Fail(code FailureCode, retryable bool, raw string, at time.Time, via Resolution) error {
	if err := a.finalCheck(c.AttemptFailed); err != nil {
		return err
	}
	a.s.Status, a.s.FailureCode, a.s.Retryable, a.s.FinalizedAt, a.s.Resolution = c.AttemptFailed, &code, &retryable, &at, &via
	if raw != "" {
		a.s.ProviderRawCode = &raw
	}
	a.events = append(a.events, Event{Type: c.EventPaymentFailed})
	return nil
}

// AcceptAsync は pending_async にする（processing・unknown から）。
func (a *Attempt) AcceptAsync() error {
	switch a.s.Status {
	case c.AttemptProcessing, c.AttemptUnknown:
		a.s.Status = c.AttemptPendingAsync
		a.events = append(a.events, Event{Type: c.EventPaymentPending})
		return nil
	case c.AttemptPendingAsync:
		return ErrSameOutcome
	case c.AttemptSucceeded, c.AttemptFailed:
	}
	return c.ErrAttemptFinalized
}

// MarkUnknown は unknown にする（processing から）。
func (a *Attempt) MarkUnknown() error {
	switch a.s.Status {
	case c.AttemptProcessing:
		a.s.Status = c.AttemptUnknown
		a.events = append(a.events, Event{Type: c.EventPaymentOutcomeUnknown})
		return nil
	case c.AttemptUnknown, c.AttemptPendingAsync:
		return ErrSameOutcome
	case c.AttemptSucceeded, c.AttemptFailed:
	}
	return c.ErrAttemptFinalized
}

// ManualResolvable は手動確定できるか（unknown・pending_async、または 30 秒を超えた processing）。
func (a *Attempt) ManualResolvable(now time.Time) bool {
	switch a.s.Status {
	case c.AttemptUnknown, c.AttemptPendingAsync:
		return true
	case c.AttemptProcessing:
		return now.Sub(a.s.RequestedAt) > 30*time.Second
	case c.AttemptSucceeded, c.AttemptFailed:
	}
	return false
}

// ResolveManually は運営者の手動確定（§7.6）。
func (a *Attempt) ResolveManually(succeeded bool, code FailureCode, retryable bool, operator, note string, now time.Time) error {
	if a.s.Status.Final() {
		want := c.AttemptFailed
		if succeeded {
			want = c.AttemptSucceeded
		}
		if a.s.Status == want {
			return ErrSameOutcome
		}
		return c.ErrAttemptFinalized
	}
	if !a.ManualResolvable(now) {
		return c.ErrAttemptNotResolvable
	}
	a.s.ResolvedBy, a.s.ResolveNote = &operator, &note
	if succeeded {
		return a.Succeed(now, ViaManual)
	}
	return a.Fail(code, retryable, "", now, ViaManual)
}

// RecordCheck は照会の回数を記録する。
func (a *Attempt) RecordCheck(at time.Time) {
	a.s.CheckCount++
	a.s.LastCheckedAt = &at
}

// Result は contracts の AttemptResult。
func (a *Attempt) Result(replayed bool) c.AttemptResult {
	r := c.AttemptResult{AttemptID: a.s.ID, Status: a.s.Status, Retryable: a.s.Retryable, Replayed: replayed}
	if a.s.FailureCode != nil {
		code, cat := string(*a.s.FailureCode), a.s.FailureCode.ConsumerCategory()
		r.FailureCode, r.ConsumerCategory = &code, &cat
	}
	return r
}
