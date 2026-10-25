package domain

import (
	"fmt"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// Day は 1 日分の集計の行。
type Day struct {
	Date  sk.Date
	Delta Delta
}

// Month は JST の暦月（月初の日）。
type Month struct{ First sk.Date }

// MonthOf は d を含む月。
func MonthOf(d sk.Date) Month { return Month{First: sk.MustDate(d.Year(), d.Month(), 1)} }

// ParseMonth は "2026-10" を読む。
func ParseMonth(s string) (Month, error) {
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return Month{}, fmt.Errorf("reporting: month must be YYYY-MM: %w", err)
	}
	return Month{First: sk.MustDate(t.Year(), t.Month(), 1)}, nil
}

func (m Month) String() string { return fmt.Sprintf("%04d-%02d", m.First.Year(), int(m.First.Month())) }

// Last は月末の日。
func (m Month) Last() sk.Date { return m.First.AddDays(m.First.DaysInMonth() - 1) }

// Add は n か月後（負なら前）の月。
func (m Month) Add(n int) Month { return Month{First: m.First.AddMonthsClamped(n, 1)} }

// Before は m が o より前か。
func (m Month) Before(o Month) bool { return m.First.Before(o.First) }

// Rate は率（分母 0 なら Value は nil）。
type Rate struct {
	Numerator   int64    `json:"numerator"`
	Denominator int64    `json:"denominator"`
	Value       *float64 `json:"value"`
}

func rate(n, d int64) Rate {
	r := Rate{Numerator: n, Denominator: d}
	if d > 0 {
		v := float64(n) / float64(d)
		r.Value = &v
	}
	return r
}

// Stock はある時点の値（差分の累積）。
type Stock struct{ MRR, Paying, Trialing, PastDue int64 }

func (s *Stock) add(d Delta) {
	s.MRR += d.MRRDelta
	s.Paying += int64(d.PayingDelta)
	s.Trialing += int64(d.TrialingDelta)
	s.PastDue += int64(d.PastDueDelta)
}

// Canceled は解約数の内訳。
type Canceled struct {
	Total           int `json:"total"`
	Customer        int `json:"customer"`
	Dunning         int `json:"dunning"`
	TenantSuspended int `json:"tenant_suspended"`
}

// Revenue は入金額。
type RevenueSum struct {
	Total   int64 `json:"total"`
	Initial int64 `json:"initial"`
	Renewal int64 `json:"renewal"`
}

// KPIs は 1 か月分の KPI（要件 §5.3）。
type KPIs struct {
	NewSubscriptions int        `json:"new_subscriptions"`
	NewMRR           int64      `json:"new_mrr"`
	Canceled         Canceled   `json:"canceled"`
	ChurnRate        Rate       `json:"churn_rate"`
	RevenueChurnRate Rate       `json:"revenue_churn_rate"`
	TrialConversion  Rate       `json:"trial_conversion"`
	Recovery         Rate       `json:"recovery"`
	Revenue          RevenueSum `json:"revenue"`
	ARPU             *int64     `json:"arpu"`
	MRR              int64      `json:"mrr"`
	MRRStart         int64      `json:"mrr_start"`
	Paying           int64      `json:"paying"`
	Trialing         int64      `json:"trialing"`
	PastDue          int64      `json:"past_due"`
	Breakdown        Breakdown  `json:"-"`
}

// Breakdown は純増 MRR の内訳。
type Breakdown struct {
	New         int64 `json:"new"`
	Expansion   int64 `json:"expansion"`
	Contraction int64 `json:"contraction"`
	Churned     int64 `json:"churned"`
}

// Net は純増 MRR。
func (b Breakdown) Net() int64 { return b.New + b.Expansion - b.Contraction - b.Churned }

// TrendPoint は月ごとの推移。
type TrendPoint struct {
	Month            string `json:"month"`
	MRR              int64  `json:"mrr"`
	NetNewMRR        int64  `json:"net_new_mrr"`
	NewSubscriptions int    `json:"new_subscriptions"`
	Canceled         int    `json:"canceled"`
	Revenue          int64  `json:"revenue"`
}

