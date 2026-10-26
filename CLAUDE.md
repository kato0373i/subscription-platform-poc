# subscription-platform

マルチテナント型サブスクリプション基盤（PoC）。Go のモジュラーモノリス、ECS Fargate、ローカルは MiniStack。

## 文書の場所
- ディレクトリの案内：`README.md`（どこに何を置くか・1 つのモジュールの中の形・生成物）
- 要件：`docs/requirements/subscription-platform.md`
- 設計：`docs/design/subscription-platform.md`（ADR は §7）
- PoC の範囲と決定：`docs/poc/00-poc-charter.md`、`docs/poc/02-pdm-decisions.md`
- ドメインごとの詳細要件：`docs/poc/specs/*.md`（実装はこれに従う）
- 実装の状況：`docs/poc/impl/*.md`

## ローカル環境とコマンド
- 必要なもの：Docker・Go・Node。`task` がなければ `go tool task <名前>` で同じものが動く（terraform・golangci-lint はコンテナで動く）。
- `task up`（MiniStack・RDS・キュー・マイグレーション）→ `task dev`（4 プロセスを go run）または `task dev:container` → `task verify:local`。
- テスト：`task test:unit`・`task test:integration`・`task e2e`（4 プロセスが起動している前提）・`task lint`。
- まっさらにする：`task reset`。
- デモデータ：まっさらな環境で `task demo:seed`（約 12 か月分の契約・請求と、事業者コンソール・マイページ・運営者コンソールのログイン用アカウント。ログイン情報は `.local/demo-accounts.md`。dev clock を 12 か月先まで進める）。
- コード生成：`task generate`（errcodes・sqlc・oapi-codegen・TS の型）。`api/openapi/**`・`api/errors.yaml`・`modules/*/db/queries`・マイグレーションを変えたら流し、生成物もコミットする。

## 不具合の調査・テストの失敗
- 調べるときは、必ず `docs/runbooks/debugging.md` の手順に従う。
- 起点は相関 ID（`X-Correlation-Id`、Playwright のレポートの `timeline.txt`）。
- ログは `task logs -- --corr <相関ID>`、DB は `task db:reader` で確かめる。推測で直さず、ログと DB で原因を確かめてから直す。

## テスト
- E2E（Playwright、`e2e/`）が中心。モックは原則として使わない（`docs/poc/02-pdm-decisions.md` の P-TST-1〜10）。
- 時刻を進めるテストは、ファイル名の末尾を `.clock.spec.ts` にする。
