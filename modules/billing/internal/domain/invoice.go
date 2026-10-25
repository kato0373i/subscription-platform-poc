// Package domain は billing モジュールの集約（Invoice・DunningPolicy。billing.md §3）。
// 金額と税は Money の整数演算と MulRatFloor で計算する（浮動小数点を使わない）。
package domain

import (
	"bytes"
	"fmt"
	"time"

	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/billing/contracts"
)

// Line は請求の明細（作成時点のスナップショット）。
type Line struct {
	LineNo         int
	Kind           string // plan | discount
	Description    string
	PriceVersionID *sk.PriceVersionID
	Discount       *DiscountSnapshot
	Quantity       int
	UnitAmount     sk.Money
	Amount         sk.Money // discount も正の値
	TaxRate        sk.TaxRate
	Period         sk.BillingPeriod
}

// DiscountSnapshot は割引明細の条件。
type DiscountSnapshot struct {
	Type               sk.DiscountType  `json:"type"`
	Value              int64            `json:"value"`
	CouponRedemptionID *sk.RedemptionID `json:"coupon_redemption_id,omitempty"`
}

// DunningSchedule は最初の予定内の失敗の時点で保存するリトライの予定（以後変えない。DP-5）。
type DunningSchedule struct {
	OffsetsDays   []int     `json:"offsets_days"`
	PolicySource  string    `json:"policy_source"` // tenant | default
	FirstFailedOn sk.Date   `json:"first_failed_on"`
	SnapshottedAt time.Time `json:"snapshotted_at"`
}

// PendingRequest は結果待ちの回収要求。
type PendingRequest struct {
	Seq         int                `json:"seq"`
	Reason      c.CollectionReason `json:"reason"`
	RequestedBy string             `json:"requested_by"`
	RequestedAt time.Time          `json:"requested_at"`
}

// State は Invoice の永続化される状態。
type State struct {
	ID                sk.InvoiceID
	TenantID          sk.TenantID
	SubscriptionID    sk.SubscriptionID
	CustomerAccountID sk.CustomerAccountID
	Kind              c.InvoiceKind
	Mode              c.CollectionMode
	Period            sk.BillingPeriod
	Status            c.InvoiceStatus
	Lines             []Line
	Subtotal          sk.Money
	DiscountTotal     sk.Money
	Total             sk.Money
	Tax               []c.TaxLine
	IssuedAt          time.Time
	PaidAt            *time.Time
	PaidAttemptID     *sk.PaymentAttemptID
	PaidVia           *string // payment | zero_amount
	UncollectibleAt   *time.Time
	VoidedAt          *time.Time
	VoidReason        *c.VoidReason
	VoidRequestedAt   *time.Time
	VoidRequestReason *c.VoidReason
	AttemptCount      int
	DunningStage      int
	Dunning           *DunningSchedule
	NextCollectionAt  *time.Time
	CollectionSeq     int
	Pending           *PendingRequest
	LeaseUntil        *time.Time
	LastAttemptID     *sk.PaymentAttemptID
	LastFailureCode   *string
	LastRetryable     *bool
	Version           int
}

// Event は記録したイベント（payload は §4 の表のとおり）。
type Event struct {
	Type    string
	Payload map[string]any
}

// Invoice は請求（集約ルート）。
type Invoice struct {
	s      State
	events []Event
}

func Restore(s State) *Invoice { return &Invoice{s: s} }

func (x *Invoice) State() State { return x.s }

func (x *Invoice) Events() []Event {
	ev := x.events
	x.events = nil
	return ev
}

func ptr[T any](v T) *T { return &v }

func validation(msg string, args ...any) error {
	return fmt.Errorf("%w: %s", c.ErrValidation, fmt.Sprintf(msg, args...))
}

