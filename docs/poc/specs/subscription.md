# 詳細要件: subscription（契約ドメイン）

| 項目 | 内容 |
|---|---|
| ステータス | **Fixed**（2026-10-13 PdM 承認。テスト戦略：Playwright の E2E が中心） |
| 担当 | TL-Subscription |
| 作成日 | 2026-10-12 |
| 上位文書 | `docs/poc/00-poc-charter.md`（最優先）、`docs/poc/02-pdm-decisions.md`（決定ログ。憲章と同じ効力）、`docs/poc/01-contracts-baseline.md`（§5 改訂 v1.1 を優先）、`docs/design/subscription-platform.md`、`docs/requirements/subscription-platform.md` |
| 対象モジュール | `modules/subscription` |

## 1. 概要と PoC の範囲

契約（Subscription）の状態・期間・予約（プラン変更／解約）・割引の残り回数・利用権を管理する。課金の流れ（請求・決済の順序）は持たず、Orchestration から contracts 経由で呼ばれる。

| 区分 | 内容 |
|---|---|
| 対象の要件 | FR-013（契約の作成部分）, FR-014, FR-015, FR-016, FR-017（契約の支払方法の付け替え部分）, FR-018, FR-019（期間・更新条件）, FR-021（状態の遷移部分）, FR-026（契約側の解約処理）, FR-008 / FR-009（照会 API）, D-01〜D-05, D-11, D-12 |
| 作るもの | Subscription 集約、TrialUsage 集約、subscription/contracts の全メソッド、`subscription.*` イベント、照会・予約 API（me / console / tenant）、トライアル終了予告の日次ジョブ |
| 作らないもの | 申込 API と支払方法変更 API（Orchestration, C-04）、請求・決済、日割り、解約済み契約の再開、数量課金（項目だけ持つ）、契約者氏名・メール・事業者名・プラン名の付与（ID だけ返す） |

## 2. 用語

| 用語 | 定義 |
|---|---|
| 期間（period） | `BillingPeriod{Start, End}`。両端を含む（D-11）。次の更新日 = End + 1 |
| 基準日（anchor_day） | 期間計算に使う「元の日」（1〜31）。トライアルなし＝申込日の日、トライアルあり＝初回課金日の日（D-12）。契約中は変えない（プラン変更・周期変更でも維持） |
| 更新日（next_renewal_date） | 次の期間の開始日。trialing では初回課金日 |
| 更新処理中（renewing） | `BeginRenewal` 済みで、確定（Confirm/Recover）も解約もしていない状態。`pending_renewal` が非 NULL。**状態ではなく属性** |
| 予約の適用日（effective_date） | 予約したプラン変更が適用される日／予約した解約で「解約済み」になる日。どちらも「ある期間の開始日」と一致する |
| 解約予約の出所（cancellation_source） | `customer`（契約者が予約。取り消せる）／`system`（事業者停止で予約。契約者は取り消せない, D-01） |
| 有効な契約（open） | pending_initial / trialing / active / past_due（D-05 の 3 状態に pending_initial を加える。P-SUB-3）。対象は退会の判定・事業者停止の対象・支払方法の使用中の判定 |

## 3. ドメインモデル

### 3.1 Subscription 集約

#### フィールド

| フィールド | 型 | 説明 |
|---|---|---|
| id | `ID[Subscription]` | UUIDv7 |
| tenant_id | `ID[Tenant]` | |
| customer_account_id | `ID[CustomerAccount]` | |
| tenant_customer_id | `TenantCustomerID`（`cus_…`） | 申込時に Orchestration が `EnsureTenantCustomerID` で得て `CreateSignupCmd.TenantCustomerID` で渡す値を持つ（P-SUB-1）。事業者 API は `(tenant_id, tenant_customer_id)` で検索する |
| signup_request_id | string | 申込の冪等キー由来の ID。`(customer_account_id, signup_request_id)` で一意 |
| product_id / plan_id / price_version_id | 型付き ID | 現在のプランと価格バージョン |
| interval | `BillingInterval` | 現在のプランの周期 |
| quantity | int | 常に 1（拡張点, §10） |
| status | `SubscriptionStatus` | 6 状態 |
| signup_date | `Date` | 申込日（JST） |
| current_period | `BillingPeriod` | trialing ではトライアル期間、pending_initial では初回の期間（予定） |
| next_renewal_date | `*Date` | 終端の状態では nil |
| anchor_day | int(1..31) | |
| trial | `*TrialInfo{Start, End Date}` | End = トライアル最終日（D-12） |
| trial_will_end_notified | bool | 予告の重複防止 |
| pending_plan_change | `*PendingPlanChange{PlanID, PriceVersionID, Interval, EffectiveDate}` | 予約時点の価格バージョンを固定する |
| cancellation | `*CancellationSchedule{Source, Reason, EffectiveDate}` | Reason = `customer` / `tenant_suspended` |
| pending_renewal | `*PendingRenewal{Period, PlanID, PriceVersionID, Interval, Discount *AppliedDiscount, PlanChanged bool, FirstCharge bool}` | 更新処理中の確定待ちの条件 |
| applied_discount | `*AppliedDiscount{CouponID, RedemptionID, Type(amount/percent), Value, DurationCycles, RemainingCycles}` | |
| payment_method_id | `ID[PaymentMethod]` | 必須 |
| past_due_invoice_id / last_paid_invoice_id | `*ID[Invoice]` | 冪等判定用 |
| cancel_reason / canceled_at | `customer`/`dunning`/`tenant_suspended`, time | 解約済みのときだけ |
| signup_failure_reason | string | signup_failed のときだけ |
| version | int | 楽観ロック。変更ごとに +1 |

#### 不変条件

| # | 不変条件 |
|---|---|
| I-1 | `current_period.Start <= current_period.End`。非終端なら `next_renewal_date = current_period.End + 1`（pending_renewal があっても確定まで変えない） |
| I-2 | `cancellation != nil` と `pending_plan_change != nil` は同時に成り立たない（解約予約が勝つ, FR-015） |
| I-3 | 終端（canceled / signup_failed）では next_renewal_date・pending_*・cancellation が nil |
| I-4 | `status = trialing` ⇔ trial != nil かつ未確定。trialing 中の current_period = trial 期間 |
| I-5 | pending_renewal は trialing / active / past_due でだけ存在する。past_due では必ず存在する |
| I-6 | 予約の EffectiveDate は「期間の開始日」と一致し、今日より後である |
| I-7 | `applied_discount.RemainingCycles` は 0 以上 DurationCycles 以下 |
| I-8 | 利用権 `entitled = status ∈ {trialing, active, past_due}`（日付では判定しない） |
| I-9 | plan の product_id は契約中に変わらない（プラン変更は同じ商品の中だけ） |

#### 期間・更新日の計算（sharedkernel の `Date.AddMonthsClamped` / `BillingPeriod.Next` を使う）

