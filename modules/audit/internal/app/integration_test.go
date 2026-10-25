//go:build integration

package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
	auditdb "subscription-platform/modules/audit/db"
	"subscription-platform/modules/audit/internal/app"
)

func TestMain(m *testing.M) {
	pgtest.Main(m, migrate.Source{Schema: "audit", FS: migrate.SubFS(auditdb.Migrations, "migrations")})
}

// AC-INT-029：ランタイムのロール（sp_audit・sp_audit_sys）では UPDATE・DELETE・TRUNCATE できない。所有者でもトリガーで拒否する。
func TestAuditAppendOnly(t *testing.T) {
	d := pgtest.New(t)
	m := d.Pool.Module("audit")
	ctx := db.WithSystemScope(context.Background(), "test")
	id := uuid.New()
	if err := m.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO audit.audit_records (id, occurred_at, actor_type, actor_id, action, outcome, request_id, http_method, route)
			VALUES ($1, now(), 'operator', 'op', 'tenant.suspend', 'succeeded', 'req', 'POST', 'POST /v1/ops/tenants/{id}/suspend')`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`UPDATE audit.audit_records SET action = 'x'`, `DELETE FROM audit.audit_records`, `TRUNCATE audit.audit_records`} {
		for _, sc := range []context.Context{ctx, db.WithTenantScope(context.Background(), sk.NewID[sk.TenantTag]())} {
			err := m.InTx(sc, func(ctx context.Context, tx db.Tx) error {
				_, err := tx.Exec(ctx, stmt)
				return err
			})
			if err == nil {
				t.Errorf("%s が成功した", stmt)
			}
		}
	}
	// 所有者（マイグレーションのロール）でもトリガーで拒否する
	for _, stmt := range []string{`UPDATE audit.audit_records SET action = 'x'`, `DELETE FROM audit.audit_records`, `TRUNCATE audit.audit_records`} {
		if _, err := d.SuperPool(t).Exec(context.Background(), stmt); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: %v", stmt, err)
		}
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM audit.audit_records WHERE id = $1 AND action = 'tenant.suspend'`, id); n != 1 {
		t.Fatal("監査の行が変わった")
	}
}

// Write は before/after をもう一度マスキングし、事業者が決まらない運営者の操作（tenant_id が NULL）も書ける。
func TestWriteMasksAndAllowsNullTenant(t *testing.T) {
	d := pgtest.New(t)
	svc := app.New(d.Pool.Module("audit"))
	tid := sk.NewID[sk.TenantTag]()
	err := svc.Write(context.Background(), []auditctx.Record{
		{TenantID: &tid, ActorType: "staff", ActorID: "s1", ActorRole: "owner", Action: "webhook_endpoint.create", Outcome: "succeeded", RequestID: "r1",
			HTTPMethod: "POST", Route: "POST /v1/console/webhook-endpoints", SourceIP: "10.0.0.1",
			After: map[string]any{"url": "https://e", "secret": "whsec_x", "nested": map[string]any{"email": "a@b"}}},
		{ActorType: "operator", ActorID: "op", Action: "http.post", Outcome: "succeeded", RequestID: "r2", HTTPMethod: "POST", Route: "POST /v1/ops/x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var after string
	if err := d.SuperPool(t).QueryRow(context.Background(), `SELECT after::text FROM audit.audit_records WHERE request_id = 'r1'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, "whsec_x") || strings.Contains(after, "a@b") || !strings.Contains(after, "https://e") {
		t.Fatalf("after = %s", after)
	}
	rows, more, err := svc.List(context.Background(), app.Filter{Limit: 1})
	if err != nil || len(rows) != 1 || !more {
		t.Fatalf("list: %v %d %v", err, len(rows), more)
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM audit.audit_records WHERE tenant_id IS NULL AND request_id = 'r2'`); n != 1 {
		t.Fatal("tenant_id が NULL の行がない")
	}
}
