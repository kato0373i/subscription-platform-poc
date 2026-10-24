// Package archtest は import グラフを調べて、モジュール間の依存ルールを完全一致で検査する（platform.md §10.1 段階 2・3、P-PLT-3）。
// depguard は前方一致なので表せない規則（モジュールごとの許可リスト・ルートパッケージの import 元）をここで見る。
package archtest

import (
	"fmt"
	"sort"
	"strings"
)

// Module はリポジトリの Go モジュールパス。
const Module = "subscription-platform"

// Allowed はコンテキストマップの矢印（設計書 §2）。from が import してよい他モジュールの contracts。
// 矢印を足すときは、設計書の更新とセットでレビューする。
var Allowed = map[string][]string{
	"tenant":        {},
	"customer":      {},
	"catalog":       {"tenant"},
	"subscription":  {"catalog", "customer"},
	"billing":       {"tenant", "payments"},
	"payments":      {"customer"},
	"orchestration": {"subscription", "billing", "payments", "catalog", "customer", "tenant"},
	"integration":   {"customer", "tenant", "subscription", "billing"},
	"audit":         {},
	"reporting":     {},
}

// Violation は規則違反 1 件。
type Violation struct{ From, To, Rule string }

func (v Violation) String() string { return fmt.Sprintf("%s → %s: %s", v.From, v.To, v.Rule) }

// moduleOf は "subscription-platform/modules/billing/internal/app" → ("billing", "internal/app")。
func moduleOf(pkg string) (name, rest string, ok bool) {
	p, found := strings.CutPrefix(pkg, Module+"/modules/")
	if !found {
		return "", "", false
	}
	name, rest, _ = strings.Cut(p, "/")
	return name, rest, true
}

// Check は import グラフ（パッケージ → import するパッケージ）を検査する。
func Check(graph map[string][]string) []Violation {
	var out []Violation
	for from, imports := range graph {
		fromMod, fromRest, fromIsModule := moduleOf(from)
		isWiring := strings.HasPrefix(from, Module+"/cmd/")
		isDevseed := strings.HasPrefix(from, Module+"/cmd/internal/wiring/devseed")
		for _, to := range imports {
			toMod, toRest, toIsModule := moduleOf(to)
			if !toIsModule {
				continue
			}
			if strings.HasPrefix(from, Module+"/internal/") {
				out = append(out, Violation{from, to, "platform must not import modules"})
				continue
			}
			switch {
			case isDevseed:
				// 一括シードは各モジュールの contracts だけを通して作る（DB に直接書かない。P-TST-10）
				if !strings.HasPrefix(toRest, "contracts") {
					out = append(out, Violation{from, to, "devseed may import only modules/*/contracts"})
				}
			case fromIsModule && fromMod != toMod && strings.HasSuffix(from, "_test") && (toRest == "" || toRest == "db"):
				// 結合テスト（外部テストパッケージ *_test）は、依存先の本物のモジュール（ルートとマイグレーション）を組み立ててよい
				// （P-TST-2・P-IMP-12）。依存先はコンテキストマップの矢印にあるものに限る
				if !contains(Allowed[fromMod], toMod) {
					out = append(out, Violation{from, to, fmt.Sprintf("%s → %s is not on the context map", fromMod, toMod)})
				}
			case toRest == "":
				// モジュールのルート（module.go）を import できるのは cmd/** だけ
				if !isWiring && (!fromIsModule || fromMod != toMod) {
					out = append(out, Violation{from, to, "module root may be imported only from cmd/**"})
				}
			case fromIsModule && fromMod != toMod:
				if !strings.HasPrefix(toRest, "contracts") {
					out = append(out, Violation{from, to, "only contracts of another module may be imported"})
					continue
				}
				allowed, known := Allowed[fromMod]
				if !known || !contains(allowed, toMod) {
					out = append(out, Violation{from, to, fmt.Sprintf("%s → %s is not on the context map", fromMod, toMod)})
				}
				// contracts は std と sharedkernel だけに依存する（depguard と二重）
				if strings.HasPrefix(fromRest, "contracts") {
					out = append(out, Violation{from, to, "contracts must not import other modules"})
				}
			case !fromIsModule && !isWiring && !strings.HasPrefix(toRest, "contracts"):
				out = append(out, Violation{from, to, "only cmd/** may reach into modules"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
