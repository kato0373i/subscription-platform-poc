# 実装状況: 運営者コンソール（ログインを含む）

| 項目 | 内容 |
|---|---|
| 対象 | `docs/requirements/operator-console.md`・`docs/design/operator-console.md`（Approved） |
| 更新日 | 2026-10-26 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-IMP-25 |
| コード | `web/ops`（SPA）、`infra/modules/operator_auth`、`modules/payments`（ops の 2 つの API）、`api/openapi/ops/payments.yaml`、`deploy/local/web.conf` |

## 1. 作ったもの

| 層 | 内容 |
|---|---|
| infra | 運営者のプール（管理者による作成のみ・パスワード 12 文字以上で 4 種の文字・仮パスワード 3 日・グループ `operators`・ID / アクセストークン 1 時間・リフレッシュトークン 8 時間）。`infra/envs/local` の仮のプールを置き換えた。`task up` が api の設定・`web/ops/public/config.json`・`e2e/.env.e2e` の `COGNITO_OPERATOR_USER_POOL_ID` を書き出す |
| payments | `GET /v1/ops/payment-attempts`（確定していない試行。状態・経過時間で絞り、依頼の古い順にカーソルで読む。既存の `ix_attempt_unresolved` を使う）、`POST /v1/ops/reconciliation-reports/{id}/acknowledge`（`open → acknowledged`。対応済みなら何もしない。メモは監査 `reconciliation.acknowledge` の after に残す。ADR-O02） |
| SPA | 認証（ログイン・仮パスワードからの新しいパスワード・再設定・無操作 30 分のログアウト・`operators` でなければ「運営者の権限がありません」）、ホーム（要対応の件数）、事業者（一覧・登録 → オーナーの招待・承認・停止（理由と事業者名の確認）・一括解約の進み具合）、Webhook の配信の再送、結果が不明な決済（一覧・詳細・手動の確定・請求の再決済）、照合の不整合（対応済み・対応の記録）、更新処理（課金日の結果と再実行）、監査ログ（絞り込み・変更の前後）。PC 前提 |
| 配信 | nginx の `web` サービスに `/ops/` を足した（`web/ops/dist` と `config.json`） |
| デモデータ | `task demo:seed` が運営者のアカウント（`operator@demo.example.com`）を作り、`.local/demo-accounts.md` に書く |

## 2. 確かめたこと

| 確認 | 結果 |
|---|---|
| unit・結合（`TestUnresolvedAttempts`・`TestAcknowledgeDiscrepancy`）・lint・e2elint・`pnpm -r typecheck`・3 つの SPA のビルド | 通る |
| E2E（API）`tests/payments/ops-unresolved.spec.ts`：結果不明の試行が出る（経過時間・状態の絞り込み）・確定すると消える・契約者 / スタッフは 403 | 通る |
| E2E（ブラウザ）`tests/ops/auth.spec.ts`：仮パスワード → 新しいパスワード → ホーム、ログアウトと未ログインの保護、`operators` でないアカウントの拒否（API も 403）、無操作のログアウト | 通る |
| E2E（ブラウザ）`tests/ops/operations.spec.ts`：登録 → オーナーの招待（メール）→ 承認 → 監査ログで承認を探す、使われているショップ ID のエラーと問い合わせ番号、ホームの件数から絞り込んだ一覧へ、更新処理の再実行 | 通る |
| E2E（ブラウザ・clock）`tests/ops/operations.clock.spec.ts`：停止（名前の確認）→ 一括解約の完了、結果不明の決済を失敗として確定、照合の不整合を対応済みに（対応の記録・冪等・403・監査）、失敗した配信の再送 | 通る |

## 3. 範囲外・残り

- 運営者の多要素認証・接続元の制限（設計 §11 #1・#2）、3 つの SPA の共通のパッケージへの切り出し（ADR-O01）。
- 画面の想定外のエラーの送信（事業者コンソールの `client-errors` はスタッフ用の API なので、運営者コンソールからは送らない。必要になったら ops 用の受け口を足す）。
