// Command apigen は api/openapi/<audience>/<module>.yaml から、oapi-codegen の strict server（std net/http）を生成する（platform.md §10.1）。
// 出力は modules/<module>/internal/adapter/http/gen/<audience>/<audience>.gen.go。
// oapi-codegen は外部ファイルの $ref を扱えないので、kin-openapi で参照を取り込んでから（InternalizeRefs）ライブラリとして呼ぶ。
// dev/*.yaml は platform の手書きのハンドラなので対象外。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/oapi-codegen/oapi-codegen/v2/pkg/codegen"

	"subscription-platform/api/openapi"
)

func main() {
	root := flag.String("root", ".", "リポジトリのルート")
	flag.Parse()
	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, "apigen:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	var errs []error
	err := fs.WalkDir(openapi.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yaml") || !strings.Contains(p, "/") {
			return err
		}
		audience, file := path.Split(p)
		audience = strings.TrimSuffix(audience, "/")
		if audience == "dev" {
			return nil
		}
		module := strings.TrimSuffix(file, ".yaml")
		if err := generate(root, p, audience, module); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
		}
		return nil
	})
	if err != nil {
		return err
	}
	return errors.Join(errs...)
}

func generate(root, specPath, audience, module string) error {
	modDir := filepath.Join(root, "modules", module)
	if _, err := os.Stat(modDir); err != nil {
		return fmt.Errorf("module directory %s does not exist", modDir)
	}
	spec, err := openapi.Load(specPath)
	if err != nil {
		return err
	}
	spec.InternalizeRefs(context.Background(), nil)
	code, err := codegen.Generate(spec, codegen.Configuration{
		PackageName: audience,
		Generate:    codegen.GenerateOptions{StdHTTPServer: true, Strict: true, Models: true},
	})
	if err != nil {
		return fmt.Errorf("codegen: %w", err)
	}
	out := filepath.Join(modDir, "internal", "adapter", "http", "gen", audience, audience+".gen.go")
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	return os.WriteFile(out, []byte(code), 0o644) //nolint:gosec // 生成物（コミットする）
}
