package archtest_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"subscription-platform/internal/archtest"
)

const m = archtest.Module

// AC-PLT-048（規則そのもの）
func TestRulesDetectViolations(t *testing.T) {
	graph := map[string][]string{
		m + "/modules/billing/internal/app":       {m + "/modules/payments/internal/domain", m + "/modules/payments/contracts"},
		m + "/modules/payments/internal/app":      {m + "/modules/billing/contracts"},
		m + "/modules/catalog":                    {m + "/modules/tenant"},
		m + "/cmd/internal/wiring":                {m + "/modules/billing", m + "/modules/catalog"},
		m + "/cmd/internal/wiring/devseed":        {m + "/modules/billing/contracts", m + "/modules/billing/internal/adapter/postgres"},
		m + "/internal/platform/httpx":            {m + "/modules/tenant/contracts"},
		m + "/modules/subscription/contracts":     {m + "/modules/catalog/contracts"},
		m + "/modules/orchestration/internal/app": {m + "/modules/billing/contracts", m + "/modules/payments/contracts"},
	}
	got := archtest.Check(graph)
	var lines []string
	for _, v := range got {
		lines = append(lines, v.String())
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"billing/internal/app → " + m + "/modules/payments/internal/domain: only contracts of another module",
		"payments → billing is not on the context map",
		m + "/modules/catalog → " + m + "/modules/tenant: module root may be imported only from cmd/**",
		"devseed may import only modules/*/contracts",
		"platform must not import modules",
		"contracts must not import other modules",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("検出されなかった: %s\n---\n%s", want, joined)
		}
	}
	for _, v := range got {
		if strings.Contains(v.From, "orchestration") || v.From == m+"/cmd/internal/wiring" {
			t.Errorf("許可されている依存を違反とした: %s", v)
		}
	}
}

// リポジトリの実際の import グラフが規則を満たす（CI の ①）
func TestRepositoryFollowsRules(t *testing.T) {
	if testing.Short() {
		t.Skip("go/packages でリポジトリ全体を読むので -short では省略")
	}
	trackSources(t, "../..")
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedImports, Dir: "../..", Tests: true, BuildFlags: []string{"-tags=integration,devtools"}}, "./...")
	if err != nil {
		t.Fatal(err)
	}
	graph := map[string][]string{}
	for _, p := range pkgs {
		for imp := range p.Imports {
			graph[p.PkgPath] = append(graph[p.PkgPath], imp)
		}
	}
	if len(graph) < 10 {
		t.Fatalf("パッケージが読めていない: %d", len(graph))
	}
	for _, v := range archtest.Check(graph) {
		t.Error(v)
	}
}

// trackSources は全 .go ファイルを stat する。packages.Load は子プロセス（go list）で読むので、
// これがないと go test のキャッシュがファイルの追加・変更に気づかず、古い結果を返す。
func trackSources(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == ".local") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".go") {
			_, err = os.Stat(p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
