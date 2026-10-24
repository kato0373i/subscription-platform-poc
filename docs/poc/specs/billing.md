# 詳細要件: Billing（modules/billing）

| 項目 | 内容 |
|---|---|
| ステータス | **Fixed**（2026-10-13 PdM 承認。テスト戦略：Playwright の E2E が中心） |
| 担当 | TL-Billing |
| 作成日 | 2026-10-12 |
| 上位文書 | `docs/poc/00-poc-charter.md`（最優先）、`docs/poc/01-contracts-baseline.md`（§5 改訂 v1.1 を含む）、`docs/poc/02-pdm-decisions.md`（P-ARC-7・P-ARC-10・P-ARC-11 を含む）、`docs/design/subscription-platform.md`、`docs/requirements/subscription-platform.md` |

## 1. 概要と PoC の範囲

Billing は「契約の 1 期間に対する請求（Invoice）」と「支払い失敗時の回収スケジュール（ダニング）」を持つ。決済手段は知らない（ADR-003）。決済の実行は Orchestration → Payments が行い、Billing はその**結果**だけを受け取る。

| 対象要件 | Billing での扱い |
|---|---|
| FR-005（0 円請求） | 0 円の請求は決済せず即 paid |
| FR-006 | DunningPolicy の取得・設定。リトライ開始時点の方針を請求に保存 |
| FR-017 | 即時の再決済要求（`RequestImmediateCollection`）。失敗しても元のスケジュールは維持 |
| FR-018 | 契約者の請求履歴 API |
| FR-019 | 更新の請求の作成（冪等） |
| FR-020 | `(subscription_id, period_start)` の一意制約、attempt_id による結果反映の冪等性 |
| FR-021 | 試行回数・next_collection_at・回収不能・リトライ不能な失敗 |
| FR-024 | payment_unknown 状態。照合の結果をイベントで反映 |
| NFR-011 / NFR-012 | processing 状態。税の内訳・明細のスナップショットを保存 |
| NFR-014 | `/v1/ops/invoices/{id}/collect` |
| D-02 / D-03 / D-09 / D-10 / C-02 / C-03 | §3〜§5 に反映 |

| 作るもの | 作らないもの（PoC） |
|---|---|
| Invoice 集約・InvoiceLine（plan / discount）・税計算・DunningSchedule | 従量課金の明細（usage）。kind と DTO の拡張口だけ残す |
| DunningPolicy 集約と console API | 領収書・適格請求書の発行、請求書番号の採番 |
| billing/contracts の 5 メソッド | 返金・請求の金額訂正（クレジットノート） |
| payments.* の購読、billing.* の発行（Outbox） | 複数税率の請求（データ構造だけ対応） |
| 1 時間ごとの回収時刻ジョブ | 請求の PDF・メール本文（Integration の担当） |
| 請求履歴 API（me / console）、ops の再決済 API | 事業者 API（`/v1/tenant`）での請求照会 |

## 2. 用語

| 用語 | 意味 |
|---|---|
| 回収要求（collection request） | Billing が `billing.invoice_collection_due` を出すこと（caller_managed では請求の作成そのもの） |
| collection_seq | 回収要求の請求内の連番（P-X-1）。スケジュールどおりの試行・即時の再決済・ops の再決済の**すべてで 1 増やす**。リース切れの再送では増やさない。決済の冪等キー `invoice:{invoice_id}:{collection_seq}` の元。Payments の `attempt_no` と同じ値 |
| 要求理由（reason） | `initial`（作成直後）/ `retry`（スケジュールどおりのリトライ）/ `immediate`（FR-017）/ `ops`（NFR-014） |
| 予定内の試行 | reason が `initial` / `retry` の試行。失敗すると**ダニングの段数**を 1 進める |
| 予定外の試行 | reason が `immediate` / `ops` の試行。失敗してもダニングの段数とスケジュールを変えない |
| 段数（dunning_stage） | 予定内の試行で失敗した回数。0 = まだ失敗していない。collection_seq とは別に数える |
| 回収モード（collection_mode） | `scheduled`（Billing が回収要求を出す）/ `caller_managed`（申込の同期処理。呼び出し元が直接決済し、Billing は回収要求もダニングもしない） |
| リース（collection_lease_until） | 回収要求を出してから結果が届くまで、同じ請求に重ねて要求を出さない期限。期限を過ぎても結果がなければ、同じ `collection_seq` で再送する（Payments が `provider_unavailable` で試行を作らなかった場合もこれで救済する。P-PAY-4） |
| 非終端 / 終端 | 非終端 = open / processing / payment_unknown。終端 = paid / uncollectible / void |

## 3. ドメインモデル

### 3.1 Invoice 集約

**フィールド**

| フィールド | 型 | 説明 |
|---|---|---|
| id | `ID[Invoice]` | UUIDv7 |
| tenant_id | `ID[Tenant]` | |
| subscription_id | `ID[Subscription]` | |
| customer_account_id | `ID[CustomerAccount]` | 契約者の請求履歴用。個人情報は持たない |
| kind | `initial` \| `renewal` | |
| collection_mode | `scheduled` \| `caller_managed` | §4 の `IssueInvoiceCmd.CollectionMode` |
| period | `BillingPeriod` | 開始日・終了日を含む（D-11） |
| status | `InvoiceStatus` | 6 状態 |
| lines | `[]InvoiceLine` | 1 件以上。順序つき |
| subtotal | `Money` | plan 明細の合計 |
| discount_total | `Money` | discount 明細の合計（≦ subtotal） |
| total | `Money` | subtotal − discount_total（税込、0 以上） |
| tax | `TaxBreakdown` | 税率ごとの {rate, taxable_amount（税込）, tax_amount}。PoC は 1 行 |
| issued_at | `time.Time` | |
| paid_at / paid_attempt_id / paid_via | `*time.Time` / `*AttemptID` / `payment` \| `zero_amount` | |
| uncollectible_at / voided_at / void_reason | | |
| attempt_count | int | 失敗として記録した試行の総数（予定外も含む。表示用） |
| dunning_stage | int | 予定内の失敗回数 |
| dunning | `*DunningSchedule` | 最初の予定内の失敗で作る。以後変えない |
| next_collection_at | `*time.Time` | scheduled のときだけ。次に回収要求を出す時刻 |
| collection_seq | int | 最後に払い出した回収要求の番号（0 = まだ出していない。caller_managed は作成時に 1） |
| pending_request | `*{seq, reason, requested_by, requested_at}` | 結果待ちの回収要求 |
| collection_lease_until | `*time.Time` | |
| last_attempt_id / last_failure_code / last_failure_retryable | | |
| void_requested_at / void_requested_reason | | §9 の「処理中の請求の無効化」 |
| version | int | 楽観ロック。イベントの aggregate_version |

**値オブジェクト**

| VO | 内容 |
|---|---|
| InvoiceLine | {line_no, kind: `plan` \| `discount`（将来 `usage`）, description（作成時点の表示名のスナップショット）, price_version_id?, discount_snapshot?{type, value, coupon_redemption_id?}, quantity, unit_amount, amount, tax_rate, period} |
| TaxBreakdown | `[]{rate TaxRate, taxable_amount Money, tax_amount Money}` |
| DunningSchedule | {offsets_days []int, policy_source: `tenant` \| `default`, first_failed_on Date, snapshotted_at} |
| PaymentOutcome | {status: `succeeded` \| `failed` \| `pending` \| `unknown`, failure_code?, retryable?, amount, source: `sync` \| `event`} |

**不変条件**

