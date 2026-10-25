//go:build integration

package app_test

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/tenant/contracts"
	tenantdb "subscription-platform/modules/tenant/db"
	"subscription-platform/modules/tenant/internal/app"
	"subscription-platform/modules/tenant/internal/domain"
)

func TestMain(m *testing.M) {
	pgtest.Main(m, migrate.Source{Schema: "tenant", FS: migrate.SubFS(tenantdb.Migrations, "migrations")})
}

var clock = sk.NewManualClock(time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC))

func newService(d *pgtest.DB) *app.Service {
	return app.New(d.Pool.Module("tenant"), outbox.NewWriter("tenant", clock), clock, rand.Reader, nil)
}

func ops() context.Context { return db.WithSystemScope(context.Background(), "operator") }

func console(t sk.TenantID) context.Context { return db.WithTenantScope(context.Background(), t) }

// tenantWith は承認済みのテナントと、指定のロールのスタッフを作る。
func tenantWith(t *testing.T, svc *app.Service, roles ...contracts.Role) (sk.TenantID, []domain.StaffState) {
	t.Helper()
	st, err := svc.RegisterTenant(ops(), "ACME")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveTenant(ops(), st.ID, "shop_"+strings.ReplaceAll(st.ID.String(), "-", "")); err != nil {
		t.Fatal(err)
	}
	var staff []domain.StaffState
	for i, r := range roles {
		sub := st.ID.String() + "-" + string(r) + "-" + string(rune('a'+i))
		m, err := svc.CreateStaff(ops(), st.ID, sub, sub+"@example.com", "スタッフ", r)
		if err != nil {
			t.Fatal(err)
		}
		staff = append(staff, m)
	}
	return st.ID, staff
}

// AC-IDN-007
func TestGetTenant(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	ctx := context.Background()
	pending, err := svc.RegisterTenant(ops(), "保留中")
	if err != nil {
		t.Fatal(err)
	}
	info, err := svc.GetTenant(ctx, pending.ID)
	if err != nil || info.Status != contracts.TenantPendingReview || info.FincodeShopID != "" || info.Name != "保留中" {
		t.Errorf("pending: %+v %v", info, err)
	}
	active, _ := tenantWith(t, svc)
	if info, err := svc.GetTenant(ctx, active); err != nil || info.Status != contracts.TenantActive || info.FincodeShopID == "" {
		t.Errorf("active: %+v %v", info, err)
	}
	if _, err := svc.SuspendTenant(ops(), active, "違反"); err != nil {
		t.Fatal(err)
	}
	if info, err := svc.GetTenant(ctx, active); err != nil || info.Status != contracts.TenantSuspended {
		t.Errorf("suspended: %+v %v", info, err)
	}
	if _, err := svc.GetTenant(ctx, sk.NewID[sk.TenantTag]()); !errors.Is(err, contracts.ErrTenantNotFound) {
		t.Errorf("存在しない: %v", err)
	}
}

// AC-IDN-012: 2 名の owner がお互いを同時に降格すると、片方だけ成功する（E1）
func TestConcurrentDemotionKeepsOneOwner(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	for range 5 {
		tid, staff := tenantWith(t, svc, contracts.RoleOwner, contracts.RoleOwner)
		x, y := staff[0], staff[1]
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, target := range []domain.StaffState{y, x} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = svc.ChangeStaffRole(console(tid), tid, target.ID, contracts.RoleAdmin, nil)
			}()
		}
		wg.Wait()
		failed := 0
		for _, err := range errs {
			if errors.Is(err, domain.ErrLastOwner) {
				failed++
			} else if err != nil {
				t.Fatalf("想定外のエラー: %v", err)
			}
		}
		if failed != 1 {
			t.Fatalf("片方だけ失敗するはず: %v", errs)
		}
		if n := d.QueryInt(t, `SELECT count(*) FROM tenant.staff_members WHERE tenant_id = $1 AND role = 'owner'`, tid.UUID()); n != 1 {
			t.Fatalf("owner = %d", n)
		}
	}
}

// E2: API キーの同時発行でも上限を超えない
func TestConcurrentIssueRespectsLimit(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	tid, _ := tenantWith(t, svc, contracts.RoleOwner)
	var wg sync.WaitGroup
	for range 15 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.IssueAPIKey(console(tid), tid, "k", "")
		}()
	}
	wg.Wait()
	if n := d.QueryInt(t, `SELECT count(*) FROM tenant.api_keys WHERE tenant_id = $1 AND revoked_at IS NULL`, tid.UUID()); n != domain.MaxActiveAPIKeys {
		t.Fatalf("有効なキー = %d", n)
	}
}

