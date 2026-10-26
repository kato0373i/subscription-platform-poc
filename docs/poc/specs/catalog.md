# 詳細要件: Catalog（modules/catalog）

| 項目 | 内容 |
|---|---|
| ステータス | **Fixed**（2026-10-13 PdM 承認。テスト戦略：Playwright の E2E が中心） |
| 担当 | TL-Catalog |
| 作成日 | 2026-10-12 |
| 上位文書 | `00-poc-charter.md`（D-08・D-09・D-10・C-03）、`01-contracts-baseline.md`（§5 改訂 v1.1 を含む）、`02-pdm-decisions.md`、設計書 §3・§4・§5、要件定義書 FR-003〜005 |

---

## 1. 概要と PoC の範囲

商品・プラン・価格バージョン・料金計算方式（PricingScheme）・クーポンを管理する上流コンテキスト。申込（Orchestration）に「申込できるプランと価格」を渡し、クーポンの予約・確定・解放を行う。割引額と税額の**確定計算は Billing**（C-03）、Catalog は**価格の算出（`QuotePrice`）**だけを持つ。

| 対象要件 | 内容 |
|---|---|
| FR-001（一部） | 事業者が active でないとプランを公開できない |
| FR-002（一部） | console API のロール判定（owner/admin は変更可、viewer は参照のみ） |
| FR-003 | 商品・プランの登録、PricingScheme（fixed） |
| FR-004 | 公開・非公開、価格改定（追記のみの PriceVersion） |
| FR-005 | クーポン（定額・定率、N 回、対象プラン、期限、総数上限、1 人 1 回、最後の 1 枚の競合） |
| FR-013（一部） | 申込ページ用の公開 API（プラン情報、クーポンの検証とプレビュー） |
| FR-025（一部） | console の変更操作で、変更前後の値を `auditctx` に注釈として書く（記録は Platform の監査ミドルウェア。P-X-2） |
| NFR-008 / NFR-010 | RLS によるテナント分離 / PricingScheme の差し替え口 |

| 作るもの | 作らないもの（PoC） |
|---|---|
| Product・Plan・PriceVersion・Coupon・CouponRedemption の集約と永続化 | 商品・プランの削除、アーカイブ |
| PricingScheme `fixed` と、スキームのレジストリ（差し替え口） | 従量課金・数量課金の実装（拡張口のみ §10） |
| contracts（`GetPurchasablePlan` ほか 6 メソッド） | 将来日付の価格改定（予約改定）。改定は即時のみ |
| console API・申込ページ用の公開 API | 税率 10% 以外（D-09）、複数通貨 |
| 放置された予約の掃除ジョブ | クーポンコードの総当たり対策（レート制限）、クーポンの再有効化 |
| `catalog.*` イベントの Outbox 発行 | 申込ページでの「トライアル対象外」表示（Subscription/Orchestration の責務） |

## 2. 用語

| 用語 | 意味 |
|---|---|
| 非公開（draft / unpublished） | 要件の「非公開」。作成直後は `draft`、公開後に取り下げたものは `unpublished`。どちらも新規申込不可 |
| 申込可能（purchasable） | プランが `published` かつ事業者が `active` |
| 価格バージョン（PriceVersion） | プランの価格の 1 版。追記のみ。最新版（`version_no` 最大）を新規申込に使う |
| PricingScheme | 価格の算出方式。PoC は `fixed`（固定額）だけ |
| 予約（reserved） | 申込の途中でクーポンの枠を 1 つ押さえた状態。上限の計算に含める |
| 確定（confirmed） | 申込が成立し、クーポンを使用済みにした状態 |
| 解放（released） | 申込が不成立、または放置で期限切れになり、枠を返した状態 |
| 消費枠数（redeemed_count） | `reserved` ＋ `confirmed` の件数。上限（max_redemptions）と比べる |

## 3. ドメインモデル

`internal/domain` に置く。生成関数と復元関数以外ではフィールドを変更できない（設計書 §1.2 の規約）。

### 3.1 Product（集約）

| フィールド | 型 | 制約 |
|---|---|---|
| id | `ID[Product]` | UUIDv7 |
| tenantID | `ID[Tenant]` | 必須 |
| name | string | 1〜100 文字 |
| description | string | 0〜1,000 文字 |
| version | int | 楽観ロック用 |

| メソッド | 処理 | イベント |
|---|---|---|
| `NewProduct(tenantID, name, desc)` | 作成 | なし（監査は auditctx。P-X-2） |
| `Rename(name, desc)` | 更新 | なし（同上） |

### 3.2 Plan（集約。PriceVersion を含む）

| フィールド | 型 | 制約 |
|---|---|---|
| id | `ID[Plan]` | |
| tenantID / productID | `ID[Tenant]` / `ID[Product]` | 商品は同じ事業者のもの |
| name / description | string | 1〜100 / 0〜1,000 文字 |
| interval | `sharedkernel.BillingInterval` | month / year。**作成後は変更不可** |
| trialDays | int | 0〜365。変更可（新規申込にだけ効く。契約は作成時に値を持つ） |
| scheme | `PricingSchemeKind` | `fixed`。作成後は変更不可 |
| status | `PlanStatus` | draft / published / unpublished |
| currentPrice | `PriceVersion` | 最新版。必ず 1 つ以上ある |
| publishedAt | *time.Time | 最後に公開した時刻 |
| version | int | |

**PriceVersion（エンティティ、不変）**

| フィールド | 型 | 制約 |
|---|---|---|
| id | `ID[PriceVersion]` | |
| versionNo | int | 1 から連番。プラン内で一意 |
| scheme | `PricingSchemeKind` | 作成時のプランの scheme を写す（`QuotePrice` が版だけで計算できるように） |
| amount | `Money` | fixed のとき必須。**1〜1,000,000 円（税込、D-08）** |
| schemeParams | `SchemeParams` | fixed は `{}`。将来のスキーム用 |
| taxRate | `sharedkernel.TaxRate` | PoC は 10/100 固定（API からは指定させない） |
| effectiveFrom | time.Time | 作成時刻（即時適用） |

**不変条件**
- PriceVersion は追記のみ。作成後に変更・削除しない（DB トリガーでも防ぐ）。
- 公開中のプランには有効な価格版がある（作成時に v1 を必須にするので常に成り立つ）。
- 公開（draft/unpublished → published）は、事業者が `active` のときだけ。

**状態遷移**

| 現在 \ 操作 | Publish | Unpublish |
|---|---|---|
| draft | → published（`catalog.plan_published`） | エラー `ErrInvalidPlanTransition` |
| published | 変化なし（冪等、イベントなし） | → unpublished（`catalog.plan_unpublished`） |
| unpublished | → published（`catalog.plan_published`） | 変化なし（冪等、イベントなし） |

