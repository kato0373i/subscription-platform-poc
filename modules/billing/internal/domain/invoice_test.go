package domain_test

import (
	"errors"
	"testing"
	"time"

	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/billing/contracts"
	"subscription-platform/modules/billing/internal/domain"
)

var (
	rate10 = func() sk.TaxRate { r, _ := sk.NewTaxRate(10, 100); return r }()
	lease  = 6 * time.Hour
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, sk.JST)
	if err != nil {
		panic(err)
	}
	return t
}

func cmd(amount int64, d *c.AppliedDiscount, mode c.CollectionMode) c.IssueInvoiceCmd {
	return c.IssueInvoiceCmd{
		TenantID: sk.NewID[sk.TenantTag](), SubscriptionID: sk.NewID[sk.SubscriptionTag](), CustomerAccountID: sk.NewID[sk.CustomerAccountTag](),
		Period:   sk.BillingPeriod{Start: sk.MustDate(2026, 11, 15), End: sk.MustDate(2026, 12, 14)},
		PlanLine: c.PlanLine{PriceVersionID: sk.NewID[sk.PriceVersionTag](), Amount: sk.MustMoney(amount), TaxRate: rate10},
		Discount: d, Kind: c.KindRenewal, CollectionMode: mode,
	}
}

func issue(t *testing.T, cm c.IssueInvoiceCmd, now time.Time) *domain.Invoice {
	t.Helper()
	x, err := domain.NewInvoice(sk.NewID[sk.InvoiceTag](), cm, now, lease)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func types(ev []domain.Event) []string {
	out := make([]string, len(ev))
	for i, e := range ev {
		out[i] = e.Type
	}
	return out
}

func eq[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
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

// AC-BIL-001〜004：§11 の計算例。
func TestAmountsAndTax(t *testing.T) {
	pct := func(v int64) *c.AppliedDiscount { return &c.AppliedDiscount{Type: sk.DiscountPercent, Value: v} }
	amt := func(v int64) *c.AppliedDiscount { return &c.AppliedDiscount{Type: sk.DiscountAmount, Value: v} }
	cases := []struct {
		name                 string
		plan                 int64
		d                    *c.AppliedDiscount
		discount, total, tax int64
	}{
		{"a", 980, nil, 0, 980, 89}, {"b", 980, pct(50), 490, 490, 44}, {"c", 980, pct(15), 147, 833, 75}, {"d", 999, pct(33), 329, 670, 60},
		{"e", 1000, amt(300), 300, 700, 63}, {"f", 300, amt(500), 300, 0, 0}, {"g", 1_000_000, nil, 0, 1_000_000, 90_909}, {"h", 1, pct(50), 0, 1, 0},
	}
	now := at("2026-11-15 10:00")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := issue(t, cmd(tc.plan, tc.d, c.ModeScheduled), now)
			s := x.State()
			eq(t, "discount", s.DiscountTotal.Int64(), tc.discount)
			eq(t, "total", s.Total.Int64(), tc.total)
			eq(t, "tax", s.Tax[0].TaxAmount.Int64(), tc.tax)
			wantLines := 1
			if tc.d != nil {
				wantLines = 2
			}
			eq(t, "lines", len(s.Lines), wantLines)
			if tc.total == 0 { // AC-BIL-004
				eq(t, "status", s.Status, c.StatusPaid)
				eq(t, "paid_via", *s.PaidVia, "zero_amount")
				eqTypes(t, x.Events(), c.EventInvoiceIssued, c.EventInvoicePaid)
			}
		})
	}
}

// AC-BIL-005・§9-13。
func TestIssueValidation(t *testing.T) {
	now := at("2026-11-15 10:00")
	bad := []c.IssueInvoiceCmd{
		cmd(980, &c.AppliedDiscount{Type: sk.DiscountPercent, Value: 0}, c.ModeScheduled),
		cmd(980, &c.AppliedDiscount{Type: sk.DiscountPercent, Value: 101}, c.ModeScheduled),
		cmd(0, nil, c.ModeScheduled), cmd(1_000_001, nil, c.ModeScheduled),
	}
	p := cmd(980, nil, c.ModeScheduled)
	p.Period.End = sk.MustDate(2026, 11, 14)
	bad = append(bad, p)
	for i, cm := range bad {
		if _, err := domain.NewInvoice(sk.NewID[sk.InvoiceTag](), cm, now, lease); !errors.Is(err, c.ErrValidation) {
			t.Errorf("%d: err = %v", i, err)
		}
	}
}