// Calc は明細と税の計算（§3.2）。
func Calc(plan sk.Money, d *c.AppliedDiscount, rate sk.TaxRate) (discount, total, tax sk.Money) {
	if d != nil {
		discount, _ = sk.Discount{Type: d.Type, Value: d.Value}.Apply(plan)
	}
	total = plan.SubClamped(discount)
	return discount, total, rate.InclusiveTax(total) // 請求 1 件 × 税率ごとに 1 回だけ丸める
}

// NewInvoice は請求を作る（§3.3 の「作成」）。lease は回収要求のリース時間。
func NewInvoice(id sk.InvoiceID, cmd c.IssueInvoiceCmd, now time.Time, lease time.Duration) (*Invoice, error) {
	p := cmd.PlanLine
	switch {
	case cmd.Period.Start.IsZero() || cmd.Period.End.Before(cmd.Period.Start):
		return nil, validation("period %s", cmd.Period)
	case p.Amount.Int64() < 1 || p.Amount.Int64() > 1_000_000:
		return nil, validation("plan amount %d", p.Amount.Int64())
	case cmd.Kind != c.KindInitial && cmd.Kind != c.KindRenewal:
		return nil, validation("kind %q", cmd.Kind)
	case cmd.CollectionMode != c.ModeScheduled && cmd.CollectionMode != c.ModeCallerManaged:
		return nil, validation("collection mode %q", cmd.CollectionMode)
	}
	if d := cmd.Discount; d != nil {
		if err := (sk.Discount{Type: d.Type, Value: d.Value}).Validate(); err != nil || (d.Type == sk.DiscountAmount && d.Value > 1_000_000) {
			return nil, validation("discount %s %d", d.Type, d.Value)
		}
	}
	discount, total, tax := Calc(p.Amount, cmd.Discount, p.TaxRate)
	desc := p.Description
	if desc == "" {
		desc = "プラン料金"
	}
	pv := p.PriceVersionID
	lines := []Line{{LineNo: 1, Kind: "plan", Description: desc, PriceVersionID: &pv, Quantity: 1, UnitAmount: p.Amount, Amount: p.Amount, TaxRate: p.TaxRate, Period: cmd.Period}}
	if d := cmd.Discount; d != nil {
		lines = append(lines, Line{LineNo: 2, Kind: "discount", Description: "割引",
			Discount: &DiscountSnapshot{Type: d.Type, Value: d.Value, CouponRedemptionID: d.CouponRedemptionID},
			Quantity: 1, UnitAmount: discount, Amount: discount, TaxRate: p.TaxRate, Period: cmd.Period})
	}
	x := &Invoice{s: State{
		ID: id, TenantID: cmd.TenantID, SubscriptionID: cmd.SubscriptionID, CustomerAccountID: cmd.CustomerAccountID, Kind: cmd.Kind,
		Mode: cmd.CollectionMode, Period: cmd.Period, Status: c.StatusOpen, Lines: lines, Subtotal: p.Amount, DiscountTotal: discount, Total: total,
		Tax: []c.TaxLine{{Rate: p.TaxRate, TaxableAmount: total, TaxAmount: tax}}, IssuedAt: now, Version: 1,
	}}
	x.emit(c.EventInvoiceIssued, x.issuedPayload())
	switch {
	case total.IsZero(): // INV-5：0 円は作成と同時に paid。回収要求を出さない
		x.s.Status, x.s.PaidAt, x.s.PaidVia = c.StatusPaid, &now, ptr("zero_amount")
		if cmd.CollectionMode == c.ModeCallerManaged {
			x.s.CollectionSeq = 1 // INV-8：caller_managed は collection_seq=1（決済はしない）
		}
		x.emit(c.EventInvoicePaid, x.paidPayload())
	case cmd.CollectionMode == c.ModeScheduled:
		x.s.NextCollectionAt = &now
		x.newRequest(c.ReasonInitial, "system", now, lease)
	default: // caller_managed：collection_seq=1 を払い出すが、回収要求は出さない（P-X-3）
		x.s.CollectionSeq = 1
		x.s.Pending = &PendingRequest{Seq: 1, Reason: c.ReasonInitial, RequestedBy: "caller", RequestedAt: now}
	}
	return x, nil
}

