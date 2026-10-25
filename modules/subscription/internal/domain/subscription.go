// Package domain は subscription モジュールの Subscription 集約（subscription.md §3.1）。
// 状態遷移の表をそのままメソッドにする。各メソッドは変化があればドメインイベントを記録し、冪等な再実行（○）では何も記録しない。
package domain

import (
	"reflect"
	"time"

	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/subscription/contracts"
)

// PendingRenewal は更新処理中（R）の確定待ちの条件。
type PendingRenewal struct {
	Period         sk.BillingPeriod   `json:"period"`
	PlanID         sk.PlanID          `json:"plan_id"`
	PriceVersionID sk.PriceVersionID  `json:"price_version_id"`
	Interval       sk.BillingInterval `json:"interval"`
	Discount       *c.AppliedDiscount `json:"discount,omitempty"` // 確定後の applied_discount（残り回数は減らした後）
	TermsDiscount  *c.AppliedDiscount `json:"terms_discount,omitempty"`
	PlanChanged    bool               `json:"plan_changed"`
	FirstCharge    bool               `json:"first_charge"`
	FromPlanID     *sk.PlanID         `json:"from_plan_id,omitempty"`
}

// State は Subscription の永続化される状態。
type State struct {
	ID                   sk.SubscriptionID
	TenantID             sk.TenantID
	CustomerAccountID    sk.CustomerAccountID
	TenantCustomerID     string
	SignupRequestID      string
	ProductID            sk.ProductID
	PlanID               sk.PlanID
	PriceVersionID       sk.PriceVersionID
	Interval             sk.BillingInterval
	Quantity             int
	Status               c.Status
	SignupDate           sk.Date
	CurrentPeriod        sk.BillingPeriod
	NextRenewalDate      *sk.Date
	AnchorDay            int
	TrialStart, TrialEnd *sk.Date
	TrialWillEndNotified bool
	PendingPlanChange    *c.PendingPlanChange
	Cancellation         *c.Cancellation
	PendingRenewal       *PendingRenewal
	AppliedDiscount      *c.AppliedDiscount
	PaymentMethodID      sk.PaymentMethodID
	PastDueInvoiceID     *sk.InvoiceID
	LastPaidInvoiceID    *sk.InvoiceID
	CancelReason         *c.CancelReason
	CanceledAt           *time.Time
	SignupFailureReason  *string
	Version              int
	CreatedAt            time.Time
}

// Event は記録したドメインイベント（payload のスナップショットはアプリ層が付ける）。
type Event struct {
	Type  string
	Extra map[string]any
}

// Subscription は契約（集約ルート）。
type Subscription struct {
	s      State
	orig   State // 読み込んだ時点の状態（変更の有無の判定）
	events []Event
}

func Restore(s State) *Subscription { return &Subscription{s: s, orig: clone(s)} }

// clone はポインタの先まで写す（変更の判定のため）。
func clone(s State) State {
	b := s
	cp := func(d *sk.Date) *sk.Date {
		if d == nil {
			return nil
		}
		v := *d
		return &v
	}
	b.NextRenewalDate, b.TrialStart, b.TrialEnd = cp(s.NextRenewalDate), cp(s.TrialStart), cp(s.TrialEnd)
	if s.PendingPlanChange != nil {
		v := *s.PendingPlanChange
		b.PendingPlanChange = &v
	}
	if s.Cancellation != nil {
		v := *s.Cancellation
		b.Cancellation = &v
	}
	if s.PendingRenewal != nil {
		v := *s.PendingRenewal
		b.PendingRenewal = &v
	}
	if s.AppliedDiscount != nil {
		v := *s.AppliedDiscount
		b.AppliedDiscount = &v
	}
	return b
}

// Dirty は読み込んだ後に状態が変わったか（イベントを出さない変更も保存するため）。
func (x *Subscription) Dirty() bool { return !reflect.DeepEqual(x.s, x.orig) }

// MarkSaved は保存したことを反映する（version を 1 進める）。
func (x *Subscription) MarkSaved() {
	x.s.Version++
	x.orig = clone(x.s)
}

func (x *Subscription) State() State { return x.s }

// Events は記録したイベントを返して消す。
func (x *Subscription) Events() []Event {
	ev := x.events
	x.events = nil
	return ev
}

func (x *Subscription) emit(t string, extra map[string]any) {
	x.events = append(x.events, Event{Type: t, Extra: extra})
}

