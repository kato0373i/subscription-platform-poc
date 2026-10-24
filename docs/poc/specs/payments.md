# 詳細要件: payments（決済）

| 項目 | 内容 |
|---|---|
| ステータス | **Fixed**（2026-10-13 PdM 承認。テスト戦略：Playwright の E2E が中心） |
| 担当 | TL-Payments |
| 作成日 | 2026-10-12 |
| 上位文書 | `00-poc-charter.md`（C-02・C-04・C-05（取り消し）・§4 FR-012）、`01-contracts-baseline.md`（§5 改訂 v1.1 を含む）、`02-pdm-decisions.md`（P-ARC-10・P-ARC-11 を含む）、設計書 ADR-001・ADR-003・§3・§4・§6.4、要件 FR-012/020/024・NFR-006/007/011 |
| 対象モジュール | `modules/payments` |

---

## 1. 概要と PoC の範囲

**目的**：決済手段（MethodType）× 決済代行（Provider）を、**既存コードを変えずにアダプターの追加だけで増やせる**ことを、動くコードで示す。PoC ではカードだけを、スタブで end-to-end に動かす。

| 対象要件 | 内容 |
|---|---|
| FR-012 | 支払方法の登録・一覧・削除（削除の可否判定は Orchestration、C-04） |
| FR-013（一部） | 申込時の同期決済 `Collect`、結果不明時の扱い |
| FR-017（一部） | 支払い遅延中の即時再決済で呼ばれる `Collect` |
| FR-020 / NFR-006 | 冪等キー・部分ユニークによる二重課金防止 |
| FR-024 | 結果不明の決済の照合ジョブ、日次照合（最小限） |
| NFR-007 | カード番号がサーバーに届かないこと |
| NFR-011 | PaymentProvider ポートとレジストリ |
| NFR-014（一部） | 決済結果の手動確定 ops API |

| 作るもの | 作らないもの |
|---|---|
| PaymentProvider ポート（Go interface 群）とレジストリ | 返金・取消・チャージバック（要件のスコープ外） |
| StubAdapter（PoC の標準、DB 永続のスタブ決済代行） | カード以外の手段のアダプター（拡張点の形だけ用意） |
| FincodeCardAdapter の**骨格**（設定・型・要確認点の TODO。§4.5） | fincode の実通信（要確認点が解消するまで未実装。P-PAY-5） |
| PaymentMethod / PaymentAttempt 集約、API、Inbox、照合ジョブ | 3D セキュア、オーソリと売上確定の分離 |
| 最小申込ページのトークン化部分（スタブのトークナイザー） | 申込ページの見た目の作り込み |

---

## 2. 用語

| 用語 | 意味 |
|---|---|
| MethodType | 決済手段の種類。PoC は `card` のみ。将来 `bank_debit` / `konbini` / `qr` |
| Provider | 決済代行の識別子。`stub` / `fincode`（将来追加） |
| ProviderKey | `(MethodType, Provider)` の組。レジストリのキー |
| ProviderRef | 決済代行側の参照 `{provider, tenant_shop_id?, customer_ref, method_ref}` |
| tenant_shop_id | 事業者ごとの決済代行ショップ ID（fincode のテナントショップ）。共有可能なら NULL |
| payable | 支払い対象への不透明参照 `{type, id}`。PoC は `invoice` のみ。Payments は中身を知らない |
| PaymentAttempt | 1 回の決済の試行 |
| 冪等キー（業務） | 呼び出し元が付ける `idempotency_key`。同じ請求の n 回目の試行を一意にする |
| 冪等キー（代行） | 決済代行に渡すキー。`attempt_id` を使う |
| provider_order_ref | 決済代行側の注文 ID。照会・Webhook の突き合わせに使う |
| 結果不明（unknown） | タイムアウト等で、代行側で課金されたか分からない状態 |
| 非同期確定（pending_async） | 代行が受け付け、結果が後から Webhook / 照会で確定する状態 |
| ProviderEvent | 決済代行から届く通知（Webhook）。`provider_events`（Inbox）に保存する |

---

## 3. ドメインモデル

### 3.1 PaymentMethod（集約ルート、契約者ごと・プラットフォーム全体で持つ）

| フィールド | 型 | 説明 |
|---|---|---|
| id | `ID[PaymentMethod]` | UUIDv7 |
| customerAccountID | `ID[CustomerAccount]` | 所有者（ID 参照のみ） |
| methodType | `MethodType` | `card` |
| status | `PaymentMethodStatus` | `active` / `deleted` |
| display | `MethodDisplay` | 表示用。card は `{brand, last4, exp_month, exp_year}`。手段ごとに型を分ける（判別共用体を JSON で保存） |
| refs | `[]ProviderRef` | 代行側の参照（1 件以上） |
| createdAt / deletedAt | `time.Time` / `*time.Time` | |
| version | `int` | 楽観ロック |

**不変条件**
- refs は 1 件以上。`(provider, tenant_shop_id)` の組は重複しない（NULL も 1 つの値として扱う）。
- `deleted` は終端。deleted の支払方法では `Collect` しない（§9 E-07）。
- display にカード番号・CVC を持たない（last4 のみ）。
- 1 契約者の active な支払方法は最大 **10 件**。
- 削除の可否（使用中か）は判定しない。判定は Orchestration（C-04）。

| 現在 | 操作 | 次 | 発行イベント |
|---|---|---|---|
| (なし) | `NewPaymentMethod(...)`（登録） | active | なし（§12 参照：baseline にイベントなし） |
| active | `AddProviderRef(ref)`（事業者ショップ向け参照の追加） | active | なし |
| active | `Delete(now)` | deleted | なし |
| active | `DeleteOnWithdrawal(now)`（退会：display をマスキング `{brand:"", last4:"****"}`） | deleted | なし |
| deleted | `Delete` / `DeleteOnWithdrawal` | deleted（冪等、何もしない） | なし |

メソッド：`RefFor(provider, tenantShopID) (ProviderRef, bool)` — 事業者ショップ向けの参照を探し、なければ共有参照（tenant_shop_id=NULL）を返す。

### 3.2 PaymentAttempt（集約ルート、事業者ごと）

| フィールド | 型 | 説明 |
|---|---|---|
| id | `ID[PaymentAttempt]` | UUIDv7。代行への冪等キーにも使う |
| tenantID | `ID[Tenant]` | RLS 対象 |
| payable | `PayableRef{Type, ID}` | `invoice` のみ |
| customerAccountID | `ID[CustomerAccount]` | 支払方法の所有者確認に使う |
| paymentMethodID | `ID[PaymentMethod]` | |
| methodType / provider | `MethodType` / `ProviderID` | 試行時点の値を固定で記録 |
| tenantShopID | `*string` | 試行時点の値 |
| amount | `Money` | 1〜1,000,000（D-08 の上限に揃える） |
| idempotencyKey | `string` | 業務の冪等キー（§3.3） |
| collectionSeq | `int` | Billing の回収要求の連番（= attempt_no） |
| requestHash | `string` | `sha256(tenant_id, payable, amount, payment_method_id, customer_account_id)` |
| providerOrderRef | `*string` | 代行側の注文 ID（送信前に採番して保存） |
| status | `AttemptStatus` | 下表 |
| failureCode / retryable | `*FailureCode` / `*bool` | failed のときだけ値を持つ |
| providerRawCode | `*string` | 代行の生のエラーコード（内部用。外部・イベントに出さない） |
| nextAction | `*NextAction` | 将来用（リダイレクト URL・払込票番号）。PoC は常に nil |
| resolution | `*Resolution` | 確定の経路 `sync` / `webhook` / `reconcile` / `manual` |
| resolvedBy / resolveNote | `*string` | manual のとき運営者 ID と理由 |
| checkCount / lastCheckedAt | `int` / `*time.Time` | 照合ジョブの照会回数 |
| requestedAt / finalizedAt | `time.Time` / `*time.Time` | |
| version | `int` | 楽観ロック。イベントの `aggregate_version` にも使う |

**不変条件**
- 1 つの payable に `succeeded` は最大 1 件、**進行中（processing / pending_async / unknown）も最大 1 件**（DB の部分ユニークで担保。§6）。
- `idempotency_key` はテーブル全体で一意。同じキーで requestHash が違えば `ErrIdempotencyKeyReused`。
- `succeeded` / `failed` は終端。終端から他の状態に変えない。
- `failed` のときだけ failureCode と retryable を持つ。

**状態遷移**

| 現在 | 操作（メソッド） | 次 | 発行イベント |
|---|---|---|---|
| (なし) | `NewAttempt(...)` | processing | なし（送信前。外部に出す結果がまだない） |
| processing | `Succeed(at, via)` | succeeded | `payments.payment_succeeded` |
| processing | `Fail(code, retryable, raw, at, via)` | failed | `payments.payment_failed` |
| processing | `AcceptAsync(nextAction, at)` | pending_async | `payments.payment_pending` |
| processing | `MarkUnknown(at)` | unknown | `payments.payment_outcome_unknown` |
| unknown | `Succeed` / `Fail` | succeeded / failed | 同上 |
| unknown | `AcceptAsync` | pending_async | `payments.payment_pending` |
| pending_async | `Succeed` / `Fail` | succeeded / failed | 同上 |
| processing（30 秒超過）/ unknown / pending_async | `ResolveManually(outcome, operator, note, at)` | succeeded / failed | 同上（payload に `resolution: manual`, `resolved_by`） |
| succeeded / failed | 任意の操作 | — | `ErrAttemptFinalized`（冪等再適用は §5.2） |

- 同じ状態への再適用（例：succeeded に対して Succeed）は**エラーにせず何もしない**（Webhook・照合・同期の三重到達を吸収するため）。異なる終端への変更は `ErrAttemptFinalized` とし、照合の不整合として記録する（§8.3）。

### 3.3 冪等キーの決め方

| キー | 形式 | 作る者 | 一意性の意味 |
|---|---|---|---|
| 業務の冪等キー | **`invoice:{invoice_id}:{collection_seq}`**（例：`invoice:0192…:3`。P-X-1・P-X-3） | Billing が `collection_seq` を払い出し（`billing.invoice_collection_due` の payload、caller_managed の初回は 1）、Orchestration がキーを組み立てる | 同じ回収要求。SQS の再配信・ワーカー重複・リース切れの出し直しは同じ番号＝同じキーになり、試行は 1 件に収束する |
| 代行の冪等キー | `attempt_id`（UUID 文字列） | Payments | 1 試行 = 代行への 1 依頼。照合での再照会・再送でも二重にならない |
| provider_order_ref | アダプターの `OrderRefFor(attemptID)` が決める。stub は `stub_<attempt_id>`、fincode は §4.5 要確認 | Payments | 代行側での注文の一意性。照会・Webhook の突き合わせ |