| 項目 | 計算 |
|---|---|
| 次の期間の開始日 | `s' = AddMonthsClamped(s, n, anchor_day)`。n = 1（month）/ 12（year）。月に anchor_day がなければ月末日 |
| 期間の終了日 | `End = s' - 1日` |
| トライアル | `trial.Start = signup_date`、`trial.End = signup_date + trial_days - 1`、`next_renewal_date = signup_date + trial_days`、anchor_day = 初回課金日の日（D-12） |
| トライアルなし | 初回の期間 = `{signup_date, Next.Start-1}`、anchor_day = signup_date の日 |
| 周期をまたぐ変更 | 新しい期間は更新日を開始日として、新しい周期で計算する。anchor_day は維持 |
| 遅れて処理する場合 | 期間の開始日は常に `next_renewal_date`（処理日ではない）。回収日にもずらさない（FR-021） |

計算例（AC で検証する）

| 開始 | 周期 | 期間の列 |
|---|---|---|
| 2026-10-15 | month | 10-15〜11-14, 11-15〜12-14 |
| 2027-01-31 | month | 01-31〜02-27, 02-28〜03-30, 03-31〜04-29, 04-30〜05-30, 05-31〜06-29 |
| 2028-02-29 | month | 02-29〜03-28, 03-29〜04-28, …, 2029-01-29〜02-27, 2029-02-28〜03-28 |
| 2028-02-29 | year | 2028-02-29〜2029-02-27, 2029-02-28〜2030-02-27, 2030-02-28〜2031-02-27, 2031-02-28〜2032-02-28, 2032-02-29〜 |
| 2027-01-17 申込, trial 14 日 | month | trial 01-17〜01-30、初回課金 01-31（anchor 31）、01-31〜02-27, 02-28〜03-30 |

#### 予約の適用日の決め方

| 条件 | プラン変更の EffectiveDate | 解約の EffectiveDate |
|---|---|---|
| 更新処理中でなく、今日 < next_renewal_date | next_renewal_date | next_renewal_date |
| 更新処理中（pending_renewal あり） | `pending_renewal.Period.End + 1`（FR-015 の 3 つ目） | 同左 |
| 更新処理中でなく、今日 ≥ next_renewal_date（日次処理の前） | 次の次の更新日（「前日まで」, FR-015） | next_renewal_date（その日の更新で解約し、課金しない。P-SUB-4） |

`BeginRenewal(date)` は `EffectiveDate <= 処理する期間の開始日` の予約だけを適用する。

#### 状態遷移の完全な表

凡例：`→X` 遷移／`=` 状態は変えず属性だけ変更／`○` 冪等（変更なし・イベントなし・成功を返す）／`E:x` エラー（§4.3）。R = 更新処理中。

| コマンド＼状態 | pending_initial | trialing | active | past_due | canceled | signup_failed |
|---|---|---|---|---|---|---|
| CreateForSignup | （作成）トライアル適用なら →trialing、それ以外 →pending_initial。同じ signup_request_id は ○（既存を返す） | ← | ← | ← | ← | ← |
| Activate(inv) | →active | E:invalid_transition | last_paid=inv なら ○、他は E:invalid_transition | E:invalid_transition | E:terminal | E:terminal |
| MarkSignupFailed | →signup_failed | E:invalid_transition | E:invalid_transition | E:invalid_transition | E:terminal | ○ |
| BeginRenewal(date) | E:not_renewable | date < next_renewal_date は E:not_due。解約予約の適用日 ≤ 更新日なら →canceled（Ended）。それ以外 = R にして Terms。R 済みで同じ期間なら ○（同じ Terms） | ←（同じ） | R の期間開始 = 更新日なら ○（同じ Terms）、他は E:not_renewable（D-02） | ○（Ended を返す） | E:terminal |
| ConfirmRenewal(inv) | E:invalid_transition | R なら →active（R を確定） | R なら =（R を確定）。R なしで last_paid=inv なら ○ | Recover と同じ | E:terminal | E:terminal |
| MarkPastDue(inv) | E:invalid_transition | R なら →past_due。解約予約ありなら →canceled（予約の reason） | 同左。R なしで last_paid=inv なら ○（順序の入れ替わり） | past_due_invoice=inv なら ○、他は E:invoice_mismatch | ○ | E:terminal |
| Recover(inv) | E:invalid_transition | E:invalid_transition | last_paid=inv なら ○、他は E:invalid_transition | past_due_invoice=inv なら →active（R を確定）、他は E:invoice_mismatch | E:terminal | E:terminal |
| Cancel(reason) | E:invalid_transition | →canceled | →canceled | →canceled | ○ | E:terminal |
| ScheduleSystemCancellation(reason) | =（予約を付ける。Activate 後に有効） | = source=system（customer の予約は上書き、プラン変更予約は取り消し） | ←（同じ） | →canceled（reason=tenant_suspended, D-01） | ○ | ○ |
| ScheduleCancellation（契約者） | E:state_conflict | = source=customer（プラン変更予約は取り消し）。既に予約済み（customer/system）は ○ | ←（同じ） | →canceled（reason=customer, D-03） | E:terminal | E:terminal |
| RevokeCancellation（契約者） | E:state_conflict | customer の予約を削除。予約なしは ○。system は E:not_revocable | ←（同じ） | ○（予約は存在しえない） | E:terminal | E:terminal |
| SchedulePlanChange（契約者） | E:state_conflict | 予約／上書き。解約予約ありは E:cancellation_scheduled。同じ内容は ○ | ←（同じ） | E:state_conflict | E:terminal | E:terminal |
| RevokePlanChange（契約者） | ○ | 予約を削除。なしは ○ | ←（同じ） | ←（同じ） | E:terminal | E:terminal |
| ChangePaymentMethod(pm) | E:state_conflict | = 付け替え。同じ pm は ○ | ←（同じ） | = 付け替え。**同じ pm でもイベントを出す**（即時再決済のきっかけ, FR-017） | E:terminal | E:terminal |
| NotifyTrialWillEnd(date) | ○ | trial.End = date+3 かつ未通知なら = 通知済みにする。他は ○ | ○ | ○ | ○ | ○ |

補足
- trialing の R を確定すると、トライアルから有効になる（`FirstCharge = true`）。trialing → past_due はトライアル後の初回課金の失敗。
- 「R 中に予約された解約」は、R が確定すれば新しい期間の終わりで解約、R が失敗（MarkPastDue）すればその場で解約する（支払い遅延中の解約は即時、D-01・D-03 と同じ考え方）。

#### メソッドと発行するイベント

| メソッド | 主な処理 | 発行するイベント |
|---|---|---|
| `NewForSignup` | §4 CreateForSignup を参照 | trialing なら `subscription.started`。pending_initial は発行しない（不成立時に開始 Webhook を出さない, FR-013） |
| `Activate` | last_paid_invoice_id = inv | `subscription.started` |
| `MarkSignupFailed` | | `subscription.signup_failed` |
| `BeginRenewal` | 予約・割引・トライアル終了を反映して R を作る（§4.2） | Ended のとき `subscription.canceled`（reason = 予約の reason） |
| `ConfirmRenewal` / `Recover` | R を確定：current_period・plan・price_version・interval・applied_discount を差し替え、next_renewal_date 更新、trial を確定済みに、last_paid_invoice_id = inv | `subscription.renewed`（trial からなら `first_charge: true`）または `subscription.recovered`。プランが変わったら加えて `subscription.plan_changed` |
| `MarkPastDue` | past_due_invoice_id = inv | `subscription.past_due`（解約予約ありなら `subscription.canceled`） |
| `Cancel` | 予約・R をすべて破棄、canceled_at | `subscription.canceled` |
| `ScheduleCancellation` / `ScheduleSystemCancellation` | | `subscription.cancellation_scheduled`（payload に `reason` = customer / tenant_suspended, P-INT-6。プラン変更予約を消したら先に `subscription.plan_change_revoked`）。即時解約なら `subscription.canceled` |
| `RevokeCancellation` | | `subscription.cancellation_revoked` |
| `SchedulePlanChange` / `RevokePlanChange` | | `subscription.plan_change_scheduled` / `subscription.plan_change_revoked` |
| `ChangePaymentMethod` | | `subscription.payment_method_changed` |
| `MarkTrialWillEndNotified` | | `subscription.trial_will_end` |