| メソッド | 事前条件 | イベント |
|---|---|---|
| `NewPlan(tenantID, product, name, desc, interval, trialDays, scheme, amount, taxRate, now)` | 金額が範囲内 | `catalog.price_version_added`（v1） |
| `Publish(tenantStatus, now)` | tenantStatus = active。それ以外は `ErrTenantNotActive` | 上表 |
| `Unpublish(now)` | 上表 | 上表 |
| `AddPriceVersion(amount, taxRate, now)` | 金額が範囲内。現在の金額と同じなら `ErrPriceUnchanged` | `catalog.price_version_added` |
| `UpdateAttributes(name, desc, trialDays)` | | なし（監査は auditctx。P-X-2） |

- 事業者の状態は、アプリ層が `tenant/contracts.GetTenant` で取得し、値としてメソッドに渡す（依存は P-CAT-1 で承認済み）。ドメインは tenant を知らない。module.go は本物の tenant モジュールの contracts を注入する。テストでもフェイクに差し替えない（P-TST-2）。
- 事業者が停止されても、Catalog はプランの状態を変えない。申込可否は「プランが published かつ事業者が active」で判定する（§7.4、Orchestration も `GetTenant` で確認する）。

### 3.3 PricingScheme（値オブジェクト ＋ レジストリ）

```go
// internal/domain/pricing
type Kind string // "fixed"（exhaustive で列挙漏れを検出）

type Scheme interface {
    Kind() Kind
    ValidateParams(amount *sharedkernel.Money, params SchemeParams) error // 価格版の作成時
    Quote(pv PriceVersion, usage *Usage) (sharedkernel.Money, error)      // 価格の算出
}

type Registry map[Kind]Scheme // module.go で組み立てる
```

| スキーム | ValidateParams | Quote |
|---|---|---|
| fixed | amount が 1〜1,000,000、params は空 | `usage == nil` なら amount。nil でなければ `ErrUsageNotSupported` |

### 3.4 Coupon（集約）

| フィールド | 型 | 制約 |
|---|---|---|
| id / tenantID | `ID[Coupon]` / `ID[Tenant]` | |
| code | string | 大文字に正規化。`^[A-Z0-9_-]{3,32}$`。事業者内で一意 |
| name | string | 1〜100 文字（管理用） |
| discount | `sharedkernel.Discount{Type, Value, DurationCycles}`（P-CAT-5） | **作成後は変更不可** |
| ├ Type | amount / percent | |
| ├ Value | int64 | amount: 1〜1,000,000（円）、percent: 1〜100（%） |
| └ DurationCycles | int | 1〜24。初回のみは 1 |
| appliesTo | all_plans / selected_plans | selected のとき planIDs は 1 件以上、同じ事業者のプラン |
| planIDs | []`ID[Plan]` | |
| expiresAt | *time.Time | これ以降は予約不可（`now >= expiresAt` で期限切れ）。null は無期限 |
| maxRedemptions | *int | 1〜1,000,000。null は無制限 |
| redeemedCount | int | 消費枠数（reserved＋confirmed）。0 以上、上限以下 |
| status | active / inactive | inactive は戻せない |
| version | int | |

**不変条件**：`redeemedCount <= maxRedemptions`（DB の CHECK と条件付き UPDATE で守る）。

| メソッド | 処理 | イベント |
|---|---|---|
| `NewCoupon(...)` | 作成。code 重複は DB の一意制約で `ErrCouponCodeTaken` | なし（監査は auditctx。P-X-2） |
| `Deactivate(now)` | active → inactive（冪等）。既存の予約・確定には影響しない | なし（同上） |
| `CheckRedeemable(planID, now)` | 検証のみ（下の順で判定） | なし |

**検証の順序**（最初に当たったものを返す。プレビューと予約で共通）

| # | 条件 | エラー |
|---|---|---|
| 1 | 事業者内に code がない、または inactive | `ErrCouponNotFound` |
| 2 | `now >= expiresAt` | `ErrCouponExpired` |
| 3 | selected_plans で planID が対象外 | `ErrCouponNotApplicable` |
| 4 | 契約者がこのクーポンを reserved/confirmed で持っている（別の契約で） | `ErrCouponAlreadyUsed` |
| 5 | `redeemedCount >= maxRedemptions` | `ErrCouponExhausted` |

### 3.5 CouponRedemption（集約）

| フィールド | 型 | 制約 |
|---|---|---|
| id | `ID[CouponRedemption]` | contracts の redemption_id |
| tenantID / couponID / planID | | |
| customerAccountID / subscriptionID | `ID[CustomerAccount]` / `ID[Subscription]` | Orchestration が契約 ID を先に採番して渡す |
| status | reserved / confirmed / released | |
| releaseReason | caller / expired | released のときだけ |
| overLimit | bool | 期限切れ後の遅れた確定で枠を取れなかった（P-CAT-2） |
| reservedAt / reservationExpiresAt | time.Time | 期限 = reservedAt + TTL（既定 24 時間、設定可） |
| confirmedAt / releasedAt | *time.Time | |
| version | int | |

**状態遷移**

| 現在 \ 操作 | Confirm | Release（呼び出し元） | Expire（掃除ジョブ） |
|---|---|---|---|
| reserved | → confirmed | → released(caller)、枠 −1、`catalog.coupon_redemption_released` | 期限超過なら → released(expired)、枠 −1、同イベント |
| confirmed | 変化なし（冪等） | `ErrRedemptionAlreadyConfirmed` | 対象外 |
| released(caller) | `ErrRedemptionReleased` | 変化なし（冪等） | 対象外 |
| released(expired) | 枠を取り直して → confirmed。取れなければ overLimit=true で confirmed（P-CAT-2） | 変化なし（冪等） | 対象外 |

**Reserve（ドメインサービス `RedemptionService.Reserve`、1 トランザクション）**
1. 同じ (couponID, customerAccountID, subscriptionID) の reserved/confirmed があれば、それを返す（冪等。枠は増やさない）。
2. Coupon を読み、検証 #1〜#4 を行う。
3. 条件付き UPDATE で枠を取る（§9.1）。0 行なら再読込して #2（期限）か #5（上限）を返す。
4. CouponRedemption を INSERT。部分一意制約の違反は `ErrCouponAlreadyUsed`（トランザクションごとロールバックするので枠も戻る）。
5. Outbox に `catalog.coupon_redeemed`（設計書 §3 のとおり**予約時**に出す）。確定時はイベントを出さない。

**確定のタイミング（P-ORC-4）**：Orchestration が次の時点で Confirm を呼ぶ。Catalog はトライアルの有無を区別しない。

| 申込の種類 | Confirm が呼ばれる時点 | その後の解約 |
|---|---|---|
| トライアルなし | 初回決済の成功時 | 確定のまま（使用済み） |
| トライアルあり | トライアルの開始時（契約を trialing で作った直後） | トライアル中に解約しても確定のまま（使用済み）。Catalog に解放の要求は来ない |

## 4. 公開する契約（`modules/catalog/contracts`）

ベースラインの名前・責務を維持し、RLS のために `tenantID` 引数を追加する（ベースラインで許可された引数の追加）。

