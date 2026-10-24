# 実装状況: Billing

| 項目 | 内容 |
|---|---|
| 対象 | `docs/poc/specs/billing.md`（Fixed） |
| 更新日 | 2026-10-25 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-IMP-16 |
| コード | `modules/billing`、`api/openapi/{me,console,ops}/billing.yaml` |

## 1. 作ったもの

| 層 | 内容 |
|---|---|
| contracts | `Service`（IssueInvoice・RecordPaymentOutcome・RequestImmediateCollection・VoidOpenInvoices・GetOpenInvoice）、InvoiceView（CollectionSeq を含む）、エラー 7 種と `InvalidDunningPolicyError`、イベント 6 種 |
| domain | Invoice（§3.2 の金額と税、§3.3 の状態遷移表、§3.4 の失敗の反映とダニングのスナップショット、回収要求・リース・同じ collection_seq の再送、void 要求、異常 4 種）、DunningPolicy（DP-1〜DP-4） |
| 永続化 | `billing.invoices`（invoices_period・invoices_one_unsettled・invoices_due）、`invoice_lines`、`invoice_payment_records`（追記のみ）、`dunning_policies`、Outbox / Inbox。全表に tenant の RLS、請求と明細に consumer_read |
| app | 請求の作成（一意制約の違反は読み直し）、結果の反映（`(attempt_id, outcome)` で冪等。同期とイベントのどちらが先でも 1 回）、即時・ops の再決済、無効化、照会、方針の取得・更新（監査の注釈 `dunning_policy.update`）、ops の再決済（`invoice.collect`） |
| API | me（`GET /v1/me/subscriptions/{id}/invoices`。payment_unknown は processing として返す、他人の契約は空の一覧）、console（請求の一覧・詳細、`GET / PUT /v1/console/dunning-policy`）、ops（`POST /v1/ops/invoices/{id}/collect`） |
| 非同期 | キュー `billing-payments`（payments.* 4 種、購読者 `billing.payment_outcome`）、ジョブ `billing-collection-due`（毎時 0 分、worker-billing、`platform.job_cursors` で再開） |
| 設定 | `BILLING_COLLECTION_LEASE`（既定 6h） |
| 一括シード | 段階 2 の後半：IssueInvoice（caller_managed）→ Collect → RecordPaymentOutcome を通す。失敗は請求を void にして MarkSignupFailed |

## 2. 確かめたこと

| 確認 | 結果 |
|---|---|
| `go test ./...`（unit：§11 の計算例 a〜h、検証、冪等の比較、作成のモード、ダニングの予定・スナップショット・uncollectible・0 回、即時の再決済、unknown・pending、古い試行、リースと再送、void 要求、caller_managed、方針の検証） | 通る |
| `go test -tags integration ./...` | 通る（作成の 8 並列と冪等・食い違い・未精算、同期とイベントの二重の反映、Inbox、invoice 以外の ack、記録の UPDATE の拒否、retryable の欠落と amount_mismatch、回収時刻ジョブの 2 回実行とページング、ops の再決済とリース、void と GetOpenInvoice、RLS と consumer_read、方針のスナップショット） |
| `task reset` → `task up` → `task dev:container` → `task verify:local` → E2E（billing 7 件を含む 89 件） | 空の DB から通る |
| golangci-lint・e2elint・archtest | 0 件 |

## 3. 受け入れ条件

✅ = 自動テストで確かめた。⏳ = 依存するモジュールの実装後に確かめる。

| AC | 状況 | 確かめている場所 |
|---|---|---|
| 001〜005・007・010・011・013〜019・021・023〜025・027・029・033 | ✅ | `modules/billing/internal/domain/invoice_test.go` |
| 006・008・009・012・026・030 | ✅ | `modules/billing/internal/app/integration_test.go` |
| 031 | ✅ | `e2e/tests/billing/invoices.spec.ts`（E2E-016 での確認は orchestration の実装時） |
| 032・033（HTTP の 400）・034 | ✅ | `e2e/tests/billing/dunning-policy.spec.ts` |
| 038 | 一部 ✅ | サービス層（seq+1・リース中は同じ seq）は結合テスト、HTTP の 409・404 は `ops-collect.spec.ts`。scheduled の請求への 202 は更新課金（orchestration）の実装後 |
| 020・022・028・035〜037・039 | ⏳ orchestration | 更新課金・ダニング・解約のシナリオ（E2E-007〜011・021・022） |

## 4. 他の TL への引き継ぎ

- **TL-Orchestration**：申込は `IssueInvoice(Kind=initial, CollectionMode=caller_managed, PlanLine{Description: プラン名})` → `Collect(IdempotencyKey: IdempotencyKeyFor(view.ID, view.CollectionSeq))` → `RecordPaymentOutcome(source=sync)`。失敗・`provider_unavailable` の補償は `VoidOpenInvoices(signup_failed)` が先。更新・trial 終了は `CollectionMode=scheduled` で作れば作成と同時に `billing.invoice_collection_due`（collection_seq・dunning_stage・reason）が出る（購読キュー `orc-collection` は orchestration が追加する）。0 円は作成時に paid（invoice_paid のみ）。FR-017 は `RequestImmediateCollection(reason=immediate)`、解約は `VoidOpenInvoices(subscription_canceled)`。
- **TL-Integration**：`invoice_payment_failed` の `collection_mode=caller_managed` は通知の対象外。`retryable=false` は支払方法の更新の案内に使う。
- **E2E**：一括シードの請求は initial・caller_managed で paid（`payment_scenario` が `decline` などなら void、`timeout_lost` なら payment_unknown、`async_ok` なら processing）。
