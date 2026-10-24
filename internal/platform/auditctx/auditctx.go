// Package auditctx は監査注釈を context に積む（platform.md §3.2・§4、P-X-2）。標準ライブラリと sharedkernel だけに依存する。
// 監査ミドルウェアが「袋」を用意し、ユースケースが Annotate で「何を・どう変えたか」を書く。袋がなければ何もしない。
package auditctx

import (
	"context"
	"strings"
	"sync"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// Target は操作の対象（例 {"plan", "<uuid>"}）。
type Target struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Annotation はユースケースが積む注釈 1 件。
type Annotation struct {
	Action string
	Target Target
	Before map[string]any
	After  map[string]any
	Tenant *sk.TenantID
}

// Option は注釈の設定。
type Option func(*Annotation)

// WithTenant は ops の操作で対象の事業者を明示する。
func WithTenant(t sk.TenantID) Option { return func(a *Annotation) { a.Tenant = &t } }

// Record は AuditWriter に渡す 1 件（integration §3.6 の AuditRecord に対応）。
type Record struct {
	OccurredAt    time.Time
	TenantID      *sk.TenantID
	ActorType     string
	ActorID       string
	ActorRole     string
	Action        string
	Target        Target
	Before, After map[string]any
	Outcome       string // succeeded | denied
	RequestID     string
	CorrelationID string
	HTTPMethod    string
	Route         string
	SourceIP      string
}

type bag struct {
	mu   sync.Mutex
	anns []Annotation
}

type bagKey struct{}

// NewBag は袋を用意する（監査ミドルウェア専用）。
func NewBag(ctx context.Context) context.Context { return context.WithValue(ctx, bagKey{}, &bag{}) }

// Annotate は成功した変更の直後に呼ぶ（規約 M8）。before/after のうち秘密・個人情報のキーは "***" に置き換える（安全網）。
func Annotate(ctx context.Context, action string, target Target, before, after map[string]any, opts ...Option) {
	b, ok := ctx.Value(bagKey{}).(*bag)
	if !ok {
		return
	}
	a := Annotation{Action: action, Target: target, Before: Mask(before), After: Mask(after)}
	for _, o := range opts {
		o(&a)
	}
	b.mu.Lock()
	b.anns = append(b.anns, a)
	b.mu.Unlock()
}

// Drain は積まれた注釈を取り出す（監査ミドルウェア専用）。
func Drain(ctx context.Context) []Annotation {
	b, ok := ctx.Value(bagKey{}).(*bag)
	if !ok {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.anns
	b.anns = nil
	return out
}

var maskedKeys = []string{"secret", "token", "password", "email", "name"}

func masked(k string) bool {
	k = strings.ToLower(k)
	if k == "tenant_name" || k == "display_name_changed" {
		return false
	}
	for _, m := range maskedKeys {
		if strings.Contains(k, m) {
			return true
		}
	}
	return false
}

// Mask は秘密・個人情報のキー（secret・token・password・email・name を含む）の値を "***" にする。入れ子の map も対象。
func Mask(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch {
		case masked(k):
			out[k] = "***"
		default:
			if inner, ok := v.(map[string]any); ok {
				out[k] = Mask(inner)
			} else {
				out[k] = v
			}
		}
	}
	return out
}