| # | 不変条件 |
|---|---|
| INV-1 | 1 つの契約の 1 つの期間（`subscription_id, period.start`）に請求は 1 つ |
| INV-2 | 1 つの契約に非終端の請求は同時に 1 つまで（D-02 を DB でも担保） |
| INV-3 | total = subtotal − discount_total ≧ 0。discount_total ≦ subtotal |
| INV-4 | 終端（paid / uncollectible / void）になったら状態・金額・明細を変えない（結果の記録だけは追記する） |
| INV-5 | total = 0 の請求は作成と同時に paid（paid_via = zero_amount）。回収要求を出さない |
| INV-6 | 決済手段（payment_method_id 等）を一切持たない |
| INV-7 | 1 つの attempt_id について、同じ結果は 1 回だけ反映する。確定した結果（succeeded / failed）のあとに届く未確定の結果（pending / unknown）は反映しない |
| INV-8 | caller_managed の請求は next_collection_at と dunning を持たず、collection_seq = 1 から増えない |
| INV-10 | collection_seq は単調増加。新しい回収要求ごとに +1、同じ要求の再送では変えない（P-X-1） |
| INV-9 | 明細はすべて同じ税率（PoC。違う場合は作成エラー） |

### 3.2 金額と税の計算（D-09・D-10・C-03）

計算はすべて `Money` の整数演算と `MulRatFloor` で行う。浮動小数点は使わない。

| 手順 | 式 |
|---|---|
| 1. plan 明細 | amount = `plan_line.amount`（Catalog の `QuotePrice` の結果。1〜1,000,000、D-08） |
| 2. 割引（定率） | discount = floor(plan.amount × percent ÷ 100)（D-10）。percent は 1〜100 の整数 |
| 2. 割引（定額） | discount = min(value, plan.amount)（0 円未満にしない） |
| 3. 合計 | total = plan.amount − discount |
| 4. 税額 | 税率ごとに、その税率の明細の合計（税込、割引後）に対して tax = floor(taxable × r ÷ (1 + r))（D-09）。r = 10/100 なら floor(taxable × 10 ÷ 110) |
| 5. 端数の単位 | 税は**請求 1 件 × 税率ごとに 1 回だけ**丸める（明細ごとに丸めない。適格請求書の端数処理の規則に合わせる） |

割引明細の税率は、割り引く対象の plan 明細と同じにする。具体例は AC-BIL-001〜007。

### 3.3 状態遷移表

凡例：`→X` は遷移先、`=` は状態を変えない、`記録` は `invoice_payment_records` に追記するだけ、`!E` はエラー。イベントは「発行」列の記号で示す（I = invoice_issued, D = invoice_collection_due, P = invoice_paid, F = invoice_payment_failed, U = invoice_marked_uncollectible, V = invoice_voided）。
同じ attempt_id・同じ結果の再送は、どの状態でも `=`（何もせず現在の状態を返す）なので列から省く。

**作成（IssueInvoice）**

| 条件 | 結果 | 発行 |
|---|---|---|
| total = 0 | →paid（paid_via = zero_amount） | I, P |
| total > 0, scheduled | →open, next_collection_at = now, collection_seq=1・reason=initial の回収要求、リース設定 | I, D（collection_seq=1, dunning_stage=0） |
| total > 0, caller_managed | →open, next_collection_at = NULL, collection_seq=1・reason=initial を払い出す（P-X-3。呼び出し元は InvoiceView.CollectionSeq で冪等キーを作る） | I |
| 同じ (subscription_id, period.start) が既にある・内容が同じ | 既存をそのまま返す | なし |
| 同じキーで内容が違う | !ErrIdempotencyConflict | なし |
| 同じ契約に別期間の非終端の請求がある | !ErrUnsettledInvoiceExists | なし |

**作成後の遷移**

| 現在 ＼ 入力 | succeeded | failed（有効な試行） | failed（古い試行） | pending | unknown | RequestImmediateCollection | 回収時刻ジョブ | VoidOpenInvoices |
|---|---|---|---|---|---|---|---|---|
| open | →paid / P | §3.4 の「失敗の反映」 | 記録 | →processing | →payment_unknown | 結果待ちなし：collection_seq+1・リース / D。リース中：= （既存の要求を返す）。caller_managed：!ErrInvoiceNotCollectible | 条件を満たせば要求 / D（§8） | →void / V |
| processing | →paid / P | §3.4（void 要求ありなら →void / V） | 記録 | = | →payment_unknown | !ErrCollectionInProgress | 対象外 | void 要求を記録して = （P-BIL-3） |
| payment_unknown | →paid / P | §3.4（void 要求ありなら →void / V） | 記録 | →processing | = | !ErrCollectionInProgress | 対象外 | void 要求を記録して = |
| paid | 別の attempt なら記録＋異常 `duplicate_success` | 記録 | 記録 | 記録 | 記録 | !ErrInvoiceNotCollectible | 対象外 | 対象外（スキップ） |
| uncollectible | 記録＋異常 `late_success` | 記録 | 記録 | 記録 | 記録 | !ErrInvoiceNotCollectible | 対象外 | 対象外 |
| void | 記録＋異常 `late_success` | 記録 | 記録 | 記録 | 記録 | !ErrInvoiceNotCollectible | 対象外 | 対象外 |

- **「有効な試行」**：この attempt_id にまだ確定結果がなく、かつ `last_attempt_id` が NULL・同じ・またはこの attempt_id より古い（UUIDv7 の時刻順）。それ以外の failed は「古い試行」として記録だけする。succeeded は試行の新旧に関係なく、非終端なら必ず paid にする（お金は動いているため）。
- 異常（anomaly）は記録して `ERROR` ログを出し、監視（NFR-013）で検知する（P-BIL-3 のケースは監査ログにも残す）。状態は変えない。返金は運用で行う（スコープ外）。
- processing / payment_unknown から succeeded で paid にするとき、void 要求があれば異常 `paid_after_void_request` も記録する。

### 3.4 失敗の反映（failed を受け取ったとき）

| 条件 | 処理 | 発行 |
|---|---|---|
| void 要求あり | →void | V |
| caller_managed | attempt_count+1、→open。ダニングしない | F（next_collection_at = null） |
| 予定外の試行（pending_request.reason ∈ immediate, ops） | attempt_count+1、→open。dunning_stage・next_collection_at は変えない | F（next_collection_at = 既存の値） |
| 予定内の試行・dunning が未作成 | DunningPolicy から DunningSchedule を作る（first_failed_on = Clock.Today()）。続けて下の行 | |
| 予定内の試行（pending_request.reason ∈ initial, retry） | attempt_count+1、dunning_stage+1 = n。n ≦ len(offsets) なら →open、next_collection_at = max(first_failed_on + offsets[n−1] の 10:00 JST, now) | F |
| 同上で n > len(offsets) | →uncollectible、next_collection_at = NULL | U |

- **retryable=false（FR-021 の 3 つ目、P-BIL-2）**も、上の表と同じに扱う（段数を消費し、次の予定を立てる）。F に `retryable` と `failure_code` を載せ、Integration が「支払方法を更新してください」の通知を出す。支払方法が削除済み・退会済みのときは、Payments が決済代行を呼ばずに failed（`payment_method_unavailable`、retryable=true。P-PAY-3）を返すので、Billing は同じ流れで記録する。契約者が支払方法を変えれば FR-017 の即時再決済で回収できる。
- 失敗を反映したら、pending_request とリースを消す。
- **`provider_unavailable`（P-PAY-4）**：Payments は試行を作らず、イベントも出さない。Billing には結果が届かないので、**段数も attempt_count も消費しない**。pending_request はそのまま残り、リースが切れたら回収時刻ジョブが**同じ collection_seq** で回収要求を出し直す（§8）。caller_managed（申込）では、呼び出し元（Orchestration）が補償処理で請求を void にしてから 503 を返す。同じ申込の再実行は新しい契約・新しい請求（collection_seq=1）として最初からやり直す（P-X-5）。