- Payments の `attempt_no` は `collection_seq` と同じ値とし、`payment_attempts.collection_seq` に保存する（キーから取り出して検証する）。
- `collection_seq` はスケジュールどおりの試行・即時の再決済（FR-017）・ops からの再決済の**すべてで増える**ので、前の失敗した試行とは別のキーになる。リトライの段数（`dunning_stage`）とは別物で、Payments は段数を知らない。
- 形式の検証：`^invoice:[0-9a-f-]{36}:[1-9][0-9]{0,4}$`、かつ `invoice_id` が `CollectCmd.Payable.ID` と一致すること。違反は `validation_failed`（プログラムの誤り）。

### 3.4 値オブジェクト

| 型 | 値 |
|---|---|
| `AttemptStatus` | `processing` / `pending_async` / `succeeded` / `failed` / `unknown` |
| `FailureCode`（正規化済み・代行非依存） | 下表 |
| `ConsumerFailureCategory` | `card_expired` / `insufficient_funds` / `generic`（API で契約者に返す分類） |

| FailureCode | retryable | 契約者向け分類 | 主な発生源 |
|---|---|---|---|
| `card_declined` | true | generic | カード会社の拒否 |
| `insufficient_funds` | true | insufficient_funds | 残高・限度額不足 |
| `card_expired` | false | card_expired | 有効期限切れ |
| `invalid_card` | false | generic | 無効なカード・盗難等 |
| `payment_method_unavailable` | true | generic | 支払方法が削除済み・退会済み（§9 E-07） |
| `method_reentry_required` | false | generic | 事業者ショップ向け参照を作れず、再入力が必要 |
| `not_processed` | true | generic | 照会で「代行に注文が存在しない」が確定 |
| `processing_error` | true | generic | 代行側の処理エラー（課金されていないことが確定したもの） |
| `async_expired` | false | generic | 将来：コンビニ払い等の支払期限切れ |
| `manual` | 指定値 | generic | ops の手動確定 |

retryable=false でも、Billing のリトライ方針をどう変えるかは Billing の判断（Payments はイベントで値を伝えるだけ）。

---

## 4. 公開する契約（contracts）とポート

### 4.1 `payments/contracts`（Orchestration から呼ばれる）

主要な型の定義は**付録 A**。要点：`Service` は `Collect` / `RegisterPaymentMethod` / `GetPaymentMethod` / `ListPaymentMethods` / `DeletePaymentMethod` / `EnsureProviderRef` / `ResolveUnknownAttempts` の 7 つ。`CollectCmd` に `TenantShopID`（P-PAY-1）と `CustomerAccountID` を持たせる。

- `TenantID`・`CustomerAccountID` は sharedkernel の型を使う（ベースライン §5.1）。`PaymentMethodID`・`PaymentAttemptID` は `sharedkernel.ID[T]` で contracts に定義する。
- `RegisterPaymentMethod` は baseline の位置引数を Cmd 構造体にまとめ、`MethodType`・`IdempotencyKey` を追加した（引数追加の範囲）。

### 4.2 エラー（`contracts/errors.go`）

| エラー | 意味 | HTTP（Payments の API で返す場合） |
|---|---|---|
| `ErrPaymentMethodNotFound` | 存在しない／他人のもの（区別しない） | 404 `not_found` |
| `ErrPaymentMethodLimitExceeded` | active が 10 件 | 422 `payment_method_limit_exceeded` |
| `ErrInvalidToken` | トークンの形式不正・使用済み・代行が拒否 | 422 `invalid_payment_token` |
| `ErrUnsupportedMethod` | ProviderKey がレジストリにない／無効 | 422 `unsupported_payment_method` |
| `ErrIdempotencyKeyReused` | 同じキーで中身が違う | 409 `idempotency_key_reused` |
| `ErrAttemptInProgress{AttemptID}` | 別キーの進行中の試行がある | （Orchestration が処理） |
| `ErrPayableAlreadyPaid{AttemptID}` | 成功済みの試行がある | （Orchestration が処理） |
| `ErrInvalidAmount` | 1〜1,000,000 の範囲外 | — |
| `ErrProviderUnavailable` | サーキットブレーカー開・レート制限で**送信していない** | 503 `provider_unavailable`（共通コード。P-PAY-4） |
| `ErrAttemptFinalized` | 終端の試行を別の結果にしようとした | 409 `attempt_already_finalized` |
| `ErrAttemptNotResolvable` | 手動確定できない状態（30 秒以内の processing 等） | 409 `attempt_not_resolvable` |

`Collect` の「拒否」「結果不明」は **error ではなく `AttemptResult.Status`** で返す。error は「試行を作れなかった」ときだけ。

### 4.3 PaymentProvider ポート（`internal/adapter/provider/provider.go`）

必須の能力と任意の能力を分け、任意の能力は型アサーションで検出する（新しい手段が不要なメソッドを実装しなくて済むように）。

型の定義は**付録 B**。必須の `PaymentProvider`（`Key` / `Capabilities` / `OrderRefFor` / `Collect` / `GetStatus`）と、任意の `MethodRegistrar` / `TenantRefProvisioner` / `WebhookParser` / `SettlementLister` / `MethodDisabler` からなる。`Outcome.Status` は `succeeded | failed | pending_async | unknown | not_found`。

**アダプターの error の扱い（アプリ層の規則）**

| アダプターの返り値 | アプリ層の解釈 |
|---|---|
| `Outcome{succeeded/failed/pending_async}` | その状態に遷移 |
| `Outcome{unknown}` または `context.DeadlineExceeded` / ネットワークエラー | **unknown**（送ったかもしれない） |
| `errors.Is(err, provider.ErrNotSent)` | 送信前に失敗。試行を `failed(processing_error, retryable=true)` にする |
| `Outcome{not_found}`（GetStatus のみ） | §8.1 の規則で failed(not_processed) に倒す |

### 4.4 レジストリ（`internal/adapter/provider/registry.go`）

| 項目 | 決定 |
|---|---|
| 登録 | `module.go` で `registry.Register(adapter)`。同じ ProviderKey の二重登録は起動時に panic |
| 有効化 | 設定 `PAYMENTS_METHODS=card:stub`（カンマ区切りで `method:provider`）。**1 つの MethodType につき新規登録に使う Provider は 1 つ**。登録済みでも設定にない Provider は「既存の支払方法の決済・照会だけ可能」（代行の移行期間を想定） |
| 新規登録時の選択 | リクエストの `(method_type, provider)` が有効化されていなければ `ErrUnsupportedMethod` |
| 決済時の選択 | 支払方法の `methodType` と、`RefFor` で選んだ ref の `provider` から ProviderKey を作って引く |
| Webhook 時の選択 | パスの provider 名（`/webhooks/{provider}`。fincode と、dev ビルドだけ stub）で `WebhookParser` を引く |
| 起動時検査 | `Capabilities.AsyncSettlement=true` なのに `WebhookParser` 未実装 → 警告ログ（照会での確定のみになる）。`RefSharing=per_tenant` なのに `TenantRefProvisioner` 未実装 → panic |
| 横断機能 | レジストリが返すアダプターを、サーキットブレーカーとレート制限（トークンバケット）のデコレーターで包む。ブレーカー開・レート超過は送信前に `ErrProviderUnavailable`。**ブレーカーの状態は DB（`provider_circuit_breakers`、Provider ごと）に持ち、全タスクで共有する**（P-ARC-11 ④）：連続失敗（unknown・接続エラー）が 5 回で 30 秒開き、その後 1 件だけ試す（half-open）。トークンバケットは**タスクごとのメモリ上の安全網**（Provider ごと、設定値） |
| 流量の制御（P-ARC-7・P-ARC-11 ②） | **全体の流量は、worker-billing の中の回収キュー `orc-collection` の受信の同時実行数（固定）× worker-billing のタスク数の上限で制御する**（更新・リトライの回収。受信ループは Orchestration の担当、値は fincode のレート制限の確認後に決める）。申込（api タスク）からの呼び出しは利用者の操作数で決まり、タスクごとのトークンバケットと共有のブレーカーで守る |
| 実行場所 | `Collect` を呼ぶのは api タスク（申込）と worker-billing タスク（回収）、`GetStatus` は worker-billing タスク（照合ジョブ）。fincode 向けの固定 IP は、これらのタスクを置くサブネットの NAT（EIP）で確保する |

### 4.5 アダプター

#### StubAdapter（`provider/stub`、ProviderKey = card:stub）

- 状態を `payments.stub_charges` / `stub_methods`（§6）に保存する（api と worker-billing の両プロセスから同じ結果を照会できるように）。本番ビルドでは登録しない（ビルドタグ `dev`）。
- Capabilities：`AsyncSettlement=true`、`RefSharing=shared`、`MethodRegistrar`・`SettlementLister`・`MethodDisabler`（何もしない）・`WebhookParser` を実装。Webhook の署名は `X-Stub-Signature: t=<unix>,v1=<HMAC-SHA256(secret, t + "." + body)>`（secret は設定 `STUB_WEBHOOK_SECRET`、許容時刻差 5 分）。dev 用の確定 API（§7.8）は、この形式で署名した通知を `/webhooks/stub` に送り、fincode と同じ経路を通す。
- **トークン形式**：`tok_stub_<scenario>_<last4>_<expMMYY>_<rand8>`
  正規表現 `^tok_stub_([a-z_]+)_(\d{4})_(\d{4})_([a-z0-9]{8})$`。brand は last4 ではなく scenario 表の値。
- 登録時：同じトークンの再使用は `ErrInvalidToken`（同じ Idempotency-Key での再送は同じ結果を返す）。登録すると stub_methods に `scenario` を記録し、`customer_ref = stub_cus_<customer_account_id>`、`method_ref = stub_pm_<rand>` を返す。

**決済結果の決定規則（上から順に最初に当てはまるもの）**

| 優先 | 条件 | 説明 |
|---|---|---|
| 1 | dev の挙動上書き（§7.8 `PUT .../behavior`）が残り回数 > 0 | 指定シナリオで処理し、残り回数を 1 減らす。更新・リトライのテストで「途中からカードが通らなくなる」を作る |
| 2 | 金額が `999,4xx` の特別値 | 下の金額表 |
| 3 | 登録時のトークンの scenario | 下のシナリオ表 |