```go
package contracts

type Service interface {
    // 申込用。published でなければ ErrPlanUnavailable。事業者の状態は見ない（呼び出し元が GetTenant で確認）
    GetPurchasablePlan(ctx context.Context, tenantID TenantID, planID PlanID) (PlanSnapshot, error)
    // プラン変更先の検証・ID の検証用。状態に関係なく返す
    GetPlanSnapshot(ctx context.Context, tenantID TenantID, planID PlanID) (PlanDetail, error)
    // 契約者に見せるプランの説明（プラン名・商品名・事業者名）。状態に関係なく返す（consumer-mypage ADR-M01）
    DescribePlan(ctx context.Context, tenantID TenantID, planID PlanID) (PlanDescription, error)
    // 商品の公開中のプラン（作成順。マイページのプラン変更の候補）
    ListPurchasablePlans(ctx context.Context, tenantID TenantID, productID ProductID) ([]PurchasablePlan, error)
    // 価格の算出。PoC の usage は常に nil
    QuotePrice(ctx context.Context, tenantID TenantID, priceVersionID PriceVersionID, usage *UsageInput) (Quote, error)
    ReserveCoupon(ctx context.Context, tenantID TenantID, code string, planID PlanID,
        customerAccountID CustomerAccountID, subscriptionID SubscriptionID) (CouponReservation, error)
    ConfirmCouponRedemption(ctx context.Context, tenantID TenantID, redemptionID RedemptionID) error
    ReleaseCouponRedemption(ctx context.Context, tenantID TenantID, redemptionID RedemptionID) error
}
```

| DTO | 項目 |
|---|---|
| `PlanSnapshot` | PlanID, ProductID, PriceVersionID, Amount `Money`, TaxRate `TaxRate`, Interval `BillingInterval`, TrialDays int, PricingScheme string |
| `PlanDetail` | `PlanSnapshot` を埋め込み ＋ Status string（draft/published/unpublished）, Published bool |
| `Quote` | Amount `Money`, TaxRate `TaxRate`（将来 Lines を追加） |
| `UsageInput` | 空の struct（将来の項目は追加のみ。§10） |
| `CouponReservation` | RedemptionID, CouponID, Discount, DurationCycles（P-IMP-12） |
| `Discount` | `sharedkernel.Discount` をそのまま使う（P-CAT-5）。割引額は `Discount.Apply(amount)` で算出する（D-10 の式は sharedkernel の 1 か所だけ） |

- `TenantID`・`CustomerAccountID` は sharedkernel のもの（ベースライン §5.1）。`PlanID` などカタログ固有の ID は contracts で `sharedkernel.ID[T]` の別名として定義する。
- **Orchestration への要請**：申込の金額は `PlanSnapshot.Amount` ではなく `QuotePrice` の結果を使う（スキーム追加時に呼び出し側を変えないため。§10）。PoC では両者は一致する。

**エラー（contracts に sentinel として定義）**

| エラー | 返すメソッド | API の code / HTTP |
|---|---|---|
| `ErrPlanNotFound` | GetPurchasablePlan, GetPlanSnapshot, ReserveCoupon | `not_found` / 404 |
| `ErrPlanUnavailable` | GetPurchasablePlan | `plan_unavailable` / 410（公開 API）・422（申込） |
| `ErrPriceVersionNotFound` | QuotePrice | `not_found` / 404 |
| `ErrUsageNotSupported` | QuotePrice | `internal_error`（プログラムの誤り） |
| `ErrCouponNotFound` | ReserveCoupon | `coupon_not_found` / 422 |
| `ErrCouponExpired` | ReserveCoupon | `coupon_expired` / 422 |
| `ErrCouponNotApplicable` | ReserveCoupon | `coupon_not_applicable` / 422 |
| `ErrCouponAlreadyUsed` | ReserveCoupon | `coupon_already_used` / 422 |
| `ErrCouponExhausted` | ReserveCoupon | `coupon_exhausted` / 422 |
| `ErrRedemptionNotFound` | Confirm, Release | 呼び出し元のバグとして扱う |
| `ErrRedemptionAlreadyConfirmed` | Release | 同上（補償の順序誤り） |
| `ErrRedemptionReleased` | Confirm | 同上 |

- 別事業者の ID を渡された場合は、RLS により「見つからない」として `Err…NotFound` を返す。

## 5. 購読するイベントと処理

**PoC では購読しない。** 事業者の状態は `tenant/contracts.GetTenant` で同期に取得する（P-CAT-1）。クーポンの確定・解放は、申込の結果を知る Orchestration が同期で呼ぶ（Catalog は上流なので Subscription・Payments のイベントを購読しない）。

- `processed_messages` テーブルは共通ルールどおり作っておく（購読を足すときにスキーマ変更を不要にする）。

## 6. 永続化（スキーマ `catalog`）

```sql
CREATE TABLE catalog.products (
  id uuid PRIMARY KEY, tenant_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 1000),
  version int NOT NULL DEFAULT 1, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, id));

CREATE TABLE catalog.plans (
  id uuid PRIMARY KEY, tenant_id uuid NOT NULL, product_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 1000),
  billing_interval text NOT NULL CHECK (billing_interval IN ('month','year')),
  trial_days int NOT NULL CHECK (trial_days BETWEEN 0 AND 365),
  pricing_scheme text NOT NULL CHECK (pricing_scheme IN ('fixed')),  -- スキーム追加時に広げる
  status text NOT NULL CHECK (status IN ('draft','published','unpublished')),
  published_at timestamptz NULL,
  version int NOT NULL DEFAULT 1, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, product_id) REFERENCES catalog.products (tenant_id, id));

CREATE TABLE catalog.price_versions (
  id uuid PRIMARY KEY, tenant_id uuid NOT NULL, plan_id uuid NOT NULL,
  version_no int NOT NULL CHECK (version_no >= 1),
  pricing_scheme text NOT NULL CHECK (pricing_scheme IN ('fixed')),
  amount bigint NULL CHECK (amount BETWEEN 1 AND 1000000),              -- D-08
  scheme_params jsonb NOT NULL DEFAULT '{}'::jsonb,
  tax_rate numeric(4,3) NOT NULL CHECK (tax_rate >= 0 AND tax_rate < 1), -- PoC はアプリで 0.100 のみ
  effective_from timestamptz NOT NULL, created_at timestamptz NOT NULL,
  CHECK (pricing_scheme <> 'fixed' OR amount IS NOT NULL),
  UNIQUE (plan_id, version_no),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES catalog.plans (tenant_id, id));
-- UPDATE / DELETE を拒否するトリガー（BEFORE UPDATE OR DELETE → RAISE EXCEPTION）

CREATE TABLE catalog.coupons (
  id uuid PRIMARY KEY, tenant_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{3,32}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  discount_type text NOT NULL CHECK (discount_type IN ('amount','percent')),
  discount_value bigint NOT NULL,
  duration_cycles smallint NOT NULL CHECK (duration_cycles BETWEEN 1 AND 24),
  applies_to text NOT NULL CHECK (applies_to IN ('all_plans','selected_plans')),
  expires_at timestamptz NULL,
  max_redemptions int NULL CHECK (max_redemptions BETWEEN 1 AND 1000000),
  redeemed_count int NOT NULL DEFAULT 0 CHECK (redeemed_count >= 0),
  status text NOT NULL CHECK (status IN ('active','inactive')),
  version int NOT NULL DEFAULT 1, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
  CHECK ((discount_type = 'amount'  AND discount_value BETWEEN 1 AND 1000000)
      OR (discount_type = 'percent' AND discount_value BETWEEN 1 AND 100)),
  CHECK (max_redemptions IS NULL OR redeemed_count <= max_redemptions),
  UNIQUE (tenant_id, code), UNIQUE (tenant_id, id));

CREATE TABLE catalog.coupon_plans (
  tenant_id uuid NOT NULL, coupon_id uuid NOT NULL, plan_id uuid NOT NULL,
  PRIMARY KEY (coupon_id, plan_id),
  FOREIGN KEY (tenant_id, coupon_id) REFERENCES catalog.coupons (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_id)   REFERENCES catalog.plans (tenant_id, id));

CREATE TABLE catalog.coupon_redemptions (
  id uuid PRIMARY KEY, tenant_id uuid NOT NULL, coupon_id uuid NOT NULL, plan_id uuid NOT NULL,
  customer_account_id uuid NOT NULL, subscription_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('reserved','confirmed','released')),
  release_reason text NULL CHECK (release_reason IN ('caller','expired')),
  over_limit boolean NOT NULL DEFAULT false,
  reserved_at timestamptz NOT NULL, reservation_expires_at timestamptz NOT NULL,
  confirmed_at timestamptz NULL, released_at timestamptz NULL,
  version int NOT NULL DEFAULT 1,
  CHECK ((status = 'released') = (release_reason IS NOT NULL AND released_at IS NOT NULL)),
  CHECK ((status = 'confirmed') = (confirmed_at IS NOT NULL)),
  FOREIGN KEY (tenant_id, coupon_id) REFERENCES catalog.coupons (tenant_id, id));
```