### 3.5 メソッドと発行するイベント

| メソッド（domain） | 前提 | 発行 |
|---|---|---|
| `NewInvoice(cmd, now) (*Invoice, error)` | §3.2 の検証 | I（+P または +D） |
| `RecordOutcome(attemptID, outcome, policy DunningPolicy, now) (applied bool, err)` | §3.3・§3.4 | P / F / U / V / なし |
| `RequestCollection(reason, requestedBy, now, lease)` | open・scheduled。リース中は既存の要求を返す。新しい要求なら collection_seq+1 | D |
| `EmitDueCollection(now, lease)` | open・scheduled・next_collection_at ≦ now・リース切れ | D（結果待ちの要求があれば同じ collection_seq で再送、なければ collection_seq+1） |
| `Void(reason, now)` | open なら void。processing / payment_unknown なら void 要求を記録 | V / なし |

### 3.6 DunningPolicy 集約

| フィールド | 型 | 説明 |
|---|---|---|
| tenant_id | `ID[Tenant]` | PK。1 事業者に 1 つ |
| retry_offsets_days | `[]int` | 最初の失敗日からの日数 |
| version / updated_at / updated_by | | updated_by はスタッフの subject |

| 不変条件 | |
|---|---|
| DP-1 | 要素数 0〜5（FR-006） |
| DP-2 | 各要素 1〜60（P-BIL-4） |
| DP-3 | 厳密に増えていく順（重複不可） |
| DP-4 | 行がない事業者はデフォルト `[1, 3, 7]`（policy_source = default） |
| DP-5 | 変更しても既存の請求の DunningSchedule は変わらない（請求側にスナップショットがあるため） |

| メソッド | 発行 |
|---|---|
| `NewDunningPolicy(tenantID, offsets)` / `Update(offsets, by, now)` | なし。監査は `auditctx` の注釈（変更前後の offsets）で記録する（P-X-2） |
| `DefaultDunningPolicy()` | なし |

## 4. 公開する契約（billing/contracts）

```go
package contracts // 標準ライブラリと sharedkernel だけを import する

type Service interface {
    IssueInvoice(ctx context.Context, cmd IssueInvoiceCmd) (InvoiceView, error)
    RecordPaymentOutcome(ctx context.Context, invoiceID sk.ID[Invoice], attemptID AttemptID, outcome PaymentOutcome) (InvoiceView, error)
    RequestImmediateCollection(ctx context.Context, invoiceID sk.ID[Invoice], req CollectionRequest) (InvoiceView, error)
    VoidOpenInvoices(ctx context.Context, subscriptionID sk.ID[Subscription], reason VoidReason) ([]InvoiceView, error)
    GetOpenInvoice(ctx context.Context, subscriptionID sk.ID[Subscription]) (*InvoiceView, error) // 非終端の請求。なければ nil, nil
}

type IssueInvoiceCmd struct {
    TenantID          sk.ID[Tenant]
    SubscriptionID    sk.ID[Subscription]
    CustomerAccountID sk.ID[CustomerAccount]
    Period            sk.BillingPeriod
    PlanLine          PlanLine          // {PriceVersionID, Amount sk.Money, TaxRate sk.TaxRate, Description string}
    Discount          *AppliedDiscount  // {Type: amount|percent, Value int64, CouponRedemptionID *uuid}
    Kind              InvoiceKind       // initial | renewal
    CollectionMode    CollectionMode    // scheduled | caller_managed（01 §5.3。申込の同期処理だけ caller_managed）
}

type PaymentOutcome struct {
    Status      OutcomeStatus // succeeded | failed | pending | unknown
    FailureCode string        // failed のとき
    Retryable   *bool         // failed のとき必須
    Amount      sk.Money
    Source      OutcomeSource // sync | event
}

type CollectionRequest struct {
    Reason      CollectionReason // immediate | ops
    RequestedBy string           // "system" または operator の subject
}

type VoidReason string // signup_failed | subscription_canceled | tenant_suspended

type InvoiceView struct {
    ID, TenantID, SubscriptionID, CustomerAccountID
    Kind, CollectionMode, Status
    Period; Subtotal, DiscountTotal, Total sk.Money; Tax []TaxLine; Lines []LineView
    AttemptCount, DunningStage int; NextCollectionAt *time.Time
    CollectionSeq int // caller_managed の作成直後は 1。呼び出し元は invoice:{ID}:{CollectionSeq} を冪等キーにする
    IssuedAt time.Time; PaidAt *time.Time; Version int
}
```

**メソッドごとの仕様**

| メソッド | 冪等性 | 主なエラー |
|---|---|---|
| IssueInvoice | `(subscription_id, period.start)`。同じ内容（period.end・PlanLine・Discount・Kind・CollectionMode）なら既存を返しイベントを出さない。同時実行は一意制約違反を捕まえて再読込 | ErrValidation, ErrIdempotencyConflict, ErrUnsettledInvoiceExists, ErrMixedTaxRates |
| RecordPaymentOutcome | `(attempt_id, status)` を `invoice_payment_records` の主キーで一意にする。同期（C-02）とイベントのどちらが先でも、反映は 1 回・発行も 1 回 | ErrInvoiceNotFound, ErrValidation（failed で Retryable なし） |
| RequestImmediateCollection | リース中は新しい要求を出さず現在の要求（同じ collection_seq）を返す。新しい要求なら collection_seq+1 | ErrInvoiceNotFound, ErrCollectionInProgress, ErrInvoiceNotCollectible（終端・caller_managed） |
| VoidOpenInvoices | 対象がなければ空で成功。既に void なら何もしない | — |
| GetOpenInvoice | 読み取りのみ | — |

**エラー（型付き。HTTP の code への対応は §7）**

| エラー | 意味 |
|---|---|
| ErrValidation | コマンドの値が範囲外（金額・割引・期間・税率） |
| ErrIdempotencyConflict | 同じ請求キーで内容が違う（呼び出し側のバグ。ログに残す） |
| ErrUnsettledInvoiceExists | 別期間の非終端の請求がある（D-02） |
| ErrMixedTaxRates | 明細の税率が揃っていない（PoC の制約） |
| ErrInvoiceNotFound | 見つからない（他テナントも含む） |
| ErrCollectionInProgress | processing / payment_unknown で回収を要求した |
| ErrInvoiceNotCollectible | 終端または caller_managed の請求に回収を要求した |
| ErrInvalidDunningPolicy | DP-1〜DP-3 違反（詳細コード：`too_many`, `out_of_range`, `not_increasing`） |

**01 のシグネチャ目安からの差分（承認済み。P-BIL-5、01 §5.3 に反映済み）**

| 対象 | 差分 |
|---|---|
| IssueInvoiceCmd | `CollectionMode`（`scheduled` / `caller_managed`）を追加（trial 終了時の initial は scheduled、申込の同期処理は caller_managed で区別するため） |
| InvoiceView | `CollectionSeq` を追加（P-X-3） |
| RecordPaymentOutcome | 戻り値に `InvoiceView` を追加 |
| RequestImmediateCollection | 引数 `CollectionRequest` を追加、戻り値に `InvoiceView` |
| VoidOpenInvoices | 戻り値に無効化した請求の一覧 |

**発行するイベント（billing.*、source = `sp.billing`、aggregate_type = `invoice`）**