#### イベントの payload（スナップショット）

01 の必須項目に、次を加える（加算のみ）。

```json
{
  "subscription": {
    "subscription_id": "…", "tenant_id": "…", "customer_account_id": "…",
    "product_id": "…", "plan_id": "…", "price_version_id": "…", "interval": "month", "quantity": 1,
    "status": "active",
    "current_period": {"start": "2026-10-15", "end": "2026-11-14"},
    "next_renewal_date": "2026-11-15", "trial_end": null,
    "cancel_at_period_end": false,
    "cancellation": null,                      // {source, reason, effective_date}
    "pending_plan_id": null,
    "pending_plan_change": null,               // {plan_id, effective_date}
    "renewal_in_progress": false,
    "entitled": true,
    "cancel_reason": null, "canceled_at": null,
    "version": 3
  },
  "...イベント固有の項目": "01 の表のとおり（invoice_id, from_plan_id, to_plan_id, effective_date, reason, trial_end）。cancellation_scheduled は effective_date と reason（customer / tenant_suspended, P-INT-6）"
}
```

- エンベロープの任意項目 `actor`（01 §5.1）を設定する：契約者の API は `consumer`、Orchestration・ジョブからのコマンドは `system`（id は呼び出し元が context で渡したもの）。
- エンベロープの `aggregate_version` = 発行時の version。1 トランザクションで複数のイベントを出す場合は同じ version で、Outbox への書き込み順に並べる。
- `subscription.signup_failed` と `subscription.payment_method_changed` は 01 の最小項目（スナップショットなし）＋ tenant_id。

### 3.2 TrialUsage 集約

| 項目 | 内容 |
|---|---|
| フィールド | tenant_id, customer_account_id, product_id, subscription_id, used_on(Date) |
| 不変条件 | `(customer_account_id, product_id)` で 1 件まで（FR-014） |
| 記録するとき | **トライアルを実際に適用した契約（trialing で作成）のときだけ**、同じトランザクションで記録する。トライアルなしの契約では記録しない（「トライアルを使ったか」の記録であり、トライアルなしプランの契約者が後でトライアルを使うのは妨げない） |
| 解約・退会 | 削除しない（トライアル中の解約でも利用済みのまま） |
| 同時申込 | 主キー違反になった側は `ErrTrialNotEligible` |

## 4. 公開する契約（`modules/subscription/contracts`）

### 4.1 interface

```go
type Service interface {
    CreateForSignup(ctx context.Context, cmd CreateSignupCmd) (SubscriptionView, error)
    Activate(ctx context.Context, subID ID[Subscription], invoiceID ID[Invoice]) (SubscriptionView, error)
    MarkSignupFailed(ctx context.Context, subID ID[Subscription], reason string) error
    BeginRenewal(ctx context.Context, subID ID[Subscription], date Date) (RenewalOutcome, error)
    ConfirmRenewal(ctx context.Context, subID ID[Subscription], invoiceID ID[Invoice]) (SubscriptionView, error)
    MarkPastDue(ctx context.Context, subID ID[Subscription], invoiceID ID[Invoice]) (SubscriptionView, error)
    Recover(ctx context.Context, subID ID[Subscription], invoiceID ID[Invoice]) (SubscriptionView, error)
    Cancel(ctx context.Context, subID ID[Subscription], reason CancelReason) (SubscriptionView, error)
    ScheduleSystemCancellation(ctx context.Context, subID ID[Subscription], reason CancelReason) (SystemCancellationResult, error)
    ChangePaymentMethod(ctx context.Context, subID ID[Subscription], customerAccountID ID[CustomerAccount], pmID ID[PaymentMethod]) (SubscriptionView, error)
    ListDueForRenewal(ctx context.Context, date Date, cursor string, limit int) (Page[SubscriptionRef], error)
    ListTrialEndingOn(ctx context.Context, date Date, cursor string, limit int) (Page[SubscriptionRef], error)
    ListOpenByTenant(ctx context.Context, tenantID ID[Tenant], cursor string, limit int) (Page[SubscriptionRef], error)
    HasOpenSubscriptions(ctx context.Context, customerAccountID ID[CustomerAccount]) (bool, error)
    IsPaymentMethodInUse(ctx context.Context, pmID ID[PaymentMethod]) (bool, error)
    Get(ctx context.Context, subID ID[Subscription]) (SubscriptionView, error)
}
```

戻り値に `SubscriptionView` を加えたのは、Orchestration が状態を再取得せずに次の手を決められるようにするため（01 で許された DTO の追加の範囲）。

### 4.2 DTO

| DTO | 項目 |
|---|---|
| `CreateSignupCmd` | tenant_id, customer_account_id, **TenantCustomerID**（必須, P-SUB-1）, signup_request_id, plan（`PlanSnapshot` から plan_id, product_id, price_version_id, interval, trial_days）, payment_method_id, discount `*DiscountInput{coupon_id, redemption_id, type, value, duration_cycles}`, signup_date Date, allow_trial_fallback bool |
| `SubscriptionView` | §3.1 スナップショットの項目 ＋ tenant_customer_id, payment_method_id, applied_discount, `initial_terms *Terms`（pending_initial のときだけ） |
| `RenewalOutcome` | `Kind`（`ended` / `terms`）, `Terms *Terms`, `Subscription SubscriptionView` |
| `Terms` | period, plan_id, price_version_id, interval, quantity, discount `*AppliedDiscount`（この請求に適用する割引。適用しないなら nil）, payment_method_id, first_charge bool, plan_changed bool |
| `SystemCancellationResult` | `Kind`（`scheduled` / `canceled_immediately` / `noop`）, effective_date, Subscription |
| `SubscriptionRef` | subscription_id, tenant_id, status, next_renewal_date / trial_end |
| `Page[T]` | items, next_cursor（キーセット。空なら終わり） |

**CreateForSignup の判定**

| 条件 | 結果 |
|---|---|
| trial_days > 0 かつ TrialUsage なし | trialing で作成、TrialUsage を記録、割引は消費しない（初回課金で使う） |
| trial_days > 0 かつ TrialUsage あり、`allow_trial_fallback = false` | `ErrTrialNotEligible`（申込画面で「トライアル対象外」を示す, FR-014） |
| 同上、`allow_trial_fallback = true` | トライアルなしとして pending_initial |
| trial_days = 0 | pending_initial。initial_terms を返す。割引があれば RemainingCycles = DurationCycles - 1 |
| 同じ (customer_account_id, signup_request_id) が既にある | 既存を返す（中身が違っても既存を返す。Orchestration の冪等キーで中身の一致は保証済み） |