| インデックス・制約 | 目的 |
|---|---|
| `UNIQUE (coupon_id, customer_account_id) WHERE status IN ('reserved','confirmed')` | 1 人 1 回。**解放済みは除外**し、決済失敗後の再申込で同じクーポンを使えるようにする（設計書の全件 UNIQUE から変更。P-CAT-4） |
| `UNIQUE (subscription_id) WHERE status IN ('reserved','confirmed')` | 1 契約にクーポンは 1 つ |
| `coupon_redemptions (reservation_expires_at) WHERE status = 'reserved'` | 掃除ジョブ |
| `plans (tenant_id, product_id, created_at, id)` / `plans (tenant_id, status, created_at, id)` | 一覧・絞り込み（キーセット） |
| `products (tenant_id, created_at, id)` / `coupons (tenant_id, created_at, id)` | 一覧 |
| `price_versions (plan_id, version_no DESC)` | 最新版の取得（UNIQUE で兼ねる） |

**RLS**
- 上の全テーブルに `ENABLE` と `FORCE ROW LEVEL SECURITY` をかける（`outbox_events` は Platform の定義に従い RLS をかけない。P-IMP-12）。
- ポリシー：`USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)`、`WITH CHECK` も同じ。未設定なら 0 件になる。
- テナントをまたぐ読み取りは、次の `SECURITY DEFINER` 関数だけに限定する（返すのは ID だけ。中身は改めて RLS 下で読む）。

| 関数 | 用途 | 返す値 |
|---|---|---|
| `catalog.resolve_checkout_plan_tenant(plan_id uuid)` | 未認証の公開 API で tenant_id を特定する | status が published/unpublished なら tenant_id、draft・存在しないなら NULL |
| `catalog.list_expired_reservations(now timestamptz, lim int)` | 掃除ジョブ | (tenant_id, redemption_id) の組 |

- 共通テーブル `catalog.outbox_events`・`catalog.processed_messages` は Platform の定義に従う。

## 7. API

共通：エラーは Problem Details。一覧は `limit`（既定 20、上限 100）・`starting_after`、レスポンスは `{data, has_more, next_cursor}`。金額は JPY の整数（税込）。税率は小数 3 桁の文字列（例 `"0.100"`。P-IMP-12）。console の POST は Idempotency-Key 不要（ベースラインの必須対象外）。

### 7.1 認可（console）

| 主体 | 参照（GET） | 変更（POST/PATCH） |
|---|---|---|
| staff: owner / admin | ✅ | ✅ |
| staff: viewer | ✅ | ❌ `forbidden` 403 |
| 他の主体 | ❌ `unauthenticated` 401 / `forbidden` 403 | ❌ |

- 操作するテナントはヘッダー `X-Tenant-Id` で指定する（P-IDN-1）。別事業者のリソース ID は 404（`not_found`）。
- 停止した事業者（suspended）は、GET はでき、変更系は拒否される（P-IDN-4。判定は Platform の認可で行い、Catalog は個別に実装しない）。

### 7.2 商品

| エンドポイント | リクエスト | レスポンス | エラー |
|---|---|---|---|
| `POST /v1/console/products` | name, description? | 201 Product{id, name, description, created_at, updated_at} | `validation_failed` |
| `GET /v1/console/products` | limit, starting_after | 200 一覧 | |
| `GET /v1/console/products/{id}` | | 200 Product | `not_found` |
| `PATCH /v1/console/products/{id}` | name?, description? | 200 Product | `validation_failed`, `not_found` |

### 7.3 プラン・価格

**Plan リソース**：id, product_id, name, description, billing_interval, trial_days, pricing_scheme, status, current_price{price_version_id, version_no, amount, tax_rate, effective_from}, published_at, created_at, updated_at

| エンドポイント | リクエスト | レスポンス | エラー |
|---|---|---|---|
| `POST /v1/console/plans` | product_id, name, description?, billing_interval, trial_days, amount（pricing_scheme は省略時 fixed） | 201 Plan（status=draft、v1 を作成） | `validation_failed`（金額範囲外は `errors[].code=out_of_range` と許容範囲 1〜1,000,000 を detail に示す）, `not_found`（商品） |
| `GET /v1/console/plans` | product_id?, status?, limit, starting_after | 200 一覧 | |
| `GET /v1/console/plans/{id}` | | 200 Plan | `not_found` |
| `PATCH /v1/console/plans/{id}` | name?, description?, trial_days?（interval・scheme・amount は不可→`validation_failed`） | 200 Plan | `validation_failed`, `not_found` |
| `POST /v1/console/plans/{id}/price-versions` | amount | 201 PriceVersion{id, version_no, amount, tax_rate, effective_from} | `validation_failed`, `price_unchanged` 422, `not_found` |
| `GET /v1/console/plans/{id}/price-versions` | limit, starting_after（version_no 降順） | 200 一覧 | `not_found` |
| `POST /v1/console/plans/{id}/publish` | なし | 200 Plan | `tenant_not_active` 403（detail「審査完了後に公開できます」。P-IMP-12）, `not_found` |
| `POST /v1/console/plans/{id}/unpublish` | なし | 200 Plan | `conflict` 409（draft のとき）, `not_found` |

