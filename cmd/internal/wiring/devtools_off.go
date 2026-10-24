//go:build !devtools

package wiring

import (
	"context"
	"crypto/ecdsa"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
)

func newClock(_ *db.Pool) sk.Clock { return sk.SystemClock{} }

// registerDev は本番のビルドでは何もしない（/v1/dev/* は 404。AC-PLT-042）。
func registerDev(_ *httpx.Router, _ *Base, _ *ecdsa.PrivateKey) error { return nil }

func platformParts(b *Base, process string, schemas []string) ([]messaging.ConsumerSpec, []scheduler.Job) {
	if process != "worker-billing" {
		return nil, nil
	}
	// 配線の確認用キューの購読者は本番のビルドでも登録する（イベントは devtools の API からしか発行されない）
	return []messaging.ConsumerSpec{{Name: "platform.smoke", Handler: func(_ context.Context, _ sk.EventEnvelope) error { return nil }}},
		scheduler.PlatformJobs(b.Pool, schemas)
}
