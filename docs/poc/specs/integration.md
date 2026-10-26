# 詳細要件: Integration（事業者 Webhook・契約者メール）と Audit

| 項目 | 内容 |
|---|---|
| ステータス | **Fixed**（2026-10-13 PdM 承認。テスト戦略：Playwright の E2E が中心） |
| 担当 | TL-Integration |
| 作成日 | 2026-10-12 |
| 上位文書 | `00-poc-charter.md`（最優先）、`02-pdm-decisions.md`、`01-contracts-baseline.md`（§5 改訂 v1.1 を含む）、`docs/design/subscription-platform.md`、`docs/requirements/subscription-platform.md` |
| 対象モジュール | `modules/integration`、`modules/audit`（`modules/reporting` は拡張口の記述だけ） |

## 1. 概要と PoC の範囲

| 要件 | PoC | 作るもの | 作らないもの |
|---|---|---|---|
| FR-007 Webhook の設定 | ✅ | 送信先の CRUD、受け取るイベントの選択、署名鍵の発行・ローテーション（現在と前の 2 つ）。鍵は DB に暗号化して保存 | Secrets Manager 連携、送信先ごとの IP 許可リスト |
| FR-022 Webhook の配信 | ✅ | 外部イベントへの変換、署名、少なくとも 1 回の配信、バックオフ、72 時間の打ち切り、手動再送、サーキットブレーカー、SSRF 対策 | 配信ログの保持期限による削除、テスト送信（ping） |
| FR-023 メール | ✅ 簡略 | 6 種類のプレーンテキストのテンプレート、dedupe、SES（MiniStack）、再送 | HTML メール、多言語、配信停止（unsubscribe）、バウンス処理 |
| FR-025 監査 | ✅ 簡略 | 監査記録の保存（書き込み関数）、追記のみの仕組み（DB 権限＋トリガー）、ops の閲覧 API。注釈 `auditctx` と監査ミドルウェアは TL-Platform の担当（P-X-2・P-PLT-4） | S3 Object Lock への書き出し、ハッシュチェーン、事業者向けの閲覧 API、監査のためのイベント購読 |
| NFR-009 | ✅ 一部 | Webhook の署名（HMAC-SHA256・時刻つき） | TLS 終端の設定（Platform/インフラ） |
| NFR-014 | ✅ 一部 | ops からの Webhook 再送。ops 操作はすべて監査に記録 | 管理画面 |
| FR-010 基本指標 | ❌ | §10 に拡張口だけを書く | Reporting モジュール、集計ジョブ、`/v1/console/metrics/daily` |

**実行基盤**（P-ARC-10・P-ARC-11、憲章 C-05 の取り消し）：ECS Fargate のモジュラーモノリス（ADR-001）。PoC の完了条件は、ローカルで各プロセスを起動して E2E が通ること。

| プロセス（`cmd/`） | integration・audit が受け持つ処理 |
|---|---|
| `api` | console・ops の API、監査の書き込み（ミドルウェア） |
| `worker-integration` | ① SQS の受信ループ：`integration-webhook`（§5.2）、`integration-email`（§5.3・§8.3。行を作ってコミットした直後に送る）<br>② 常駐ループ：Webhook の配信（§8.1）、メールの再送（§8.3）<br>③ スケジュールのジョブ：`webhook-secret-cleanup`（日次。§8.5）<br>Webhook 専用のサブネット（NAT2）に置く。複数タスクで動かしても正しく動くようにする |

`worker-projection` は作らない（P-PLT-4）。

## 2. 用語

| 用語 | 意味 |
|---|---|
| 外部イベント | 事業者に Webhook で送るイベント。内部のドメインイベントを変換したもの。ID は `evt_…` |
| 送信先（Endpoint） | 事業者が登録した Webhook の受信 URL |
| 配信（Delivery） | 1 つの外部イベントを 1 つの送信先に届ける単位。試行（Attempt）を複数持つ |
| 打ち切り期限（deadline） | 配信の最初の試行から 72 時間後。これを過ぎたら自動では再試行しない |
| 移行期間（grace period） | ローテーション後、前の鍵でも署名を付け続ける期間 |
| 署名鍵（signing secret） | `whsec_` + 32 バイトの乱数（base64url）。事業者に平文を返すのは発行時だけ |
| 監査注釈（audit annotation） | ユースケースが context に載せる「何を・どう変えたか（before/after）」。ミドルウェアが記録に使う |

## 3. ドメインモデル

### 3.1 WebhookEndpoint（集約ルート）

| フィールド | 型 | 説明 |
|---|---|---|
| id | `ID[WebhookEndpoint]` | UUIDv7 |
| tenant_id | `ID[Tenant]` | |
| url | `EndpointURL`（VO） | §3.1.1 の検証を通ったもの |
| description | string | 0〜256 文字 |
| event_types | `[]ExternalEventType`（VO） | 1 件以上、重複なし、§4.1 の 13 種類のいずれか |
| status | `enabled` / `disabled` | |
| api_version | string | 作成時点の外部イベントのスキーマ版（PoC は `2026-10-01` だけ） |
| current_secret | `SigningSecret` | 暗号化して保存 |
| previous_secret | `*SigningSecret` | |
| previous_expires_at | `*time.Time` | previous_secret と必ず同時に有無が決まる |
| secret_rotated_at | `*time.Time` | |
| deleted_at | `*time.Time` | 論理削除（配信履歴を残すため） |
| version | int | 楽観ロック（事業者の編集だけで上げる。ブレーカーの更新では上げない） |
| breaker.consecutive_failures | int | 連続した失敗の回数（§8.6）。成功で 0 に戻す |
| breaker.open_until | `*time.Time` | この時刻まで送らない。NULL または過去なら閉（過去なら次の 1 件が半開の試し） |
| dispatch_lease_until / dispatch_lease_owner | `*time.Time` / string | 配信ループのどのタスクがこの送信先を担当中か（§8.1。送信先ごとの同時実行を 1 にする）。`version` は上げない |

#### 3.1.1 EndpointURL の検証

| # | ルール | エラー（`errors[].code`） |
|---|---|---|
| U1 | 絶対 URL、2,048 文字以下、ユーザー情報（`user:pass@`）とフラグメント（`#`）を含まない | `url_invalid` |
| U2 | スキームは `https` だけ | `url_not_https` |
| U3 | 例外：設定 `WEBHOOK_INSECURE_ALLOWED_HOSTS`（既定 `localhost,127.0.0.1`。E2E では `webhook-sink` を足す。§11.2）に挙げたホストだけ、`http://<host>[:port]` を許可する。この設定は `APP_ENV=local` 以外では起動時にエラーにする | `url_not_https` |
| U4 | ホストが IP リテラルなら、プライベート・ループバック・リンクローカル・CGNAT・マルチキャスト・未指定（§8.4 の拒否リスト）を拒否する（U3 の例外を除く） | `url_forbidden_host` |
| U5 | 登録時に名前解決し、すべての解決結果が拒否リストに入るなら拒否する。解決できない場合は登録を許可する（送信時に再判定するため） | `url_forbidden_host` |

#### 3.1.2 不変条件

- 削除済みの送信先は変更できない（`not_found` を返す）。
- 1 事業者あたり、削除されていない送信先は 10 個まで（PoC の上限。設定値）。
- previous_secret は `now < previous_expires_at` の間だけ署名に使う。期限後は存在しないものとして扱う（遅延評価。物理的な消去は §8 のジョブ）。
- 平文の鍵は、作成とローテーションの応答でだけ返す。ログ・監査・取得 API には出さない。

#### 3.1.3 メソッド

| メソッド | 振る舞い |
|---|---|
| `NewWebhookEndpoint(tenantID, url, eventTypes, desc, now, gen)` | 鍵を 1 つ生成し、enabled で作る。平文の鍵を一度だけ返す |
| `Update(url?, eventTypes?, desc?, enabled?)` | 部分更新。enabled→disabled にした時点の pending 配信の扱いは §9 |
| `RotateSecret(now, grace)` | previous ← current（既存の previous は即座に捨てる）、current ← 新しい鍵、previous_expires_at ← now + grace。grace = 0 なら previous を持たない |
| `ActiveSecrets(now) []SigningSecret` | current と、期限内の previous を返す（署名に使う順） |
| `Subscribes(t ExternalEventType) bool` | enabled かつ未削除かつ event_types に含む |
| `Delete(now)` | deleted_at を設定 |
| `BreakerAllows(now) bool` | `open_until` が NULL または `≤ now` なら真 |
| `RecordDeliveryResult(ok, now, cfg)` | 成功：consecutive_failures = 0、open_until = NULL。失敗：+1 し、閾値（5）以上なら open_until = now + open の時間（60 秒） |

### 3.2 WebhookEvent（不変のエンティティ）

| フィールド | 型 | 説明 |
|---|---|---|
| id | `EventID`（`evt_` + Crockford base32 26 文字） | 新しい UUIDv7 から生成する。内部の event_id はそのまま出さない |
| tenant_id | `ID[Tenant]` | |
| source_event_id | UUID | 内部の EventEnvelope.event_id。ユニーク（冪等性の鍵） |
| type | `ExternalEventType` | |
| aggregate_type / aggregate_id / aggregate_version | string / UUID / int | 内部のエンベロープの値 |
| occurred_at | time | 内部イベントの発生日時 |
| payload | []byte | §4.2 の JSON を**直列化したバイト列そのもの**。再送でも同じバイト列を送る |

### 3.3 WebhookDelivery（集約ルート）

| フィールド | 型 | 説明 |
|---|---|---|
| id | `ID[WebhookDelivery]` | |
| tenant_id / endpoint_id / webhook_event_id | | |
| trigger | `auto` / `manual` | manual は手動再送で作られたもの |
| redelivery_of | `*ID[WebhookDelivery]` | 手動再送の元 |
| status | `pending` / `succeeded` / `failed` / `abandoned` | |
| attempt_count | int | 実際に HTTP を送った回数（ブレーカーで見送った分は数えない） |
| next_attempt_at | `*time.Time` | pending のときだけ値を持つ |
| first_attempted_at / last_attempted_at | `*time.Time` | |
| deadline_at | `*time.Time` | first_attempted_at + 打ち切り時間（本番 72h） |
| last_status_code / last_error | `*int` / string | 一覧表示用 |

状態遷移：

| 現在 | きっかけ | 次 | 備考 |
|---|---|---|---|
| （なし） | 外部イベントの作成（auto）／手動再送（manual） | pending | next_attempt_at = now |
| pending | 2xx 応答 | succeeded | 終端 |
| pending | 失敗し、次の予定時刻 ≤ deadline_at | pending | attempt_count+1、next_attempt_at = §8.2 |
| pending | 失敗し、次の予定時刻 > deadline_at | failed | 終端。オーナーへの通知メールを作る（§5.3） |
| pending | 送信先が disabled／削除された | abandoned | 終端。送信はしない |
| succeeded / failed / abandoned | — | — | 変更しない。手動再送は**新しい配信**を作る |

メソッド：`NewAuto(endpoint, event, now)`、`NewManual(src, now)`、`RecordSuccess(now, result)`、`RecordFailure(now, result, schedule)`、`Abandon(now, reason)`、`Postpone(until)`（ブレーカーが open のとき。attempt_count は変えない）。

