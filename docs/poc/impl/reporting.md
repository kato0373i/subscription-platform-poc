# 実装状況: Reporting（ダッシュボードの KGI・KPI）とスマートフォン対応

| 項目 | 内容 |
|---|---|
| 対象 | `docs/requirements/console-dashboard.md`・`docs/design/console-dashboard.md`（Approved） |
| 更新日 | 2026-10-26 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-IMP-23 |
| コード | `modules/reporting`、`cmd/worker-projection`、`api/openapi/console/reporting.yaml`、`deploy/messaging.yaml`（reporting-projection）、`web/console/src/pages/dashboard`、`web/console/src/components/AppLayout.tsx` |

## 1. 作ったもの

| 層 | 内容 |
|---|---|
| domain | 変化の分類（`Classify`。設計 §3.1）、月額換算（年払いは 12 で割って四捨五入）、月の KGI・KPI・12 か月の推移・日ごとの MRR の計算（`Compute`。差分の累積） |
| DB | `reporting` スキーマ：`subscription_facts`・`price_amounts`・`daily_metrics`（UPSERT で足し込み）・`mrr_goals`。すべて RLS。Inbox あり |
| 購読 | `reporting.projection`（worker-projection、キュー reporting-projection）。価格はトランザクションの外で `catalog.QuotePrice` から取得して写す。古い版のイベントは無視 |
| API | `GET /v1/console/reports/overview`、`PUT`・`DELETE /v1/console/mrr-goals/{month}`（`goal.write`、If-Match、監査 `mrr_goal.update`・`mrr_goal.delete`） |
| SPA | ダッシュボード（KGI の帯：MRR・前月末からの増減・ARR・目標の達成率・純増 MRR の内訳・12 か月の推移／要対応：支払い遅延・トライアル終了間近／KPI 8 枚／推移 3 つのグラフと表）。事業者を選んだ後の最初の画面。1 分ごとに読み直す。スマートフォン対応（Burger のメニュー、表の横スクロール、全幅のドロワー、KPI は 2 列） |

## 2. 確かめたこと

| 確認 | 結果 |
|---|---|
| unit：変化の分類の表（開始・転換・不成立・更新・増額・減額・遅延・回復・理由ごとの解約・初めて見る契約）、月額換算、要件 FR-001 の例（MRR 1,960 円・純増 −20 円）、過去の月・未来の日、分母 0、トピックと yaml の一致 | 通る |
| 結合（testcontainers）：Inbox の重複・古い版の無視、入金・要対応、RLS、差分の累積と facts の合計の一致、目標（If-Match・範囲） | 通る |
| E2E（API）`tests/reporting/dashboard.spec.ts`：MRR・ARPU・年払いの換算・入金、事業者の分離、目標と権限・監査、月の範囲 | 通る |
| E2E（clock）`tests/reporting/dashboard-flows.clock.spec.ts`：トライアルの転換と不成立、回復と支払い失敗の解約、解約の予約による解約 → 解約率・収益の解約率・回復率・内訳 | 通る |
| E2E（ブラウザ）`tests/console/dashboard.spec.ts`：KGI・KPI の表示、目標の設定、表の表示、要対応から絞り込んだ一覧へ、閲覧者の無効化、幅 375px（メニューの開閉・横にはみ出さない・全幅のドロワー） | 通る |
| `task reset` → `task up` → `task dev:container`（4 プロセス）→ `task verify:local` → E2E 全件（parallel＋clock）・selftest | 176 件すべて通る。selftest OK |

## 3. 範囲外・残り

- 導入前の過去データの取り込み（要件 §9 #1。本番の構築まで）。
- プラン別・コホート・LTV・CSV 出力（要件 §8）。
- dark テーマ（コンソールは light のみ）。
