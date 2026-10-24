package wiring

import (
	"context"
	"io/fs"

	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/scheduler"
)

// ModuleSet は 1 モジュール分の組み立ての結果（規約 M7 の形）。各ドメインの TL がモジュールを実装したら、modules() に追加する。
type ModuleSet struct {
	Name       string // スキーマ名
	Migrations fs.FS  // db/migrations
	HasOutbox  bool   // create_messaging_tables を呼んでいる（Relay の対象）
	MountHTTP  func(*httpx.Router) error
	Consumers  []messaging.ConsumerSpec
	Jobs       []scheduler.Job
}

// modules はプロセスに組み込むモジュール。マイグレーションの順序（tenant → … → audit）で並べる。
// Platform の実装時点ではドメインのモジュールはまだない。
func modules(_ context.Context, _ *Base, _ string) ([]ModuleSet, error) {
	return nil, nil
}

// ports は httpx が使う modules の関数（identity・audit が実装されたら注入する）。
func ports(_ []ModuleSet) httpx.Ports {
	return httpx.Ports{}
}
