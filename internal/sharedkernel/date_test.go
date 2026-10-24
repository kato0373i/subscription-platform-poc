package sharedkernel_test

import (
	"errors"
	"testing"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

func d(s string) sk.Date {
	v, err := sk.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

// AC-PLT-007
func TestDateOfJSTBoundary(t *testing.T) {
	if got := sk.DateOf(time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)); got != d("2026-10-12") {
		t.Errorf("15:00Z = %s", got)
	}
	if got := sk.DateOf(time.Date(2026, 10, 11, 14, 59, 59, 0, time.UTC)); got != d("2026-10-11") {
		t.Errorf("14:59:59Z = %s", got)
	}
}

func TestNewDateRejectsNonexistent(t *testing.T) {
	for _, s := range []string{"2027-02-29", "2027-02-30", "2026-13-01", "26-10-01", "2026-1-1"} {
		if _, err := sk.ParseDate(s); !errors.Is(err, sk.ErrInvalidDate) {
			t.Errorf("ParseDate(%q) err = %v", s, err)
		}
	}
	if got := d("2026-10-12").AtJST(10, 0); !got.Equal(time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("AtJST = %s", got)
	}
}

// AC-PLT-008
func TestAddMonthsClamped(t *testing.T) {
	base := d("2027-01-31")
	want := []string{"2027-02-28", "2027-03-31", "2027-04-30"}
	for i, w := range want {
		if got := base.AddMonthsClamped(i+1, 31); got != d(w) {
			t.Errorf("+%d = %s, want %s", i+1, got, w)
		}
	}
	if got := d("2028-01-31").AddMonthsClamped(1, 31); got != d("2028-02-29") {
		t.Errorf("leap = %s", got)
	}
	if got := d("2026-12-31").AddMonthsClamped(1, 31); got != d("2027-01-31") {
		t.Errorf("year end = %s", got)
	}
}

// AC-PLT-009
func TestMonthlyPeriodsFromJan31(t *testing.T) {
	p := sk.FirstPeriod(d("2027-01-31"), sk.IntervalMonth, 31)
	want := []struct{ start, end, next string }{
		{"2027-01-31", "2027-02-27", "2027-02-28"},
		{"2027-02-28", "2027-03-30", "2027-03-31"},
		{"2027-03-31", "2027-04-29", "2027-04-30"},
		{"2027-04-30", "2027-05-30", "2027-05-31"},
	}
	for i, w := range want {
		if p.Start != d(w.start) || p.End != d(w.end) || p.NextStart() != d(w.next) {
			t.Fatalf("#%d = %s next %s, want %s..%s next %s", i, p, p.NextStart(), w.start, w.end, w.next)
		}
		p = p.Next(sk.IntervalMonth, 31)
	}
}

// AC-PLT-010
func TestYearlyPeriodsFromLeapDay(t *testing.T) {
	p := sk.FirstPeriod(d("2028-02-29"), sk.IntervalYear, 29)
	for _, w := range []string{"2029-02-28", "2030-02-28", "2031-02-28", "2032-02-29"} {
		if p.NextStart() != d(w) {
			t.Fatalf("更新日 = %s, want %s", p.NextStart(), w)
		}
		p = p.Next(sk.IntervalYear, 29)
	}
}

// AC-PLT-011, AC-PLT-012
func TestFirstAndTrialPeriods(t *testing.T) {
	p := sk.FirstPeriod(d("2026-10-15"), sk.IntervalMonth, 15)
	if p.Start != d("2026-10-15") || p.End != d("2026-11-14") || p.NextStart() != d("2026-11-15") {
		t.Fatalf("first = %s", p)
	}
	tr, err := sk.DaysPeriod(d("2026-10-01"), 14)
	if err != nil || tr.End != d("2026-10-14") || tr.NextStart() != d("2026-10-15") || tr.Days() != 14 {
		t.Fatalf("trial = %s %v", tr, err)
	}
	if _, err := sk.DaysPeriod(d("2026-10-01"), 0); err == nil {
		t.Fatal("days=0 が通った")
	}
}

// AC-PLT-013
func FuzzPeriodsAreContiguous(f *testing.F) {
	f.Add(2027, 1, 31, 31, true)
	f.Add(2028, 2, 29, 29, false)
	f.Add(1900, 12, 31, 1, true)
	f.Fuzz(func(t *testing.T, y, m, day, anchor int, monthly bool) {
		y = 1900 + abs(y)%201
		m = 1 + abs(m)%12
		anchor = 1 + abs(anchor)%31
		start, err := sk.NewDate(y, time.Month(m), min(anchor, 1+abs(day)%28))
		if err != nil {
			t.Skip()
		}
		start = start.AddMonthsClamped(0, anchor)
		iv := sk.IntervalYear
		if monthly {
			iv = sk.IntervalMonth
		}
		p := sk.FirstPeriod(start, iv, anchor)
		for range 100 {
			if p.End.Before(p.Start) {
				t.Fatalf("逆転した期間 %s", p)
			}
			next := p.Next(iv, anchor)
			if next.Start != p.End.AddDays(1) {
				t.Fatalf("隙間がある %s → %s", p, next)
			}
			if want := min(anchor, next.Start.DaysInMonth()); next.Start.Day() != want {
				t.Fatalf("開始日の日 = %d, want %d (%s)", next.Start.Day(), want, next)
			}
			p = next
		}
	})
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestManualClock(t *testing.T) {
	c := sk.NewManualClock(time.Date(2026, 10, 11, 14, 0, 0, 0, time.UTC))
	c.Advance(time.Hour)
	if c.Today() != d("2026-10-12") {
		t.Fatalf("today = %s", c.Today())
	}
}