// DailyPoint は日ごとの推移。
type DailyPoint struct {
	Date             string `json:"date"`
	MRR              int64  `json:"mrr"`
	NewSubscriptions int    `json:"new_subscriptions"`
	Canceled         int    `json:"canceled"`
}

// Overview はダッシュボードの計算の結果（goal・attention・as_of はアプリ層が足す）。
type Overview struct {
	Month     Month
	Today     sk.Date
	IsCurrent bool
	Current   KPIs
	Previous  KPIs
	Trend     []TrendPoint
	Daily     []DailyPoint
}

// Compute は事業者の全期間の日ごとの行（日付の昇順）から、month の KGI・KPI・推移を計算する。today は業務時刻の今日。
func Compute(days []Day, month Month, today sk.Date) Overview {
	cur := MonthOf(today)
	ov := Overview{Month: month, Today: today, IsCurrent: month == cur}
	ov.Current = kpis(days, month, today)
	ov.Previous = kpis(days, month.Add(-1), today)
	for i := 11; i >= 0; i-- {
		m := month.Add(-i)
		k := kpis(days, m, today)
		ov.Trend = append(ov.Trend, TrendPoint{
			Month: m.String(), MRR: k.MRR, NetNewMRR: k.Breakdown.Net(), NewSubscriptions: k.NewSubscriptions, Canceled: k.Canceled.Total, Revenue: k.Revenue.Total,
		})
	}
	// 日ごと：月初から（当月なら今日まで、過去の月は月末まで）
	end := month.Last()
	if ov.IsCurrent && today.Before(end) {
		end = today
	}
	var stock Stock
	byDate := map[sk.Date]Delta{}
	for _, d := range days {
		if d.Date.Before(month.First) {
			stock.add(d.Delta)
		} else {
			byDate[d.Date] = d.Delta
		}
	}
	for d := month.First; !d.After(end); d = d.AddDays(1) {
		x := byDate[d]
		stock.add(x)
		ov.Daily = append(ov.Daily, DailyPoint{Date: d.String(), MRR: stock.MRR, NewSubscriptions: x.NewSubscriptions, Canceled: x.CanceledTotal})
	}
	return ov
}

// kpis は月 m の KPI（当月より後の日は含めない）。
func kpis(days []Day, m Month, today sk.Date) KPIs {
	var start, end Stock
	var flow Delta
	last := m.Last()
	for _, d := range days {
		if d.Date.After(today) {
			continue
		}
		switch {
		case d.Date.Before(m.First):
			start.add(d.Delta)
			end.add(d.Delta)
		case !d.Date.After(last):
			end.add(d.Delta)
			flow.Add(d.Delta)
		}
	}
	k := KPIs{
		NewSubscriptions: flow.NewSubscriptions,
		NewMRR:           flow.NewMRR,
		Canceled:         Canceled{Total: flow.CanceledTotal, Customer: flow.CanceledCustomer, Dunning: flow.CanceledDunning, TenantSuspended: flow.CanceledTenant},
		ChurnRate:        rate(int64(flow.CanceledPaying), start.Paying),
		RevenueChurnRate: rate(flow.ChurnedMRR+flow.ContractionMRR, start.MRR),
		TrialConversion:  rate(int64(flow.TrialConverted), int64(flow.TrialConverted+flow.TrialLost)),
		Recovery:         rate(int64(flow.Recovered), int64(flow.Recovered+flow.DunningLost)),
		Revenue:          RevenueSum{Total: flow.RevenueInitial + flow.RevenueRenewal, Initial: flow.RevenueInitial, Renewal: flow.RevenueRenewal},
		MRR:              end.MRR, MRRStart: start.MRR, Paying: end.Paying, Trialing: end.Trialing, PastDue: end.PastDue,
		Breakdown: Breakdown{New: flow.NewMRR, Expansion: flow.ExpansionMRR, Contraction: flow.ContractionMRR, Churned: flow.ChurnedMRR},
	}
	if end.Paying > 0 {
		a := int64(float64(end.MRR)/float64(end.Paying) + 0.5)
		k.ARPU = &a
	}
	return k
}