| scenario（トークン） | 金額の特別値 | Collect の返り値 | 代行側（stub_charges）の真の状態 | GetStatus | brand |
|---|---|---|---|---|---|
| `ok` | — | succeeded | succeeded | succeeded | visa |
| `decline` | 999,401 | failed `card_declined` retryable=true | failed | failed | visa |
| `insufficient` | 999,402 | failed `insufficient_funds` retryable=true | failed | failed | mastercard |
| `expired` | 999,403 | failed `card_expired` retryable=false | failed | failed | visa |
| `invalid` | 999,422 | failed `invalid_card` **retryable=false** | failed | failed | jcb |
| `timeout` | 999,408 | unknown（タイムアウトを模擬） | **succeeded**（課金済み。二重課金防止の検証用） | succeeded | visa |
| `timeout_fail` | 999,409 | unknown | failed `card_declined` | failed | visa |
| `timeout_lost` | 999,410 | unknown | （記録しない＝届かなかった） | not_found | visa |
| `async_ok` | 999,202 | pending_async | pending → dev API で確定 | pending_async（確定後は確定値） | visa |
| `async_fail` | 999,203 | pending_async | pending → dev API で確定 | 同上 | visa |
| `reg_reject` | — | （登録時に `ErrInvalidToken`） | — | — | — |
| `provider_down`（dev の挙動上書きでだけ指定できる） | — | 試行を作らず `ErrProviderUnavailable`（送信前に代行が落ちている状態。P-PAY-4 の経路を E2E で起こす） | （記録しない） | unknown | — |

- 確定待ちの `async_*` は、dev API `settle` で `result` を省略したとき、`async_ok`→succeeded、`async_fail`→failed(card_declined, true) になる。
- 同じ代行冪等キー（attempt_id）での Collect 再送は、記録済みの真の状態を返す（unknown シナリオでも 2 回目は真の状態を返す）。
- 遅延の模擬：設定 `STUB_LATENCY_MS`（既定 0）。タイムアウトは実時間を待たずに即座に unknown を返す（テストを速くするため）。

**ブラウザ側のスタブトークナイザー（`stub-tokenizer.js`）のテストカード**

| カード番号 | 生成するトークンの scenario |
|---|---|
| 4242 4242 4242 4242 | ok |
| 4000 0000 0000 0002 | decline |
| 4000 0000 0000 9995 | insufficient |
| 4000 0000 0000 0069 | expired |
| 4000 0000 0000 0127 | invalid |
| 4000 0000 0000 0408 / 0409 / 0410 | timeout / timeout_fail / timeout_lost |
| 4000 0000 0000 0202 / 0203 | async_ok / async_fail |
| 4000 0000 0000 0400 | reg_reject |
| 上記以外 | ok |

カード番号は**ブラウザ内でだけ**読み、トークンに変換したら入力欄を空にする。サーバーには送らない（§7.9）。

#### FincodeCardAdapter（`provider/fincode`、ProviderKey = card:fincode）

PoC では**骨格だけ**作る（設定の読み込み、型、`Capabilities`、全メソッドは `ErrNotImplemented` を返す）。認証情報が設定されていない場合はレジストリに登録しない（憲章 §4 FR-012）。実装は要確認点の解消後（P-PAY-5）。fincode の公開ドキュメントは JavaScript で描画されるため本作業では内容を取得できず、下の「前提」も TL の事前知識に基づく**未検証**の情報である。

| 区分 | 項目 | 内容 |
|---|---|---|
| 前提（要件・設計で確定） | トークン化 | 契約者のブラウザで fincode の JS がトークンを発行。自社には token と顧客 ID・カード ID だけが届く |
| 前提（確定） | 契約形態 | プラットフォーム型。事業者ごとにテナントショップ（`fincode_shop_id`）がある |
| 前提（確定） | 都度決済 | fincode のサブスクリプション機能は使わない |
| 前提（未検証） | 決済の手順 | 「決済の登録 → 決済の実行」の 2 段階。アダプター内で 1 つの `Collect` にまとめる。登録だけ成功・実行で不明のケースも unknown として扱う |
| 前提（未検証） | テナントの指定 | リクエストヘッダーでテナントショップを指定する方式 |
| 前提（未検証） | テスト環境 | 本番と別のエンドポイントとキーがある |
| 要確認 | 顧客・カードの共有 | 事業者ショップ間で共有できるか → できれば `RefSharing=shared`、できなければ `per_tenant` で `EnsureTenantRef` を実装。再入力が必要なら `method_reentry_required` |
| 要確認 | 冪等キー | 冪等キーのヘッダーの有無・有効期間・同じキーで中身が違う時の挙動 |
| 要確認 | 注文 ID | 自社で注文 ID を指定できるか、長さ・文字種の制約 → `OrderRefFor` の実装 |
| 要確認 | レート制限 | 上限値と超過時のレスポンス → デコレーターの設定値 |
| 要確認 | Webhook の署名 | 署名の方式（共有シークレットの照合か HMAC か）、ヘッダー名、イベントの種類、再送の仕様、イベント ID の有無 |
| 要確認 | 結果の照会 | 注文 ID での照会 API、日次の決済一覧の取得方法（`ListSettlements`） |
| 要確認 | エラーコード | `FailureCode` への対応表（retryable の判定を含む） |
| 要確認 | 最低決済金額・オーソリの有効期限 | 金額の検証範囲 |

---

## 5. 購読するイベントと処理

### 5.1 EventBridge 経由（SQS `payments` → worker-billing の受信ループ）

| event_type | 処理 | 冪等性 |
|---|---|---|
| `customer.withdrawn` | 契約者の active な支払方法をすべて `DeleteOnWithdrawal`（display をマスキング）。各 ref について `MethodDisabler` があればベストエフォートで呼ぶ（失敗はログのみ） | Inbox（`processed_messages`、consumer=`payments.customer_withdrawn`）で重複を除く。さらに deleted への再適用は何もしない |

Payments は billing / subscription のイベントを購読しない（依存の向きを守る）。

### 5.2 決済代行からの通知（Inbox: `provider_events`）

| 処理 | 冪等性 |
|---|---|
| `ProcessProviderEvents`（worker-billing の `payments-provider-events` 受信ループ。§8.2）が、未処理の通知を `provider_order_ref` で試行に対応づけ、Outcome に従って遷移する | `PK(provider, provider_event_id)` で受信時に重複排除。遷移は「同じ状態への再適用は何もしない」で吸収。異なる終端は不整合として `reconciliation_discrepancies` に記録 |

### 5.3 発行するイベント（すべて Outbox、試行の更新と同じトランザクション）

`source = sp.payments`、`aggregate_type = payment_attempt`、`aggregate_id = attempt_id`、`aggregate_version = version`。

| event_type | タイミング | payload |
|---|---|---|
| `payments.payment_succeeded` | succeeded になった | attempt_id, payable{type,id}, amount, payment_method_id, customer_account_id, resolution, resolved_by? |
| `payments.payment_failed` | failed になった | 上記 ＋ failure_code, retryable |
| `payments.payment_pending` | pending_async になった | 上記 ＋ next_action?（PoC は null） |
| `payments.payment_outcome_unknown` | unknown になった | attempt_id, payable, amount, payment_method_id |

- **結果は必ずイベントでも出す**：同期で `AttemptResult` を返す場合も、返す前に Outbox への記録をコミットしている（C-02 の二重到達は Billing が attempt_id で吸収）。

---

## 6. 永続化（スキーマ `payments`）

DDL の全文は**付録 C**。要点：

| テーブル | 主な制約・インデックス |
|---|---|
| payment_methods | `method_type CHECK IN ('card')`、`status CHECK IN ('active','deleted')`、`(customer_account_id, created_at DESC, id) WHERE status='active'` |
| payment_method_provider_refs | `UNIQUE(payment_method_id, provider, COALESCE(tenant_shop_id,''))` |
| payment_attempts | `idempotency_key UNIQUE`、`provider_order_ref UNIQUE`、`collection_seq`、金額 `1〜1,000,000`、**部分ユニーク** `uq_attempt_succeeded`（status='succeeded'）・`uq_attempt_inflight`（processing / pending_async / unknown）、照合用 `ix_attempt_unresolved` |
| provider_events | `PK(provider, provider_event_id)`、`(next_attempt_at) WHERE status='pending'` |
| reconciliation_discrepancies | `UNIQUE(business_date, provider, kind, provider_order_ref)`（再実行で冪等） |
| provider_circuit_breakers | `PK(provider)`、`consecutive_failures`、`open_until`（全タスクで共有するブレーカーの状態） |
| stub_methods / stub_charges | dev ビルドのマイグレーションだけで作る |
| outbox_events / processed_messages | Platform の定義どおり |

**RLS**

| テーブル | ポリシー |
|---|---|
| payment_attempts | `tenant_id = current_setting('app.tenant_id')::uuid`（標準）。照合ジョブ・Webhook 処理・ops API は運営者用のバイパスロール |
| payment_methods / provider_refs | 事業者のデータではないので tenant の RLS をかけない。`customer_account_id = current_setting('app.customer_account_id', true)::uuid` のポリシーで consumer API を守る。Collect 時（api / worker-billing）はアプリ層で `customer_account_id` 一致を検証する（Platform の RLS 設定の仕組みに合わせる。§12 調整事項） |
| provider_events / reconciliation_discrepancies / provider_circuit_breakers / stub_* | RLS なし。運営者・ワーカーのロールだけに権限 |

---

## 7. API

### 7.1 `GET /v1/checkout/payment-config`（認証なし）
申込ページがトークナイザーを選ぶために使う。
レスポンス：`{ "methods": [{ "method_type": "card", "provider": "stub" | "fincode", "public_key": string|null }] }`（設定 `PAYMENTS_METHODS` の有効なもの）。

### 7.2 `POST /v1/me/payment-methods`（consumer）
| 項目 | 内容 |
|---|---|
| ヘッダー | `Idempotency-Key` 必須（platform/httpx の共通処理。24 時間、同じキーで同じ本文なら保存済みレスポンス） |
| リクエスト | `{ "method_type": "card", "provider": "stub", "token": "tok_stub_ok_4242_1228_ab12cd34" }`。`additionalProperties: false`（カード番号の項目を受け付けない） |
| レスポンス 201 | `{ id, method_type, status, display:{brand,last4,exp_month,exp_year}, expired, created_at }` |
| エラー | 422 `validation_failed` / `invalid_payment_token` / `unsupported_payment_method` / `payment_method_limit_exceeded`、409 `idempotency_key_reused`、503 `provider_unavailable` |

### 7.3 `GET /v1/me/payment-methods`（consumer）
`limit`（既定 20、上限 100）、`starting_after`。active のみ、`created_at DESC`。レスポンス `{data:[PaymentMethod], has_more, next_cursor}`。