// AC-BIL-007：同じキーで内容が違う。
func TestSameRequest(t *testing.T) {
	cm := cmd(980, nil, c.ModeScheduled)
	x := issue(t, cm, at("2026-11-15 10:00"))
	eq(t, "same", x.SameRequest(cm), true)
	diff := cm
	diff.PlanLine.Amount = sk.MustMoney(990)
	eq(t, "diff amount", x.SameRequest(diff), false)
	diff = cm
	diff.Discount = &c.AppliedDiscount{Type: sk.DiscountPercent, Value: 10}
	eq(t, "diff discount", x.SameRequest(diff), false)
}

// AC-BIL-010・011。
func TestIssueModes(t *testing.T) {
	now := at("2026-11-15 10:00")
	x := issue(t, cmd(980, nil, c.ModeScheduled), now)
	ev := x.Events()
	eqTypes(t, ev, c.EventInvoiceIssued, c.EventInvoiceCollectionDue)
	eq(t, "seq", ev[1].Payload["collection_seq"], any(1))
	eq(t, "stage", ev[1].Payload["dunning_stage"], any(0))
	eq(t, "reason", ev[1].Payload["reason"], any(c.ReasonInitial))
	eq(t, "status", x.State().Status, c.StatusOpen)

	z := issue(t, cmd(300, &c.AppliedDiscount{Type: sk.DiscountAmount, Value: 500}, c.ModeCallerManaged), now)
	eq(t, "zero caller_managed status", z.State().Status, c.StatusPaid)
	eq(t, "zero caller_managed seq", z.State().CollectionSeq, 1) // invoices_caller_managed の CHECK

	y := issue(t, cmd(980, nil, c.ModeCallerManaged), now)
	eqTypes(t, y.Events(), c.EventInvoiceIssued)
	eq(t, "next nil", y.State().NextCollectionAt == nil, true)
	eq(t, "view seq", y.View().CollectionSeq, 1)
}

func attempt(t time.Time) sk.PaymentAttemptID {
	// UUIDv7 の時刻順を固定するため、時刻から作る
	u := sk.NewID[sk.PaymentAttemptTag]().UUID()
	ms := t.UnixMilli()
	for i := 5; i >= 0; i-- {
		u[i] = byte(ms)
		ms >>= 8
	}
	return sk.IDFromUUID[sk.PaymentAttemptTag](u)
}

func fail(a sk.PaymentAttemptID, retryable bool) domain.Outcome {
	return domain.Outcome{AttemptID: a, Status: c.OutcomeFailed, FailureCode: "card_declined", Retryable: retryable}
}

func policy(offs ...int) domain.DunningPolicy {
	return domain.DunningPolicy{Offsets: offs}
}

func day(t time.Time) sk.Date { return sk.DateOf(t) }

// AC-BIL-013：デフォルト 1・3・7 で 11-15 に失敗 → 11-16 10:00。
func TestFirstScheduledFailure(t *testing.T) {
	now := at("2026-11-15 10:00")
	x := issue(t, cmd(980, nil, c.ModeScheduled), now)
	x.Events()
	ok, _ := x.RecordOutcome(fail(attempt(now), true), domain.DefaultPolicy(x.State().TenantID), day(now), now.Add(time.Minute))
	eq(t, "applied", ok, true)
	s := x.State()
	eq(t, "status", s.Status, c.StatusOpen)
	eq(t, "stage", s.DunningStage, 1)
	eq(t, "next", s.NextCollectionAt.Equal(at("2026-11-16 10:00")), true)
	eq(t, "source", s.Dunning.PolicySource, "default")
	ev := x.Events()
	eqTypes(t, ev, c.EventInvoicePaymentFailed)
	eq(t, "attempt_no", ev[0].Payload["attempt_no"], any(1))
	eq(t, "remaining", ev[0].Payload["remaining_retries"], any(3))
}

// due は回収時刻ジョブを now で実行して、出した collection_due の seq を返す（出さなければ 0）。
func due(t *testing.T, x *domain.Invoice, now time.Time) int {
	t.Helper()
	x.Events()
	if !x.EmitDueCollection(now, lease) {
		return 0
	}
	ev := x.Events()
	eqTypes(t, ev, c.EventInvoiceCollectionDue)
	return ev[0].Payload["collection_seq"].(int)
}

