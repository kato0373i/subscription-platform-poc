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
| アダプター | StubAdapter（シナリオ表・特別な金額・挙動の上書き・署名した Webhook・決済一覧）、FincodeCardAdapter（fincode API 1.4.0 に合わせて実装。決済の登録 → 実行・照会・顧客とカードの登録・カードの削除・Webhook の署名の検証と正規化。認証情報がなければ登録しない。本物の fincode のテスト環境での確認は未実施。P-IMP-21） |
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
| 023・024 | ✅ | `ops-resolve.spec.ts`（監査記録 `payment_attempt.resolve` も） |
| 032 | ✅ | `webhook-stub.spec.ts` |
| 019 | 一部 ✅ | 非同期の確定が Webhook の経路で反映されることは `stub-settle.spec.ts`。更新課金を通す確認は billing・orchestration の実装後 |
| 004・008・009・015・016・026・028・033 | ✅ | E2E-016・001、`orchestration/checkout-declined.spec.ts`、E2E-011、`orchestration/checkout-unknown-fail.spec.ts`・`withdraw.spec.ts`、E2E-007・009 |
| 007 | ✅ | `e2e/tests/payments/checkout-pan.spec.ts`（§11.3 の手順 1〜11 を 3 枚のカードで。申込ページは `web/checkout`） |
| 017・022 | ✅ | `unknown-resolution.clock.spec.ts`（017。016 も同じファイルで時刻を進めて確認）、`reconciliation.clock.spec.ts`（022。resolve_unknown は実時間の毎分 0 秒に動くので、境目を避けて日付を進める） |

## 3a. 後から直したこと

- 2026-10-27：ブレーカーが開いているときの支払方法の登録が 500 になっていたのを、仕様どおり 503 `provider_unavailable` に直した（登録の前のブレーカーの確認の誤りを変換していなかった。`TestSharedBreaker` に追加）。ブレーカーは決済代行ごと・全プロセスで共有で、結果不明・接続エラーが連続 5 回で 30 秒開く（E2E で結果不明を作るときは 1 テスト 1 件にする）。

## 4. 他の TL への引き継ぎ

- **TL-Orchestration**：申込は「`RegisterPaymentMethod`（API）→ 申込で `Collect(CollectCmd{TenantShopID, CustomerAccountID, Payable{invoice, id}, Amount, PaymentMethodID, IdempotencyKey: IdempotencyKeyFor(invoice, collection_seq)})`」。拒否・結果不明は error ではなく `AttemptResult.Status`。`errors.Is(err, contracts.ErrAttemptInProgressKind)`・`ErrPayableAlreadyPaidKind` は型付きのエラーから試行 ID を取れる。`ErrProviderUnavailable` は申込なら 503（`payments.Problem`）。申込の拒否は `ConsumerCategory` で契約者向けの分類を返す。`DELETE /v1/me/payment-methods/{id}` は `IsPaymentMethodInUse`（subscription）→ `DeletePaymentMethod`。
- **TL-Billing**：決済の結果は `payments.payment_succeeded`・`payment_failed`（`failure_code`・`retryable`）・`payment_pending`・`payment_outcome_unknown` で届く。同期の結果と重複して届くので attempt_id で吸収する。`collection_seq` は回収要求ごとに増やす（冪等キーの一部）。
- **E2E**：`world.createPaymentMethod(consumer, scenario)`、`world.seedSubscriptions(tenant, plan, count, paymentScenario)`、dev API の settle・挙動の上書きを使う。`e2e/fixtures/payments.ts`（spec §11.2 の `stub` fixture）は申込・更新のシナリオを書く orchestration の実装時に作る。

## fincode アダプター（2026-10-25、P-IMP-21）

| 要確認点（§4.5） | fincode API 1.4.0（docs.fincode.jp の OpenAPI 仕様）での答え | 実装 |
|---|---|---|
| 決済の手順 | 登録 `POST /v1/payments`（オーダー ID・`job_code`・金額）→ 実行 `PUT /v1/payments/{id}`（`access_id`・顧客 ID・カード ID） | 1 つの `Collect` にまとめる。登録の失敗は決済を起こさないので `ErrNotSent`、実行を送った後の 5xx・タイムアウトは unknown |
| テナントの指定 | `Tenant-Shop-Id` ヘッダー | 試行の `TenantShopID` を付ける |
| 冪等キー | `idempotent_key` ヘッダー（UUIDv4、有効 30 分） | 試行 ID と処理の種類から UUIDv4 の形の値を決める。30 分を過ぎた再送は、オーダー ID の重複（E0110025014）と決済済み（E9993134002）を照会に切り替える |
| 注文 ID | 自社で指定できる。30 文字まで | `o` + 試行 ID の Crockford base32（27 文字）。骨格の `fc_` + UUID（39 文字）は上限を超えていた |
| 顧客・カードの共有 | プラットフォームの `shared_customer_flag` で決まる（1 なら全テナントで共有） | 共有する契約だけに対応（`FINCODE_SHARED_CUSTOMER=true`、既定）。共有しない契約では起動しない（トークンは 1 回しか使えず、テナントごとに登録し直すには再入力が要る） |
| レート制限 | 403（一定時間内の要求が多すぎる）、503（メンテナンス） | `ErrUnavailable`（試行を作らない） |
| Webhook の署名 | Webhook 設定の `signature` が `Fincode-Signature` ヘッダーにそのまま入る。応答は 200 と `{"receive":"0"}`（それ以外は再送） | 定数時間で比較。`payments.card.exec`・`payments.card.capture` だけを取り込み、イベント ID はオーダー ID・イベント・状態から決める（通知に ID がない）。応答の本文は `WebhookAcker` で返す |
| 照会 | `GET /v1/payments/{id}?pay_type=Card` | CAPTURED・AUTHORIZED は成功、CANCELED・EXPIRED・FAILED は失敗、未処理でエラーコードなしは not_found |
| エラーコード | カード会社の拒否は `E9993*` | 残高不足・限度額 → insufficient_funds、有効期限 → card_expired、番号の誤り・利用できないカード → invalid_card、時間をあけて再度 → processing_error、ほか → card_declined |

- 設定：`FINCODE_API_BASE_URL`（テスト環境 `https://api.test.fincode.jp`）、`FINCODE_SECRET_KEY`、`FINCODE_PUBLIC_KEY`、`FINCODE_WEBHOOK_SIGNATURE`（秘密鍵を設定したら必須）、`FINCODE_SHARED_CUSTOMER`。
- テスト：`fincode_test.go` は、127.0.0.1 の HTTP サーバーが仕様の応答例どおりに返す形で、送る要求（パス・ヘッダー・本文）と応答の解釈を確かめる。**本物の fincode（テスト環境）での確認はまだ**で、テスト用の API キーと Webhook の受信先（HTTPS・443）が必要。
- 残り：申込ページの fincode JS でのトークン化（`payment-config` が fincode を返したときの読み込みと CSP の許可）、日次照合の決済一覧（`SettlementLister`。`GET /v1/payments` のページング）、本物のテスト環境での結合確認。