**BeginRenewal の処理順（date = 処理日。s = next_renewal_date）**

1. 状態チェック（§3.1 表）。`date < s` は `ErrNotDue`。R 済みで `R.Period.Start = s` なら同じ Terms を返す。
2. 解約予約があり `EffectiveDate <= s` → canceled（reason = 予約の reason）、`Ended`。割引・プラン変更予約は破棄。
3. プラン変更予約があり `EffectiveDate <= s` → 予約の plan / price_version / interval を採用し、予約を消す（`plan_changed = true`）。予約先のプランが非公開になっていても適用する（受付済みのため）。
4. 新しい期間 = `{s, Next(s, interval, anchor_day).Start - 1}`。
5. 割引：`RemainingCycles > 0` なら Terms.discount に入れ、R 側の RemainingCycles を 1 減らす（**BeginRenewal で減らす**。理由：請求の条件を確定した時点で 1 回分を使ったことにする。R で冪等なので二重に減らない。R の請求は「支払済」か「解約に伴う回収不能・無効」にしかならず、戻す場面がない）。0 なら nil。
6. trialing なら `first_charge = true`。
7. payment_method_id は現在の値。R を保存して Terms を返す。

### 4.3 エラー

| Go のエラー | 意味 | HTTP（API で使う場合） |
|---|---|---|
| `ErrNotFound` | 存在しない／別テナント／別の契約者 | 404 `not_found` |
| `ErrInvalidTransition{From, Command}` | Orchestration からの不正な遷移（バグまたは順序の乱れ） | —（contracts 専用） |
| `ErrTerminal` | 終端の状態への操作 | 409 `subscription_terminal` |
| `ErrStateConflict` | 今の状態ではできない契約者操作 | 409 `subscription_state_conflict` |
| `ErrNotDue` / `ErrNotRenewable` | 更新日前／更新対象外（past_due など） | — |
| `ErrInvoiceMismatch` | 渡された invoice_id が記録と違う | — |
| `ErrCancellationScheduled` | 解約予約中のプラン変更 | 409 `cancellation_scheduled` |
| `ErrCancellationNotRevocable` | システムが予約した解約の取り消し | 409 `cancellation_not_revocable` |
| `ErrPlanChangeInvalid{Reason}` | Reason = `different_product` / `plan_unavailable` / `same_plan` | 422 `plan_change_not_allowed`（errors[].code に Reason） |
| `ErrTrialNotEligible` | トライアルを使用済み | 422 `trial_not_eligible`（Orchestration が返す） |
| `ErrVersionConflict` | 楽観ロックの衝突（app 層で 3 回まで再試行した後） | 409 `conflict` |

## 5. 購読するイベントと処理

**購読するイベントはない。** 状態を変える入力はすべて Orchestration からの同期コマンド（契約者・事業者の API は自モジュール内）で受ける。

| 想定される入力 | 扱い |
|---|---|
| `tenant.suspended` | Orchestration が受けて `ListOpenByTenant` → `ScheduleSystemCancellation` を呼ぶ |
| `catalog.plan_unpublished` / `price_version_added` | 既存契約に影響しない（FR-004）。購読しない |
| `customer.withdrawn` | D-05 により有効な契約がある契約者は退会できないので、購読しない |

**コマンドの冪等性**（イベントの重複・再実行・順序の入れ替わりに耐える）

| 仕組み | 内容 |
|---|---|
| 遷移表の ○ | 同じコマンドの再実行は成功を返し、イベントを出さない |
| invoice_id の記録 | Activate / ConfirmRenewal / Recover / MarkPastDue は last_paid_invoice_id・past_due_invoice_id と照合 |
| R の期間 | BeginRenewal は `R.Period.Start` で同じ更新かを判定 |
| signup_request_id | CreateForSignup の再実行 |
| 楽観ロック | `UPDATE … WHERE id=$1 AND version=$2`。衝突したら集約を読み直して再適用（最大 3 回）。再適用で ○ になればそれで成功 |
| Outbox | 集約の保存と同じトランザクションで `subscription.outbox_events` に書く |

## 6. 永続化（schema: `subscription`）

```sql
CREATE TABLE subscriptions (
  id                         uuid PRIMARY KEY,
  tenant_id                  uuid NOT NULL,
  customer_account_id        uuid NOT NULL,
  tenant_customer_id         text NOT NULL,
  signup_request_id          text NOT NULL,
  product_id                 uuid NOT NULL,
  plan_id                    uuid NOT NULL,
  price_version_id           uuid NOT NULL,
  billing_interval           text NOT NULL CHECK (billing_interval IN ('month','year')),
  quantity                   integer NOT NULL DEFAULT 1 CHECK (quantity >= 1),
  status                     text NOT NULL CHECK (status IN ('pending_initial','trialing','active','past_due','canceled','signup_failed')),
  signup_date                date NOT NULL,
  current_period_start       date NOT NULL,
  current_period_end         date NOT NULL,
  next_renewal_date          date NULL,
  billing_anchor_day         smallint NOT NULL CHECK (billing_anchor_day BETWEEN 1 AND 31),
  trial_start                date NULL,
  trial_end                  date NULL,
  trial_will_end_notified    boolean NOT NULL DEFAULT false,
  pending_plan_id            uuid NULL,
  pending_price_version_id   uuid NULL,
  pending_plan_interval      text NULL CHECK (pending_plan_interval IN ('month','year')),
  pending_plan_effective_date date NULL,
  cancel_at_period_end       boolean NOT NULL DEFAULT false,
  cancellation_source        text NULL CHECK (cancellation_source IN ('customer','system')),
  cancellation_reason        text NULL CHECK (cancellation_reason IN ('customer','tenant_suspended')),
  cancellation_effective_date date NULL,
  pending_renewal            jsonb NULL,
  applied_discount           jsonb NULL,
  payment_method_id          uuid NOT NULL,
  past_due_invoice_id        uuid NULL,
  last_paid_invoice_id       uuid NULL,
  cancel_reason              text NULL CHECK (cancel_reason IN ('customer','dunning','tenant_suspended')),
  canceled_at                timestamptz NULL,
  signup_failure_reason      text NULL,
  version                    integer NOT NULL DEFAULT 1,
  created_at                 timestamptz NOT NULL,
  updated_at                 timestamptz NOT NULL,
  UNIQUE (customer_account_id, signup_request_id),
  CHECK (current_period_start <= current_period_end),
  CHECK (cancel_at_period_end = (cancellation_source IS NOT NULL)),
  CHECK ((cancellation_source IS NULL) = (cancellation_effective_date IS NULL)),
  CHECK ((pending_plan_id IS NULL) = (pending_plan_effective_date IS NULL)),
  CHECK (NOT (cancel_at_period_end AND pending_plan_id IS NOT NULL)),                     -- I-2
  CHECK (status NOT IN ('canceled','signup_failed')
         OR (next_renewal_date IS NULL AND pending_renewal IS NULL AND NOT cancel_at_period_end AND pending_plan_id IS NULL)), -- I-3
  CHECK (status <> 'canceled' OR (cancel_reason IS NOT NULL AND canceled_at IS NOT NULL)),
  CHECK (status <> 'trialing' OR trial_end IS NOT NULL),
  CHECK (status <> 'past_due' OR (pending_renewal IS NOT NULL AND past_due_invoice_id IS NOT NULL))  -- I-5
);

CREATE TABLE trial_usages (
  customer_account_id uuid NOT NULL,
  product_id          uuid NOT NULL,
  tenant_id           uuid NOT NULL,
  subscription_id     uuid NOT NULL,
  used_on             date NOT NULL,
  created_at          timestamptz NOT NULL,
  PRIMARY KEY (customer_account_id, product_id)
);
-- outbox_events / processed_messages は platform の共通定義をこのスキーマに作る
```

