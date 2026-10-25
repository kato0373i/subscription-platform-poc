# 実装状況: Catalog

| 項目 | 内容 |
|---|---|
| 対象 | `docs/poc/specs/catalog.md`（Fixed） |
| 更新日 | 2026-10-24 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-IMP-12・13 |
| コード | `modules/catalog`、`api/openapi/console/catalog.yaml`、`api/openapi/checkout/catalog.yaml` |

## 1. 作ったもの

| 層 | 内容 |
|---|---|
| contracts | `Service`（`GetPurchasablePlan`・`GetPlanSnapshot`・`QuotePrice`・`ReserveCoupon`・`ConfirmCouponRedemption`・`ReleaseCouponRedemption`）、ID の型、5 種類のイベントの payload |
| domain | Product、Plan（状態遷移・価格改定）、PricingScheme（`fixed` とレジストリ。`internal/domain/pricing`）、Coupon（検証の順序・プレビュー）、CouponRedemption（予約・確定・解放・期限切れ） |
| DB | `catalog` スキーマ、全テーブルの RLS、価格版の追記のみのトリガー、部分一意（1 人 1 回・1 契約 1 つ）、SECURITY DEFINER 関数 2 つ（申込ページの事業者の特定・掃除ジョブ） |
| API | console（商品・プラン・価格版・公開・クーポン、16 操作）、申込ページの公開 API（プラン・クーポンのプレビュー） |
| ジョブ | `catalog-coupon-reservation-sweep`（1 時間ごと、worker-billing。TTL は `CATALOG_COUPON_RESERVATION_TTL`、既定 24h） |
| 配線 | tenant の contracts（`GetTenant`）を注入（P-CAT-1）、マイグレーションの順序、`deploy/schedules.yaml` |

## 2. 確かめたこと

| 確認 | 結果 |
|---|---|
| `go test ./...`（unit：金額の範囲・状態遷移・fixed の算出・クーポンの検証と順序・プレビューの計算・予約の状態遷移） | 通る |
| `go test -tags integration ./modules/catalog/...`（本物の PostgreSQL 16 と本物の tenant モジュール） | 通る。残り 1 枚の取り合いは 100 回繰り返して常に 1 人だけ成功 |
| E2E（`e2e/tests/catalog/`、parallel 24 件＋clock 1 件） | `task dev:container` で、identity・Platform を含む 63 件すべて通る |
| golangci-lint・e2elint・archtest | 0 件 |
| 掃除ジョブ | `POST /v1/dev/jobs/catalog-coupon-reservation-sweep/run` で worker-billing が実行し succeeded |

## 3. 受け入れ条件

✅ = 自動テストで確かめた。⏳ = 依存するモジュールの実装後に確かめる。

| AC | 状況 | 確かめている場所 |
|---|---|---|
| 001・003・004・006・007 | ✅ | `e2e/tests/catalog/plans.spec.ts`（004 は 403。P-IMP-12） |
| 002・005・014・015・018・026 | ✅ | `modules/catalog/internal/domain/domain_test.go` |
| 008・013・021〜023・028・029 | ✅ | `modules/catalog/internal/app/integration_test.go` |
| 009 | 一部 ✅ | 公開 API の 410 は `checkout-plans.spec.ts`。申込 API の 422 は orchestration の実装後 |
| 010・011 | ✅ | `checkout-plans.spec.ts` |
| 012・034 | ✅ | `authorization.spec.ts`・`tenant-isolation.spec.ts` |
| 016・017 | ✅ | `coupons.spec.ts`・`coupon-preview.spec.ts` |
| 019 | 一部 ✅ | 期限切れ（`coupon-validation.clock.spec.ts`）、対象外・無効化（`coupon-preview.spec.ts`）。上限到達と申込 API での確認は orchestration の実装後 |
| 020 | ✅ | E2E-003・017 |
| 024・025・030・031・033 | ✅ | `e2e/tests/orchestration/checkout-coupon.spec.ts`・`checkout-declined.spec.ts`、E2E-017・003 |
| 027 | 一部 ✅ | 掃除ジョブの解放・二重に戻さないこと・解放イベントは結合テスト（`TestLateConfirm`）。申込で結果不明にした E2E（`coupon-reservation-sweep.clock.spec.ts`）は orchestration・payments の実装後 |
| 032 | 一部 ✅ | EventEnvelope の actor は `audit.spec.ts`。監査記録の DB 確認は audit の実装後 |

## 4. 他の TL への引き継ぎ

- **TL-Orchestration**：申込の金額は `PlanSnapshot.Amount` ではなく `QuotePrice` の結果を使う（§4）。クーポンは「契約 ID を採番 → `ReserveCoupon` → 決済 → 成功なら `ConfirmCouponRedemption`（トライアルありは契約作成の直後）、失敗なら `ReleaseCouponRedemption`」。`ReserveCoupon` は同じ (coupon, customer, subscription) の再試行で同じ予約を返す。申込 API の `plan_unavailable` は 422 で返す（`catalog.Problem(err)` の Status を 422 に変える）。事業者の状態は `GetTenant` で別に確かめる。
- **TL-Subscription・Billing**：割引は `CouponReservation.Discount`（`sharedkernel.Discount.Apply` で計算）と `DurationCycles` を AppliedDiscount に写す。
- **TL-Audit**：catalog の注釈のアクションは integration.md §3.6.1 の `product.*`・`plan.*`・`coupon.*`（P-IMP-13）。
- **E2E**：`e2e/helpers/catalog.ts` に商品・プラン・クーポンを作る補助がある。クーポンの期限は業務時刻（`businessNow`）を基準にする。