func invalid(from c.Status, cmd string) error {
	return &c.InvalidTransitionError{From: from, Command: cmd}
}

func datePtr(d sk.Date) *sk.Date { return &d }

// ---- 作成 ----

// SignupInput は NewForSignup の入力。ApplyTrial はアプリ層が TrialUsage を見て決める（§4.2）。
type SignupInput struct {
	ID                sk.SubscriptionID
	TenantID          sk.TenantID
	CustomerAccountID sk.CustomerAccountID
	TenantCustomerID  string
	SignupRequestID   string
	Plan              c.PlanInput
	PaymentMethodID   sk.PaymentMethodID
	Discount          *c.DiscountInput
	SignupDate        sk.Date
	ApplyTrial        bool
	Now               time.Time
}

// NewForSignup は申込の契約を作る。トライアルなら trialing（started を出す）、それ以外は pending_initial（イベントなし。FR-013）。
func NewForSignup(in SignupInput) *Subscription {
	s := State{
		ID: in.ID, TenantID: in.TenantID, CustomerAccountID: in.CustomerAccountID, TenantCustomerID: in.TenantCustomerID,
		SignupRequestID: in.SignupRequestID, ProductID: in.Plan.ProductID, PlanID: in.Plan.PlanID, PriceVersionID: in.Plan.PriceVersionID,
		Interval: in.Plan.Interval, Quantity: 1, SignupDate: in.SignupDate, PaymentMethodID: in.PaymentMethodID, Version: 1, CreatedAt: in.Now,
	}
	if in.Discount != nil {
		s.AppliedDiscount = &c.AppliedDiscount{
			CouponID: in.Discount.CouponID, RedemptionID: in.Discount.RedemptionID, Type: in.Discount.Type, Value: in.Discount.Value,
			DurationCycles: in.Discount.DurationCycles, RemainingCycles: in.Discount.DurationCycles,
		}
	}
	x := &Subscription{s: s}
	if in.ApplyTrial && in.Plan.TrialDays > 0 {
		trial, _ := sk.DaysPeriod(in.SignupDate, in.Plan.TrialDays)
		first := trial.NextStart()
		x.s.Status, x.s.TrialStart, x.s.TrialEnd = c.StatusTrialing, datePtr(trial.Start), datePtr(trial.End)
		x.s.CurrentPeriod, x.s.NextRenewalDate, x.s.AnchorDay = trial, datePtr(first), first.Day() // anchor は初回課金日の日（D-12）
		x.emit(c.EventStarted, nil)
		return x
	}
	x.s.Status, x.s.AnchorDay = c.StatusPendingInitial, in.SignupDate.Day()
	x.s.CurrentPeriod = sk.FirstPeriod(in.SignupDate, in.Plan.Interval, x.s.AnchorDay)
	x.s.NextRenewalDate = datePtr(x.s.CurrentPeriod.NextStart())
	if d := x.s.AppliedDiscount; d != nil && d.RemainingCycles > 0 {
		d.RemainingCycles-- // 初回の請求で 1 回分を使う
	}
	return x
}

// InitialTerms は pending_initial の初回の請求の条件。
func (x *Subscription) InitialTerms() *c.Terms {
	if x.s.Status != c.StatusPendingInitial {
		return nil
	}
	t := &c.Terms{
		Period: x.s.CurrentPeriod, PlanID: x.s.PlanID, PriceVersionID: x.s.PriceVersionID, Interval: x.s.Interval,
		Quantity: x.s.Quantity, PaymentMethodID: x.s.PaymentMethodID,
	}
	if d := x.s.AppliedDiscount; d != nil {
		cp := *d
		t.Discount = &cp
	}
	return t
}

// ---- 申込の成立・不成立 ----

// Activate は初回決済の成功（pending_initial → active）。
func (x *Subscription) Activate(inv sk.InvoiceID) error {
	switch x.s.Status {
	case c.StatusPendingInitial:
		x.s.Status, x.s.LastPaidInvoiceID = c.StatusActive, &inv
		x.emit(c.EventStarted, map[string]any{"invoice_id": inv})
		return nil
	case c.StatusActive:
		if x.lastPaid(inv) {
			return nil
		}
	case c.StatusCanceled, c.StatusSignupFailed:
		return c.ErrTerminal
	case c.StatusTrialing, c.StatusPastDue:
	}
	return invalid(x.s.Status, "Activate")
}

