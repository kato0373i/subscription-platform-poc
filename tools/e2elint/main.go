// Command e2elint は E2E のテストの規約を検査する（task lint。platform.md §10.2）。
//   - *.clock.spec.ts 以外で clock.advance・setTo・runJob を呼ばない（P-TST-9）。わざと呼ぶテストは先頭に「// e2elint:allow-clock」
//   - 固定の sleep（waitForTimeout）を使わない
//   - x-sp-isolation: tested の全 operationId に @isolation:<operationId> のタグのテストがある（AC-PLT-051）
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
)

var (
	clockCall   = regexp.MustCompile(`\bclock\.(advance|setTo|runJob)\(`)
	sleepCall   = regexp.MustCompile(`\bwaitForTimeout\(`)
	isolationRe = regexp.MustCompile(`@isolation:([A-Za-z0-9_]+)`)
)

// Check は testsDir のテストと、tested と宣言された operationId を突き合わせる。
func Check(testsDir string, tested []string) []error {
	var errs []error
	tags := map[string]bool{}
	_ = filepath.WalkDir(testsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".ts") {
			return err
		}
		b, err := os.ReadFile(path) //nolint:gosec // リポジトリのテストを読む
		if err != nil {
			return err
		}
		src := string(b)
		rel, _ := filepath.Rel(testsDir, path)
		if !strings.HasSuffix(path, ".clock.spec.ts") && clockCall.MatchString(src) && !strings.Contains(src, "e2elint:allow-clock") {
			errs = append(errs, fmt.Errorf("%s: clock.advance・setTo・runJob は *.clock.spec.ts でだけ使う（P-TST-9）", rel))
		}
		if sleepCall.MatchString(src) {
			errs = append(errs, fmt.Errorf("%s: waitForTimeout は使わない（wait.until を使う）", rel))
		}
		for _, m := range isolationRe.FindAllStringSubmatch(src, -1) {
			tags[m[1]] = true
		}
		return nil
	})
	for _, op := range tested {
		if !tags[op] {
			errs = append(errs, fmt.Errorf("operationId %s は x-sp-isolation: tested だが、@isolation:%s のテストがない", op, op))
		}
	}
	return errs
}

// testedOperations は全 OpenAPI 定義から x-sp-isolation: tested の operationId を集める。
func testedOperations() ([]string, error) {
	var out []string
	err := fs.WalkDir(openapi.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == "common.yaml" || !strings.HasSuffix(p, ".yaml") {
			return err
		}
		s, err := openapi.Load(p)
		if err != nil {
			return err
		}
		pols, err := httpx.ParsePolicies(s)
		if err != nil {
			return err
		}
		out = append(out, httpx.TestedOperations(pols)...)
		return nil
	})
	sort.Strings(out)
	return out, err
}

func main() {
	dir := flag.String("tests", "e2e/tests", "Playwright のテストのディレクトリ")
	flag.Parse()
	ops, err := testedOperations()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2elint:", err)
		os.Exit(1)
	}
	if errs := Check(*dir, ops); len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "e2elint:", errors.Join(errs...))
		os.Exit(1)
	}
}