試行（WebhookDeliveryAttempt）は配信の子エンティティとして追記だけする：attempt_no、requested_at、duration_ms、status_code、error_kind（`timeout` / `connection` / `tls` / `ssrf_blocked` / `http_status` / `response_too_slow`）、response_excerpt（先頭 1 KB）。

### 3.4 EmailNotification（集約ルート）

| フィールド | 型 | 説明 |
|---|---|---|
| id | UUIDv7 | |
| tenant_id | `ID[Tenant]` | |
| template_key | §5.2 の 6 種類 + `webhook_delivery_failed` | |
| dedupe_key | string | §5.2。ユニーク |
| recipient_type | `customer` / `tenant_owner` | |
| customer_account_id / subscription_id / invoice_id / endpoint_id | 任意 | 送信時に宛先・変数を解決するための参照 |
| variables | jsonb | **個人情報を含めない**変数だけ（金額・日付・理由・事業者名・リンクなど）。購読時に入れ、送信時に解決した値を書き足す。契約者の氏名・メールアドレスは保存しない |
| subject | string | 送った件名（送信後に入れる） |
| status | `pending` / `sent` / `failed` / `skipped` | |
| attempt_count / next_attempt_at / last_error / provider_message_id / sent_at | | |

状態遷移：pending →（送信成功）sent ／（恒久エラー、または 24 時間の再試行で失敗）failed ／（宛先が退会済み・マスキング済み）skipped。pending →（一時エラー）pending（§8.3 のバックオフ）。

### 3.5 InvoiceSummary（読み取り用の投影。集約ではない）

メールに金額と次回の決済日を入れるために、Billing のイベントから作る。`invoice_id, tenant_id, subscription_id, total, next_collection_at`。Billing の contracts は呼ばない（コンテキストマップに矢印を足さないため）。

### 3.6 AuditRecord（不変。audit モジュール）

| フィールド | 型 | 説明 |
|---|---|---|
| id | UUIDv7 | |
| occurred_at | time | Clock の時刻 |
| tenant_id | `*ID[Tenant]` | 対象の事業者。運営者の事業者横断の操作でも、対象が決まれば入れる |
| actor_type | `operator` / `staff` / `tenant_api` / `system` | 認証の主体（baseline §4） |
| actor_id | string | operator・staff は認証の subject、tenant_api は API キーの ID（平文は入れない） |
| actor_role | string | staff のロール（owner/admin/viewer）。それ以外は空 |
| action | string | §3.6.1 の一覧 |
| target_type / target_id | string | 例：`plan` / UUID |
| before / after | jsonb | 変更前後の値。秘密情報と個人情報は入れない（§3.6.2） |
| outcome | `succeeded` / `denied` | denied は 403 になった操作 |
| request_id / correlation_id | string | |
| http_method / route | string | ルートはテンプレート（例：`/v1/console/plans/{id}/price-versions`） |
| source_ip | inet | |

#### 3.6.1 記録する操作

| action | API | before / after | 注釈を書く TL |
|---|---|---|---|
| `tenant.create` / `tenant.approve` / `tenant.suspend` | ops | status（suspend は reason も） | Identity |
| `staff.create` / `staff.update_role` / `staff.delete` | console・ops | role | Identity |
| `staff.invite` / `staff.resend_invitation` | console・ops | after：staff_id・role・status（メールアドレス・subject は入れない）。business-console FR-006・FR-007 | Identity |
| `api_key.create` / `api_key.revoke` | console | key_prefix、status | Identity |
| `product.create` / `product.update` | console | 変更した項目 | Catalog |
| `plan.create` / `plan.update` | console | 変更した項目 | Catalog |
| `plan.price_version_add` | console | before：直前の amount・tax_rate、after：新しい amount・tax_rate・price_version_id | Catalog |
| `plan.publish` / `plan.unpublish` | console | published | Catalog |
| `coupon.create` / `coupon.disable` | console | 割引の内容・status | Catalog |
| `dunning_policy.update` | console | retry_offsets | Billing |
| `mrr_goal.update` / `mrr_goal.delete` | console | amount（target_id は月 YYYY-MM。console-dashboard FR-002） | Reporting |
| `webhook_endpoint.create` / `.update` / `.delete` / `.rotate_secret` | console | url・event_types・status（鍵は入れない。rotate は previous_expires_at だけ） | Integration |
| `webhook_delivery.redeliver` | console・ops | 元の配信 ID → 新しい配信 ID | Integration |
| `invoice.collect` | ops | 請求の status | Orchestration |
| `payment_attempt.resolve` | ops | 試行の status | Payments |
| `billing_run.rerun` | ops | 対象日・件数 | Orchestration |

- `/v1/me`（契約者の操作）と `/v1/dev/*` は記録しない。`/v1/tenant` の変更系は PoC にないが、追加されたら自動的に記録される。
- 将来の「強制解約」などの ops 操作も、注釈を書けば同じ仕組みで記録される。

#### 3.6.2 マスキング

before/after に入れてはいけないもの：署名鍵、API キーの平文、トークン、カード情報、契約者の氏名・メールアドレス。`audit.Module.Write` は before/after（`map[string]any`）を保存する前に走査し、キー名 `secret` / `token` / `password` / `email` / `name`（契約者）を含むものは `"***"` に置き換える（安全網。本来は注釈側で入れない）。

## 4. 公開する契約

### 4.1 外部イベントの種類（baseline §2「Webhook で外部に出すときの扱い」の詳細化）

| 外部 event type | 内部 event_type | data.object |
|---|---|---|
| `subscription.started` | `subscription.started` | subscription |
| `subscription.trial_will_end` | `subscription.trial_will_end` | subscription（+ `trial_end`） |
| `subscription.renewed` | `subscription.renewed` | subscription（+ `invoice_id`） |
| `subscription.past_due` | `subscription.past_due` | subscription（+ `invoice_id`） |
| `subscription.recovered` | `subscription.recovered` | subscription（+ `invoice_id`） |
| `subscription.plan_change_scheduled` / `_revoked` / `subscription.plan_changed` | 同名 | subscription（+ `from_plan_id`, `to_plan_id`, `effective_date`） |
| `subscription.cancellation_scheduled` | 同名 | subscription（+ `effective_date`, `reason`：customer / tenant_suspended） |
| `subscription.cancellation_revoked` | 同名 | subscription（+ `effective_date`） |
| `subscription.canceled` | `subscription.canceled` | subscription（+ `reason`） |
| `invoice.paid` | `billing.invoice_paid` | invoice |
| `invoice.payment_failed` | `billing.invoice_payment_failed`（`collection_mode = scheduled` のものだけ） | invoice |

- `subscription.signup_failed` と `subscription.payment_method_changed` は出さない（baseline のとおり）。
- `billing.invoice_payment_failed` のうち `collection_mode = caller_managed`（申込の決済失敗。`next_collection_at = null`）は出さない（P-BIL-5、baseline §5.5）。`collection_mode` がない payload は `scheduled` とみなす。

### 4.2 ペイロードの JSON スキーマ

```json
{
  "id": "evt_01JA8Z3K6Q9W2M4T7X0B5C8D1E",
  "type": "subscription.renewed",
  "api_version": "2026-10-01",
  "created_at": "2026-10-15T01:00:03Z",
  "tenant_id": "0192f0c4-....",
  "data": {
    "object": {
      "object": "subscription",
      "id": "0192f0c5-....",
      "customer_id": "cus_8f3kq0w2n7x1",
      "plan_id": "…", "price_version_id": "…",
      "status": "active", "entitled": true,
      "current_period": { "start": "2026-10-15", "end": "2026-11-14" },
      "next_renewal_date": "2026-11-15", "trial_end": null,
      "cancel_at_period_end": false, "pending_plan_id": null,
      "version": 7
    },
    "invoice_id": "0192f0c6-...."
  }
}
```

| 項目 | ルール |
|---|---|
| `id` | 外部イベント ID。同じイベントの再送（自動・手動とも）では同じ値。事業者はこれで重複を除く |
| `created_at` | 内部イベントの occurred_at（UTC、RFC 3339、秒精度）。配信時刻ではない |
| `data.object.object` | `subscription` または `invoice` |
| subscription の項目 | baseline の「契約のスナップショット」から `tenant_id` を除き、`subscription_id` → `id`、`customer_account_id` → `customer_id`（`cus_…`）に置き換える。`version` は aggregate_version |
| invoice の項目 | `id`、`subscription_id`、`amount`、`version`。paid は `paid_at`、payment_failed は `attempt_no`、`next_collection_at`、`failure_code`。`customer_id` は入れない（P-INT-3） |
| 日付 | `Date` は `YYYY-MM-DD`（JST の暦日）、時刻は UTC の RFC 3339 |
| 互換性 | 項目の追加は互換とみなす（事業者は未知の項目を無視する）。削除・意味の変更は `api_version` を上げる |
| サイズ | 64 KB 以下 |

**順番の判定**：事業者は `data.object.id` ごとに受け取った `data.object.version` の最大値を持ち、それ以下のイベントは状態の更新に使わない（設計書 §6.3）。

**内部 ID を出さない**：customer_account_id・内部 event_id・correlation_id は出さない。`ResolveTenantCustomerID`（customer/contracts）で `cus_` に置き換える。

### 4.3 署名

| 項目 | 内容 |
|---|---|
| ヘッダー | `SP-Signature: t=<UNIX 秒>,v1=<hex>[,v1=<hex>]` |
| 署名対象 | `"<t>." + 送信する body のバイト列` |
| アルゴリズム | HMAC-SHA256（鍵は `whsec_` 以降の文字列を base64url デコードしたバイト列）。hex 小文字 |
| 複数の鍵 | 移行期間中は current と previous の 2 つの `v1=` を付ける（current が先） |
| `t` | 試行ごとの送信時刻（Clock）。試行ごとに署名し直す |
| リプレイ対策（受信側の推奨） | `|now - t| ≤ 300 秒` を満たさないものを拒否し、`id` で重複を除く。検証は定数時間比較で行う。ドキュメントに検証のサンプルを載せる |
| その他のヘッダー | `Content-Type: application/json`、`User-Agent: SubscriptionPlatform-Webhooks/1.0`、`SP-Event-Id`、`SP-Event-Type`、`SP-Delivery-Id`、`SP-Delivery-Attempt` |

署名と検証は `integration/contracts/webhooksig`（標準ライブラリだけに依存）に置く。E2E では、テスト側（TypeScript）が別に実装した検証で確かめる（§11.2 WS-7）。

### 4.4 監査の記録経路と担当の分け方（P-X-2・P-PLT-4）

| 部品 | 置き場所 | 担当 | 内容 |
|---|---|---|---|
| `auditctx.Annotate(ctx, action, target{type,id}, before, after)` | `internal/platform/auditctx` | TL-Platform | context の中の袋に注釈を書く。ドメインの知識は持たない |
| 監査ミドルウェア | `internal/platform/httpx` | TL-Platform | `/v1/console`・`/v1/ops`・`/v1/tenant` の変更系（POST/PUT/PATCH/DELETE）で袋を用意し、応答後に `auditctx.Entry`（§3.6 の項目）を組み立てて、注入された書き込み関数を呼ぶ |
| 書き込み関数の型 | `internal/platform/auditctx` | TL-Platform | `type WriteFunc func(ctx context.Context, e Entry) error`。ミドルウェアは `WriteFunc` を引数で受け取るだけ |
| `audit.Module.Write(ctx, auditctx.Entry) error` | `modules/audit`（module.go から公開） | TL-Integration | §3.6.2 のマスキングを適用して `audit.audit_records` に INSERT する。`cmd/api` が `httpx.AuditMiddleware(auditModule.Write)` の形で**関数として渡す** |
| 閲覧 API・追記だけの仕組み | `modules/audit` | TL-Integration | §7.2、付録 A（audit の REVOKE とトリガー） |