func (x *Subscription) lastPaid(inv sk.InvoiceID) bool {
	return x.s.LastPaidInvoiceID != nil && *x.s.LastPaidInvoiceID == inv
}

// MarkSignupFailed は申込の不成立（pending_initial → signup_failed）。
func (x *Subscription) MarkSignupFailed(reason string) error {
	switch x.s.Status {
	case c.StatusPendingInitial:
		x.s.Status, x.s.SignupFailureReason = c.StatusSignupFailed, &reason
		x.clearSchedules()
		x.emit(c.EventSignupFailed, map[string]any{"reason": reason})
		return nil
	case c.StatusSignupFailed:
		return nil
	case c.StatusCanceled:
		return c.ErrTerminal
	case c.StatusTrialing, c.StatusActive, c.StatusPastDue:
	}
	return invalid(x.s.Status, "MarkSignupFailed")
}

// clearSchedules は終端にするときに予約・R・更新日を消す（I-3）。
func (x *Subscription) clearSchedules() {
	x.s.NextRenewalDate, x.s.PendingRenewal, x.s.Cancellation, x.s.PendingPlanChange = nil, nil, nil, nil
}

// ---- 更新 ----

// BeginRenewal は date（処理日）の更新を始める。Ended なら terms は nil。
func (x *Subscription) BeginRenewal(date sk.Date, now time.Time) (ended bool, terms *c.Terms, err error) {
	switch x.s.Status {
	case c.StatusPendingInitial:
		return false, nil, c.ErrNotRenewable
	case c.StatusSignupFailed:
		return false, nil, c.ErrTerminal
	case c.StatusCanceled:
		return true, nil, nil
	case c.StatusPastDue:
		// 同じ更新の再実行なら同じ条件を返す。別の期間は D-02 により更新しない
		if r := x.s.PendingRenewal; r != nil && !date.Before(r.Period.Start) && !date.After(r.Period.End) {
			return false, x.termsOf(r), nil
		}
		return false, nil, c.ErrNotRenewable
	case c.StatusTrialing, c.StatusActive:
	}
	s := *x.s.NextRenewalDate
	if date.Before(s) {
		return false, nil, c.ErrNotDue
	}
	if r := x.s.PendingRenewal; r != nil {
		return false, x.termsOf(r), nil // R 済みなら同じ条件（冪等）
	}
	if cn := x.s.Cancellation; cn != nil && !cn.EffectiveDate.After(s) {
		x.toCanceled(cn.Reason, now)
		return true, nil, nil
	}
	r := &PendingRenewal{PlanID: x.s.PlanID, PriceVersionID: x.s.PriceVersionID, Interval: x.s.Interval, FirstCharge: x.s.Status == c.StatusTrialing}
	if pc := x.s.PendingPlanChange; pc != nil && !pc.EffectiveDate.After(s) {
		from := x.s.PlanID
		r.PlanID, r.PriceVersionID, r.Interval, r.PlanChanged, r.FromPlanID = pc.PlanID, pc.PriceVersionID, pc.Interval, true, &from
		x.s.PendingPlanChange = nil
	}
	r.Period = sk.FirstPeriod(s, r.Interval, x.s.AnchorDay)
	if d := x.s.AppliedDiscount; d != nil {
		after := *d
		if after.RemainingCycles > 0 {
			after.RemainingCycles-- // 条件を確定した時点で 1 回分を使う（R で冪等なので二重に減らない）
			td := after
			r.TermsDiscount = &td
		}
		r.Discount = &after
	}
	x.s.PendingRenewal = r
	return false, x.termsOf(r), nil
}

func (x *Subscription) termsOf(r *PendingRenewal) *c.Terms {
	return &c.Terms{
		Period: r.Period, PlanID: r.PlanID, PriceVersionID: r.PriceVersionID, Interval: r.Interval, Quantity: x.s.Quantity,
		Discount: r.TermsDiscount, PaymentMethodID: x.s.PaymentMethodID, FirstCharge: r.FirstCharge, PlanChanged: r.PlanChanged,
	}
}