| インデックス | 用途 |
|---|---|
| `(next_renewal_date, id) WHERE status IN ('trialing','active')` | ListDueForRenewal（`next_renewal_date <= $date`、キーセット） |
| `(trial_end, id) WHERE status = 'trialing'` | ListTrialEndingOn |
| `(tenant_id, status, created_at DESC, id)` | console 一覧 |
| `(tenant_id, tenant_customer_id, created_at DESC, id)` | tenant API・entitlements |
| `(customer_account_id, created_at DESC, id)` | マイページ・HasOpenSubscriptions |
| `(tenant_id, id) WHERE status IN ('pending_initial','trialing','active','past_due')` | ListOpenByTenant |
| `(payment_method_id) WHERE status IN ('pending_initial','trialing','active','past_due')` | IsPaymentMethodInUse |

**RLS**（仕組みは TL-Platform。本モジュールの要求）

| ポリシー | 条件 | 使う場面 |
|---|---|---|
| tenant_isolation | `tenant_id = current_setting('app.tenant_id', true)::uuid` | console（ヘッダー `X-Tenant-Id` で決まるテナント, P-IDN-1）/ tenant API / テナントが分かっているコマンド |
| consumer_self | `customer_account_id = current_setting('app.customer_account_id', true)::uuid`（SELECT・UPDATE のみ） | `/v1/me`（事業者をまたぐ一覧, FR-018） |
| システムロール（BYPASSRLS） | — | テナントをまたぐ一覧（ListDueForRenewal / ListTrialEndingOn / HasOpenSubscriptions / IsPaymentMethodInUse）。読み出した後のコマンドは、契約の tenant_id を設定したトランザクションで行う |

trial_usages にも tenant_isolation と consumer_self を同じ条件でかける。

## 7. API

共通：エラーは Problem Details、一覧はカーソル方式（limit 既定 20・上限 100）。
- 予約を含むレスポンスでは、適用日（`scheduled_plan_change.effective_date` / `scheduled_cancellation.effective_date`）を必ず返す（P-SUB-4）。
- 停止中の事業者でも、console の閲覧と事業者 API での照会はできる（P-IDN-4。本モジュールの API はすべて参照系なので制限しない）。
- **監査（P-X-2）**：console・ops から契約を変更する操作は PoC にはない（本モジュールの console API は参照のみ）。追加する場合は、ユースケースで `internal/platform/auditctx` に監査の注釈（操作の種類・対象の subscription_id・変更前後の値）を書き、監査ミドルウェアに記録させる。監査のためだけのイベントは出さない。契約者の操作（予約・取り消し）を監査の対象にするかは、TL-Integration の FR-025 の定義に従う。PUT / DELETE は意味として冪等なので `Idempotency-Key` は必須にしない（送られた場合の扱いは Platform の共通処理に従う）。

### 7.1 リソースの形

| 項目 | me | console / tenant |
|---|---|---|
| id, tenant_id, product_id, plan_id, price_version_id, interval, quantity, status, entitled | ○ | ○（tenant_id は出さない） |
| current_period{start,end}, next_renewal_date, trial{start,end} | ○ | ○ |
| scheduled_plan_change{plan_id, effective_date} / scheduled_cancellation{effective_date, revocable, source} | ○ | ○ |
| renewal_in_progress, cancel_reason, canceled_at, created_at | ○ | ○ |
| customer_id（`cus_…`） | × | ○ |
| payment_method_id, applied_discount{remaining_cycles} | ○ | ×（console は applied_discount のみ ○） |
| customer_account_id | × | × |

### 7.2 エンドポイント

| メソッド・パス | 主体 | リクエスト | レスポンス | エラー |
|---|---|---|---|---|
| GET /v1/me/subscriptions | consumer | `status`（複数可）, limit, starting_after | 200 一覧（事業者をまたぐ, FR-018） | 400 validation_failed |
| GET /v1/me/subscriptions/{id} | consumer | — | 200 リソース | 404（他人の契約も 404） |
| PUT /v1/me/subscriptions/{id}/scheduled-plan-change | consumer | `{plan_id}` | 200 リソース（`scheduled_plan_change.effective_date` を必ず含む, FR-015） | 404, 409 cancellation_scheduled / subscription_state_conflict / subscription_terminal / conflict, 422 plan_change_not_allowed |
| DELETE 同上 | consumer | — | 200 リソース（予約なしでも 200） | 404, 409 subscription_terminal |
| PUT /v1/me/subscriptions/{id}/scheduled-cancellation | consumer | `{}` | 200 リソース。`scheduled_cancellation.effective_date` を必ず含む（P-SUB-4）。past_due なら `status=canceled`（即時, D-03）で `canceled_at` を返す | 404, 409 subscription_state_conflict（pending_initial）/ subscription_terminal / conflict |
| DELETE 同上 | consumer | — | 200 リソース | 404, 409 cancellation_not_revocable / subscription_terminal |
| GET /v1/console/subscriptions | staff（owner/admin/viewer） | `status`（複数可）, `plan_id`, `customer_id`, limit, starting_after | 200 一覧 | 400, 403 forbidden |
| GET /v1/console/subscriptions/{id} | staff | — | 200 リソース（請求履歴は含めない。Billing の `GET /v1/console/subscriptions/{id}/invoices` で返す, P-SUB-2） | 404 |
| GET /v1/tenant/subscriptions | tenant_api | `customer_id`（任意）, `status`, limit, starting_after | 200 一覧 | 400 |
| GET /v1/tenant/subscriptions/{id} | tenant_api | — | 200 リソース | 404（他テナントも 404, FR-008） |
| GET /v1/tenant/customers/{customerId}/entitlements | tenant_api | `product_id`（任意） | 200 `{customer_id, entitled, products:[{product_id, entitled, subscription_ids[]}]}`。entitled = 対象に利用権のある契約が 1 件以上 | 404（当テナントに該当 cus_ の契約が 1 件もない／他テナントの cus_） |

**プラン変更の検証**（PUT scheduled-plan-change）：catalog/contracts の `GetPlanSnapshot` を呼ぶ（01 で許可された「ID の検証のみ」の用途）。

| 条件 | 結果 |
|---|---|
| 別の商品 | 422 `different_product` |
| 非公開・存在しない | 422 `plan_unavailable` |
| 現在のプランと同じ | 422 `same_plan`（予約を消したいときは DELETE） |
| 予約中と同じプラン | 200（○） |
| OK | 予約時点の price_version_id・interval を保存 |

**支払い遅延中の解約（D-03）の分担**（P-ORC-1 で決定）

