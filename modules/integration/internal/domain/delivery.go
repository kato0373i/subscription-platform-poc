package domain

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// DeliveryTag は配信の ID の型の印。
type DeliveryTag struct{}

// DeliveryID は配信の ID。
type DeliveryID = sk.ID[DeliveryTag]

// DeliveryStatus は配信の状態（§3.3）。
type DeliveryStatus string

const (
	DeliveryPending   DeliveryStatus = "pending"
	DeliverySucceeded DeliveryStatus = "succeeded"
	DeliveryFailed    DeliveryStatus = "failed"
	DeliveryAbandoned DeliveryStatus = "abandoned"
)

// Trigger は配信のきっかけ。
type Trigger string

const (
	TriggerAuto   Trigger = "auto"
	TriggerManual Trigger = "manual"
)

// ErrorKind は試行の失敗の種類。
type ErrorKind string

const (
	ErrTimeout     ErrorKind = "timeout"
	ErrConnection  ErrorKind = "connection"
	ErrTLS         ErrorKind = "tls"
	ErrSSRFBlocked ErrorKind = "ssrf_blocked"
	ErrHTTPStatus  ErrorKind = "http_status"
)

// ErrNotPending は終端の配信を変えようとした。
var ErrNotPending = errors.New("integration: delivery is not pending")

// Schedule はバックオフ（§8.2）。Waits の最後の値を繰り返す。
type Schedule struct {
	Waits     []time.Duration
	GiveUp    time.Duration
	Jitter    float64 // 0〜0.1
	randFloat func() float64
}

// DefaultSchedule は本番の値（1 分, 5 分, 30 分, 2 時間, 5 時間, 10 時間, 12 時間…、72 時間で打ち切り）。
func DefaultSchedule() Schedule {
	return Schedule{
		Waits:  []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 5 * time.Hour, 10 * time.Hour, 12 * time.Hour},
		GiveUp: 72 * time.Hour, Jitter: 0.1,
	}
}

// ParseSchedule は WEBHOOK_RETRY_SCHEDULE（カンマ区切りの期間）を読む。
func ParseSchedule(s string, giveUp time.Duration, jitter float64) (Schedule, error) {
	var waits []time.Duration
	for _, p := range strings.Split(s, ",") {
		d, err := time.ParseDuration(strings.TrimSpace(p))
		if err != nil || d <= 0 {
			return Schedule{}, fmt.Errorf("integration: retry schedule %q", s)
		}
		waits = append(waits, d)
	}
	if giveUp <= 0 || jitter < 0 || jitter > 0.1 {
		return Schedule{}, errors.New("integration: retry give-up / jitter out of range")
	}
	return Schedule{Waits: waits, GiveUp: giveUp, Jitter: jitter}, nil
}

// Wait は n 回目（1 始まり）の失敗の後の待ち時間（ジッターを含む）。
func (s Schedule) Wait(n int) time.Duration {
	w := s.Waits[min(n, len(s.Waits))-1]
	if s.Jitter == 0 {
		return w
	}
	f := rand.Float64 //nolint:gosec // ジッターに暗号論的な乱数は要らない
	if s.randFloat != nil {
		f = s.randFloat
	}
	return time.Duration(float64(w) * (1 + s.Jitter*(2*f()-1)))
}

// AttemptResult は 1 回の試行の結果。
type AttemptResult struct {
	RequestedAt time.Time
	Duration    time.Duration
	StatusCode  *int
	ErrorKind   ErrorKind // 成功なら ""
	Excerpt     string    // 先頭 1 KB
}

// OK は 2xx の応答か。
func (r AttemptResult) OK() bool {
	return r.ErrorKind == "" && r.StatusCode != nil && *r.StatusCode >= 200 && *r.StatusCode < 300
}

// Delivery は 1 つの外部イベントを 1 つの送信先に届ける単位（集約ルート。§3.3）。
type Delivery struct {
	ID               DeliveryID
	TenantID         sk.TenantID
	EndpointID       EndpointID
	EventID          string
	Trigger          Trigger
	RedeliveryOf     *DeliveryID
	Status           DeliveryStatus
	AttemptCount     int
	NextAttemptAt    *time.Time
	FirstAttemptedAt *time.Time
	LastAttemptedAt  *time.Time
	DeadlineAt       *time.Time
	LastStatusCode   *int
	LastError        string
	CompletedAt      *time.Time
	Version          int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// NewAutoDelivery は外部イベントの作成で作る配信（next_attempt_at = now）。
func NewAutoDelivery(ep *Endpoint, eventID string, now time.Time) *Delivery {
	return &Delivery{
		ID: sk.NewID[DeliveryTag](), TenantID: ep.TenantID, EndpointID: ep.ID, EventID: eventID, Trigger: TriggerAuto, Status: DeliveryPending,
		NextAttemptAt: &now, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

// NewManualDelivery は手動再送で作る新しい配信（元の配信は変えない）。
func NewManualDelivery(src *Delivery, now time.Time) *Delivery {
	id := src.ID
	return &Delivery{
		ID: sk.NewID[DeliveryTag](), TenantID: src.TenantID, EndpointID: src.EndpointID, EventID: src.EventID, Trigger: TriggerManual,
		RedeliveryOf: &id, Status: DeliveryPending, NextAttemptAt: &now, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func (d *Delivery) attempted(r AttemptResult) {
	d.AttemptCount++
	at := r.RequestedAt
	if d.FirstAttemptedAt == nil {
		d.FirstAttemptedAt = &at
	}
	d.LastAttemptedAt = &at
	d.LastStatusCode = r.StatusCode
	d.LastError = string(r.ErrorKind)
}

func (d *Delivery) finish(st DeliveryStatus, now time.Time) {
	d.Status, d.NextAttemptAt, d.CompletedAt, d.UpdatedAt = st, nil, &now, now
	d.Version++
}

// Record は試行の結果を反映する（§3.3 の状態遷移）。deadline は最初の試行 + 打ち切り時間。
func (d *Delivery) Record(r AttemptResult, now time.Time, s Schedule) error {
	if d.Status != DeliveryPending {
		return ErrNotPending
	}
	d.attempted(r)
	if d.DeadlineAt == nil {
		dl := d.FirstAttemptedAt.Add(s.GiveUp)
		d.DeadlineAt = &dl
	}
	if r.OK() {
		d.finish(DeliverySucceeded, now)
		return nil
	}
	next := now.Add(s.Wait(d.AttemptCount))
	if next.After(*d.DeadlineAt) {
		d.finish(DeliveryFailed, now) // E9：予定時刻 ≤ deadline なら試行する
		return nil
	}
	d.NextAttemptAt, d.UpdatedAt = &next, now
	d.Version++
	return nil
}

// Abandon は送信先が disabled・削除されたときに送らずに閉じる。
func (d *Delivery) Abandon(now time.Time) error {
	if d.Status != DeliveryPending {
		return ErrNotPending
	}
	d.finish(DeliveryAbandoned, now)
	return nil
}

// Postpone はブレーカーが開いている間の見送り（attempt_count は変えない）。
func (d *Delivery) Postpone(until, now time.Time) error {
	if d.Status != DeliveryPending {
		return ErrNotPending
	}
	d.NextAttemptAt, d.UpdatedAt = &until, now
	d.Version++
	return nil
}
