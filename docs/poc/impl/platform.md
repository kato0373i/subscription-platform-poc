# 実装状況: Platform

| 項目 | 内容 |
|---|---|
| 対象 | `docs/poc/specs/platform.md`（Fixed） |
| 更新日 | 2026-10-24 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-OBS-10、P-IMP-1〜6 |

## 1. 確かめたこと

| 確認 | 結果 |
|---|---|
| `task reset` → `task up` → `task dev`（go run）→ `task verify:local` → Playwright（parallel → clock） | 通る（11 件）。MiniStack の RDS（PostgreSQL 16）で、権限を絞った `sp_admin` から bootstrap・マイグレーションが通る |
| `task dev:container`（compose の profile app。CI と同じ） | 通る（11 件） |
| `DB_MODE=container task up`（DB のフォールバック） | 通る（11 件） |
| `task verify:fallback`（`MESSAGING_MODE=direct`） | 通る |
| `task e2e:selftest`（失敗したテストにログが添付される） | 通る |
| `go test ./...`（UT・archtest・生成器・YAML の検証） | 通る |
| `go test -tags integration ./...`（testcontainers の PostgreSQL 16 と MiniStack） | 通る |
| golangci-lint（depguard・forbidigo など）・`e2elint` | 0 件。`modules/` の `time.Now`・ドメイン層の `net/http` を検出することも確かめた |
| `task verify:ministack` | RDS・EventBridge の prefix・SES（送信と取り出し）・CloudWatch Logs の受信は OK。Scheduler のタイムゾーンと Logs Insights の `filter` は非対応（P-IMP-1・P-OBS-10） |

## 2. 受け入れ条件

✅ = 自動テストで確かめた。⏳ = 対象のドメインのモジュールが実装されてから確かめる（基盤側の仕組みは実装済み）。

| AC | 状況 | 確かめている場所 |
|---|---|---|
| 001〜013 | ✅ | `internal/sharedkernel/*_test.go`（013 は fuzz） |
| 014〜018 | ✅ | `internal/platform/db/db_integration_test.go` |
| 019〜024 | ✅ | `internal/platform/messaging/integration_test.go`（023 は VT 2 秒・処理 5 秒に縮めて同じ性質を確かめる。020 は P-IMP-4） |
| 025 | ⏳ identity | 認証の流れ（正しい主体・別の主体 403・トークンなし 401）はテスト用のルートで `httpx_test.go` が確かめている。`world` で作った主体での E2E は identity の実装後 |
| 026・028 | ✅ | `internal/platform/httpx/httpx_test.go` |
| 027 | ⏳ identity | 所属していない staff の 403・`X-Tenant-Id` なしの 400 は `httpx_test.go`。停止中テナントの `tenant_not_active` の E2E は identity の実装後 |
| 029・030・033 | ⏳ payments | Idempotency の仕組みは 031・032 で確かめ済み（キーなしの 400 も） |
| 031・032 | ✅ | `internal/platform/httpx/integration_test.go` |
| 034〜036 | ✅ | `httpx_test.go`、`internal/tools/errcodesgen` |
| 037・038・062・065 | ✅ | `internal/platform/scheduler/integration_test.go` |
| 039〜041 | ✅ | `e2e/tests/platform/dev-clock.clock.spec.ts`（ジョブは `platform-noop`） |
| 042 | ✅ | `cmd/internal/wiring/devtools_off_test.go`、`internal/platform/config/config_test.go` |
| 043 | ⏳ orchestration | 相関 ID の引き継ぎ（API → Outbox → 購読者）は `smoke`・`dev-clock` の E2E と結合テストで確かめている。申込での確認は orchestration の実装後 |
| 044・045・075・080・084 | ✅ | `internal/platform/observability`・`config` の UT |
| 046 | ✅ | `e2e/tests/platform/smoke.spec.ts`、`task verify:local`（何もない状態からの `task up` で確かめた） |
| 047 | ✅ | `task verify:fallback`（direct）、`DB_MODE=container task up` で E2E を通した |
| 048・049 | ✅ | `internal/archtest`、golangci-lint（depguard・forbidigo） |
| 050・071 | ✅（定義） | `.github/workflows/ci.yml`（GitHub ではまだ動かしていない） |
| 051 | ✅ | `internal/tools/e2elint` |
| 052〜054・057 | ⏳ audit・catalog | 監査ミドルウェア（succeeded・denied・注釈のない 2xx の `http.<method>`・書き込み失敗時の error ログ）は実装済みで、`httpx_test.go` がテスト用の `AuditWriter` で確かめている。本物の `AuditWriter`（audit モジュール）での確認は実装後 |
| 055・056 | ✅ | `internal/platform/auditctx/auditctx_test.go` |
| 058 | ✅ | `internal/tools/errcodesgen`（共通コード 11 種類＋`clock_backward_not_allowed`・`tenant_not_active`） |
| 059・060・079 | ✅ | `db_integration_test.go` |
| 061 | ✅ | `internal/platform/messaging/dispatch_test.go` |
| 063・064 | ✅ | `messaging/integration_test.go` |
| 066 | ✅（DB） | `db_integration_test.go`。`db` fixture からの書き込みの拒否は、ロールの設定（read only）で保証 |
| 067 | ⏳ identity | `world.createTenant` は identity の API を呼ぶ形で実装済み |
| 068・070 | ✅ | `e2e/tests/platform/clock-guard.spec.ts`・`wait.spec.ts`、`e2elint` |
| 069 | ⏳ integration | `webhook-sink` 自体は `tools/webhook-sink` の UT で確かめ済み |
| 072 | ⏳ 全ドメイン | `POST /v1/dev/seed/subscriptions`（`cmd/internal/wiring/devseed`）は各モジュールの contracts が必要 |
| 073・078 | ⏳ orchestration | `devlogs` と `domain.state_changed` の補助関数は実装済み |
| 074 | ✅ | `task e2e:selftest`（`e2e/selftest/`） |
| 076 | ✅（platform の範囲） | `e2e/tests/platform/log-redaction.spec.ts`（トークン・カード番号・Bearer・メールが平文で出ない）。カード登録・申込・API キーの流れは各ドメインの実装後 |
| 077 | ✅ | `e2e/tests/platform/logs-cwl.spec.ts`（P-OBS-10 の形） |
| 081 | ✅ | `e2e/tests/platform/correlation.spec.ts`、`httpx_test.go` |
| 082 | ✅ | `dev-clock.clock.spec.ts`（手動実行）、`scheduler/integration_test.go`（スケジュール起動の `job:<job>:<run_id>`） |
| 083 | ✅ | `cmd/devlogs/main_test.go` |

## 3. 各ドメインの TL への引き継ぎ

- モジュールを実装したら、`cmd/internal/wiring/modules.go` の `modules()` と `ports()`、`migrate.go` の `MigrationSources()` に追加する（規約 M7 の形）。
- キューは `deploy/messaging.yaml`、ジョブは `deploy/schedules.yaml` に追記する。購読者が登録されていないキューがあると、ワーカーは起動しない。
- エラーコードは `api/errors.yaml` に追記して `task generate`。OpenAPI は `api/openapi/<audience>/<module>.yaml`（`x-sp-permission`・`x-sp-isolation`・`x-sp-idempotency`）。
- 状態を持つ集約のリポジトリは、保存時に `observability.StateChanged` を呼ぶ。外部への HTTP は `observability.NewHTTPClient` を使う。
- E2E は `e2e/tests/<module>/`、時刻を進めるものは `*.clock.spec.ts`。`world` の各関数は identity・payments の API の実装に合わせて調整してよい。
