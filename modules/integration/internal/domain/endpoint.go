// Package domain は事業者 Webhook とメールのドメイン（integration.md §3）。DB・HTTP・AWS を知らない純粋なロジックだけを置く。
package domain

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"slices"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// EndpointTag は送信先の ID の型の印。
type EndpointTag struct{}

// EndpointID は送信先の ID。
type EndpointID = sk.ID[EndpointTag]

// EndpointStatus は送信先の状態。
type EndpointStatus string

const (
	EndpointEnabled  EndpointStatus = "enabled"
	EndpointDisabled EndpointStatus = "disabled"
)

// MaxEndpointsPerTenant は削除されていない送信先の上限（§3.1.2）。
const MaxEndpointsPerTenant = 10

// MaxGraceHours はローテーションの移行期間の上限（§7.1）。
const MaxGraceHours = 168

var (
	ErrEventTypesEmpty   = errors.New("integration: event_types is empty")
	ErrEventTypeUnknown  = errors.New("integration: unknown event type")
	ErrDescriptionLength = errors.New("integration: description too long")
	ErrGracePeriod       = errors.New("integration: grace_period_hours out of range")
)

// Secret は署名鍵 "whsec_" + 32 バイトの乱数（base64url）。平文を返すのは作成とローテーションの応答だけ。
type Secret string

// NewSecret は新しい署名鍵を作る。
func NewSecret() Secret {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read は失敗しない（Go 1.24〜）
	return Secret("whsec_" + base64.RawURLEncoding.EncodeToString(b))
}

// BreakerConfig はサーキットブレーカーの設定（§8.6）。
type BreakerConfig struct {
	Threshold int
	OpenFor   time.Duration
}

// DefaultBreaker は連続 5 回の失敗で 60 秒開く。
var DefaultBreaker = BreakerConfig{Threshold: 5, OpenFor: 60 * time.Second}

// Endpoint は送信先（集約ルート。§3.1）。鍵は復号した平文で持つ（暗号化はリポジトリが行う）。
type Endpoint struct {
	ID                EndpointID
	TenantID          sk.TenantID
	URL               string
	Description       string
	EventTypes        []string
	Status            EndpointStatus
	APIVersion        string
	Current           Secret
	Previous          *Secret
	PreviousExpiresAt *time.Time
	RotatedAt         *time.Time
	DeletedAt         *time.Time
	Version           int
	BreakerFailures   int
	BreakerOpenUntil  *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NormalizeEventTypes は event_types を検証し、重複を 1 つにまとめる（E18）。
func NormalizeEventTypes(ts []string) ([]string, error) {
	if len(ts) == 0 {
		return nil, ErrEventTypesEmpty
	}
	var out []string
	for _, t := range ts {
		if !KnownEventType(t) {
			return nil, ErrEventTypeUnknown
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// NewEndpoint は鍵を 1 つ生成し、作る（URL の検証は呼び出し元が ValidateURL で行う）。
func NewEndpoint(tenant sk.TenantID, url string, eventTypes []string, desc string, enabled bool, now time.Time) (*Endpoint, error) {
	ts, err := NormalizeEventTypes(eventTypes)
	if err != nil {
		return nil, err
	}
	if len([]rune(desc)) > 256 {
		return nil, ErrDescriptionLength
	}
	st := EndpointEnabled
	if !enabled {
		st = EndpointDisabled
	}
	return &Endpoint{
		ID: sk.NewID[EndpointTag](), TenantID: tenant, URL: url, Description: desc, EventTypes: ts, Status: st, APIVersion: APIVersion,
		Current: NewSecret(), Version: 1, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// Patch は部分更新の入力（nil は変えない）。
type Patch struct {
	URL         *string
	EventTypes  []string
	Description *string
	Enabled     *bool
}

// Update は部分更新（§3.1.3）。
func (e *Endpoint) Update(p Patch, now time.Time) error {
	if p.EventTypes != nil {
		ts, err := NormalizeEventTypes(p.EventTypes)
		if err != nil {
			return err
		}
		e.EventTypes = ts
	}
	if p.Description != nil {
		if len([]rune(*p.Description)) > 256 {
			return ErrDescriptionLength
		}
		e.Description = *p.Description
	}
	if p.URL != nil {
		e.URL = *p.URL
	}
	if p.Enabled != nil {
		e.Status = EndpointDisabled
		if *p.Enabled {
			e.Status = EndpointEnabled
		}
	}
	e.UpdatedAt = now
	return nil
}

// RotateSecret は previous ← current（既存の previous は捨てる）、current ← 新しい鍵（E7・E8）。
func (e *Endpoint) RotateSecret(now time.Time, graceHours int) error {
	if graceHours < 0 || graceHours > MaxGraceHours {
		return ErrGracePeriod
	}
	if graceHours == 0 {
		e.Previous, e.PreviousExpiresAt = nil, nil
	} else {
		prev, exp := e.Current, now.Add(time.Duration(graceHours)*time.Hour)
		e.Previous, e.PreviousExpiresAt = &prev, &exp
	}
	e.Current = NewSecret()
	e.RotatedAt, e.UpdatedAt = &now, now
	return nil
}

// ActiveSecrets は署名に使う鍵（current と期限内の previous。§3.1.2 の遅延評価）。
func (e *Endpoint) ActiveSecrets(now time.Time) []string {
	out := []string{string(e.Current)}
	if e.Previous != nil && e.PreviousExpiresAt != nil && now.Before(*e.PreviousExpiresAt) {
		out = append(out, string(*e.Previous))
	}
	return out
}

// Deliverable は配信してよい状態か（enabled かつ未削除）。
func (e *Endpoint) Deliverable() bool { return e.Status == EndpointEnabled && e.DeletedAt == nil }

// Subscribes は外部 type を受け取るか。
func (e *Endpoint) Subscribes(t string) bool {
	return e.Deliverable() && slices.Contains(e.EventTypes, t)
}

// Delete は論理削除する。
func (e *Endpoint) Delete(now time.Time) { e.DeletedAt, e.UpdatedAt = &now, now }

// BreakerAllows はブレーカーが閉じているか（open_until が過去なら次の 1 件が半開の試し）。
func (e *Endpoint) BreakerAllows(now time.Time) bool {
	return e.BreakerOpenUntil == nil || !e.BreakerOpenUntil.After(now)
}

// RecordDeliveryResult はブレーカーを更新する（version は上げない。§8.6）。
func (e *Endpoint) RecordDeliveryResult(ok bool, now time.Time, cfg BreakerConfig) {
	if ok {
		e.BreakerFailures, e.BreakerOpenUntil = 0, nil
		return
	}
	e.BreakerFailures++
	if e.BreakerFailures >= cfg.Threshold {
		until := now.Add(cfg.OpenFor)
		e.BreakerOpenUntil = &until
	}
}