### 7.4 `DELETE /v1/me/payment-methods/{id}`（consumer、**ハンドラは Orchestration**）
Orchestration が `IsPaymentMethodInUse` を確認し、使われていなければ `payments.DeletePaymentMethod` を呼ぶ。Payments の責務：他人・存在しない → `ErrPaymentMethodNotFound`（404）、削除済み → 成功（204、冪等）。

### 7.5 `GET /v1/ops/payment-attempts/{id}`（operator）
試行の全項目（`provider_raw_code` を含む）。404 `not_found`。

### 7.6 `POST /v1/ops/payment-attempts/{id}/resolve`（operator）
| 項目 | 内容 |
|---|---|
| リクエスト | `{ "outcome": "succeeded"｜"failed", "failure_code": "card_declined"（failed 時必須）, "retryable": bool（failed 時必須）, "note": string（必須、1〜500 文字）, "force": bool（任意、既定 false） }` |
| 前提 | 状態が unknown / pending_async、または processing で `requested_at` から 30 秒超 |
| 処理 | 確定の前に `GetStatus` を 1 回照会し、代行の結果と指定が食い違えば 409 `outcome_mismatch`（`force: true` で上書き可、note に理由必須）。代行が照会不能（unknown）なら指定どおり確定 |
| レスポンス 200 | 試行（`resolution: manual`, `resolved_by`） |
| エラー | 404 `not_found`、409 `attempt_already_finalized`（同じ結果なら 200 で冪等）、409 `attempt_not_resolvable`、409 `payable_already_paid`（成功済みの別試行がある）、409 `outcome_mismatch`、422 `validation_failed` |
| 監査 | 監査ミドルウェアと `internal/platform/auditctx` の注釈（変更前後の status・outcome・note）で記録する（P-X-2）。発行するイベントの `actor` は `operator` |

### 7.7 `GET /v1/ops/reconciliation-reports`（operator）
`business_date`、`status`、カーソル。`reconciliation_discrepancies` の一覧。

### 7.8 dev 専用（ビルドタグ `dev`、本番ビルドに含めない）
| エンドポイント | 内容 |
|---|---|
| `POST /v1/dev/payments/stub/charges/{provider_order_ref}/settle` `{ "result": "succeeded"｜"failed"?, "failure_code"? }` | pending の stub_charge を確定し、**署名した通知を `POST /webhooks/stub` に送る**（§7.9 と同じ経路で Inbox → SQS → 受信ループ）。Webhook が 200 を返したら 202。pending でなければ 409 |
| `POST /v1/dev/payments/stub/payables/invoice/{invoice_id}/settle`（本文は上と同じ） | 請求の進行中の試行の `provider_order_ref` を引いて、上と同じ処理をする（E2E の fixture 用。§11.2）。進行中の試行がなければ 404 |
| `PUT /v1/dev/payments/stub/payment-methods/{payment_method_id}/behavior` `{ "scenario": "decline", "remaining": 1 }` | §4.5 の優先 1 の上書き。`remaining: 0` で解除 |

### 7.9 `POST /webhooks/{provider}`（fincode・stub。認証の主体なし、署名で検証）
| 手順 | 内容 |
|---|---|
| 1 | その provider のアダプターが登録されていない、または `WebhookParser` を実装していなければ 404（`/webhooks/stub` は dev ビルドだけ） |
| 2 | `WebhookParser.ParseWebhook` で署名を検証。失敗 → 401（本文なし）・警告ログ。本文は 64KB まで |
| 3 | `provider_events` に `ON CONFLICT DO NOTHING` で保存してコミット |
| 4 | **コミットの直後に SQS `payments-provider-events` に `{provider, provider_event_id}` を送る**（新しく保存できた場合だけ）。送信に失敗しても 200 を返す（Inbox に残るので、§8.2 の掃除が送り直す） |
| 5 | 200 を返す（重複でも 200）。処理は worker-billing の受信ループ（§8.2）で非同期に行い、HTTP の中で試行を更新しない |
| PoC | fincode はアダプターが未実装のため手順 2 は `ErrNotImplemented` → 501。経路の検証は `/webhooks/stub`（§7.8 の settle から送る）で行う |

### 7.10 最小申込ページ（`GET /checkout?plan_id=…`、HTML 1 枚）のトークン化の流れ
1. `GET /v1/checkout/payment-config` で provider を得る。stub → `stub-tokenizer.js`、fincode → fincode の JS（将来）。
2. カード番号・有効期限の入力欄は `<form>` の外に置き、`name` 属性を付けない。トークン化したら入力欄を空にする。
3. `POST /v1/me/payment-methods`（token のみ）→ 返った `id` で `POST /v1/me/subscriptions`（Orchestration）。

**NFR-007 の確認方法**：Playwright のブラウザテストで、自社への全リクエスト・ブラウザの保存領域・DB・ログにカード番号が現れないことを確かめる（§11.3、AC-PAY-007）。API が `card_number` 等を受け付けないことは AC-PAY-006。

---

## 8. ジョブ・非同期処理

実行基盤は ECS Fargate（ADR-001、P-ARC-10）。payments の非同期処理はすべて **worker-billing タスク**の中で動く。スケジュールジョブは EventBridge Scheduler → SQS `scheduler` → worker-billing の Runner（`job_runs` で重複を除く）。ローカルでは各プロセスを普通に起動し（go run または compose）、`/v1/dev/jobs/{job}/run` で手動実行できる。

| 名前 | 種類・起動 | 同時実行など | 処理 |
|---|---|---|---|
| `payments.resolve_unknown` | スケジュールジョブ、1 分ごと（PoC 設定。本番想定 5 分） | 1 回 200 件まで（残りは次の回） | §8.1 |
| `payments.provider_events_sweep` | スケジュールジョブ、1 分ごと | — | §8.2 の掃除 |
| `payments-provider-events` の受信ループ | SQS 常駐受信（long polling） | 同時実行 5、可視性タイムアウト 60 秒 | §8.2 |
| `payments` の受信ループ | SQS 常駐受信 | 同時実行 2 | §5.1 |
| `payments.reconcile_daily` | スケジュールジョブ、毎日 03:00 JST（前日分） | Platform 共通の `platform.job_cursors` を `Run.Cursors` で使う（P-X-7。§8.3）。途中で落ちても、再実行すると続きから再開する | §8.3 |

### 8.1 ResolveUnknownAttempts(olderThan)

| 項目 | 決定 |
|---|---|
| 対象 | `unknown` で `requested_at < now - olderThan`（既定 30 秒）／`processing` で 30 秒超（タスクの異常終了・停止による取り残し）／`pending_async` で 1 時間超かつ最終照会から 1 時間超（Webhook の取りこぼしの保険） |
| 取得 | `ix_attempt_unresolved` から最大 200 件、`FOR UPDATE SKIP LOCKED`、1 件ずつ別トランザクション（前の回の実行と重なっても同じ試行を二重に照会しない） |
| 照会 | `GetStatus(provider_order_ref)`。`check_count += 1` |
| 結果 | succeeded / failed / pending_async → 遷移（`resolution=reconcile`）とイベント。unknown → そのまま |
| not_found | `requested_at` から 10 分超なら `failed(not_processed, retryable=true)`。10 分以内は保留（代行側の反映遅延を考慮） |
| 長期化 | unknown が 24 時間を超えても自動では確定せず、ERROR ログ `payments.unknown_stale`（アラート）を出して手動確定（§7.6）に回す（P-PAY-2） |
| 送信 | ブレーカー開（DB の共有状態）なら照会しない（次の回に回す） |

### 8.2 ProviderEvents の処理（受信ループ＋掃除）

Webhook は Inbox に保存してから SQS に送る形（§7.9）を維持し、worker-billing の常駐の受信ループで処理する。ポーリングには戻さない（通常の遅延が短く、処理の経路が 1 つで済むため）。

**受信ループ `payments-provider-events`**（メッセージ = `{provider, provider_event_id}`）

| 手順 | 内容 |
|---|---|
| 1 | トランザクションで `provider_events` の行を `FOR UPDATE SKIP LOCKED` で取る。ロック中・`status <> 'pending'` なら何もせずメッセージを削除（重複配信の吸収） |
| 2 | `provider_order_ref` で試行を探す。見つからない → `ignored`（`orphan_provider_charge` の候補として照合に任せる） |
| 3 | Outcome を適用。遷移成功・同じ状態 → `processed`／異なる終端 → `processed` ＋ `conflicting_final_outcome` を記録。試行の更新・Outbox・`provider_events` の更新は同じトランザクション。中継は worker-billing の Outbox 中継ループが行う（P-ARC-11） |
| 4 | 業務エラー（試行の楽観ロックの衝突・ロック待ちのタイムアウトなど）→ `attempts+1`、`next_attempt_at` を指数バックオフにしてコミットし、メッセージは削除（再試行は掃除に任せる）。10 回で `failed`・ERROR ログ |
| 5 | DB に書けないエラー → メッセージを削除せず、可視性タイムアウト後に SQS が再配信する（上限を超えたら DLQ） |

**掃除 `payments.provider_events_sweep`**：`status='pending' AND next_attempt_at <= now AND received_at < now - 1 分` を最大 500 件取り出し、SQS に送り直す（処理の経路は受信ループの 1 つだけにする）。キックの送信失敗と、手順 4 のバックオフ後の再試行を拾う。

### 8.3 日次照合（最小限、FR-024）
スケジュールジョブ（worker-billing）として、`SettlementLister` を実装したアダプターごとに、前日（JST）の決済一覧と、同じ期間の `payment_attempts` を `provider_order_ref` で突き合わせる。PoC で対象になるのは StubAdapter（`stub_charges`）だけ。

| 検出 | 条件 |
|---|---|
| `provider_succeeded_local_not` | 代行は成功、自社の試行は succeeded 以外（FR-024 シナリオ 2） |
| `local_succeeded_provider_not` | 自社は succeeded、代行は失敗・存在しない |
| `amount_mismatch` | 金額が違う |
| `orphan_provider_charge` | 代行に記録があり、自社に試行がない |

- 検出したら `reconciliation_discrepancies` に保存（UNIQUE で再実行は冪等）し、ERROR ログ `payments.reconciliation_discrepancy`（PoC のアラート）。自動修正はしない。
- Payments は請求を知らないため、「請求が未払い」の判定は試行の状態で代用する（試行の結果は必ずイベントで Billing に届くので、試行と請求のずれは Billing 側の責務）。

**再開の仕組み（P-X-7）**：payments は独自のカーソル表を持たず、Platform 共通の `platform.job_cursors` をジョブの `Run.Cursors`（`Load` / `Save` / `Complete`）で使う。

