package domain_test

import (
	"errors"
	"testing"
	"time"

	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/subscription/contracts"
	"subscription-platform/modules/subscription/internal/domain"
)

var (
	now     = time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	product = sk.NewID[sk.ProductTag]()
	planA   = sk.NewID[sk.PlanTag]()
	planB   = sk.NewID[sk.PlanTag]()
)

func d(s string) sk.Date {
	v, err := sk.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

type opt func(*domain.SignupInput)

func withTrial(days int) opt {
	return func(in *domain.SignupInput) { in.Plan.TrialDays, in.ApplyTrial = days, true }
}
func yearly() opt { return func(in *domain.SignupInput) { in.Plan.Interval = sk.IntervalYear } }
func withDiscount(cycles int) opt {
	return func(in *domain.SignupInput) {
		in.Discount = &c.DiscountInput{CouponID: sk.NewID[sk.CouponTag](), RedemptionID: sk.NewID[sk.RedemptionTag](), Type: sk.DiscountPercent, Value: 50, DurationCycles: cycles}
	}
}

func signup(date string, opts ...opt) *domain.Subscription {
	in := domain.SignupInput{
		ID: sk.NewID[sk.SubscriptionTag](), TenantID: sk.NewID[sk.TenantTag](), CustomerAccountID: sk.NewID[sk.CustomerAccountTag](),
		TenantCustomerID: "cus_x", SignupRequestID: "req", PaymentMethodID: sk.NewID[sk.PaymentMethodTag](), SignupDate: d(date), Now: now,
		Plan: c.PlanInput{PlanID: planA, ProductID: product, PriceVersionID: sk.NewID[sk.PriceVersionTag](), Interval: sk.IntervalMonth},
	}
	for _, o := range opts {
		o(&in)
	}
	return domain.NewForSignup(in)
}

// active は申込して初回決済まで済ませた契約。
func active(date string, opts ...opt) *domain.Subscription {
	x := signup(date, opts...)
	if err := x.Activate(sk.NewID[sk.InvoiceTag]()); err != nil {
		panic(err)
	}
	x.Events()
	return x
}

func types(evs []domain.Event) []string {
	out := []string{}
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func eqTypes(t *testing.T, got []domain.Event, want ...string) {
	t.Helper()
	g := types(got)
	if len(g) != len(want) {
		t.Fatalf("events = %v, want %v", g, want)
	}
	for i := range g {
		if g[i] != want[i] {
			t.Fatalf("events = %v, want %v", g, want)
		}
	}
}

// renew は BeginRenewal → ConfirmRenewal を 1 回行う。
func renew(t *testing.T, x *domain.Subscription, date sk.Date) *c.Terms {
	t.Helper()
	ended, terms, err := x.BeginRenewal(date, now)
	if err != nil || ended {
		t.Fatalf("BeginRenewal(%s): ended=%v err=%v", date, ended, err)
	}
	if err := x.ConfirmRenewal(sk.NewID[sk.InvoiceTag]()); err != nil {
		t.Fatal(err)
	}
	return terms
}

// AC-SUB-001・002
func TestSignupWithoutTrial(t *testing.T) {
	x := signup("2026-10-15")
	st := x.State()
	if st.Status != c.StatusPendingInitial || st.CurrentPeriod.String() != "2026-10-15..2026-11-14" || st.NextRenewalDate.String() != "2026-11-15" {
		t.Fatalf("%+v", st)
	}
	if it := x.InitialTerms(); it == nil || it.Period != st.CurrentPeriod || it.FirstCharge {
		t.Errorf("initial terms: %+v", it)
	}
	eqTypes(t, x.Events())
	inv := sk.NewID[sk.InvoiceTag]()
	if err := x.Activate(inv); err != nil {
		t.Fatal(err)
	}
	eqTypes(t, x.Events(), c.EventStarted)
	if !x.View().Entitled || x.State().Status != c.StatusActive {
		t.Error("active・entitled になっていない")
	}
	if err := x.Activate(inv); err != nil {
		t.Errorf("同じ請求の再実行: %v", err)
	}
	eqTypes(t, x.Events())
	if err := x.Activate(sk.NewID[sk.InvoiceTag]()); !errors.Is(err, c.ErrInvalidTransitionKind) {
		t.Errorf("別の請求: %v", err)
	}
}

// AC-SUB-003
func TestSignupFailed(t *testing.T) {
	x := signup("2026-10-15")
	if err := x.MarkSignupFailed("payment_declined"); err != nil {
		t.Fatal(err)
	}
	eqTypes(t, x.Events(), c.EventSignupFailed)
	if st := x.State(); st.Status != c.StatusSignupFailed || st.NextRenewalDate != nil {
		t.Errorf("%+v", st)
	}
	if err := x.MarkSignupFailed("x"); err != nil {
		t.Errorf("再実行: %v", err)
	}
	if err := x.Activate(sk.NewID[sk.InvoiceTag]()); !errors.Is(err, c.ErrTerminal) {
		t.Errorf("終端の Activate: %v", err)
	}
}

func renewalDates(t *testing.T, start string, n int, opts ...opt) []string {
	x := active(start, opts...)
	var out []string
	for range n {
		next := *x.State().NextRenewalDate
		renew(t, x, next)
		out = append(out, x.State().CurrentPeriod.Start.String())
	}
	return out
}

func eqStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// AC-SUB-005・006・007: 月末・うるう年の更新日（D-11）
func TestRenewalDates(t *testing.T) {
	eqStrings(t, renewalDates(t, "2027-01-31", 4), []string{"2027-02-28", "2027-03-31", "2027-04-30", "2027-05-31"})
	eqStrings(t, renewalDates(t, "2028-02-29", 13), []string{
		"2028-03-29", "2028-04-29", "2028-05-29", "2028-06-29", "2028-07-29", "2028-08-29", "2028-09-29",
		"2028-10-29", "2028-11-29", "2028-12-29", "2029-01-29", "2029-02-28", "2029-03-29",
	})
	eqStrings(t, renewalDates(t, "2028-02-29", 4, yearly()), []string{"2029-02-28", "2030-02-28", "2031-02-28", "2032-02-29"})
	if p := signup("2028-02-29", yearly()).State().CurrentPeriod.String(); p != "2028-02-29..2029-02-27" {
		t.Errorf("最初の期間 = %s", p)
	}
}

// AC-SUB-008・009: トライアル（D-12）
func TestTrial(t *testing.T) {
	x := signup("2027-01-17", withTrial(14))
	st := x.State()
	if st.Status != c.StatusTrialing || st.TrialStart.String() != "2027-01-17" || st.TrialEnd.String() != "2027-01-30" || st.NextRenewalDate.String() != "2027-01-31" || st.AnchorDay != 31 {
		t.Fatalf("%+v", st)
	}
	eqTypes(t, x.Events(), c.EventStarted)
	terms := renew(t, x, d("2027-01-31"))
	if !terms.FirstCharge || terms.Period.String() != "2027-01-31..2027-02-27" {
		t.Errorf("terms: %+v", terms)
	}
	evs := x.Events()
	eqTypes(t, evs, c.EventRenewed)
	if evs[0].Extra["first_charge"] != true {
		t.Error("first_charge が true でない")
	}
	if st := x.State(); st.Status != c.StatusActive || st.NextRenewalDate.String() != "2027-02-28" {
		t.Errorf("%+v", st)
	}
	renew(t, x, d("2027-02-28"))
	if p := x.State().CurrentPeriod.String(); p != "2027-02-28..2027-03-30" {
		t.Errorf("2 回目 = %s", p)
	}
}

// AC-SUB-013: トライアル中の解約は初回課金日に Ended
func TestTrialCancellation(t *testing.T) {
	x := signup("2026-10-18", withTrial(14)) // trial 10-18〜10-31
	if err := x.ScheduleCancellation(now); err != nil {
		t.Fatal(err)
	}
	if eff := x.State().Cancellation.EffectiveDate.String(); eff != "2026-11-01" {
		t.Errorf("effective = %s", eff)
	}
	x.Events()
	ended, terms, err := x.BeginRenewal(d("2026-11-01"), now)
	if err != nil || !ended || terms != nil {
		t.Fatalf("ended=%v terms=%v err=%v", ended, terms, err)
	}
	if st := x.State(); st.Status != c.StatusCanceled || *st.CancelReason != c.CancelByCustomer {
		t.Errorf("%+v", st)
	}
	eqTypes(t, x.Events(), c.EventCanceled)
}

// AC-SUB-014・015
func TestCancellationAndRevoke(t *testing.T) {
	x := active("2026-10-15")
	if err := x.ScheduleCancellation(now); err != nil {
		t.Fatal(err)
	}
	if !x.View().Entitled || x.State().Cancellation.EffectiveDate.String() != "2026-11-15" {
		t.Errorf("%+v", x.State().Cancellation)
	}
	evs := x.Events()
	eqTypes(t, evs, c.EventCancellationScheduled)
	if evs[0].Extra["reason"] != c.CancelByCustomer {
		t.Errorf("reason = %v", evs[0].Extra["reason"])
	}
	if err := x.ScheduleCancellation(now); err != nil {
		t.Fatal(err)
	}
	eqTypes(t, x.Events()) // 予約済みは ○
	if err := x.RevokeCancellation(); err != nil {
		t.Fatal(err)
	}
	eqTypes(t, x.Events(), c.EventCancellationRevoked)
	if ended, terms, err := x.BeginRenewal(d("2026-11-15"), now); ended || terms == nil || err != nil {
		t.Errorf("取り消し後は更新される: %v %v %v", ended, terms, err)
	}

	y := active("2026-10-15")
	_ = y.ScheduleCancellation(now)
	if ended, _, _ := y.BeginRenewal(d("2026-11-15"), now); !ended {
		t.Error("予約した解約が適用されない")
	}
}

// AC-SUB-017・018・019・020・021
func TestPlanChange(t *testing.T) {
	x := active("2026-10-15")
	target := domain.PlanTarget{PlanID: planB, ProductID: product, PriceVersionID: sk.NewID[sk.PriceVersionTag](), Interval: sk.IntervalMonth}
	if err := x.SchedulePlanChange(target, d("2026-10-20")); err != nil {
		t.Fatal(err)
	}
	if x.State().PlanID != planA || x.State().PendingPlanChange.EffectiveDate.String() != "2026-11-15" {
		t.Errorf("%+v", x.State().PendingPlanChange)
	}
	eqTypes(t, x.Events(), c.EventPlanChangeScheduled)
	if err := x.SchedulePlanChange(target, d("2026-10-21")); err != nil {
		t.Fatal(err)
	}
	eqTypes(t, x.Events()) // 同じ内容は ○
	terms := renew(t, x, d("2026-11-15"))
	if terms.PlanID != planB || terms.PriceVersionID != target.PriceVersionID || !terms.PlanChanged {
		t.Errorf("terms: %+v", terms)
	}
	eqTypes(t, x.Events(), c.EventRenewed, c.EventPlanChanged)
	if x.State().PlanID != planB || x.State().PendingPlanChange != nil {
		t.Error("プランが変わっていない")
	}

	// AC-SUB-018: プラン変更予約中に解約予約 → 予約は消える
	y := active("2026-10-15")
	_ = y.SchedulePlanChange(target, d("2026-10-20"))
	y.Events()
	_ = y.ScheduleCancellation(now)
	eqTypes(t, y.Events(), c.EventPlanChangeRevoked, c.EventCancellationScheduled)
	if y.State().PendingPlanChange != nil {
		t.Error("プラン変更予約が残っている")
	}
	// AC-SUB-020
	if err := y.SchedulePlanChange(target, d("2026-10-21")); !errors.Is(err, c.ErrCancellationScheduled) {
		t.Errorf("解約予約中: %v", err)
	}

	// AC-SUB-019: R 済みに予約すると R の期間の次から
	z := active("2026-10-15")
	_, rTerms, _ := z.BeginRenewal(d("2026-11-15"), now)
	if err := z.SchedulePlanChange(target, d("2026-11-15")); err != nil {
		t.Fatal(err)
	}
	if eff := z.State().PendingPlanChange.EffectiveDate.String(); eff != "2026-12-15" {
		t.Errorf("effective = %s", eff)
	}
	if _, again, _ := z.BeginRenewal(d("2026-11-15"), now); again.PlanID != rTerms.PlanID {
		t.Error("R の Terms が変わった")
	}

	// 別の商品・同じプラン
	w := active("2026-10-15")
	if err := w.SchedulePlanChange(domain.PlanTarget{PlanID: planB, ProductID: sk.NewID[sk.ProductTag]()}, d("2026-10-20")); !errors.Is(err, c.ErrPlanChangeInvalidKind) {
		t.Errorf("別の商品: %v", err)
	}
	if err := w.SchedulePlanChange(domain.PlanTarget{PlanID: planA, ProductID: product}, d("2026-10-20")); err == nil || err.Error() != "subscription: plan change not allowed: same_plan" {
		t.Errorf("同じプラン: %v", err)
	}

	// AC-SUB-021: 月額（anchor 31）を年額に変える
	m := active("2027-01-31")
	renew(t, m, d("2027-02-28"))
	renew(t, m, d("2027-03-31")) // 次は 04-30
	yearTarget := domain.PlanTarget{PlanID: planB, ProductID: product, PriceVersionID: sk.NewID[sk.PriceVersionTag](), Interval: sk.IntervalYear}
	if err := m.SchedulePlanChange(yearTarget, d("2027-04-01")); err != nil {
		t.Fatal(err)
	}
	if p := renew(t, m, d("2027-04-30")).Period.String(); p != "2027-04-30..2028-04-29" {
		t.Errorf("年額の期間 = %s", p)
	}
}

// AC-SUB-040 のドメイン部分：更新日の当日（日次処理の前）の予約
func TestRenewalDayReservation(t *testing.T) {
	x := active("2026-10-15")
	_ = x.ScheduleCancellation(now)
	if eff := x.State().Cancellation.EffectiveDate.String(); eff != "2026-11-15" {
		t.Errorf("解約の適用日 = %s", eff)
	}
	y := active("2026-10-15")
	_ = y.SchedulePlanChange(domain.PlanTarget{PlanID: planB, ProductID: product, Interval: sk.IntervalMonth}, d("2026-11-15"))
	if eff := y.State().PendingPlanChange.EffectiveDate.String(); eff != "2026-12-15" {
		t.Errorf("プラン変更の適用日 = %s", eff)
	}
	if terms := renew(t, y, d("2026-11-15")); terms.PlanID != planA {
		t.Error("11-15 は元のプランで更新する")
	}
}

// AC-SUB-023: 初回のみの割引
func TestDiscountCycles(t *testing.T) {
	x := signup("2026-10-01", withTrial(14), withDiscount(1))
	if x.State().AppliedDiscount.RemainingCycles != 1 {
		t.Fatal("トライアル中は割引を使わない")
	}
	ended, t1, err := x.BeginRenewal(d("2026-10-15"), now)
	if ended || err != nil || t1.Discount == nil {
		t.Fatalf("1 回目: %+v %v", t1, err)
	}
	_, again, _ := x.BeginRenewal(d("2026-10-15"), now)
	if again.Discount == nil {
		t.Error("同じ日の再実行で割引が消えた")
	}
	_ = x.ConfirmRenewal(sk.NewID[sk.InvoiceTag]())
	if x.State().AppliedDiscount.RemainingCycles != 0 {
		t.Errorf("残り = %d", x.State().AppliedDiscount.RemainingCycles)
	}
	if t2 := renew(t, x, d("2026-11-15")); t2.Discount != nil {
		t.Error("2 回目に割引が付いた")
	}
	// トライアルなし・1 回：初回で使い切る
	y := signup("2026-10-01", withDiscount(1))
	if it := y.InitialTerms(); it.Discount == nil || y.State().AppliedDiscount.RemainingCycles != 0 {
		t.Error("初回の請求に割引が付かない")
	}
}

// AC-SUB-024・025・026・027
func TestPastDue(t *testing.T) {
	x := active("2026-10-15")
	_, _, _ = x.BeginRenewal(d("2026-11-15"), now)
	inv := sk.NewID[sk.InvoiceTag]()
	if err := x.MarkPastDue(inv, now); err != nil {
		t.Fatal(err)
	}
	eqTypes(t, x.Events(), c.EventPastDue)
	if !x.View().Entitled || x.State().Status != c.StatusPastDue {
		t.Error("past_due・entitled でない")
	}
	if err := x.MarkPastDue(inv, now); err != nil {
		t.Errorf("再実行: %v", err)
	}
	if err := x.MarkPastDue(sk.NewID[sk.InvoiceTag](), now); !errors.Is(err, c.ErrInvoiceMismatch) {
		t.Errorf("別の請求: %v", err)
	}
	if _, _, err := x.BeginRenewal(d("2026-12-15"), now); !errors.Is(err, c.ErrNotRenewable) {
		t.Errorf("別の期間の更新: %v", err)
	}
	if _, terms, err := x.BeginRenewal(d("2026-11-15"), now); err != nil || terms == nil {
		t.Errorf("同じ更新の再実行: %v", err)
	}
	if err := x.Recover(inv); err != nil {
		t.Fatal(err)
	}
	eqTypes(t, x.Events(), c.EventRecovered)
	if st := x.State(); st.Status != c.StatusActive || st.CurrentPeriod.String() != "2026-11-15..2026-12-14" || st.NextRenewalDate.String() != "2026-12-15" {
		t.Errorf("%+v", st)
	}

	y := active("2026-10-15")
	_, _, _ = y.BeginRenewal(d("2026-11-15"), now)
	_ = y.MarkPastDue(sk.NewID[sk.InvoiceTag](), now)
	y.Events()
	if err := y.Cancel(c.CancelByDunning, now); err != nil {
		t.Fatal(err)
	}
	if y.View().Entitled || *y.State().CancelReason != c.CancelByDunning {
		t.Error("dunning で解約されていない")
	}
	eqTypes(t, y.Events(), c.EventCanceled)
	if err := y.Recover(sk.NewID[sk.InvoiceTag]()); !errors.Is(err, c.ErrTerminal) {
		t.Errorf("解約後の回収: %v", err)
	}
}

// AC-SUB-028・030
func TestSystemCancellation(t *testing.T) {
	x := active("2026-10-15")
	_ = x.ScheduleCancellation(now)
	x.Events()
	res := x.ScheduleSystemCancellation(c.CancelByTenantSuspended, now)
	if res.Kind != c.SystemCancellationScheduled || res.EffectiveDate.String() != "2026-11-15" || x.State().Cancellation.Source != c.SourceSystem {
		t.Errorf("%+v %+v", res, x.State().Cancellation)
	}
	evs := x.Events()
	eqTypes(t, evs, c.EventCancellationScheduled)
	if evs[0].Extra["reason"] != c.CancelByTenantSuspended {
		t.Errorf("reason = %v", evs[0].Extra["reason"])
	}
	if err := x.RevokeCancellation(); !errors.Is(err, c.ErrCancellationNotRevocable) {
		t.Errorf("システムの予約の取り消し: %v", err)
	}
	if again := x.ScheduleSystemCancellation(c.CancelByTenantSuspended, now); again.Kind != c.SystemCancellationNoop {
		t.Errorf("再実行: %v", again.Kind)
	}

	p := active("2026-10-15")
	_, _, _ = p.BeginRenewal(d("2026-11-15"), now)
	_ = p.MarkPastDue(sk.NewID[sk.InvoiceTag](), now)
	if res := p.ScheduleSystemCancellation(c.CancelByTenantSuspended, now); res.Kind != c.SystemCanceledImmediately || *p.State().CancelReason != c.CancelByTenantSuspended {
		t.Errorf("past_due: %+v", res)
	}

	// AC-SUB-030: R あり・システム予約 → 決済失敗なら即時解約、成功なら新しい期間の終わりに解約
	r1 := active("2026-10-15")
	_, _, _ = r1.BeginRenewal(d("2026-11-15"), now)
	r1.ScheduleSystemCancellation(c.CancelByTenantSuspended, now)
	_ = r1.MarkPastDue(sk.NewID[sk.InvoiceTag](), now)
	if st := r1.State(); st.Status != c.StatusCanceled || *st.CancelReason != c.CancelByTenantSuspended {
		t.Errorf("R 失敗: %+v", st)
	}
	r2 := active("2026-10-15")
	_, _, _ = r2.BeginRenewal(d("2026-11-15"), now)
	r2.ScheduleSystemCancellation(c.CancelByTenantSuspended, now)
	_ = r2.ConfirmRenewal(sk.NewID[sk.InvoiceTag]())
	if st := r2.State(); st.Status != c.StatusActive || st.Cancellation.EffectiveDate.String() != "2026-12-15" {
		t.Errorf("R 成功: %+v", st)
	}
	// X-17: pending_initial は予約だけ付ける
	pi := signup("2026-10-15")
	if res := pi.ScheduleSystemCancellation(c.CancelByTenantSuspended, now); res.Kind != c.SystemCancellationScheduled || pi.State().Status != c.StatusPendingInitial {
		t.Errorf("pending_initial: %+v", res)
	}
}

// AC-SUB-031
func TestChangePaymentMethod(t *testing.T) {
	x := active("2026-10-15")
	pm := x.State().PaymentMethodID
	if err := x.ChangePaymentMethod(pm); err != nil {
		t.Fatal(err)
	}
	eqTypes(t, x.Events())
	_, _, _ = x.BeginRenewal(d("2026-11-15"), now)
	_ = x.MarkPastDue(sk.NewID[sk.InvoiceTag](), now)
	x.Events()
	if err := x.ChangePaymentMethod(pm); err != nil {
		t.Fatal(err)
	}
	eqTypes(t, x.Events(), c.EventPaymentMethodChanged)
	if err := signup("2026-10-15").ChangePaymentMethod(pm); !errors.Is(err, c.ErrStateConflict) {
		t.Errorf("pending_initial: %v", err)
	}
}

// AC-SUB-038・I-8
func TestOpenAndEntitled(t *testing.T) {
	for st, want := range map[c.Status][2]bool{
		c.StatusPendingInitial: {true, false}, c.StatusTrialing: {true, true}, c.StatusActive: {true, true},
		c.StatusPastDue: {true, true}, c.StatusCanceled: {false, false}, c.StatusSignupFailed: {false, false},
	} {
		if st.Open() != want[0] || st.Entitled() != want[1] {
			t.Errorf("%s: open=%v entitled=%v", st, st.Open(), st.Entitled())
		}
	}
}

// 状態遷移の表のうち、エラーになる組み合わせ
func TestInvalidTransitions(t *testing.T) {
	pi := signup("2026-10-15")
	if _, _, err := pi.BeginRenewal(d("2026-11-15"), now); !errors.Is(err, c.ErrNotRenewable) {
		t.Errorf("pending_initial の更新: %v", err)
	}
	if err := pi.ScheduleCancellation(now); !errors.Is(err, c.ErrStateConflict) {
		t.Errorf("pending_initial の解約予約: %v", err)
	}
	if err := pi.Cancel(c.CancelByCustomer, now); !errors.Is(err, c.ErrInvalidTransitionKind) {
		t.Errorf("pending_initial の Cancel: %v", err)
	}
	a := active("2026-10-15")
	if _, _, err := a.BeginRenewal(d("2026-11-14"), now); !errors.Is(err, c.ErrNotDue) {
		t.Errorf("更新日前: %v", err)
	}
	if err := a.ConfirmRenewal(sk.NewID[sk.InvoiceTag]()); !errors.Is(err, c.ErrInvalidTransitionKind) {
		t.Errorf("R なしの確定: %v", err)
	}
	_ = a.Cancel(c.CancelByCustomer, now)
	if err := a.Cancel(c.CancelByCustomer, now); err != nil {
		t.Errorf("canceled の Cancel: %v", err)
	}
	if ended, _, err := a.BeginRenewal(d("2026-11-15"), now); !ended || err != nil {
		t.Errorf("canceled の更新は Ended: %v", err)
	}
	if err := a.SchedulePlanChange(domain.PlanTarget{}, d("2026-11-01")); !errors.Is(err, c.ErrTerminal) {
		t.Errorf("終端のプラン変更: %v", err)
	}
}

// NotifyTrialWillEnd（3 日前に 1 回だけ）
func TestTrialWillEnd(t *testing.T) {
	x := signup("2026-10-21", withTrial(14)) // trial_end 11-03
	if x.NotifyTrialWillEnd(d("2026-10-30")) {
		t.Error("4 日前に通知した")
	}
	x.Events()
	if !x.NotifyTrialWillEnd(d("2026-10-31")) || x.NotifyTrialWillEnd(d("2026-10-31")) {
		t.Error("3 日前に 1 回だけ通知する")
	}
	eqTypes(t, x.Events(), c.EventTrialWillEnd)
}