| 段階 | 担当 | 内容 |
|---|---|---|
| 1 | Subscription（本 API） | 同じトランザクションで canceled（reason=customer）にし、`subscription.canceled` を Outbox に書く。200 を返す |
| 2 | Orchestration | `subscription.canceled` を購読し、`Billing.VoidOpenInvoices(subID, reason=subscription_canceled)` を呼ぶ（冪等） |
| 3 | Orchestration | 決済の直前（`invoice_collection_due` の処理時）に `Subscription.Get` で canceled なら決済しない（段階 2 より先にリトライが来た場合の防御） |

Subscription → Billing の依存を作らないため、無効化は非同期にする（設計書 §3 の「SubscriptionCanceled → Orchestration が残りの請求を無効にする」に沿う）。

## 8. ジョブ・非同期処理

| ジョブ | 担当 | 起動 | 処理 | 冪等性 |
|---|---|---|---|---|
| trial-will-end | **subscription**（module.go でスケジューラーに登録, P-SUB-6） | 毎日 09:00 JST。dev は `POST /v1/dev/jobs/trial-will-end/run` | `trial_end = today + 3` の trialing をキーセットで 500 件ずつ取得 → 契約ごとに tenant を設定したトランザクションで `MarkTrialWillEndNotified` | `trial_will_end_notified` で 1 回だけ。trial_days が 3 以下の契約は対象日が過去になるので送らない |
| 更新処理 | Orchestration | 毎日 00:30 JST | `ListDueForRenewal(today)` → FIFO → `BeginRenewal` | 遷移表の ○ |
| 事業者停止の一括解約 | Orchestration | `tenant.suspended` | `ListOpenByTenant` → `ScheduleSystemCancellation` | 遷移表の ○ |

`ListDueForRenewal(date)` の条件：`status IN (trialing, active) AND next_renewal_date <= date`（遅れた分も拾う。R 済みも含めて再実行を冪等に返す）。past_due は含めない（D-02）。

## 9. 異常系・境界値・競合

| # | ケース | 振る舞い |
|---|---|---|
| X-1 | 更新処理中にプラン変更を予約（FR-015-3） | 受け付ける。適用日 = R 期間の終了日 + 1。レスポンスに適用日を出す |
| X-2 | 更新処理中に解約を予約 | 受け付ける。R 確定 → 新しい期間の終わりで解約、R 失敗 → MarkPastDue で即時解約 |
| X-3 | 更新処理中に事業者停止 | 同上（source=system）。D-01 の「払った期間は使える」「支払い遅延は即時」と一致 |
| X-4 | 契約者の予約の後にシステムが予約 | source を system に上書き。以後、契約者は取り消せない |
| X-5 | 解約予約中にプラン変更 | 409 cancellation_scheduled（先に解約予約を取り消す） |
| X-6 | プラン変更予約中に解約予約 | プラン変更予約を消して解約予約だけ残す（FR-015-2） |
| X-7 | 予約後に価格改定・プラン非公開 | 予約時の価格バージョンで適用する |
| X-8 | 支払い遅延中の回収が、次の更新日より後 | Recover で R を確定。next_renewal_date が過去なら、翌日以降の更新処理が 1 日 1 期間ずつ追いつく（期間はずらさない） |
| X-9 | 解約直後に進行中の決済が成功（Recover / ConfirmRenewal が canceled に来る） | `ErrTerminal`。契約は戻さない。Orchestration が運用者に通知（返金は運用, P-SUB-5・P-BIL-3） |
| X-10 | イベントの順序が入れ替わる（InvoicePaid の後に古い PaymentFailed 由来の MarkPastDue） | last_paid_invoice_id と一致すれば ○ |
| X-11 | 同時に 2 つのトライアル申込（同じ商品） | 主キー違反の側は ErrTrialNotEligible |
| X-12 | 契約者の操作とバッチが衝突 | 楽観ロックで再読込・再適用（最大 3 回）。それでも衝突なら 409 conflict |
| X-13 | トライアル中の解約（FR-014-2） | 初回課金日（trial_end + 1）の BeginRenewal で Ended。請求は作られない |
| X-14 | 申込日が月末・2/29 でトライアルあり | anchor_day は初回課金日の日。例：01-17 + 14 日 → 01-31（anchor 31） |
| X-15 | 1 年プランで 2/29 開始 | 平年は 02-28、うるう年に 02-29 に戻る |
| X-16 | BeginRenewal が更新日より前に呼ばれる | ErrNotDue（バッチの日付誤り防止） |
| X-17 | pending_initial のまま事業者停止 | 予約だけ付ける。Activate すれば期間の終わりで解約、不成立なら signup_failed |

## 10. 拡張ポイント

| 拡張 | PoC での扱い |
|---|---|
| 数量課金（quantity） | 列・DTO・Terms・スナップショットに quantity を持ち、常に 1。数量変更の予約は PendingPlanChange に quantity を足して表現する想定（今は作らない） |
| 従量課金 | Terms は価格を持たず price_version_id だけを返す（金額は Catalog の QuotePrice）。契約側の変更は不要 |
| 期間途中の変更・日割り | EffectiveDate を期間の開始日に限る（I-6）。将来は I-6 を緩める |
| 一時停止（pause） | status の CHECK と遷移表に追加する。entitled の判定を 1 か所（I-8）に閉じているので影響を限定できる |
| 契約者の表示情報（事業者名・プラン名・氏名） | API は ID だけ。必要になれば BFF か読み取りモデルで付与する |

## 11. 受け入れ条件

テストのレベルは P-TST-1〜7 に従う。モック・フェイクは使わない。
- **unit**：Go のテーブル駆動テスト。DB も外部も使わない純粋なドメインロジック（期間と更新日の計算、状態遷移の表、予約の適用のルール）。集約のメソッドを直接呼び、集約に記録されたドメインイベントを確かめる。
- **integration**：Go のテストで本物の PostgreSQL（testcontainers-go）を使う。楽観ロックの衝突と RLS に限る。
- **e2e**：Playwright（`e2e/`）。API のレスポンスを中心に確かめ、API から見えない不変条件だけ `sp_e2e_reader` で DB を読む（P-TST-3）。時刻を進めるものは `clock` project で直列に流す（P-TST-4）。
- 「E2E」列（P-TST-8）：Playwright のどちらのテストで確かめるかを書く。
  - `E2E-xxx`：課金の流れを通すシナリオ（`e2e/tests/scenarios/`、`orchestration.md` の E2E-001〜022）。
  - `subscription/<名前>.spec.ts`：ドメインごとの API テスト（`e2e/tests/subscription/`）。ファイル名は案。時刻を進めるテストはファイル名の末尾を `.clock.spec.ts` にし、`clock` project で直列に流す（P-TST-4・P-TST-9）。時刻を進めるテストと進めないテストは同じファイルに混ぜない。
  - unit / integration の AC では、同じ振る舞いを通しで確かめるシナリオを参考として書く。
  - PoC の完了条件には、シナリオとドメインの API テストの両方を含む（P-TST-8）。