| 項目 | 決定 |
|---|---|
| scope_key | `"<業務日付 YYYY-MM-DD>:<provider>"`（例：`2026-10-12:stub`）。`SettlementLister` を実装した Provider ごとに 1 つ |
| cursor（jsonb） | `{"provider_cursor": "<代行のページカーソル>", "pages": <処理済みページ数>}` |
| 書き込み | 1 ページ分の `reconciliation_discrepancies` の保存と `Run.Cursors.Save` を**同じトランザクション**で行う（カーソルと検出結果がずれない） |
| 完了 | 最後のページの後に `Complete`。完了済みの scope_key は再実行しても何もしない |
| 開始 | `Load` が NULL なら先頭から。値があればその続きから |

---

## 9. 異常系・境界値・競合

| # | 状況 | 振る舞い |
|---|---|---|
| E-01 | 同じ冪等キー・同じ内容で Collect が 2 回（ダブルクリック・SQS 再配信） | 2 回目は INSERT が UNIQUE 違反 → 既存の試行を返す（`Replayed=true`）。代行には 1 回だけ送る。既存が processing なら processing を返す |
| E-02 | 同じ冪等キー・違う内容 | `ErrIdempotencyKeyReused` |
| E-03 | 同じ請求に 2 ワーカーが別キーで同時に Collect（FR-020） | 片方だけ `uq_attempt_inflight` を通る。他方は `ErrAttemptInProgress{既存 ID}` で、代行に送らない |
| E-04 | 成功済みの請求に Collect | `ErrPayableAlreadyPaid{成功した試行 ID}`。代行に送らない |
| E-05 | unknown の請求に別キーで Collect（リトライ時刻になった等） | `ErrAttemptInProgress`。結果が確定するまで新しい試行は作らない（二重課金防止を優先） |
| E-06 | 代行呼び出し後、結果の保存（Tx2）に失敗 | 試行は processing のまま。呼び出し元には `unknown` を返す。照合ジョブが代行冪等キーで照会して確定する |
| E-07 | 削除済み・退会済みの支払方法で Collect | 試行を作り、代行に送らず `failed(payment_method_unavailable, retryable=true)` にしてイベントを出す（通常のリトライの流れに乗る。P-PAY-3・P-BIL-2） |
| E-08 | 他人の支払方法で Collect | `ErrPaymentMethodNotFound`（試行を作らない。呼び出し元の誤り） |
| E-09 | 金額 0 円・負・1,000,001 円以上 | `ErrInvalidAmount`（0 円の請求は Billing がすぐ paid にするので来ない） |
| E-10 | ブレーカー開・レート超過 | 試行を作らず `ErrProviderUnavailable`（`provider_unavailable`）。リトライ回数を消費しない。更新では Billing のリースが切れたら同じ `collection_seq` で回収要求が出し直される。申込では 503（P-PAY-4） |
| E-11 | 事業者ショップ向けの参照が作れない（per_tenant の代行で再入力が必要） | `failed(method_reentry_required, retryable=false)` |
| E-12 | Webhook と照合ジョブと同期応答が同時に同じ結果を適用 | 楽観ロック（version）で 1 つだけ成功。負けた側は再読込して「同じ状態」なら何もしない |
| E-13 | Webhook が succeeded、照会が failed のように食い違う | 先に適用された結果を残し、`conflicting_final_outcome` を記録。手動で確認 |
| E-14 | 削除と決済の競合（Orchestration が使用中でないと判定した直後に更新の Collect が来る） | Collect は Tx1 で支払方法の status を読む。deleted なら E-07。active なら決済する（削除は決済の後に成立）|
| E-15 | active が 10 件ある状態で登録 | `ErrPaymentMethodLimitExceeded` |
| E-16 | 有効期限切れのカード（display 上） | 登録は可能（代行が拒否しない限り）。一覧に `expired: true`。決済の可否は代行の結果に従う |
| E-17 | pending_async が長期間続く | PoC では期限を設けない（照会で確定するまで続く）。将来の手段ではアダプターが `async_expired` を返す |
| E-18 | 手動確定と照合ジョブの競合 | E-12 と同じ。手動側が負けて結果が異なれば 409 `attempt_already_finalized` |

---

## 10. 拡張ポイント

### 10.1 決済手段を追加するときの作業（例と、変えない場所）

| 作業 | 場所 | 口座振替 `bank_debit` | コンビニ払い `konbini` | QR コード決済 `qr` |
|---|---|---|---|---|
| 1. MethodType の定数 | `contracts/types.go` | 追加 | 追加 | 追加 |
| 2. CHECK 制約を広げる | `db/migrations`（`payment_methods.method_type`） | 追加 | 追加 | 追加 |
| 3. display の型 | `internal/domain/display.go`（判別共用体に 1 型追加） | 銀行名・口座下 4 桁 | なし（連絡先は持たない） | ウォレット名 |
| 4. アダプター | `internal/adapter/provider/<provider>_<method>/` | `RegisterMethod` が `NextAction{redirect}`（口座登録の外部画面）を返す。`Collect` は `pending_async`、確定は Webhook | `RequiresStoredMethod=false`。`Collect` が払込票を発行し `pending_async` ＋ `NextAction{konbini_slip}`。期限切れは `async_expired` | `Collect` が `NextAction{redirect / qr}` 付きの `pending_async`。Webhook で確定 |
| 5. Webhook | `WebhookParser` を実装し、`/webhooks/{provider}` を有効化 | 必要 | 必要 | 必要 |
| 6. 失敗コードの対応表 | アダプター内 | 振替不能 → `card_declined` 相当の新コード追加 | `async_expired` | `async_expired` |
| 7. 登録と有効化 | `module.go`（`registry.Register`）、設定 `PAYMENTS_METHODS` | | | |
| 8. 画面 | 申込ページの手段選択（Payments の外） | | | |

**変えない場所**：`PaymentAttempt` の状態遷移・部分ユニーク・冪等キー・イベントの型、`contracts.Service`、Billing・Subscription・Orchestration（`pending_async` は請求の「決済処理中」として既に扱う。ADR-003・§6.4）。

### 10.2 その他の拡張点

| 拡張 | 用意するもの（PoC） | PoC での扱い |
|---|---|---|
| fincode 以外のカード代行 | ProviderKey に Provider を持つ。ref は provider ごと。設定で新規登録先だけ切り替え | 実装しない |
| 外部画面への誘導（NextAction） | `next_action jsonb`、`payment_pending` の payload に `next_action` | 常に null。契約者への通知経路（Integration）は将来 |
| payable の種類追加（一回払い等） | `PayableType`、CHECK 制約 | `invoice` のみ |
| 返金 | 新しい集約 `Refund` を想定（PaymentAttempt を変えない） | 作らない |
| 照合の自動修正 | `reconciliation_discrepancies.status` | 検出・通知のみ |

---

## 11. 受け入れ条件

**テストのレベル**（P-TST-1〜7）

| レベル | 書き方 | payments で対象にするもの |
|---|---|---|
| unit | Go のテーブル駆動テスト。DB も外部も使わない純粋なロジック | PaymentAttempt の状態遷移表、冪等キーの組み立てと検証、FailureCode と retryable・契約者向け分類の対応、StubAdapter の結果決定規則（トークンの解析・優先順位）、スタブの Webhook 署名、レジストリ |
| integration | Go ＋ 本物の PostgreSQL（testcontainers-go）。E2E で狙って起こせないものだけ | 部分ユニークと並行の Collect、RLS、Outbox・Inbox・掃除、SKIP LOCKED、照合の再開、共有のブレーカー |
| e2e | Playwright（`e2e/`）。API は APIRequestContext、申込ページはブラウザ。DB の確認は `sp_e2e_reader` で、API から見えない不変条件にだけ使う。**確認先**（P-TST-8）は、課金の流れを通すシナリオ（`e2e/tests/scenarios/`、E2E-001〜022 の番号）か、payments の API テスト（`e2e/tests/payments/*.spec.ts`。申込ページのブラウザテストを含む）のどちらかで、表のレベル欄に「：」の後ろで示す | 支払方法の API、申込・更新・照合・手動確定の通し、NFR-007 |

- **モックやフェイクは使わない**。fincode の代わりの StubAdapter は、シナリオで結果を切り替える**本物のアダプター実装**として扱う（P-TST-2 の例外 ①）。integration でも StubAdapter と本物の DB をそのまま使う。
- e2e のうち時刻を進めるもの（表の「clock」）は Playwright の project `clock`（workers=1）で直列に、それ以外は `parallel` で流す（P-TST-4）。時刻を進める spec のファイル名は末尾を `.clock.spec.ts` にし、時刻を進めるテストと進めないテストを同じファイルに混ぜない（P-TST-9。例：手動確定は `ops-resolve.clock.spec.ts`（AC-PAY-023）と `ops-resolve.spec.ts`（AC-PAY-024）に分ける）。

### 11.1 受け入れ条件の一覧