- publish / unpublish / price-versions は、すでに目的の状態なら 200 で現状を返す（冪等）。価格改定の二重送信は `price_unchanged` で弾かれる。
- 同じプランへの同時変更は、トランザクション内で `SELECT … FOR UPDATE` により直列化する。

### 7.4 クーポン（console）

**Coupon リソース**：id, code, name, discount{type, value, duration_cycles}, applies_to, plan_ids, expires_at, max_redemptions, redeemed_count, status, created_at, updated_at

| エンドポイント | リクエスト | レスポンス | エラー |
|---|---|---|---|
| `POST /v1/console/coupons` | code, name, discount{type, value, duration_cycles}, applies_to, plan_ids?, expires_at?（RFC 3339）, max_redemptions? | 201 Coupon | `validation_failed`（過去の expires_at を含む）, `coupon_code_taken` 409, `not_found`（plan_ids） |
| `GET /v1/console/coupons` | status?, limit, starting_after | 200 一覧 | |
| `GET /v1/console/coupons/{id}` | | 200 Coupon | `not_found` |
| `PATCH /v1/console/coupons/{id}` | name?, status?（`inactive` のみ可） | 200 Coupon | `validation_failed`（active への変更・割引条件の変更）, `not_found` |

### 7.5 申込ページ用の公開 API

| 項目 | `GET /v1/checkout/plans/{planId}` |
|---|---|
| 認証 | なし（公開）。`Cache-Control: public, max-age=60` |
| 処理 | `resolve_checkout_plan_tenant` → RLS 下でプランを読む → `tenant/contracts.GetTenant` で事業者の状態を見る |
| 200 | plan_id, product{id, name}, name, description, billing_interval, trial_days, amount（税込）, tax_rate, price_version_id |
| エラー | `not_found` 404（存在しない・draft）、`plan_unavailable` 410（unpublished、または事業者が active でない。表示文言「現在お申し込みいただけません」） |

| 項目 | `GET /v1/checkout/plans/{planId}/coupons/{code}` |
|---|---|
| 認証 | 任意。consumer の JWT があれば検証 #4（使用済み）も行う。キャッシュ不可 |
| 処理 | プランの検証（上と同じ）→ `CheckRedeemable`（予約はしない） |
| 200 | code, discount{type, value, duration_cycles}, preview{list_amount, discount_amount, first_amount, discounted_cycles, applies_from: `first_charge`}。discount_amount は `sharedkernel.Discount.Apply`（D-10、P-CAT-5）で算出する。Billing と同じ式だが、表示用の**参考値**とする。確定額は Billing が決める |
| エラー | 422：`coupon_not_found`, `coupon_expired`, `coupon_not_applicable`, `coupon_already_used`, `coupon_exhausted`。プランのエラーは上と同じ |

- トライアル付きプランでは、割引は初回の課金（トライアル終了時）から N 回に効く（`applies_from = first_charge`）。回数の消化は Subscription の AppliedDiscount が持つ。クーポン自体はトライアルの開始時に使用済みになる（P-ORC-4）。

**追加するエラーコード**（Platform の一覧に登録する）：`coupon_not_found`、`coupon_not_applicable`、`coupon_code_taken`、`price_unchanged`。既存：`plan_unavailable`、`tenant_not_active`、`coupon_expired`、`coupon_exhausted`、`coupon_already_used`。

## 8. ジョブ・非同期処理

| ジョブ | `catalog-coupon-reservation-sweep` |
|---|---|
| 起動 | 1 時間ごと（Platform のスケジュール起点）。ローカルは `POST /v1/dev/jobs/catalog-coupon-reservation-sweep/run` |
| 処理 | `list_expired_reservations(Clock.Now(), 500)` → 1 件ごとに、その事業者の RLS 下のトランザクションで `FOR UPDATE SKIP LOCKED` → まだ reserved かつ期限超過なら released(expired)・`redeemed_count - 1`・Outbox に `catalog.coupon_redemption_released`（reason=expired） |
| 冪等性・並行 | 状態を再確認してから更新するので、重複起動・再実行しても二重に枠を戻さない |
| 設定 | `CATALOG_COUPON_RESERVATION_TTL`（既定 24h）。決済結果の照合（FR-024）で結果が確定するまでの時間より十分長くする |
| ログ | 解放件数を構造化ログに出す（1 件以上なら warn。Orchestration の確定・解放漏れの兆候） |

**発行イベント一覧**（Outbox、`source = sp.catalog`）

| event_type | aggregate | payload |
|---|---|---|
| `catalog.plan_published` | plan | plan_id, product_id, price_version_id |
| `catalog.plan_unpublished` | plan | plan_id, product_id |
| `catalog.price_version_added` | plan | plan_id, price_version_id, version_no, amount, tax_rate |
| `catalog.coupon_redeemed` | coupon_redemption | coupon_id, redemption_id, customer_account_id, subscription_id, plan_id |
| `catalog.coupon_redemption_released` | coupon_redemption | coupon_id, redemption_id, customer_account_id, subscription_id, reason（caller / expired） |

- 操作者は EventEnvelope の任意項目 `actor`（console 操作は staff、掃除ジョブは system）に入れる。payload には入れない（ベースライン §5.1、P-CAT-3）。
- 監査のためだけのイベント（作成・変更イベント）は作らない（P-X-2）。

**監査の注釈（auditctx）**：console の変更系ユースケースは、`internal/platform/auditctx` に次を書く。記録は Platform の監査ミドルウェアが行う。

| 操作 | 対象 | 変更前後の値 |
|---|---|---|
| 商品の作成・更新 | product_id | name, description |
| プランの作成・更新 | plan_id | name, description, trial_days（作成時は amount も） |
| 価格の改定 | plan_id | 改定前と改定後の price_version_id・amount |
| 公開・非公開 | plan_id | status |
| クーポンの作成・無効化 | coupon_id | 割引条件・status |

## 9. 異常系・境界値・競合

### 9.1 最後の 1 枚の取り合い

```sql
UPDATE catalog.coupons
   SET redeemed_count = redeemed_count + 1, version = version + 1, updated_at = $now
 WHERE id = $id AND status = 'active'
   AND (expires_at IS NULL OR expires_at > $now)
   AND (max_redemptions IS NULL OR redeemed_count < max_redemptions)
RETURNING redeemed_count;
```

- 同じ行への UPDATE は PostgreSQL の行ロックで直列化され、後の側は先の側のコミット後の値で WHERE を再評価する（READ COMMITTED）。残り 1 枚なら後の側は 0 行になり `ErrCouponExhausted`。
- CHECK `redeemed_count <= max_redemptions` が最後の砦。
- 解放は `redeemed_count = redeemed_count - 1`（状態を reserved から変えた行についてだけ）。

### 9.2 境界値・異常系