| ID | 要件 | レベル | E2E | Given / When / Then |
|---|---|---|---|---|
| AC-SUB-001 | FR-013, D-11 | unit | E2E-001 | Given 月額・トライアルなし When 2026-10-15 に NewForSignup Then pending_initial、initial_terms.period = 10-15〜11-14、next_renewal_date = 11-15、ドメインイベントなし |
| AC-SUB-002 | FR-013 | unit | E2E-001 | Given AC-001 の契約 When Activate(inv) Then active、`subscription.started` が 1 件、entitled = true。同じ inv で再度 Activate しても成功しイベントは増えない |
| AC-SUB-003 | FR-013 | unit | E2E-021(a) | Given pending_initial When MarkSignupFailed Then signup_failed、`subscription.signup_failed` が出て `started` は出ない |
| AC-SUB-004 | FR-013, NFR-006 | e2e | E2E-013 | Given 同じ Idempotency-Key・同じ本文の申込 When 連続・並行に 2 回送る Then 契約は 1 件（同じ id）。DB の subscriptions も 1 行 |
| AC-SUB-005 | FR-019 | unit | E2E-004 | Given 2027-01-31 開始の月額 When 期間を 4 回進める Then 更新日は 02-28, 03-31, 04-30, 05-31 |
| AC-SUB-006 | FR-019 | unit | — | Given 2028-02-29 開始の月額 When 期間を 13 回進める Then 更新日は 2028-03-29 … 2029-01-29, 2029-02-28, 2029-03-29（平年の 2 月だけ 28 日） |
| AC-SUB-007 | FR-019 | unit | — | Given 2028-02-29 開始の年額 When 期間を 4 回進める Then 更新日は 2029-02-28, 2030-02-28, 2031-02-28, 2032-02-29、最初の期間は 2028-02-29〜2029-02-27 |
| AC-SUB-008 | FR-014, D-12 | unit | E2E-002 | Given trial 14 日のプラン・TrialUsage なし When 2027-01-17 に NewForSignup Then trialing、trial = 01-17〜01-30、next_renewal_date = 01-31、anchor_day = 31、`subscription.started`、TrialUsage を作る |
| AC-SUB-009 | FR-019, D-12 | unit | E2E-002 | Given AC-008 の契約 When 01-31 に BeginRenewal → ConfirmRenewal Then active、current_period = 01-31〜02-27、次は 02-28、`subscription.renewed`（first_charge = true） |
| AC-SUB-010 | FR-014 | unit ＋ e2e | subscription/trial.spec.ts | Given 商品 P の TrialUsage あり When trial 付きプランで allow_trial_fallback=false Then ErrTrialNotEligible。true なら pending_initial。e2e（API テスト）：トライアル利用済みの契約者が同じ商品のトライアル付きプランに申込むと 422 `trial_not_eligible`、フォールバック指定なら初回決済して active |
| AC-SUB-011 | FR-014 | integration | — | Given 商品 P のトライアル付き申込が同時に 2 件 When 並行に CreateForSignup（本物の DB） Then trialing は 1 件、もう 1 件は ErrTrialNotEligible（trial_usages の主キー） |
| AC-SUB-012 | FR-014 | unit | — | Given トライアルなしで作成 When NewForSignup Then TrialUsage を作らない（トライアル利用時だけ記録）。TrialUsage なしならトライアル付きプランで trialing になる |
| AC-SUB-013 | FR-014, FR-016 | unit | E2E-003 | Given trial_end = 10-31 の trialing で解約予約 When 初回課金日 11-01 に BeginRenewal Then Ended、canceled（reason=customer）、Terms なし |
| AC-SUB-014 | FR-016 | unit | E2E-006 | Given active・期間 10-15〜11-14 When 10-20 に解約予約 Then entitled = true、effective_date = 11-15。11-15 の BeginRenewal で Ended |
| AC-SUB-015 | FR-016 | unit ＋ e2e | subscription/cancellation.clock.spec.ts | Given 解約予約中 When 取り消し → 11-15 に BeginRenewal Then Terms が返る。e2e（API テスト）：取り消し後の 11-15 の `renewal-dispatch` で請求 1 件 paid、active のまま |
| AC-SUB-016 | FR-016, D-03, P-ORC-1 | e2e | E2E-010 | Given past_due When PUT scheduled-cancellation Then 200・status canceled（reason=customer）、請求 void、webhook-sink に `subscription.canceled`、翌日の回収で決済されない |
| AC-SUB-017 | FR-015 | unit | E2E-005 | Given active 980 円プラン・更新日 11-15 When 10-20 に 1,980 円プランへ変更予約 Then 現在の plan は変わらず、11-15 の Terms の plan / price_version は新プラン、ConfirmRenewal で `plan_changed` が出る |
| AC-SUB-018 | FR-015 | unit | — | Given プラン変更予約中 When 解約予約 Then `plan_change_revoked` と `cancellation_scheduled`（reason = customer）が出て、プラン変更予約は消える |
| AC-SUB-019 | FR-015 | unit | — | Given 11-15 の BeginRenewal 済み（R あり）When 11-15 にプラン変更予約 Then 受け付け、effective_date = 12-15。R の Terms は変わらない |
| AC-SUB-020 | FR-015 | unit | — | Given 解約予約中 When プラン変更予約 Then ErrCancellationScheduled |
| AC-SUB-021 | FR-015, D-04 | unit | — | Given 月額（anchor 31）・更新日 2027-04-30 When 年額プランへの変更予約後に更新 Then 新しい期間は 2027-04-30〜2028-04-29 |
| AC-SUB-022 | FR-015 | e2e | subscription/plan-change.spec.ts | Given 別の商品のプラン・非公開のプラン・現在と同じプラン When PUT scheduled-plan-change Then それぞれ 422 plan_change_not_allowed（different_product / plan_unavailable / same_plan） |
| AC-SUB-023 | FR-005, FR-019 | unit | E2E-017 | Given 初回のみ（1 回）の割引付き trialing When 初回課金の BeginRenewal、その次の BeginRenewal Then 1 回目は Terms.discount あり、2 回目は nil。同じ日の BeginRenewal を 2 回呼んでも残り回数は 1 回分しか減らない |
| AC-SUB-024 | FR-021 | unit | E2E-007 | Given R ありの active When MarkPastDue(inv) Then past_due、entitled = true、`subscription.past_due`。同じ inv で再度呼んでも ○ |
| AC-SUB-025 | FR-021 | unit | E2E-007 | Given 11-15 の更新が past_due When 11-18 に Recover(inv) Then active、current_period = 11-15〜12-14、next_renewal_date = 12-15、`subscription.recovered` |
| AC-SUB-026 | FR-021, D-02 | unit | — | Given past_due When BeginRenewal（R と別の期間） Then ErrNotRenewable |
| AC-SUB-027 | FR-021 | unit | E2E-008 | Given past_due When Cancel(dunning) Then canceled、entitled = false、`subscription.canceled`（reason=dunning） |
| AC-SUB-028 | FR-026, D-01 | unit | E2E-012 | Given active と past_due の契約 When ScheduleSystemCancellation Then active は scheduled（source=system, effective_date=next_renewal_date）で `cancellation_scheduled` の reason = tenant_suspended（P-INT-6）、past_due は canceled_immediately（tenant_suspended） |
| AC-SUB-029 | D-01 | e2e | subscription/cancellation.spec.ts | Given 事業者停止でシステムが予約した解約 When 契約者が DELETE scheduled-cancellation Then 409 cancellation_not_revocable |
| AC-SUB-030 | FR-026 | unit | — | Given R ありの active に system 予約 When MarkPastDue Then canceled（tenant_suspended）。ConfirmRenewal なら active のまま新しい期間の終わりで解約予約が残る |
| AC-SUB-031 | FR-017 | unit | E2E-009 | Given past_due When 同じ支払方法で ChangePaymentMethod Then `payment_method_changed` が出る。active で同じ支払方法なら ○ |
| AC-SUB-032 | FR-008, NFR-008 | e2e | E2E-016 | Given 事業者 A の API キー When 事業者 B の契約 ID・cus_ で照会・entitlements Then 404 |
| AC-SUB-033 | FR-008 | e2e | E2E-007 | Given past_due の契約 When GET /v1/tenant/subscriptions/{id} と entitlements Then status = past_due、entitled = true |
| AC-SUB-034 | FR-009 | e2e | subscription/console-subscriptions.spec.ts | Given active / past_due / canceled の契約 When console で status=past_due Then past_due だけ |
| AC-SUB-035 | FR-018, NFR-008 | e2e | E2E-016（他人の契約の 404）＋ subscription/me-subscriptions.spec.ts（事業者をまたぐ一覧） | Given 契約者 X が事業者 A・B と契約 When GET /v1/me/subscriptions Then 両方が返る。別の契約者の契約は 404（E2E-016） |
| AC-SUB-036 | FR-019 | integration | — | Given 同じ契約への BeginRenewal と SchedulePlanChange When 本物の DB で並行に実行 Then 楽観ロックの衝突を再読込・再適用で解消し、両方が反映される（再試行の上限を超えたら ErrVersionConflict）。version は変更ごとに 1 ずつ増える |
| AC-SUB-037 | FR-023 | e2e | subscription/trial-will-end.clock.spec.ts | Given trial_end = 11-04 の trialing When clock=11-01 09:00 で `trial-will-end` ジョブを 2 回実行 Then webhook-sink に `subscription.trial_will_end` が 1 件 |
| AC-SUB-038 | D-05, P-SUB-3 | unit | E2E-018 | Given 各状態 When open の判定 Then pending_initial / trialing / active / past_due は true、canceled / signup_failed は false |
| AC-SUB-039 | FR-019, FR-016 | e2e | E2E-004 ＋ E2E-006 | Given 2027-01-31 開始の月額 When dev clock で 02-28・03-31 まで進めて日次処理を実行 Then 各日に更新される（E2E-004）。解約予約した契約は期間の終わりの翌日に canceled（E2E-006） |
| AC-SUB-040 | FR-015, FR-016, P-SUB-4 | e2e | subscription/renewal-day-reservation.clock.spec.ts | Given active・更新日 11-15、clock=11-15 00:10（日次処理の前） When PUT scheduled-cancellation と、別の契約で PUT scheduled-plan-change → `renewal-dispatch` Then 解約は `scheduled_cancellation.effective_date = 11-15` を返し、同日に canceled（請求なし）。プラン変更は `scheduled_plan_change.effective_date = 12-15` を返し、11-15 は元のプランで更新 |
| AC-SUB-041 | NFR-008 | integration | — | Given テナント A・B と契約者 X・Y の subscriptions・trial_usages（本物の DB） When `app.tenant_id` = A、または `app.customer_account_id` = X を設定して SELECT・UPDATE Then A／X の行だけが見え、他の行は更新できない。設定なしでは 0 行 |
| AC-SUB-042 | FR-018, NFR-008 | integration | — | Given 契約者 X の consumer_self ポリシー When X の契約を UPDATE（予約） Then 更新できる。別の契約者の契約は 0 行更新（API では 404） |

