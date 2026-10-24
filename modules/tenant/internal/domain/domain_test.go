package domain_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/tenant/contracts"
	"subscription-platform/modules/tenant/internal/domain"
)

var now = time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)

func tenantIn(t *testing.T, st contracts.TenantStatus) *domain.Tenant {
	t.Helper()
	tn, err := domain.RegisterTenant(sk.NewID[sk.TenantTag](), "ACME", now)
	if err != nil {
		t.Fatal(err)
	}
	if st == contracts.TenantPendingReview {
		return tn
	}
	if _, err := tn.Approve("shop_1", now); err != nil {
		t.Fatal(err)
	}
	if st == contracts.TenantSuspended {
		if _, err := tn.Suspend("違反", now); err != nil {
			t.Fatal(err)
		}
	}
	return tn
}

// AC-IDN-005: §3.1 の遷移表（全 6 通り＋同じ shop ID の再実行）
func TestTenantTransitions(t *testing.T) {
	cases := []struct {
		name    string
		from    contracts.TenantStatus
		op      func(*domain.Tenant) (bool, error) // イベントが出たか
		to      contracts.TenantStatus
		wantErr error
	}{
		{"pending→approve", contracts.TenantPendingReview, approve("shop_1"), contracts.TenantActive, nil},
		{"pending→suspend", contracts.TenantPendingReview, suspend("x"), contracts.TenantPendingReview, domain.ErrTenantStateConflict},
		{"active→approve 同じ shop", contracts.TenantActive, approve("shop_1"), contracts.TenantActive, nil},
		{"active→approve 別の shop", contracts.TenantActive, approve("shop_2"), contracts.TenantActive, domain.ErrTenantStateConflict},
		{"active→suspend", contracts.TenantActive, suspend("違反"), contracts.TenantSuspended, nil},
		{"suspended→approve", contracts.TenantSuspended, approve("shop_1"), contracts.TenantSuspended, domain.ErrTenantStateConflict},
		{"suspended→suspend", contracts.TenantSuspended, suspend("別の理由"), contracts.TenantSuspended, nil},
	}
	wantEvent := map[string]bool{"pending→approve": true, "active→suspend": true}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tn := tenantIn(t, c.from)
			before := tn.State()
			ev, err := c.op(tn)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if tn.Status() != c.to {
				t.Errorf("status = %s, want %s", tn.Status(), c.to)
			}
			if ev != wantEvent[c.name] {
				t.Errorf("event = %v, want %v", ev, wantEvent[c.name])
			}
			if !ev && (tn.State().SuspendReason != before.SuspendReason || tn.State().FincodeShopID != before.FincodeShopID) {
				t.Error("イベントのない操作で状態が変わった（reason・shop ID は上書きしない）")
			}
		})
	}
	tn := tenantIn(t, contracts.TenantSuspended)
	if tn.CanSell() || tn.AllowsStaffWrite() {
		t.Error("suspended は販売・書き込みできない")
	}
	if p := tenantIn(t, contracts.TenantPendingReview); p.CanSell() || !p.AllowsStaffWrite() {
		t.Error("pending_review は販売できず、書き込みはできる")
	}
}

func approve(shop string) func(*domain.Tenant) (bool, error) {
	return func(t *domain.Tenant) (bool, error) { ev, err := t.Approve(shop, now); return ev != nil, err }
}

func suspend(reason string) func(*domain.Tenant) (bool, error) {
	return func(t *domain.Tenant) (bool, error) { ev, err := t.Suspend(reason, now); return ev != nil, err }
}

// E14: 境界値
func TestBoundaries(t *testing.T) {
	id := sk.NewID[sk.TenantTag]()
	for n, ok := range map[int]bool{0: false, 1: true, 100: true, 101: false} {
		_, err := domain.RegisterTenant(id, strings.Repeat("あ", n), now)
		if (err == nil) != ok {
			t.Errorf("name %d 文字: err = %v", n, err)
		}
	}
	if _, err := domain.RegisterTenant(id, "  ", now); err == nil {
		t.Error("空白だけの name を受け付けた")
	}
	for n, ok := range map[int]bool{0: false, 1: true, 500: true, 501: false} {
		tn := tenantIn(t, contracts.TenantActive)
		_, err := tn.Suspend(strings.Repeat("x", n), now)
		if (err == nil) != ok {
			t.Errorf("reason %d 文字: err = %v", n, err)
		}
	}
	tn := tenantIn(t, contracts.TenantActive)
	email := strings.Repeat("a", 243) + "@example.com" // 255 文字
	if _, err := domain.NewStaffMember(tn, sk.NewID[domain.StaffTag](), "sub", email, "名前", contracts.RoleAdmin, now); err == nil {
		t.Error("255 文字の email を受け付けた")
	}
	m, err := domain.NewStaffMember(tn, sk.NewID[domain.StaffTag](), "sub", "Foo@Example.COM", "名前", contracts.RoleAdmin, now)
	if err != nil || m.State().Email != "foo@example.com" {
		t.Errorf("email の正規化: %v %v", m, err)
	}
	if _, err := domain.NewStaffMember(tenantIn(t, contracts.TenantSuspended), sk.NewID[domain.StaffTag](), "s", "a@b.c", "n", contracts.RoleOwner, now); !errors.Is(err, domain.ErrTenantNotWritable) {
		t.Errorf("suspended への追加: %v", err)
	}
}

