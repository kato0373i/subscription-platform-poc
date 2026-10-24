# ドメイン間の契約ベースライン（PdM 管理）

各 TL は、このベースラインを**変更せずに**詳細化する。変更が必要なら、詳細要件の「PdM への確認事項」に理由を添えて書く（勝手に変えない）。
以下で使う Go のシグネチャは、形を合わせるための目安とする。引数の追加や DTO の項目の追加は TL が判断してよい。ただし、名前・責務・依存の向きは変えない。

## 1. sharedkernel（TL-Platform が詳細化する）

| 型 | 定義 |
|---|---|
| `ID[T any]` | UUIDv7 をラップする型。`ID[Subscription]` と `ID[Invoice]` は、取り違えるとコンパイルエラーになる |
| `Money` | JPY の整数（int64）。値は 0 以上だけ。加算・減算・`MulRatFloor(num, den)`（D-09・D-10 の端数処理）を持つ |
| `TaxRate` | 有理数（例：10/100）で持つ |
| `Date` | JST の暦日（年・月・日）。`AddDays`、`AddMonthsClamped(anchorDay)`（月末に寄せる補正）を持つ |
| `BillingPeriod` | `{Start, End Date}`。開始日と終了日を含む（D-11）。`Next(interval, anchorDay)` を持つ |
| `BillingInterval` | `month` / `year` |
| `Clock` | `Now() time.Time`、`Today() Date`（JST） |
| `EventEnvelope` | `{event_id, event_type, occurred_at, tenant_id, aggregate_type, aggregate_id, aggregate_version, correlation_id, causation_id, payload}` |

## 2. ドメインイベント一覧（イベント名の文字列 = EventBridge の detail-type）

`source` は `sp.<module>` とする。payload の項目は「最低限これだけは入れる」もの。

| event_type | 発行元 | payload の最低限の項目 |
|---|---|---|
| `tenant.approved` | tenant | tenant_id |
| `tenant.suspended` | tenant | tenant_id, reason |
| `customer.withdrawn` | customer | customer_account_id |
| `catalog.plan_published` / `catalog.plan_unpublished` | catalog | plan_id, product_id |
| `catalog.price_version_added` | catalog | plan_id, price_version_id, amount |
| `catalog.coupon_redeemed` / `catalog.coupon_redemption_released` | catalog | coupon_id, redemption_id, customer_account_id, subscription_id |
| `subscription.started` | subscription | 契約のスナップショット（下を参照） |
| `subscription.signup_failed` | subscription | subscription_id, reason |
| `subscription.trial_will_end` | subscription | スナップショット, trial_end |
| `subscription.renewed` | subscription | スナップショット, invoice_id |
| `subscription.past_due` | subscription | スナップショット, invoice_id |
| `subscription.recovered` | subscription | スナップショット, invoice_id |
| `subscription.plan_change_scheduled` / `subscription.plan_change_revoked` / `subscription.plan_changed` | subscription | スナップショット, from_plan_id, to_plan_id, effective_date |
| `subscription.cancellation_scheduled` / `subscription.cancellation_revoked` | subscription | スナップショット, effective_date |
| `subscription.canceled` | subscription | スナップショット, reason（customer / dunning / tenant_suspended） |
| `subscription.payment_method_changed` | subscription | subscription_id, payment_method_id, status |
| `billing.invoice_issued` | billing | invoice_id, subscription_id, period, total, tax |
| `billing.invoice_collection_due` | billing | invoice_id, subscription_id, tenant_id, amount, attempt_no |
| `billing.invoice_paid` | billing | invoice_id, subscription_id, amount, paid_at |
| `billing.invoice_payment_failed` | billing | invoice_id, subscription_id, attempt_no, next_collection_at, failure_code |
| `billing.invoice_marked_uncollectible` | billing | invoice_id, subscription_id |
| `billing.invoice_voided` | billing | invoice_id, subscription_id, reason |
| `payments.payment_succeeded` / `payments.payment_failed` / `payments.payment_pending` / `payments.payment_outcome_unknown` | payments | attempt_id, payable{type, id}, amount, failure_code?, retryable? |