| ID | Given | When | Then | 要件 | レベル |
|---|---|---|---|---|---|
| AC-PAY-001 | fixture の契約者、`stub.token('ok')` | `POST /v1/me/payment-methods` | 201。display は `visa / 4242 / 12 / 2028` | FR-012 | e2e：`payments/payment-methods.spec.ts` |
| AC-PAY-002 | AC-PAY-001 と同じ Idempotency-Key・同じ本文 | 再送 | 同じ id の 201。DB（reader）で支払方法は 1 件 | FR-012, NFR-006 | e2e：`payments/payment-methods.spec.ts` |
| AC-PAY-003 | 契約者 X の支払方法 M | 契約者 Y のトークンで `GET /v1/me/payment-methods`、M を指定した申込 | 一覧に出ない。申込は 404 | NFR-008 | e2e：`payments/payment-methods-isolation.spec.ts` |
| AC-PAY-004 | 事業者 A の申込で登録した M | 同じ契約者が事業者 B のプランに M で申し込む | カードの再入力なしで契約が active | FR-012 | e2e：`payments/card-reuse.spec.ts` |
| AC-PAY-005 | active が 10 件 | 11 件目を登録 | 422 `payment_method_limit_exceeded` | FR-012 | e2e：`payments/payment-methods.spec.ts` |
| AC-PAY-006 | `card_number` を含む本文 | `POST /v1/me/payment-methods` | 422 `validation_failed` | NFR-007 | e2e：`payments/payment-methods.spec.ts` |
| AC-PAY-007 | 申込ページ（stub）でテストカードを入力 | ブラウザで申込を完了 | §11.3 の検査をすべて満たす | NFR-007 | e2e：`payments/checkout-pan.spec.ts` |
| AC-PAY-008 | scenario `ok` の M、980 円のプラン | 申し込む | 201・契約 active。DB（reader）で試行 1 件 succeeded、`payments.payment_succeeded` が Outbox で送信済み | FR-013 | e2e：シナリオ E2E-001 |
| AC-PAY-009 | scenario `decline` の M | 申し込む | 422 `payment_declined`（分類 `generic`）。DB で試行は failed `card_declined` retryable=true | FR-013, FR-021 | e2e：`payments/signup-declined.spec.ts` |
| AC-PAY-010 | StubAdapter の結果決定規則の表（§4.5）の全行 | 規則関数に scenario・金額・上書きを与える | 表どおりの Outcome（例：`invalid` → `invalid_card`、retryable=false）。優先順位（上書き > 金額 > トークン）も表どおり | FR-021 | unit |
| AC-PAY-011 | 同じキーで Collect を 10 並列 | 同時実行 | 試行 1 件、stub_charges 1 件、全員が同じ attempt_id を受け取る | FR-020, NFR-006 | integration |
| AC-PAY-012 | 同じ請求に別キーで 2 つの goroutine が同時に Collect | 同時実行 | 片方が `ErrAttemptInProgress`、stub_charges は 1 件 | FR-020 | integration |
| AC-PAY-013 | 請求 I に succeeded の試行がある | 新しいキーで Collect | `ErrPayableAlreadyPaid`、stub_charges は増えない | FR-020 | integration |
| AC-PAY-014 | 同じキーで金額だけ違う | Collect | `ErrIdempotencyKeyReused`、試行は増えない | NFR-006 | integration |
| AC-PAY-033 | 支払い遅延中の契約（`invoice:I:1` が failed） | 契約者が支払方法を `ok` のカードに変える | 契約が active に戻る。DB で `invoice:I:1` failed・`invoice:I:2` succeeded の 2 件、stub_charges の成功は 1 件 | FR-017, FR-020 | e2e（clock）：シナリオ E2E-009 |
| AC-PAY-034 | 冪等キーの組み立て・検証の表（正しい形式、invoice_id 不一致、連番 0、長すぎる） | 検証関数 | 正しい形式だけ通り、他は `validation_failed` | NFR-006 | unit |
| AC-PAY-015 | scenario `timeout`（代行側は課金済み）の M | 申し込む → `payments.resolve_unknown` を実行 | 申込は 202。ジョブの後、契約が active。DB で試行は succeeded（resolution=reconcile）、stub_charges は 1 件だけ | FR-013, FR-024, FR-020 | e2e（clock）：シナリオ E2E-011 |
| AC-PAY-016 | scenario `timeout_fail` | 申し込む → `resolve_unknown` | 申込は 202、ジョブの後に契約は signup_failed。試行は failed `card_declined` | FR-024 | e2e（clock）：`payments/unknown-resolution.clock.spec.ts` |
| AC-PAY-017 | scenario `timeout_lost` | 申し込む → 時刻を 9 分進めて `resolve_unknown` → さらに 2 分進めて実行 | 1 回目の後は unknown のまま、2 回目の後に failed `not_processed` retryable=true | FR-024 | e2e（clock）：`payments/unknown-resolution.clock.spec.ts` |
| AC-PAY-018 | unknown の試行がある請求 | 別キーで Collect | `ErrAttemptInProgress`（新しい試行を作らない） | FR-020 | integration |
| AC-PAY-019 | scenario `async_ok` の M で更新課金を迎える | 更新ジョブ → `stub.settleByInvoice(I)` | 請求は一時 processing、Webhook（`/webhooks/stub`）経由で succeeded（resolution=webhook）になり契約は renewed | NFR-011 | e2e（clock）：`payments/async-settlement.clock.spec.ts` |
| AC-PAY-020 | AC-PAY-019 の通知 | 同じ provider_event_id の通知を 2 回、並行して処理させる | provider_events は 1 件、遷移とイベントも 1 件 | NFR-011 | integration |
| AC-PAY-035 | `provider_events` に pending の通知があり、SQS には送られていない（キックが届かなかった状態を DB に直接作る） | 時刻を 1 分進めて `payments.provider_events_sweep` を実行 | SQS に送り直され、受信ループが処理して試行が確定する。処理は 1 回だけ | NFR-011, FR-024 | integration |
| AC-PAY-036 | 受信ループの処理中、試行の行を別のトランザクションがロックしている（lock_timeout を超える） | 処理 | `attempts=1`・`next_attempt_at` が延び、メッセージは削除される。ロックが外れた後、掃除経由で確定する | NFR-011 | integration |
| AC-PAY-021 | succeeded の試行に、failed を示す通知が届く | 通知を処理 | succeeded のまま、`conflicting_final_outcome` を記録 | FR-024 | integration |
| AC-PAY-022 | 前日に「代行は成功・自社は unknown」の試行がある（`resolve_unknown` を動かさずに日付を進める） | `payments.reconcile_daily` を実行 → `GET /v1/ops/reconciliation-reports` | `provider_succeeded_local_not` が 1 件。もう一度実行しても 1 件 | FR-024 | e2e（clock）：`payments/reconciliation.clock.spec.ts` |
| AC-PAY-037 | 前日の stub の決済が 25 件、ページの大きさ 10、`Run.Cursors.Save("<前日>:stub", 2 ページ処理済み)` が保存されている | `reconcile_daily` を実行 | 3 ページ目だけを処理し、結果は 1 回で全件を処理した場合と同じ。`Complete` 済みの scope_key の再実行では何もしない（stub の決済一覧も読まない） | FR-024 | integration |
| AC-PAY-023 | unknown の試行（30 秒超）、代行も照会不能 | ops `resolve {outcome: failed, ...}` | failed（resolution=manual, resolved_by=運営者）。DB で監査の記録がある | NFR-014, FR-024 | e2e（clock）：`payments/ops-resolve.clock.spec.ts` |
| AC-PAY-024 | succeeded の試行 | ops `resolve {outcome: failed}` | 409 `attempt_already_finalized`。`succeeded` 指定なら 200（冪等） | NFR-014 | e2e：`payments/ops-resolve.spec.ts` |
| AC-PAY-025 | 支払方法 M が deleted の状態で、M を指す回収要求が来る（削除と更新の競合を DB の状態で作る） | Collect | failed `payment_method_unavailable` retryable=true、stub_charges は増えない | FR-012, FR-021 | integration |
| AC-PAY-026 | 有効な契約がなく、active な支払方法が 2 件ある契約者 | `DELETE /v1/me`（退会） | `GET /v1/me/payment-methods` が空。DB で 2 件とも deleted・display がマスキング済み | FR-011, NFR-015 | e2e：`payments/withdrawal.spec.ts` |
| AC-PAY-038 | AC-PAY-026 の `customer.withdrawn` | 同じメッセージを 2 回配信 | 処理は 1 回（Inbox） | NFR-015 | integration |
| AC-PAY-027 | `provider_circuit_breakers` が open | 別々の DB プールを持つ 2 つの Service（api と worker-billing を想定）から Collect | どちらも `ErrProviderUnavailable`、試行は作られない。open_until を過ぎた後、同じキーの Collect は試行を 1 件作る | NFR-005 | integration |
| AC-PAY-028 | scenario `ok` の M に `stub.setBehavior(M, 'decline', 1)` | 更新課金 → 次のリトライ | 1 回目は失敗して契約が past_due、リトライで回復して active | FR-017, FR-021 | e2e（clock）：シナリオ E2E-007 |
| AC-PAY-029 | 事業者 A の `app.tenant_id` | 事業者 B の試行を読む | 0 件（RLS） | NFR-008 | integration |
| AC-PAY-030 | 同じ ProviderKey を 2 回登録、`PAYMENTS_METHODS` にない手段 | レジストリの登録・解決 | 二重登録は panic、未設定の手段は `ErrUnsupportedMethod` | NFR-011 | unit |
| AC-PAY-031 | StubAdapter を別の ProviderID（`stub_alt`）でも生成して 2 つ登録 | レジストリで両方を解決し、それぞれで登録・Collect | どちらも動き、`internal/domain`・`internal/app`・contracts に Provider ごとの分岐がない（import の検査で、app が `provider/*` の具体パッケージを import していない） | NFR-011 | unit |
| AC-PAY-032 | `/webhooks/stub`（dev ビルド） | 署名が不正な通知、正しい通知を送る | 不正は 401 で provider_events に保存されない。正しい通知は保存されて 200 | NFR-011 | e2e：`payments/webhook-stub.spec.ts` |

**PoC の受け入れシナリオ**（Orchestration の E2E-001〜022 に組み込む）：AC-PAY-007（NFR-007）、AC-PAY-015（結果不明 → 照合で成立）、AC-PAY-019（非同期の確定が Webhook の経路で反映）。

### 11.2 E2E から StubAdapter を操作する fixture（`e2e/fixtures/payments.ts`）

StubAdapter の切り替えは、**トークン**（登録時に決まる）・**特別な金額**（プランの価格）・**dev API**（決済のたびに変えたいとき）の 3 つの経路で行う。Playwright の fixture `stub` にまとめ、テストからは fixture だけを使う。

| fixture のメソッド | 中身 | 使う場面 |
|---|---|---|
| `stub.card(scenario)` | §4.5 のテストカード番号を返す（例：`'decline'` → `4000 0000 0000 0002`） | ブラウザで申込ページに入力する |
| `stub.token(scenario, {last4?, exp?})` | `tok_stub_<scenario>_<last4>_<MMYY>_<rand8>` を組み立てる（rand8 はテストごとに乱数） | API だけで支払方法を登録する |
| `stub.registerMethod(consumer, scenario)` | `POST /v1/me/payment-methods`（Idempotency-Key は新しく生成）。`PaymentMethod` を返す | 申込・更新のテストの準備 |
| `stub.amount(scenario)` | 特別な金額（例：`'timeout'` → 999,408）を返す。fixture の `catalog.publishPlan({amount})` に渡す | 支払方法を変えずに結果を変える |
| `stub.setBehavior(paymentMethodId, scenario, remaining)` | `PUT /v1/dev/payments/stub/payment-methods/{id}/behavior` | 「初回は成功、次の更新で失敗」のように途中で変える |
| `stub.settleByInvoice(invoiceId, result?)` | `POST /v1/dev/payments/stub/payables/invoice/{invoiceId}/settle` | 非同期の決済を確定させる（Webhook の経路を通る） |
| `stub.charges(invoiceId)` | `sp_e2e_reader` で `payments.stub_charges` と `payment_attempts` を読む | 二重課金がないことの確認 |