- 依存の向き：`cmd/api` → `modules/audit` と `internal/platform`。`modules/audit` → `internal/platform/auditctx`（型だけ）。**platform は modules を import しない**。
- 各ドメインのユースケースは、§3.6.1 の action について注釈を書く。注釈がない変更系の 2xx も、action = `http.<method>`、route 付きで記録する（取りこぼし防止）。
- 監査のためにイベントを購読する処理（`payments.*` などを含む）は作らない。EventEnvelope の `actor`（baseline §5.1）は監査には使わない（トレース用）。

## 5. 購読するイベントと処理

### 5.1 キューと冪等性

| キュー（標準 SQS） | 購読者（Inbox の consumer 名） | 購読するイベント |
|---|---|---|
| `integration-webhook` | `integration.webhook` | §4.1 の内部イベント 13 種類 |
| `integration-email` | `integration.email` | `subscription.started` / `trial_will_end` / `renewed` / `past_due` / `cancellation_scheduled` / `canceled`、`billing.invoice_issued`、`billing.invoice_payment_failed`、`customer.withdrawn` |

- 監査（audit）はイベントを購読しない（P-X-2）。

- 2 つのキューに分ける（メールの障害が Webhook を止めない。逆も同じ）。どちらも DLQ を持ち、maxReceiveCount = 5。`worker-integration` の受信ループの同時実行数は、`integration-webhook` が 10、`integration-email` が 5（SES の送信レート）。
- 冪等性：1 つの DB トランザクションで、`processed_messages(consumer, message_id = event_id)` への INSERT（ON CONFLICT DO NOTHING）と業務の書き込みを行う。INSERT が 0 行なら何もせずに ack する。
- 二重の防御：`webhook_events.source_event_id UNIQUE`、`webhook_deliveries` の部分ユニーク、`email_notifications.dedupe_key UNIQUE`。
- テナントのコンテキスト：エンベロープの tenant_id を `app.tenant_id` に設定してから処理する。

### 5.2 Webhook の購読処理（`integration.webhook`）

1. エンベロープの event_type を外部 type に変換する（§4.1）。対象外、または `billing.invoice_payment_failed` で `collection_mode = caller_managed` なら、Inbox だけ記録して ack する。
2. 事業者の送信先のうち `Subscribes(type)` が真のものを取得する。0 件なら外部イベントを作らずに ack する（過去のイベントを後から作った送信先に送ることはしない）。
3. payload に customer_account_id があれば `ResolveTenantCustomerID` で `cus_` に置き換える。見つからない・エラーなら、ack せずに SQS の再配信に任せる。
4. `webhook_events` を 1 行、送信先ごとに `webhook_deliveries`（pending、next_attempt_at = now）を作る。Inbox と同じトランザクションで commit する。
5. ここでは HTTP の送信はしない。配信は DB の配信予定（`next_attempt_at = now`）として残り、§8.1 の配信ループが 1 秒以内に取り出して送る。課金側のキューとは独立しているので、送信先の遅延が課金処理に影響しない（FR-022）。

### 5.3 メールの購読処理（`integration.email`）

| template_key | きっかけ | 宛先 | テンプレートの変数 | dedupe_key |
|---|---|---|---|---|
| `signup_completed` | `subscription.started` | 契約者 | customer_name, tenant_name, status（trialing/active）, trial_end, first_charge_date（= trial_end + 1 日。D-12）, current_period, mypage_url | `signup_completed:{subscription_id}` |
| `trial_will_end` | `subscription.trial_will_end` | 契約者 | customer_name, tenant_name, trial_end, first_charge_date, mypage_url | `trial_will_end:{subscription_id}:{trial_end}` |
| `renewal_succeeded` | `subscription.renewed` | 契約者 | customer_name, tenant_name, amount（InvoiceSummary.total）, current_period, next_renewal_date | `renewal_succeeded:{invoice_id}` |
| `payment_failed` | `subscription.past_due` | 契約者 | customer_name, **tenant_name**, **amount**, next_collection_at（あれば）, **payment_method_update_url** | `payment_failed:{invoice_id}` |
| `cancellation_scheduled` | `subscription.cancellation_scheduled` | 契約者 | customer_name, tenant_name, effective_date, reason（customer / tenant_suspended で文面を分ける。下記） | `cancellation_scheduled:{subscription_id}:{aggregate_version}` |
| `subscription_canceled` | `subscription.canceled` | 契約者 | customer_name, tenant_name, reason（customer / dunning / tenant_suspended で文面を分ける）, canceled_on | `subscription_canceled:{subscription_id}` |
| `webhook_delivery_failed` | 配信が failed になったとき（§8.1。イベントではない） | 事業者オーナー | tenant_name, endpoint_url, failed_count, console_url | `webhook_delivery_failed:{endpoint_id}:{JST の日付}` |

- プラン名は PoC では入れない（P-INT-2）。
- 太字は FR-023 の受け入れ条件で必須の変数。
- `payment_method_update_url` は設定 `PAYMENT_METHOD_UPDATE_URL_TEMPLATE`（例：`http://localhost:8080/checkout/?subscription={subscription_id}`）から作る。
- `payment_failed` は**支払い遅延になったとき（請求ごとに 1 回）**だけ送る。その後のリトライの失敗では送らない（P-INT-5）。
- `cancellation_scheduled` の payload の `reason`（P-INT-6、baseline §5.5）で文面を分ける。
  - `customer`：「解約のお申し込みを受け付けました。{effective_date} まで引き続きご利用いただけます」
  - `tenant_suspended`：「{tenant_name} のサービス提供終了に伴い、ご契約は {effective_date} で終了します。以後の請求はありません」（D-01）。契約者は取り消せないので、取り消しの案内を入れない
  - `reason` がない payload は `customer` とみなす
- `subscription_canceled` も同様に、`tenant_suspended` は事業者の停止による終了の文面にする（FR-026「各契約者に解約通知メール」）。
- `billing.invoice_payment_failed` で `collection_mode = caller_managed` のもの（申込の決済失敗）はメールを作らず、InvoiceSummary も更新しない（P-BIL-5）。申込の失敗は申込 API の応答で契約者に伝わる。
- `billing.invoice_issued` → InvoiceSummary を upsert。`billing.invoice_payment_failed` → next_collection_at を更新。
- `customer.withdrawn` → その契約者宛ての pending のメールを skipped にする。
- 購読者は、行を作ってコミットした直後に、**同じ起動の中でそのメールを送る**（§8.3）。宛先・氏名・事業者名・金額は送信時に解決するので、個人情報を integration に保存しない。

## 6. 永続化

DDL の全文は付録 A に置く。ここでは要点だけを書く。

| スキーマ.テーブル | 役割 | 主な制約・インデックス |
|---|---|---|
| `integration.webhook_endpoints` | 送信先（§3.1） | url は https（ローカル例外の localhost だけ DB でも許す）、鍵は暗号文、`(secret_previous IS NULL) = (previous_expires_at IS NULL)`、`(tenant_id) WHERE deleted_at IS NULL`。**ブレーカーの列** `breaker_consecutive_failures`・`breaker_open_until`（§8.6）、**配信のリースの列** `dispatch_lease_until`・`dispatch_lease_owner`（§8.1） |
| `integration.webhook_events` | 外部イベント（§3.2） | `id` は `evt_` の形式、`source_event_id UNIQUE`、payload は 64 KB 以下のバイト列そのもの |
| `integration.webhook_deliveries` | 配信（§3.3） | `UNIQUE(endpoint_id, webhook_event_id) WHERE trigger='auto'`、`… WHERE status='pending'`、`(endpoint_id, next_attempt_at) WHERE status='pending'`（配信ループ用）、`(tenant_id / endpoint_id, created_at DESC, id)` |
| `integration.webhook_delivery_attempts` | 試行の履歴（追記だけ） | `UNIQUE(delivery_id, attempt_no)`、response_excerpt は 1 KB 以下 |
| `integration.email_notifications` | メール（§3.4） | `dedupe_key UNIQUE`、`(next_attempt_at) WHERE status='pending'`、個人情報の列を持たない |
| `integration.invoice_summaries` | 投影（§3.5） | `invoice_id` が主キー |
| `integration.outbox_events` / `processed_messages` | Platform 共通 | Inbox は `PK(consumer, message_id)` |
| `audit.audit_records` | 監査（§3.6） | ランタイムのロールは INSERT・SELECT だけ。UPDATE・DELETE・TRUNCATE はトリガーでも拒否する |

**RLS**

| テーブル | ポリシー |
|---|---|
| integration の全テーブル（`invoice_summaries` を含む） | `ENABLE` + `FORCE ROW LEVEL SECURITY`。`USING (tenant_id = current_setting('app.tenant_id')::uuid)`、`WITH CHECK` も同じ |
| `audit.audit_records` | 同上（`current_setting('app.tenant_id', true)` を使い、未設定なら 0 行）。tenant_id が NULL の行は運営者のロールでだけ読める |
| 事業者横断の処理 | 配信ループ・メールの再送ループの「取り出し」と ops API は、Platform が用意する RLS を回避するロールを使う。取り出した後の 1 件ごとの処理では `app.tenant_id` を設定してから処理する |

**暗号化**：`SecretCipher` ポート（`Encrypt(plain) (keyID, ciphertext)` / `Decrypt(keyID, ciphertext)`）。PoC の実装は AES-256-GCM で、鍵は環境変数 `WEBHOOK_SECRET_ENC_KEY`（base64、32 バイト）。本番では KMS の実装に差し替える。

## 7. API

共通：エラーは Problem Details、一覧はカーソル方式（baseline §4）。他の事業者のリソースは 404。console は `X-Tenant-Id` で操作するテナントを指定する（P-IDN-1）。停止中の事業者は、閲覧はできるが書き込み（作成・更新・削除・rotate-secret・redeliver）はできない（P-IDN-4。判定は Platform の認可）。

### 7.1 console（主体：staff）