// SameRequest は同じ請求キーで内容が同じか（IssueInvoice の冪等性）。
func (x *Invoice) SameRequest(cmd c.IssueInvoiceCmd) bool {
	s := x.s
	plan := s.Lines[0]
	if !s.Period.End.Equal(cmd.Period.End) || s.Kind != cmd.Kind || s.Mode != cmd.CollectionMode || plan.PriceVersionID == nil ||
		*plan.PriceVersionID != cmd.PlanLine.PriceVersionID || plan.Amount != cmd.PlanLine.Amount || plan.TaxRate != cmd.PlanLine.TaxRate {
		return false
	}
	var d *DiscountSnapshot
	if len(s.Lines) > 1 {
		d = s.Lines[1].Discount
	}
	if (d == nil) != (cmd.Discount == nil) {
		return false
	}
	return d == nil || (d.Type == cmd.Discount.Type && d.Value == cmd.Discount.Value)
}

func (x *Invoice) emit(t string, p map[string]any) {
	x.events = append(x.events, Event{Type: t, Payload: p})
}

// newRequest は新しい回収要求を払い出す（collection_seq+1。INV-10）。
func (x *Invoice) newRequest(reason c.CollectionReason, by string, now time.Time, lease time.Duration) {
	x.s.CollectionSeq++
	x.s.Pending = &PendingRequest{Seq: x.s.CollectionSeq, Reason: reason, RequestedBy: by, RequestedAt: now}
	x.emitDue(now, lease)
}

// emitDue は結果待ちの要求で collection_due を出し、リースを張る（再送でも同じ collection_seq）。
func (x *Invoice) emitDue(now time.Time, lease time.Duration) {
	until := now.Add(lease)
	x.s.LeaseUntil = &until
	x.emit(c.EventInvoiceCollectionDue, map[string]any{
		"invoice_id": x.s.ID, "subscription_id": x.s.SubscriptionID, "tenant_id": x.s.TenantID, "amount": x.s.Total.Int64(),
		"collection_seq": x.s.Pending.Seq, "dunning_stage": x.s.DunningStage, "reason": x.s.Pending.Reason,
		"requested_by": x.s.Pending.RequestedBy, "customer_account_id": x.s.CustomerAccountID,
	})
}

// ---- 結果の反映（§3.3・§3.4） ----

// Outcome は反映する結果。AttemptFinalized はこの試行に確定結果（succeeded / failed）が既にあるか（INV-7）。
type Outcome struct {
	AttemptID        sk.PaymentAttemptID
	Status           c.OutcomeStatus
	FailureCode      string
	Retryable        bool
	AttemptFinalized bool
}

