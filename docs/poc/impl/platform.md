# 実装状況: Platform

| 項目 | 内容 |
|---|---|
| 対象 | `docs/poc/specs/platform.md`（Fixed） |
| 更新日 | 2026-10-27（残っていた E2E を追加し、⏳ を更新） |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-OBS-10、P-IMP-1〜6 |

## 1. 確かめたこと

| 確認 | 結果 |
|---|---|
| `task reset` → `task up` → `task dev`（go run）→ `task verify:local` → Playwright（parallel → clock） | 通る（11 件）。※2026-10-24 の identity の実装時に、`task reset` が RDS のデータボリュームを残していたことが分かり直した。直した後に空の DB から通ることを確かめた（`docs/poc/impl/identity.md`）。MiniStack の RDS（PostgreSQL 16）で、権限を絞った `sp_admin` から bootstrap・マイグレーションが通る |
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
| 025 | ✅ | consumer・staff・operator は identity の E2E（`me-account`・`ops-tenants`・`console-*`）、tenant_api は `e2e/tests/subscription/tenant-subscriptions.spec.ts` |
| 026・028 | ✅ | `internal/platform/httpx/httpx_test.go` |
| 027 | ✅ | `e2e/tests/identity/console-authz.spec.ts`（所属していない 403、停止中の書き込みの `tenant_not_active`） |
| 029・030・033 | ✅ | `e2e/tests/platform/idempotency.spec.ts`（支払方法の登録で、再送の `Idempotent-Replayed`・違う本文の 422・キーなしの 400） |
| 031・032 | ✅ | `internal/platform/httpx/integration_test.go` |
| 034〜036 | ✅ | `httpx_test.go`、`tools/errcodesgen` |
| 037・038・062・065 | ✅ | `internal/platform/scheduler/integration_test.go` |
| 039〜041 | ✅ | `e2e/tests/platform/dev-clock.clock.spec.ts`（ジョブは `platform-noop`） |
| 042 | ✅ | `cmd/internal/wiring/devtools_off_test.go`、`internal/platform/config/config_test.go` |
| 043 | ✅ | `e2e/tests/platform/correlation-chain.spec.ts`（結果不明の申込 → 運営者の確定 → billing・orchestration の購読者が出した後続のイベントまで同じ相関 ID、causation_id は受け取った event_id。同期の申込はイベントをすべて API の中で書くので、購読の連鎖はこの経路で見る） |
| 044・045・075・080・084 | ✅ | `internal/platform/observability`・`config` の UT |
| 046 | ✅ | `e2e/tests/platform/smoke.spec.ts`、`task verify:local`（何もない状態からの `task up` で確かめた） |
| 047 | ✅ | `task verify:fallback`（direct）、`DB_MODE=container task up` で E2E を通した |
| 048・049 | ✅ | `internal/archtest`、golangci-lint（depguard・forbidigo） |
| 050・071 | ✅（定義） | `.github/workflows/ci.yml`（GitHub ではまだ動かしていない） |
| 051 | ✅ | `tools/e2elint` |
| 052・053 | ✅ | `e2e/tests/audit/audit-records.spec.ts`（価格改定の before/after、viewer の 403 は denied、トークンなしの 401 は残らない） |
| 054 | 一部 ✅ | `/v1/me` の書き込みは残らないことを `audit-records.spec.ts` で確認。注釈のない console の 2xx（`http.post`）は、注釈のないルートが今はないので `httpx_test.go`（テスト用の `AuditWriter`）だけ |
| 057 | 一部 | 監査の書き込みに失敗しても応答は 2xx のまま error ログを出すことは `httpx_test.go` で確認。E2E は DB の権限を剥がせない（`sp_e2e_reader` は読み取り専用）ので作らない |
| 055・056 | ✅ | `internal/platform/auditctx/auditctx_test.go` |
| 058 | ✅ | `tools/errcodesgen`（共通コード 11 種類＋`clock_backward_not_allowed`・`tenant_not_active`） |
| 059・060・079 | ✅ | `db_integration_test.go` |
| 061 | ✅ | `internal/platform/messaging/dispatch_test.go` |
| 063・064 | ✅ | `messaging/integration_test.go` |
| 066 | ✅（DB） | `db_integration_test.go`。`db` fixture からの書き込みの拒否は、ロールの設定（read only）で保証 |
| 067 | ✅ | `parallel` project の各テストが自分の事業者を作り、契約一覧で他の事業者のデータが見えないこと（`console-subscriptions.spec.ts` など `@isolation` のテスト） |
| 068・070 | ✅ | `e2e/tests/platform/clock-guard.spec.ts`・`wait.spec.ts`、`e2elint` |
| 069 | ✅ | `tools/webhook-sink` の UT、回数つきの振る舞いは `webhook-delivery.clock.spec.ts`（AC-INT-012：500 を 4 回）、応答しない送信先は `webhook-delivery.spec.ts` |
| 072 | 一部 | `POST /v1/dev/seed/subscriptions` を段階 1 で実装（`cmd/internal/wiring/devseed`。P-IMP-14）。subscription の E2E で使っている。初回の請求が paid・100 件の確認は payments・billing の実装後（段階 2） |
| 073・078 | ✅ | `e2e/tests/platform/logs-trace.spec.ts`。確認の途中で 2 つの漏れを直した：Webhook の配信の `http.outbound.completed` に相関 ID がなかった（`integration.webhook_events.correlation_id` を追加し、配信の context に引き継ぐ）、請求の `domain.state_changed` を出していなかった（billing のリポジトリの更新で出す） |
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
