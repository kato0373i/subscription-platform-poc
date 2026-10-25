// Package domain は集計の規則（console-dashboard 要件 §5・設計 §3.1）。イベントから契約ごとの集計の状態（Fact）を作り、
// その変化（前 → 後）を日ごとの増減（Delta）に分類する。
package domain

import (
	"math"

	sk "subscription-platform/internal/sharedkernel"
)

// 契約の状態（subscription の contracts.Status と同じ文字列）。
const (
	StatusPendingInitial = "pending_initial"
	StatusTrialing       = "trialing"
	StatusActive         = "active"
	StatusPastDue        = "past_due"
	StatusCanceled       = "canceled"
)

// 解約の理由（subscription の contracts.CancelReason と同じ文字列）。
const (
	ReasonCustomer        = "customer"
	ReasonDunning         = "dunning"
	ReasonTenantSuspended = "tenant_suspended"
)

// Paying は有料契約か（要件 §5.1：active・past_due）。
func Paying(status string) bool { return status == StatusActive || status == StatusPastDue }

// MonthlyAmount は月額換算額（価格 × 数量。年払いは 12 で割って円未満を四捨五入。要件 §5.1）。
func MonthlyAmount(amount int64, quantity int, interval string) int64 {
	total := amount * int64(max(quantity, 1))
	if interval == "year" {
		return int64(math.Round(float64(total) / 12))
	}
	return total
}

// Fact は契約ごとの集計の状態。初めて見る契約は Status が空（MRR 0）。
type Fact struct {
	Status         string
	MRR            int64 // 有料契約でなければ 0
	PriceVersionID string
	Interval       string
	Quantity       int
	StartedInTrial bool
	HasBeenPaying  bool
	TrialEnd       *sk.Date
	LastVersion    int
}

// Exists は一度でも反映した契約か。
func (f Fact) Exists() bool { return f.Status != "" }

// Snapshot はイベントのスナップショットから読む値（subscription.md §3.1）。
type Snapshot struct {
	Status         string
	PriceVersionID string
	Interval       string
	Quantity       int
	TrialEnd       *sk.Date
	CancelReason   string
	Version        int
}

// Next はスナップショットを反映した後の Fact。amount は価格バージョンの金額（税込・1 数量あたり）。
func (f Fact) Next(s Snapshot, amount int64) Fact {
	n := f
	n.Status, n.PriceVersionID, n.Interval, n.Quantity, n.TrialEnd, n.LastVersion = s.Status, s.PriceVersionID, s.Interval, max(s.Quantity, 1), s.TrialEnd, s.Version
	if !f.Exists() {
		n.StartedInTrial = s.Status == StatusTrialing || s.TrialEnd != nil
	}
	n.MRR = 0
	if Paying(s.Status) {
		n.MRR = MonthlyAmount(amount, s.Quantity, s.Interval)
		n.HasBeenPaying = true
	}
	return n
}

// Delta は 1 日分（または 1 イベント分）の増減。DailyMetrics の列と 1 対 1。
type Delta struct {
	MRRDelta, NewMRR, ExpansionMRR, ContractionMRR, ChurnedMRR int64
	PayingDelta, TrialingDelta, PastDueDelta                    int
	NewSubscriptions                                            int
	CanceledTotal, CanceledPaying                               int
	CanceledCustomer, CanceledDunning, CanceledTenant           int
	TrialConverted, TrialLost, Recovered, DunningLost           int
	RevenueInitial, RevenueRenewal                              int64
}

// Add は d に o を足す。
func (d *Delta) Add(o Delta) {
	d.MRRDelta += o.MRRDelta
	d.NewMRR += o.NewMRR
	d.ExpansionMRR += o.ExpansionMRR
	d.ContractionMRR += o.ContractionMRR
	d.ChurnedMRR += o.ChurnedMRR
	d.PayingDelta += o.PayingDelta
	d.TrialingDelta += o.TrialingDelta
	d.PastDueDelta += o.PastDueDelta
	d.NewSubscriptions += o.NewSubscriptions
	d.CanceledTotal += o.CanceledTotal
	d.CanceledPaying += o.CanceledPaying
	d.CanceledCustomer += o.CanceledCustomer
	d.CanceledDunning += o.CanceledDunning
	d.CanceledTenant += o.CanceledTenant
	d.TrialConverted += o.TrialConverted
	d.TrialLost += o.TrialLost
	d.Recovered += o.Recovered
	d.DunningLost += o.DunningLost
	d.RevenueInitial += o.RevenueInitial
	d.RevenueRenewal += o.RevenueRenewal
}

// IsZero は何も変わらないか。
func (d Delta) IsZero() bool { return d == Delta{} }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Classify は o → n の変化を増減に分類する（設計 §3.1）。eventType は元のイベント、cancelReason は n の解約理由。
func Classify(o, n Fact, eventType, cancelReason string) Delta {
	var d Delta
	if eventType == "subscription.started" && !o.Exists() {
		d.NewSubscriptions = 1
	}
	op, np := Paying(o.Status), Paying(n.Status)
	switch {
	case o.MRR == 0 && n.MRR > 0:
		d.NewMRR = n.MRR
	case op && np && n.MRR > o.MRR:
		d.ExpansionMRR = n.MRR - o.MRR
	case op && np && n.MRR < o.MRR:
		d.ContractionMRR = o.MRR - n.MRR
	case op && !np:
		d.ChurnedMRR = o.MRR
	}
	if n.Status == StatusCanceled && o.Status != StatusCanceled {
		d.CanceledTotal = 1
		d.CanceledPaying = b2i(op)
		switch cancelReason {
		case ReasonDunning:
			d.CanceledDunning, d.DunningLost = 1, 1
		case ReasonTenantSuspended:
			d.CanceledTenant = 1
		default:
			d.CanceledCustomer = 1
		}
		if n.StartedInTrial && !n.HasBeenPaying {
			d.TrialLost = 1
		}
	}
	if n.StartedInTrial && !o.HasBeenPaying && np {
		d.TrialConverted = 1
	}
	if eventType == "subscription.recovered" {
		d.Recovered = 1
	}
	d.MRRDelta = n.MRR - o.MRR
	d.PayingDelta = b2i(np) - b2i(op)
	d.TrialingDelta = b2i(n.Status == StatusTrialing) - b2i(o.Status == StatusTrialing)
	d.PastDueDelta = b2i(n.Status == StatusPastDue) - b2i(o.Status == StatusPastDue)
	return d
}

// Revenue は入金の増減（billing.invoice_paid。kind は initial / renewal）。
func Revenue(amount int64, kind string) Delta {
	if kind == "initial" {
		return Delta{RevenueInitial: amount}
	}
	return Delta{RevenueRenewal: amount}
}