// RecordOutcome は結果を反映する。applied は状態を変えたか。anomaly は記録する異常（duplicate_success など）。
func (x *Invoice) RecordOutcome(o Outcome, policy DunningPolicy, today sk.Date, now time.Time) (applied bool, anomaly string) {
	s := &x.s
	if s.Status.Terminal() {
		if o.Status == c.OutcomeSucceeded {
			if s.Status == c.StatusPaid {
				if s.PaidAttemptID == nil || *s.PaidAttemptID != o.AttemptID {
					return false, "duplicate_success"
				}
				return false, ""
			}
			return false, "late_success" // 返金は運用（§9-6）
		}
		return false, ""
	}
	switch o.Status {
	case c.OutcomeSucceeded: // 試行の新旧に関係なく paid にする（お金は動いている）
		if s.VoidRequestedAt != nil {
			anomaly = "paid_after_void_request" // P-BIL-3
		}
		s.Status, s.PaidAt, s.PaidAttemptID, s.PaidVia = c.StatusPaid, &now, &o.AttemptID, ptr("payment")
		s.NextCollectionAt, s.Pending, s.LeaseUntil, s.LastAttemptID = nil, nil, nil, &o.AttemptID
		x.emit(c.EventInvoicePaid, x.paidPayload())
		return true, anomaly
	case c.OutcomePending:
		if o.AttemptFinalized || s.Status == c.StatusProcessing {
			return false, ""
		}
		s.Status, s.LastAttemptID = c.StatusProcessing, &o.AttemptID
		return true, ""
	case c.OutcomeUnknown:
		if o.AttemptFinalized || s.Status == c.StatusPaymentUnknown {
			return false, ""
		}
		s.Status, s.LastAttemptID = c.StatusPaymentUnknown, &o.AttemptID
		return true, ""
	case c.OutcomeFailed:
	}
	if o.AttemptFinalized || !x.newerOrSame(o.AttemptID) {
		return false, "" // 古い試行の失敗は記録だけ
	}
	return x.applyFailure(o, policy, today, now), ""
}

// newerOrSame は last_attempt_id が NULL・同じ・またはこの試行より古いか（UUIDv7 の時刻順）。
func (x *Invoice) newerOrSame(a sk.PaymentAttemptID) bool {
	last := x.s.LastAttemptID
	if last == nil || *last == a {
		return true
	}
	lu, au := last.UUID(), a.UUID()
	return bytes.Compare(lu[:], au[:]) < 0
}

func (x *Invoice) applyFailure(o Outcome, policy DunningPolicy, today sk.Date, now time.Time) bool {
	s := &x.s
	seq := s.CollectionSeq
	reason := c.ReasonInitial
	if s.Pending != nil {
		seq, reason = s.Pending.Seq, s.Pending.Reason
	}
	s.LastAttemptID, s.LastFailureCode, s.LastRetryable = &o.AttemptID, &o.FailureCode, &o.Retryable
	s.Pending, s.LeaseUntil = nil, nil
	if s.VoidRequestedAt != nil {
		x.void(*s.VoidRequestReason, now)
		return true
	}
	s.AttemptCount++
	s.Status = c.StatusOpen
	failed := func() {
		remaining := 0
		if s.Dunning != nil {
			remaining = max(len(s.Dunning.OffsetsDays)-s.DunningStage+1, 0) // 段数 n の失敗の後に残る予定内のリトライ（offsets[n-1:]）
		}
		x.emit(c.EventInvoicePaymentFailed, map[string]any{
			"invoice_id": s.ID, "subscription_id": s.SubscriptionID, "attempt_no": seq, "collection_seq": seq, "next_collection_at": s.NextCollectionAt,
			"failure_code": o.FailureCode, "retryable": o.Retryable, "dunning_stage": s.DunningStage, "attempt_id": o.AttemptID, "reason": reason,
			"collection_mode": s.Mode, "remaining_retries": remaining,
		})
	}
	if s.Mode == c.ModeCallerManaged || !reason.Scheduled() {
		failed() // ダニングしない／予定外の試行は段数とスケジュールを変えない（FR-017）
		return true
	}
	if s.Dunning == nil {
		s.Dunning = &DunningSchedule{OffsetsDays: append([]int(nil), policy.Offsets...), PolicySource: policy.Source(), FirstFailedOn: today, SnapshottedAt: now}
	}
	s.DunningStage++
	n := s.DunningStage
	if n > len(s.Dunning.OffsetsDays) {
		s.Status, s.UncollectibleAt, s.NextCollectionAt = c.StatusUncollectible, &now, nil
		x.emit(c.EventInvoiceMarkedUncollectible, map[string]any{
			"invoice_id": s.ID, "subscription_id": s.SubscriptionID, "attempt_no": seq, "collection_seq": seq, "dunning_stage": n,
			"failure_code": o.FailureCode, "retryable": o.Retryable,
		})
		return true
	}
	next := s.Dunning.FirstFailedOn.AddDays(s.Dunning.OffsetsDays[n-1]).AtJST(10, 0)
	if next.Before(now) {
		next = now // 計算上の時刻が過去なら次の毎時ジョブで回収する（§9-8）
	}
	s.NextCollectionAt = &next
	failed()
	return true
}