**契約のスナップショット**には、次の項目を入れる。
- subscription_id, tenant_id, customer_account_id, plan_id, price_version_id, status
- current_period{start, end}, next_renewal_date, trial_end
- cancel_at_period_end, pending_plan_id
- entitled（利用権があるか）
- version

**Webhook で外部に出すときの扱い**（Integration が変換する）
- 契約者の ID は、customer_account_id を出さずに tenant_customer_id（`cus_…`）に置き換える。
- 外部に出すイベントの種類は、`subscription.*`（signup_failed と payment_method_changed を除く）と、`invoice.paid`・`invoice.payment_failed` とする。
- 外部向けのイベント名では、内部の `billing.` を外して `invoice.` にする。

## 3. 同期の契約（contracts パッケージの interface）

依存の向きは、設計書 §2 のコンテキストマップに従う。

**catalog/contracts**（呼び出し元：orchestration、subscription（ID の検証のみ））
- `GetPurchasablePlan(ctx, tenantID, planID) (PlanSnapshot, error)`
  - PlanSnapshot = {plan_id, product_id, price_version_id, amount, tax_rate, interval, trial_days, pricing_scheme}
- `GetPlanSnapshot(ctx, tenantID, planID)`：プラン変更先の検証に使う。同じ商品かどうか、公開中かどうかも返す。
- `QuotePrice(ctx, priceVersionID, usage *UsageInput) (Quote, error)`
  - Quote = {amount, tax_rate}
  - PoC の usage は常に nil（従量課金の拡張口）
- `ReserveCoupon(ctx, tenantID, code, planID, customerAccountID, subscriptionID) (CouponReservation, error)`
  - CouponReservation = {redemption_id, discount{type, value, duration_cycles}}
- `ConfirmCouponRedemption(ctx, redemptionID)`、`ReleaseCouponRedemption(ctx, redemptionID)`

**customer/contracts**（呼び出し元：orchestration、integration）
- `EnsureTenantCustomerID(ctx, tenantID, customerAccountID) (TenantCustomerID, error)`
- `ResolveTenantCustomerID(ctx, tenantID, customerAccountID) (TenantCustomerID, error)`
- `GetContact(ctx, customerAccountID) (Contact{email, name}, error)`
- `Withdraw(ctx, customerAccountID) error`

**tenant/contracts**（呼び出し元：platform/httpx の認証、orchestration、billing）
- `GetTenant(ctx, tenantID) (TenantInfo{status, fincode_shop_id}, error)`
- `AuthenticateAPIKey(ctx, rawKey) (tenantID, error)`
- `GetStaffRole(ctx, tenantID, subject) (Role, error)`

**subscription/contracts**（呼び出し元：orchestration）
- `CreateForSignup(ctx, CreateSignupCmd) (SubscriptionView, error)`
  - トライアルがある場合は trialing、ない場合は pending_initial で作る。TrialUsage の判定もここで行う。
- `Activate(ctx, subID, invoiceID)`
- `MarkSignupFailed(ctx, subID, reason)`
- `BeginRenewal(ctx, subID, date) (RenewalOutcome, error)`
  - RenewalOutcome は次のどちらか。
    - Ended（解約の予約により解約済みになった）
    - Terms{period, plan_id, price_version_id, discount *AppliedDiscount, payment_method_id}
  - トライアルが終わったときの初回課金も、この関数で扱う。