| ケース | 扱い |
|---|---|
| 価格 0 円 / 1 円 / 1,000,000 円 / 1,000,001 円 | 拒否 / 可 / 可 / 拒否（D-08） |
| 定率 100% | 可。割引後 0 円（Billing が 0 円請求を paid にする） |
| 定額割引 > 価格 | 可。割引後は 0 円（0 円未満にしない、D-10） |
| 定率の端数（980 円 × 33%） | プレビューは floor(323.4) = 323 円引き |
| expires_at ちょうどの時刻 | 期限切れ（`now >= expires_at`） |
| 予約後に期限が過ぎた・無効化された | 確定できる（検証は予約時だけ） |
| 予約後にプランが非公開になった | 確定できる（申込開始時点で判定済み） |
| トライアル付きの申込で確定した後、トライアル中に解約 | 確定のまま。枠も 1 人 1 回の判定も使用済みとして扱う（P-ORC-4） |
| 最大数 = 現在の消費枠数 で予約 | `ErrCouponExhausted`。予約中の枠も数える |
| 決済失敗で解放された後の再申込 | 同じクーポンを再び予約できる（部分一意、P-CAT-4） |
| 別の申込でクーポンを予約中に、同じ契約者が別の申込で使う | `ErrCouponAlreadyUsed` |
| Orchestration の Reserve の再試行（同じ subscription_id） | 既存の予約を返す（枠は増えない） |
| 同じ契約者が 2 つの申込で同時に予約 | 部分一意制約で一方が `ErrCouponAlreadyUsed`。枠はロールバックで戻る |
| 価格改定と申込が同時 | 申込は読み取った時点の最新版で固定される。どちらの版でも整合する |
| 同じプランへの価格改定が同時 | プラン行の `FOR UPDATE` で直列化。後の側は版番号 +1、同額なら `price_unchanged` |
| 事業者が審査中で publish | `tenant_not_active` |
| tenant/contracts が失敗 | publish・公開 API は `internal_error` 500（公開側は安全側に倒す） |
| price_versions を UPDATE/DELETE | トリガーで例外 |
| app.tenant_id 未設定で読み取り | 0 件（RLS）。アプリは `ErrTenantContextMissing` として 500 |

## 10. 拡張ポイント

### 10.1 従量課金・数量課金を足すとき（NFR-010）

| # | 追加・変更する場所 | 内容 | 既存コードの変更 |
|---|---|---|---|
| 1 | `catalog/internal/domain/pricing/<kind>.go` | `Scheme` の実装（例 `perUnit`：単価 × 数量、`tiered`：段階表） | なし（新規ファイル） |
| 2 | `catalog/module.go` | `Registry` に登録 | 1 行追加 |
| 3 | `catalog/db/migrations` | `plans`・`price_versions` の `pricing_scheme` の CHECK を広げる。`scheme_params` に単価・段階表を入れる（amount は NULL 可） | マイグレーション追加 |
| 4 | `catalog/contracts` | `UsageInput` に項目を追加（例 `Quantity *int64`、`Records []UsageRecord`）。`Quote` に `Lines []QuoteLine` を追加 | 追加のみ（後方互換） |
| 5 | `api/openapi` | price-versions の作成リクエストを scheme ごとの `oneOf` にする | 追加 |
| 6 | 他ドメイン（参考） | Usage コンテキストが使用量を集計し、Orchestration が `QuotePrice(…, usage)` に渡す。Billing は `invoice_lines.kind` に `usage` を足す | Subscription・Billing の中核ロジックは変えない |

- 前提：呼び出し側が金額を `QuotePrice` から得ていること（§4 の要請）。`PlanSnapshot.Amount` は fixed のときの表示用で、スキーム追加後は NULL 相当（0）になり得る。

### 10.2 その他

| 拡張 | PoC での扱い |
|---|---|
| 将来日付の価格改定 | `effective_from` を保持済み。最新版の選び方を「effective_from <= now の最大」に変えるだけで対応できる |
| 税率の追加（軽減税率など） | 価格版ごとに tax_rate を持つので、API で受け付けるだけでよい |
| 複数クーポンの併用 | 非対応。`UNIQUE (subscription_id)` を外し、Discount の配列にする |
| 2 回目以降の申込にも使えるクーポン、1 人 N 回 | 非対応。部分一意制約を回数のカウントに置き換える |
| クーポンコードの総当たり対策 | 非対応。公開 API にレート制限ミドルウェア（Platform）を後から挟む |
| 確定時のイベント（`catalog.coupon_redemption_confirmed`） | 非対応。Reporting を作るときに追加する |

## 11. 受け入れ条件

**テストのレベル**（P-TST-1〜7。モック・フェイクは使わない）

| レベル | 書き方 | Catalog での対象 |
|---|---|---|
| unit | Go のテーブル駆動テスト。DB も外部も使わない | 価格・クーポン条件の検証、`Discount.Apply` を使ったプレビューの計算、`fixed` の Quote、Plan と CouponRedemption の状態遷移 |
| integration | Go のテスト。testcontainers-go の本物の PostgreSQL に、本物の catalog・tenant モジュールを組み立てて使う | 最後の 1 枚の取り合い、部分一意の並行、RLS、価格版の不変トリガー、API から作りにくい予約の状態 |
| e2e | Playwright（`e2e/`）。compose で起動した api・worker と MiniStack に対して、APIRequestContext で console API・公開 API・申込 API を呼ぶ | console API、申込ページ用の公開 API、申込を通したクーポンの予約・確定・解放 |

- e2e の各テストは、ops・dev の API で自分専用のテナント・スタッフ・契約者を作る（fixture）。API から見えない不変条件（枠の数、Outbox、監査の記録）は、読み取り専用ロール `sp_e2e_reader` で DB を確かめる（P-TST-3）。
- dev clock を進めるテストは、Playwright の `clock` project（workers=1）で流す（下表で「e2e（clock）」）。それ以外は `parallel` project で流す（P-TST-4）。
- 決済の結果は StubAdapter のシナリオで切り替える（P-TST-2 の例外）。
- e2e の確かめる場所は 2 種類ある（P-TST-8）。課金の流れを通すシナリオで確かめるものは番号（`e2e/tests/scenarios/` の E2E-001〜022）を書き、Catalog 単体の確認は `e2e/tests/catalog/` の spec ファイル名の案を書く（下表の「確かめる場所」）。
- dev clock を進める spec はファイル名を `*.clock.spec.ts` にし、`clock` project がこのパターンで拾う（案。命名は TL-Platform の e2e 基盤に合わせる）。

| spec ファイル（案） | 内容 | project |
|---|---|---|
| `catalog/plans.spec.ts` | 商品・プランの作成、価格改定、publish | parallel |
| `catalog/checkout-plans.spec.ts` | `GET /v1/checkout/plans/{id}` の可否 | parallel |
| `catalog/authorization.spec.ts` | viewer の権限 | parallel |
| `catalog/tenant-isolation.spec.ts` | console API でのテナント分離 | parallel |
| `catalog/coupons.spec.ts` | クーポンの作成・無効化 | parallel |
| `catalog/coupon-preview.spec.ts` | クーポンの検証とプレビュー | parallel |
| `catalog/coupon-redemption.spec.ts` | 申込を通した予約・確定・解放、最後の 1 枚 | parallel |
| `catalog/audit.spec.ts` | 価格改定の監査記録と actor | parallel |
| `catalog/coupon-validation.clock.spec.ts` | 期限切れなどのクーポンエラー | clock |
| `catalog/coupon-reservation-sweep.clock.spec.ts` | 放置された予約の掃除ジョブ | clock |