| メソッド・パス | ロール | リクエスト | レスポンス | エラーコード |
|---|---|---|---|---|
| `POST /v1/console/webhook-endpoints` | owner, admin | `url`（必須）、`event_types[]`（必須）、`description`、`enabled`（既定 true） | 201：Endpoint + `secret`（平文。この応答だけ） | `validation_failed`（`url_invalid` / `url_not_https` / `url_forbidden_host` / `event_type_unknown` / `event_types_empty`）、`webhook_endpoint_limit_exceeded`（409） |
| `GET /v1/console/webhook-endpoints` | 全ロール | `limit`、`starting_after` | 200：`{data: Endpoint[], has_more, next_cursor}`（削除済みは含めない） | — |
| `GET /v1/console/webhook-endpoints/{id}` | 全ロール | — | 200：Endpoint | `not_found` |
| `PATCH /v1/console/webhook-endpoints/{id}` | owner, admin | `url`、`event_types[]`、`description`、`enabled`（すべて任意）、`If-Match: <version>`（任意） | 200：Endpoint | `validation_failed`、`not_found`、`conflict` |
| `DELETE /v1/console/webhook-endpoints/{id}` | owner, admin | — | 204 | `not_found` |
| `POST /v1/console/webhook-endpoints/{id}/rotate-secret` | owner, admin | `grace_period_hours`（0〜168、既定 24） | 200：`{secret, previous_secret_expires_at}` | `validation_failed`、`not_found` |
| `GET /v1/console/webhook-deliveries` | 全ロール | `endpoint_id`、`status`、`event_id`、`event_type`、`limit`、`starting_after` | 200：`{data: Delivery[], …}`（新しい順） | `validation_failed` |
| `GET /v1/console/webhook-deliveries/{id}` | 全ロール | — | 200：Delivery + `attempts[]` + `payload`（送った JSON） | `not_found` |
| `POST /v1/console/webhook-deliveries/{id}/redeliver` | owner, admin | — | 202：新しい Delivery（trigger = manual、next_attempt_at = now）。配信ループが 1 秒以内に送る | `not_found`、`webhook_delivery_in_progress`（409：同じ送信先・同じイベントに pending がある）、`webhook_endpoint_unavailable`（409：送信先が disabled・削除済み） |

- **Endpoint**：`id, url, description, event_types[], status, api_version, secret_rotated_at, previous_secret_expires_at, created_at, updated_at`（鍵は含めない）
- **Delivery**：`id, endpoint_id, event{id, type, created_at}, trigger, redelivery_of, status, attempt_count, first_attempted_at, last_attempted_at, next_attempt_at, deadline_at, last_response{status_code, error_kind}, created_at`
- **attempts[]**：`attempt_no, requested_at, duration_ms, status_code, error_kind, response_excerpt`

### 7.2 ops（主体：operator）

| メソッド・パス | リクエスト | レスポンス | エラーコード |
|---|---|---|---|
| `GET /v1/ops/webhook-deliveries` | `tenant_id`（必須）、`status`、`endpoint_id`、ページング | console と同じ形 | `validation_failed` |
| `POST /v1/ops/webhook-deliveries/{id}/redeliver` | — | 202：新しい Delivery | console と同じ |
| `GET /v1/ops/audit-records` | `tenant_id`、`action`、`actor_id`、`occurred_from`、`occurred_to`、ページング（`(occurred_at, id)` の新しい順） | 200：`{data: AuditRecord[], …}` | `validation_failed` |

設計書のエンドポイント一覧にない追加は、`GET /v1/console/webhook-deliveries/{id}` と §7.2 の 3 つ（NFR-014 の「Webhook の再送」と監査の閲覧のため）。

### 7.3 dev（ローカルだけ。本番ビルドから除外）

`POST /v1/dev/jobs/{job}/run` の `job` に `webhook-secret-cleanup` を登録する。配信とメールの再送は常駐ループなので、E2E では `POST /v1/dev/clock` で時刻を進め、応答の `effective_after` を過ぎてから（P-ARC-8）ループの処理を待つ。

## 8. ジョブ・非同期処理

### 8.1 配信ループ（`worker-integration` に常駐。P-ARC-11）

**送信先ごとの同時実行は 1 にする**（TL 判断）。複数タスクでも守れるように、送信先の行にリースを取ってから、その送信先の配信を順に送る。4 に増やす口は §10 に残す。

| 項目 | 値（本番） | PoC の既定 |
|---|---|---|
| ポーリング間隔 | 1 秒 | 同じ |
| 送信先のリースの取得 | 下の SQL。空いている並列の枠の数まで、時刻を迎えた配信を持つ送信先を取る | 同じ |
| リース | `dispatch_lease_until = now + 30 秒`、`dispatch_lease_owner = <タスク ID>`。1 件送るたびに延長する（owner が自分のときだけ） | 同じ |
| 1 回のリースで送る件数 | 最大 20 件（ほかの送信先を待たせないため）。終わったらリースを外す | 同じ |
| 1 タスクの並列数（同時に担当する送信先の数） | 64 | 8 |
| HTTP タイムアウト | 接続 5 秒・全体 10 秒。リダイレクトは追わない（3xx は失敗） | 同じ |
| 応答の扱い | 2xx = 成功。それ以外・タイムアウト・接続エラー = 失敗（すべて再試行の対象） | 同じ |

```sql
UPDATE integration.webhook_endpoints e
   SET dispatch_lease_until = $now + interval '30 seconds', dispatch_lease_owner = $task
 WHERE e.id IN (
   SELECT e2.id FROM integration.webhook_endpoints e2
    WHERE (e2.dispatch_lease_until IS NULL OR e2.dispatch_lease_until < $now)
      AND EXISTS (SELECT 1 FROM integration.webhook_deliveries d
                   WHERE d.endpoint_id = e2.id AND d.status = 'pending' AND d.next_attempt_at <= $now)
    ORDER BY e2.dispatch_lease_until NULLS FIRST
    LIMIT $free_slots
    FOR UPDATE SKIP LOCKED)
RETURNING e.id, e.tenant_id;
```

**送信先 1 つの処理**（リースを持つ goroutine が行う）

1. `app.tenant_id` を設定し、その送信先の `status='pending' AND next_attempt_at <= now` の配信を `next_attempt_at, id` の順に 1 件読む。なければリースを外して終わる。
2. 送信先が disabled・削除済みなら、pending の配信をすべて `Abandon` してリースを外す。
3. `BreakerAllows(now)` が偽なら、時刻を迎えた配信を `Postpone(breaker_open_until)` にしてリースを外す（attempt_count は増やさない）。
4. `ActiveSecrets(now)` で署名して送る（§4.3、§8.4）。
5. 1 つのトランザクションで、試行の追記・配信の状態（§3.3）・送信先のブレーカー（§8.6）を更新し、リースを延長する。
6. failed になったら `webhook_delivery_failed` のメールを作る（dedupe で 1 送信先 1 日 1 通）。
7. 20 件に達するか、時刻を迎えた配信がなくなるまで 1 に戻る。

- タスクが落ちたら、30 秒後にリースが切れ、別のタスク（または再起動後のタスク）が続きを送る。送信済みで記録前だった 1 件は、もう一度送られる（少なくとも 1 回）。
- 時刻はすべて `Clock` から取る。E2E では、本番のバックオフ値のまま `POST /v1/dev/clock` で時刻を進めて再現する（72 時間の打ち切りも同じ）。

### 8.2 バックオフ表

| 失敗した試行 | 次の試行までの待ち（本番） | 最初の試行からの累計（目安） | PoC の短縮設定の例 |
|---|---|---|---|
| 1 | 1 分 | 1 分 | 1 秒 |
| 2 | 5 分 | 6 分 | 2 秒 |
| 3 | 30 分 | 36 分 | 4 秒 |
| 4 | 2 時間 | 2 時間 36 分 | 8 秒 |
| 5 | 5 時間 | 7 時間 36 分 | 8 秒 |
| 6 | 10 時間 | 17 時間 36 分 | 8 秒 |
| 7 以降 | 12 時間 | 29h36m → 41h36m → 53h36m → 65h36m | 8 秒 |
| 打ち切り | 予定時刻が deadline（最初の試行 + 72 時間）を超えたら failed | 最大 11 回 | 30 秒 |

- ジッター：待ち時間 × (1 ± `WEBHOOK_RETRY_JITTER`) の一様乱数（既定 0.1）。E2E では設定で 0 にする（§11.1）。
- 設定：`WEBHOOK_RETRY_SCHEDULE`（カンマ区切りの期間。最後の値を繰り返す）、`WEBHOOK_GIVE_UP_AFTER`（既定 `72h`）。
- FR-022 の「2 時間ダウン」の場合、2 時間 36 分の試行で届く。

### 8.3 メールの送信：購読者の中で送り、常駐ループで再送する

| 項目 | 内容 |
|---|---|
| 初回の送信 | `integration-email` の受信処理が、行を作るトランザクションで `locked_until = now + 60 秒` を入れてコミットし、**その直後に送る**。結果（sent / pending＋次の時刻 / failed / skipped）を記録し、`locked_until = NULL` に戻す。送信が失敗しても SQS のメッセージは成功として扱う |
| 再送 | `worker-integration` の常駐ループ（10 秒ごと。TL 判断で scheduler のジョブにはしない）：`status='pending' AND next_attempt_at <= now AND (locked_until IS NULL OR locked_until < now)` を 50 件まで SKIP LOCKED で取り出し、`locked_until = now + 60 秒` にしてから送る。受信処理が送信の途中で落ちた行も、リースが切れたらここで拾う |
| 変数の解決 | 契約者：`GetContact`（customer/contracts）。事業者名：`GetTenant(...).Name`、オーナーの宛先：`ListOwnerContacts`（tenant/contracts。P-INT-1、baseline §5.2）。金額：InvoiceSummary（なければ一時エラー扱い） |
| 送信 | `Mailer` ポート。実装は `SESMailer`（aws-sdk-go-v2、エンドポイントを MiniStack に向ける）と `LogMailer`（`MAIL_MODE=log`。手動確認用）。差出人は設定 `MAIL_FROM`。送信が成功したら、件名を `subject` に、送信時に解決した個人情報でない変数（`tenant_name`・`amount`・`payment_method_update_url` など）を `variables` に書き足す（§11.3 の B で確かめるため） |
| テンプレート | `text/template`、`modules/integration/internal/adapter/mail/templates/*.txt`（件名と本文）。金額は `1,980円`、日付は `2026年10月15日` |
| 一時エラー | SES のスロットリング・5xx・タイムアウト・InvoiceSummary 未着 → 1 分, 5 分, 30 分, 2 時間, 6 時間（最後を繰り返す）。最初の作成から 24 時間を過ぎたら failed |
| 恒久エラー | `MessageRejected`・宛先の形式不正 → failed |
| 宛先なし | 退会済み・マスキング済み → skipped |
| 課金との関係 | 課金処理はメールの成否を待たない（別キュー・別プロセス）。FR-023「メール送信の失敗は課金処理を巻き戻さない」 |

### 8.4 SSRF 対策（送信時）

- `net.Dialer.Control` で、**実際に接続する IP** を検査する（DNS リバインディング対策。登録時の検査だけに頼らない）。
- 拒否リスト：`0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/4, 240.0.0.0/4, ::/128, ::1/128, fc00::/7, fe80::/10`。IPv4 射影アドレス（`::ffff:a.b.c.d`）は IPv4 に戻してから判定する。
- 拒否したら error_kind = `ssrf_blocked` の失敗として記録する（再試行の対象。設定の誤りに気づけるように配信履歴に出す）。
- プロキシの環境変数は使わない（`Transport.Proxy = nil`）。本番では Webhook 専用の NAT を通す（インフラ側）。
- `WEBHOOK_INSECURE_ALLOWED_HOSTS`（U3）に挙げたホストへの接続だけは、拒否リストの対象から外す（ローカルの `webhook-sink` は Docker のプライベート IP を持つため）。

### 8.5 署名鍵の後片付け（`webhook-secret-cleanup`、日次）

`previous_expires_at < now` の行の `secret_previous_ciphertext` と `previous_expires_at` を NULL にする。署名は §3.1.2 の遅延評価で期限を守るので、このジョブが遅れても前の鍵は使われない。

