# 実装状況: Payments

| 項目 | 内容 |
|---|---|
| 対象 | `docs/poc/specs/payments.md`（Fixed） |
| 更新日 | 2026-10-25 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-IMP-15 |
| コード | `modules/payments`、`api/openapi/{me,checkout,ops,dev}/payments.yaml` |

## 1. 作ったもの

| 層 | 内容 |
|---|---|
| contracts | `Service`（Collect・支払方法の登録/照会/一覧/削除・EnsureProviderRef・ResolveUnknownAttempts）、エラー（進行中・支払い済みは試行 ID を持つ型）、冪等キーの組み立て、4 種類のイベントの payload |
| domain | PaymentAttempt（状態遷移・手動確定）、PaymentMethod（参照の選び方・退会のマスキング）、冪等キーの検証、FailureCode と retryable・契約者向け分類 |
| ポートとレジストリ | 必須の `PaymentProvider` と任意の能力（登録・Webhook・決済一覧・無効化・事業者向け参照）、`PAYMENTS_METHODS` による有効化、二重登録の panic、ブレーカー（DB で共有）とトークンバケット（タスクごと） |
| アダプター | StubAdapter（シナリオ表・特別な金額・挙動の上書き・署名した Webhook・決済一覧）、FincodeCardAdapter の骨格（認証情報がなければ登録しない） |
| app | Collect（Tx1 → 送信 → Tx2、E-01〜E-10）、結果不明の照合、Webhook の Inbox への保存と処理の依頼、通知の処理（SKIP LOCKED・ロック待ちは再試行を延ばす）、掃除、日次照合（`platform.job_cursors` で再開）、退会の処理、ops の手動確定 |
| API | me（支払方法の登録・一覧）、checkout（payment-config）、ops（試行・手動確定・照合結果）、`POST /webhooks/{provider}`、dev（settle・挙動の上書き） |
| 非同期 | キュー `payments`（customer.withdrawn）・`payments-provider-events`、ジョブ 3 つ（worker-billing） |
| 一括シード | 段階 2 の前半：支払方法の登録と初回の決済を payments の contracts で通す（billing の実装で請求 ID は本物の請求になった。P-IMP-16） |

## 2. 確かめたこと

| 確認 | 結果 |
|---|---|
| `go test ./...`（unit：遷移表・手動確定・冪等キー・失敗コード・支払方法・Stub の決定規則・トークン・署名・レジストリ・具体的なアダプターへの依存がないこと） | 通る |
| `go test -tags integration ./modules/payments/...` | 通る（同じキーの 10 並列、別キーの同時実行、支払い済み、キーの再利用、結果不明、削除済みの支払方法、照合、通知の二重処理、食い違い、ロック待ちと掃除、共有ブレーカーと provider_down、RLS、退会、照合の再開） |
| `task reset` → `task up` → `task dev:container` → `task verify:local` → E2E（payments 10 件を含む 82 件） | 空の DB から通る。Webhook → Inbox → SQS → worker-billing の受信ループで確定することも E2E で確かめた |
| golangci-lint・e2elint・archtest | 0 件 |

## 3. 受け入れ条件

✅ = 自動テストで確かめた。⏳ = 依存するモジュールの実装後に確かめる。

| AC | 状況 | 確かめている場所 |
|---|---|---|
| 001・002・005・006 | ✅ | `e2e/tests/payments/payment-methods.spec.ts` |
| 003 | 一部 ✅ | 一覧は `payment-methods.spec.ts`、決済での他人の支払方法は結合テスト（E-08）。申込での 404 は orchestration の実装後 |
| 010・030・031・034 | ✅ | `stub_test.go`・`registry_test.go`・`domain_test.go` |
| 011〜014・018・020・021・025・027・029・035〜038 | ✅ | `modules/payments/internal/app/integration_test.go` |
| 023・024 | ✅（監査記録は ⏳ audit） | `ops-resolve.spec.ts` |
| 032 | ✅ | `webhook-stub.spec.ts` |
| 019 | 一部 ✅ | 非同期の確定が Webhook の経路で反映されることは `stub-settle.spec.ts`。更新課金を通す確認は billing・orchestration の実装後 |
| 004・008・009・015・016・026・028・033 | ✅ | E2E-016・001、`orchestration/checkout-declined.spec.ts`、E2E-011、`orchestration/checkout-unknown-fail.spec.ts`・`withdraw.spec.ts`、E2E-007・009 |
| 007 | ✅ | `e2e/tests/payments/checkout-pan.spec.ts`（§11.3 の手順 1〜11 を 3 枚のカードで。申込ページは `web/checkout`） |
| 017・022 | 一部 ✅ | 照合の振る舞いは結合テスト。E2E（clock）は未作成 |

## 4. 他の TL への引き継ぎ

- **TL-Orchestration**：申込は「`RegisterPaymentMethod`（API）→ 申込で `Collect(CollectCmd{TenantShopID, CustomerAccountID, Payable{invoice, id}, Amount, PaymentMethodID, IdempotencyKey: IdempotencyKeyFor(invoice, collection_seq)})`」。拒否・結果不明は error ではなく `AttemptResult.Status`。`errors.Is(err, contracts.ErrAttemptInProgressKind)`・`ErrPayableAlreadyPaidKind` は型付きのエラーから試行 ID を取れる。`ErrProviderUnavailable` は申込なら 503（`payments.Problem`）。申込の拒否は `ConsumerCategory` で契約者向けの分類を返す。`DELETE /v1/me/payment-methods/{id}` は `IsPaymentMethodInUse`（subscription）→ `DeletePaymentMethod`。
- **TL-Billing**：決済の結果は `payments.payment_succeeded`・`payment_failed`（`failure_code`・`retryable`）・`payment_pending`・`payment_outcome_unknown` で届く。同期の結果と重複して届くので attempt_id で吸収する。`collection_seq` は回収要求ごとに増やす（冪等キーの一部）。
- **E2E**：`world.createPaymentMethod(consumer, scenario)`、`world.seedSubscriptions(tenant, plan, count, paymentScenario)`、dev API の settle・挙動の上書きを使う。`e2e/fixtures/payments.ts`（spec §11.2 の `stub` fixture）は申込・更新のシナリオを書く orchestration の実装時に作る。