- `jobs.run(name)`（`/v1/dev/jobs/{job}/run`）と `clock.advance(duration)`（`/v1/dev/clock`、`effective_after` を待つ）は Platform の共通 fixture を使う。
- テナント・プラン・契約者は、テストごとに fixture で新しく作る（P-TST-4）。
- fixture はシナリオ（`e2e/tests/scenarios/`）と payments の API テスト（`e2e/tests/payments/`）の両方から使う。

**Orchestration のシナリオで使われている「台本」の名前との対応**（名前の統一は TL-Orchestration と調整する）

| シナリオ側の表記 | payments での実現 |
|---|---|
| `ok` / `decline` / `timeout` 等 | 同じ名前の scenario（トークンまたは特別な金額） |
| `fail_n:N` | `stub.setBehavior(M, 'decline', N)` |
| `timeout_then:ok` | scenario `timeout`（代行側では課金済み。照会で succeeded） |
| `provider_down` | `stub.setBehavior(M, 'provider_down', N)`（§4.5 の表の追加シナリオ） |stub のトークンも乱数を含むので、並列に流しても衝突しない。

### 11.3 NFR-007 のブラウザテスト（`e2e/tests/payments/checkout-pan.spec.ts`、project `parallel`。P-TST-8 の②に置く）

**準備**：fixture でテナント・980 円のプラン・契約者（開発用 JWT）を作る。テストカードは `stub.card('ok')`（4242 4242 4242 4242）、`stub.card('decline')`、`stub.card('insufficient')` の 3 枚で、`test.describe` のパラメータとして同じ手順を流す（拒否のカードも、トークン化までは同じ経路を通るため）。

| 手順 | 内容 |
|---|---|
| 1. 記録の開始 | `page.goto` の前に `page.on('request', r => …)` を登録し、**全リクエスト**の `url()`・`await r.allHeaders()`・`r.postDataBuffer()` を配列に保存する。自社オリジン（`E2E_BASE_URL`）以外へのリクエストは、オリジンを別に記録する |
| 2. 申込ページを開く | `/checkout?plan_id=…` を開く。`page.locator('input[autocomplete="cc-number"]')` に `name` 属性がないこと、カード入力欄が `<form>` の外にあること（`closest('form') === null`）を確かめる |
| 3. 入力と申込 | カード番号を空白入りで `type`（1 文字ずつ、input イベントを発生させる）、有効期限 `12/28` を入力し、申込ボタンを押す |
| 4. 完了の待機 | `page.waitForResponse` で `POST /v1/me/subscriptions` の応答を待つ（ok は 201、decline・insufficient は 422） |
| 5. 入力欄 | カード番号の入力欄の値が空になっていること |
| 6. リクエストの検査 | 記録した全リクエストについて、URL（`decodeURIComponent` 後）・全ヘッダーの値・本文（UTF-8、URL デコード、JSON の文字列値）を連結した文字列に、カード番号の**正規化形**（数字だけ・4 桁ごとの空白区切り・ハイフン区切り・数字だけの Base64）が 1 つも含まれないこと |
| 7. 記録が働いていることの確認 | `POST /v1/me/payment-methods` の本文に `tok_stub_` で始まるトークンがあり、その下 4 桁が入力したカードと一致すること（記録の漏れで検査が素通りするのを防ぐ） |
| 8. 外部オリジン | 自社以外へのリクエストがないこと（stub のとき）。fincode のときは許可リスト（fincode の JS・API のオリジン）だけ |
| 9. ブラウザの保存領域 | `localStorage`・`sessionStorage`・`document.cookie` にカード番号がないこと |
| 10. DB | `sp_e2e_reader` で、全アプリスキーマの `text`・`varchar`・`json`・`jsonb` 列（`information_schema.columns` から列挙）を `col::text LIKE '%<数字だけのカード番号>%'` で検索し、0 件であること（Outbox の payload・Inbox・idempotency の保存レスポンスも対象になる） |
| 11. ログ | api・worker-billing・worker-integration の JSON ログ（compose がファイルに出力し、E2E は `E2E_LOG_DIR` 配下を読む。出力先の仕組みは Platform の定めに従う）に、手順 6 と同じ正規化形が 0 件であること。テスト開始時刻以降の行だけを対象にする |

- 失敗したときは、Playwright のトレースに入力したカード番号が残るが、テスト用の番号なので成果物として保存してよい。
- OpenAPI での拒否（AC-PAY-006）は API テスト、ここではブラウザの経路全体を確かめる。

---

## 12. PdM への確認事項

すべて解決済み（2026-10-12、`02-pdm-decisions.md`）。

| # | 論点 | 決定 | 状態 |
|---|---|---|---|
| Q1 | 事業者ショップ ID の取得 | A：`CollectCmd`・`EnsureProviderRefCmd` に `TenantShopID` を追加し、Orchestration が渡す | **解決済み（P-PAY-1）** |
| Q2 | 24 時間を超えて確定しない unknown | A：unknown のまま残し、アラートを出して手動確定に回す | **解決済み（P-PAY-2）** |
| Q3 | 削除済み・退会済みの支払方法での更新課金 | A：代行を呼ばずに failed（`payment_method_unavailable`、retryable=true） | **解決済み（P-PAY-3）** |
| Q4 | 決済代行の障害（ブレーカー開） | A：試行を作らず `provider_unavailable`。回数を消費しない | **解決済み（P-PAY-4）** |
| Q5 | FincodeCardAdapter の範囲 | A：骨格だけ。PoC の標準はスタブ | **解決済み（P-PAY-5）** |
| — | 決済の冪等キーの形式（Billing・Orchestration との食い違い） | `invoice:{invoice_id}:{collection_seq}` に統一 | **解決済み（P-X-1・P-X-3）** |

**TL 間の調整事項（記録）**
- TL-Billing：`collection_seq` の払い出しと、retryable=false の扱い → **解決済み（P-X-1・P-BIL-2）**。
- TL-Orchestration：`ErrAttemptInProgress` / `ErrPayableAlreadyPaid` / `ErrProviderUnavailable` の扱い、`TenantShopID` の受け渡し、申込の 422 で返す consumer 分類。冪等キーの形式は **解決済み（P-X-3）**。
- TL-Platform：payment_methods の RLS に `app.customer_account_id` を使う仕組みは、platform.md §3（db の `InTx` が `set_config('app.customer_account_id', …)` を実行する customer スコープ）で解決済み（P-X-6）。dev ビルドタグでのマイグレーション分離は、platform.md の `devtools` タグの方針に従う。
- 監査：手動確定などの ops 操作は監査ミドルウェアと auditctx で記録する → **解決済み（P-X-2）**。支払方法の登録・削除のイベントは追加しない（P-X-2 の方針に従う）。

---

## 13. 変更履歴

| 日付 | 変更内容 | 変更者 |
|---|---|---|
| 2026-10-12 | 初版作成（Draft） | TL-Payments（Claude） |
| 2026-10-12 | PdM レビューを反映（Review）。冪等キーを `invoice:{invoice_id}:{collection_seq}` に統一し `collection_seq` 列を追加（P-X-1・P-X-3）、Q1〜Q5 を解決済みに（P-PAY-1〜5）、エラーコードを `provider_unavailable` に、手動確定の監査を auditctx に変更（P-X-2）、AC-PAY-033・034 を追加、Go の型と DDL を付録に移動 | TL-Payments（Claude） |
| 2026-10-12 | PdM の最終確認を経て Fixed とする | PdM |
| 2026-10-12 | 実行基盤の Lambda 化を反映（Review。C-05・P-ARC-1〜7、レビュー §7.3）。§4.4 にトークンバケット・ブレーカーはインスタンスごとの安全網で全体の流量は `orc-collection` の最大同時実行で制御することを追記、§7.8・§7.9 を「Inbox に保存 → コミット後に SQS へ送る」に、§8 を常駐ループなしの構成（スケジュールジョブ＋SQS 購読者＋掃除）に書き直し、reconcile_daily に継続実行を追加。AC-PAY-019・020・022・027・032 を修正、AC-PAY-035〜037 を追加 | TL-Payments（Claude） |
| 2026-10-12 | 実行基盤の Lambda 化の反映について PdM の最終確認を経て、再び Fixed とする | PdM |
| 2026-10-12 | 実行基盤を ECS Fargate に戻す決定を反映（Review。P-ARC-10・P-ARC-11）。§4.4：全体の流量は worker-billing の `orc-collection` 受信同時実行数（固定）× タスク数の上限、トークンバケットはタスクごとの安全網、ブレーカーの状態は DB（`provider_circuit_breakers`）で共有。§5・§8：Lambda の関数・job-runner を worker-billing の常駐受信ループとスケジュールジョブに置き換え（Webhook の Inbox → SQS は維持）、reconcile_daily の 10 分区切りをやめ `job_cursors` による再開に変更。固定 IP はタスクのサブネットで確保。AC-PAY-019・020・027・035〜037 を修正 | TL-Payments（Claude） |
| 2026-10-13 | ジョブの再開を Platform 共通の `platform.job_cursors` に合わせる（P-X-7）。payments スキーマの `job_cursors`（DDL を含む）を削除し、reconcile_daily は `scope_key = "<業務日付>:<provider>"` で `Run.Cursors` を使う形に変更（§8.3 に再開の仕組みを追記）。AC-PAY-037 を修正 | TL-Payments（Claude） |
| 2026-10-13 | 実行基盤を ECS Fargate に戻した改訂（P-ARC-10・11、P-X-7）について PdM の最終確認を経て、Fixed とする | PdM |
| 2026-10-13 | テスト方針の変更を反映（Review。P-TST-1〜7）。§11 をテストレベル（unit / integration（本物の DB）/ e2e（Playwright））で付け直し、フェイク・テスト用フック・ダミーアダプター前提の AC を、本物の StubAdapter と DB の状態で作る形に変更（AC-PAY-038 を追加）。§11.2 に StubAdapter を操作する Playwright fixture、§11.3 に NFR-007 のブラウザテストの手順を追加。e2e の AC に確認先（シナリオ番号または `e2e/tests/payments/*.spec.ts`）を記載（P-TST-8）。時刻を進める spec は `.clock.spec.ts` にし、進めないテストと分離（P-TST-9）。dev の挙動上書きに `provider_down` を追加し、Orchestration の台本名との対応表を追加。StubAdapter に `WebhookParser`（`/webhooks/stub`、HMAC 署名）を実装し、dev の settle を Webhook 経由に、請求 ID で settle できる dev API を追加 | TL-Payments（Claude） |
| 2026-10-13 | テスト戦略の改訂（Playwright の E2E を中心にし、モックは原則使わない。P-TST-1〜10、P-X-8・9）について PdM の最終確認を経て、Fixed とする | PdM |

