# 実装状況: Orchestration

| 項目 | 内容 |
|---|---|
| 対象 | `docs/poc/specs/orchestration.md`（Fixed） |
| 更新日 | 2026-10-25 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-IMP-17 |
| コード | `modules/orchestration`、`api/openapi/{me,ops}/orchestration.yaml` |

## 1. 作ったもの

| 層 | 内容 |
|---|---|
| domain | 判定表だけ（純粋な関数）：回収キー `invoice:{id}:{seq}`、collection_due の seq の比較、結果のイベント × 契約状態 → 命令、申込の再実行の判定、一括解約の動作 |
| 永続化 | `billing_cycle_processes`（請求・契約ごとに進行中 1 つの部分ユニーク）、`checkout_sagas`、`billing_runs`、`bulk_cancellation_jobs`・`_failures`・`_items`、Outbox / Inbox |
| 申込 | サガ（手順 0 で subscription_id を採番、本文のハッシュで照合）→ 事業者・プラン・支払方法・契約者 ID・事業者ショップ向けの参照・クーポンの予約 → 契約 → トライアルならクーポン確定で 201 ／ 初回の請求（caller_managed）→ 決済 → RecordPaymentOutcome（sync）→ Activate・確定（201）／ 補償（422 payment_declined、503 provider_unavailable はサガ aborted）／ 結果待ち（202、保存しない） |
| 更新 | `orchestration-renewal-dispatch`（BillingRun・カーソルから再開・`orc-renewal.fifo` に 10 件ずつ）→ FIFO の受信で BeginRenewal → QuotePrice → IssueInvoice（scheduled）。回収は Billing の collection_due（`orc-collection`）で Collect |
| 結果の反映 | invoice_paid / payment_failed / marked_uncollectible を契約状態で振り分け（Activate・ConfirmRenewal・Recover・MarkPastDue・Cancel(dunning)・申込の補償・paid_after_cancel のアラート）。停止中の事業者の申込の確定は期間終了で解約 |
| その他の購読 | 支払い遅延中の支払方法の変更 → recovery プロセス → RequestImmediateCollection、`subscription.canceled` → VoidOpenInvoices、`tenant.suspended` → 一括解約のジョブ |
| ジョブ | 一括解約（毎分、2 周、items で件数を数える）、process-sweeper（10 分ごと：止まった更新は FIFO に再投入、申込の collecting は同じキーで再決済、5 回で failed、5 分を超えたサガは契約の状態から閉じる）、サガの掃除（30 日） |
| API | `POST /v1/me/subscriptions`、`PUT /v1/me/subscriptions/{id}/payment-method`、`DELETE /v1/me/payment-methods/{id}`、`DELETE /v1/me`、ops の rerun・課金日の照会・一括解約の照会 |
| Platform への追加・修正 | `db.ClearScope`、`inbox.Guard`、`messaging.RetryAfter`、`SendFIFOBatch`、Problem の `decline_category`。dev clock が `Now()` で DB の接続を待ってデッドロックする不具合を修正 |

## 2. 確かめたこと

| 確認 | 結果 |
|---|---|
| `go test ./...`（unit：判定表・回収キー） | 通る |
| `go test -tags integration ./...`（全モジュールを本物で組み立て、Outbox の中継を手で回す） | 通る（申込の通し、IssueInvoice の直後の停止からの再開、sweeper の再開と failed、サガの sweeper、更新メッセージの重複、同じ seq・古い seq、進行中の回収があるときの再配信、Inbox の重複、停止中の事業者の確定、一括解約の重複・途中停止・件数、rerun の 409、投入の途中停止とカーソルからの再開） |
| `task reset` → `task up` → `task dev:container` → `task verify:local` → E2E（121 件。シナリオ 20 本と orchestration の API テストを含む） | 空の DB から通る。リセットせずに 2 回・3 回と続けて流しても通る（dev clock のデッドロックを直した後） |
| E2E-020（`E2E_PERF_COUNT=10000 pnpm e2e:perf`） | 1 万件を計測（§5）。二重課金 0 件・期間ごとの請求 1 件 |
| golangci-lint・e2elint・archtest | 0 件 |

## 3. 受け入れ条件

✅ = 自動テストで確かめた。⏳ = 依存するモジュール・仕組みの実装後に確かめる。

