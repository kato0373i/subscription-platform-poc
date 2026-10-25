# 実装状況: 事業者コンソール（business-console）

| 項目 | 内容 |
|---|---|
| 対象 | `docs/requirements/business-console.md`（Approved）、`docs/design/business-console.md`（Approved） |
| 更新日 | 2026-10-26 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-IMP-22 |
| コード | `web/console`（SPA）、`modules/tenant`（招待・所属）、`internal/platform/httpx`（Cognito・`x-sp-tenant: none`）、`infra/modules/staff_auth`、`deploy/local/console.conf`、`e2e/tests/console` |

## 1. 作ったもの

| 層 | 内容 |
|---|---|
| 認証（Platform） | Cognito の ID トークンの検証（JWKS は MiniStack の上書き URL から取得）、devtools では開発用 JWT と Cognito を `iss` で振り分ける `MultiVerifier`（ADR-C04）。`x-sp-tenant: none` のルート（所属の一覧・画面のエラーの報告）は X-Tenant-Id を求めない |
| tenant | StaffMember に invited / active・`invited_at`・`last_seen_at` を追加（マイグレーション 00002）。招待（`EnsureUser`：Cognito に `AdminCreateUser`、既存のアカウントならそのまま追加）、招待の再送、初回の認証済みの要求での有効化（ADR-C05）、所属の一覧、ロールの変更の If-Match |
| integration | 既存のアカウントを追加したときのお知らせのメール（`tenant.staff_added` → `staff_added` テンプレート、宛先はスタッフ） |
| catalog・billing | 商品・プラン・リトライ方針の更新に If-Match（なければ従来どおり） |
| subscription・customer | 契約の一覧のメールアドレスの検索、契約者（氏名・メールアドレス・退会済み）を一緒に返す |
| infra | スタッフ用のユーザープール（招待だけでアカウントを作る、パスワードの方針、日本語の招待・確認コードのメール、トークンの有効期限）。`task up` が SPA の設定 `web/console/public/config.json` を書き出す |
| SPA | React 19・Vite・React Router・TanStack Query・Mantine・Amplify Auth（SRP）。ログイン・初回のパスワード変更・再設定・無操作 8 時間のログアウト、事業者の選択と切り替え、契約（一覧・検索・詳細・請求の履歴）、商品・プラン（作成・編集・価格の改定・公開・申込ページの URL）、クーポン、Webhook（送信先・署名鍵の表示とローテーション・配信の履歴・再送）、リトライ方針、API キー、スタッフ（招待・再送・ロールの変更・削除）。ロールで使えない操作は無効にして理由を出す。UI は Stripe のダッシュボードを参考にした（設計 §5.6） |
| 配信 | compose の `console`（nginx、ポート 8090）。`/console/` は SPA（CSP 付き）、`/v1/`・`/checkout` は api |

## 2. 確かめたこと

| 確認 | 結果 |
|---|---|
| `go test ./...`・`go test -tags integration ./...` | 通る |
| golangci-lint・e2elint・`pnpm -r typecheck`・`pnpm --filter @sp/console build` | 0 件・通る |
| E2E（API）：`e2e/tests/identity/console-invite.spec.ts`（招待・重複・権限・再送・テナント分離・所属の一覧・If-Match）、`e2e/tests/subscription/console-search.spec.ts` | 通る |
| E2E（ブラウザ・MiniStack の Cognito）：`e2e/tests/console/auth.spec.ts`（招待 → 初回ログイン → 新しいパスワード、誤ったパスワード、ログアウトと保護、パスワードの再設定、複数の事業者の切り替え）、`operations.spec.ts`（商品・プランの作成と公開、契約の検索と詳細、viewer の無効化、画面からの招待、同時編集の検知） | 通る |
| `task reset` → `task up` → `task dev:container` → `task verify:local` → E2E 全件（parallel＋clock）・selftest | 168 件すべて通る（既存の AC-BIL-032 は応答に version が増えたので期待値を更新）。selftest OK |

## 3. 範囲外・残り

- MFA（要件 FR-001 の将来。Amplify Auth は TOTP のチャレンジに対応しているので画面の追加で足りる）。
- 本番の招待メールの送信元ドメイン（設計 §9 #1。SES の設定は `staff_auth` モジュールの変数で渡せる）。
- 本番の配信（CloudFront＋S3 など）と CSP の `connect-src` の本番の Cognito のエンドポイント（ローカルは `deploy/local/console.conf`）。
- 契約の操作（解約の代行など。要件のスコープ外）。