### 8.6 サーキットブレーカー（状態は DB に持つ。P-ARC-11 ④）

| 項目 | 内容 |
|---|---|
| 状態の置き場所 | `webhook_endpoints.breaker_consecutive_failures`・`breaker_open_until`。複数の `worker-integration` タスクで同じ状態を見る（プロセスのメモリには持たない。`sony/gobreaker` は使わない） |
| 開く条件 | 連続 5 回の失敗で `open_until = now + 60 秒`（本番）。PoC も同じ値で、設定 `WEBHOOK_BREAKER_THRESHOLD`・`WEBHOOK_BREAKER_OPEN_FOR` で変えられる |
| 半開 | `open_until ≤ now` になった後の最初の 1 件が試しになる。送信先ごとのリース（§8.1）で同時実行が 1 なので、試しが重なることはない。成功で閉じ、失敗なら再び 60 秒開く |
| 更新 | 試行の記録と同じトランザクションで更新する。`version` は上げない（事業者の PATCH と楽観ロックで衝突させない） |
| 見送り | open 中の配信は `Postpone(open_until)` にし、attempt_count に数えない。deadline は進む |

## 9. 異常系・境界値・競合

| # | 状況 | 振る舞い |
|---|---|---|
| E1 | 同じ内部イベントが 2 回届く | Inbox で 2 回目を捨てる。Inbox をすり抜けても `source_event_id UNIQUE` で外部イベントは 1 つ |
| E2 | `ResolveTenantCustomerID` が失敗する | ack しない → 再配信 → 5 回で DLQ。DLQ はアラートの対象（Platform） |
| E3 | 送信先の削除・無効化と、配信ループの送信が同時に起きる | 配信ループは送信直前に送信先を読み直す。すでに送信中の 1 件は届いてよい（少なくとも 1 回） |
| E4 | 送信先を disabled → enabled に戻す | disabled の間に abandoned になった配信は戻らない。必要なら手動再送する |
| E5 | 2xx を返したが、タスクが記録する前に落ちた | 30 秒後にリースが切れ、別のタスクが同じ配信を再送する（重複は `id` で除いてもらう） |
| E5a | 2 つのタスクが同じ送信先を同時に取りにいく | `FOR UPDATE SKIP LOCKED` とリースの条件で、片方だけが取る |
| E5b | リースの延長が間に合わず、別のタスクがリースを取った | 延長は `dispatch_lease_owner = 自分` のときだけ成功する。失敗したら、送信中の 1 件の結果を記録して、その送信先の処理をやめる |
| E5c | 1 つの送信先に大量の配信がたまった（大きな事業者の課金日） | 送信先ごとに 1 件ずつ順に送る。応答 200ms なら毎秒 5 件。20 件ごとにリースを外すので、ほかの送信先を待たせない。足りなければ §10 の上限の引き上げ |
| E5d | 送信先が 10 秒のタイムアウトを返し続ける | その送信先だけが遅れる。5 回でブレーカーが開き、以降は HTTP を送らずに見送る。ほかの送信先には影響しない |
| E6 | 手動再送の連打 | 部分ユニーク（pending は 1 つ）で 2 回目は 409 `webhook_delivery_in_progress` |
| E7 | 移行期間中にもう一度ローテーションする | 前の鍵（K1）は即座に無効、K2 が previous、K3 が current。応答と監査に previous_expires_at を返す |
| E8 | `grace_period_hours = 0` | previous を持たず、新しい鍵だけで署名する |
| E9 | 72 時間ちょうど | 予定時刻 ≤ deadline なら試行する。> なら failed |
| E10 | 応答が 10 秒を超える | timeout の失敗。本文は捨てる |
| E11 | 応答の本文が巨大 | 先頭 1 KB だけ読み、残りは読まずに接続を閉じる |
| E12 | 申込の決済失敗で `billing.invoice_payment_failed`（`collection_mode = caller_managed`）が届く | Webhook もメールも作らず、Inbox だけ記録して ack する（P-BIL-5） |
| E13 | 事業者が停止された | 配信は続ける（解約イベントを事業者のシステムに届けるため。P-INT-4）。送信先の管理（書き込み）は停止中はできない（P-IDN-4）が、閲覧と、すでにある送信先への配信は続く |
| E14 | `subscription.past_due` が `billing.invoice_issued` より先に届く | メールは作るが、送信時に InvoiceSummary がなければ一時エラーとして待つ |
| E15 | 退会した契約者宛てのメール | pending は skipped。送信時に GetContact が「マスキング済み」を返した場合も skipped |
| E16 | 監査の書き込み（`api` プロセス）が失敗する | 変更はすでに commit 済みなので応答は 2xx のまま。ERROR ログと指標を出す（PoC で許容。§10 で Outbox 化） |
| E17 | 監査行を UPDATE・DELETE しようとする | 権限エラー、またはトリガーの例外 |
| E18 | event_types に未知の値・重複 | 未知は 400。重複は 1 つにまとめて保存する |
| E19 | URL の長さが 2,048 / 2,049 文字 | 2,048 は受け付け、2,049 は `url_invalid` |
| E20 | 11 個目の送信先 | 409 `webhook_endpoint_limit_exceeded`。削除済みは数えない |

## 10. 拡張ポイント

| 拡張 | PoC での扱い | 将来 |
|---|---|---|
| Reporting（FR-010） | 作らない。EventBridge のルールも SQS も `worker-projection` も作らない（P-PLT-4） | `modules/reporting` と `worker-projection` を追加し、新しいキューで下の表のイベントを購読する。発行側の変更は不要 |
| 外部イベントの種類の追加 | §4.1 の対応表（コードの 1 か所）に行を足すだけ | 例：`invoice.issued`、領収書（NFR-012）の `receipt.created` |
| スキーマの版 | `api_version` を送信先に保存し、ペイロードに入れる | 版ごとに変換関数を持つ |
| 鍵の保管 | `SecretCipher`（AES-GCM、環境変数の鍵） | KMS／Secrets Manager の実装に差し替える。`secret_key_id` で鍵を入れ替える |
| 送信先ごとの処理量 | 送信先ごとの同時実行 1（送信先の行のリース） | 上限を 4 に上げる。リースの列を「枠」の表（`webhook_endpoint_dispatch_slots(endpoint_id, slot 0..3)`）に移し、枠ごとにリースを取る（アーキテクチャレビュー R9 と同じ目的） |
| ブレーカーの状態 | DB（`webhook_endpoints`）に持つ | 負荷が増えたら書き込みを減らす（成功時の 0 リセットは値が 0 でないときだけ書く） |
| 監査の確実性 | `api` プロセスで、応答の前に同期で書く | 変更と同じトランザクションで書く（platform の UoW に載せる）か、Outbox 経由にする。そのときに EventEnvelope の `actor` を使う |
| 監査の改ざん検知・保管 | DB の権限とトリガー | 日次で S3 Object Lock に書き出し、7 年保持。行ごとのハッシュチェーン |
| 監査の閲覧 | ops だけ | console に自分の事業者の監査ログを出す（RLS はすでに対応） |
| メール | プレーンテキスト・日本語だけ | HTML、多言語、バウンス・苦情の処理（SES のイベント）、配信停止 |
| 保持期間 | 削除しない | 配信履歴 30 日、メール 1 年などで削除するジョブ |
| テスト送信 | なし | `POST /v1/console/webhook-endpoints/{id}/test` |

**Reporting が購読するイベント（FR-010）**

| 指標 | 使うイベント | 集計方法 |
|---|---|---|
| MRR | `subscription.started` / `renewed` / `plan_changed` / `canceled`、`catalog.price_version_added` | `subscription_projection` に契約ごとの月額換算（年額は ÷12）を持ち、日次で有効な契約を合計 |
| 有効な契約数 | `subscription.*` のスナップショット（status） | 日次の終了時点で trialing / active / past_due を数える |
| 新規の契約数 | `subscription.started` | 発生日（JST）で当月を数える |
| 解約数 | `subscription.canceled` | reason ごとに当月を数える |
| 支払い遅延の件数 | `subscription.past_due` / `recovered` / `canceled` | 日次の終了時点の past_due を数える |

順番が入れ替わっても壊れないように、`subscription_projection` は aggregate_version が大きいときだけ更新する。

## 11. 受け入れ条件

### 11.1 テストの方針（P-TST-1〜7）

| レベル | 書き方 | この spec で対象にするもの |
|---|---|---|
| unit | Go のテーブル駆動テスト。DB も外部も使わない純粋なロジックだけ。モックは使わない | URL の検証、SSRF の IP 判定、署名、バックオフの計算、状態遷移の表、外部イベントへの変換、テンプレートの選択と変数の組み立て、依存ルール（archtest） |
| integration | Go のテスト。本物の PostgreSQL（testcontainers-go、MiniStack の RDS と同じイメージ）。受信側が必要なものは `webhook-sink` のコンテナを testcontainers で起動する | Inbox の重複排除、送信先のリースと SKIP LOCKED の競合、複数タスクでのブレーカーの共有、落ちたタスクからの引き継ぎ、RLS、追記だけの権限 |
| e2e | Playwright（TypeScript）。API は APIRequestContext。各プロセス（api・worker-billing・worker-integration）、MiniStack、`webhook-sink` を実際に起動する。テストごとに ops・dev の API で専用のテナント・契約者・`sink_id` を作る。DB は `sp_e2e_reader` で読んでよい（P-TST-3） | API の振る舞い、Webhook が届く内容と署名、再送と打ち切り（時刻を進めるものは `clock` の project で直列に流す）、メール、監査 |

- Playwright のテストは 2 種類に分ける（P-TST-8）。課金の流れを通すシナリオ（E2E-001〜022、`e2e/tests/scenarios/`、TL-Orchestration の担当）と、この spec のドメインの API テスト（`e2e/tests/integration/*.spec.ts`、監査は `e2e/tests/audit/*.spec.ts`）。§11.4 の e2e の行には、どちらで確かめるかを書く。
  - 時刻を進めるテストはファイル名の末尾を `.clock.spec.ts` にし（`clock` project がファイル名で拾う。P-TST-9）、進めないテストと同じファイルに混ぜない。
  - `webhook-endpoints.spec.ts`：送信先の作成・検証・鍵・認可・テナント分離
  - `webhook-signature.spec.ts`：署名とローテーション直後の 2 つの署名／`webhook-signature.clock.spec.ts`：移行期間が過ぎた後の署名
  - `webhook-payload.spec.ts`：申込の決済失敗が対象外になること／`webhook-payload.clock.spec.ts`：更新課金のイベントの中身・購読の絞り込み・停止した事業者への配信
  - `webhook-delivery.spec.ts`：配信ループの即時の配信・遅い送信先の隔離／`webhook-delivery.clock.spec.ts`：バックオフ・72 時間の打ち切り・課金が配信を待たないこと
  - `webhook-redeliver.spec.ts`：再試行待ちの配信への再送の拒否／`webhook-redeliver.clock.spec.ts`：打ち切られた配信の手動再送
  - `email.clock.spec.ts`：支払い遅延のメール（§11.3 の A・B）
  - `e2e/tests/audit/audit-records.spec.ts`：監査の記録（parallel）
