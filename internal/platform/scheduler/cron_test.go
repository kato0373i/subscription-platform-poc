package scheduler_test

import (
	"testing"
	"time"

	"subscription-platform/internal/platform/scheduler"
)

func TestCron(t *testing.T) {
	c, err := scheduler.ParseCron("cron(30 0 * * ? *)")
	if err != nil {
		t.Fatal(err)
	}
	// 00:30 JST = 前日 15:30 UTC
	if !c.Matches(time.Date(2026, 10, 11, 15, 30, 0, 0, time.UTC)) || c.Matches(time.Date(2026, 10, 11, 0, 30, 0, 0, time.UTC)) {
		t.Fatal("00:30 JST の判定が違う")
	}
	h, _ := scheduler.ParseCron("cron(0/15 * ? * MON-FRI *)")
	if !h.Matches(time.Date(2026, 10, 12, 1, 45, 0, 0, time.UTC)) { // 月曜 10:45 JST
		t.Fatal("15 分おき・平日の判定が違う")
	}
	if h.Matches(time.Date(2026, 10, 11, 1, 45, 0, 0, time.UTC)) { // 日曜
		t.Fatal("日曜に起動した")
	}
	for _, bad := range []string{"30 0 * * ? *", "cron(30 0 * * *)", "cron(30 0 1 * 2 *)", "cron(61 0 * * ? *)"} {
		if _, err := scheduler.ParseCron(bad); err == nil {
			t.Errorf("通ってしまった: %s", bad)
		}
	}
}

func TestSchedules(t *testing.T) {
	s, err := scheduler.ParseSchedules([]byte(`
jobs:
  - name: platform-idempotency-gc
    process: worker-billing
    schedule: "cron(0 3 * * ? *)"
  - name: platform-dev-seed-subscriptions
    process: worker-billing
    dedupe: none
    devtools: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := s.Job("platform-idempotency-gc"); j.Dedupe != "daily" {
		t.Fatalf("dedupe = %q", j.Dedupe)
	}
	if scheduler.SchedulerQueue("worker-billing") != "scheduler-billing" {
		t.Fatal("SchedulerQueue")
	}
	if _, err := scheduler.ParseSchedules([]byte("jobs:\n  - {name: Bad_Name, process: api}")); err == nil {
		t.Fatal("不正な定義が通った")
	}
}
