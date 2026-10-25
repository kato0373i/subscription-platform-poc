package db

import (
	"context"
	"errors"

	sk "subscription-platform/internal/sharedkernel"
)

var (
	// ErrNoScope はスコープを設定せずに InTx を呼んだ（fail closed。E1）。
	ErrNoScope = errors.New("db: no scope in context")
	// ErrConflictingScope は system と tenant/customer のスコープを併用した。
	ErrConflictingScope = errors.New("db: system scope cannot be combined with tenant/customer scope")
)

// Scope は DB トランザクションに設定する「誰の権限で読むか」。
type Scope struct {
	Tenant       *sk.TenantID
	Customer     *sk.CustomerAccountID
	System       bool
	SystemReason string
	// Unscoped は「テナント・契約者を確定する前」の読み取り（API キーの認証、初回ログイン）。
	// sp_<m> で動き、app.tenant_id・app.customer_account_id は空なので RLS のある表は 0 行になる（安全側）。
	Unscoped bool
}

func (s Scope) empty() bool { return s.Tenant == nil && s.Customer == nil && !s.System && !s.Unscoped }

func (s Scope) validate() error {
	if s.empty() {
		return ErrNoScope
	}
	if s.System && (s.Tenant != nil || s.Customer != nil) {
		return ErrConflictingScope
	}
	return nil
}

type scopeKey struct{}

func scopeOf(ctx context.Context) Scope {
	s, _ := ctx.Value(scopeKey{}).(Scope)
	return s
}

// WithTenantScope は tenant スコープを設定する。system スコープは外す（規約 M5: 対象を特定したら切り替える）。
func WithTenantScope(ctx context.Context, t sk.TenantID) context.Context {
	s := scopeOf(ctx)
	s.Tenant, s.System, s.SystemReason = &t, false, ""
	return context.WithValue(ctx, scopeKey{}, s)
}

// WithCustomerScope は customer スコープを設定する。tenant と併用できる（ポリシーは OR）。
func WithCustomerScope(ctx context.Context, c sk.CustomerAccountID) context.Context {
	s := scopeOf(ctx)
	s.Customer, s.System, s.SystemReason = &c, false, ""
	return context.WithValue(ctx, scopeKey{}, s)
}

// WithSystemScope は sp_<m>_sys（BYPASSRLS）で動くスコープ。reason は必須でログに出す。
// ジョブ・ops ハンドラ・テナントを跨ぐ購読者だけが使う（規約 M5）。tenant/customer の上に重ねると InTx が ErrConflictingScope を返す。
func WithSystemScope(ctx context.Context, reason string) context.Context {
	if reason == "" {
		panic("db: WithSystemScope requires a reason")
	}
	s := scopeOf(ctx)
	s.System, s.SystemReason = true, reason
	return context.WithValue(ctx, scopeKey{}, s)
}

// ClearScope はスコープを外す。他モジュールの contracts を順に呼ぶ Orchestration が、自分の表を触るときだけスコープを付け直すために使う
// （contracts は自分でスコープを決めるので、呼び出し元の tenant / customer スコープを引き継ぐと system スコープと衝突する）。
func ClearScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopeKey{}, Scope{})
}

// WithUnscoped は主体を確定する前の処理（API キーの認証・初回ログインのアカウント作成）だけが使う。
// RLS を越えない sp_<m> で動くので、RLS のある表は読めない。必要な行は SECURITY DEFINER 関数か RLS のない表から読む。
func WithUnscoped(ctx context.Context, reason string) context.Context {
	if reason == "" {
		panic("db: WithUnscoped requires a reason")
	}
	s := scopeOf(ctx)
	if s.empty() {
		s.Unscoped = true
	}
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFrom は context のスコープを返す。
func ScopeFrom(ctx context.Context) (Scope, bool) {
	s := scopeOf(ctx)
	return s, !s.empty()
}
