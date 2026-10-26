# 実装状況: 契約者のマイページ（ログインを含む）

| 項目 | 内容 |
|---|---|
| 対象 | `docs/requirements/consumer-mypage.md`・`docs/design/consumer-mypage.md`（Approved） |
| 更新日 | 2026-10-26 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-IMP-24 |
| コード | `web/mypage`（SPA）、`infra/modules/consumer_auth`、`modules/catalog`（contracts の `DescribePlan`・`ListPurchasablePlans`）、`modules/subscription`（me の `display`・`plan-change-options`）、`modules/billing`（me の請求の `attempt_count`）、`web/checkout`（ログインの導線）、`deploy/local/web.conf` |

## 1. 作ったもの

| 層 | 内容 |
|---|---|
| infra | 契約者のプール（自己登録・確認コード・パスワード 8 文字以上で英小文字と数字・リフレッシュトークン 30 日）。`task up` が api の設定・`web/mypage/public/config.json`・メールのリンク（`MYPAGE_URL`・`PAYMENT_METHOD_UPDATE_URL_TEMPLATE`）を書き出す。MiniStack はパスワードの方針で指定しない項目を必須として扱うので、`local-env.sh` が方針を明示して上書きする（ローカル専用） |
| catalog | contracts に `DescribePlan`（プラン名・商品名・事業者名。状態に関係なく返す）・`ListPurchasablePlans`（商品の公開中のプラン） |
| subscription | me の契約に `display`（事業者名・商品名・プラン名・契約時の価格。取れなければ null）。`GET /v1/me/subscriptions/{id}/plan-change-options`（同じ商品の公開中のプランのうち現在以外・適用日つき。予約できない状態なら空。`x-sp-isolation: tested`） |
| billing | me の請求に `attempt_count`（即時の再決済の結果の判定に使う） |
| 申込ページ | トークンがなければプランの内容を見せ、「ログインして申し込む」「アカウントを作って申し込む」へ案内（`?return=` で戻る） |
| SPA | 認証（登録・確認コード・ログイン・再設定・パスワードの変更・ログアウト）、契約の一覧（支払い遅延を先頭・解約済みは折りたたみ）、詳細（予約中の変更・支払方法・請求の履歴・プラン変更・解約）、支払方法の変更（新しいカードの登録・即時の再決済の結果）、カードの一覧と削除、アカウント（退会・Cognito のアカウントの削除）。スマートフォン中心（下部のナビゲーション） |
| 配信 | nginx の `console` サービスを `web` に改名し、`/mypage/` を足した（ADR-M04） |

## 2. 確かめたこと

| 確認 | 結果 |
|---|---|
| unit・結合・lint・e2elint・`pnpm -r typecheck`・両 SPA のビルド | 通る |
| E2E（API）`tests/subscription/me-display.spec.ts`：display と価格の据え置き、plan-change-options（同じ商品・公開中・現在以外・適用日・解約予約中は空・他人は 404） | 通る |
| E2E（ブラウザ）`tests/mypage/signup-checkout.spec.ts`：申込ページから登録して申し込む、登録済み・方針違反・ログインの失敗、再設定と return | 通る |
| E2E（ブラウザ）`tests/mypage/operations.spec.ts`：解約・プラン変更の予約と取り消し、カードの登録と削除（使用中は不可）、退会（契約があると不可・退会後はログインできない）、パスワードの変更、幅 375px | 通る |
| E2E（ブラウザ・clock）`tests/mypage/recovery.clock.spec.ts`：支払い失敗のメールのリンク → ログイン → 拒否されるカードで失敗の案内 → 有効なカードで回復 | 通る |

## 3. 範囲外・残り

- 本番の確認コードのメールの送信元（設計 §11 #1）、fincode の JS のトークン化（同 #2）。
- 要件 §8 のスコープ外（ソーシャルログイン・MFA・メールアドレスの変更・領収書など）。
