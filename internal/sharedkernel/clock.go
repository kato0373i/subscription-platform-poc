package sharedkernel

import (
	"sync"
	"time"
)

// Clock は業務時刻の取得元。業務の判定は必ずここから時刻を取る（規約 M1）。
type Clock interface {
	Now() time.Time
	Today() Date
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }
func (c SystemClock) Today() Date  { return DateOf(c.Now()) }

// ManualClock はテスト用の手で動かす時計。
type ManualClock struct {
	mu sync.Mutex
	t  time.Time
}

func NewManualClock(t time.Time) *ManualClock { return &ManualClock{t: t.UTC()} }

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *ManualClock) Today() Date { return DateOf(c.Now()) }

func (c *ManualClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t.UTC()
	c.mu.Unlock()
}

func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