- `ConfirmRenewal(ctx, subID, invoiceID)`
- `MarkPastDue(ctx, subID, invoiceID)`
- `Recover(ctx, subID, invoiceID)`
- `Cancel(ctx, subID, reason)`：すぐに解約する
- `ScheduleSystemCancellation(ctx, subID, reason)`：期間が終わったら解約する。契約者は取り消せない（D-01）
- `ChangePaymentMethod(ctx, subID, customerAccountID, paymentMethodID)`
- `ListDueForRenewal(ctx, date, cursor, limit)`
- `ListTrialEndingOn(ctx, date, cursor, limit)`
- `ListOpenByTenant(ctx, tenantID, cursor, limit)`
- `HasOpenSubscriptions(ctx, customerAccountID) (bool, error)`
- `IsPaymentMethodInUse(ctx, paymentMethodID) (bool, error)`
- `Get(ctx, subID) (SubscriptionView, error)`

**billing/contracts**（呼び出し元：orchestration）
- `IssueInvoice(ctx, IssueInvoiceCmd) (InvoiceView, error)`
  - IssueInvoiceCmd = {tenant_id, subscription_id, customer_account_id, period, plan_line{price_version_id, amount, tax_rate, description}, discount *AppliedDiscount, kind: initial | renewal}
  - `(subscription_id, period.start)` で冪等にする。
  - 作成と同時に `billing.invoice_collection_due` を出す。合計が 0 円の場合は、すぐに paid にして `billing.invoice_paid` を出す。
  - 申込のように同期で進める場合、`kind = initial` では `invoice_collection_due` を出さない（Orchestration が直接決済する）。
- `RecordPaymentOutcome(ctx, invoiceID, attemptID, outcome)`：attempt_id で冪等にする（C-02）
- `RequestImmediateCollection(ctx, invoiceID)`
- `VoidOpenInvoices(ctx, subscriptionID, reason)`
- `GetOpenInvoice(ctx, subscriptionID) (*InvoiceView, error)`

**payments/contracts**（呼び出し元：orchestration。billing は payments のイベントだけを購読する）
- `Collect(ctx, CollectCmd) (AttemptResult, error)`
  - CollectCmd = {tenant_id, payable{type:"invoice", id}, amount, payment_method_id, idempotency_key}
  - AttemptResult = {attempt_id, status: succeeded | failed | pending_async | unknown, failure_code, retryable}
  - 結果はイベントとしても必ず出す。
- `RegisterPaymentMethod(ctx, customerAccountID, provider, token) (PaymentMethodView, error)`
- `GetPaymentMethod`、`ListPaymentMethods`、`DeletePaymentMethod`
- `EnsureProviderRef(ctx, paymentMethodID, tenantID)`：事業者のショップ向けの参照を用意する
- `ResolveUnknownAttempts(ctx, olderThan)`：照合ジョブ

## 4. API 共通（TL-Platform が実装する仕組み、各 TL がエンドポイントを詳細化する）

- エラーは RFC 9457（Problem Details）で返す。エラーコードは、下の表と各ドメインで追加したものを、Platform がまとめて一覧にする。
  - 共通のコード：`validation_failed`、`unauthenticated`、`forbidden`、`not_found`、`conflict`、`idempotency_key_reused`、`internal_error`
- 一覧のページングは、カーソル方式（`limit`、`starting_after`）。レスポンスは `{data, has_more, next_cursor}` の形にする。
- 冪等キー：`POST /v1/me/subscriptions` と `POST /v1/me/payment-methods` では `Idempotency-Key` を必須にする。
- 認証の主体は 4 種類ある。主体とテナントは context に入れて引き継ぐ。
  - consumer（customer_account_id）
  - staff（tenant_id, role）
  - tenant_api（tenant_id）
  - operator

---

## 5. 改訂 v1.1（2026-10-12、PdM 決定に基づく）

詳細は 02-pdm-decisions.md を参照する。上の §1〜§4 と食い違う場合は、この節を優先する。

### 5.1 sharedkernel と EventEnvelope
- `TenantID`、`CustomerAccountID`、`EventID`、`Discount`（`Apply(amount) Money`。D-10 の式を 1 か所にまとめる）を sharedkernel に追加する（P-PLT-2・P-CAT-5）。
- EventEnvelope に、任意の項目 `actor{type: consumer|staff|tenant_api|operator|system, id}` を追加する（P-CAT-3）。`customer.withdrawn` など、テナントに属さないイベントでは `tenant_id = null` を許す。