- フェイク・モック（手書きの Mailer や HTTP クライアントの差し替えなど）は使わない。外部の代わりにするのは `webhook-sink`・MiniStack の SES・dev clock だけ（P-TST-2）。
- 失敗を起こすときは、差し替えではなく**設定と本物の通信**で起こす（例：SES のエンドポイントを閉じたポートに向ける、`webhook-sink` に 500 を返すよう指示する）。
- 乱数のジッターは設定 `WEBHOOK_RETRY_JITTER`（0〜0.1、既定 0.1）で 0 にして、試行の時刻を決まった値にする。

### 11.2 `webhook-sink`（事業者の送信先の代わり。実装と compose への追加は TL-Platform）

integration から見た要件。項目名とポートは TL-Platform の platform.md と合わせる（食い違ったら platform.md を正とし、この節を直す）。

| # | 要件 |
|---|---|
| WS-1 | `POST /hooks/{sink_id}`（`sink_id` の後ろに任意のパスを付けてよい）を受け付ける。`sink_id` はテストが自由に決める文字列で、テストごとに分けることで並列に流せる（P-TST-4） |
| WS-2 | 受け取ったリクエストごとに、`sink_id`・連番・受け取った時刻（sink のコンテナの実時刻、RFC 3339 のミリ秒）・メソッド・パス・**すべてのヘッダー**・**本文のバイト列そのもの**（base64 と UTF-8 の文字列の両方）・返した応答（ステータス・適用した指示）を記録する |
| WS-3 | 受け取った瞬間の、同じ `sink_id` への処理中の要求の数を記録し、`sink_id` ごとの最大値を返せる（送信先ごとの同時実行 1 の確認。AC-INT-038） |
| WS-4 | 指示の API：`PUT /_control/sinks/{sink_id}/behavior` に、上から順に使う規則の列を渡す。規則は `{status, times}`（指定のステータスを n 回返す）、`{delay_ms, status, times}`（遅らせてから返す）、`{hang: true, times}`（応答せずに接続を保ち、送信側のタイムアウトを起こす）、`{close: true, times}`（応答せずに接続を切る）、`{status: 302, location, times}`、`{status: 200, body_bytes, times}`（大きな本文）。規則を使い切った後は 200 と本文 `ok` を返す。`times` を省略したら無制限 |
| WS-5 | 記録の取得：`GET /_control/sinks/{sink_id}/requests`（連番の順。`?after=<連番>&wait=<秒>` を付けると、新しい記録が届くまで最大 wait 秒待って返す long polling。platform.md §10.3 の追加を PdM が承認）、`GET /_control/sinks/{sink_id}/stats`（件数・最大同時数）。指示と記録の消去：`DELETE /_control/sinks/{sink_id}` |
| WS-6 | 回数で指示する（時刻では指示しない）。dev clock を進めても sink の時刻は進まないので、「2 時間ダウン」は「最初の 4 回は 500」のように回数で表す |
| WS-7 | **署名は検証しない**。検証はテスト側（TypeScript の `node:crypto` で HMAC-SHA256 を自前で計算）で行う。Go の `webhooksig` とは別の実装で確かめることになり、§4.3 の仕様どおりに作れることの確認も兼ねる |
| WS-8 | HTTP だけで待ち受ける。compose のホスト名は `webhook-sink`。送信先の URL は `http://webhook-sink:<port>/hooks/{sink_id}`（プロセスをコンテナで動かす CI）または `http://localhost:<公開ポート>/hooks/{sink_id}`（プロセスを `go run` で動かすローカル） |

`webhook-sink` を送信先として登録できるように、ローカルの例外（§3.1.1 U3）は次のとおりにする。
- 設定 `WEBHOOK_INSECURE_ALLOWED_HOSTS`（既定 `localhost,127.0.0.1`。E2E では `webhook-sink` を足す）に挙げたホストだけ、`http://` を許可し、SSRF の拒否リスト（§8.4）の対象からも外す。`APP_ENV=local` 以外で設定したら起動を拒否する（今の `WEBHOOK_ALLOW_INSECURE_LOCALHOST` はこの設定に置き換える）。
- DB の CHECK（付録 A）も `localhost|127.0.0.1|webhook-sink` を許すように直す。

### 11.3 メールの確かめ方

| 方法 | 内容 | 使う条件 |
|---|---|---|
| A. MiniStack の SES から取り出す | MiniStack が、送ったメールを取り出す API を持つか（LocalStack の `GET /_aws/ses` と同じ形のものがあるか）を、PoC の最初の週に確かめる（TL-Platform の MiniStack の検証、設計書の未解決 #9 と同じ枠）。あれば、宛先・件名・本文を取り出し、事業者名・金額・リンクが含まれることを確かめる | API があれば、文面を確かめる AC（AC-INT-022・033）で使う |
| B. DB で確かめる（代わりの方法。A がなくても常に使える） | `sp_e2e_reader` で `integration.email_notifications` を読み、`dedupe_key`（例：`payment_failed:{invoice_id}`）の行が 1 行だけあること、`status = 'sent'`、`provider_message_id` に SES の MessageId が入っていること、`template_key` と `variables` を確かめる | すべてのメールの AC で使う |

- B で文面の中身も確かめられるように、送信が成功したときに、**送信時に解決した個人情報でない変数**（`tenant_name`・`amount`・`payment_method_update_url`・`effective_date`・`reason` など）を `variables` に書き足し、件名を `subject` 列に残す（§3.4・§8.3）。契約者の氏名・メールアドレスはこれまでどおり保存しない。
- メールの宛先が正しいこと（契約者のアドレスに送ったこと）は、A がある場合だけ確かめる。A がない場合は、unit で「宛先に `GetContact` の値を使うこと」を確かめない（モックになるため）。代わりに integration で、本物の customer モジュールと DB を使って、送信直前に組み立てた宛先を確かめる（SES のエンドポイントは MiniStack）。
- `MAIL_MODE`：`ses`（既定。E2E も MiniStack の SES に送る）／`log`（標準出力に書くだけ。SES がない環境での手動確認用。テストでは使わない）。

### 11.4 受け入れ条件