| event_type | payload（01 の最低限 ＋ 追加） | 契機 |
|---|---|---|
| billing.invoice_issued | invoice_id, subscription_id, period, total, tax ＋ customer_account_id, kind, collection_mode, subtotal, discount_total, tax_breakdown, lines, issued_at | 作成 |
| billing.invoice_collection_due | invoice_id, subscription_id, tenant_id, amount, **collection_seq**, **dunning_stage**（01 §5.4 で attempt_no から改名）＋ reason, requested_by, customer_account_id | 回収要求（再送を含む） |
| billing.invoice_paid | invoice_id, subscription_id, amount, paid_at ＋ attempt_id?, paid_via, kind, period, tax_breakdown | paid になった |
| billing.invoice_payment_failed | invoice_id, subscription_id, attempt_no, next_collection_at, failure_code ＋ collection_seq, dunning_stage, attempt_id, retryable, reason, collection_mode, remaining_retries | 失敗を反映し open に戻った |
| billing.invoice_marked_uncollectible | invoice_id, subscription_id ＋ attempt_no, collection_seq, dunning_stage, failure_code, retryable | 最後の予定内の試行が失敗 |
| billing.invoice_voided | invoice_id, subscription_id, reason | void |

- `dunning_stage`：collection_due では要求時点の段数（0 = 初回、k = k 回目のリトライ。immediate / ops では要求時点の値をそのまま載せる）。failed / uncollectible では反映後の段数。
- failed / uncollectible の `attempt_no` は、失敗した回収要求の `collection_seq`（= Payments の attempt_no。P-X-1）。
- **決済の冪等キー（P-X-1・P-X-3）**：Orchestration は `invoice:{invoice_id}:{collection_seq}` を使う。再送は同じキーになるので二重に決済しない。即時・ops の再決済は新しいキーになり、前の失敗した試行と区別できる。
- caller_managed の `invoice_payment_failed` は Webhook とメールの対象外（P-BIL-5。Integration が除外）。
- 監査のためだけのイベントは出さない（P-X-2）。

## 5. 購読するイベントと処理

キュー：`billing-payments`（標準 SQS、C-01）。購読者名：`billing.payment_outcome`。実行は worker-billing の常駐の受信ループ（P-ARC-10）。以下の「ack」はメッセージを削除すること、「エラーを返す」は削除せず可視性タイムアウト後の再配信に任せることを指す。

| event_type | 変換 | 備考 |
|---|---|---|
| payments.payment_succeeded | outcome = succeeded | |
| payments.payment_failed | outcome = failed（failure_code, retryable） | retryable が欠けていたら false とみなす |
| payments.payment_pending | outcome = pending | |
| payments.payment_outcome_unknown | outcome = unknown | |

**処理手順（1 メッセージ = 1 トランザクション）**

1. `payable.type != "invoice"` なら ack して終了。
2. `set_config('app.tenant_id', envelope.tenant_id)`。
3. Inbox：`processed_messages(consumer, message_id = event_id)` に INSERT。重複なら ack して終了（メッセージ単位の重複排除）。
4. `SELECT … FOR UPDATE` で請求を取得。見つからなければエラーを返し、SQS の再配信に任せる（最大 5 回で DLQ）。
5. `RecordOutcome`（attempt 単位の冪等性。§3.3）。`amount` が請求の total と違えば異常 `amount_mismatch` を記録したうえで反映する（succeeded を捨てると二重決済の危険があるため）。
6. 請求の保存・`invoice_payment_records` への追記・Outbox への書き込みを同じトランザクションでコミット。楽観ロックの衝突はエラーを返し、再配信で再実行する。

**到着順の入れ替わりの扱い**

| ケース | 結果 |
|---|---|
| 同期の RecordPaymentOutcome（C-02）の後に同じ結果のイベント | 2 回目は `=`。invoice_paid も 1 回だけ |
| イベントが先、同期が後 | 同上（同期の呼び出しは現在の InvoiceView を返す） |
| 同じ試行で succeeded の後に pending / unknown | 反映しない（INV-7）。記録だけ |
| 試行 A の failed で open に戻った後、古い試行 B の succeeded | paid にする（§3.3） |
| 古い試行の failed が新しい試行の後に届く | 記録だけ（「古い試行」） |
| 最後の失敗で uncollectible になった後、古い試行の succeeded | 状態は変えず異常 `late_success`（§9） |

## 6. 永続化（スキーマ `billing`）

```sql
CREATE TABLE billing.invoices (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  customer_account_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('initial','renewal')),
  collection_mode text NOT NULL CHECK (collection_mode IN ('scheduled','caller_managed')),
  period_start date NOT NULL,
  period_end date NOT NULL CHECK (period_end >= period_start),
  status text NOT NULL CHECK (status IN ('open','processing','payment_unknown','paid','uncollectible','void')),
  currency char(3) NOT NULL DEFAULT 'JPY' CHECK (currency = 'JPY'),
  subtotal_amount integer NOT NULL CHECK (subtotal_amount BETWEEN 1 AND 1000000),
  discount_amount integer NOT NULL CHECK (discount_amount >= 0),
  total_amount integer NOT NULL CHECK (total_amount >= 0),
  tax_amount integer NOT NULL CHECK (tax_amount >= 0),
  tax_breakdown jsonb NOT NULL,            -- [{rate_num, rate_den, taxable_amount, tax_amount}]
  issued_at timestamptz NOT NULL,
  paid_at timestamptz, paid_attempt_id uuid,
  paid_via text CHECK (paid_via IN ('payment','zero_amount')),
  uncollectible_at timestamptz,
  voided_at timestamptz,
  void_reason text CHECK (void_reason IN ('signup_failed','subscription_canceled','tenant_suspended')),
  void_requested_at timestamptz, void_requested_reason text,
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  dunning_stage integer NOT NULL DEFAULT 0 CHECK (dunning_stage BETWEEN 0 AND 6),
  dunning_schedule jsonb,                  -- {offsets_days, policy_source, first_failed_on, snapshotted_at}
  next_collection_at timestamptz,
  collection_seq integer NOT NULL DEFAULT 0 CHECK (collection_seq >= 0),   -- P-X-1
  pending_request jsonb,                   -- {seq, reason, requested_by, requested_at}
  collection_lease_until timestamptz,
  last_attempt_id uuid, last_failure_code text, last_failure_retryable boolean,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT invoices_total CHECK (total_amount = subtotal_amount - discount_amount),
  CONSTRAINT invoices_paid CHECK ((status = 'paid') = (paid_at IS NOT NULL)),
  CONSTRAINT invoices_void CHECK ((status = 'void') = (voided_at IS NOT NULL)),
  CONSTRAINT invoices_caller_managed CHECK (collection_mode = 'scheduled' OR (next_collection_at IS NULL AND dunning_schedule IS NULL AND collection_seq = 1)),
  CONSTRAINT invoices_period UNIQUE (subscription_id, period_start)          -- INV-1 / FR-020
);
CREATE UNIQUE INDEX invoices_one_unsettled ON billing.invoices (subscription_id)
  WHERE status IN ('open','processing','payment_unknown');                   -- INV-2 / D-02
CREATE INDEX invoices_due ON billing.invoices (next_collection_at, id)
  WHERE status = 'open' AND collection_mode = 'scheduled';                        -- 回収時刻ジョブ
CREATE INDEX invoices_tenant_sub ON billing.invoices (tenant_id, subscription_id, period_start DESC, id);
CREATE INDEX invoices_customer_sub ON billing.invoices (customer_account_id, subscription_id, period_start DESC, id);
CREATE INDEX invoices_tenant_issued ON billing.invoices (tenant_id, issued_at DESC, id);

CREATE TABLE billing.invoice_lines (
  invoice_id uuid NOT NULL REFERENCES billing.invoices(id),
  line_no smallint NOT NULL,
  tenant_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('plan','discount')),   -- 将来 'usage' を足す
  description text NOT NULL,
  price_version_id uuid,
  discount_snapshot jsonb,                 -- {type, value, coupon_redemption_id}
  quantity integer NOT NULL DEFAULT 1 CHECK (quantity >= 1),
  unit_amount integer NOT NULL CHECK (unit_amount >= 0),
  amount integer NOT NULL CHECK (amount >= 0),               -- discount も正の値で持つ
  tax_rate_num integer NOT NULL, tax_rate_den integer NOT NULL CHECK (tax_rate_den > 0),
  period_start date NOT NULL, period_end date NOT NULL,
  PRIMARY KEY (invoice_id, line_no),
  CHECK ((kind = 'plan') = (price_version_id IS NOT NULL)),
  CHECK ((kind = 'discount') = (discount_snapshot IS NOT NULL))
);

CREATE TABLE billing.invoice_payment_records (     -- 結果の記録（追記のみ）
  attempt_id uuid NOT NULL,
  outcome text NOT NULL CHECK (outcome IN ('succeeded','failed','pending','unknown')),
  invoice_id uuid NOT NULL REFERENCES billing.invoices(id),
  tenant_id uuid NOT NULL,
  amount integer NOT NULL,
  failure_code text, retryable boolean,
  source text NOT NULL CHECK (source IN ('sync','event')),
  applied boolean NOT NULL,                -- 状態を変えたか
  anomaly text CHECK (anomaly IN ('duplicate_success','late_success','paid_after_void_request','amount_mismatch')),
  recorded_at timestamptz NOT NULL,
  PRIMARY KEY (attempt_id, outcome)        -- RecordPaymentOutcome の冪等性
);
CREATE INDEX invoice_payment_records_invoice ON billing.invoice_payment_records (invoice_id, recorded_at);
CREATE INDEX invoice_payment_records_anomaly ON billing.invoice_payment_records (recorded_at) WHERE anomaly IS NOT NULL;

CREATE TABLE billing.dunning_policies (
  tenant_id uuid PRIMARY KEY,
  retry_offsets_days integer[] NOT NULL CHECK (cardinality(retry_offsets_days) <= 5),
  version integer NOT NULL DEFAULT 1,
  updated_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
-- 各要素の範囲と昇順はドメインで検証する（配列の CHECK は書きにくいため。DB は個数だけ守る）
-- 共通：billing.outbox_events / billing.processed_messages（platform の定義に従う）
```

