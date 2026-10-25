package domain_test

import (
	"testing"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/reporting/internal/domain"
)

func snap(status string, v int) domain.Snapshot {
	return domain.Snapshot{Status: status, PriceVersionID: "pv", Interval: "month", Quantity: 1, Version: v}
}

func TestMonthlyAmount(t *testing.T) {
	cases := []struct {
		amount   int64
		qty      int
		interval string
		want     int64
	}{
		{980, 1, "month", 980},
		{980, 3, "month", 2940},
		{9800, 1, "year", 817}, // 816.67 → 817（要件 FR-001）
		{12000, 1, "year", 1000},
		{6, 1, "year", 1}, // 0.5 → 1
		{980, 0, "month", 980},
	}
	for _, c := range cases {
		if got := domain.MonthlyAmount(c.amount, c.qty, c.interval); got != c.want {
			t.Errorf("MonthlyAmount(%d, %d, %s) = %d, want %d", c.amount, c.qty, c.interval, got, c.want)
		}
	}
}

// 1 契約のイベントの列を流し、最後の Fact と増減の合計を返す。
func run(t *testing.T, steps []struct {
	event, status, reason string
	amount                int64
}) (domain.Fact, domain.Delta) {
	t.Helper()
	var f domain.Fact
	var total domain.Delta
	for i, s := range steps {
		n := f.Next(snap(s.status, i+1), s.amount)
		total.Add(domain.Classify(f, n, s.event, s.reason))
		f = n
	}
	return f, total
}

type step = struct {
	event, status, reason string
	amount                int64
}

func TestClassifyScenarios(t *testing.T) {
	cases := []struct {
		name  string
		steps []step
		want  domain.Delta
	}{
		{"トライアルなしの開始", []step{{"subscription.started", "active", "", 980}},
			domain.Delta{MRRDelta: 980, NewMRR: 980, PayingDelta: 1, NewSubscriptions: 1}},
		{"トライアルの開始は MRR 0", []step{{"subscription.started", "trialing", "", 980}},
			domain.Delta{TrialingDelta: 1, NewSubscriptions: 1}},
		{"トライアルから有料化", []step{{"subscription.started", "trialing", "", 980}, {"subscription.renewed", "active", "", 980}},
			domain.Delta{MRRDelta: 980, NewMRR: 980, PayingDelta: 1, NewSubscriptions: 1, TrialConverted: 1}},
		{"トライアル中の解約は不成立で解約 MRR なし", []step{{"subscription.started", "trialing", "", 980}, {"subscription.canceled", "canceled", "customer", 980}},
			domain.Delta{NewSubscriptions: 1, CanceledTotal: 1, CanceledCustomer: 1, TrialLost: 1}},
		{"更新は変化なし", []step{{"subscription.started", "active", "", 980}, {"subscription.renewed", "active", "", 980}},
			domain.Delta{MRRDelta: 980, NewMRR: 980, PayingDelta: 1, NewSubscriptions: 1}},
		{"プラン変更で増額", []step{{"subscription.started", "active", "", 980}, {"subscription.plan_changed", "active", "", 1980}},
			domain.Delta{MRRDelta: 1980, NewMRR: 980, ExpansionMRR: 1000, PayingDelta: 1, NewSubscriptions: 1}},
		{"プラン変更で減額", []step{{"subscription.started", "active", "", 1980}, {"subscription.plan_changed", "active", "", 980}},
			domain.Delta{MRRDelta: 980, NewMRR: 1980, ContractionMRR: 1000, PayingDelta: 1, NewSubscriptions: 1}},
		{"支払い遅延から回復", []step{{"subscription.started", "active", "", 980}, {"subscription.past_due", "past_due", "", 980}, {"subscription.recovered", "active", "", 980}},
			domain.Delta{MRRDelta: 980, NewMRR: 980, PayingDelta: 1, NewSubscriptions: 1, Recovered: 1}},
		{"支払い失敗で解約", []step{{"subscription.started", "active", "", 980}, {"subscription.past_due", "past_due", "", 980}, {"subscription.canceled", "canceled", "dunning", 980}},
			domain.Delta{NewMRR: 980, ChurnedMRR: 980, NewSubscriptions: 1, CanceledTotal: 1, CanceledPaying: 1, CanceledDunning: 1, DunningLost: 1}},
		{"事業者の停止で解約", []step{{"subscription.started", "active", "", 980}, {"subscription.canceled", "canceled", "tenant_suspended", 980}},
			domain.Delta{NewMRR: 980, ChurnedMRR: 980, NewSubscriptions: 1, CanceledTotal: 1, CanceledPaying: 1, CanceledTenant: 1}},
		{"トライアル後の初回決済の失敗は有料化に数える", []step{{"subscription.started", "trialing", "", 980}, {"subscription.past_due", "past_due", "", 980}},
			domain.Delta{MRRDelta: 980, NewMRR: 980, PayingDelta: 1, PastDueDelta: 1, NewSubscriptions: 1, TrialConverted: 1}},
		{"初めて見る契約が started 以外でも有料なら新規 MRR（新規契約数には数えない）", []step{{"subscription.renewed", "active", "", 980}},
			domain.Delta{MRRDelta: 980, NewMRR: 980, PayingDelta: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, got := run(t, c.steps)
			if got != c.want {
				t.Errorf("\n got  %+v\n want %+v", got, c.want)
			}
		})
	}
}

