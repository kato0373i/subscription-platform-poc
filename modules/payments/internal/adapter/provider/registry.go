package provider

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"subscription-platform/internal/platform/observability"
	c "subscription-platform/modules/payments/contracts"
)

// Registry は登録済みのアダプター。新規登録に使える組（有効化）は設定 PAYMENTS_METHODS で決める（§4.4）。
type Registry struct {
	adapters map[Key]PaymentProvider
	enabled  map[c.MethodType]ProviderID // 1 つの MethodType につき新規登録に使う Provider は 1 つ
	wrap     func(PaymentProvider) PaymentProvider
}

// NewRegistry はレジストリを作る。enabled は "card:stub,..." 形式。
func NewRegistry(enabled string) (*Registry, error) {
	r := &Registry{adapters: map[Key]PaymentProvider{}, enabled: map[c.MethodType]ProviderID{}}
	for _, part := range strings.Split(enabled, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		m, p, ok := strings.Cut(part, ":")
		if !ok || m == "" || p == "" {
			return nil, fmt.Errorf("payments: PAYMENTS_METHODS: invalid entry %q", part)
		}
		if _, dup := r.enabled[c.MethodType(m)]; dup {
			return nil, fmt.Errorf("payments: PAYMENTS_METHODS: %s is enabled twice", m)
		}
		r.enabled[c.MethodType(m)] = ProviderID(p)
	}
	return r, nil
}

// WithDecorator はアダプターを横断機能（ブレーカー・レート制限）で包む。
func (r *Registry) WithDecorator(wrap func(PaymentProvider) PaymentProvider) { r.wrap = wrap }

// Register はアダプターを登録する。同じキーの二重登録・per_tenant なのに TenantRefProvisioner がないものは起動時に panic。
func (r *Registry) Register(p PaymentProvider) {
	k := p.Key()
	if _, dup := r.adapters[k]; dup {
		panic(fmt.Sprintf("payments: provider %s registered twice", k))
	}
	caps := p.Capabilities()
	if caps.RefSharing == RefPerTenant {
		if _, ok := p.(TenantRefProvisioner); !ok {
			panic(fmt.Sprintf("payments: provider %s is per_tenant but does not implement TenantRefProvisioner", k))
		}
	}
	if caps.AsyncSettlement {
		if _, ok := p.(WebhookParser); !ok {
			observability.Logger("payments.registry").LogAttrs(context.Background(), slog.LevelWarn, "payments.provider_without_webhook",
				slog.String("provider", k.String()))
		}
	}
	r.adapters[k] = p
}

func (r *Registry) decorated(p PaymentProvider) PaymentProvider {
	if r.wrap == nil {
		return p
	}
	return r.wrap(p)
}

// ForRegistration は新規登録に使うアダプター。有効化されていなければ ErrUnsupportedMethod。
func (r *Registry) ForRegistration(m c.MethodType, provider string) (PaymentProvider, error) {
	if r.enabled[m] != ProviderID(provider) {
		return nil, c.ErrUnsupportedMethod
	}
	p, ok := r.adapters[Key{Method: m, Provider: ProviderID(provider)}]
	if !ok {
		return nil, c.ErrUnsupportedMethod
	}
	return r.decorated(p), nil
}

// Get は決済・照会に使うアダプター。登録済みなら、有効化されていなくても使える（代行の移行期間）。
func (r *Registry) Get(k Key) (PaymentProvider, error) {
	p, ok := r.adapters[k]
	if !ok {
		return nil, c.ErrUnsupportedMethod
	}
	return r.decorated(p), nil
}

// ByProvider は Provider 名でアダプターを探す（Webhook・照合。手段をまたいで最初の 1 つ）。
func (r *Registry) ByProvider(provider string) (PaymentProvider, bool) {
	for k, p := range r.adapters {
		if string(k.Provider) == provider {
			return p, true
		}
	}
	return nil, false
}

// All は登録済みのアダプター（日次照合）。
func (r *Registry) All() []PaymentProvider {
	out := make([]PaymentProvider, 0, len(r.adapters))
	for _, p := range r.adapters {
		out = append(out, p)
	}
	return out
}

// Enabled は有効化された組（申込ページの payment-config）。
func (r *Registry) Enabled() []Key {
	var out []Key
	for m, p := range r.enabled {
		if _, ok := r.adapters[Key{Method: m, Provider: p}]; ok {
			out = append(out, Key{Method: m, Provider: p})
		}
	}
	return out
}
