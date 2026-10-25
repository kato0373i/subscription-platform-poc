// Package audit は監査記録のモジュール（integration.md §3.6・§4.4）。cmd/** からだけ import する（規約 M7）。
// 書き込みは cmd/api が監査ミドルウェアに関数として渡す（platform は modules を import しない。P-X-2）。
package audit

import (
	"context"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	audithttp "subscription-platform/modules/audit/internal/adapter/http"
	"subscription-platform/modules/audit/internal/app"
)

// Name はスキーマ名。
const Name = "audit"

// Module は audit モジュール。
type Module struct {
	svc     *app.Service
	cursors *httpx.CursorCodec
}

// New はモジュールを組み立てる。
func New(pool *db.Pool, cursors *httpx.CursorCodec) *Module {
	return &Module{svc: app.New(pool.Module(Name)), cursors: cursors}
}

// Write は httpx.AuditWriter（§3.6.2 のマスキングを適用して audit.audit_records に INSERT する）。
func (m *Module) Write(ctx context.Context, recs []auditctx.Record) error {
	return m.svc.Write(ctx, recs)
}

// MountHTTP は ops の閲覧 API を登録する。
func (m *Module) MountHTTP(r *httpx.Router) error { return audithttp.Register(r, m.svc, m.cursors) }