**RLS**（全テーブルで `ENABLE` + `FORCE ROW LEVEL SECURITY`）

| ポリシー | 対象 | 条件 |
|---|---|---|
| tenant_isolation | 全テーブル、全操作 | `tenant_id = current_setting('app.tenant_id', true)::uuid` |
| consumer_read | invoices / invoice_lines（SELECT のみ） | `invoices.customer_account_id = current_setting('app.customer_account_id', true)::uuid`（lines は親の請求で判定） |
| 運用ロール | 回収時刻ジョブの走査・ops API | RLS を回避するロール（`billing_ops`）。走査後、1 件ずつテナントを設定して更新する |

- 更新・削除の禁止：`invoice_payment_records` はアプリのロールから UPDATE / DELETE を REVOKE。終端の請求の不変（INV-4）はドメインで守る（PoC ではトリガーを作らない）。

## 7. API

共通：エラーは Problem Details。Billing で追加するコードは `invoice_not_collectible`、`collection_in_progress`。他テナント・他人のリソースは 404。

### 7.1 GET /v1/me/subscriptions/{id}/invoices（FR-018）

| 項目 | 内容 |
|---|---|
| 主体 | consumer（RLS: consumer_read） |
| クエリ | `limit`（1〜100、既定 20）, `starting_after` |
| 並び | period_start の新しい順 |
| レスポンス `data[]` | id, status, kind, period{start,end}, total, tax{rate:"10%", amount}, lines[{kind, description, amount}], issued_at, paid_at, next_collection_at（open かつ dunning 中だけ） |
| 状態の見せ方 | 契約者には `payment_unknown` を `processing` として返す（内部の不確実さを見せない） |
| エラー | `unauthenticated`, `validation_failed` |
| 備考 | 自分の契約でない・存在しない ID は**空の一覧**を返す（存在を漏らさない。Billing は契約を参照できないため 404 判定はしない） |

### 7.2 GET /v1/console/subscriptions/{id}/invoices ・ GET /v1/console/invoices/{id}（FR-009）

| 項目 | 内容 |
|---|---|
| 主体 | staff（owner / admin / viewer すべて可。テナントは `X-Tenant-Id` ヘッダー、P-IDN-1） |
| レスポンス | 7.1 の項目 ＋ customer_account_id は出さない, subscription_id, attempt_count, dunning_stage, dunning_schedule（offsets と first_failed_on）, last_failure_code, retryable, voided_at, void_reason, uncollectible_at |
| エラー | `not_found`（詳細取得で他テナント）, `forbidden` |
| 備考 | 契約詳細（Subscription 担当）に埋め込まず、別エンドポイントにする（Subscription → Billing の依存を作らないため） |

### 7.3 POST /v1/ops/invoices/{id}/collect（NFR-014）

| 項目 | 内容 |
|---|---|
| 主体 | operator（RLS 回避ロール） |
| リクエスト | 本文なし（任意で `{"note": string}`。監査は `auditctx` の注釈に invoice_id・collection_seq・note を記録、P-X-2） |
| 処理 | `RequestImmediateCollection(reason=ops, requested_by=operator)` |
| レスポンス 202 | invoice_id, status, collection_seq（新しく払い出した番号）, requested_at |
| エラー | 404 `not_found`, 409 `invoice_not_collectible`（終端・caller_managed）, 409 `collection_in_progress`（processing / payment_unknown。先に `/v1/ops/payment-attempts/{id}/resolve` で確定させる） |
| 冪等性 | リース中の再呼び出しは同じ collection_seq を 202 で返す。`Idempotency-Key` は不要 |

### 7.4 GET / PUT /v1/console/dunning-policy（FR-006）

| 項目 | GET | PUT |
|---|---|---|
| 主体 | staff（全ロール。`X-Tenant-Id`） | staff（owner / admin。viewer は 403） |
| リクエスト | — | `{"retry_offsets_days": [int]}`（0〜5 個、1〜60、昇順） |
| レスポンス 200 | `{"retry_offsets_days": [1,3,7], "is_default": true, "updated_at": null}` | 保存後の同じ形（is_default=false） |
| エラー | — | 400 `validation_failed`（errors[].code = `too_many` / `out_of_range` / `not_increasing`）, 403 `forbidden` |
| 冪等性 | — | 同じ値の PUT は何度でも同じ結果 |
| 監査 | — | `auditctx` の注釈に変更前後の retry_offsets_days を記録（P-X-2。イベントは出さない） |
| 備考 | — | デフォルトに戻すには `[1,3,7]` を PUT する（PoC では DELETE を作らない） |

## 8. ジョブ・非同期処理