// confirmR は R を確定する（ConfirmRenewal / Recover）。
func (x *Subscription) confirmR(inv sk.InvoiceID, event string) {
	r := x.s.PendingRenewal
	x.s.Status = c.StatusActive
	x.s.CurrentPeriod, x.s.PlanID, x.s.PriceVersionID, x.s.Interval = r.Period, r.PlanID, r.PriceVersionID, r.Interval
	x.s.AppliedDiscount = r.Discount
	x.s.NextRenewalDate = datePtr(r.Period.NextStart())
	x.s.LastPaidInvoiceID, x.s.PastDueInvoiceID, x.s.PendingRenewal = &inv, nil, nil
	extra := map[string]any{"invoice_id": inv}
	if event == c.EventRenewed {
		extra["first_charge"] = r.FirstCharge
	}
	x.emit(event, extra)
	if r.PlanChanged {
		x.emit(c.EventPlanChanged, map[string]any{"from_plan_id": r.FromPlanID, "to_plan_id": r.PlanID, "effective_date": r.Period.Start})
	}
}

// ConfirmRenewal は更新の請求の支払い。
func (x *Subscription) ConfirmRenewal(inv sk.InvoiceID) error {
	switch x.s.Status {
	case c.StatusTrialing:
		if x.s.PendingRenewal != nil {
			x.confirmR(inv, c.EventRenewed)
			return nil
		}
	case c.StatusActive:
		if x.s.PendingRenewal != nil {
			x.confirmR(inv, c.EventRenewed)
			return nil
		}
		if x.lastPaid(inv) {
			return nil
		}
	case c.StatusPastDue:
		return x.Recover(inv)
	case c.StatusCanceled, c.StatusSignupFailed:
		return c.ErrTerminal
	case c.StatusPendingInitial:
	}
	return invalid(x.s.Status, "ConfirmRenewal")
}

// MarkPastDue は更新の請求の決済失敗。解約予約があればその場で解約する。
func (x *Subscription) MarkPastDue(inv sk.InvoiceID, now time.Time) error {
	switch x.s.Status {
	case c.StatusTrialing, c.StatusActive:
		if x.s.PendingRenewal != nil {
			if cn := x.s.Cancellation; cn != nil {
				x.toCanceled(cn.Reason, now)
				return nil
			}
			x.s.Status, x.s.PastDueInvoiceID = c.StatusPastDue, &inv
			x.emit(c.EventPastDue, map[string]any{"invoice_id": inv})
			return nil
		}
		if x.s.Status == c.StatusActive && x.lastPaid(inv) {
			return nil // 支払い済みの後に古い失敗が届いた（X-10）
		}
	case c.StatusPastDue:
		if x.s.PastDueInvoiceID != nil && *x.s.PastDueInvoiceID == inv {
			return nil
		}
		return c.ErrInvoiceMismatch
	case c.StatusCanceled:
		return nil
	case c.StatusSignupFailed:
		return c.ErrTerminal
	case c.StatusPendingInitial:
	}
	return invalid(x.s.Status, "MarkPastDue")
}

// Recover は支払い遅延からの回収。
func (x *Subscription) Recover(inv sk.InvoiceID) error {
	switch x.s.Status {
	case c.StatusPastDue:
		if x.s.PastDueInvoiceID != nil && *x.s.PastDueInvoiceID == inv {
			x.confirmR(inv, c.EventRecovered)
			return nil
		}
		return c.ErrInvoiceMismatch
	case c.StatusActive:
		if x.lastPaid(inv) {
			return nil
		}
	case c.StatusCanceled, c.StatusSignupFailed:
		return c.ErrTerminal
	case c.StatusPendingInitial, c.StatusTrialing:
	}
	return invalid(x.s.Status, "Recover")
}

// ---- 解約 ----

func (x *Subscription) toCanceled(reason c.CancelReason, now time.Time) {
	x.s.Status, x.s.CancelReason, x.s.CanceledAt = c.StatusCanceled, &reason, &now
	x.clearSchedules()
	x.emit(c.EventCanceled, map[string]any{"reason": reason})
}

// Cancel は即時解約（回収の打ち切りなど）。
func (x *Subscription) Cancel(reason c.CancelReason, now time.Time) error {
	switch x.s.Status {
	case c.StatusTrialing, c.StatusActive, c.StatusPastDue:
		x.toCanceled(reason, now)
		return nil
	case c.StatusCanceled:
		return nil
	case c.StatusSignupFailed:
		return c.ErrTerminal
	case c.StatusPendingInitial:
	}
	return invalid(x.s.Status, "Cancel")
}