// AC-BIL-014・015・016・018。
func TestDunningSchedule(t *testing.T) {
	t.Run("AC-BIL-014 事業者の方針 2・5", func(t *testing.T) {
		now := at("2026-11-15 10:00")
		x := issue(t, cmd(980, nil, c.ModeScheduled), now)
		x.RecordOutcome(fail(attempt(now), true), policy(2, 5), day(now), now)
		eq(t, "next1", x.State().NextCollectionAt.Equal(at("2026-11-17 10:00")), true)
		t2 := at("2026-11-17 10:00")
		eq(t, "seq2", due(t, x, t2), 2)
		x.RecordOutcome(fail(attempt(t2), true), policy(2, 5), day(t2), t2)
		eq(t, "next2", x.State().NextCollectionAt.Equal(at("2026-11-20 10:00")), true)
	})
	t.Run("AC-BIL-015 方針の変更はスナップショットに影響しない／AC-BIL-018 retryable=false", func(t *testing.T) {
		now := at("2026-11-15 10:00")
		x := issue(t, cmd(980, nil, c.ModeScheduled), now)
		x.RecordOutcome(fail(attempt(now), true), policy(1, 3, 7), day(now), now)
		t2 := at("2026-11-16 10:00")
		due(t, x, t2)
		x.RecordOutcome(fail(attempt(t2), false), policy(2), day(t2), t2)
		s := x.State()
		eq(t, "stage", s.DunningStage, 2)
		eq(t, "next", s.NextCollectionAt.Equal(at("2026-11-18 10:00")), true)
		ev := x.Events()
		eq(t, "retryable", ev[0].Payload["retryable"], any(false))
	})
	t.Run("AC-BIL-016 最後のリトライの失敗で uncollectible", func(t *testing.T) {
		now := at("2026-11-15 10:00")
		x := issue(t, cmd(980, nil, c.ModeScheduled), now)
		p := policy(1, 3, 7)
		x.RecordOutcome(fail(attempt(now), true), p, day(now), now)
		for _, ts := range []string{"2026-11-16 10:00", "2026-11-18 10:00", "2026-11-22 10:00"} {
			tt := at(ts)
			if due(t, x, tt) == 0 {
				t.Fatalf("%s: no due", ts)
			}
			x.RecordOutcome(fail(attempt(tt), true), p, day(tt), tt)
		}
		s := x.State()
		eq(t, "status", s.Status, c.StatusUncollectible)
		eq(t, "next nil", s.NextCollectionAt == nil, true)
		eqTypes(t, x.Events(), c.EventInvoiceMarkedUncollectible)
	})
	t.Run("AC-BIL-017 方針が 0 回なら即 uncollectible", func(t *testing.T) {
		now := at("2026-11-15 10:00")
		x := issue(t, cmd(980, nil, c.ModeScheduled), now)
		x.Events()
		x.RecordOutcome(fail(attempt(now), true), policy(), day(now), now)
		eq(t, "status", x.State().Status, c.StatusUncollectible)
		eqTypes(t, x.Events(), c.EventInvoiceMarkedUncollectible)
	})
	t.Run("§9-8 計算上の時刻が過去なら now", func(t *testing.T) {
		now := at("2026-11-15 10:00")
		x := issue(t, cmd(980, nil, c.ModeScheduled), now)
		later := at("2026-11-20 09:00")
		x.RecordOutcome(fail(attempt(now), true), policy(1, 3, 7), day(now), later)
		eq(t, "next", x.State().NextCollectionAt.Equal(later), true)
	})
}

// AC-BIL-019：即時の再決済の失敗はスケジュールを変えない。
func TestImmediateCollectionKeepsSchedule(t *testing.T) {
	now := at("2026-11-15 10:00")
	x := issue(t, cmd(980, nil, c.ModeScheduled), now)
	p := policy(1, 3, 7)
	x.RecordOutcome(fail(attempt(now), true), p, day(now), now)
	t2 := at("2026-11-16 10:00")
	due(t, x, t2) // seq=2
	x.RecordOutcome(fail(attempt(t2), true), p, day(t2), t2)
	x.Events()
	eq(t, "next", x.State().NextCollectionAt.Equal(at("2026-11-18 10:00")), true)

	t3 := at("2026-11-17 12:00")
	if err := x.RequestCollection(c.ReasonImmediate, "system", t3, lease); err != nil {
		t.Fatal(err)
	}
	ev := x.Events()
	eqTypes(t, ev, c.EventInvoiceCollectionDue)
	eq(t, "seq", ev[0].Payload["collection_seq"], any(3))
	eq(t, "reason", ev[0].Payload["reason"], any(c.ReasonImmediate))
	// §9-16：リース中の再要求は同じ要求のまま（イベントなし）
	if err := x.RequestCollection(c.ReasonOps, "op", t3.Add(time.Minute), lease); err != nil {
		t.Fatal(err)
	}
	eqTypes(t, x.Events())
	eq(t, "seq still", x.State().CollectionSeq, 3)

	x.RecordOutcome(fail(attempt(t3), true), p, day(t3), t3.Add(time.Minute))
	s := x.State()
	eq(t, "stage", s.DunningStage, 2)
	eq(t, "next kept", s.NextCollectionAt.Equal(at("2026-11-18 10:00")), true)
	eq(t, "attempt_count", s.AttemptCount, 3)
	eq(t, "seq4", due(t, x, at("2026-11-18 10:00")), 4)
}

