package sharedkernel

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// JST は tzdata に依存しない固定オフセットの日本時間（distroless 対策）。
var JST = time.FixedZone("Asia/Tokyo", 9*60*60)

// Date は JST の暦日。不変条件: 実在する日付、1 <= y <= 9999。ゼロ値は「未設定」を表す。
type Date struct {
	y int
	m time.Month
	d int
}

func NewDate(y int, m time.Month, d int) (Date, error) {
	if y < 1 || y > 9999 || m < 1 || m > 12 || d < 1 || d > daysIn(y, m) {
		return Date{}, fmt.Errorf("%w: %04d-%02d-%02d", ErrInvalidDate, y, m, d)
	}
	return Date{y: y, m: m, d: d}, nil
}

// MustDate はテストと定数用。
func MustDate(y int, m time.Month, d int) Date {
	v, err := NewDate(y, m, d)
	if err != nil {
		panic(err)
	}
	return v
}

// ParseDate は "YYYY-MM-DD" だけを受け付ける。
func ParseDate(s string) (Date, error) {
	if len(s) != 10 {
		return Date{}, fmt.Errorf("%w: %q", ErrInvalidDate, s)
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return Date{}, fmt.Errorf("%w: %q", ErrInvalidDate, s)
	}
	return NewDate(t.Year(), t.Month(), t.Day())
}

// DateOf は t を JST に変換した暦日。
func DateOf(t time.Time) Date {
	j := t.In(JST)
	return Date{y: j.Year(), m: j.Month(), d: j.Day()}
}

func daysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func (d Date) Year() int           { return d.y }
func (d Date) Month() time.Month   { return d.m }
func (d Date) Day() int            { return d.d }
func (d Date) IsZero() bool        { return d.y == 0 }
func (d Date) DaysInMonth() int    { return daysIn(d.y, d.m) }
func (d Date) utc() time.Time      { return time.Date(d.y, d.m, d.d, 0, 0, 0, 0, time.UTC) }
func (d Date) StartJST() time.Time { return time.Date(d.y, d.m, d.d, 0, 0, 0, 0, JST) }

// AtJST はその日の hour:min（JST）。例: リトライの 10:00 JST。
func (d Date) AtJST(hour, minute int) time.Time {
	return time.Date(d.y, d.m, d.d, hour, minute, 0, 0, JST)
}

func (d Date) AddDays(n int) Date {
	t := d.utc().AddDate(0, 0, n)
	return Date{y: t.Year(), m: t.Month(), d: t.Day()}
}

// AddMonthsClamped は months か月後の、日 = min(anchorDay, その月の日数) の日付。anchorDay は 1〜31。
func (d Date) AddMonthsClamped(months, anchorDay int) Date {
	if anchorDay < 1 || anchorDay > 31 {
		panic(fmt.Sprintf("sharedkernel: anchorDay out of range: %d", anchorDay))
	}
	total := d.y*12 + int(d.m-1) + months
	y, m := total/12, time.Month(total%12+1)
	return Date{y: y, m: m, d: min(anchorDay, daysIn(y, m))}
}

func (d Date) Compare(o Date) int {
	switch {
	case d.y != o.y:
		return cmpInt(d.y, o.y)
	case d.m != o.m:
		return cmpInt(int(d.m), int(o.m))
	default:
		return cmpInt(d.d, o.d)
	}
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (d Date) Before(o Date) bool { return d.Compare(o) < 0 }
func (d Date) After(o Date) bool  { return d.Compare(o) > 0 }
func (d Date) Equal(o Date) bool  { return d.Compare(o) == 0 }

// DaysUntil は d から o までの日数（o が後なら正）。
func (d Date) DaysUntil(o Date) int {
	return int(o.utc().Sub(d.utc()).Hours() / 24)
}

func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.y, d.m, d.d)
}

func (d Date) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func (d *Date) UnmarshalText(b []byte) error {
	v, err := ParseDate(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// Scan は PostgreSQL の date を受け取る。
func (d *Date) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*d = Date{}
		return nil
	case time.Time:
		*d = Date{y: v.Year(), m: v.Month(), d: v.Day()} // date はタイムゾーンを持たないので暦日をそのまま使う
		return nil
	case string:
		return d.UnmarshalText([]byte(v))
	case []byte:
		return d.UnmarshalText(v)
	default:
		return fmt.Errorf("%w: cannot scan %T", ErrInvalidDate, src)
	}
}

func (d Date) Value() (driver.Value, error) {
	if d.IsZero() {
		return nil, nil
	}
	return d.String(), nil
}