// ---- 回収要求（§3.5・§8） ----

// RequestCollection は予定外の回収要求（即時の再決済・ops）。リース中は既存の要求を返す（イベントなし）。
func (x *Invoice) RequestCollection(reason c.CollectionReason, by string, now time.Time, lease time.Duration) error {
	s := &x.s
	switch { // §3.3 の表：processing / payment_unknown はモードに関係なく結果待ち
	case s.Status == c.StatusProcessing || s.Status == c.StatusPaymentUnknown:
		return c.ErrCollectionInProgress
	case s.Status.Terminal() || s.Mode == c.ModeCallerManaged:
		return c.ErrInvoiceNotCollectible
	}
	if s.Pending != nil && s.LeaseUntil != nil && now.Before(*s.LeaseUntil) {
		return nil
	}
	x.newRequest(reason, by, now, lease)
	return nil
}

// DueForCollection は回収時刻ジョブの対象か。
func (x *Invoice) DueForCollection(now time.Time) bool {
	s := x.s
	return s.Status == c.StatusOpen && s.Mode == c.ModeScheduled && s.NextCollectionAt != nil && !s.NextCollectionAt.After(now) &&
		(s.LeaseUntil == nil || !s.LeaseUntil.After(now))
}

// EmitDueCollection は予定の回収要求を出す。結果待ちの要求があれば同じ collection_seq で再送する（段数は消費しない。P-PAY-4）。
func (x *Invoice) EmitDueCollection(now time.Time, lease time.Duration) bool {
	if !x.DueForCollection(now) {
		return false
	}
	if x.s.Pending != nil {
		x.emitDue(now, lease)
		return true
	}
	reason := c.ReasonRetry
	if x.s.DunningStage == 0 {
		reason = c.ReasonInitial
	}
	x.newRequest(reason, "system", now, lease)
	return true
}

// ---- 無効化 ----

func (x *Invoice) void(reason c.VoidReason, now time.Time) {
	s := &x.s
	s.Status, s.VoidedAt, s.VoidReason = c.StatusVoid, &now, &reason
	s.NextCollectionAt, s.Pending, s.LeaseUntil = nil, nil, nil
	x.emit(c.EventInvoiceVoided, map[string]any{"invoice_id": s.ID, "subscription_id": s.SubscriptionID, "reason": reason})
}

// Void は open なら void にする。processing / payment_unknown なら void 要求を記録して結果を待つ（P-BIL-3）。変化があれば true。
func (x *Invoice) Void(reason c.VoidReason, now time.Time) bool {
	switch x.s.Status {
	case c.StatusOpen:
		x.void(reason, now)
		return true
	case c.StatusProcessing, c.StatusPaymentUnknown:
		if x.s.VoidRequestedAt != nil {
			return false
		}
		x.s.VoidRequestedAt, x.s.VoidRequestReason = &now, &reason
		return true
	case c.StatusPaid, c.StatusUncollectible, c.StatusVoid:
	}
	return false
}

// ---- payload・参照 ----

func (x *Invoice) taxPayload() []map[string]any {
	out := make([]map[string]any, len(x.s.Tax))
	for i, t := range x.s.Tax {
		out[i] = map[string]any{"rate": t.Rate.String(), "taxable_amount": t.TaxableAmount.Int64(), "tax_amount": t.TaxAmount.Int64()}
	}
	return out
}