| ID | Given | When | Then | 要件 | レベル | 確かめる場所 |
|---|---|---|---|---|---|---|
| AC-INT-001 | owner がログインしている | `http://example.com/hook` で送信先を作る | 400 `validation_failed`（`url_not_https`）。一覧に出てこない | FR-007 | e2e | `e2e/tests/integration/webhook-endpoints.spec.ts` |
| AC-INT-002 | `APP_ENV=local`、`WEBHOOK_INSECURE_ALLOWED_HOSTS=localhost,webhook-sink` で起動した api | `http://webhook-sink:8080/hooks/x` と `http://other-host/hooks/x` で作る | 前者は 201、後者は 400 `url_not_https` | FR-007 | e2e | `e2e/tests/integration/webhook-endpoints.spec.ts` |
| AC-INT-003 | — | `https://10.0.0.5/`・`https://169.254.169.254/`・`https://[::1]/`・`https://example.com/` を検証する | 前の 3 つは `url_forbidden_host`、最後は通る | FR-007 | unit | — |
| AC-INT-004 | — | 送信先を作り、GET する | 作成の応答にだけ `whsec_` で始まる鍵があり、GET にはない。DB（`sp_e2e_reader`）には平文がなく、暗号文だけがある | FR-007 | e2e | `e2e/tests/integration/webhook-endpoints.spec.ts` |
| AC-INT-005 | 鍵 K1 の送信先（`webhook-sink`） | `grace_period_hours=24` でローテーションし、契約イベントを起こす | sink が受け取った `SP-Signature` に v1 が 2 つあり、テスト側の計算で K2・K1 の両方と一致する | FR-007 | e2e | `e2e/tests/integration/webhook-signature.spec.ts` |
| AC-INT-006 | AC-INT-005 の状態 | Clock を 24 時間進め、契約イベントを起こす | v1 は 1 つで、K2 とだけ一致する | FR-007 | e2e（clock） | `e2e/tests/integration/webhook-signature.clock.spec.ts` |
| AC-INT-007 | 送信の body と時刻 t と鍵 | 署名を作り、検証する | `HMAC-SHA256(key, "t." + body)` の hex と一致する。body を 1 バイト変える・t を 301 秒ずらすと検証に失敗する | NFR-009 | unit | — |
| AC-INT-008 | `subscription.renewed` を購読する送信先 | 更新課金を成功させる | sink が受け取った本文に `evt_` の ID・`customer_id = cus_…`・`version` があり、customer_account_id と内部 event_id がない。本文はスキーマ（§4.2）に合う | FR-022 | e2e（clock） | `e2e/tests/integration/webhook-payload.clock.spec.ts`（`cus_` への置き換えはシナリオ E2E-001 でも確かめる） |
| AC-INT-009 | `invoice.paid` だけを購読する送信先 | 更新課金を成功させる | sink には `invoice.paid` だけが届く | FR-007, FR-022 | e2e（clock） | `e2e/tests/integration/webhook-payload.clock.spec.ts` |
| AC-INT-010 | 内部イベントの種類ごとの入力 | 外部イベントへの変換を行う | `subscription.signup_failed`・`payment_method_changed`・`caller_managed` の `billing.invoice_payment_failed` は「出さない」になり、残り 13 種類は §4.1 の外部 type になる | FR-022 | unit | — |
| AC-INT-011 | integration-webhook の受信処理と本物の DB | 同じ内部イベントを 2 回処理する | 外部イベント 1 つ、配信 1 つ | FR-022 | integration | — |
| AC-INT-012 | sink に「最初の 4 回は 500」を指示した送信先 | 契約イベントを起こし、Clock を 2 時間 36 分まで進める | 5 回目（2 時間 36 分）の試行で succeeded。sink の 5 件の本文と `SP-Event-Id` はすべて同じ | FR-022 | e2e（clock） | `e2e/tests/integration/webhook-delivery.clock.spec.ts`（障害からの回復の流れはシナリオ E2E-015） |
| AC-INT-013 | sink に「常に 500」を指示した送信先 | Clock を 72 時間を超えるまで進める | 配信は failed になり、それ以降 sink に要求が来ない。オーナー宛ての `webhook_delivery_failed` が DB に 1 行だけある（§11.3 の B） | FR-022 | e2e（clock） | `e2e/tests/integration/webhook-delivery.clock.spec.ts` |
| AC-INT-014 | failed の配信 | owner が redeliver する | 202 で manual の新しい配信ができ、sink に同じ `evt_` の ID で届く。元の配信は failed のまま | FR-022, NFR-014 | e2e（clock） | `e2e/tests/integration/webhook-redeliver.clock.spec.ts` |
| AC-INT-015 | sink に「常に 500」を指示し、配信が pending（再試行待ち） | 同じ配信を redeliver する | 409 `webhook_delivery_in_progress` | FR-022 | e2e | `e2e/tests/integration/webhook-redeliver.spec.ts` |
| AC-INT-016 | viewer のトークン | 送信先の作成・redeliver と、一覧・取得を呼ぶ | 前者は 403、後者は 200 | FR-007 | e2e | `e2e/tests/integration/webhook-endpoints.spec.ts` |
| AC-INT-017 | 事業者 A の staff | 事業者 B の送信先・配信を取得・再送する | 404 | NFR-008 | e2e | `e2e/tests/integration/webhook-endpoints.spec.ts`（テナント分離の全体はシナリオ E2E-016） |
| AC-INT-018 | 送信先 X（sink に `hang`）と送信先 Y（正常） | 契約イベントを起こす | Y は X を待たずに届き、X は `timeout` の失敗として記録される | FR-022 | e2e | `e2e/tests/integration/webhook-delivery.spec.ts` |
| AC-INT-019 | 本物の DB、送信先は sink（常に 500）、配信ループのインスタンスを 2 つ | 1 つ目のインスタンスで 5 回失敗させた後、2 つ目のインスタンスで次の配信を処理する | 2 つ目は HTTP を送らず（sink の件数が増えない）、配信を open_until まで見送り、attempt_count は増えない。sink を正常に戻すと、open_until の後の最初の 1 件の成功で `breaker_consecutive_failures = 0` になる | FR-022 | integration | — |
| AC-INT-020 | 接続先の IP の一覧 | SSRF の判定（`Dialer.Control` が使う関数）を行う | 拒否リスト（§8.4）の IP・IPv4 射影アドレスは拒否、公開 IP は許可。`WEBHOOK_INSECURE_ALLOWED_HOSTS` のホストだけは許可 | NFR-009 | unit | — |
| AC-INT-021 | 10 個の送信先すべてに sink の `hang` を指示した事業者 | 更新課金のジョブを実行する | 更新課金のジョブは、Webhook の配信の完了を待たずに終わる（ジョブの完了時刻が、最初の Webhook のタイムアウト 10 秒より前） | FR-022 | e2e（clock） | `e2e/tests/integration/webhook-delivery.clock.spec.ts`（申込が影響を受けないことはシナリオ E2E-015） |
| AC-INT-022 | 更新課金が失敗する設定（StubAdapter） | 契約が支払い遅延になる | `payment_failed:{invoice_id}` のメールが 1 行だけ sent で、`variables` に事業者名・金額・支払方法更新ページのリンクがある（B）。SES から取り出せる場合は、本文にも含まれる（A） | FR-023 | e2e（clock） | `e2e/tests/integration/email.clock.spec.ts`（支払い遅延になる流れはシナリオ E2E-007） |
| AC-INT-023 | 本物の DB、SES のエンドポイントを閉じたポートに向けた worker | 支払い遅延のイベントを処理し、その後 SES のエンドポイントを MiniStack に戻して再送ループを動かす | 1 回目の送信は失敗して pending（Inbox は記録済みで、課金側には何も起きない）、戻した後に sent になる | FR-023 | integration | — |
| AC-INT-024 | integration-email の受信処理と本物の DB | 同じ請求の `subscription.past_due` を、別の event_id で 2 回処理する | `payment_failed` の行は 1 つ（dedupe_key） | FR-023 | integration | — |
| AC-INT-025 | 6 種類のきっかけのイベント | template_key と変数を組み立てる | §5.3 の表どおりになり、`variables` に氏名・メールアドレスが含まれない | FR-023, NFR-015 | unit | — |
| AC-INT-026 | 本物の DB、pending のメールがある契約者 | `customer.withdrawn` を処理する | その契約者の pending の行が skipped になり、再送ループで送られない | FR-023, NFR-015 | integration | — |
| AC-INT-027 | admin がプランの価格を 1,000 円から 1,200 円に改定する | 改定の API が 2xx を返す | 監査（DB）に actor（staff・subject・role）、日時、plan_id、before.amount=1000、after.amount=1200 がある | FR-025 | e2e | `e2e/tests/audit/audit-records.spec.ts` |
| AC-INT-028 | operator が事業者を停止する | 停止の API が 2xx を返す | 監査に `tenant.suspend`、operator、reason がある | FR-025, NFR-014 | e2e | `e2e/tests/audit/audit-records.spec.ts` |
| AC-INT-029 | 本物の DB、監査の行 | ランタイムのロールで UPDATE / DELETE / TRUNCATE する | すべて失敗し、行は変わらない | FR-025 | integration | — |
| AC-INT-030 | 送信先がある | 署名鍵をローテーションする | 監査に `webhook_endpoint.rotate_secret` があり、行のどこにも鍵の平文・暗号文が含まれない | FR-025, NFR-009 | e2e | `e2e/tests/audit/audit-records.spec.ts` |
| AC-INT-031 | viewer のトークン | 送信先を作ろうとする（403） | outcome = denied の監査がある | FR-025 | e2e | `e2e/tests/audit/audit-records.spec.ts` |
| AC-INT-032 | `invoice.payment_failed` を購読する送信先 | 申込の決済を失敗させる（StubAdapter） | sink に `invoice.payment_failed` が届かず、DB にもその請求のメールの行がない | FR-022, FR-023 | e2e | `e2e/tests/integration/webhook-payload.spec.ts` |
| AC-INT-033 | `cancellation_scheduled` の reason が `tenant_suspended` / `customer` | テンプレートを選び、文面を作る | `tenant_suspended` は事業者の停止による終了の文面（取り消しの案内なし）で effective_date を含む。`customer` は通常の文面 | FR-023, FR-026 | unit | — |
| AC-INT-034 | 送信先を持つ事業者 | 運営者が事業者を停止する | sink に、停止に伴う `subscription.cancellation_scheduled`（reason = tenant_suspended）・`subscription.canceled` が届く | FR-022, FR-026 | e2e（clock） | `e2e/tests/integration/webhook-payload.clock.spec.ts`（一括解約の流れはシナリオ E2E-012） |
| AC-INT-035 | リポジトリの import グラフ | archtest を実行する | `internal/platform/**` から `modules/**` への import がない。`cmd/api` が `audit.Module.Write` を監査ミドルウェアに渡している | FR-025 | unit | — |
| AC-INT-036 | 送信先があり、worker-integration が動いている | 契約イベントを起こす | sink に 2 秒以内に届く | FR-022 | e2e | `e2e/tests/integration/webhook-delivery.spec.ts`（シナリオ E2E-001 の W1 への配信でも確かめる） |
| AC-INT-037 | 本物の DB、配信ループのインスタンス A が送信先のリースを持ったまま止まった | Clock を 30 秒進め、インスタンス B を動かす | B がリースを取り、pending の配信を sink に送る | FR-022 | integration | — |
| AC-INT-038 | 本物の DB、同じ送信先への pending が 3 件、送信先は sink（`delay_ms: 500`）、配信ループのインスタンスを 2 つ | 両方を動かす | sink の `stats` の最大同時数が 1 | FR-022 | integration | — |
| AC-INT-039 | 本物の DB、送信先 X に pending が 100 件、Y に 1 件、並列数 1 | 配信ループを動かす | sink の記録で、Y の 1 件は X の 20 件の直後に届く | FR-022 | integration | — |
| AC-INT-040 | 本物の DB、メールの行をコミットしたが送信していない（locked_until が残る） | Clock を 60 秒進めて再送ループを動かす | メールが 1 回だけ送られ、sent になる（SES は MiniStack） | FR-023 | integration | — |

## 12. PdM への確認事項

すべて解決済み。新しい確認事項はない。

| # | 論点 | 決定 | 反映先 |
|---|---|---|---|
| Q-01 | メールの事業者名と、オーナーの宛先の取得 | 解決済み（P-INT-1）：A。Integration → `tenant/contracts`（`TenantInfo.name`、`ListOwnerContacts`）。追加は TL-Identity | §5.3、§8.3 |
| Q-02 | メールにプラン名を入れるか | 解決済み（P-INT-2）：B。PoC では入れない | §5.3 |
| Q-03 | 監査の記録経路 | 解決済み（P-X-2・P-PLT-4）：A。`auditctx` と監査ミドルウェアは TL-Platform、audit は保存・閲覧 API・追記だけの仕組み。書き込みは `cmd/api` から関数として渡す。監査のためのイベント購読はしない | §1、§4.4、§3.6.2 |
| Q-04 | `invoice.*` に `customer_id` を入れるか | 解決済み（P-INT-3）：A。入れない | §4.2 |
| Q-05 | 停止された事業者への配信 | 解決済み（P-INT-4）：A。続ける | §9 E13、AC-INT-034 |
| Q-06 | 支払い失敗メールの回数 | 解決済み（P-INT-5）：A。支払い遅延になったときに請求ごとに 1 回 | §5.3 |
| （依頼） | `cancellation_scheduled` の `reason` | 解決済み（P-INT-6）：入れる（TL-Subscription） | §4.1、§5.3、AC-INT-033 |
| （指示） | 申込の決済失敗の `invoice_payment_failed` | 解決済み（P-BIL-5）：Webhook・メールの対象外 | §4.1、§5.2、§5.3、§9 E12、AC-INT-032 |

## 13. 変更履歴

| 日付 | 変更内容 | 変更者 |
|---|---|---|
| 2026-10-12 | 初版（Draft） | TL-Integration（Claude） |
| 2026-10-12 | PdM レビューを反映（P-INT-1〜6、P-X-2、P-BIL-5、P-PLT-4）。監査は TL-Platform のミドルウェアから `cmd/api` 経由で関数として呼ぶ形に変更し、イベント購読と worker-projection をなくした。caller_managed の支払い失敗を Webhook・メールの対象外にし、停止に伴う解約のメール文面を分けた。§6 の DDL を付録 A に移動。AC-INT-032〜035 を追加。§12 をすべて解決済みにし、ステータスを Review に変更 | TL-Integration（Claude） |
| 2026-10-12 | PdM の最終確認を経て Fixed とする | PdM |
| 2026-10-12 | 実行基盤の Lambda 化を反映（C-05、P-ARC-1・2、アーキテクチャレビュー §3.2.5・§7.3）。§1 に関数の割り当てを追加。§5.2 の手順 5 をコミット直後の FIFO へのキックに変更。§8.1 を SQS FIFO（グループ = 送信先、同時実行 1）＋ 1 分ごとの `webhook-sweep` に変更。§8.3 をメールは購読者の中で送り `email-sweep` で再送する形に変更。§8.6 のブレーカーの状態を DB に移し、§6・付録 A の `webhook_endpoints` に列を追加（配信のリースは `enqueued_until` に改名）。§9・§10 を更新。AC-INT-012・013・014・019・023 を修正し、AC-INT-036〜040 を追加。ステータスを Review に戻す | TL-Integration（Claude） |
| 2026-10-12 | 実行基盤の Lambda 化の反映について PdM の最終確認を経て、再び Fixed とする | PdM |
| 2026-10-12 | 実行基盤を ECS Fargate に戻す決定を反映（P-ARC-10・11）。§1 をプロセスの表に戻した。§5.2 のキック、`webhook-delivery.fifo`・webhook-sender・webhook-sweep・email-sweep を削除。§8.1 を `worker-integration` の常駐の配信ループ（送信先の行のリース＋SKIP LOCKED、送信先ごとの同時実行 1）に変更し、§6・付録 A に `dispatch_lease_*` の列を追加（`enqueued_until` は削除）。§8.3 のメールの再送を常駐ループに変更。§8.6 のブレーカーは DB のまま。§9・§10 を更新し、AC-INT-012〜014・019・023・036〜040 を常駐ループに合わせて修正。ステータスを Review に戻す | TL-Integration（Claude） |
| 2026-10-13 | 実行基盤を ECS Fargate に戻した改訂（P-ARC-10・11、P-X-7）について PdM の最終確認を経て、Fixed とする | PdM |
| 2026-10-13 | テストの方針の変更を反映（P-TST-1〜7）。§11 に、テストの方針、`webhook-sink` の要件（WS-1〜8。実装は TL-Platform）、メールの確かめ方（MiniStack の SES から取り出す方法は要検証。代わりに `email_notifications` の status・dedupe_key・variables を DB で確かめる）を追加。U3 を `WEBHOOK_INSECURE_ALLOWED_HOSTS` に置き換え、`webhook-sink` を許可。`email_notifications` に `subject` を追加し、送信時の変数を書き足すようにした。`RecordingMailer` を削除し `MAIL_MODE` を定義。AC-INT-001〜040 のレベルを unit・integration・e2e で付け直し、フェイク前提の記述をなくした。e2e の行に、確かめる場所（シナリオの番号、または `e2e/tests/integration/*.spec.ts`・`e2e/tests/audit/*.spec.ts`）を付け、時刻を進めるものは `.clock.spec.ts` に分けた（P-TST-8・9）。ステータスを Review に戻す | TL-Integration（Claude） |
| 2026-10-13 | テスト戦略の改訂（Playwright の E2E を中心にし、モックは原則使わない。P-TST-1〜10、P-X-8・9）について PdM の最終確認を経て、Fixed とする | PdM |
| 2026-10-26 | §3.6.1 に `staff.invite`・`staff.resend_invitation` を追加（business-console、P-IMP-22） | PdM |
| 2026-10-26 | §3.6.1 に `mrr_goal.update`・`mrr_goal.delete` を追加（console-dashboard、P-IMP-23） | PdM |
| 2026-10-27 | `webhook_events` に `correlation_id` を追加し、配信の送信（`http.outbound.completed`）のログに元のイベントの相関 ID と `delivery_id` を付ける（AC-PLT-073 の確認で見つかった漏れ。マイグレーション 00003） | TL-Integration（Claude） |

