//go:build integration

package app_test

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db/migrate"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/customer/contracts"
	customerdb "subscription-platform/modules/customer/db"
	"subscription-platform/modules/customer/internal/app"
)

func TestMain(m *testing.M) {
	pgtest.Main(m, migrate.Source{Schema: "customer", FS: migrate.SubFS(customerdb.Migrations, "migrations")})
}

var clock = sk.NewManualClock(time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC))

func newService(d *pgtest.DB) *app.Service {
	return app.New(d.Pool.Module("customer"), outbox.NewWriter("customer", clock), clock)
}

func identity() contracts.AuthIdentity {
	sub := "consumer-" + uuid.NewString()
	return contracts.AuthIdentity{Subject: sub, Email: sub + "@example.com"}
}

func account(t *testing.T, svc *app.Service) sk.CustomerAccountID {
	t.Helper()
	v, err := svc.ResolveOrCreateAccount(context.Background(), identity())
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}

// AC-IDN-022: 未登録の subject で同時に 2 回 → アカウントは 1 件、同じ id（E8）
func TestConcurrentProvisioning(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	for range 5 {
		in := identity()
		var wg sync.WaitGroup
		ids := make([]sk.CustomerAccountID, 2)
		errs := make([]error, 2)
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, err := svc.ResolveOrCreateAccount(context.Background(), in)
				ids[i], errs[i] = v.ID, err
			}()
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil || ids[0] != ids[1] {
			t.Fatalf("ids = %v errs = %v", ids, errs)
		}
		if n := d.QueryInt(t, `SELECT count(*) FROM customer.customer_accounts WHERE subject = $1`, in.Subject); n != 1 {
			t.Fatalf("accounts = %d", n)
		}
	}
}