func (x *Invoice) issuedPayload() map[string]any {
	s := x.s
	lines := make([]map[string]any, len(s.Lines))
	for i, l := range s.Lines {
		lines[i] = map[string]any{"line_no": l.LineNo, "kind": l.Kind, "description": l.Description, "amount": l.Amount.Int64(), "tax_rate": l.TaxRate.String()}
	}
	return map[string]any{
		"invoice_id": s.ID, "subscription_id": s.SubscriptionID, "period": map[string]any{"start": s.Period.Start, "end": s.Period.End},
		"total": s.Total.Int64(), "tax": s.Tax[0].TaxAmount.Int64(), "customer_account_id": s.CustomerAccountID, "kind": s.Kind,
		"collection_mode": s.Mode, "subtotal": s.Subtotal.Int64(), "discount_total": s.DiscountTotal.Int64(), "tax_breakdown": x.taxPayload(),
		"lines": lines, "issued_at": s.IssuedAt,
	}
}

func (x *Invoice) paidPayload() map[string]any {
	s := x.s
	return map[string]any{
		"invoice_id": s.ID, "subscription_id": s.SubscriptionID, "amount": s.Total.Int64(), "paid_at": s.PaidAt, "attempt_id": s.PaidAttemptID,
		"paid_via": s.PaidVia, "kind": s.Kind, "period": map[string]any{"start": s.Period.Start, "end": s.Period.End}, "tax_breakdown": x.taxPayload(),
	}
}

// View は contracts の形。
func (x *Invoice) View() c.InvoiceView {
	s := x.s
	lines := make([]c.LineView, len(s.Lines))
	for i, l := range s.Lines {
		lines[i] = c.LineView{LineNo: l.LineNo, Kind: l.Kind, Description: l.Description, Amount: l.Amount}
	}
	return c.InvoiceView{
		ID: s.ID, TenantID: s.TenantID, SubscriptionID: s.SubscriptionID, CustomerAccountID: s.CustomerAccountID, Kind: s.Kind, CollectionMode: s.Mode,
		Status: s.Status, Period: s.Period, Subtotal: s.Subtotal, DiscountTotal: s.DiscountTotal, Total: s.Total, Tax: s.Tax, Lines: lines,
		AttemptCount: s.AttemptCount, DunningStage: s.DunningStage, NextCollectionAt: s.NextCollectionAt, CollectionSeq: s.CollectionSeq,
		IssuedAt: s.IssuedAt, PaidAt: s.PaidAt, Version: s.Version,
	}
}

// MarkSaved は保存したことを反映する（version を進める）。
func (x *Invoice) MarkSaved() { x.s.Version++ }

// ---- DunningPolicy（§3.6） ----

// DefaultOffsets は方針が未設定の事業者の既定（DP-4）。
var DefaultOffsets = []int{1, 3, 7}

// DunningPolicy はリトライの方針。IsDefault は行がない事業者の既定値。
type DunningPolicy struct {
	TenantID  sk.TenantID
	Offsets   []int
	IsDefault bool
	Version   int
	UpdatedAt *time.Time
	UpdatedBy string
}

func (p DunningPolicy) Source() string {
	if p.IsDefault {
		return "default"
	}
	return "tenant"
}

// DefaultPolicy は既定の方針。
func DefaultPolicy(t sk.TenantID) DunningPolicy {
	return DunningPolicy{TenantID: t, Offsets: append([]int(nil), DefaultOffsets...), IsDefault: true}
}

// ValidateOffsets は DP-1〜DP-3（0〜5 個、1〜60、厳密に昇順）。
func ValidateOffsets(offsets []int) error {
	if len(offsets) > 5 {
		return &c.InvalidDunningPolicyError{Code: "too_many"}
	}
	for i, d := range offsets {
		if d < 1 || d > 60 {
			return &c.InvalidDunningPolicyError{Code: "out_of_range"}
		}
		if i > 0 && d <= offsets[i-1] {
			return &c.InvalidDunningPolicyError{Code: "not_increasing"}
		}
	}
	return nil
}