// cancellationDate は解約の適用日（§3.1「予約の適用日の決め方」）。
func (x *Subscription) cancellationDate() sk.Date {
	if r := x.s.PendingRenewal; r != nil {
		return r.Period.NextStart()
	}
	return *x.s.NextRenewalDate // 更新日の当日（日次処理の前）でも、その日の更新で解約する（P-SUB-4）
}

// planChangeDate はプラン変更の適用日。
func (x *Subscription) planChangeDate(today sk.Date) sk.Date {
	if r := x.s.PendingRenewal; r != nil {
		return r.Period.NextStart()
	}
	next := *x.s.NextRenewalDate
	if today.Before(next) {
		return next
	}
	return sk.FirstPeriod(next, x.s.Interval, x.s.AnchorDay).NextStart() // 「前日まで」に間に合わないので次の次（FR-015）
}

func (x *Subscription) revokePlanChange() {
	if x.s.PendingPlanChange != nil {
		x.s.PendingPlanChange = nil
		x.emit(c.EventPlanChangeRevoked, nil)
	}
}

func (x *Subscription) schedule(src c.CancellationSource, reason c.CancelReason) sk.Date {
	x.revokePlanChange()
	eff := x.cancellationDate()
	x.s.Cancellation = &c.Cancellation{Source: src, Reason: reason, EffectiveDate: eff}
	x.emit(c.EventCancellationScheduled, map[string]any{"reason": reason, "effective_date": eff})
	return eff
}

// ScheduleCancellation は契約者の解約予約。支払い遅延中は即時解約（D-03）。
func (x *Subscription) ScheduleCancellation(now time.Time) error {
	switch x.s.Status {
	case c.StatusTrialing, c.StatusActive:
		if x.s.Cancellation != nil {
			return nil
		}
		x.schedule(c.SourceCustomer, c.CancelByCustomer)
		return nil
	case c.StatusPastDue:
		x.toCanceled(c.CancelByCustomer, now)
		return nil
	case c.StatusCanceled, c.StatusSignupFailed:
		return c.ErrTerminal
	case c.StatusPendingInitial:
	}
	return c.ErrStateConflict
}

// SystemCancellation は ScheduleSystemCancellation の結果。
type SystemCancellation struct {
	Kind          c.SystemCancellationKind
	EffectiveDate *sk.Date
}

// ScheduleSystemCancellation は事業者停止に伴う解約（D-01）。支払い遅延中は即時、それ以外は期間の終わりに解約する。
func (x *Subscription) ScheduleSystemCancellation(reason c.CancelReason, now time.Time) SystemCancellation {
	switch x.s.Status {
	case c.StatusPendingInitial, c.StatusTrialing, c.StatusActive:
		if cn := x.s.Cancellation; cn != nil && cn.Source == c.SourceSystem {
			return SystemCancellation{Kind: c.SystemCancellationNoop, EffectiveDate: datePtr(cn.EffectiveDate)}
		}
		eff := x.schedule(c.SourceSystem, reason)
		return SystemCancellation{Kind: c.SystemCancellationScheduled, EffectiveDate: &eff}
	case c.StatusPastDue:
		x.toCanceled(reason, now)
		return SystemCancellation{Kind: c.SystemCanceledImmediately}
	case c.StatusCanceled, c.StatusSignupFailed:
	}
	return SystemCancellation{Kind: c.SystemCancellationNoop}
}

// RevokeCancellation は契約者による解約予約の取り消し。システムの予約は取り消せない。
func (x *Subscription) RevokeCancellation() error {
	switch x.s.Status {
	case c.StatusTrialing, c.StatusActive:
		cn := x.s.Cancellation
		if cn == nil {
			return nil
		}
		if cn.Source == c.SourceSystem {
			return c.ErrCancellationNotRevocable
		}
		x.s.Cancellation = nil
		x.emit(c.EventCancellationRevoked, nil)
		return nil
	case c.StatusPastDue:
		return nil
	case c.StatusCanceled, c.StatusSignupFailed:
		return c.ErrTerminal
	case c.StatusPendingInitial:
	}
	return c.ErrStateConflict
}

// ---- プラン変更 ----

// PlanTarget は変更先のプラン（catalog の GetPlanSnapshot から作る）。
type PlanTarget struct {
	PlanID         sk.PlanID
	ProductID      sk.ProductID
	PriceVersionID sk.PriceVersionID
	Interval       sk.BillingInterval
}

