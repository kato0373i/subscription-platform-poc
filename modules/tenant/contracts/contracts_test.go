package contracts_test

import (
	"io/fs"
	"strings"
	"testing"

	"subscription-platform/api/openapi"
	"subscription-platform/modules/tenant/contracts"
)

// AC-IDN-008: 全ロール × 全 Permission が §3.4 の表と一致する
func TestAllowsMatchesTable(t *testing.T) {
	// owner・admin・viewer の順
	table := map[contracts.Permission][3]bool{
		contracts.PermConsoleRead:         {true, true, true},
		contracts.PermCatalogWrite:        {true, true, false},
		contracts.PermCouponWrite:         {true, true, false},
		contracts.PermDunningWrite:        {true, true, false},
		contracts.PermSubscriptionOperate: {true, true, false},
		contracts.PermWebhookRedeliver:    {true, true, false},
		contracts.PermWebhookManage:       {true, false, false},
		contracts.PermAPIKeyManage:        {true, false, false},
		contracts.PermStaffManage:         {true, false, false},
	}
	if len(table) != len(contracts.Permissions) {
		t.Fatalf("表の行数 %d と Permissions の数 %d が違う", len(table), len(contracts.Permissions))
	}
	roles := []contracts.Role{contracts.RoleOwner, contracts.RoleAdmin, contracts.RoleViewer}
	for _, p := range contracts.Permissions {
		want, ok := table[p]
		if !ok {
			t.Errorf("%s が表にない", p)
			continue
		}
		for i, r := range roles {
			if got := contracts.Allows(r, p); got != want[i] {
				t.Errorf("Allows(%s, %s) = %v, want %v", r, p, got, want[i])
			}
		}
		if p.IsWrite() == (p == contracts.PermConsoleRead) {
			t.Errorf("%s.IsWrite() = %v", p, p.IsWrite())
		}
	}
	if contracts.Allows("operator", contracts.PermConsoleRead) || contracts.Allows(contracts.RoleOwner, "unknown.perm") {
		t.Error("未知のロール・Permission を許可した")
	}
}

// console の全ルートの x-sp-permission が定義済みの Permission である（§3.4「新しい console API はこの表のどれかを付ける」）
func TestConsoleRoutesUseKnownPermissions(t *testing.T) {
	err := fs.WalkDir(openapi.FS, "console", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return err
		}
		spec, err := openapi.Load(p)
		if err != nil {
			return err
		}
		for path, item := range spec.Paths.Map() {
			for method, op := range item.Operations() {
				v, _ := op.Extensions["x-sp-permission"].(string)
				if !contracts.Permission(v).Known() {
					t.Errorf("%s %s %s: x-sp-permission %q は §3.4 の表にない", p, method, path, v)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