| ID | 要件 | レベル | Given / When / Then | 確かめる場所 |
|---|---|---|---|---|
| AC-CAT-001 | FR-003 | e2e | Given active な事業者の admin / When `POST /v1/console/plans` で 980 円・month・trial 14 日のプランを作成 / Then 201、status=draft、current_price は v1（980 円・税率 "0.100"）。DB の Outbox に `catalog.price_version_added` がある | `catalog/plans.spec.ts` |
| AC-CAT-002 | FR-003, D-08 | unit | Given プランの生成関数・価格版の追加 / When 金額が 0・−1・1,000,001 / Then 範囲外のエラー。1 と 1,000,000 は成功する | —（Go のテスト） |
| AC-CAT-003 | FR-003 | e2e | Given 別の事業者の商品 ID / When その商品でプランを作成 / Then 404 `not_found` | `catalog/plans.spec.ts` |
| AC-CAT-004 | FR-001 | e2e | Given 事業者が pending_review / When publish / Then 403 `tenant_not_active`（P-IMP-12）、GET で status は draft のまま | `catalog/plans.spec.ts` |
| AC-CAT-005 | FR-004 | unit | Given 各状態のプラン / When Publish・Unpublish / Then §3.2 の状態遷移表どおり（冪等な操作はイベントを記録しない、draft の unpublish はエラー） | —（Go のテスト） |
| AC-CAT-006 | FR-004 | e2e | Given 980 円（v1）の公開プラン / When 1,280 円に改定 / Then 201 で v2。`GET /v1/checkout/plans/{id}` は 1,280 円・v2 の price_version_id を返し、price-versions の一覧に v1（980 円）が残る | `catalog/plans.spec.ts` |
| AC-CAT-007 | FR-004 | e2e | Given 現在 980 円 / When 980 円で改定 / Then 422 `price_unchanged`、版は増えない | `catalog/plans.spec.ts` |
| AC-CAT-008 | FR-004 | integration | Given price_versions の行 / When UPDATE または DELETE を直接実行 / Then DB が例外を返す | —（Go のテスト） |
| AC-CAT-009 | FR-004 | e2e | Given 公開後に unpublish したプラン / When `GET /v1/checkout/plans/{id}` と、そのプランへの申込 / Then 410 `plan_unavailable` と 422 `plan_unavailable` | `catalog/checkout-plans.spec.ts` |
| AC-CAT-010 | FR-004, FR-026 | e2e | Given 公開プランの事業者を ops API で停止した / When `GET /v1/checkout/plans/{id}` / Then 410 `plan_unavailable` | `catalog/checkout-plans.spec.ts` |
| AC-CAT-011 | FR-004 | e2e | Given draft のプラン / When `GET /v1/checkout/plans/{id}` / Then 404 | `catalog/checkout-plans.spec.ts` |
| AC-CAT-012 | FR-002 | e2e | Given viewer のスタッフ / When 価格改定・publish・クーポン作成 / Then 403。GET は 200 | `catalog/authorization.spec.ts` |
| AC-CAT-013 | NFR-008 | integration | Given 事業者 A・B のデータ / When A の文脈で B の plan・coupon・redemption を contracts で取得・変更する、各テーブルを直接 SELECT する / Then すべて見つからない。app.tenant_id が未設定なら 0 件 | —（Go のテスト） |
| AC-CAT-014 | NFR-010 | unit | Given fixed の価格版 / When Quote（usage=nil） / Then amount を返す。usage≠nil なら `ErrUsageNotSupported` | —（Go のテスト） |
| AC-CAT-015 | FR-005 | unit | Given クーポンの生成関数 / When percent 0・101、amount 0、duration 0・25、selected_plans で plan_ids が空、過去の expires_at / Then それぞれ検証エラー | —（Go のテスト） |
| AC-CAT-016 | FR-005 | e2e | Given 同じ事業者に code "WELCOME" がある / When "welcome" で作成 / Then 409 `coupon_code_taken`（大文字に正規化）。別の事業者なら 201 | `catalog/coupons.spec.ts` |
| AC-CAT-017 | FR-005, D-10 | e2e | Given 980 円のプランと「初回 50%」クーポン / When プレビュー API / Then discount_amount=490、first_amount=490、discounted_cycles=1 | `catalog/coupon-preview.spec.ts` |
| AC-CAT-018 | FR-005, D-10 | unit | Given 980 円 / When 33% と、定額 1,500 円のプレビューを計算 / Then 323 円引き（floor）と、割引後 0 円 | —（Go のテスト） |
| AC-CAT-019 | FR-005 | e2e（clock） | Given 期限切れ（dev clock を進める）／上限到達（上限 1 で別の契約者が申込済み）／対象外プラン／無効化済みのクーポン / When プレビュー API と申込 API / Then それぞれ 422 `coupon_expired`／`coupon_exhausted`／`coupon_not_applicable`／`coupon_not_found`。DB の redeemed_count は変わらない | `catalog/coupon-validation.clock.spec.ts` |
| AC-CAT-020 | FR-005 | e2e | Given 契約者 X がクーポン C で申込済み（confirmed） / When X が別の申込で C を使う、ログイン状態でプレビュー / Then どちらも 422 `coupon_already_used` | E2E-017（申込で再利用）＋ `catalog/coupon-preview.spec.ts`（ログイン状態のプレビュー） |
| AC-CAT-021 | FR-005 | integration | Given 残り 1 枚のクーポン C / When 2 人の契約者が goroutine で同時に `ReserveCoupon` / Then 1 人だけ成功し、もう 1 人は `ErrCouponExhausted`、redeemed_count = max_redemptions（100 回繰り返して常に成り立つ） | —（Go のテスト） |
| AC-CAT-022 | FR-005 | integration | Given 契約者 X が 2 つの申込で同時に C を予約 / When 並行実行 / Then 1 件だけ reserved、他方は `ErrCouponAlreadyUsed`、redeemed_count は +1 だけ | —（Go のテスト） |
| AC-CAT-023 | FR-005 | integration | Given 同じ (coupon, customer, subscription) で予約済み / When `ReserveCoupon` を再実行 / Then 同じ redemption_id を返し、redeemed_count は変わらない | —（Go のテスト） |
| AC-CAT-024 | FR-005, FR-013 | e2e | Given クーポン C（上限 10） / When 契約者が C 付きで申し込み、StubAdapter が決済を拒否する / Then 申込は 422 `payment_declined`。DB で redemption は released(caller)、redeemed_count は 0、Outbox に `catalog.coupon_redemption_released` | `catalog/coupon-redemption.spec.ts` |
| AC-CAT-025 | FR-005 | e2e | Given AC-CAT-024 のあと / When 同じ契約者が C 付きで、成功するカードで再申込 / Then 201 で契約が成立し、割引が適用される | `catalog/coupon-redemption.spec.ts` |
| AC-CAT-026 | FR-005 | unit | Given 各状態の redemption / When Confirm・Release・Expire / Then §3.5 の状態遷移表どおり（confirmed の Release、released(caller) の Confirm はエラー） | —（Go のテスト） |
| AC-CAT-027 | FR-005 | e2e（clock） | Given StubAdapter で結果不明になった C 付きの申込（reserved のまま） / When dev clock を 24 時間 1 分進め、`POST /v1/dev/jobs/catalog-coupon-reservation-sweep/run` を 2 回実行 / Then DB で released(expired)、redeemed_count は 1 だけ減り、解放イベントは 1 件 | `catalog/coupon-reservation-sweep.clock.spec.ts` |
| AC-CAT-028 | FR-005 | integration | Given released(expired) で、枠が空いている／埋まっている / When Confirm / Then confirmed（枠 +1）／confirmed・over_limit=true（枠は上限のまま）（P-CAT-2） | —（Go のテスト） |
| AC-CAT-029 | FR-005 | integration | Given 予約済みのクーポンを無効化し、期限も過ぎた / When Confirm / Then confirmed になる | —（Go のテスト） |
| AC-CAT-030 | FR-005, FR-013 | e2e | Given 980 円・トライアルなしのプランと「初回 50%」クーポン / When 契約者がクーポン付きで申し込み、決済が成功 / Then 初回請求 490 円。DB で redemption は confirmed（Orchestration の E2E シナリオと共用してよい） | E2E-017 |
| AC-CAT-031 | FR-005, FR-013 | e2e | Given 残り 1 枚のクーポン / When 2 人が申込 API をほぼ同時に呼ぶ / Then 1 人だけ割引付きで成立し、もう 1 人は 422 `coupon_exhausted`（競合の網羅は AC-CAT-021 で保証する） | `catalog/coupon-redemption.spec.ts` |
| AC-CAT-032 | FR-025 | e2e | Given admin が 980 円のプランを 1,280 円に改定する / When console API を呼ぶ / Then DB の監査記録に plan_id・改定前後の amount（980→1,280）・操作者が残り、Outbox の `catalog.price_version_added` の EventEnvelope の actor が {staff, 操作者} | `catalog/audit.spec.ts` |
| AC-CAT-033 | FR-005, FR-014 | e2e | Given トライアル付きプランに C 付きで申し込んだ（トライアル開始時に確定） / When 契約者がトライアル中に解約し、同じ C で別の申込をする / Then 2 回目は 422 `coupon_already_used`。DB で redemption は confirmed のまま、redeemed_count は 1（P-ORC-4） | E2E-003 |
| AC-CAT-034 | NFR-008 | e2e | Given 事業者 A・B のスタッフ / When A のスタッフが B の plan・product・coupon を GET・PATCH・publish する（`X-Tenant-Id` に A） / Then すべて 404 `not_found` | `catalog/tenant-isolation.spec.ts` |