// AC-BIL-021：unknown → payment_unknown。ジョブ対象外、回収要求は collection_in_progress。
func TestUnknownOutcome(t *testing.T) {
	now := at("2026-11-15 10:00")
	x := issue(t, cmd(980, nil, c.ModeScheduled), now)
	a := attempt(now)
	ok, _ := x.RecordOutcome(domain.Outcome{AttemptID: a, Status: c.OutcomeUnknown}, policy(1), day(now), now)
	eq(t, "applied", ok, true)
	eq(t, "status", x.State().Status, c.StatusPaymentUnknown)
	eq(t, "due", x.DueForCollection(now.Add(24*time.Hour)), false)
	if err := x.RequestCollection(c.ReasonOps, "op", now, lease); !errors.Is(err, c.ErrCollectionInProgress) {
		t.Fatalf("err = %v", err)
	}
}

// AC-BIL-023：succeeded の後の遅れた pending は反映しない。
func TestPendingAfterSuccess(t *testing.T) {
	now := at("2026-11-15 10:00")
	x := issue(t, cmd(980, nil, c.ModeScheduled), now)
	a := attempt(now)
	x.RecordOutcome(domain.Outcome{AttemptID: a, Status: c.OutcomePending}, policy(1), day(now), now)
	eq(t, "processing", x.State().Status, c.StatusProcessing)
	x.RecordOutcome(domain.Outcome{AttemptID: a, Status: c.OutcomeSucceeded}, policy(1), day(now), now)
	ok, anomaly := x.RecordOutcome(domain.Outcome{AttemptID: a, Status: c.OutcomePending, AttemptFinalized: true}, policy(1), day(now), now)
	eq(t, "applied", ok, false)
	eq(t, "anomaly", anomaly, "")
	eq(t, "status", x.State().Status, c.StatusPaid)
}

// AC-BIL-024・025：古い試行の succeeded。古い試行の failed は記録だけ。
func TestOldAttempts(t *testing.T) {
	now := at("2026-11-15 10:00")
	old := attempt(now.Add(-time.Hour))
	x := issue(t, cmd(980, nil, c.ModeScheduled), now)
	x.RecordOutcome(fail(attempt(now), true), policy(1), day(now), now)
	ok, _ := x.RecordOutcome(fail(old, true), policy(1), day(now), now)
	eq(t, "old failed applied", ok, false)
	eq(t, "stage", x.State().DunningStage, 1)
	x.Events()
	ok, _ = x.RecordOutcome(domain.Outcome{AttemptID: old, Status: c.OutcomeSucceeded}, policy(1), day(now), now)
	eq(t, "old success applied", ok, true)
	eq(t, "status", x.State().Status, c.StatusPaid)
	eqTypes(t, x.Events(), c.EventInvoicePaid)
	_, anomaly := x.RecordOutcome(domain.Outcome{AttemptID: attempt(now.Add(time.Hour)), Status: c.OutcomeSucceeded}, policy(1), day(now), now)
	eq(t, "duplicate", anomaly, "duplicate_success")

	y := issue(t, cmd(980, nil, c.ModeScheduled), now)
	y.RecordOutcome(fail(attempt(now), true), policy(), day(now), now)
	eq(t, "uncollectible", y.State().Status, c.StatusUncollectible)
	ok, anomaly = y.RecordOutcome(domain.Outcome{AttemptID: old, Status: c.OutcomeSucceeded}, policy(), day(now), now)
	eq(t, "applied", ok, false)
	eq(t, "late", anomaly, "late_success")
	eq(t, "still", y.State().Status, c.StatusUncollectible)
}