// SchedulePlanChange は契約者のプラン変更の予約（次の期間から）。同じ内容の再実行は ○。
func (x *Subscription) SchedulePlanChange(t PlanTarget, today sk.Date) error {
	switch x.s.Status {
	case c.StatusTrialing, c.StatusActive:
	case c.StatusCanceled, c.StatusSignupFailed:
		return c.ErrTerminal
	case c.StatusPendingInitial, c.StatusPastDue:
		return c.ErrStateConflict
	}
	if x.s.Cancellation != nil {
		return c.ErrCancellationScheduled
	}
	if t.ProductID != x.s.ProductID {
		return &c.PlanChangeInvalidError{Reason: "different_product"}
	}
	if t.PlanID == x.s.PlanID {
		return &c.PlanChangeInvalidError{Reason: "same_plan"}
	}
	if pc := x.s.PendingPlanChange; pc != nil && pc.PlanID == t.PlanID {
		return nil
	}
	eff := x.planChangeDate(today)
	x.s.PendingPlanChange = &c.PendingPlanChange{PlanID: t.PlanID, PriceVersionID: t.PriceVersionID, Interval: t.Interval, EffectiveDate: eff}
	x.emit(c.EventPlanChangeScheduled, map[string]any{"from_plan_id": x.s.PlanID, "to_plan_id": t.PlanID, "effective_date": eff})
	return nil
}

// RevokePlanChange はプラン変更の予約の取り消し。予約がなければ ○。
func (x *Subscription) RevokePlanChange() error {
	if x.s.Status.Terminal() {
		return c.ErrTerminal
	}
	x.revokePlanChange()
	return nil
}

// ---- 支払方法・トライアル終了の予告 ----

// ChangePaymentMethod は支払方法の付け替え。支払い遅延中は同じ支払方法でもイベントを出す（即時の再決済のきっかけ。FR-017）。
func (x *Subscription) ChangePaymentMethod(pm sk.PaymentMethodID) error {
	switch x.s.Status {
	case c.StatusTrialing, c.StatusActive:
		if x.s.PaymentMethodID == pm {
			return nil
		}
	case c.StatusPastDue:
	case c.StatusCanceled, c.StatusSignupFailed:
		return c.ErrTerminal
	case c.StatusPendingInitial:
		return c.ErrStateConflict
	}
	x.s.PaymentMethodID = pm
	// status は変更時点の契約の状態（Orchestration が past_due なら即時の再決済をする。orchestration.md §4.4・§4.10）
	x.emit(c.EventPaymentMethodChanged, map[string]any{"payment_method_id": pm, "status": x.s.Status})
	return nil
}

// NotifyTrialWillEnd は trial.End = date+3 かつ未通知なら通知済みにする（1 回だけ）。
func (x *Subscription) NotifyTrialWillEnd(date sk.Date) bool {
	if x.s.Status != c.StatusTrialing || x.s.TrialWillEndNotified || x.s.TrialEnd == nil || !x.s.TrialEnd.Equal(date.AddDays(3)) {
		return false
	}
	x.s.TrialWillEndNotified = true
	x.emit(c.EventTrialWillEnd, map[string]any{"trial_end": *x.s.TrialEnd})
	return true
}

// ---- 参照 ----

// View は contracts の形。
func (x *Subscription) View() c.SubscriptionView {
	s := x.s
	v := c.SubscriptionView{
		ID: s.ID, TenantID: s.TenantID, CustomerAccountID: s.CustomerAccountID, TenantCustomerID: s.TenantCustomerID,
		ProductID: s.ProductID, PlanID: s.PlanID, PriceVersionID: s.PriceVersionID, Interval: s.Interval, Quantity: s.Quantity,
		Status: s.Status, SignupDate: s.SignupDate, CurrentPeriod: s.CurrentPeriod, NextRenewalDate: s.NextRenewalDate,
		TrialStart: s.TrialStart, TrialEnd: s.TrialEnd, Cancellation: s.Cancellation, PendingPlanChange: s.PendingPlanChange,
		RenewalInProgress: s.PendingRenewal != nil, Entitled: s.Status.Entitled(), AppliedDiscount: s.AppliedDiscount,
		PaymentMethodID: s.PaymentMethodID, CancelReason: s.CancelReason, CanceledAt: s.CanceledAt, InitialTerms: x.InitialTerms(),
		Version: s.Version, CreatedAt: s.CreatedAt,
	}
	return v
}
