# subscription-platform

マルチテナント型（複数の事業者）× B2C のサブスクリプション基盤の PoC。Go のモジュラーモノリス、本番は ECS Fargate、ローカルは MiniStack。

始め方・コマンド・調査の手順は [CLAUDE.md](CLAUDE.md) を見る（`task up` → `task dev:container` → `task e2e`）。

## ディレクトリの案内

どこに何を書くかで迷ったら、まずこの表を見る。

| 場所 | 中身 | 例 |
|---|---|---|
| `modules/<業務>/` | 業務のコード。1 つの業務の単位（境界づけられたコンテキスト）が 1 ディレクトリ | `modules/billing`（請求）、`modules/payments`（決済） |
| `internal/platform/` | 業務によらない共通の仕組み（DB・HTTP・Outbox・ログ・監査・ジョブ） | `internal/platform/outbox` |
| `internal/sharedkernel/` | モジュールのあいだで共有する小さな型（ID・金額・日付・イベントの封筒） | |
| `cmd/` | 起動するプロセスの main（api と 3 つのワーカー、マイグレーション、ログの閲覧） | `cmd/api`、`cmd/worker-billing` |
| `api/` | API の契約（OpenAPI）とエラーコードの一覧。ここを変えたら `task generate` | `api/openapi/console/*.yaml` |
| `web/` | 画面。SPA が 3 つと、api が配る申込ページ | `web/console`（事業者）、`web/mypage`（契約者）、`web/ops`（運営者）、`web/checkout` |
| `e2e/` | テストの中心（Playwright）。ブラウザ・API・DB の値まで確かめる | `e2e/tests/scenarios` |
| `infra/` | Terraform（AWS の定義）。`modules` を `envs/<環境>` で組み合わせる | `infra/modules/staff_auth` |
| `deploy/` | プロセスとキュー・スケジュールの対応（本番と共通）と、ローカル専用の設定（`deploy/local`） | `deploy/schedules.yaml` |
| `tools/` | 開発用の道具（コード生成、E2E の規約チェック、テスト用の Webhook の受け口）。本番には含めない | `tools/apigen` |
| `scripts/` | `task` から呼ぶシェル（ローカル環境の準備・待ち合わせ・確認） | `scripts/local-env.sh` |
| `docs/` | 要件・設計・PoC の仕様と決定・実装の状況・調査の手順 | 下の表 |

### 1 つのモジュールの中

どのモジュールも同じ形にしている。依存は外側から内側への一方向（`adapter` → `app` → `domain`）。

```
modules/billing/
  contracts/        他のモジュールに公開する型と関数（他のモジュールはここだけを使う）
  db/migrations/    このモジュールのスキーマ（goose）
  db/queries/       SQL（sqlc の入力）
  internal/domain/  業務のルール（DB・HTTP を知らない）
  internal/app/     ユースケースの処理（トランザクション・イベントの発行）
  internal/adapter/ DB（postgres）・HTTP のハンドラなど外とのつなぎ
  module.go         モジュールの入口（cmd/internal/wiring だけが import する）
```

Go の単体・結合テスト（`*_test.go`）は、対象のコードと同じディレクトリに置く（Go の決まり。公開していない関数もテストできる）。結合テストは `//go:build integration` 付き。

### 生成物

次は手で編集しない（`task generate` で作り直し、コミットする）。

| 生成物 | 元 |
|---|---|
| `modules/*/internal/adapter/postgres/sqlcgen/` | `modules/*/db/queries`・`db/migrations` |
| `modules/*/internal/adapter/http/gen/` | `api/openapi/**` |
| `web/packages/api-client/src/gen/` | `api/openapi/**` |
| `internal/platform/httpx/errcodes/codes_gen.go`・`web/packages/api-client/src/error-codes.ts` | `api/errors.yaml` |

## 文書

| 場所 | 中身 |
|---|---|
| `docs/requirements/` | 要件定義（全体と、画面ごと） |
| `docs/design/` | 設計（ADR を含む） |
| `docs/poc/` | PoC の範囲（00）、契約のベースライン（01）、決定の記録（02）、ドメインごとの詳細な仕様（`specs/`）、実装の状況（`impl/`） |
| `docs/runbooks/` | 不具合の調査の手順 |