// AC-IDN-014: tenant スコープでは自テナントの行だけ、未設定なら 0 行
func TestRowLevelSecurity(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	a, _ := tenantWith(t, svc, contracts.RoleOwner)
	b, _ := tenantWith(t, svc, contracts.RoleOwner, contracts.RoleAdmin)
	for _, tid := range []sk.TenantID{a, b} {
		if _, err := svc.IssueAPIKey(console(tid), tid, "k", ""); err != nil {
			t.Fatal(err)
		}
	}
	count := func(ctx context.Context, table string) int {
		var n int
		err := d.Pool.Module("tenant").ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for table, want := range map[string]int{"tenant.staff_members": 1, "tenant.api_keys": 1, "tenant.tenants": 1} {
		if got := count(console(a), table); got != want {
			t.Errorf("%s（テナント A）= %d, want %d", table, got, want)
		}
		if got := count(db.WithUnscoped(context.Background(), "test"), table); got != 0 {
			t.Errorf("%s（未設定）= %d", table, got)
		}
	}
	pgtest.AssertAllTenantTablesHaveRLS(t, d)
}

// AC-IDN-017・018: SECURITY DEFINER 関数での認証
func TestAuthenticateAPIKey(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	a, _ := tenantWith(t, svc, contracts.RoleOwner)
	k, err := svc.IssueAPIKey(console(a), a, "本番", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.AuthenticateAPIKey(context.Background(), k.Raw)
	if err != nil || got != a {
		t.Fatalf("有効なキー: %v %v", got, err)
	}
	// アプリのロールで api_keys を直接読むと 0 行（関数を通さないと読めない）
	var n int
	err = d.Pool.Module("tenant").ReadTx(db.WithUnscoped(context.Background(), "test"), func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM tenant.api_keys`).Scan(&n)
	})
	if err != nil || n != 0 {
		t.Errorf("直接の SELECT = %d %v", n, err)
	}

	other, err := svc.IssueAPIKey(console(a), a, "予備", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeAPIKey(console(a), a, other.State.ID, ""); err != nil {
		t.Fatal(err)
	}
	last := k.Raw[len(k.Raw)-1]
	alt := "A"
	if last == 'A' {
		alt = "B"
	}
	for name, raw := range map[string]string{
		"失効済み":            other.Raw,
		"形式不正":            "sk_short",
		"存在しない prefix":    "sk_zzzzzzzzzzzz_" + k.Raw[16:],
		"secret の 1 文字違い": k.Raw[:len(k.Raw)-1] + alt,
	} {
		if _, err := svc.AuthenticateAPIKey(context.Background(), raw); !errors.Is(err, contracts.ErrInvalidAPIKey) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 平文は DB のどの列にも無く、key_hash = SHA-256(平文)（AC-IDN-016 の DB 側）
	if n := d.QueryInt(t, `SELECT count(*) FROM tenant.api_keys t WHERE position($1 in (t.*)::text) > 0`, k.Raw); n != 0 {
		t.Error("平文が保存されている")
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM tenant.api_keys WHERE id = $1 AND key_hash = sha256(convert_to($2, 'UTF8'))`, k.State.ID.UUID(), k.Raw); n != 1 {
		t.Error("key_hash が SHA-256(平文) でない")
	}
}

// AC-IDN-034
func TestListOwnerContacts(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	tid, staff := tenantWith(t, svc, contracts.RoleOwner, contracts.RoleAdmin, contracts.RoleOwner)
	if _, err := svc.SuspendTenant(ops(), tid, "停止"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ListOwnerContacts(context.Background(), tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].StaffID != staff[0].ID.String() || got[1].StaffID != staff[2].ID.String() || got[0].Email == "" || got[0].DisplayName == "" {
		t.Errorf("owners = %+v", got)
	}
	if _, err := svc.ListOwnerContacts(context.Background(), sk.NewID[sk.TenantTag]()); !errors.Is(err, contracts.ErrTenantNotFound) {
		t.Errorf("存在しない: %v", err)
	}
}

// E3・AC-IDN-004 の DB 側: 同じ shop ID は 1 テナントだけ。E4: 承認と停止の競合は楽観ロック
func TestShopIDUniqueAndEvents(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	a, _ := svc.RegisterTenant(ops(), "A")
	b, _ := svc.RegisterTenant(ops(), "B")
	if _, err := svc.ApproveTenant(ops(), a.ID, "shop_same"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveTenant(ops(), b.ID, "shop_same"); err == nil {
		t.Fatal("同じ shop ID で承認できた")
	}
	// 同じ shop ID での再実行はイベントを増やさない（AC-IDN-003）
	if _, err := svc.ApproveTenant(ops(), a.ID, "shop_same"); err != nil {
		t.Fatal(err)
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM tenant.outbox_events WHERE aggregate_id = $1 AND event_type = 'tenant.approved'`, a.ID.UUID()); n != 1 {
		t.Errorf("tenant.approved = %d", n)
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM tenant.outbox_events WHERE tenant_id = $1 AND payload->>'tenant_id' = $1::text`, a.ID.UUID()); n != 1 {
		t.Errorf("envelope の tenant_id と payload: %d", n)
	}
}
