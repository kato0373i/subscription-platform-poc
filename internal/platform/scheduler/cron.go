package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// Cron は EventBridge Scheduler の cron 式 "cron(分 時 日 月 曜日 年)"（Asia/Tokyo）。
// SCHEDULER_MODE=internal のときに、ワーカーが自分で起動時刻を判定するために使う。
type Cron struct {
	minute, hour, dom, month, dow, year field
	domAny, dowAny                      bool
}

type field map[int]bool

var dowNames = map[string]int{"SUN": 1, "MON": 2, "TUE": 3, "WED": 4, "THU": 5, "FRI": 6, "SAT": 7}
var monthNames = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}

// ParseCron は "cron(30 0 * * ? *)" を解析する。L・W・# は使わない（PoC で要るものだけ）。
func ParseCron(expr string) (Cron, error) {
	inner, ok := strings.CutPrefix(strings.TrimSpace(expr), "cron(")
	if !ok || !strings.HasSuffix(inner, ")") {
		return Cron{}, fmt.Errorf("cron: must be cron(...): %q", expr)
	}
	parts := strings.Fields(strings.TrimSuffix(inner, ")"))
	if len(parts) != 6 {
		return Cron{}, fmt.Errorf("cron: need 6 fields: %q", expr)
	}
	var c Cron
	var err error
	specs := []struct {
		dst     *field
		lo, hi  int
		names   map[string]int
		anyFlag *bool
		raw     string
	}{
		{&c.minute, 0, 59, nil, nil, parts[0]},
		{&c.hour, 0, 23, nil, nil, parts[1]},
		{&c.dom, 1, 31, nil, &c.domAny, parts[2]},
		{&c.month, 1, 12, monthNames, nil, parts[3]},
		{&c.dow, 1, 7, dowNames, &c.dowAny, parts[4]},
		{&c.year, 1970, 2199, nil, nil, parts[5]},
	}
	for _, s := range specs {
		if s.raw == "?" {
			if s.anyFlag == nil {
				return Cron{}, fmt.Errorf("cron: ? is only allowed in day-of-month or day-of-week: %q", expr)
			}
			*s.anyFlag = true
			*s.dst = rangeField(s.lo, s.hi, 1)
			continue
		}
		if *s.dst, err = parseField(s.raw, s.lo, s.hi, s.names); err != nil {
			return Cron{}, fmt.Errorf("cron: %w in %q", err, expr)
		}
		if s.anyFlag != nil && s.raw == "*" {
			*s.anyFlag = true
		}
	}
	if !c.domAny && !c.dowAny {
		return Cron{}, fmt.Errorf("cron: one of day-of-month and day-of-week must be ? : %q", expr)
	}
	return c, nil
}

func rangeField(lo, hi, step int) field {
	f := field{}
	for v := lo; v <= hi; v += step {
		f[v] = true
	}
	return f
}

func parseField(raw string, lo, hi int, names map[string]int) (field, error) {
	f := field{}
	for _, part := range strings.Split(raw, ",") {
		step := 1
		if base, s, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("bad step %q", part)
			}
			step, part = n, base
		}
		from, to := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			var err error
			if from, err = value(a, names); err != nil {
				return nil, err
			}
			if to, err = value(b, names); err != nil {
				return nil, err
			}
		default:
			v, err := value(part, names)
			if err != nil {
				return nil, err
			}
			from = v
			if step == 1 {
				to = v
			}
		}
		if from < lo || to > hi || from > to {
			return nil, fmt.Errorf("value out of range %q", part)
		}
		for v := from; v <= to; v += step {
			f[v] = true
		}
	}
	return f, nil
}

func value(s string, names map[string]int) (int, error) {
	if v, ok := names[strings.ToUpper(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return v, nil
}

// Matches は t（JST に変換して判定）がこの式の起動時刻（分の単位）か。
func (c Cron) Matches(t time.Time) bool {
	j := t.In(sk.JST)
	dayOK := (c.domAny || c.dom[j.Day()]) && (c.dowAny || c.dow[int(j.Weekday())+1])
	return c.minute[j.Minute()] && c.hour[j.Hour()] && c.month[int(j.Month())] && c.year[j.Year()] && dayOK
}