### 5.2 依存の追加（設計書 §2 にも反映する）
- catalog → tenant/contracts（`GetTenant`）（P-CAT-1）
- integration → tenant/contracts（`TenantInfo.name`、`ListOwnerContacts`）（P-INT-1）
- Payments は Tenant に依存しない。事業者ショップの ID は、Orchestration が `CollectCmd.TenantShopID` と `EnsureProviderRefCmd.TenantShopID` に入れて渡す（P-PAY-1）

### 5.3 契約の追加・変更
- customer/contracts：`ResolveOrCreateAccount`、`GetTenantCustomerProfile` を追加する（P-IDN-3）
- tenant/contracts：`Permission`、`Allows`（認可の表）、`TenantInfo.name`、`ListOwnerContacts` を追加する
- subscription/contracts：`CreateSignupCmd` に `TenantCustomerID` を入れる（P-SUB-1）。「open」は pending_initial / trialing / active / past_due とする（P-SUB-3）
- billing/contracts：
  - `IssueInvoiceCmd.CollectionMode`（`scheduled` / `caller_managed`）を追加する
  - `RecordPaymentOutcome` と `RequestImmediateCollection` は `InvoiceView` を返す
  - `RequestImmediateCollection` に引数（理由・依頼者）を追加する（P-BIL-5）
- payments/contracts：`CollectCmd.TenantShopID`、`CustomerAccountID` を追加し、`RegisterPaymentMethodCmd` を構造体にまとめる

### 5.4 決済の冪等キー（P-X-1・P-X-3）
- **`idempotency_key = "invoice:{invoice_id}:{collection_seq}"`**
- `collection_seq` は Billing が払い出す「回収要求の連番」で、スケジュールどおりの試行・即時の再決済・ops からの再決済の**すべてで増やす**。リースが切れて出し直すときは、同じ番号を使う。
- caller_managed の初回の請求は、`collection_seq = 1` で作る。
- `billing.invoice_collection_due` の payload の `attempt_no` は **`collection_seq` に名前を変え**、別の項目として `dunning_stage`（リトライの段数）を持たせる。
- Payments の `attempt_no` は `collection_seq` と同じ値にする。

### 5.5 イベント
- `subscription.cancellation_scheduled` の payload に `reason`（customer / tenant_suspended）を追加する（P-INT-6）。
- 申込の決済が失敗したときも `billing.invoice_payment_failed`（`next_collection_at = null`、`collection_mode = caller_managed`）を出す。このイベントは Webhook とメールの対象にしない（P-BIL-5・P-ORC-2）。
- **監査のためだけのイベントは PoC では追加しない**（P-X-2）。監査は HTTP ミドルウェアと `internal/platform/auditctx` で記録する。

### 5.6 API 共通
- 冪等キーの表は `platform.idempotency_keys` に置く（P-PLT-1）。
- console では、ヘッダー `X-Tenant-Id` で操作するテナントを指定する（P-IDN-1）。
- 共通エラーコードに `method_not_allowed`、`request_in_progress`、`service_unavailable`、`provider_unavailable` を追加する。
- 認可・冪等性・テナント分離の方針は、OpenAPI の拡張属性 `x-sp-permission` / `x-sp-idempotency` / `x-sp-isolation` で宣言する。

### 5.7 相関 ID（2026-10-24、P-OBS-4・P-OBS-9）
- EventEnvelope の `correlation_id` と `causation_id` は**文字列**とする（API の `X-Correlation-Id`、E2E の `e2e-<testId>-<乱数>`、ジョブの `job:<job>:<run_id>` を、そのまま入れるため）。
- HTTP のヘッダー名は `X-Request-Id` と `X-Correlation-Id` とする。