| AC | 状況 | 確かめている場所 |
|---|---|---|
| 001・008・009・011・013・016・017・018・020・023・029・031・033・034・039〜041 | ✅ | E2E-001・011・013・016・002・006・007・021・009・019・010・008・012・018・014 |
| 002・003・004・005・006・007・010・015・031（pending_initial）・032・043 | ✅ | E2E-003・012、`e2e/tests/orchestration/*.spec.ts` |
| 012 | ✅（E2E） | E2E-013（同時送信。Platform のリースで片方 409） |
| 014・019・021・022・030・035〜038・042・044〜048 | ✅ | `modules/orchestration/internal/app/integration_test.go` |
| 024〜026・028・052〜054 | ✅ | `modules/orchestration/internal/domain/decide_test.go`、E2E-002・007・008・022 |
| 027 | ✅ | E2E-010 |
| 049〜051 | 一部 ✅ | 配線は deploy/messaging.yaml の検査と E2E、部分的な失敗は Platform の messaging のテスト。同時実行数の計測は E2E-020 の拡張で行う |
| E2E-001 の申込ページ | ✅ | E2E-001 の ① をブラウザの申込ページ（`/checkout?plan_id=…`）で行う |
| webhook-sink の確認・E2E-015 | ✅ | E2E-001（W1 への配信と `cus_`）・E2E-019（payment_method_changed が届かない）・E2E-015（integration の実装で追加。`docs/poc/impl/integration.md`） |
| E2E-014 の worker-billing の停止 | ⏳ procs fixture | 再開の正しさは結合テスト（AC-ORC-047・048） |

## 4. 他の TL への引き継ぎ

- **TL-Integration**：Orchestration はイベントを出さない（Outbox は拡張口）。契約・請求のイベントは各モジュールが出す。E2E の sink の確認は各シナリオに「integration モジュールの実装後」とコメントしてある箇所に足す。
- **TL-Audit**：注釈 `billing_run.rerun`（run_date・billing_run_id・seq・tenant_id）を書いている。
- **E2E**：`e2e/helpers/orchestration.ts` に申込（`checkout`・`subscribe`）、StubAdapter の上書き（`setBehavior`）、clock の基準日（`startAtNext15`）、DB の共通の確認（`expectInvariants`）がある。ジョブ名は `JOB.*`。

## 5. E2E-020 の計測結果（2026-10-25、コミット 6ba851e）

ローカル（MacBook の Docker、MiniStack、worker-billing 1 タスク、`orc-renewal.fifo` の同時実行 10・`orc-collection` 5、StubAdapter の遅延 0）。処理時間は参考値で、本番相当の値は dev 環境（AWS）で取る（D-07・P-ARC-9）。

| 条件 | 1 万件の処理時間 | 件数/秒 | 再送（collection_due の重複） | 合否（二重課金 0・期間ごとの請求 1） |
|---|---|---|---|---|
| `BILLING_COLLECTION_LEASE=1m`（E2E の既定） | 937 秒 | 10.7 | 2,898 件 | ✅ |
| `BILLING_COLLECTION_LEASE=6h`（本番の既定） | 338 秒 | 29.5 | 0 件 | ✅ |

- NFR-001（1 日 10 万件を 4 時間以内 ≒ 6.9 件/秒）を、1 タスクのローカル環境でも上回る。
- リース 1 分では、キューで 1 分以上待った請求が毎時の `billing-collection-due` で同じ collection_seq のまま再送され（同じ回収キーなので二重課金にはならない）、「進行中のプロセスがある」として 60 秒後に回されるので、途中で約 60 秒止まった。リースは回収キューの待ち時間より長くする必要がある（本番の 6h は十分）。
- 1 分のリースのときの段階ごとの内訳：投入ジョブ（1 万件の FIFO 送信）約 66 秒、請求の発行 約 15.6 件/秒（いちばん遅い段階。`orc-renewal.fifo` の同時実行 10）、Outbox の中継の遅れは最大 111 秒。
- 計測の手順：本番相当で測るときは `.env.container` の `BILLING_COLLECTION_LEASE` を `6h` にしてプロセスを起動し直し、`cd e2e && E2E_PERF_COUNT=10000 pnpm e2e:perf`。結果は `e2e/test-results/e2e-020-result.json`（推移を含む）に残る。終わったら `./scripts/local-env.sh` で戻す。
