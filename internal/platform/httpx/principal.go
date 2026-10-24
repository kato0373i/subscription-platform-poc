package httpx

import (
	"context"

	sk "subscription-platform/internal/sharedkernel"
)

// PrincipalKind は認証主体の種類。
type PrincipalKind string

const (
	KindConsumer  PrincipalKind = "consumer"
	KindStaff     PrincipalKind = "staff"
	KindTenantAPI PrincipalKind = "tenant_api"
	KindOperator  PrincipalKind = "operator"
)

// Principal は認証済みの主体。
type Principal struct {
	Kind              PrincipalKind
	Subject           string // JWT sub / API キーの key_prefix / 運営者の sub
	TenantID          *sk.TenantID
	CustomerAccountID *sk.CustomerAccountID
	Role              string // staff のみ
}

type principalKey struct{}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom は認証済みの主体を返す。
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// ConsumerID は契約者のアカウント ID。主体が consumer でなければ panic（ルートで保証済みのため）。
func ConsumerID(ctx context.Context) sk.CustomerAccountID {
	p, ok := PrincipalFrom(ctx)
	if !ok || p.CustomerAccountID == nil {
		panic("httpx: ConsumerID called without consumer principal")
	}
	return *p.CustomerAccountID
}

// TenantID は staff・tenant_api のテナント。なければ panic。
func TenantID(ctx context.Context) sk.TenantID {
	p, ok := PrincipalFrom(ctx)
	if !ok || p.TenantID == nil {
		panic("httpx: TenantID called without tenant principal")
	}
	return *p.TenantID
}

type requestIDKey struct{}

func requestIDFrom(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey{}).(string)
	return s
}

// RequestID はこのリクエストの X-Request-Id。
func RequestID(ctx context.Context) string { return requestIDFrom(ctx) }
