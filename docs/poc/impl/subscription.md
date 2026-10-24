# 実装状況: Subscription

| 項目 | 内容 |
|---|---|
| 対象 | `docs/poc/specs/subscription.md`（Fixed） |
| 更新日 | 2026-10-24 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-IMP-14 |
| コード | `modules/subscription`、`api/openapi/{me,console,tenant}/subscription.yaml`、`cmd/internal/wiring/devseed`（一括シードの段階 1） |

## 1. 作ったもの

| 層 | 内容 |
|---|---|
| contracts | `Service`（§4.1 の 16 メソッド）、DTO（`CreateSignupCmd`・`Terms`・`SubscriptionView`・`RenewalOutcome` など）、エラー、13 種類のイベントの名前 |
| domain | Subscription 集約。§3.1 の状態遷移の表をそのままメソッドにした（冪等な再実行はイベントを出さない）。期間・更新日の計算は sharedkernel、予約の適用日の規則（更新処理中・更新日の当日）、割引の残り回数は BeginRenewal で減らす |
| DB | `subscription` スキーマ、§6 の CHECK（I-2・I-3・I-5 など）、trial_usages の主キー、tenant_isolation と consumer_self の RLS |
| app | 楽観ロックの再読込・再適用（最大 3 回）、イベントの payload のスナップショット、Orchestration 向けのコマンドは事業者を引いてから tenant スコープで変更 |
| API | me（一覧・詳細・プラン変更と解約の予約・取り消し）、console（一覧・詳細）、tenant（一覧・詳細・利用権） |
| ジョブ | `subscription-trial-will-end`（毎日 09:00 JST、worker-billing） |
| 一括シード（段階 1） | `POST /v1/dev/seed/subscriptions` → ジョブ `platform-dev-seed-subscriptions`。customer → catalog → subscription の contracts だけで active な契約を作る（支払方法・初回の請求は合成 ID。P-IMP-14） |

## 2. 確かめたこと

| 確認 | 結果 |
|---|---|
| `go test ./...`（unit：期間と更新日・月末とうるう年・トライアル・遷移表・予約の適用日・割引・システムの解約） | 通る |
| `go test -tags integration ./modules/subscription/...` | 通る（同時のトライアル申込、同じ申込の同時実行、楽観ロックの再適用、RLS、一覧とジョブ） |
| E2E（`e2e/tests/subscription/`、9 件） | Platform・identity・catalog を含む 72 件すべて通る |
| golangci-lint・e2elint・archtest | 0 件 |

## 3. 受け入れ条件

✅ = 自動テストで確かめた。⏳ = 依存するモジュールの実装後に確かめる。

| AC | 状況 | 確かめている場所 |
|---|---|---|
| 001〜003・005〜009・012〜014・017〜021・023〜028・030・031・038 | ✅ | `modules/subscription/internal/domain/subscription_test.go` |
| 010 | 一部 ✅ | unit と結合テスト（フォールバック）。申込 API の 422 `trial_not_eligible` の E2E は orchestration の実装後 |
| 011・036・041・042 | ✅ | `modules/subscription/internal/app/integration_test.go` |
| 015 | 一部 ✅ | 取り消し後に更新されることは unit。`renewal-dispatch` を通す E2E は orchestration の実装後 |
| 022 | ✅ | `e2e/tests/subscription/plan-change.spec.ts` |
| 004・016・039・040 | ⏳ orchestration | 申込・日次の更新・支払い遅延中の解約のシナリオ（040 の適用日の規則は unit で確かめている） |
| 029 | ⏳ orchestration | システムの予約の取り消し不可は unit。`tenant.suspended` を購読して予約する E2E は orchestration の実装後 |
| 032 | ✅ | `tenant-subscriptions.spec.ts` |
| 033 | 一部 ✅ | active の利用権は `tenant-subscriptions.spec.ts`。past_due は orchestration の実装後 |
| 034 | 一部 ✅ | 一覧の絞り込み（plan_id・customer_id・status）は `console-subscriptions.spec.ts`。past_due・canceled を混ぜた確認は orchestration の実装後 |
| 035 | 一部 ✅ | 自分の契約の一覧・他人の 404 は `me-subscriptions.spec.ts`。事業者をまたぐ一覧は結合テスト（`TestRLS`）と E2E-016 |
| 037 | 一部 ✅ | ジョブが 1 回だけ通知することは結合テスト。webhook-sink への到達は integration の実装後（トライアル付きの契約の作成も申込 API が要る） |

## 4. 他の TL への引き継ぎ

- **TL-Orchestration**：申込はサガの手順どおり「手順 0 で subscription_id を採番 → `EnsureTenantCustomerID` → `GetPurchasablePlan`・`QuotePrice` → `ReserveCoupon(…, subscription_id)` → `CreateForSignup(CreateSignupCmd{SubscriptionID, …})` → 決済 → `Activate` / `MarkSignupFailed`」。`CreateSignupCmd.SubscriptionID` を渡す（空なら subscription が採番する）。同じ `(customer, signup_request_id)` は既存を返す。`ErrTrialNotEligible` は 422 `trial_not_eligible`（`subscription.Problem`）。日次の更新は `ListDueForRenewal(today)` → `BeginRenewal`（R 済みは同じ Terms）。`subscription.canceled`（past_due からの reason=customer）を購読して `VoidOpenInvoices`（P-ORC-1）。`tenant.suspended` で `ListOpenByTenant` → `ScheduleSystemCancellation`。
- **TL-Billing**：`Terms` は金額を持たない。請求の金額は catalog の `QuotePrice(price_version_id)`、割引は `Terms.Discount`（`AppliedDiscount.Discount().Apply`）。
- **TL-Payments**：`IsPaymentMethodInUse` で削除の可否を判定する。支払い遅延中の支払方法の変更は `ChangePaymentMethod` が同じ支払方法でもイベントを出す（即時の再決済のきっかけ）。
- **一括シード**：payments・billing の実装時に段階 2（支払方法の登録・初回の請求と決済）を `cmd/internal/wiring/devseed` に足す。
