package openapi_test

import (
	"io/fs"
	"strings"
	"testing"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
)

// 全定義が読み込め、拡張属性の規則（x-sp-permission・x-sp-isolation）を満たす
func TestAllSpecsLoad(t *testing.T) {
	err := fs.WalkDir(openapi.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == "common.yaml" || !strings.HasSuffix(p, ".yaml") {
			return err
		}
		s, err := openapi.Load(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			return nil
		}
		if _, err := httpx.ParsePolicies(s); err != nil {
			t.Errorf("%s: %v", p, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