## 12. PdM への確認事項

すべて解決済み（2026-10-12、`02-pdm-decisions.md`）。未解決の項目はない。

| # | 論点 | 決定 | 反映先 |
|---|---|---|---|
| Q1 | 事業者 API で `cus_` から契約者を引く方法 | **解決済み（P-SUB-1）**。`CreateSignupCmd.TenantCustomerID` で受け取り、契約に持たせる。Subscription → Customer の依存は増やさない | §3.1, §4.2, §6, §7 |
| Q2 | console の契約詳細に出す請求の履歴 | **解決済み（P-SUB-2）**。Billing が `GET /v1/console/subscriptions/{id}/invoices` で返す | §7.2 |
| Q3 | 「open」に pending_initial を含めるか | **解決済み（P-SUB-3）**。含める（退会の判定・事業者停止の対象・支払方法の使用中の判定） | §2, §6, AC-SUB-038 |
| Q4 | 更新日当日・日次処理の前に届いた予約 | **解決済み（P-SUB-4）**。解約はその日の更新で解約（課金しない）、プラン変更は次の次の更新日。API は適用日を必ず返す | §3.1, §7 |
| Q5 | 解約の直後に進行中の決済が成功した場合 | **解決済み（P-SUB-5・P-BIL-3）**。解約済みのまま運用者に通知し、返金は運用 | §9 X-9 |
| Q6 | トライアル終了予告ジョブの担当 | **解決済み（P-SUB-6・P-ORC-3）**。Subscription のジョブ | §8 |
| — | 支払い遅延中の解約（D-03）の分担 | **解決済み（P-ORC-1）**。Subscription が解約し、Orchestration が `VoidOpenInvoices` を呼ぶ | §7.2 |

## 13. 変更履歴

| 日付 | 変更内容 | 変更者 |
|---|---|---|
| 2026-10-12 | 初版作成（Draft） | TL-Subscription（Claude） |
| 2026-10-12 | PdM レビューを反映（P-SUB-1〜6, P-ORC-1, P-INT-6, P-X-2）。`CreateSignupCmd.TenantCustomerID` を必須化、`cancellation_scheduled` に `reason` を追加、予約の適用日を API で必ず返すことを明記、監査（auditctx）の扱いと EventEnvelope の `actor` を追記、console の `X-Tenant-Id`・停止中の事業者の照会を追記、§12 を解決済みに更新。ステータスを Review に変更 | TL-Subscription（Claude） |
| 2026-10-12 | PdM の最終確認を経て Fixed とする | PdM |
| 2026-10-13 | テストの方針の変更（P-TST-1〜7）を反映。§11 のテストのレベルを unit（純粋なロジック）／integration（本物の DB で楽観ロック・RLS）／e2e（Playwright）で付け直し、E2E 列（orchestration.md の E2E-xxx、該当なしは「追加」）を追加。RLS の AC-SUB-041・042 を追加。ステータスを Review に変更 | TL-Subscription（Claude） |
| 2026-10-13 | P-TST-8 を反映。E2E 列に、シナリオ（`e2e/tests/scenarios/` の E2E-xxx）かドメインの API テスト（`e2e/tests/subscription/*.spec.ts`、ファイル名は案）かを明記し、「追加」の表記を spec ファイル名に置き換え | TL-Subscription（Claude） |
| 2026-10-13 | P-TST-9 を反映。時刻を進める API テストを `.clock.spec.ts` に分けた（015 → `cancellation.clock.spec.ts`、037 → `trial-will-end.clock.spec.ts`、040 → `renewal-day-reservation.clock.spec.ts`。029 は `cancellation.spec.ts` に残す）。凡例の `[clock]` の説明を規約に置き換え | TL-Subscription（Claude） |
| 2026-10-13 | テスト戦略の改訂（Playwright の E2E を中心にし、モックは原則使わない。P-TST-1〜10、P-X-8・9）について PdM の最終確認を経て、Fixed とする | PdM |