func TestTrialFlagsAreKeptAcrossEvents(t *testing.T) {
	f, _ := run(t, []step{{"subscription.started", "trialing", "", 980}, {"subscription.renewed", "active", "", 980}, {"subscription.canceled", "canceled", "customer", 980}})
	if !f.StartedInTrial || !f.HasBeenPaying || f.MRR != 0 {
		t.Errorf("fact = %+v", f)
	}
}

func d(s string) sk.Date {
	v, err := sk.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

// 要件 FR-001 の例：月初 1,980 円、当月に 980 円 × 2 件の開始と 1,980 円の解約 → MRR 1,960 円、純増 −20 円。
func TestComputeRequirementExample(t *testing.T) {
	days := []domain.Day{
		{d("2026-09-10"), domain.Delta{MRRDelta: 1980, NewMRR: 1980, PayingDelta: 1, NewSubscriptions: 1, RevenueInitial: 1980}},
		{d("2026-10-03"), domain.Delta{MRRDelta: 1960, NewMRR: 1960, PayingDelta: 2, NewSubscriptions: 2, RevenueInitial: 1960}},
		{d("2026-10-10"), domain.Delta{MRRDelta: -1980, ChurnedMRR: 1980, PayingDelta: -1, CanceledTotal: 1, CanceledPaying: 1, CanceledCustomer: 1}},
	}
	m, _ := domain.ParseMonth("2026-10")
	ov := domain.Compute(days, m, d("2026-10-26"))
	k := ov.Current
	if k.MRR != 1960 || k.MRRStart != 1980 || k.Breakdown.Net() != -20 || k.Breakdown.New != 1960 || k.Breakdown.Churned != 1980 {
		t.Fatalf("kgi = %+v / %+v", k, k.Breakdown)
	}
	if k.ChurnRate.Value == nil || *k.ChurnRate.Value != 1.0 || k.ChurnRate.Numerator != 1 || k.ChurnRate.Denominator != 1 {
		t.Errorf("churn = %+v", k.ChurnRate)
	}
	if k.ARPU == nil || *k.ARPU != 980 || k.Revenue.Total != 1960 || k.Paying != 2 {
		t.Errorf("arpu/revenue = %+v", k)
	}
	if !ov.IsCurrent || len(ov.Daily) != 26 || ov.Daily[0].MRR != 1980 || ov.Daily[2].MRR != 3940 || ov.Daily[25].MRR != 1960 {
		t.Errorf("daily = %d %+v", len(ov.Daily), ov.Daily)
	}
	if len(ov.Trend) != 12 || ov.Trend[11].Month != "2026-10" || ov.Trend[10].MRR != 1980 || ov.Trend[0].Month != "2025-11" {
		t.Errorf("trend = %+v", ov.Trend)
	}
	if p := ov.Previous; p.MRR != 1980 || p.NewSubscriptions != 1 || p.ChurnRate.Value != nil {
		t.Errorf("previous = %+v", p)
	}
}

func TestComputePastMonthAndFutureDays(t *testing.T) {
	days := []domain.Day{
		{d("2026-08-31"), domain.Delta{MRRDelta: 500, PayingDelta: 1}},
		{d("2026-09-15"), domain.Delta{MRRDelta: 500, PayingDelta: 1}},
	}
	m, _ := domain.ParseMonth("2026-09")
	ov := domain.Compute(days, m, d("2026-10-02"))
	if ov.IsCurrent || len(ov.Daily) != 30 || ov.Current.MRR != 1000 || ov.Current.MRRStart != 500 {
		t.Errorf("past month = %+v daily=%d", ov.Current, len(ov.Daily))
	}
	// 当月より後の日（dev clock を戻したときなど）は数えない
	ov = domain.Compute(days, m, d("2026-09-01"))
	if ov.Current.MRR != 500 || len(ov.Daily) != 1 {
		t.Errorf("future = %+v daily=%d", ov.Current, len(ov.Daily))
	}
}

func TestRatesWithZeroDenominator(t *testing.T) {
	m, _ := domain.ParseMonth("2026-10")
	k := domain.Compute(nil, m, d("2026-10-01")).Current
	if k.ChurnRate.Value != nil || k.TrialConversion.Value != nil || k.Recovery.Value != nil || k.RevenueChurnRate.Value != nil || k.ARPU != nil {
		t.Errorf("rates = %+v", k)
	}
}

func TestParseMonth(t *testing.T) {
	m, err := domain.ParseMonth("2026-01")
	if err != nil || m.Add(-1).String() != "2025-12" || m.Last().String() != "2026-01-31" {
		t.Errorf("%v %v", m, err)
	}
	if _, err := domain.ParseMonth("2026-13"); err == nil {
		t.Error("2026-13 should fail")
	}
}
