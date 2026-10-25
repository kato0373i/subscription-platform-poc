# 実装状況: Integration（事業者 Webhook・契約者メール）と Audit

| 項目 | 内容 |
|---|---|
| 対象 | `docs/poc/specs/integration.md`（Fixed） |
| 更新日 | 2026-10-25 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-IMP-18 |
| コード | `modules/integration`、`modules/audit`、`api/openapi/console/integration.yaml`、`api/openapi/ops/{integration,audit}.yaml` |

## 1. 作ったもの

| 層 | 内容 |
|---|---|
| contracts | `webhooksig`（署名と検証。標準ライブラリだけ。事業者向けの検証サンプルを兼ねる） |
| domain | 送信先（U1〜U5 の URL の検証、SSRF の拒否リスト、鍵の生成・ローテーション・遅延評価、ブレーカー）、配信（状態遷移、バックオフ、72 時間の打ち切り、見送り）、外部イベント（13 種類の対応表、caller_managed の除外、`evt_` の ID、§4.2 の本文）、メール（7 種類のテンプレートの選択と dedupe_key、再送の間隔と 24 時間の打ち切り） |
| 永続化 | integration：`webhook_endpoints`（鍵は AES-256-GCM の暗号文、ブレーカー・リースの列）、`webhook_events`（送ったバイト列そのもの）、`webhook_deliveries`（auto は 1 つ、pending は 1 つの部分ユニーク）、`webhook_delivery_attempts`（追記だけ）、`email_notifications`（個人情報の列なし）、`invoice_summaries`、Outbox / Inbox。全表に tenant の RLS。audit：`audit_records`（RLS、ランタイムのロールは INSERT・SELECT だけ、トリガーで UPDATE・DELETE・TRUNCATE を拒否） |
| 購読 | `integration-webhook`（13 種類 → 外部イベントと配信を Inbox と同じトランザクションで作る。`cus_` への置き換えは先に行う）、`integration-email`（6 種類のメール、InvoiceSummary の投影、退会で pending を skipped。行を作ってコミットした直後に同じ起動の中で送る） |
| 常駐ループ（worker-integration） | 配信ループ（1 秒ごと、送信先の行のリース＋SKIP LOCKED、送信先ごとに同時実行 1、1 回のリースで 20 件まで、送信直前に読み直し、ブレーカー、failed でオーナーにメール）、メールの再送ループ（10 秒ごと、locked_until が切れた行も拾う） |
| 送信 | `Dialer.Control` で実際に接続する IP を検査（`WEBHOOK_INSECURE_ALLOWED_HOSTS` は例外）、プロキシなし、リダイレクトを追わない、接続 5 秒・全体 10 秒、応答は先頭 1 KB だけ読む。ヘッダー `SP-Signature`・`SP-Event-Id`・`SP-Event-Type`・`SP-Delivery-Id`・`SP-Delivery-Attempt` |
| メール | `text/template` のプレーンテキスト 7 種類（金額 `1,980円`、日付 `2026年10月15日`）。宛先・氏名・事業者名・金額は送信時に解決し、送信後に個人情報でない変数と件名を行に書き足す。`SESMailer`（MiniStack）／`LogMailer`（`MAIL_MODE=log`） |
| ジョブ | `integration-webhook-secret-cleanup`（毎日 04:00 JST、worker-integration） |
| API | console：送信先の CRUD・`rotate-secret`、配信の一覧・取得（試行と送った JSON）・`redeliver`。ops：配信の一覧・`redeliver`、`GET /v1/ops/audit-records` |
| 監査 | `audit.Module.Write`（マスキングを適用して INSERT）を `cmd` が `httpx.Ports.AuditWriter` に渡す。integration の注釈：`webhook_endpoint.create`・`.update`・`.delete`・`.rotate_secret`（期限だけ）、`webhook_delivery.redeliver` |
| 他モジュールの変更 | billing：`invoice_payment_failed` の payload に `amount` を追加 |

## 2. 確かめたこと

| 確認 | 結果 |
|---|---|
| `go test ./...`（unit：署名と検証、URL の検証、SSRF の判定、13 種類の変換、`evt_` の ID、本文、バックオフと 72 時間（本番の値で 11 回）、E9 の境界、見送り・放棄・手動再送、ジッター、ローテーション、ブレーカー、メールの計画、テンプレートの文面、暗号化、messaging.yaml との一致） | 通る |
| `go test -tags integration ./...`（本物の PostgreSQL・tenant・customer、MiniStack の SES、127.0.0.1 の送信先） | 通る（RLS、Inbox の重複と source_event_id、ブレーカーの共有、落ちたタスクからの引き継ぎ、送信先ごとの同時実行 1、公平さ、無効化で abandoned、dedupe_key、SES の障害からの再送と宛先、退会で skipped、リースが切れた行の再送、監査の追記だけ・マスキング） |
| `task up` → `task dev:container` → E2E（integration・audit のテスト、E2E-001・019 の sink の確認、E2E-015） | 通る |
| golangci-lint・e2elint・archtest | 0 件 |

## 3. 受け入れ条件

| AC | 状況 | 確かめている場所 |
|---|---|---|
| 001・002・004・016・017 | ✅ | `e2e/tests/integration/webhook-endpoints.spec.ts` |
| 003・007・010・020・025・033 | ✅ | `modules/integration/**/domain_test.go`・`webhooksig_test.go`・`mail_test.go` |
| 005・006 | ✅ | `webhook-signature.spec.ts`・`webhook-signature.clock.spec.ts` |
| 008・009・034 | ✅ | `webhook-payload.clock.spec.ts`（`cus_` はシナリオ E2E-001 でも確認） |
| 011・019・023・024・026・029・037〜040 | ✅ | `modules/integration/internal/app/integration_test.go`、`modules/audit/internal/app/integration_test.go` |
| 012・013・021 | ✅ | `webhook-delivery.clock.spec.ts`（回復の流れは E2E-015） |
| 014・015 | ✅ | `webhook-redeliver.clock.spec.ts`・`webhook-redeliver.spec.ts` |
| 018・036 | ✅ | `webhook-delivery.spec.ts` |
| 022 | ✅ | `email.clock.spec.ts`（DB の行と MiniStack の SES の本文の両方） |
| 027・028・030・031 | ✅ | `e2e/tests/audit/audit-records.spec.ts` |
| 032 | ✅ | `webhook-payload.spec.ts` |
| 035 | ✅ | archtest（platform は modules を import しない）。`cmd` が `audit.Module` を `AuditWriter` に渡すことは監査の E2E で確認 |

## 4. 他の TL への引き継ぎ

- **E2E**：`e2e/helpers/integration.ts` に送信先の登録（`createEndpoint`・`sinkEndpoint`）、本文の取り出し（`bodyOf`・`header`）、届かないことの確認（`expectNoMoreRequests`）、配信の行（`deliveries`・`untilDelivery`）がある。ローカルでプロセスをコンテナで動かすときは `SINK_TARGET_BASE=http://webhook-sink:8080` で Playwright を流す（CI と同じ）。
- **TL-Identity**：integration §7.1 のロールの記述を identity §3.4 に合わせて直す（P-IMP-18 ②）。
- **残り**：Reporting（FR-010）は拡張口だけ（§10）。配信履歴の保持期限による削除・テスト送信（ping）は作っていない。