| ジョブ | 起動 | 内容 |
|---|---|---|
| `billing-collection-due` | EventBridge Scheduler、毎時 0 分（JST）→ SQS `scheduler` → worker-billing が実行する（P-ARC-10）。ローカルは `POST /v1/dev/jobs/billing-collection-due/run` | `status='open' AND collection_mode='scheduled' AND next_collection_at <= now AND (collection_lease_until IS NULL OR collection_lease_until <= now)` をキーセット（next_collection_at, id）で 500 件ずつ取得。1 件ずつ `FOR UPDATE SKIP LOCKED` のトランザクションで `EmitDueCollection`。処理済みのカーソル（next_collection_at, id）を保存し、タスクの停止などで中断しても次の実行で続きから再開する（P-ARC-11 ⑥。1 件ごとにコミットし冪等なので、重複・欠落はない） |
| Outbox 中継 | platform の共通機構（P-ARC-11 ②） | billing スキーマの outbox_events を、worker-billing の常駐ループ（`FOR UPDATE SKIP LOCKED`）で中継する |
| payments.* 購読 | §5 | |

**EmitDueCollection の規則**

| 状況 | 動作 |
|---|---|
| 結果待ちの要求なし | collection_seq+1、reason = dunning_stage=0 なら initial、それ以外は retry |
| 結果待ちの要求あり（リース切れ） | 同じ collection_seq・reason で再送。Orchestration が止まった場合と、Payments が `provider_unavailable` で試行を作らなかった場合（P-PAY-4）の救済。段数は消費しない |
| 共通 | collection_lease_until = now + `BILLING_COLLECTION_LEASE`（既定 6 時間。ローカルは短くできる） |

- 10:00 JST に予定した請求は、10:00 の実行で拾われる（next_collection_at は 01:00 UTC）。
- `billing.invoice_collection_due` を購読するのは Orchestration の回収専用キュー **`orc-collection`**（P-ARC-7）。fincode への流量は、worker-billing の中のこのキューの受信の同時実行数（固定）× タスク数の上限で、全体として制御される（P-ARC-11 ②）。Billing の発行内容（payload・冪等キー）は変わらない。
- ジョブの再実行・二重起動は、行ロックとリースで重複発行を防ぐ。万一重複しても、同じ collection_seq → 同じ冪等キー `invoice:{invoice_id}:{collection_seq}` なので二重決済にならない。

## 9. 異常系・境界値・競合

| # | ケース | 扱い |
|---|---|---|
| 1 | IssueInvoice の同時実行（ワーカー重複） | 一意制約違反 → 再読込して既存を返す。イベントは 1 回 |
| 2 | 更新処理の再実行（FR-020 シナリオ 1） | IssueInvoice は既存を返す。paid の請求に回収要求は出ない |
| 3 | 割引で 0 円 / 定額割引が金額超過 | discount = plan.amount に切り詰め、total=0 → paid（INV-5） |
| 4 | DunningPolicy が 0 回 | 最初の予定内の失敗で即 uncollectible |
| 5 | processing / payment_unknown の請求に VoidOpenInvoices（D-03 の解約と決済の競合） | void 要求を記録して待つ。failed → void、succeeded → paid ＋ 異常 `paid_after_void_request`（返金は運用。監査ログとアラートに残す。P-BIL-3） |
| 6 | 終端の請求に succeeded が届く | 状態は変えず異常 `late_success`。ERROR ログ（返金は運用。スコープ外） |
| 7 | 結果のイベントが請求より先に届く | 起こらない想定（請求作成 → 決済の順）。届いたらエラーで再配信、DLQ |
| 8 | 計算上の next_collection_at が過去（unknown が後日 failed で確定） | now にする（次の毎時ジョブで回収） |
| 9 | 予定外の試行（FR-017・ops）が失敗 | 段数・スケジュールは変えない（FR-017 の「元のリトライスケジュールは維持」） |
| 10 | 予定外の要求中にリトライ予定時刻が来る | リース中なのでジョブは発行しない。予定外の失敗後、次の毎時ジョブで予定内のリトライを発行 |
| 11 | 楽観ロックの衝突（同期の反映とイベントの反映が同時） | 片方がエラー → 再読込して再実行。2 回目は attempt 単位の冪等で `=` |
| 12 | 失敗で uncollectible になる直前に方針が変わる | 請求のスナップショットを使う（DP-5） |
| 13 | 期間・金額の境界 | period.end < start、amount 0 または 1,000,001、percent 0 または 101 は ErrValidation |
| 14 | 非終端の請求がある契約に次の期間の IssueInvoice | ErrUnsettledInvoiceExists（D-02 は Orchestration が守るが、DB でも防ぐ） |
| 15 | 決済代行の障害で Payments が `provider_unavailable`（試行なし・イベントなし） | Billing は何も記録しない。段数・attempt_count は不変。リース切れで同じ collection_seq を再送（P-PAY-4） |
| 16 | ops の再決済の直後に、同じ請求へ即時の再決済 | リース中なので同じ collection_seq を返し、新しい要求は出さない |

## 10. 拡張ポイント

| 将来 | 拡張の仕方 | PoC での扱い |
|---|---|---|
| 従量課金（NFR-010） | IssueInvoiceCmd に `UsageLines []UsageLine` を追加、InvoiceLine.kind に `usage`、quantity / unit_amount を使う | kind の CHECK と quantity 列だけ用意 |
| 複数税率 | TaxBreakdown を税率ごとの配列で持っている。ErrMixedTaxRates を外す | 1 税率に制限 |
| 非同期の決済手段（NFR-011） | processing 状態・pending の反映は実装済み。Billing の変更は不要 | pending の反映は unit（AC-BIL-023）で確かめる |
| 領収書・適格請求書（NFR-012） | Receipts が `invoice_issued` / `invoice_paid` を購読。明細の表示名・税率ごとの対象額と税額・発行日時・支払日時・paid_attempt_id を payload と DB に保存済み。番号の採番・事業者の登録番号・宛名は Receipts 側で持つ | イベントの payload に含めるだけ |
| クレジットノート・返金 | 終端の請求は変えず、別の集約として追加 | 作らない |
| リトライ方針の拡張（時刻・決済手段別） | DunningSchedule に項目を追加（スナップショットなので既存の請求に影響しない） | 日数だけ |

## 11. 受け入れ条件

**金額・税の計算例（AC-BIL-001〜007 の期待値。税率 10%）**

| # | plan | 割引 | 割引額 | total | 税額 = floor(total×10/110) |
|---|---|---|---|---|---|
| a | 980 | なし | 0 | 980 | 89（89.09…） |
| b | 980 | 50% | floor(490.0)=490 | 490 | 44（44.54…） |
| c | 980 | 15% | floor(147.0)=147 | 833 | 75（75.72…） |
| d | 999 | 33% | floor(329.67)=329 | 670 | 60（60.90…） |
| e | 1,000 | 定額 300 | 300 | 700 | 63（63.63…） |
| f | 300 | 定額 500 | min(500,300)=300 | 0 | 0 → 即 paid |
| g | 1,000,000 | なし | 0 | 1,000,000 | 90,909（90909.09…） |
| h | 1 | 50% | floor(0.5)=0 | 1 | 0（0.09…） |

テストのレベル（P-TST-1〜6。モックやフェイクは使わない）

| レベル | 対象 | 書き方 |
|---|---|---|
| unit | 純粋なドメインロジックだけ（税と割引の端数、Invoice の状態遷移の表、リトライのスケジュール、collection_seq と冪等キーの組み立て、DunningPolicy の検証） | Go のテーブル駆動テスト。DB も外部も使わない。集約が記録したドメインイベントを確かめる |
| integration | 本物の PostgreSQL が必要なもの（一意制約・部分ユニーク・並行処理・SKIP LOCKED とリース、RLS、Inbox と結果の記録の冪等性） | Go のテスト（testcontainers-go、MiniStack の RDS と同じバージョン） |
| e2e | プロセスを起動した状態で API から確かめるもの | Playwright（`e2e/`）。外部は StubAdapter・`webhook-sink`・開発用 JWT・dev clock だけ。API から見えない不変条件は `sp_e2e_reader` で DB を確かめる（P-TST-3） |