// J1〜J5 の規則（API での確認は e2e の me-account.spec.ts）
func TestJITRules(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	ctx := context.Background()
	f := false
	in := identity()
	in.EmailVerified = &f
	if _, err := svc.ResolveOrCreateAccount(ctx, in); !errors.Is(err, contracts.ErrEmailUnverified) {
		t.Errorf("J3 未検証: %v", err)
	}
	in = identity()
	in.Email = ""
	if _, err := svc.ResolveOrCreateAccount(ctx, in); !errors.Is(err, contracts.ErrEmailUnverified) {
		t.Errorf("J3 email なし: %v", err)
	}
	first := identity()
	if _, err := svc.ResolveOrCreateAccount(ctx, first); err != nil {
		t.Fatal(err)
	}
	other := identity()
	other.Email = first.Email
	if _, err := svc.ResolveOrCreateAccount(ctx, other); !errors.Is(err, contracts.ErrEmailAlreadyRegistered) {
		t.Errorf("J4: %v", err)
	}
	// J1: email を他の active と同じにしようとしても更新せずに続行する
	second := identity()
	v2, err := svc.ResolveOrCreateAccount(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	second.Email = first.Email
	second.Name = "新しい名前"
	got, err := svc.ResolveOrCreateAccount(ctx, second)
	if err != nil || got.ID != v2.ID || got.Email != v2.Email || got.Name != "新しい名前" {
		t.Errorf("J1 の衝突: %+v %v", got, err)
	}
}

// AC-IDN-026・027・028
func TestTenantCustomerIDs(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	ctx := context.Background()
	x := account(t, svc)
	a, b := sk.NewID[sk.TenantTag](), sk.NewID[sk.TenantTag]()
	if _, err := svc.ResolveTenantCustomerID(ctx, a, x); !errors.Is(err, contracts.ErrLinkNotFound) {
		t.Errorf("リンクなしの Resolve: %v", err)
	}
	var wg sync.WaitGroup
	ids := make([]contracts.TenantCustomerID, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := svc.EnsureTenantCustomerID(ctx, a, x)
			if err != nil {
				t.Error(err)
			}
			ids[i] = id
		}()
	}
	wg.Wait()
	again, err := svc.EnsureTenantCustomerID(ctx, a, x)
	if err != nil || ids[0] != ids[1] || ids[0] != again {
		t.Fatalf("A の ID が揃わない: %v %v %v", ids, again, err)
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM customer.tenant_customer_links WHERE customer_account_id = $1`, x.UUID()); n != 1 {
		t.Errorf("links = %d", n)
	}
	inB, err := svc.EnsureTenantCustomerID(ctx, b, x)
	if err != nil || inB == again {
		t.Errorf("B は別の ID: %v %v", inB, err)
	}
	re := regexp.MustCompile(`^cus_[0-9A-Za-z]{24}$`)
	if !re.MatchString(string(again)) || !re.MatchString(string(inB)) {
		t.Errorf("形式: %s %s", again, inB)
	}
	if _, err := svc.EnsureTenantCustomerID(ctx, a, sk.NewID[sk.CustomerAccountTag]()); !errors.Is(err, contracts.ErrAccountNotFound) {
		t.Errorf("アカウントなし: %v", err)
	}
	if err := svc.Withdraw(ctx, x); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureTenantCustomerID(ctx, sk.NewID[sk.TenantTag](), x); !errors.Is(err, contracts.ErrAccountWithdrawn) {
		t.Errorf("退会済み: %v", err)
	}
}

// AC-IDN-029（DB 側）・030
func TestWithdraw(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	ctx := context.Background()
	in := identity()
	v, err := svc.ResolveOrCreateAccount(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	a := sk.NewID[sk.TenantTag]()
	link, err := svc.EnsureTenantCustomerID(ctx, a, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Withdraw(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Withdraw(ctx, v.ID); err != nil {
		t.Fatalf("再実行: %v", err)
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM customer.customer_accounts WHERE id = $1 AND status = 'withdrawn' AND email IS NULL AND name IS NULL`, v.ID.UUID()); n != 1 {
		t.Error("マスキングされていない")
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM customer.outbox_events WHERE aggregate_id = $1 AND event_type = 'customer.withdrawn' AND tenant_id IS NULL`, v.ID.UUID()); n != 1 {
		t.Errorf("customer.withdrawn = %d（1 件だけ、tenant_id は null）", n)
	}
	if _, err := svc.GetContact(ctx, v.ID); !errors.Is(err, contracts.ErrAccountWithdrawn) {
		t.Errorf("GetContact: %v", err)
	}
	if got, err := svc.ResolveTenantCustomerID(ctx, a, v.ID); err != nil || got != link {
		t.Errorf("Resolve は退会後も従来の ID: %v %v", got, err)
	}
	if _, err := svc.ResolveOrCreateAccount(ctx, in); !errors.Is(err, contracts.ErrAccountWithdrawn) {
		t.Errorf("J2: %v", err)
	}
	// AC-IDN-031: 退会済みの email は新しい subject で使える（部分ユニーク）
	n := identity()
	n.Email = in.Email
	if _, err := svc.ResolveOrCreateAccount(ctx, n); err != nil {
		t.Errorf("退会済みの email の再利用: %v", err)
	}
}

// AC-IDN-032
func TestTenantCustomerProfile(t *testing.T) {
	d := pgtest.New(t)
	svc := newService(d)
	ctx := context.Background()
	in := identity()
	in.Name = "山田"
	v, _ := svc.ResolveOrCreateAccount(ctx, in)
	a, b := sk.NewID[sk.TenantTag](), sk.NewID[sk.TenantTag]()
	link, _ := svc.EnsureTenantCustomerID(ctx, a, v.ID)
	p, err := svc.GetTenantCustomerProfile(ctx, a, v.ID)
	if err != nil || p.TenantCustomerID != link || p.Email != v.Email || p.Name != "山田" || p.Withdrawn {
		t.Errorf("A: %+v %v", p, err)
	}
	if _, err := svc.GetTenantCustomerProfile(ctx, b, v.ID); !errors.Is(err, contracts.ErrLinkNotFound) {
		t.Errorf("B: %v", err)
	}
	_ = svc.Withdraw(ctx, v.ID)
	if p, err := svc.GetTenantCustomerProfile(ctx, a, v.ID); err != nil || !p.Withdrawn || p.Email != "" || p.Name != "" {
		t.Errorf("退会後: %+v %v", p, err)
	}
}