## 付録 A. DDL

integration と audit のテーブル、RLS 以外の権限設定を含む。

```sql
CREATE SCHEMA integration;

CREATE TABLE integration.webhook_endpoints (
  id                          uuid PRIMARY KEY,
  tenant_id                   uuid NOT NULL,
  url                         text NOT NULL CHECK (char_length(url) <= 2048),
  description                 text NOT NULL DEFAULT '' CHECK (char_length(description) <= 256),
  event_types                 text[] NOT NULL CHECK (cardinality(event_types) BETWEEN 1 AND 13),
  status                      text NOT NULL CHECK (status IN ('enabled','disabled')),
  api_version                 text NOT NULL,
  secret_key_id               text NOT NULL,            -- 暗号化に使った鍵の ID（鍵の入れ替えに備える）
  secret_current_ciphertext   bytea NOT NULL,           -- AES-256-GCM（nonce を含む）
  secret_previous_ciphertext  bytea NULL,
  previous_expires_at         timestamptz NULL,
  secret_rotated_at           timestamptz NULL,
  breaker_consecutive_failures int NOT NULL DEFAULT 0 CHECK (breaker_consecutive_failures >= 0),  -- §8.6
  breaker_open_until          timestamptz NULL,
  dispatch_lease_until        timestamptz NULL,         -- 配信ループのリース（§8.1）
  dispatch_lease_owner        text NULL,
  deleted_at                  timestamptz NULL,
  version                     int NOT NULL DEFAULT 1,
  created_at                  timestamptz NOT NULL,
  updated_at                  timestamptz NOT NULL,
  CHECK ((secret_previous_ciphertext IS NULL) = (previous_expires_at IS NULL)),
  -- https のみ。ローカル例外（U3）のための localhost だけは DB でも許す。環境の判定はアプリが行う
  CHECK (url LIKE 'https://%' OR url ~ '^http://(localhost|127\.0\.0\.1|webhook-sink)(:[0-9]{1,5})?(/|$)')
);
CREATE INDEX ON integration.webhook_endpoints (tenant_id) WHERE deleted_at IS NULL;

CREATE TABLE integration.webhook_events (
  id                 text PRIMARY KEY CHECK (id ~ '^evt_[0-9A-HJKMNP-TV-Z]{26}$'),
  tenant_id          uuid NOT NULL,
  source_event_id    uuid NOT NULL UNIQUE,
  event_type         text NOT NULL,
  aggregate_type     text NOT NULL,
  aggregate_id       uuid NOT NULL,
  aggregate_version  int  NOT NULL,
  occurred_at        timestamptz NOT NULL,
  payload            text NOT NULL CHECK (octet_length(payload) <= 65536),
  created_at         timestamptz NOT NULL,
  correlation_id     text NULL CHECK (char_length(correlation_id) <= 128)  -- 元のイベントの相関 ID（配信のログに引き継ぐ。platform.md §8.6）
);
CREATE INDEX ON integration.webhook_events (tenant_id, created_at DESC);

CREATE TABLE integration.webhook_deliveries (
  id                  uuid PRIMARY KEY,
  tenant_id           uuid NOT NULL,
  endpoint_id         uuid NOT NULL REFERENCES integration.webhook_endpoints(id),
  webhook_event_id    text NOT NULL REFERENCES integration.webhook_events(id),
  trigger             text NOT NULL CHECK (trigger IN ('auto','manual')),
  redelivery_of       uuid NULL REFERENCES integration.webhook_deliveries(id),
  status              text NOT NULL CHECK (status IN ('pending','succeeded','failed','abandoned')),
  attempt_count       int  NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  next_attempt_at     timestamptz NULL,
  first_attempted_at  timestamptz NULL,
  last_attempted_at   timestamptz NULL,
  deadline_at         timestamptz NULL,
  last_status_code    int  NULL,
  last_error          text NULL,
  completed_at        timestamptz NULL,
  version             int  NOT NULL DEFAULT 1,
  created_at          timestamptz NOT NULL,
  updated_at          timestamptz NOT NULL,
  CHECK ((status = 'pending') = (next_attempt_at IS NOT NULL)),
  CHECK ((trigger = 'manual') = (redelivery_of IS NOT NULL))
);
CREATE UNIQUE INDEX ON integration.webhook_deliveries (endpoint_id, webhook_event_id) WHERE trigger = 'auto';
CREATE UNIQUE INDEX ON integration.webhook_deliveries (endpoint_id, webhook_event_id) WHERE status = 'pending';
CREATE INDEX ON integration.webhook_deliveries (endpoint_id, next_attempt_at) WHERE status = 'pending';
CREATE INDEX ON integration.webhook_deliveries (tenant_id, created_at DESC, id);
CREATE INDEX ON integration.webhook_deliveries (endpoint_id, created_at DESC, id);

CREATE TABLE integration.webhook_delivery_attempts (
  id                uuid PRIMARY KEY,
  tenant_id         uuid NOT NULL,
  delivery_id       uuid NOT NULL REFERENCES integration.webhook_deliveries(id),
  attempt_no        int  NOT NULL CHECK (attempt_no >= 1),
  requested_at      timestamptz NOT NULL,
  duration_ms       int  NOT NULL,
  status_code       int  NULL,
  error_kind        text NULL CHECK (error_kind IN ('timeout','connection','tls','ssrf_blocked','http_status','response_too_slow')),
  response_excerpt  text NULL CHECK (octet_length(response_excerpt) <= 1024),
  UNIQUE (delivery_id, attempt_no)
);

CREATE TABLE integration.email_notifications (
  id                   uuid PRIMARY KEY,
  tenant_id            uuid NOT NULL,
  template_key         text NOT NULL,
  dedupe_key           text NOT NULL UNIQUE,
  recipient_type       text NOT NULL CHECK (recipient_type IN ('customer','tenant_owner')),
  customer_account_id  uuid NULL,
  subscription_id      uuid NULL,
  invoice_id           uuid NULL,
  endpoint_id          uuid NULL,
  source_event_id      uuid NULL,
  variables            jsonb NOT NULL DEFAULT '{}',   -- 個人情報を入れない
  subject              text NULL,                     -- 送信後に入れる（§11.3）
  status               text NOT NULL CHECK (status IN ('pending','sent','failed','skipped')),
  attempt_count        int  NOT NULL DEFAULT 0,
  next_attempt_at      timestamptz NULL,
  locked_until         timestamptz NULL,
  provider_message_id  text NULL,
  last_error           text NULL,
  sent_at              timestamptz NULL,
  created_at           timestamptz NOT NULL,
  updated_at           timestamptz NOT NULL,
  CHECK ((status = 'pending') = (next_attempt_at IS NOT NULL)),
  CHECK (recipient_type <> 'customer' OR customer_account_id IS NOT NULL)
);
CREATE INDEX ON integration.email_notifications (next_attempt_at) WHERE status = 'pending';
CREATE INDEX ON integration.email_notifications (customer_account_id) WHERE status = 'pending';

CREATE TABLE integration.invoice_summaries (
  invoice_id          uuid PRIMARY KEY,
  tenant_id           uuid NOT NULL,
  subscription_id     uuid NOT NULL,
  total               int  NOT NULL CHECK (total >= 0),
  next_collection_at  timestamptz NULL,
  updated_at          timestamptz NOT NULL
);
-- outbox_events と processed_messages は Platform 共通の定義を integration スキーマにも作る

CREATE SCHEMA audit;
CREATE TABLE audit.audit_records (
  id              uuid PRIMARY KEY,
  occurred_at     timestamptz NOT NULL,
  tenant_id       uuid NULL,
  actor_type      text NOT NULL CHECK (actor_type IN ('operator','staff','tenant_api','system')),
  actor_id        text NOT NULL,
  actor_role      text NOT NULL DEFAULT '',
  action          text NOT NULL,
  target_type     text NOT NULL DEFAULT '',
  target_id       text NOT NULL DEFAULT '',
  before          jsonb NULL,
  after           jsonb NULL,
  outcome         text NOT NULL CHECK (outcome IN ('succeeded','denied')),
  request_id      text NOT NULL,
  correlation_id  text NOT NULL DEFAULT '',
  http_method     text NOT NULL,
  route           text NOT NULL,
  source_ip       inet NULL
);
CREATE INDEX ON audit.audit_records (tenant_id, occurred_at DESC);
CREATE INDEX ON audit.audit_records (action, occurred_at DESC);

-- 追記のみ（1）：ランタイムのロールには INSERT と SELECT だけを与える
REVOKE ALL ON audit.audit_records FROM PUBLIC;
GRANT INSERT, SELECT ON audit.audit_records TO sp_app;        -- ロール名は TL-Platform の定義に合わせる
REVOKE UPDATE, DELETE, TRUNCATE ON audit.audit_records FROM sp_app;
-- 追記のみ（2）：所有者のロールでも更新・削除できないようにする（安全網）
CREATE FUNCTION audit.forbid_mutation() RETURNS trigger LANGUAGE plpgsql AS
$$ BEGIN RAISE EXCEPTION 'audit_records is append-only'; END $$;
CREATE TRIGGER audit_records_append_only BEFORE UPDATE OR DELETE ON audit.audit_records
  FOR EACH ROW EXECUTE FUNCTION audit.forbid_mutation();
CREATE TRIGGER audit_records_no_truncate BEFORE TRUNCATE ON audit.audit_records
  FOR EACH STATEMENT EXECUTE FUNCTION audit.forbid_mutation();
```