## 12. PdM への確認事項

すべて解決済み。未解決の確認事項はない。

| # | 論点 | 状態 | 決定と本書への反映 |
|---|---|---|---|
| Q-01 | Catalog → tenant/contracts の依存 | **解決済み（P-CAT-1）** | 案 A（`GetTenant` を同期で呼ぶ）。§3.2・§5・§7.5 に反映し、事業者の状態の写し（案 B）の記述は削除した |
| Q-02 | 放置された予約の扱い | **解決済み（P-CAT-2）** | 24 時間で自動解放する。遅れて届いた確定は枠を取り直し、取れなければ over_limit=true で確定する。§3.5・§8・AC-CAT-027/028 |
| Q-03 | 監査用イベントの追加と操作者 | **解決済み（P-CAT-3・P-X-2）** | EventEnvelope の `actor` は採用する。作成・変更イベントは作らない。監査は HTTP ミドルウェアと `auditctx` の注釈で記録する。§3・§8・AC-CAT-032 |
| Q-04 | 「1 人 1 回」の一意制約 | **解決済み（P-CAT-4）** | 部分一意（reserved / confirmed）。§6・§9・AC-CAT-025 |
| Q-05 | 割引額の計算式の置き場所 | **解決済み（P-CAT-5）** | `sharedkernel.Discount.Apply` を共用する。§3.4・§4・§7.5 |
| — | トライアル付きの申込でクーポンを確定する時点 | **解決済み（P-ORC-4）** | トライアルの開始時に確定し、トライアル中に解約しても使用済みのまま。§3.5・§7.5・§9・AC-CAT-033 |

## 13. 変更履歴

| 日付 | 変更内容 | 変更者 |
|---|---|---|
| 2026-10-12 | 初版作成（Draft） | TL-Catalog（Claude） |
| 2026-10-12 | PdM レビューを反映（02-pdm-decisions.md、ベースライン §5 v1.1）。P-CAT-1：事業者の状態の写しの代替案を削除。P-CAT-3・P-X-2：作成・変更イベントと payload の actor を削除し、EventEnvelope の actor と auditctx の注釈に置き換えた（AC-CAT-032 を変更）。P-CAT-5：Discount を sharedkernel のものに変更。P-ORC-4：トライアル開始時の確定を追記し、AC-CAT-033 を追加。P-IDN-1・P-IDN-4：console の X-Tenant-Id と停止中の事業者の扱いを追記。§12 をすべて解決済みにした。ステータスを Review に変更 | TL-Catalog（Claude） |
| 2026-10-12 | PdM の最終確認を経て Fixed とする | PdM |
| 2026-10-13 | テスト方針の変更（P-TST-1〜7）を反映。§11 にテストのレベルの定義を追加し、AC-CAT のレベルを unit / integration（本物の DB）/ e2e（Playwright）に付け直した。console API・公開 API の条件を e2e に移し、API と DB で確かめる形に書き直した（AC-CAT-001・006・009・019・024・025・027・032・033）。API でのテナント分離を AC-CAT-034 として追加。`TenantStatusReader` ポートをやめ、本物の tenant モジュールを組み立てる形にした。ステータスを Review に変更 | TL-Catalog（Claude） |
| 2026-10-13 | P-TST-8 を反映。§11 の受け入れ条件に「確かめる場所」の列を追加し、e2e の条件ごとにシナリオの番号（E2E-003・017）か `e2e/tests/catalog/` の spec ファイル名の案を書いた。spec ファイルの一覧を追加 | TL-Catalog（Claude） |
| 2026-10-13 | テスト戦略の改訂（Playwright の E2E を中心にし、モックは原則使わない。P-TST-1〜10、P-X-8・9）について PdM の最終確認を経て、Fixed とする | PdM |
| 2026-10-24 | 実装時の決定を反映（P-IMP-12・13）：税率の表現を `"0.100"` に、publish の `tenant_not_active` を 403 に、`CouponReservation` に DurationCycles を追加、`outbox_events` の RLS の記述を Platform に合わせた。実装の状況は `docs/poc/impl/catalog.md` | PdM |
| 2026-10-26 | contracts に `DescribePlan`・`ListPurchasablePlans` を追加（consumer-mypage、P-IMP-24） | PdM |
