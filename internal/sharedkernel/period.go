package sharedkernel

import "fmt"

type BillingInterval string

const (
	IntervalMonth BillingInterval = "month"
	IntervalYear  BillingInterval = "year"
)

func (i BillingInterval) Valid() bool { return i == IntervalMonth || i == IntervalYear }

// Months は 1 か 12。不正値は 0（Valid で検出する）。
func (i BillingInterval) Months() int {
	switch i {
	case IntervalMonth:
		return 1
	case IntervalYear:
		return 12
	default:
		return 0
	}
}

// BillingPeriod は両端を含む期間（D-11）。不変条件: Start <= End。
type BillingPeriod struct{ Start, End Date }

func NewBillingPeriod(start, end Date) (BillingPeriod, error) {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return BillingPeriod{}, fmt.Errorf("%w: %s..%s", ErrInvalidPeriod, start, end)
	}
	return BillingPeriod{Start: start, End: end}, nil
}

// FirstPeriod は {s, s.AddMonthsClamped(iv.Months(), a) − 1 日}。
func FirstPeriod(start Date, iv BillingInterval, anchorDay int) BillingPeriod {
	return BillingPeriod{Start: start, End: start.AddMonthsClamped(iv.Months(), anchorDay).AddDays(-1)}
}

// DaysPeriod はトライアル用。End = start + days − 1（days >= 1）。
func DaysPeriod(start Date, days int) (BillingPeriod, error) {
	if days < 1 {
		return BillingPeriod{}, fmt.Errorf("%w: days=%d", ErrInvalidPeriod, days)
	}
	return BillingPeriod{Start: start, End: start.AddDays(days - 1)}, nil
}

// Next は次の期間。start' = End+1、end' = start'.AddMonthsClamped(iv.Months(), a) − 1 日。
func (p BillingPeriod) Next(iv BillingInterval, anchorDay int) BillingPeriod {
	s := p.NextStart()
	return BillingPeriod{Start: s, End: s.AddMonthsClamped(iv.Months(), anchorDay).AddDays(-1)}
}

// NextStart は End+1 ＝ 次の更新日（D-11）。
func (p BillingPeriod) NextStart() Date { return p.End.AddDays(1) }

func (p BillingPeriod) Contains(d Date) bool { return !d.Before(p.Start) && !d.After(p.End) }

func (p BillingPeriod) Days() int { return p.Start.DaysUntil(p.End) + 1 }

func (p BillingPeriod) String() string { return p.Start.String() + ".." + p.End.String() }