---

## 付録 A. `payments/contracts` の型

```go
package contracts // import: 標準ライブラリと sharedkernel のみ

type PayableType string
const PayableInvoice PayableType = "invoice"
type PayableRef struct { Type PayableType; ID string } // UUID 文字列。中身を知らない
type MethodType string
const MethodCard MethodType = "card"
type AttemptStatus string // processing | pending_async | succeeded | failed | unknown

type Service interface {
    Collect(ctx context.Context, cmd CollectCmd) (AttemptResult, error)
    RegisterPaymentMethod(ctx context.Context, cmd RegisterPaymentMethodCmd) (PaymentMethodView, error)
    GetPaymentMethod(ctx context.Context, customerID CustomerAccountID, id PaymentMethodID) (PaymentMethodView, error)
    ListPaymentMethods(ctx context.Context, customerID CustomerAccountID, page sharedkernel.PageRequest) (sharedkernel.Page[PaymentMethodView], error)
    DeletePaymentMethod(ctx context.Context, customerID CustomerAccountID, id PaymentMethodID) error
    EnsureProviderRef(ctx context.Context, cmd EnsureProviderRefCmd) error
    ResolveUnknownAttempts(ctx context.Context, olderThan time.Duration) (ResolveSummary, error)
}

type CollectCmd struct {
    TenantID          TenantID
    TenantShopID      *string           // Orchestration が tenant/contracts で得て渡す（P-PAY-1）
    CustomerAccountID CustomerAccountID // 追加：支払方法の所有者確認
    Payable           PayableRef
    Amount            sharedkernel.Money
    PaymentMethodID   PaymentMethodID
    IdempotencyKey    string            // invoice:{invoice_id}:{collection_seq}（§3.3、P-X-1）
}
type AttemptResult struct {
    AttemptID   PaymentAttemptID
    Status      AttemptStatus
    FailureCode *string
    Retryable   *bool
    ConsumerCategory *string // card_expired | insufficient_funds | generic（failed のとき）
    Replayed    bool         // 既存の試行を返した（冪等の再実行）
}
type RegisterPaymentMethodCmd struct {
    CustomerAccountID CustomerAccountID
    MethodType        MethodType
    Provider          string // "stub" | "fincode"
    Token             string
    IdempotencyKey    string // HTTP の Idempotency-Key をそのまま渡す（代行への冪等キー）
}
type PaymentMethodView struct {
    ID PaymentMethodID; MethodType MethodType; Status string
    Display map[string]any // card: brand, last4, exp_month, exp_year
    Expired bool           // Clock 基準で算出
    CreatedAt time.Time
}
type EnsureProviderRefCmd struct { PaymentMethodID PaymentMethodID; TenantID TenantID; TenantShopID *string }
type ResolveSummary struct { Checked, Succeeded, Failed, StillUnknown, Pending int }
```

## 付録 B. PaymentProvider ポートの型

```go
type ProviderID string
type ProviderKey struct { Method contracts.MethodType; Provider ProviderID }

// 必須
type PaymentProvider interface {
    Key() ProviderKey
    Capabilities() Capabilities
    OrderRefFor(attemptID uuid.UUID) string
    Collect(ctx context.Context, in CollectInput) (Outcome, error)
    GetStatus(ctx context.Context, in StatusInput) (Outcome, error)
}
// 任意（実装していればレジストリが使う）
type MethodRegistrar interface {
    RegisterMethod(ctx context.Context, in RegisterInput) (RegisteredMethod, error)
}
type TenantRefProvisioner interface { // 共有できない代行で、事業者ショップ向けの参照を作る
    EnsureTenantRef(ctx context.Context, in EnsureTenantRefInput) (ProviderRef, error)
}
type WebhookParser interface { // 署名検証を含む
    ParseWebhook(ctx context.Context, h http.Header, body []byte) ([]ProviderEvent, error)
}
type SettlementLister interface { // 日次照合
    ListSettlements(ctx context.Context, from, to time.Time, cursor string) ([]Settlement, string, error)
}
type MethodDisabler interface { DisableMethod(ctx context.Context, ref ProviderRef) error }

type Capabilities struct {
    AsyncSettlement   bool        // pending_async を返しうる
    RefSharing        RefSharing  // shared（tenant_shop_id=NULL で全事業者共通）| per_tenant
    RequiresStoredMethod bool     // false: コンビニ払いのように都度払い
}
type CollectInput struct {
    AttemptID uuid.UUID; ProviderOrderRef, IdempotencyKey string // IdempotencyKey = attempt_id
    Amount sharedkernel.Money; Ref ProviderRef; TenantShopID *string
}
type StatusInput struct { ProviderOrderRef string; TenantShopID *string }
type Outcome struct {
    Status      OutcomeStatus // succeeded | failed | pending_async | unknown | not_found
    FailureCode string; Retryable bool; RawCode string
    NextAction  *NextAction   // 将来用
}
type ProviderEvent struct {
    ProviderEventID, ProviderOrderRef string
    Kind    string // payment_succeeded | payment_failed | ...（代行非依存に正規化）
    Outcome Outcome; OccurredAt time.Time; Raw json.RawMessage
}
```

## 付録 C. DDL（スキーマ `payments`）

```sql
CREATE TABLE payment_methods (
  id uuid PRIMARY KEY, customer_account_id uuid NOT NULL,
  method_type text NOT NULL CHECK (method_type IN ('card')),        -- 手段追加時に広げる
  status text NOT NULL CHECK (status IN ('active','deleted')),
  display jsonb NOT NULL,                                            -- 判別共用体。PAN・CVC を入れない
  deleted_at timestamptz, version int NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL);
CREATE INDEX ON payment_methods (customer_account_id, created_at DESC, id) WHERE status = 'active';

CREATE TABLE payment_method_provider_refs (
  id uuid PRIMARY KEY, payment_method_id uuid NOT NULL REFERENCES payment_methods(id),
  provider text NOT NULL, tenant_shop_id text NULL,
  customer_ref text NOT NULL, method_ref text NOT NULL, created_at timestamptz NOT NULL);
CREATE UNIQUE INDEX ON payment_method_provider_refs (payment_method_id, provider, COALESCE(tenant_shop_id, ''));

CREATE TABLE payment_attempts (
  id uuid PRIMARY KEY, tenant_id uuid NOT NULL,
  payable_type text NOT NULL CHECK (payable_type IN ('invoice')), payable_id uuid NOT NULL,
  customer_account_id uuid NOT NULL, payment_method_id uuid NOT NULL REFERENCES payment_methods(id),
  method_type text NOT NULL, provider text NOT NULL, tenant_shop_id text NULL,
  amount integer NOT NULL CHECK (amount BETWEEN 1 AND 1000000),
  idempotency_key text NOT NULL UNIQUE, collection_seq int NOT NULL CHECK (collection_seq >= 1),
  request_hash text NOT NULL,
  provider_order_ref text UNIQUE,
  status text NOT NULL CHECK (status IN ('processing','pending_async','succeeded','failed','unknown')),
  failure_code text, failure_retryable boolean, provider_raw_code text, next_action jsonb,
  resolution text CHECK (resolution IN ('sync','webhook','reconcile','manual')),
  resolved_by text, resolve_note text, check_count int NOT NULL DEFAULT 0, last_checked_at timestamptz,
  requested_at timestamptz NOT NULL, finalized_at timestamptz,
  version int NOT NULL DEFAULT 1, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
  CHECK ((status = 'failed') = (failure_code IS NOT NULL AND failure_retryable IS NOT NULL)));
CREATE UNIQUE INDEX uq_attempt_succeeded ON payment_attempts (payable_type, payable_id) WHERE status = 'succeeded';
CREATE UNIQUE INDEX uq_attempt_inflight  ON payment_attempts (payable_type, payable_id)
  WHERE status IN ('processing','pending_async','unknown');
CREATE INDEX ix_attempt_unresolved ON payment_attempts (status, requested_at)
  WHERE status IN ('processing','unknown','pending_async');
CREATE INDEX ix_attempt_requested ON payment_attempts (provider, requested_at);   -- 日次照合

CREATE TABLE provider_events (               -- 決済代行 Webhook の Inbox
  provider text NOT NULL, provider_event_id text NOT NULL, provider_order_ref text,
  payload jsonb NOT NULL, received_at timestamptz NOT NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processed','ignored','failed')),
  attempts int NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL, last_error text, processed_at timestamptz,
  PRIMARY KEY (provider, provider_event_id));
CREATE INDEX ON provider_events (next_attempt_at) WHERE status = 'pending';

CREATE TABLE provider_circuit_breakers (    -- 全タスクで共有するブレーカーの状態（P-ARC-11 ④）
  provider text PRIMARY KEY, consecutive_failures int NOT NULL DEFAULT 0,
  open_until timestamptz NULL, updated_at timestamptz NOT NULL);

CREATE TABLE reconciliation_discrepancies (
  id uuid PRIMARY KEY, business_date date NOT NULL, provider text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('provider_succeeded_local_not','local_succeeded_provider_not',
        'amount_mismatch','orphan_provider_charge','conflicting_final_outcome')),
  attempt_id uuid NULL, tenant_id uuid NULL, provider_order_ref text, detail jsonb NOT NULL,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','acknowledged')),
  detected_at timestamptz NOT NULL,
  UNIQUE (business_date, provider, kind, provider_order_ref));

-- dev ビルドのマイグレーションだけで作る
CREATE TABLE stub_methods (method_ref text PRIMARY KEY, token text NOT NULL UNIQUE, scenario text NOT NULL,
  behavior_override text, override_remaining int NOT NULL DEFAULT 0, created_at timestamptz NOT NULL);
CREATE TABLE stub_charges (order_ref text PRIMARY KEY, idempotency_key text NOT NULL UNIQUE,
  method_ref text NOT NULL, amount integer NOT NULL, true_status text NOT NULL, failure_code text,
  created_at timestamptz NOT NULL, settled_at timestamptz);
-- 共通: outbox_events, processed_messages（Platform の定義どおり）
```
| 2026-10-25 | 実装時の決定を反映（P-IMP-15）：StubAdapter は登録だけを devtools に限る、Stub の表は devtools の migrate だけ（バージョン表を分ける）、ジョブ名を `payments-<name>` に、通知の処理の依頼は EventEnvelope で SQS に直接送る、dev の settle は `IngestWebhook` に直接渡す、`reconciliation_discrepancies` にも tenant の RLS。実装の状況は `docs/poc/impl/payments.md` | PdM |