// AC-BIL-026・027：リース中は再発行しない。リース切れで同じ seq を再送。
func TestDueLeaseAndResend(t *testing.T) {
	now := at("2026-11-15 10:00")
	x := issue(t, cmd(980, nil, c.ModeScheduled), now)
	x.RecordOutcome(fail(attempt(now), true), policy(1, 3), day(now), now)
	t2 := at("2026-11-16 10:00")
	eq(t, "seq2", due(t, x, t2), 2)
	eq(t, "lease", due(t, x, t2.Add(time.Hour)), 0)
	eq(t, "resend", due(t, x, t2.Add(6*time.Hour)), 2)
	s := x.State()
	eq(t, "stage", s.DunningStage, 1)
	eq(t, "attempts", s.AttemptCount, 1)
}

// AC-BIL-029・§9-5：processing の請求を void 要求 → failed で void、succeeded なら paid ＋ 異常。
func TestVoidRequest(t *testing.T) {
	now := at("2026-11-15 10:00")
	x := issue(t, cmd(980, nil, c.ModeScheduled), now)
	a := attempt(now)
	x.RecordOutcome(domain.Outcome{AttemptID: a, Status: c.OutcomePending}, policy(1), day(now), now)
	x.Events()
	eq(t, "void requested", x.Void(c.VoidSubscriptionCanceled, now), true)
	eq(t, "still processing", x.State().Status, c.StatusProcessing)
	eqTypes(t, x.Events())
	x.RecordOutcome(fail(a, true), policy(1), day(now), now)
	eq(t, "void", x.State().Status, c.StatusVoid)
	eqTypes(t, x.Events(), c.EventInvoiceVoided)
	if err := x.RequestCollection(c.ReasonImmediate, "system", now, lease); !errors.Is(err, c.ErrInvoiceNotCollectible) {
		t.Fatalf("err = %v", err)
	}

	y := issue(t, cmd(980, nil, c.ModeScheduled), now)
	y.RecordOutcome(domain.Outcome{AttemptID: a, Status: c.OutcomeUnknown}, policy(1), day(now), now)
	y.Void(c.VoidSubscriptionCanceled, now)
	_, anomaly := y.RecordOutcome(domain.Outcome{AttemptID: a, Status: c.OutcomeSucceeded}, policy(1), day(now), now)
	eq(t, "paid", y.State().Status, c.StatusPaid)
	eq(t, "anomaly", anomaly, "paid_after_void_request")
}

// caller_managed の失敗はダニングしない。回収要求は not_collectible。
func TestCallerManaged(t *testing.T) {
	now := at("2026-11-15 10:00")
	x := issue(t, cmd(980, nil, c.ModeCallerManaged), now)
	x.Events()
	x.RecordOutcome(fail(attempt(now), true), policy(1), day(now), now)
	s := x.State()
	eq(t, "status", s.Status, c.StatusOpen)
	eq(t, "dunning nil", s.Dunning == nil, true)
	eq(t, "next nil", s.NextCollectionAt == nil, true)
	eq(t, "seq", s.CollectionSeq, 1)
	ev := x.Events()
	eqTypes(t, ev, c.EventInvoicePaymentFailed)
	eq(t, "mode", ev[0].Payload["collection_mode"], any(c.ModeCallerManaged))
	if err := x.RequestCollection(c.ReasonImmediate, "system", now, lease); !errors.Is(err, c.ErrInvoiceNotCollectible) {
		t.Fatalf("err = %v", err)
	}
	eq(t, "void", x.Void(c.VoidSignupFailed, now), true)
}

// AC-BIL-033：DP-1〜DP-3。
func TestValidateOffsets(t *testing.T) {
	cases := []struct {
		in   []int
		code string
	}{
		{[]int{1, 1}, "not_increasing"}, {[]int{0}, "out_of_range"}, {[]int{1, 2, 3, 4, 5, 6}, "too_many"}, {[]int{61}, "out_of_range"},
		{[]int{3, 2}, "not_increasing"}, {[]int{}, ""}, {[]int{1, 3, 7}, ""}, {[]int{1, 2, 3, 4, 60}, ""},
	}
	for _, tc := range cases {
		err := domain.ValidateOffsets(tc.in)
		var pe *c.InvalidDunningPolicyError
		switch {
		case tc.code == "" && err != nil:
			t.Errorf("%v: err = %v", tc.in, err)
		case tc.code != "" && (!errors.As(err, &pe) || pe.Code != tc.code):
			t.Errorf("%v: err = %v, want %s", tc.in, err, tc.code)
		}
	}
}
