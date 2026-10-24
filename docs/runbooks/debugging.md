# 調査の手順（人とコーディングエージェント向け）

不具合やテストの失敗を調べるときの手順。ログの仕組みは設計書 §1.4 と ADR-009 にある。
前提として、ローカル環境（`task up`）が起動している。

## 1. 起点の ID を手に入れる

調査はいつも「相関 ID」から始める。1 件の操作に関係するログは、プロセスをまたいで同じ相関 ID を持っている。

| 起点 | 相関 ID の見つけ方 |
|---|---|
| E2E テストが失敗した | Playwright のレポートの添付 `timeline.txt` の先頭行。添付の `logs.jsonl` に、そのテストのログがすべて入っている |
| API がエラーを返した | レスポンスヘッダー `X-Correlation-Id`、または Problem Details の `correlation_id`。`request_id` も同じ場所にある |
| 何が起きたかわからない | `task logs -- --errors --since 10m` で直近のエラーを見て、その行の `correlation_id` を使う |
| スケジュールのジョブ | `task logs -- --msg job.failed --since 1h` の `correlation_id`（`job:<job>:<run_id>` の形） |

## 2. ログのタイムラインを読む

```bash
task logs -- --corr <相関ID>
```

- api・worker-billing・worker-integration のログが、時刻の順に 1 本に並ぶ。
- 見るところ：
  - `http.request.completed`：どの API が、どのステータスを返したか
  - `domain.state_changed`：契約・請求・決済の状態が、どう変わったか（from → to）
  - `event.published` → `event.consumed`：イベントが届いたか。`event.failed` や `event.dlq` があれば、そこで止まっている
  - `http.outbound.completed`：fincode のスタブ・Webhook の送信先・SES への呼び出しの結果
  - `level=error` の行と、その `error.code`
- JSON のまま加工したいときは、`--format=json` を付けて jq に渡す。

絞り込みの例：

```bash
task logs -- --corr <相関ID> --service worker-billing --level warn
```

```bash
task logs -- --tenant <tenant_id> --since 30m --msg domain.state_changed
```

## 3. DB の値を確かめる

```bash
task db:reader
```

- 読み取り専用のロール `sp_e2e_reader` で psql が開く（RLS を越えてすべてのテナントを読める。書き込みはできない）。
- よく見る表：`subscription.subscriptions`、`billing.invoices`、`payments.payment_attempts`、各スキーマの `outbox_events`（`published_at` が NULL なら未送信）、`orchestration.billing_cycle_processes`、`integration.webhook_deliveries`

## 4. 直して、確かめる

1. 原因の箇所を直す。
2. 失敗していたテストだけを流す。

```bash
pnpm --dir e2e exec playwright test <spec ファイル> -g "<テスト名>"
```

3. 時刻を進めるテスト（`*.clock.spec.ts`）は、`--project=clock` を付けて流す。
4. 関係する UT と結合テストも流す。

```bash
task test:unit
```

```bash
task test:integration
```

5. 最後に、E2E 全体を流す。

```bash
task e2e
```

## 5. CloudWatch Logs に届いているかを確かめる（必要なときだけ）

```bash
task logs:cwl -- 'fields @timestamp, @log, @message | sort @timestamp desc | limit 50'
```

- Fluent Bit が `.local/logs` を MiniStack の CloudWatch Logs（`/sp/local/<service>`）に送っている。本番の `/sp/<env>/<service>` と同じ名前の付け方。
- MiniStack の Logs Insights は **`filter` が効かず**（条件に関係なく行を返す）、`fields` も `@timestamp`・`@log`・`@message` などの `@` の項目だけを返す（2026-10-24 に確認。P-OBS-10）。**調査は `task logs` で行う**。ここで確かめるのは、送り先とロググループの名前だけ。
- `filter`・`stats` を使う本番と同じ形のクエリは、dev 環境（本物の CloudWatch Logs）で試す。

## 注意

- ログには、カード番号・トークン・API キー・Authorization ヘッダーは出ない（出ていたら不具合なので、それ自体を報告する）。メールアドレスと氏名はマスキングされている。
- `.local/logs/` はローカルの実行ごとに増える。消すときは `task logs:clean`。
- CI で失敗したときは、成果物の `playwright-report` と `local-logs` を取り出して、同じ手順で調べる。