| ID | Given | When | Then | 要件 | レベル | E2E シナリオ（orchestration.md） |
|---|---|---|---|---|---|---|
| AC-BIL-001 | 980 円・税率 10/100、割引なし | IssueInvoice | total=980、tax=89、明細は plan 1 行 | D-09, NFR-012 | unit | — |
| AC-BIL-002 | 980 円に 50% 割引 | IssueInvoice | discount=490、total=490、tax=44、明細は plan と discount の 2 行 | FR-005, D-10 | unit | E2E-017（初回 490 円） |
| AC-BIL-003 | 表の c・d・e・g・h | IssueInvoice | 割引額・total・tax が表のとおり（浮動小数点を使わない） | D-09, D-10 | unit | — |
| AC-BIL-004 | 300 円に定額 500 円割引（表の f）、collection_mode=scheduled | IssueInvoice | 割引額 300、status=paid、paid_via=zero_amount、invoice_issued と invoice_paid を出し、collection_due は出さない | FR-005 | unit | — |
| AC-BIL-005 | 割引率 0 または 101、金額 0 または 1,000,001 | IssueInvoice | ErrValidation | D-08, D-10 | unit | — |
| AC-BIL-006 | renewal の IssueInvoice が成功済み | 同じ cmd で再度呼ぶ | 同じ invoice_id を返し、イベントは増えない | FR-020 | integration | — |
| AC-BIL-007 | 同じ (subscription_id, period.start) で金額だけ違う cmd | IssueInvoice | ErrIdempotencyConflict | FR-020 | unit | — |
| AC-BIL-008 | 2 つの goroutine が同じ cmd で同時に IssueInvoice | 実行 | 請求は 1 件、invoice_issued は 1 件 | FR-020 | integration | — |
| AC-BIL-009 | 契約に open の請求がある | 次の期間の IssueInvoice | ErrUnsettledInvoiceExists | D-02 | integration | — |
| AC-BIL-010 | 980 円・scheduled で作成 | IssueInvoice | status=open、collection_due（collection_seq=1、dunning_stage=0、reason=initial）を出す | FR-019 | unit | E2E-001・E2E-007（キー `invoice:{id}:1`） |
| AC-BIL-011 | kind=initial・caller_managed で作成 | IssueInvoice | collection_due を出さない。next_collection_at は NULL、InvoiceView.CollectionSeq=1 | C-02, FR-013, P-X-3 | unit | E2E-001（キー `invoice:{id}:1`） |
| AC-BIL-012 | open の請求、attempt A | RecordPaymentOutcome(A, succeeded, source=sync) の後に payments.payment_succeeded(A) が届く | paid は 1 回、invoice_paid は 1 件 | C-02, FR-020 | integration | E2E-001（成功した試行 1 件） |
| AC-BIL-013 | 方針が未設定（デフォルト 1・3・7）、更新の請求 | 2026-11-15 に予定内の試行が失敗 | status=open、dunning_stage=1、next_collection_at=2026-11-16 10:00 JST、invoice_payment_failed（attempt_no=collection_seq=1, dunning_stage=1, remaining_retries=3） | FR-006, FR-021 | unit | E2E-007 |
| AC-BIL-014 | 事業者の方針が「2・5 日後」 | 2026-11-15 に失敗、続けて 11-17 10:00 の試行も失敗 | next_collection_at は 11-17 10:00 JST、次に 11-20 10:00 JST | FR-006 | unit | — |
| AC-BIL-015 | 請求が 1・3・7 でリトライ中（dunning_stage=1） | 事業者が方針を [2] に変え、2 回目も失敗 | next_collection_at = 最初の失敗日 + 3 日 10:00 JST（スナップショットのまま） | FR-006 | unit | — |
| AC-BIL-016 | 1・3・7 で、初回と 2 回のリトライが失敗済み（dunning_stage=3） | 3 回目（最後）のリトライが失敗 | status=uncollectible、invoice_marked_uncollectible を出し、invoice_payment_failed は出さない | FR-021 | unit | E2E-008 |
| AC-BIL-017 | 方針が []（0 回） | 最初の予定内の試行が失敗 | すぐに uncollectible | FR-006, FR-021 | unit | — |
| AC-BIL-018 | 1・3・7 でリトライ中（dunning_stage=1、次は最初の失敗日 +1 日） | 予定時刻に payments.payment_failed(retryable=false) | dunning_stage=2、next_collection_at は最初の失敗日 +3 日 10:00、invoice_payment_failed に retryable=false | FR-021 | unit | — |
| AC-BIL-019 | リトライ中（collection_seq=2、dunning_stage=1、次は 11-18 10:00） | RequestImmediateCollection(immediate) → 失敗 | collection_due（collection_seq=3、reason=immediate）を出す。失敗後も dunning_stage=1 と next_collection_at=11-18 10:00 は変わらない。11-18 の要求は collection_seq=4 | FR-017 | unit | — |
| AC-BIL-020 | リトライ中 | RequestImmediateCollection → 成功 | paid、next_collection_at=NULL、以後ジョブの対象にならない | FR-017, FR-021 | e2e | E2E-009 |
| AC-BIL-021 | open の請求に attempt A の unknown | 反映 | payment_unknown。ジョブの対象外。ops の collect は 409 `collection_in_progress` | FR-024, NFR-014 | unit | E2E-022（payment_unknown になる） |
| AC-BIL-022 | payment_unknown | 照合で A の succeeded が届く | paid、invoice_paid | FR-024 | e2e | E2E-011・E2E-022 |
| AC-BIL-023 | attempt A の pending を反映し processing | A の succeeded が届き、その後に遅れた A の pending が届く | paid のまま。2 つ目の pending は記録だけ | NFR-011 | unit | — |
| AC-BIL-024 | 試行 A の failed で open に戻った（dunning_stage=1） | 古い試行 Z の succeeded が届く | paid、invoice_paid | FR-020 | unit | — |
| AC-BIL-025 | uncollectible の請求 | 古い試行の succeeded が届く | 状態は uncollectible のまま、異常 late_success を記録 | FR-024 | unit | — |
| AC-BIL-026 | open の請求（next_collection_at が過去、リースなし） | 回収時刻ジョブを 2 回続けて実行 | collection_due は 1 件（2 回目はリース中でスキップ） | FR-020, FR-021 | integration | — |
| AC-BIL-027 | collection_due（collection_seq=2）を出したが結果が届かないまま 6 時間経過 | ジョブ実行 | collection_seq=2 のまま再送（冪等キー `invoice:{id}:2` が同じ） | FR-020, P-X-1 | unit | — |
| AC-BIL-037 | リトライ中（dunning_stage=1、collection_seq=2 を要求済み） | Payments が `provider_unavailable` を返して結果が届かず、リース切れ後にジョブ実行 | dunning_stage=1・attempt_count は不変、collection_seq=2 のまま再送 | P-PAY-4, FR-021 | e2e | E2E-021（b） |
| AC-BIL-038 | open・scheduled の請求（collection_seq=2、結果反映済み） | POST /v1/ops/invoices/{id}/collect | 202、collection_seq=3 の collection_due（reason=ops）。リース中にもう一度呼ぶと同じ collection_seq=3 を返す | NFR-014, P-X-1 | e2e | `e2e/tests/billing/ops-collect.spec.ts`（P-TST-8） |
| AC-BIL-028 | 支払い遅延の契約に open の請求 | VoidOpenInvoices(subscription_canceled) | void、invoice_voided。その後の RequestImmediateCollection は ErrInvoiceNotCollectible | D-03 | e2e | E2E-010 |
| AC-BIL-029 | processing の請求 | VoidOpenInvoices の後に failed が届く | void、invoice_voided（ダニングしない） | D-03 | unit | — |
| AC-BIL-030 | 事業者 A の請求 | 事業者 B のテナント文脈で取得・更新 | 見つからない（RLS）。console の GET は 404 | NFR-008 | integration | — |
| AC-BIL-031 | 契約者 X の請求 | 契約者 Y が `/v1/me/subscriptions/{X の契約}/invoices` | 空の一覧 | NFR-008, FR-018 | e2e | E2E-016（請求履歴の確認を追加。PdM から TL-Orchestration に指示済み） |
| AC-BIL-032 | 方針が未設定 | GET dunning-policy | `[1,3,7]`、is_default=true | FR-006 | e2e | `e2e/tests/billing/dunning-policy.spec.ts`（P-TST-8） |
| AC-BIL-033 | admin | PUT `[1,1]` / `[0]` / `[1,2,3,4,5,6]` / `[61]` | 400、errors[].code が not_increasing / out_of_range / too_many / out_of_range | FR-006 | unit | —（HTTP の 400 への対応は `e2e/tests/billing/dunning-policy.spec.ts` で 1 件だけ確認） |
| AC-BIL-034 | viewer | PUT dunning-policy | 403 | FR-002, FR-006 | e2e | `e2e/tests/billing/dunning-policy.spec.ts`（P-TST-8） |
| AC-BIL-035 | 更新課金の失敗 → 2 回目のリトライで成功（Orchestration と通し） | ローカルで時刻を進めてジョブを実行 | 請求は paid、契約は active に戻る | FR-021 | e2e | E2E-007 |
| AC-BIL-039 | 支払い遅延の請求が payment_unknown | 契約者が解約し、その後に照合で succeeded が確定 | 請求は paid（void にならない）、異常 paid_after_void_request を記録 | P-BIL-3 | e2e | E2E-022 |
| AC-BIL-036 | すべてのリトライが失敗（Orchestration と通し） | 最後のリトライ時刻まで時刻を進める | 請求は uncollectible、契約は canceled（reason=dunning） | FR-021 | e2e | E2E-008 |