// 最後のオーナーの保護（OwnerGuard の判定部分）
func TestLastOwner(t *testing.T) {
	tn := tenantIn(t, contracts.TenantActive)
	owner, _ := domain.NewStaffMember(tn, sk.NewID[domain.StaffTag](), "o", "o@x.jp", "o", contracts.RoleOwner, now)
	if _, err := owner.ChangeRole(contracts.RoleAdmin, 1); !errors.Is(err, domain.ErrLastOwner) {
		t.Errorf("降格: %v", err)
	}
	if err := owner.CanRemove(1); !errors.Is(err, domain.ErrLastOwner) {
		t.Errorf("削除: %v", err)
	}
	if changed, err := owner.ChangeRole(contracts.RoleOwner, 1); changed || err != nil {
		t.Errorf("同じロール: %v %v", changed, err)
	}
	if changed, err := owner.ChangeRole(contracts.RoleViewer, 2); !changed || err != nil {
		t.Errorf("owner が 2 名なら降格できる: %v %v", changed, err)
	}
}

// AC-IDN-037: 乱数源を固定した生成器
func TestAPIKeyFormat(t *testing.T) {
	src := bytes.NewReader(bytes.Repeat([]byte{7, 250, 61, 200, 13}, 100)) // 250 は棄却される
	raw, err := domain.GenerateKey(src)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := domain.ParseKey(raw)
	if err != nil {
		t.Fatalf("%q: %v", raw, err)
	}
	if prefix != raw[:15] || !strings.HasPrefix(prefix, "sk_") {
		t.Errorf("prefix = %q", prefix)
	}
	tn := tenantIn(t, contracts.TenantActive)
	k, raw2, err := domain.IssueAPIKey(tn, sk.NewID[domain.APIKeyTag](), "本番", nil, 0, bytes.NewReader(bytes.Repeat([]byte{1, 2, 3}, 100)), now)
	if err != nil {
		t.Fatal(err)
	}
	if !k.Matches(raw2) {
		t.Error("発行した平文が一致しない")
	}
	last := raw2[len(raw2)-1]
	alt := byte('A')
	if last == 'A' {
		alt = 'B'
	}
	if k.Matches(raw2[:len(raw2)-1] + string(alt)) {
		t.Error("secret の 1 文字違いが一致した")
	}
	for _, bad := range []string{
		"sk_abcdefghijkl" + strings.Repeat("a", 44),        // 区切りがない
		"sk_abcdefghijk_" + strings.Repeat("a", 43),        // prefix が 11 文字
		"sk_abcdefghijkl_" + strings.Repeat("a", 42),       // secret が 42 文字
		"sk_abcdefghijkl_" + strings.Repeat("a", 42) + "-", // 記号
		"pk_abcdefghijkl_" + strings.Repeat("a", 43),       // 接頭辞違い
	} {
		if _, err := domain.ParseKey(bad); err == nil {
			t.Errorf("形式不正を受け付けた: %q", bad)
		}
	}
	// 上限（E14）: 10 本目は可、11 本目は拒否
	if _, _, err := domain.IssueAPIKey(tn, sk.NewID[domain.APIKeyTag](), "k", nil, 9, bytes.NewReader(bytes.Repeat([]byte{9}, 200)), now); err != nil {
		t.Errorf("10 本目: %v", err)
	}
	if _, _, err := domain.IssueAPIKey(tn, sk.NewID[domain.APIKeyTag](), "k", nil, 10, bytes.NewReader(bytes.Repeat([]byte{9}, 200)), now); !errors.Is(err, domain.ErrAPIKeyLimit) {
		t.Errorf("11 本目: %v", err)
	}
	if !k.Revoke(nil, now) || k.Revoke(nil, now) || k.Active() {
		t.Error("失効は 1 回だけ状態を変える")
	}
}