## 12. PdM への確認事項

すべて解決済み。未解決の確認事項はない。

| # | 論点 | 決定 |
|---|---|---|
| Q1 | 「未払い → 決済処理中」に入るきっかけ | 解決済み（P-BIL-1）：pending を受けたときだけ processing にする。回収要求の重複はリースと collection_seq で防ぐ（§3.3・§8） |
| Q2 | retryable=false の失敗 | 解決済み（P-BIL-2）：段数を消費しスケジュールを続け、支払方法の更新を促す（§3.4） |
| Q3 | 結果待ちの請求がある契約の解約 | 解決済み（P-BIL-3）：void 要求を記録し、failed → void、succeeded → paid。監査ログとアラートに残す（§3.3・§9-5） |
| Q4 | リトライ間隔の日数の上限 | 解決済み（P-BIL-4）：1〜60 日（DP-2） |
| Q5 | 01 への追加 | 解決済み（P-BIL-5）：承認。ただし `billing.dunning_policy_updated` は作らず、監査は `auditctx` の注釈で記録する（P-X-2） |
| Q6 | 方針を請求に保存するタイミング | 解決済み（P-BIL-6）：最初の失敗の時点（§3.4） |
| Q7 | 決済の冪等キーの形式（Payments・Orchestration との食い違い） | 解決済み（P-X-1・P-X-3）：`invoice:{invoice_id}:{collection_seq}`。collection_seq はすべての新しい回収要求で増やし、再送では同じ番号。caller_managed は collection_seq=1 で作成（§2・§4） |
| Q8 | 決済代行の障害（`provider_unavailable`） | 解決済み（P-PAY-4）：段数を消費せず、リース切れで同じ collection_seq を再送（§3.4・§8） |

## 13. 変更履歴

| 日付 | 変更内容 | 変更者 |
|---|---|---|
| 2026-10-12 | 初版作成（Draft） | TL-Billing |
| 2026-10-12 | PdM レビューを反映（P-BIL-1〜6、P-X-1〜3、P-PAY-3・4、01 §5 v1.1）。回収要求の連番を `collection_seq` に改名しすべての新しい要求で増やす、冪等キーを `invoice:{invoice_id}:{collection_seq}` に統一、caller_managed は collection_seq=1 で作成、collection_due に collection_seq と dunning_stage を追加、collection_mode を `scheduled` / `caller_managed` に改名、`provider_unavailable` の扱いを追加、`billing.dunning_policy_updated` を削除し監査を auditctx に変更、AC-BIL-037・038 を追加。ステータスを Review に変更 | TL-Billing |
| 2026-10-12 | PdM の最終確認を経て Fixed とする | PdM |
| 2026-10-12 | 実行基盤の Lambda 化（P-ARC-1・3・7）を反映。§8 の回収時刻ジョブは job-runner が実行（時間切れ時は継続）、Outbox 中継を Flush／Sweep に、`invoice_collection_due` の購読先を `orc-collection` と注記、§5 の購読を Lambda の部分的なバッチ失敗で記述。契約・イベント・状態遷移は変更なし。ステータスを Review に変更 | TL-Billing |
| 2026-10-12 | 実行基盤の Lambda 化の反映について PdM の最終確認を経て、再び Fixed とする | PdM |
| 2026-10-12 | 実行基盤を ECS Fargate に戻す決定（P-ARC-10・11）を反映。回収時刻ジョブは Scheduler → SQS `scheduler` → worker-billing で実行し、カーソルを保存して障害時に続きから再開。Outbox 中継は worker-billing の常駐ループ、payments.* の購読は常駐の受信ループ（`batchItemFailures` の記述を削除）。`orc-collection`（P-ARC-7）の注記は維持。ステータスを Review に変更 | TL-Billing |
| 2026-10-13 | 実行基盤を ECS Fargate に戻した改訂（P-ARC-10・11、P-X-7）について PdM の最終確認を経て、Fixed とする | PdM |
| 2026-10-13 | テスト方針（P-TST-1〜7）を反映。§11 にテストのレベルの定義を追加し、AC-BIL のレベルを unit（純粋なロジック）・integration（本物の DB。並行処理・部分ユニーク・RLS・冪等性）・e2e（Playwright）で付け直し、e2e には対応する E2E シナリオを記載。AC-BIL-039（E2E-022）を追加。スタブ前提の記述を削除。ステータスを Review に変更 | TL-Billing |
| 2026-10-13 | P-TST-8 を反映。AC-BIL-032・034・038 の E2E シナリオ列に `e2e/tests/billing/dunning-policy.spec.ts`・`ops-collect.spec.ts` を記載。AC-BIL-031 は E2E-016 で確認（TL-Orchestration へ指示済み）。ステータスは Review のまま | TL-Billing |
| 2026-10-13 | テスト戦略の改訂（Playwright の E2E を中心にし、モックは原則使わない。P-TST-1〜10、P-X-8・9）について PdM の最終確認を経て、Fixed とする | PdM |
| 2026-10-25 | 実装時の決定（P-IMP-16）を反映：payments.* の payload は Billing の自前の型で読む、consumer_read は SELECT のみ、ID だけの contracts は事業者を引いてからテナントのスコープで更新、remaining_retries の定義、processing / payment_unknown への回収要求は caller_managed でも collection_in_progress、ジョブの dedupe は hourly、API の税率は "10%"。契約・状態遷移・イベントは変更なし | PdM |
