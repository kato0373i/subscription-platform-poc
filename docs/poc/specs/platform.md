# 詳細要件: Platform（sharedkernel・internal/platform・ローカル環境・開発基盤）

| 項目 | 内容 |
|---|---|
| ステータス | **Fixed**（2026-10-24 PdM 承認。ログと調査の仕組みを反映済み） |
| 担当 | TL-Platform |
| 作成日 | 2026-10-12 |
| 上位文書 | `00-poc-charter.md`（最優先。C-05 取り消し・C-06）、`02-pdm-decisions.md`（決定ログ。P-ARC-8・P-ARC-10・P-ARC-11・P-X-7・P-TST-1〜10・P-OBS-1〜8）、`01-contracts-baseline.md`（§5 改訂 v1.1 を含む）、`docs/design/subscription-platform.md`（ADR-001・ADR-008・ADR-009、§1.4）、`docs/requirements/subscription-platform.md` |
| 調査の手順 | `docs/runbooks/debugging.md`（本書 §8.6 のコマンドを使う） |
| 関連する詳細要件 | `identity.md`（認可の表・JIT・X-Tenant-Id）、`catalog.md`（Discount）、`integration.md`（監査の保存先・閲覧 API・注釈の一覧 §3.6） |

## 1. 概要と PoC の範囲

全モジュールが使う「型・トランザクション・イベント配送・HTTP の共通処理・ジョブ・時刻・ローカル環境・lint／テスト基盤」を定める。ドメインの知識は持たない（`internal/platform` は `modules/**` を import しない）。

| 対象の要件 | 本書での扱い |
|---|---|
| FR-019, D-11, D-12 | `Date`・`BillingPeriod` の月末補正・うるう年・期間の計算 |
| D-09, D-10 | `Money.MulRatFloor`・`TaxRate.InclusiveTax`・`Discount.Apply` |
| FR-020, NFR-006 | Outbox・Inbox・Idempotency-Key・ジョブの重複排除（各層の冪等性の土台） |
| NFR-008 | RLS の共通関数とポリシーのひな形、モジュール別 DB ロール、テナント分離テストの共通の仕組み |
| FR-011, NFR-009 | 4 種類の認証主体、Authenticator（開発用 JWT／Cognito）、API キー認証の差し込み口 |
| NFR-005 | 購読者ごとのキューと受信の同時実行数の上限（バルクヘッド）。回収専用キュー `orc-collection` の受信の同時実行数を固定し、fincode への流量を制御（P-ARC-11 ②） |
| FR-025, NFR-014（簡略） | `auditctx`（監査注釈）と監査ミドルウェア（P-X-2）。保存先と閲覧 API は integration（audit モジュール） |
| NFR-013（簡略）, NFR-016 | slog（JSON）と correlation_id の引き継ぎ、UTC 保存・JST の暦日 |
| NFR-017 | 調査用のログ（§8.6）：共通の形式の JSON ログ、相関 ID でプロセスをまたいで時系列に取り出す `task logs`、失敗した E2E へのログの添付、秘密情報の除去、Fluent Bit → MiniStack の CloudWatch Logs |
| D-07 | `job_runs` の所要時間と件数の記録。1 万件のスループットは、ローカルで各プロセスを起動するモード（§8.4）で参考値として計測する。本番相当の値は dev 環境で取る |
| 憲章 §5 | dev clock、`/v1/dev/jobs/{job}/run`、ビルドタグでの除外 |

| 作るもの | 作らないもの（PoC 対象外） |
|---|---|
| sharedkernel の型一式 | 分散トレースの可視化（Jaeger・X-Ray のローカル再現）、ログのダッシュボード、Athena でのログ分析（P-OBS-8。ログには `trace_id`・`span_id` の項目だけを用意する）、OpenTelemetry・ADOT |
| db / outbox / inbox / messaging（`Dispatch`＋常駐の受信ループ `sqsconsumer`）/ httpx / scheduler / devclock / observability / config / auditctx | RDS Proxy（任意。必要になれば後から入れる。§10）、リーダーへの読み取り分散、Secrets Manager、AppConfig |
| `cmd/api`・`cmd/worker-billing`・`cmd/worker-integration`・`cmd/migrate`・`cmd/internal/wiring`、`deploy/messaging.yaml`・`deploy/schedules.yaml`（配線の正本） | 本番用 Terraform モジュール（`network`・`compute`・`edge` など）、CloudFront・WAF・ALB の本番設定、Multi-AZ |
| compose.yaml（MiniStack・`webhook-sink`・Fluent Bit ＋ 任意でアプリのコンテナ）、`cmd/devlogs`、`infra/modules/messaging`・`data`（ローカルで使う範囲）、`infra/envs/local`、Taskfile | CloudWatch のアラーム・ダッシュボード（DLQ の滞留はログ出力のみ） |
| depguard・archtest・golangci-lint・コード生成と CI の差分チェック | テナントごとのレート制限（差し込み口だけ） |
| テストの共通基盤：Go の UT・結合テスト（testcontainers）、Playwright の `e2e/`（設定・2 つの project・共通の fixture）、`webhook-sink`、テナント分離テストの仕組み、CI の 3 ジョブ | E2E の業務シナリオそのもの（E2E-001〜022 は TL-Orchestration、各ドメインのテストは各 TL） |
| | `cmd/worker-projection`（P-PLT-4。Reporting を作るときに追加）、監査の保存・閲覧（integration の担当） |

## 2. 用語

| 用語 | 意味 |
|---|---|
| スコープ | DB トランザクションに設定する「誰の権限で読むか」。tenant / customer / system の 3 種類。未設定なら DB に触れない（fail closed） |
| モジュールロール | `sp_<module>`（RLS が効く）と `sp_<module>_sys`（BYPASSRLS）。トランザクションの先頭の `set_config('role', …, true)` で切り替える |
| トポロジー | どのイベントをどのキューに配り、どのプロセスのどの購読者が処理するかの定義（`deploy/messaging.yaml`）。Terraform と Go の両方が読む唯一の正本 |
| プロセス | ECS Fargate のサービス（ADR-001、P-ARC-10）。`api`・`worker-billing`・`worker-integration` の 3 種類。マイグレーションは `migrate` のワンショット実行 |
| 常駐ループ | ワーカーの中で動き続ける処理。SQS の受信ループ、Outbox の中継、Webhook の配信（integration）など |
| 購読者名 | `<module>.<name>` 形式の処理単位の名前。Inbox の `consumer` 列にも使う |
| ジョブ名 | `<module>-<name>`（kebab-case、先頭はモジュール名）。例 `orchestration-renewal-dispatch`、`catalog-coupon-reservation-sweep` |
| 業務時刻 / 実時刻 | 業務時刻＝`sharedkernel.Clock`（dev clock で進められる）。実時刻＝`time.Now()`（リトライ間隔・リース・タイムアウトなど基盤の都合にだけ使う） |
| 相関 ID（correlation_id） | 1 件の操作（API リクエスト・E2E テスト 1 本・ジョブの 1 回の実行）に関係するログとイベントが共通に持つ文字列（`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`）。API は `X-Correlation-Id`、E2E は `e2e-<testId>-<乱数>`、ジョブは `job:<job>:<run_id>`（§8.6） |
| 標準のログの種類 | `msg` に入れる決まった名前（`http.request.completed` など）。platform の共通部品だけが出す（§8.6.2） |
| 監査注釈 | ユースケースが `auditctx.Annotate` で context に書く「何を・どう変えたか（before/after）」。監査ミドルウェアが応答時に `AuditWriter` に渡す |
| devtools ビルド | `-tags devtools` 付きのビルド。dev clock・`/v1/dev/*`・開発用 JWT を含む。本番イメージには含めない |

## 3. モデル・コンポーネント

### 3.1 sharedkernel（標準ライブラリと `github.com/google/uuid` だけに依存する）

```go
package sharedkernel

// ---- ID ----
type ID[T any] struct{ u uuid.UUID }              // T は使わない幽霊型。ID[A] と ID[B] は別の型
func NewID[T any]() ID[T]                         // UUIDv7
func ParseID[T any](s string) (ID[T], error)      // 形式不正は ErrInvalidID
func (id ID[T]) String() string
func (id ID[T]) IsZero() bool
func (id ID[T]) UUID() uuid.UUID                  // adapter での変換用
// MarshalText / UnmarshalText / sql.Scanner / driver.Valuer を実装する

type TenantTag struct{};          type TenantID = ID[TenantTag]
type CustomerAccountTag struct{}; type CustomerAccountID = ID[CustomerAccountTag]
type EventTag struct{};           type EventID = ID[EventTag]

func NewOpaqueID(prefix string, n int) string     // prefix + "_" + base62 n 文字（crypto/rand）。cus_・evt_ 用

// ---- Money ----
type Money struct{ yen int64 }                    // 不変条件: yen >= 0
func NewMoney(yen int64) (Money, error)           // 負は ErrNegativeMoney
func (m Money) Int64() int64
func (m Money) Add(o Money) (Money, error)        // 桁あふれは ErrMoneyOverflow
func (m Money) Sub(o Money) (Money, error)        // 結果が負なら ErrNegativeMoney
func (m Money) SubClamped(o Money) Money          // 0 未満は 0（D-10）
func (m Money) MulRatFloor(num, den int64) Money  // floor(m*num/den)。前提 num>=0, den>0。math/big で計算。前提違反・桁あふれは panic
func (m Money) Cmp(o Money) int
func (m Money) IsZero() bool

// ---- TaxRate ----
type TaxRate struct{ num, den int64 }             // 既約分数。不変条件: 0 <= num < den
func NewTaxRate(num, den int64) (TaxRate, error)
func ParseTaxRate(s string) (TaxRate, error)      // "0.10" / "0.100"（DB の numeric(4,3)）→ 1/10。浮動小数点を経由しない
func (r TaxRate) String() string                  // 小数 3 桁 "0.100"
func (r TaxRate) InclusiveTax(gross Money) Money  // floor(gross*num/(den+num)) ＝ D-09 の floor(税込×r÷(1+r))

// ---- Discount（P-CAT-5。D-10 の式の唯一の実装。Catalog のプレビューと Billing の確定額が共用する）----
type DiscountType string                          // "amount" | "percent"
type Discount struct{ Type DiscountType; Value int64 } // amount: 円（>=1）、percent: 1〜100
func (d Discount) Apply(gross Money) (discount, net Money) // percent: discount=floor(gross×Value/100)。amount: min(Value, gross)。net=gross−discount（>=0）

// ---- Date（JST の暦日）----
var JST = time.FixedZone("Asia/Tokyo", 9*60*60)   // tzdata に依存しない（distroless 対策）
type Date struct{ y int; m time.Month; d int }    // 不変条件: 実在する日付、1 <= y <= 9999
func NewDate(y int, m time.Month, d int) (Date, error) // 2027-02-30 などは ErrInvalidDate
func ParseDate(s string) (Date, error)            // "YYYY-MM-DD" のみ
func DateOf(t time.Time) Date                     // t を JST に変換した暦日
func (d Date) AddDays(n int) Date
func (d Date) AddMonthsClamped(months, anchorDay int) Date // 日 = min(anchorDay, その月の日数)。anchorDay は 1〜31
func (d Date) DaysInMonth() int
func (d Date) Compare(o Date) int                 // Before / After / Equal も持つ
func (d Date) DaysUntil(o Date) int
func (d Date) StartJST() time.Time                // その日の 00:00 JST
func (d Date) AtJST(hour, min int) time.Time      // 例: リトライ 10:00 JST
func (d Date) String() string                     // "YYYY-MM-DD"。PostgreSQL の date と相互変換する

// ---- BillingInterval / BillingPeriod ----
type BillingInterval string                       // "month" | "year"
func (i BillingInterval) Months() int             // 1 / 12。不正値は Valid() で検出
type BillingPeriod struct{ Start, End Date }      // 両端を含む（D-11）。不変条件: Start <= End
func NewBillingPeriod(start, end Date) (BillingPeriod, error)
func FirstPeriod(start Date, iv BillingInterval, anchorDay int) BillingPeriod
func DaysPeriod(start Date, days int) (BillingPeriod, error) // トライアル用。days>=1。End = start+days−1
func (p BillingPeriod) Next(iv BillingInterval, anchorDay int) BillingPeriod
func (p BillingPeriod) NextStart() Date           // End+1 ＝ 次の更新日（D-11）
func (p BillingPeriod) Contains(d Date) bool
func (p BillingPeriod) Days() int

// ---- Clock ----
type Clock interface{ Now() time.Time; Today() Date } // Today = DateOf(Now())
type SystemClock struct{}
type ManualClock struct{ /* テスト用。Set / Advance を持つ */ }

// ---- EventEnvelope ----
type Actor struct{ Type string; ID string }       // consumer | staff | tenant_api | operator | system（P-CAT-3。トレース用）
type EventEnvelope struct {
    EventID          EventID         `json:"event_id"`
    EventType        string          `json:"event_type"`        // "<module>.<snake>"。detail-type に使う
    OccurredAt       time.Time       `json:"occurred_at"`       // 業務時刻（UTC で直列化）
    TenantID         *TenantID       `json:"tenant_id"`         // テナントに属さないイベントは null（例 customer.withdrawn）
    AggregateType    string          `json:"aggregate_type"`
    AggregateID      uuid.UUID       `json:"aggregate_id"`
    AggregateVersion int64           `json:"aggregate_version"`
    CorrelationID    string          `json:"correlation_id"`    // 相関 ID（UUID とは限らない。§8.6.3）
    CausationID      *string         `json:"causation_id"`      // 直前の原因（request_id・event_id・job_run_id）
    Actor            *Actor          `json:"actor,omitempty"`   // 任意（baseline §5.1）
    Payload          json.RawMessage `json:"payload"`
}
func NewEnvelope[P any](eventType, aggType string, aggID uuid.UUID, ver int64, tenant *TenantID, at time.Time, p P) (EventEnvelope, error)
func DecodePayload[P any](e EventEnvelope) (P, error) // 未知の項目は無視する（前方互換）
func (e EventEnvelope) Module() string             // event_type の "." より前。EventBridge の source = "sp." + Module()
```

**計算の規則（BillingPeriod）**

| 関数 | 定義 |
|---|---|
| `FirstPeriod(s, iv, a)` | `{s, s.AddMonthsClamped(iv.Months(), a).AddDays(-1)}` |
| `Next(iv, a)` | `start' = End+1`、`end' = start'.AddMonthsClamped(iv.Months(), a).AddDays(-1)` |
| anchorDay の決め方 | D-12 に従い「初回の課金日」の日（トライアルなしは申込日の日）。Subscription が `billing_anchor_day` に保存し、毎回渡す |
| 年額で 2-29 開始 | `a = 29`、月は変わらないので平年は 2-28、うるう年は 2-29 に戻る |

**ID のマーカー型の置き場所（全 TL 共通の規約）**：各モジュールの `contracts` に `type XxxTag struct{}` と `type XxxID = sharedkernel.ID[XxxTag]` を置く。複数モジュールと platform が使う `TenantID`・`CustomerAccountID`・`EventID` だけ sharedkernel に置く。domain は contracts の ID 型を使ってよい（contracts は std＋sharedkernel だけに依存するので循環しない）。

### 3.2 platform のコンポーネント（内部の動き）

| コンポーネント | 責務・内部の動き |
|---|---|
| db | pgxpool を 1 プロセス 1 つ（接続を使い回す。RDS Proxy は任意）。`InTx` の先頭で **`SELECT set_config('role',$1,true), set_config('statement_timeout',$2,true), set_config('lock_timeout',$3,true), set_config('app.tenant_id',$4,true), set_config('app.customer_account_id',$5,true)` の 1 文**を実行してから fn を呼ぶ（往復が 1 回で済み、将来 Proxy を入れてもピン留めを起こしにくい）。コミット後に `AfterCommit` のフックを順に呼ぶ |
| outbox.Writer | 呼び出し元のトランザクションで `<schema>.outbox_events` に INSERT。event_id・correlation_id・causation_id・actor が空なら context から埋める。同じプロセスに Relay があれば、`db.AfterCommit` で `Relay.Kick()` を呼ぶ（任意。遅延を縮めるだけ） |
| outbox.Relay | worker-billing の常駐ループ。登録されたスキーマごとに `SELECT … WHERE published_at IS NULL AND next_attempt_at <= now() ORDER BY occurred_at, id LIMIT 100 FOR UPDATE SKIP LOCKED` → 10 件ずつ PutEvents → 成功分は `published_at = now()`、失敗分は `attempts+1`・`next_attempt_at = now() + min(2^attempts 秒, 5 分)`＋ジッター、を同じトランザクションで確定。残りがあれば続けて回し、なければ 500ms 待つ（`Kick` で待ちを打ち切る）。複数タスクで動かしても SKIP LOCKED で重複しない。ロールは `sp_relay` |
| inbox | 購読者のトランザクションで `INSERT INTO <schema>.processed_messages … ON CONFLICT DO NOTHING`。0 行なら重複としてハンドラを呼ばずに成功扱い |
| messaging.Dispatch | 実行基盤に依存しない共通部分。受け取ったメッセージを 1 件ずつ解析 → 購読者の振り分け → ハンドラ（Inbox のトランザクション）→ エラーの分類をして、成功・一時失敗・恒久失敗に分ける。FIFO では、失敗したメッセージより後ろにある同じ MessageGroupId のメッセージを処理しない（順序を守る） |
| messaging.sqsconsumer | ワーカーの常駐の受信ループ。SQS ロングポーリング（20 秒・最大 10 件）→ キューごとの同時実行数のセマフォ → `Dispatch`。処理中は VT/2 ごとに可視性を VT だけ延長。成功で Delete、一時失敗で `ChangeMessageVisibility(min(5 秒×2^(受信回数−1), 15 分))`、`Permanent` は DLQ に送って元を Delete。受信回数が上限を超えたものは SQS の redrive で DLQ へ。SIGTERM で受信を止め、処理中を最大 30 秒待つ |
| scheduler.Runner | ワーカーの scheduler キュー（`scheduler-billing`・`scheduler-integration`）のメッセージ `{job, trigger, run_id?, params}` を受けて `job_runs` を作成・ロックし、Job を実行して結果を記録。続きからの再開は共通の表 `platform.job_cursors` で行う（P-X-7。§6.4） |
| devclock | 業務時刻 = 実時刻 + offset。offset は `platform.dev_clock` に 1 行。各トランザクションの最初に読む（プロセスごとに 1 秒キャッシュ。P-ARC-8）。LISTEN と ack はしない |
| httpx | ルーター・認証・認可・スコープ設定・Idempotency・Problem Details・カーソル。ルートの方針は OpenAPI の拡張属性から起動時に読み込む。入口は CloudFront＋WAF → ALB → `api`（P-ARC-11 ①。ALB 以前の設定は本番インフラの範囲） |
| auditctx | 監査ミドルウェアが context に「袋」を用意し、ユースケースが `Annotate` で注釈を積む。袋がない context（`/v1/me`、購読者、ジョブ）では何もしない。before/after のキー名 `secret`・`token`・`password`・`email`・`name` は `"***"` に置き換える（安全網。integration §3.6.2） |
| 監査ミドルウェア | `/v1/console`・`/v1/ops`・`/v1/tenant` の変更系（POST/PUT/PATCH/DELETE）だけに挟む。認証の後・認可の前に置き、応答を記録してから `AuditWriter` に渡し、その後で応答を返す（§7.1） |
| observability | slog の JSON ハンドラ。共通の項目を context から付け、出力先（`LOG_SINKS`）・モジュールごとのレベル・秘密情報の除去を受け持つ。標準のログの種類を出す補助関数（HTTP の入口・外向きの HTTP クライアント・イベント・ジョブ・状態遷移・遅いクエリ・panic）を持つ（§8.6） |

## 4. 公開する契約（他のモジュールが使う API）

```go
// ---- internal/platform/db ----
type Tx = pgx.Tx
func Open(ctx context.Context, cfg Config) (*Pool, error)
type Config struct{ URL string; MaxConns, MinConns int32 }   // api 20 / worker 10
func (p *Pool) Module(name string) *ModuleDB                 // name = スキーマ名 = ロール名の接尾辞
func (m *ModuleDB) InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error, opts ...TxOption) error
func (m *ModuleDB) ReadTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error, opts ...TxOption) error // READ ONLY
func WithIsolation(l pgx.TxIsoLevel) TxOption
func WithRetryOnSerialization(max int) TxOption              // 40001/40P01 だけ再試行（既定 0）
func WithStatementTimeout(d time.Duration) TxOption
func AfterCommit(ctx context.Context, f func(ctx context.Context))  // InTx の中で登録し、コミット成功後に順に実行（ロールバック時は捨てる）
func WithTenantScope(ctx context.Context, t sharedkernel.TenantID) context.Context
func WithCustomerScope(ctx context.Context, c sharedkernel.CustomerAccountID) context.Context // tenant と併用可（OR）
func WithSystemScope(ctx context.Context, reason string) context.Context // sp_<m>_sys を使う。reason は必須でログに出す
func WithUnscoped(ctx context.Context, reason string) context.Context     // 主体を確定する前（API キーの認証・初回ログイン）。sp_<m> で動き、RLS のある表は 0 行（P-IMP-10）
func ScopeFrom(ctx context.Context) (Scope, bool)
var ErrNoScope, ErrConflictingScope error                   // system と tenant/customer の併用は ErrConflictingScope

// ---- internal/platform/outbox ----
func NewWriter(schema string, clock sharedkernel.Clock, opts ...WriterOption) *Writer
func WithKick(r *Relay) WriterOption                          // 同じプロセスの Relay をコミット後に起こす（任意）
func (w *Writer) Append(ctx context.Context, tx db.Tx, events ...sharedkernel.EventEnvelope) error
func NewRelay(pool *db.Pool, pub messaging.Publisher, schemas []string, cfg RelayConfig) *Relay
func (r *Relay) Run(ctx context.Context) error                // worker-billing の常駐ループ
func (r *Relay) Kick()                                        // 待ちを打ち切って次の周回に入る

// ---- internal/platform/inbox ----
type Handler func(ctx context.Context, tx db.Tx, env sharedkernel.EventEnvelope) error
func Wrap(m *db.ModuleDB, consumer string, h Handler, opts ...Option) messaging.Handler
func AllowSystemScope(reason string) Option                  // tenant_id が null のイベントを受ける購読者だけが指定する

// ---- internal/platform/messaging ----
type Publisher interface{ Publish(ctx context.Context, evs []sharedkernel.EventEnvelope) ([]PublishResult, error) }
type PublishResult struct{ EventID sharedkernel.EventID; Err error }
type Handler func(ctx context.Context, env sharedkernel.EventEnvelope) error
func Permanent(err error) error                               // 再試行しても直らない（解析不能・契約違反）
type CommandSender interface {                                // FIFO へのジョブ投入（C-01）
    SendFIFO(ctx context.Context, queue string, env sharedkernel.EventEnvelope, groupID, dedupID string) error
}
type ConsumerSpec struct{ Name string; Handler Handler }      // 各 module.go が返す
func LoadTopology(fsys fs.FS) (Topology, error)               // deploy/messaging.yaml
type Message struct{ ID, Body, GroupID, ReceiptHandle string; ReceiveCount int }
type Outcome struct{ Succeeded, Retry, Permanent []Message }
func NewDispatcher(t Topology, queue string, consumers []ConsumerSpec) (*Dispatcher, error) // 購読者がなければエラー
func (d *Dispatcher) Dispatch(ctx context.Context, msgs []Message) Outcome
func RunConsumer(ctx context.Context, d *Dispatcher, cfg ConsumerConfig) error // sqsconsumer（常駐）
type ConsumerConfig struct{ QueueURL, DLQURL string; Concurrency int; VisibilityTimeout, WaitTime, ShutdownGrace time.Duration }
// 実装: EventBridgePublisher / FanoutPublisher（direct モード）/ SQSCommandSender（テストでも MiniStack の本物を使う。メモリ上の代替は作らない）

// ---- internal/platform/scheduler ----
type Job interface{ Name() string; Run(ctx context.Context, run Run) (Stats, error) }
type Run struct {
    ID uuid.UUID; Job string; Trigger Trigger; AsOf sharedkernel.Date; Params json.RawMessage
    Cursors CursorStore                                       // このジョブ名に束ねた platform.job_cursors（P-X-7）
}
// scopeKey は「何の続きか」を表すジョブごとのキー（例 "2026-10-12"、"2026-10-12:fincode"）。run をまたいで続きを引き継ぐ
type CursorStore interface {
    Load(ctx context.Context, scopeKey string) (CursorState, error)                  // 行がなければ {Cursor: nil, Completed: false}
    Save(ctx context.Context, tx db.Tx, scopeKey string, cursor json.RawMessage) error // 業務の書き込みと同じトランザクションで保存できる（tx が nil なら単独）
    Complete(ctx context.Context, tx db.Tx, scopeKey string) error                   // 完了済みにする。以後の再実行は Load で Completed=true を見て何もしない
}
type CursorState struct{ Cursor json.RawMessage; Completed bool; UpdatedAt time.Time }
type Trigger string                                           // schedule | dev | ops
type Stats map[string]int64                                   // 例 {"enqueued": 10000}。job_runs.stats に保存
func (s *Service) Enqueue(ctx context.Context, job string, o EnqueueOptions) (uuid.UUID, error) // ops の再実行（billing-runs/rerun）も使う
func (s *Service) Get(ctx context.Context, runID uuid.UUID) (RunView, error)
type EnqueueOptions struct{ Trigger Trigger; AsOf *sharedkernel.Date; Params json.RawMessage; Force bool }

// ---- internal/platform/httpx ----
type PrincipalKind string                                     // consumer | staff | tenant_api | operator
type Principal struct {
    Kind              PrincipalKind
    Subject           string                                  // JWT sub / API キーの key_prefix / 運営者の sub
    TenantID          *sharedkernel.TenantID                  // staff, tenant_api
    CustomerAccountID *sharedkernel.CustomerAccountID         // consumer
    Role              string                                  // staff のみ
}
func PrincipalFrom(ctx context.Context) (Principal, bool)
func ConsumerID(ctx context.Context) sharedkernel.CustomerAccountID // 主体が違えば panic（ルートで保証済みのため）
func TenantID(ctx context.Context) sharedkernel.TenantID
type Claims struct{ Issuer, Subject, Email string; EmailVerified bool; Name string; Groups []string; ExpiresAt time.Time }
type TokenVerifier interface{ Verify(ctx context.Context, raw string, pool UserPool) (Claims, error) } // DevJWTVerifier / CognitoVerifier
type UserPool string                                          // consumer | staff | operator

// cmd/internal/wiring が modules の contracts を包んで注入する（platform は modules を import しない）
type Ports struct {
    ResolveConsumer func(ctx context.Context, c Claims) (sharedkernel.CustomerAccountID, error) // customer.ResolveOrCreateAccount
    AuthenticateAPIKey func(ctx context.Context, raw string) (sharedkernel.TenantID, error)   // tenant.AuthenticateAPIKey
    GetStaffRole func(ctx context.Context, t sharedkernel.TenantID, sub string) (string, error) // tenant.GetStaffRole
    Allows func(role, permission string) bool                                                  // tenant.Allows
    IsWritePermission func(permission string) bool
    TenantWritable func(ctx context.Context, t sharedkernel.TenantID) (bool, error)            // GetTenant().Status != suspended
    MapError func(err error) *Problem                                                          // 各モジュールのエラー→code の対応（任意）
    AuditWriter AuditWriter                                                                     // audit モジュールの書き込み（P-X-2）
}
type AuditWriter interface{ Write(ctx context.Context, recs []auditctx.Record) error } // 実装は modules/audit。cmd/internal/wiring で注入
func NewRouter(cfg RouterConfig, v TokenVerifier, p Ports, idem IdempotencyStore) *Router
func (r *Router) Mount(spec *openapi3.T, register func(mux ServeMux)) error // oapi-codegen の HandlerFromMux に渡す。拡張属性の検査もここ
type Problem struct{ Type, Title, Detail, RequestID, CorrelationID string; Status int; Code errcodes.Code; Errors []FieldError } // JSON は request_id・correlation_id
type FieldError struct{ Field, Code string }
func Error(code errcodes.Code, detail string, fields ...FieldError) *Problem // error を実装
func WriteProblem(w http.ResponseWriter, r *http.Request, err error)         // *Problem 以外は internal_error
func MarkTransient(w http.ResponseWriter)                     // この応答を Idempotency に保存しない（例: 申込の 202）
type PageRequest struct{ Limit int; After []byte }
func ParsePage(r *http.Request, codec *CursorCodec) (PageRequest, error)
type Page[T any] struct{ Data []T `json:"data"`; HasMore bool `json:"has_more"`; NextCursor *string `json:"next_cursor"` }
func (c *CursorCodec) Encode(key any) string; func (c *CursorCodec) Decode(s string, key any) error // AES-GCM

// ---- internal/platform/auditctx（std・sharedkernel のみに依存。ドメインの知識は持たない）----
type Target struct{ Type, ID string }                        // 例 {"plan", "<uuid>"}
type Option func(*Annotation)
func WithTenant(t sharedkernel.TenantID) Option               // ops の操作で対象の事業者を明示する
func Annotate(ctx context.Context, action string, target Target, before, after map[string]any, opts ...Option) // action は integration §3.6.1 の一覧
func NewBag(ctx context.Context) context.Context              // ミドルウェア専用
func Drain(ctx context.Context) []Annotation                  // ミドルウェア専用
type Record struct {                                          // AuditWriter に渡す 1 件（integration §3.6 の AuditRecord に対応）
    OccurredAt time.Time; TenantID *sharedkernel.TenantID
    ActorType, ActorID, ActorRole string                      // operator・staff は subject、tenant_api は API キーの ID
    Action string; Target Target; Before, After map[string]any
    Outcome string                                            // succeeded | denied
    RequestID, CorrelationID, HTTPMethod, Route, SourceIP string
}

// ---- internal/platform/observability（§8.6）----
func Setup(cfg LogConfig) (*Logging, error)                   // LOG_SINKS・LOG_LEVEL・LOG_HTTP_BODY を読み、sink を開く。終了時に Close
func (l *Logging) ForModule(module string) *slog.Logger       // モジュール・部品ごとのロガー（LOG_LEVEL のモジュール別レベルが効く）
func WithCorrelation(ctx context.Context, corr string, cause *string) context.Context
func CorrelationFrom(ctx context.Context) (corr string, cause *string)
func NewCorrelationID() string                                // UUIDv7 の文字列
func ValidCorrelationID(s string) bool                        // ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$
func WithFields(ctx context.Context, attrs ...slog.Attr) context.Context // 以後のログに付ける項目（subscription_id など）
func ActorFrom(ctx context.Context) *sharedkernel.Actor
func StateChanged(ctx context.Context, aggregate, id, from, to, reason string) // domain.state_changed（from == to なら出さない）
func NewHTTPClient(target string, base *http.Client) *http.Client // http.outbound.completed を出す外向きの共通ラッパー
func Recover(ctx context.Context, component string)           // defer で使う。panic.recovered を出して error に変える呼び出し側の補助
func PgxTracer(threshold time.Duration) pgx.QueryTracer       // db.slow_query
func Redact(v any) any                                        // §8.6.1 の除去を値に適用（本文・任意の項目）
```

**モジュール側の規約**

| # | 規約 |
|---|---|
| M1 | 業務時刻は注入された `sharedkernel.Clock` からだけ取る。SQL の `now()` を業務判定に使わない（`created_at` の既定値のみ可） |
| M2 | SQL はスキーマ名で修飾する（`billing.invoices`）。`search_path` に頼らない |
| M3 | 集約の保存と `outbox.Append` は同じ `InTx` の中で行う |
| M4 | 他モジュールの contracts 呼び出しは別トランザクションになる。呼ばれる側は自分の業務キー（`(subscription_id, period_start)`、attempt_id など）で冪等にする |
| M5 | `WithSystemScope` を使ってよいのは、ジョブ・ops ハンドラ・テナントを跨ぐ購読者だけ。対象を特定したら `WithTenantScope` に切り替えて書き込む |
| M6 | エラーコードは `api/errors.yaml` に追記し、`task generate` で得た `errcodes` の定数を使う |
| M8 | §3.6.1（integration.md）の操作を行うユースケースは、成功した変更の直後に `auditctx.Annotate` を呼ぶ。before/after に秘密情報・個人情報を入れない |
| M7 | 各 module.go は `New(deps) *Module` と、必要なものだけ `MountHTTP(r *httpx.Router) error`・`Consumers() []messaging.ConsumerSpec`・`Jobs() []scheduler.Job`・`Service() contracts.Service` を持つ（実行基盤が変わってもこの形は変えない） |
| M9 | 件数の多いジョブは、区切りのよい所（例：500 件ごと）で `Run.Cursors.Save` を呼び、再試行で続きから再開できるようにする。カーソルの表をモジュールごとに作らない（P-ARC-11 ⑥・P-X-7） |
| M10 | 複数タスクで共有すべき状態（サーキットブレーカーの開閉・連続失敗数など）はプロセスのメモリに持たず、各モジュールのスキーマの表に持つ（例 integration の `webhook_endpoints` のブレーカーの列、payments の `provider_circuit_breakers`）。メモリには短いキャッシュだけを置き、判定と更新は DB の値で行う（P-ARC-11 ④・P-X-7） |

## 5. 購読するイベントと処理

Platform 自身はドメインイベントを購読しない。全購読者に共通の処理を定める。

| 項目 | 決定 |
|---|---|
| メッセージの形 | SQS の本文は EventBridge のイベント JSON（`{source, detail-type, detail}`）で、`detail` が EventEnvelope。direct モードでも同じ形で送るので、購読側のコードは変わらない |
| 冪等性 | `inbox.Wrap` が `(consumer, event_id)` で重複を除く。重複排除の行とハンドラの書き込みは同じトランザクション。失敗時はロールバックするので、再配信で再処理される |
| 可視性が切れて二重に受信した場合 | 後からコミットする側が `processed_messages` の PK 違反になる → 重複として成功扱い（バッチ失敗に含めない） |
| 受信 | 各ワーカーの `sqsconsumer` が受信し、`Dispatch` に渡す。1 件ずつ別のトランザクションで処理し、成功したものだけ削除する |
| スコープ | `tenant_id` があれば `WithTenantScope`。null なら `AllowSystemScope` を指定した購読者だけ受け付け、それ以外は `Permanent`（DLQ） |
| context | `correlation_id` を引き継ぎ、`causation_id` に受け取った event_id を入れる。actor は `system` |
| 順序 | 保証しない。受け手は `aggregate_version` や状態で判定する（ADR-004） |
| FIFO（`orc-renewal.fifo`） | 本文は EventEnvelope（例 `event_type = orchestration.renewal_requested`）。グループ ID・重複排除 ID は orchestration の spec が決める（C-01）。Inbox も通す。受信したバッチの中で 1 件が失敗したら、同じグループの後ろのメッセージは処理せずに削除しない（グループ内の順序を守る） |

## 6. 永続化

### 6.1 DB ロール（`cmd/migrate bootstrap` が作る。全モジュールのスキーマと既定の権限も bootstrap が作り、§6.2 の関数は `platform` のマイグレーションが作る。P-IMP-5。ローカルは MiniStack の RDS で、`rds_superuser` 相当に絞った準備用ロールで実行する。§8.4）

| ロール | 属性 | 権限 |
|---|---|---|
| `sp_owner` | NOLOGIN, BYPASSRLS | 全スキーマ・テーブルの所有者。マイグレーションは `SET ROLE sp_owner` で行う。所有する SECURITY DEFINER 関数（テナント未確定の検索）が FORCE RLS の表を読めるよう BYPASSRLS を持つ（P-IMP-7） |
| `sp_app` | LOGIN, **NOINHERIT** | 実行時の接続ユーザー。自分では何の権限も持たず、トランザクションの先頭の `set_config('role', …, true)` で下のロールに切り替えるだけ（`GRANT … WITH INHERIT FALSE, SET TRUE`） |
| `sp_<module>` | NOLOGIN, NOBYPASSRLS | 自分のスキーマの DML のみ。モジュール：tenant, customer, catalog, subscription, billing, payments, orchestration, integration, audit（reporting は作らない） |
| `sp_<module>_sys` | NOLOGIN, BYPASSRLS | `sp_<module>` と同じ権限＋RLS を越えて読める |
| `sp_platform` | NOLOGIN | `platform` スキーマのみ |
| `sp_relay` | NOLOGIN, BYPASSRLS | 全 `<m>.outbox_events` の SELECT・UPDATE、DELETE（GC） |
| `sp_audit` の制限 | — | `audit.audit_records` の UPDATE・DELETE を REVOKE |
| `sp_e2e_reader` | LOGIN, BYPASSRLS, `default_transaction_read_only = on` | **devtools のマイグレーション（`cmd/migrate up -tags devtools` の `platform/db/migrations_devtools`）でだけ作る**（P-TST-3）。全スキーマの SELECT のみ。パスワードは `.env.local`。本番ビルドの migrate には含まれないので、本番 DB には存在しない |

### 6.2 共通の SQL 関数（`platform` スキーマ。マイグレーションのひな形として使う）

```sql
CREATE FUNCTION platform.current_tenant_id() RETURNS uuid STABLE LANGUAGE sql
  AS $$ SELECT nullif(current_setting('app.tenant_id', true), '')::uuid $$;
CREATE FUNCTION platform.current_customer_id() RETURNS uuid STABLE LANGUAGE sql
  AS $$ SELECT nullif(current_setting('app.customer_account_id', true), '')::uuid $$;

-- 各モジュールの最初のマイグレーションで呼ぶ: outbox_events・processed_messages の作成と権限付与
SELECT platform.create_messaging_tables('billing');
-- テナントに属するテーブルに RLS をかける（ENABLE + FORCE + ポリシー）
SELECT platform.enable_tenant_rls('billing.invoices');                       -- tenant_id = current_tenant_id()
SELECT platform.enable_customer_rls('subscription.subscriptions', 'customer_account_id'); -- 任意。permissive なので OR
```

- ポリシーは `USING` と `WITH CHECK` の両方に同じ条件を付ける（他テナントの tenant_id で INSERT できない）。
- スコープ未設定のときは関数が NULL を返し、0 行になる（fail closed）。さらに `db` はスコープなしの `InTx` を `ErrNoScope` で拒否する（二重の安全網）。

### 6.3 各モジュールのスキーマに作る共通テーブル（`create_messaging_tables` が作る）

```sql
CREATE TABLE <m>.outbox_events (
  id                uuid PRIMARY KEY,                 -- = event_id（UUIDv7）
  event_type        text NOT NULL CHECK (event_type ~ '^[a-z]+\.[a-z_]+$'),
  tenant_id         uuid NULL,
  aggregate_type    text NOT NULL,
  aggregate_id      uuid NOT NULL,
  aggregate_version bigint NOT NULL,
  correlation_id    text NOT NULL CHECK (correlation_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
  causation_id      text NULL CHECK (char_length(causation_id) <= 128),
  actor             jsonb NULL,
  payload           jsonb NOT NULL CHECK (octet_length(payload::text) <= 200000), -- EventBridge 256KB 制限の余裕
  occurred_at       timestamptz NOT NULL,             -- 業務時刻
  created_at        timestamptz NOT NULL DEFAULT now(),
  published_at      timestamptz NULL,
  attempts          integer NOT NULL DEFAULT 0,
  next_attempt_at   timestamptz NOT NULL DEFAULT now(),
  last_error        text NULL
);
CREATE INDEX ON <m>.outbox_events (next_attempt_at, occurred_at, id) WHERE published_at IS NULL;
CREATE INDEX ON <m>.outbox_events (published_at) WHERE published_at IS NOT NULL;   -- GC 用
-- RLS はかけない（sp_relay と Writer だけが触る）

CREATE TABLE <m>.processed_messages (
  consumer     text NOT NULL,
  message_id   uuid NOT NULL,                         -- = event_id
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer, message_id)
);
CREATE INDEX ON <m>.processed_messages (processed_at);
```

### 6.4 platform スキーマ

```sql
CREATE TABLE platform.idempotency_keys (           -- P-PLT-1（設計書 §4 の orchestration.idempotency_keys から移す）
  scope            text NOT NULL,                  -- "<principal kind>:<subject id>:<METHOD> <route pattern>"
  key              text NOT NULL CHECK (char_length(key) BETWEEN 1 AND 255),
  request_hash     bytea NOT NULL,                 -- sha256(method + path + 本文のバイト列)
  status           text NOT NULL CHECK (status IN ('in_progress','completed')),
  response_status  smallint NULL,
  response_headers jsonb NULL,                     -- Content-Type・Location のみ保存
  response_body    bytea NULL,
  locked_until     timestamptz NULL,               -- in_progress のリース（60 秒）
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,           -- created_at + 24h（実時刻）
  PRIMARY KEY (scope, key)
);
CREATE INDEX ON platform.idempotency_keys (expires_at);

CREATE TABLE platform.job_runs (
  id          uuid PRIMARY KEY,
  job         text NOT NULL,
  trigger     text NOT NULL CHECK (trigger IN ('schedule','dev','ops')),
  dedupe_key  text NULL,                           -- schedule: "<job>:<as_of>"（hourly は時まで）。Force 時は NULL
  as_of       date NOT NULL,
  params      jsonb NOT NULL DEFAULT '{}',
  status      text NOT NULL CHECK (status IN ('queued','running','succeeded','failed')),
  attempts    integer NOT NULL DEFAULT 0,
  lease_until timestamptz NULL,
  stats       jsonb NULL,
  error       text NULL,
  queued_at   timestamptz NOT NULL DEFAULT now(),
  started_at  timestamptz NULL,
  finished_at timestamptz NULL
);
CREATE UNIQUE INDEX ON platform.job_runs (job, dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX ON platform.job_runs (job, queued_at DESC);

CREATE TABLE platform.job_cursors (              -- P-X-7。ジョブの続きからの再開に使う共通の表（モジュールごとに作らない）
  job          text NOT NULL,                      -- ジョブ名（<module>-<name>）
  scope_key    text NOT NULL,                      -- 何の続きか（例 "2026-10-12"、"2026-10-12:fincode"）。ジョブが決める
  cursor       jsonb NULL,                         -- ジョブが解釈する不透明な値
  run_id       uuid NULL,                          -- 最後に更新した job_runs.id（追跡用。FK は張らない）
  completed_at timestamptz NULL,                   -- 完了済みなら再実行で何もしない
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (job, scope_key)
);
-- 全モジュールロールに SELECT・INSERT・UPDATE を付与（業務と同じトランザクションで保存できるようにするため）。RLS はかけない
-- GC は platform-job-cursor-gc（完了から 30 日超を削除）

CREATE TABLE platform.dev_clock (                  -- devtools ビルドだけが使う（表は常に作る）
  id         smallint PRIMARY KEY CHECK (id = 1),
  offset_ms  bigint NOT NULL CHECK (offset_ms >= 0),
  version    bigint NOT NULL,
  updated_at timestamptz NOT NULL
);
```

マイグレーションの順序：`cmd/migrate up` は `platform` → tenant → customer → catalog → subscription → billing → payments → orchestration → integration → audit の順。goose のバージョン表はスキーマごと（`<m>.goose_db_version`）。

## 7. API

### 7.1 全エンドポイント共通（httpx が実装し、各 TL のエンドポイントに適用される）

| 項目 | 決定 |
|---|---|
| 入口と主体 | `/v1/me/*`＝consumer、`/v1/console/*`＝staff、`/v1/tenant/*`＝tenant_api、`/v1/ops/*`＝operator、`/v1/checkout/*`＝認証なし、`/webhooks/*`＝モジュールが署名を検証、`/v1/dev/*`＝devtools ビルドのみ・認証なし |
| consumer | `Authorization: Bearer <JWT>`（consumer プール）→ `Ports.ResolveConsumer`（JIT。identity §7.3）→ customer スコープ |
| staff | JWT（staff プール）＋ `X-Tenant-Id`（P-IDN-1）→ `GetStaffRole` → `Allows(role, x-sp-permission)` → 書き込みなら `TenantWritable` → tenant スコープ |
| tenant_api | `Authorization: Bearer <API キー>` → `AuthenticateAPIKey` → tenant スコープ |
| operator | JWT（operator プール）かつ `groups` に `operators` → system スコープ（reason = "operator"） |
| Authenticator の切り替え | `AUTH_MODE=dev`（既定。ES256 の開発鍵 `deploy/local/dev-jwt.pem` で検証）/ `cognito`（プールごとの JWKS を lestrrat-go/jwx で取得・キャッシュ）。検証項目は iss・aud（client_id）・exp・nbf（許容誤差 60 秒）・alg の許可リスト（ES256/RS256 のみ） |
| ルートの方針（OpenAPI 拡張属性） | `x-sp-permission`（console は必須）、`x-sp-idempotency: required`、`x-sp-isolation: tested | not_applicable:<理由>`（me・console・tenant は必須）。欠けていれば `Router.Mount` が起動時エラー |
| 入力検証 | kin-openapi の検証ミドルウェア（モジュールの spec を埋め込み）。本文の上限 1MB |
| 要求ヘッダー | `Authorization`、`X-Tenant-Id`、`Idempotency-Key`、`X-Correlation-Id`（§8.6.3 の形式に合わなければ無視して新規発行） |
| 応答ヘッダー | `X-Request-Id`（UUIDv7）、`X-Correlation-Id`（常に返す）、再生時は `Idempotent-Replayed: true` |
| エラー | `application/problem+json`。`request_id` と `correlation_id` を必ず入れる（P-OBS-4）。`type = https://docs.example.com/errors/<code>`。500 では内部の詳細を出さない。別テナントのリソースは 404（モジュールが返す） |
| 監査（P-X-2） | console・ops・tenant の変更系で、2xx は `succeeded`、403 は `denied` として記録する（401・その他の 4xx・5xx は記録しない）。2xx の注釈は 1 件ずつ Record にし、注釈のない 2xx は `action = http.<method>`・route 付きで 1 件記録する（取りこぼし防止）。tenant_id は主体のテナント、ops では `WithTenant` の値。時刻は業務時刻 |
| ページング | `limit` 既定 20・上限 100（超過・0 以下は `validation_failed`）、`starting_after` は AES-GCM で暗号化したカーソル。改ざん・期限切れ鍵は `validation_failed` |

**Idempotency-Key の処理**（`x-sp-idempotency: required` のルート。ベースラインでは `POST /v1/me/subscriptions`・`POST /v1/me/payment-methods`）

| 状態 | 動作 |
|---|---|
| ヘッダーなし | 400 `validation_failed`（`errors[].field = "Idempotency-Key"`） |
| 新規 | `in_progress` で INSERT（リース 60 秒）→ ハンドラ実行 → 応答が 5xx でなく `MarkTransient` もなければ `completed` で保存、そうでなければ行を削除 |
| completed・同じハッシュ | 保存した応答をそのまま返す（ハンドラは呼ばない）。`Idempotent-Replayed: true` |
| completed・違うハッシュ | 422 `idempotency_key_reused` |
| in_progress・リース内 | 409 `request_in_progress`、`Retry-After: 1` |
| in_progress・リース切れ | 引き継いで実行（前のプロセスが落ちたとみなす） |
| 範囲 | scope に主体の ID を含めるので、別の利用者が同じキーを使っても衝突しない |

### 7.2 エラーコードの一覧の管理

- 正本は `api/errors.yaml`（`code`・`status`・`title`（日本語）・`owner`（モジュール）・`description`）。各 TL は自分の spec のコードをここに追記する。
- `task generate` が Go の定数（`internal/platform/httpx/errcodes/codes_gen.go`）と TS の union 型を生成する。生成時に、重複・snake_case でない・status が 4xx/5xx 以外、を検出したら失敗させる。
- Platform が登録する共通コード：

| code | HTTP | 用途 |
|---|---|---|
| validation_failed | 400 | 形式の誤り（OpenAPI 検証、ページング、ヘッダー不足） |
| unauthenticated | 401 | トークン・API キーがない／不正／期限切れ |
| forbidden | 403 | 主体の種類・ロールが合わない、テナントに所属していない |
| not_found | 404 | リソースなし・別テナント・未定義のルート |
| method_not_allowed | 405 | 追加。ServeMux の 405 を Problem に変換 |
| conflict | 409 | 楽観ロックの衝突 |
| request_in_progress | 409 | 追加。同じ Idempotency-Key の処理中 |
| idempotency_key_reused | 422 | 同じキーで中身が違う |
| internal_error | 500 | 想定外 |
| service_unavailable | 503 | 追加。DB に接続できない、シャットダウン中 |
| provider_unavailable | 503 | 追加（P-PAY-4）。決済代行の障害（サーキットブレーカーが開いている）。owner は payments。`Retry-After` を付ける |

### 7.3 Platform のエンドポイント

| メソッド・パス | 主体 | リクエスト | レスポンス | エラー | 冪等性 |
|---|---|---|---|---|---|
| GET /healthz | なし | — | 200 `{status:"ok"}`（プロセスが生きている。ALB／ECS のヘルスチェック） | — | — |
| GET /readyz | なし | — | 200 / 503（DB の ping、シャットダウン中は 503） | service_unavailable | — |
| GET /v1/dev/clock | なし（devtools） | — | 200 `{now, today, offset_seconds, version}` | — | — |
| POST /v1/dev/clock | なし（devtools） | `advance`（Go の duration 文字列 例 `"72h"`）または `to`（RFC 3339）のどちらか 1 つ | 200 `{now, today, offset_seconds, version, effective_after}`。書き込んだらすぐ返す。各実行環境は 1 秒キャッシュなので、**`effective_after`（＝応答時刻＋1 秒）以降の起動・トランザクションから新しい時刻が使われる** | validation_failed（両方／どちらもない）、`clock_backward_not_allowed` 422 | 同じ `to` は結果が同じ |
| POST /v1/dev/jobs/{job}/run | なし（devtools） | `{as_of?: "YYYY-MM-DD", params?: object, force?: bool, wait_seconds?: int（既定 60・上限 300）}` | 完了：200 `{run_id, job, status, as_of, stats, error, deduplicated}`。待ち時間切れ：202 `{run_id, status}` | not_found（未登録のジョブ）、validation_failed | `force=false` なら dedupe_key で重複排除し、既存の run を `deduplicated:true` で返す |
| GET /v1/dev/job-runs/{run_id} | なし（devtools） | — | 200（上と同じ形） | not_found | — |
| POST /v1/dev/smoke・GET /v1/dev/smoke/{event_id} | なし（devtools。P-IMP-5） | — | 202 `{event_id}`（`platform.smoke_requested` を Outbox に書く）／200 `{published, consumed}` | not_found | — |
| POST /v1/dev/tokens | なし（devtools、`AUTH_MODE=dev` のみ） | `{pool: consumer|staff|operator, sub, email?, email_verified?, name?, groups?, ttl_seconds?(既定 3600)}` | 200 `{token, expires_at}` | validation_failed | — |
| POST /v1/dev/seed/subscriptions | なし（devtools のみ。P-TST-10） | `{tenant_id, plan_id, count（1〜20000）, payment_scenario?（既定 `success`。StubAdapter のシナリオ）, seed_id?（省略時は採番）}` | 202 `{seed_id, run_id}`。進み具合は `GET /v1/dev/job-runs/{run_id}`（stats に `created`・`failed`。完了時に `first_renewal_date`） | validation_failed（count の範囲・トライアル付きや月額以外のプラン）、not_found（テナント・プラン）、`tenant_not_active` 403 | 同じ `seed_id` の再送は同じ run を返す。途中で落ちても `platform.job_cursors`（scope_key = seed_id）から続きを作る |

- dev のジョブ実行は、スケジュール起動と同じ経路（ジョブを持つプロセスの scheduler キューに投入 → `job_runs` の完了を待つ）を通す。API プロセスの中では実行しない。
- `as_of` を省略したら、実行時の `Clock.Today()`。
- **一括シード（`POST /v1/dev/seed/subscriptions`）**：E2E-020 で 1 万件の契約を用意するため（P-TST-10）。
  - 実体は devtools ビルドだけに入るジョブ `platform-dev-seed-subscriptions`（worker-billing が実行。API は scheduler キューに投入するだけ）。platform は modules を import しないので、コードは `cmd/internal/wiring/devseed`（`//go:build devtools`）に置く。
  - 1 件ごとに、申込と同じ順で**各モジュールの contracts だけを呼んで**作る：`customer.ResolveOrCreateAccount`（合成の subject `seed:{seed_id}:{n}`）→ `customer.EnsureTenantCustomerID` → `payments.RegisterPaymentMethod`（StubAdapter のトークン）→ `catalog.GetPurchasablePlan` → `subscription.CreateForSignup` → `billing.IssueInvoice`（initial・caller_managed）→ `payments.Collect` → `billing.RecordPaymentOutcome` → `subscription.Activate`。**DB に直接書き込まない**（archtest で `devseed` が `modules/*/contracts` 以外を import しないことを検査）。各モジュールの Outbox・監査以外の副作用も本物と同じに起きる。
  - 申込日は実行時の業務時刻（dev clock）。トライアルなしの月額プランに限るので、全件の初回の更新日が同じ 1 日に揃う（E2E-020 の目的に合わせ、更新日の分布は持たせない）。500 件ごとにカーソルを保存する。

## 8. ジョブ・非同期処理

### 8.1 配線の正本（Terraform と Go の両方が読む）

```yaml
# deploy/messaging.yaml（抜粋。キュー名・購読者名は各 TL の spec で確定する）
bus: sp-events
queues:
  - name: billing-payments              # 購読者ごとに 1 キュー（DLQ は "<name>-dlq" を自動で作る）
    process: worker-billing
    consumer: billing.payment_outcome
    events: [payments.payment_succeeded, payments.payment_failed, payments.payment_pending, payments.payment_outcome_unknown]
    concurrency: 8                      # 1 タスクあたりの受信の同時実行数
    visibility_timeout_s: 60
    max_receive: 5
  - name: orc-collection                # P-ARC-7・P-ARC-11 ②。回収専用
    process: worker-billing
    consumer: orchestration.collection_due
    events: [billing.invoice_collection_due]
    concurrency: 4                      # 固定。fincode への同時実行 = 4 × worker-billing の最大タスク数
  - name: orc-renewal.fifo
    process: worker-billing
    consumer: orchestration.renewal_requests
    fifo: true                          # events なし（SDK から直接送る。C-01）
# deploy/schedules.yaml
jobs:
  - name: orchestration-renewal-dispatch
    process: worker-billing             # 送り先は scheduler-billing キュー
    schedule: "cron(30 0 * * ? *)"      # Asia/Tokyo
    dedupe: daily                       # daily | hourly | none
```

- `events` は「detail-type の完全一致」と「`prefix:`」だけを許す（EventBridge のルールと Go の振り分けの両方で同じ意味にできる範囲に限る）。
- 起動時、プロセスは自分の `process` のキューについて、各モジュールの `Consumers()` から購読者名でハンドラを探す。見つからなければ起動を失敗させる。
- `orc-collection` の流量：fincode への同時呼び出しの上限 ≧ `concurrency` × worker-billing の最大タスク数（ECS のオートスケールの上限）。fincode のレート制限が分かったら決める（未解決 #2）。`task generate` で、YAML とタスク数の上限（`infra` の変数）からこの式を検査する。

### 8.2 プロセスの組み立て（`cmd/*`）

| プロセス | HTTP | 組み込むモジュール | 購読（キュー） | ジョブ・常駐ループ |
|---|---|---|---|---|
| `cmd/api` | 全モジュールの HTTP、`/v1/dev/*`（devtools）、監査ミドルウェア | 全モジュール（reporting 以外）。申込の同期処理（Orchestration の Checkout）と、監査の書き込み（audit モジュールの `AuditWriter`） | なし | なし（dev のジョブは投入のみ） |
| `cmd/worker-billing` | /healthz・/readyz のみ | orchestration, subscription, billing, payments, catalog, customer, tenant | `billing-payments`、`orc-events`、**`orc-collection`（同時実行数を固定）**、`orc-renewal.fifo`、`scheduler-billing` | Outbox Relay（全スキーマ）、fincode の通知処理と照合（payments の spec が定める方式。P-ARC-11 ④）、課金系ジョブ（更新の投入、トライアル終了の予告、リトライ時刻の請求の走査、結果不明の決済の確定、照合、クーポン予約の掃除）、platform の GC |
| `cmd/worker-integration` | 同上 | integration, customer, tenant | `integration-webhook`、`integration-email`、`scheduler-integration` | Webhook の配信ループ（DB の配信予定をポーリング。サーキットブレーカーの状態は DB。P-ARC-11 ③④。詳細は integration の spec） |
| `cmd/migrate` | — | 全モジュールの migrations（audit を含む） | — | `bootstrap`（ロール・platform 関数）、`up`、`status`。ECS のワンショット実行 |

- 組み立ては `cmd/internal/wiring` に置く（プロセスごとに必要なモジュールを組み立てる関数）。Go の internal の規則で `cmd/**` からしか import できないので、「`modules/X` を import できるのは `cmd/**` だけ」の規則は変わらない。各 `main` は wiring を呼ぶだけ。
- `cmd/worker-projection` は PoC では作らない（P-PLT-4）。Reporting を作るときに、プロセス・キュー・`messaging.yaml` の購読者を追加する。
- ジョブの具体的な名前・時刻は各 TL の spec で決め、`deploy/schedules.yaml` に登録する。Platform のジョブ：`platform-idempotency-gc`（毎日 03:00、期限切れを削除）、`platform-outbox-gc`（毎日 03:10、送信済み 7 日超を削除）、`platform-inbox-gc`（毎日 03:20、30 日超を削除）、`platform-job-cursor-gc`（毎日 03:30、完了から 30 日超を削除）。

### 8.3 ジョブ実行の規則

| 項目 | 決定 |
|---|---|
| 起動 | EventBridge Scheduler（タイムゾーン Asia/Tokyo）→ プロセスごとの scheduler キュー（`scheduler-billing`・`scheduler-integration`。標準 SQS）→ ワーカーの Runner。本文 `{job, trigger:"schedule"}` |
| 重複排除 | `dedupe: daily` は `job:as_of`、`hourly` は `job:as_of:HH`（業務時刻）。`job_runs` の部分ユニークで二重起動を防ぐ |
| 実行 | `running` にして `lease_until` を設定 → `Job.Run` → `succeeded`（stats を保存）／`failed`（error を保存し、メッセージを削除しない → 最大 3 回で DLQ）。実行中は受信ループが可視性を延長する。**ジョブは冪等に書く** |
| 再開（P-ARC-11 ⑥・P-X-7） | カーソルは共通の表 `platform.job_cursors`（§6.4）に `(job, scope_key)` で持つ。Job は区切りのよい所で `Run.Cursors.Save` を（できれば業務の書き込みと同じトランザクションで）呼び、最後に `Complete` を呼ぶ。タスクが落ちて再配信されたら、同じ run を `attempts+1` で実行し、`Load` したカーソルから続きを処理する（最初からやり直さない）。別の run（ops の再実行など）でも同じ scope_key なら続きを引き継ぎ、完了済みなら何もしない。各モジュールは独自のカーソル表を作らない（payments の `job_cursors` もこちらに合わせる）。実行時間で区切ることはしない |
| 時刻 | `as_of` は業務時刻の当日（dev clock を進めればジョブの日付も進む） |
| 計測（D-07） | 開始・終了・stats をログと `job_runs` に残す。1 万件のスループットは `orchestration-renewal-dispatch` の起動から最後の更新完了までを、§8.4 のローカルのモードで TL-Orchestration が計測する |

### 8.4 ローカル環境（C-06・P-ARC-11 ①⑦）

| 対象 | 決定 |
|---|---|
| モード | **1 つだけ**。MiniStack（RDS を含む）を compose で起動し、各プロセス（api・worker-billing・worker-integration）を `go run -tags devtools` で起動する。コンテナで動かしたいときは compose の profile `app` で同じ 3 プロセスを起動する（同じイメージ・同じ環境変数）。PoC の完了条件の E2E と D-07 の計測はこのモードで行う |
| compose.yaml | `ministack`（タグを固定、:4566）。**`ministack-tls`**（Fluent Bit のために TLS を終端する nginx。P-IMP-3）。RDS のために Docker のソケットをマウントし、`DOCKER_NETWORK`（compose のネットワーク名）・`RDS_PERSIST=1`・`PERSIST_STATE=1` を設定する。**`webhook-sink`**（常に起動。コンテナ内 8080、ホストへ 18080 で公開。§10.3）。profile `app`：api・worker-billing・worker-integration（devtools ビルドのイメージ。`./.local/logs` を同じパスにマウントし、`go run` と同じ場所にログを書く）。**`fluent-bit`**（常に起動。§8.6.4）。profile `mail`：`mailhog`。profile `db`：`postgres:16-alpine`（`DB_MODE=container` のフォールバック。P-IMP-2） |
| DB | `infra/envs/local` が本番と同じ `infra/modules/data` で `aws_db_instance`（engine `postgres`、engine_version `16.x`）を作る。MiniStack が中で PostgreSQL 16 のコンテナを起動する。返ってきた `host:port` を `DATABASE_URL` として `.env.local` に書き出す。RDS Proxy・VPC は作らない |
| マイグレーションの権限 | MiniStack の RDS のマスターユーザーは本物のスーパーユーザーになる見込み。本番の `rds_superuser` との差で権限の誤りを見逃さないよう、ローカル専用の準備スクリプトで `rds_superuser` 相当に絞ったロールを作り、`cmd/migrate bootstrap` はそのロールで実行する |
| infra/modules/messaging | バス・ルール・SQS（＋DLQ）・キューポリシー・Scheduler を `messaging.yaml`・`schedules.yaml` から `yamldecode` で作る。環境に依存しないので、将来 dev/stg/prod でもそのまま使う |
| infra/envs/local | provider の endpoints を `http://localhost:4566` に向ける。data・messaging、SES の送信元 ID（`noreply@sp.local`）、Cognito の 3 プール（`AUTH_MODE=cognito` 用）、**CloudWatch Logs のロググループ** `/sp/local/{api,worker-billing,worker-integration,migrate}`（保持 7 日。§8.6.4）。state はローカルファイル（gitignore） |
| testcontainers | `postgres:16-alpine` をダイジェストで固定して直接起動する（テンプレート DB による並列化と起動の速さのため）。**MiniStack の RDS が使うイメージと同じタグに揃える**（MiniStack は `postgres:16-alpine` を起動する。P-IMP-2） |
| `task up` | ① `docker compose up -d --wait` ② `terraform -chdir=infra/envs/local apply -auto-approve`（RDS → MiniStack が PostgreSQL を起動 → キュー・バス・スケジュール）③ `go run ./cmd/migrate bootstrap && up` ④ `go build -o .local/bin/devlogs ./cmd/devlogs` ⑤ `task verify:local`（テストイベントを 1 件発行し、キューに届くこと） |
| その他のタスク | `task dev`（3 プロセスを `go run` で並行起動）、`task dev:container`（profile `app`）、`task test:unit`（`go test ./...`）、`task test:integration`（`go test -tags integration ./...`）、`task e2e`（Playwright の `parallel` と `clock`）、`task e2e:reset`（DB を作り直して migrate し直す。P-TST-4）、`task e2e:selftest`（わざと失敗するテストを流し、ログが添付されることを確かめる。AC-PLT-074）、ログと調査のタスク（`task logs`・`task logs:cwl`・`task logs:clean`・`task db:reader`。§8.6.5）、`task down`、`task reset`（`compose down -v`＋ローカル state 削除）、`task dlq:redrive QUEUE=…`、`task verify:ministack`（下の確認項目を流す。CI でも毎回流す） |

**MiniStack で確かめる項目とフォールバック**（`task verify:ministack` の結果を `.env.local` と設計書の未解決 #9 に記録する）

| 機能 | 既定 | 確かめること | フォールバック |
|---|---|---|---|
| RDS（PostgreSQL 16） | `DB_MODE=rds` | `engine_version` で 16 が選ばれるか（`GRANT … WITH INHERIT FALSE, SET TRUE` に 16 が必要）、RLS・SKIP LOCKED が動くか（AC-PLT-015・017・019 を流す） | `DB_MODE=container`（compose の profile `db`、Terraform は `rds_enabled=false`） |
| EventBridge のルール → SQS | `MESSAGING_MODE=eventbridge` | `prefix` の絞り込み | `MESSAGING_MODE=direct`：`FanoutPublisher` が `messaging.yaml` を読み、EventBridge 形式の本文で `SendMessageBatch` |
| EventBridge Scheduler | `SCHEDULER_MODE=aws`（**ローカルは internal**。P-IMP-1） | タイムゾーン `Asia/Tokyo` → **MiniStack は無視して UTC で評価する（2026-10-24 に確認）** | `SCHEDULER_MODE=internal`：ジョブを持つプロセスが `schedules.yaml` の cron で同じ本文を自分の scheduler キューに送る（以降の経路は同じ） |
| Cognito の JWT | `AUTH_MODE=dev`（PoC の標準） | — | dev のまま。Cognito の検証は単体テスト（JWKS をテスト用サーバーで返す） |
| SES | MiniStack の SES | 送ったメールを取り出す API があるか（integration §11.3 の A） | なければ E2E は DB の `integration.email_notifications` で確かめる（B）。`MAIL_MODE=log` は手動確認用 |
| CloudWatch Logs | MiniStack の CloudWatch Logs | Fluent Bit の `cloudwatch_logs` 出力を受けられるか（→ `ministack-tls` 経由で受けられる）、Logs Insights の `fields`・`filter`・`sort`・`limit` が動くか（→ **`filter` が効かず、`fields` は `@` の項目だけ**。2026-10-24 に確認） | 送り先とロググループの名前の確認だけに使い、調査はファイル（`task logs`）で行う（P-OBS-10）。正はもともとファイル（ADR-009） |
| IAM・VPC | 再現しない | — | dev 環境で確かめる（権限・経路） |

### 8.5 config（環境変数。起動時に全項目を検証し、不足をまとめて表示して終了する）

| 変数 | 例・既定 | 備考 |
|---|---|---|
| `APP_ENV` | `local` / `test` / `dev` / `stg` / `prod` | devtools ビルドで `dev`/`stg`/`prod` なら起動を拒否 |
| `DATABASE_URL` | ローカルは `.env.local`（MiniStack の RDS の `host:port`）。本番は Aurora のライターのエンドポイント（Proxy を入れたらそのエンドポイント） | 必須 |
| `DB_MAX_CONNS` | api 20 / worker 10 | タスク数 × この値の合計を Aurora の接続上限の中に収める |
| `DB_MODE` | `rds` / `container` | ローカルのみ。Terraform と compose の切り替えに使う |
| `AWS_REGION`・`AWS_ENDPOINT_URL` | `ap-northeast-1`・`http://localhost:4566` | SDK 標準の変数。コードで分岐しない |
| `EVENT_BUS_NAME`・`QUEUE_URL_PREFIX` | `sp-events`・… | キュー名から URL を作る |
| `MESSAGING_MODE`・`SCHEDULER_MODE` | `eventbridge`・`aws` | §8.4 |
| `AUTH_MODE` | `dev` | `dev` は devtools ビルドでのみ有効（それ以外で指定したら起動を拒否） |
| `COGNITO_{CONSUMER,STAFF,OPERATOR}_ISSUER`・`…_CLIENT_ID` | — | `AUTH_MODE=cognito` のとき必須 |
| `CURSOR_KEY` | 32 バイトの base64 | 必須 |
| `HTTP_ADDR`・`HEALTH_ADDR` | `:8080`・`:8081` | api は HTTP_ADDR、ワーカーは HEALTH_ADDR（ALB／ECS のヘルスチェック） |
| `WEBHOOK_INSECURE_ALLOWED_HOSTS` | 既定 `localhost,127.0.0.1`。E2E は `webhook-sink` を足す | integration の U3。`APP_ENV=local` 以外で設定したら起動を拒否（旧 `WEBHOOK_ALLOW_INSECURE_LOCALHOST` は廃止） |
| `MAIL_MODE` | `ses`（既定。E2E も MiniStack の SES に送る）／`log` | integration §11.3。テストでは `log` を使わない |
| `LOG_SINKS` | ローカル・CI `stdout,file`／本番 `stdout` | §8.6.1。`file` は `LOG_DIR`（既定 `.local/logs`）に書く。本番で `file` を指定したら起動を拒否 |
| `LOG_DIR` | `.local/logs` | `go run` でもコンテナでも同じパス（compose はリポジトリの `.local/logs` をマウント） |
| `LOG_LEVEL` | `info`（例 `info,billing=debug,platform.db=warn`） | 既定のレベルと、`<module または component>=<level>` の組 |
| `LOG_FORMAT` | `json` | stdout だけ `text` にできる（`file` は常に JSON） |
| `LOG_HTTP_BODY` | ローカル・CI `redacted`／本番 `off` | `redacted` は `APP_ENV` が `local`・`test` のときだけ許す（それ以外は起動を拒否） |
| `DB_SLOW_QUERY_MS` | `200` | `db.slow_query` のしきい値 |
| `SERVICE_NAME`・`SERVICE_VERSION` | `api` など・git のコミット | `service`・`version` の項目。version はビルド時に `-ldflags` で埋め込む |

### 8.6 ログと調査（NFR-017、P-OBS-1〜8、設計書 §1.4・ADR-009）

人とコーディングエージェントが、ログを自分で取り出して不具合の調査・修正・テストに使えるようにする。手順は `docs/runbooks/debugging.md`。

#### 8.6.1 observability（`internal/platform/observability`）

| 項目 | 決定 |
|---|---|
| 形式 | `log/slog` の JSON ハンドラー。1 行 1 レコード。時刻は UTC・ミリ秒。各モジュールは `fmt.Println`・`log.Print*` を使わず、独自の形式も作らない（forbidigo で検出）。ロガーは `Logging.ForModule("<module>")` から受け取り、出すときは必ず `slog.*Context(ctx, …)`（sloglint） |
| 共通の項目 | `ts`・`level`・`msg`・`service`・`version`・`env`・`module`・`component`（http / consumer / job / outbox / provider / db）・`request_id`・`correlation_id`・`causation_id`・`trace_id`・`span_id`（OpenTelemetry を入れるまでは空）・`tenant_id`・`actor{type,id}`・`event_id`・`event_type`・`message_id`・`queue`・`job`・`job_run_id`・`error{code,kind,message}`・`stack`（ローカルと CI だけ）・`duration_ms`。context に入っているものだけを出す。ドメインの ID（`subscription_id`・`invoice_id`・`attempt_id` など）は `WithFields` で足す |
| 出力先（`LOG_SINKS`） | `stdout`：標準出力。`file`：`.local/logs/<service>.jsonl`（service は `api`・`worker-billing`・`worker-integration`・`migrate`）。両方を指定したら同じ内容を両方に書く。ファイルは 1 行ずつ書き込んでフラッシュする（テストの失敗直後に読めるように）。**ローテーション**：50MB を超えたら `<service>.jsonl.1` に名前を変え、古いものを繰り下げて 5 世代まで残す（標準ライブラリだけで実装。Fluent Bit は名前の変更に追従する） |
| レベル（`LOG_LEVEL`） | `info,billing=debug,platform.db=warn` の形。先頭が既定、残りは `module` か `component` ごとの上書き。`ForModule` が返すロガーごとに `slog.LevelVar` を持つ。不正な指定は起動時にエラー |
| 秘密情報の除去（P-OBS-6） | すべての項目と本文に、出力の直前で適用する（`ReplaceAttr` と `Redact`）：①**常に記録しない**：キー名（大文字小文字を区別しない）が `authorization`・`cookie`・`set-cookie`・`x-api-key`・`api_key`・`*token*`・`*secret*`・`password`・`signing_key` の値、および値が `Bearer …`・`sk_…`（API キー）・`whsec_…`（署名鍵）・`tok_…`（カードのトークン）の形のもの → `[REDACTED]` ②**カード番号**：13〜19 桁（空白・ハイフン区切りを含む）の数字列で Luhn を満たすもの → どの項目でも `[REDACTED_PAN]` ③**メールアドレス** → `a***@example.com`（先頭 1 文字とドメインだけ）④**氏名**（キー名 `name`・`full_name`・`customer_name`・`holder_name`・`contact_name`）→ 先頭 1 文字＋`***`。事業者名（`tenant_name`）は対象外 |
| 本文（`LOG_HTTP_BODY`） | `redacted`（ローカル・CI）：`http.request.completed` と `http.outbound.completed` に、要求と応答の本文を JSON として除去を適用したうえで載せる（1 本文 8KB で切り詰め、JSON でなければ大きさだけ）。`off`（本番）：本文を載せない |

#### 8.6.2 標準のログの種類と、出す箇所

| msg | 出す箇所（共通部品） | 主な項目 | レベル |
|---|---|---|---|
| `http.request.completed` | httpx のミドルウェア（1 リクエストに 1 行。認証の前に置き、401 も記録する） | method, route（テンプレート）, status, duration_ms, bytes_in/out, tenant_id, actor, error.code, 本文（`LOG_HTTP_BODY`） | 5xx は error、それ以外は info |
| `http.outbound.completed` | `observability.NewHTTPClient` の Transport（外部への HTTP の共通ラッパー）。**fincode のアダプター・Webhook の送信・SES のクライアント（aws-sdk の `HTTPClient` に渡す）が使う**。モジュールが `http.DefaultClient` や素の `http.Client` を使うことは forbidigo で禁止 | target（`fincode` / `webhook` / `ses`）, method, host, path（クエリなし）, status, duration_ms, attempt, idempotency_key, error.kind（timeout / connection / tls / http_status）, 本文（`LOG_HTTP_BODY`） | 失敗は warn |
| `event.published` | outbox の Relay（送信できた 1 件ごと） | event_type, event_id, aggregate_type, aggregate_id, attempts | info |
| `event.consumed` | sqsconsumer（処理が成功した 1 件ごと。Inbox で重複と判断したものは `duplicate=true`） | event_type, event_id, message_id, queue, consumer, receive_count, duration_ms | info |
| `event.failed` | Relay の送信失敗、sqsconsumer の処理失敗（再試行する） | 上記＋error | warn |
| `event.dlq` | sqsconsumer が DLQ に送った（`Permanent`・受信回数の超過） | 上記＋error | error |
| `job.started` / `job.completed` / `job.failed` | scheduler の Runner | job, job_run_id, trigger, as_of, attempts, cursor_resumed, stats, duration_ms | info / info / error |
| `domain.state_changed` | 各モジュールのリポジトリの保存処理が `observability.StateChanged` を呼ぶ（読み込んだときの状態と保存する状態が違うとき）。状態を持つ集約（Tenant・Subscription・Invoice・PaymentAttempt・CouponRedemption・BillingCycleProcess・BulkCancellationJob・WebhookDelivery・EmailNotification）は必ず呼ぶ | aggregate, id, from, to, reason, version | info |
| `db.slow_query` | pgx の `QueryTracer`（`DB_SLOW_QUERY_MS` を超えたクエリ） | query（sqlc のクエリ名。`-- name:` から取る）, duration_ms, rows。**引数は出さない**（個人情報を避ける） | warn |
| `panic.recovered` | httpx のミドルウェア、sqsconsumer、Runner、Relay の各ループ | component, stack, error | error |
| `dev.clock_changed` | `POST /v1/dev/clock`（devtools のみ） | from, to, offset_seconds, version | info |

#### 8.6.3 相関 ID の引き継ぎ（P-OBS-4）

| 起点 | 相関 ID | 引き継ぎ方 |
|---|---|---|
| API リクエスト | `X-Correlation-Id` を受け取る。形式（`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`）に合わない・ない場合は UUIDv7 を作る。`request_id` は常にサーバーが作る | context に入れ、応答ヘッダー（`X-Request-Id`・`X-Correlation-Id`）と Problem Details（`request_id`・`correlation_id`）に返す。Outbox に書くイベントは `correlation_id`＝相関 ID、`causation_id`＝request_id |
| イベント（購読者） | 受け取った EventEnvelope の `correlation_id` | sqsconsumer が context に戻す（`causation_id` は受け取った event_id）。購読者が出す次のイベントも同じ相関 ID を持つ |
| スケジュールのジョブ | `job:<job>:<run_id>` | Runner が context に入れる。ジョブが出すイベントとログに付く |
| dev のジョブ手動実行・ops の再実行 | 呼び出し側の `X-Correlation-Id` | scheduler キューのメッセージに `correlation_id` を載せ、Runner がそれを使う（`job_run_id` は別の項目で残る） |
| dev clock | 呼び出し側の `X-Correlation-Id` | `dev.clock_changed` のログに付く |
| E2E テスト | `e2e-<testId>-<乱数 6 文字>` | Playwright の fixture がすべての API 呼び出しに付ける（§10.2） |

#### 8.6.4 ローカルでの集約（Fluent Bit → MiniStack の CloudWatch Logs。P-OBS-3）

| 項目 | 決定 |
|---|---|
| compose | サービス `fluent-bit`（公式イメージ、タグを固定）。`./.local/logs` を読み取り専用でマウントし、設定は `deploy/local/fluent-bit.conf`。読み取り位置は名前付きボリュームの DB に保存する |
| 入力 | `tail`（`Path /logs/*.jsonl`、`Parser json`、`Rotate_Wait 5`、`Refresh_Interval 1`） |
| 出力 | `cloudwatch_logs`（`endpoint` は MiniStack、`log_group_template /sp/local/$service`、`log_stream_prefix local-`、`auto_create_group Off`。ロググループは Terraform が作る）。認証情報はダミー |
| 名前の付け方 | 本番の `/sp/<env>/<service>` と同じ。本番は ECS の FireLens（Fluent Bit）が標準出力を送る（本番インフラの範囲） |
| 正とするもの | ファイル（`task logs`）。CloudWatch Logs は、本番と同じ形のクエリを確かめるためのもの（ADR-009） |

#### 8.6.5 CLI とタスク（P-OBS-7。`docs/runbooks/debugging.md` が使うコマンドはすべてここにある）

| コマンド | 内容 |
|---|---|
| `cmd/devlogs`（`devlogs`） | `.local/logs/*.jsonl`（ローテーション済みの `.jsonl.N` を含む）を読み、条件で絞って `ts` の順に 1 本に並べる。標準ライブラリだけ。**フラグ**：`--corr <相関ID>`、`--req <request_id>`、`--tenant <tenant_id>`、`--level <最低レベル>`、`--service <名前>`（複数可）、`--msg <前方一致>`（例 `job.`）、`--since <期間 例 10m または RFC 3339>`、`--errors`（error 以上か `error` を持つ行）、`--format=timeline｜json`（既定 timeline）、`--dir`（既定 `.local/logs`）。**timeline**：先頭行に条件（相関 ID）、以後 `HH:MM:SS.mmm service LEVEL msg 主な項目` の 1 行ずつ。**json**：元の行をそのまま（jq に渡せる）。サブコマンド `devlogs cwl '<クエリ>' [--since 1h]`：MiniStack の CloudWatch Logs の `/sp/local/*` に Logs Insights のクエリ（`fields`・`filter`・`sort`・`limit`）を流して結果を表示する |
| `task logs -- <フラグ>` | `.local/bin/devlogs` を実行する（なければ `go run ./cmd/devlogs`） |
| `task logs:cwl -- '<クエリ>'` | `devlogs cwl` を実行する |
| `task logs:clean` | `.local/logs/` を空にする（Fluent Bit の読み取り位置も消す） |
| `task db:reader` | `sp_e2e_reader` で psql を開く（接続文字列は `.env.local` の `E2E_READER_URL`。ローカルに psql がなければ `postgres:16` のコンテナの psql を使う） |
| `task test:unit`・`task test:integration`・`task e2e` | §8.4 |
| CI | E2E のジョブ（§10.2 の ③）は、成否に関係なく `.local/logs` を成果物 **`local-logs`** として保存する |

## 9. 異常系・境界値・競合

| # | 事象 | 扱い |
|---|---|---|
| E1 | `InTx` にスコープがない | `ErrNoScope`。SQL は 1 文も実行しない。HTTP では 500 `internal_error`（バグ扱い）、ログは error |
| E2 | 他テナントの tenant_id で INSERT | RLS の WITH CHECK で拒否（42501）→ 500。モジュールのバグとして検出される |
| E3 | モジュールが他スキーマを読む | `set_config('role', …)` で切り替えたロールに権限がなく 42501 |
| E4 | PutEvents の一部失敗（FailedEntryCount > 0） | 成功分だけ `published_at` を入れ、失敗分は再試行。10 回失敗したら error ログ（イベントを捨てない） |
| E5 | EventBridge／SQS が止まっている | Outbox に溜まり続け、復旧後に Relay が送る。業務のトランザクションは止めない |
| E6 | Relay が PutEvents の後、コミット前に落ちた | 再送されて重複する（at-least-once）。Inbox で除く |
| E7 | 本文の解析に失敗・envelope の必須項目がない | `Permanent` → DLQ へ送り、元を削除 |
| E8 | ハンドラが panic | recover してエラー扱い（再試行）。スタックをログに出す |
| E9 | ハンドラが可視性タイムアウトより長い | 延長し続ける。延長に失敗したら処理は続け、二重受信は Inbox で吸収 |
| E10 | シャットダウン（SIGTERM。デプロイ・スケールイン） | 受信を止め、処理中を最大 30 秒待つ（ECS の `stopTimeout` はこれより長くする）。終わらないものは可視性切れで再配信。Relay は周回の途中で止めても、SKIP LOCKED とトランザクションで取りこぼさない |
| E10a | FIFO の受信バッチの途中で失敗 | 失敗したメッセージと、同じグループの後ろのメッセージを削除しない。別グループは処理を続ける（§5） |
| E10b | 件数の多いジョブの途中でタスクが落ちた | 再配信で、保存済みのカーソルから再開する（§8.3） |
| E10c | タスク数が増えて DB の接続が足りない | タスク数の上限 × `DB_MAX_CONNS` を接続上限の中に収める。足りなくなったら RDS Proxy を入れる（§10）。接続待ちのタイムアウトは 503 `service_unavailable`／再配信 |
| E11 | スケジューラーが同じジョブを 2 回送る | `job_runs` の部分ユニークで 2 回目は何もせず成功扱い |
| E12 | 同じジョブを dev で `force=true` で連打 | 同時に走りうる。ジョブ自体の冪等性で守る（ジョブの仕様の前提） |
| E13 | dev clock を戻す | 422 `clock_backward_not_allowed`。戻したいときは `task reset` |
| E14 | dev clock を進めた直後に処理中のメッセージ | 最大 1 秒は前の時刻で処理されうる。E2E は応答の `effective_after` を過ぎてから次の操作をする（Playwright の `clock` fixture の `advance` が待つ。P-ARC-8） |
| E15 | Idempotency の保存（別トランザクション）が業務のコミット後に失敗 | 再送時にハンドラがもう一度動く。業務側の冪等性（M4）で守る。発生はログに残す |
| E16 | JWT：期限切れ・iss/aud 不一致・`alg: none`・HS256・署名不正 | すべて 401 `unauthenticated`。理由は詳細ログのみ |
| E17 | staff が `X-Tenant-Id` のテナントに所属していない／ヘッダーなし | 403 `forbidden`／400 `validation_failed` |
| E18 | 停止中テナントの staff の書き込み | 403 `tenant_not_active`（identity の定義） |
| E19 | 失効した API キー | 401 `unauthenticated` |
| E20 | `Money` の負・桁あふれ、存在しない日付 | 生成関数がエラーを返す。`MulRatFloor` の前提違反は panic（プログラムの誤り） |
| E21 | 月末・うるう年 | 1-31 → 2-28/2-29 → 3-31。2-29 開始の年額 → 平年 2-28。12-31 の年越し（月額）→ 翌 1-31 |
| E22 | JST の日付境界 | 15:00:00Z は翌日、14:59:59Z は当日（UTC で保存、JST で解釈。NFR-016） |
| E24 | `AuditWriter` の書き込みに失敗（業務はコミット済み） | 応答はそのまま返し、監査の内容を含めて error ログに出す（PoC。将来は Outbox 経由にして取りこぼしをなくす、§10） |
| E25 | 変更系で 5xx・401・400 | 監査に記録しない（変更は起きていない／主体が不明） |
| E23 | ログに秘密情報・個人情報 | §8.6.1 の除去を出力の直前で適用する。除去の規則に漏れが見つかったら、それ自体を不具合として直す（runbook の注意） |
| E26 | ログのファイルの書き込みに失敗（ディスクがいっぱいなど） | stdout への出力は続け、ファイルの sink は 1 分ごとに再オープンを試す。処理は止めない |
| E27 | 相関 ID が形式に合わない（長すぎる・使えない文字） | 受け取らずに新しく作る。受け取った値は捨てる（ログの注入を防ぐ） |

## 10. 拡張ポイント

| 拡張 | 将来 | PoC での扱い |
|---|---|---|
| 認証方式 | Cognito（本番）、MFA、IP 制限 | `TokenVerifier` を差し替えるだけ。`CognitoVerifier` は作る |
| メッセージング | 別のバス・キュー、FIFO の追加 | `Publisher`・`CommandSender` の interface。トポロジーは YAML の追記だけ |
| 本番インフラ | dev/stg/prod（CloudFront＋WAF → ALB → ECS、Aurora） | `infra/modules/messaging`・`data` をそのまま使う。`network`・`compute`・`edge` は作らない |
| 監視 | OTel・メトリクス・DLQ アラーム（NFR-013） | ログの項目名を OTel の慣習に合わせ、`trace_id`・`span_id` を用意（今は空）。本番は FireLens で CloudWatch Logs に送り、同じクエリが使える。分散トレースの可視化・ダッシュボード・Athena は作らない（P-OBS-8） |
| RDS Proxy | 接続数が足りなくなったときに入れる（任意。P-ARC-11 ⑤） | pgxpool で接続を使い回す。スコープの設定は `set_config` の 1 文で `SET` 文を使わないので、後から Proxy を入れてもピン留めを起こしにくい |
| Outbox の遅延短縮 | プロセスをまたぐキック（`pg_notify` など） | 同じプロセスの中だけ `AfterCommit` で `Relay.Kick()`（任意）。api が書いたイベントはポーリング（最大 500ms）で送る |
| モジュールの切り出し | 別サービス化 | スキーマ・ロール・Outbox がモジュールごとに分かれているので、DB 移行だけで済む |
| レート制限 | テナント・IP ごと | httpx のミドルウェアの差し込み口（`RouterConfig.Middlewares`）。本番は WAF のレートベースルール |
| イベントのスキーマ変更 | payload の互換性のない変更 | event_type を新しくする（`…_v2`）。envelope には項目を足すだけ（受け手は未知の項目を無視） |
| 監査の確実性 | 業務と同じトランザクションで記録（Outbox 化）、S3 Object Lock | `AuditWriter` の interface はそのまま。PoC は応答前の別トランザクションで書く |
| 集計（Reporting） | worker-projection と集計用キュー | 作らない（P-PLT-4）。`messaging.yaml` への追記とプロセスの追加だけで足せる |
| 実行基盤の差し替え | 一部を別の基盤で動かす | 受信は `Dispatch`（共通）と `sqsconsumer`（駆動部分）に分けてあるので、駆動部分を足すだけで済む |
| 読み取りの分散 | Aurora リーダー | `ModuleDB.ReadTx` を将来リーダーの pool に向ける |

### 10.1 リポジトリの規約（lint・生成・CI）

| 項目 | 決定 |
|---|---|
| depguard（段階 2） | ⓪ `internal/platform/auditctx/**`：std と sharedkernel のみ ① `modules/*/contracts/**`：std と sharedkernel のみ ② `modules/*/internal/domain/**`：std・sharedkernel・自モジュールと他モジュールの contracts のみ（pgx・aws-sdk・net/http・platform を禁止）③ `internal/sharedkernel/**`：std と google/uuid のみ ④ `internal/platform/**`：`modules/**` を禁止 ⑤ `modules/**`：他モジュールの `internal` を禁止（コンパイラと二重） |
| archtest（段階 2・3 の補完、P-PLT-3） | `go test ./internal/archtest` が `golang.org/x/tools/go/packages` で import グラフを調べる。「`modules/X` のルートパッケージを import できるのは `cmd/**`（実際には `cmd/internal/wiring`）だけ」と、下のモジュールごとの許可リストを完全一致で検査する（depguard は前方一致なので表せない） |
| 許可リスト（段階 3） | subscription → catalog, customer ／ billing → tenant, payments ／ payments → customer ／ catalog → tenant（P-CAT-1）／ orchestration → subscription, billing, payments, catalog, customer, tenant ／ integration → customer, tenant（P-INT-1）, subscription, billing（イベントの型）／ audit → なし（`cmd/internal/wiring` が `AuditWriter` として注入するだけ）／ tenant・customer → なし。いずれも `contracts` のみ |
| golangci-lint（v2） | depguard, exhaustive, errcheck, errorlint, gosec, govet, staticcheck, wrapcheck（contracts 境界のみ）, bodyclose, noctx, sqlclosecheck, sloglint（`context: all`）, forbidigo |
| forbidigo | `time.Now`（許可：`internal/sharedkernel`・`internal/platform`・`_test.go`）、`time.LoadLocation`・`time.Local`、`fmt.Print*`、`log.Print*`、`float32/64` を使った金額計算は検出できないのでレビューで見る |
| コード生成 | `task generate` = sqlc（uuid → `google/uuid`、date → `sharedkernel.Date`、adapter で ID 型に変換）＋ oapi-codegen（strict server、std net/http。spec は `api/openapi/<audience>/<module>.yaml`＋`common.yaml`、出力は `modules/<m>/internal/adapter/http/gen/<audience>`）＋ errcodes ＋ TS クライアント（audience ごとに bundle して openapi-typescript）。ツールは `go.mod` の `tool` ディレクティブと pnpm の lockfile でバージョン固定 |
| CI | 3 つのジョブに分ける（§10.2 の CI の表。P-TST-7） |

### 10.2 テスト戦略（P-TST-1〜9）

**原則：モックを使わない**。DB は本物の PostgreSQL（E2E は MiniStack の RDS、結合テストは同じバージョンの testcontainers）、AWS のサービスは MiniStack、プロセス（api・worker-billing・worker-integration）は実際に起動する。contracts の interface を手書きのフェイクで差し替えることはしない（設計書 §1.2 の記述は PdM 側で変更）。

**例外は、自分たちで制御できない外部の 4 つだけ**（P-TST-2）

| # | 外部 | 置き換え | 中身 |
|---|---|---|---|
| 1 | fincode | StubAdapter | `PaymentProvider` の本物の実装の 1 つ。トークンのシナリオと dev の挙動上書きで結果を切り替える（payments の spec） |
| 2 | 事業者の Webhook の送信先 | `webhook-sink` コンテナ | 受け取ったリクエストを記録し、指示で失敗・遅延を起こす（§10.3） |
| 3 | 認証（Cognito） | 開発用 JWT（`AUTH_MODE=dev`） | 本番と同じ検証コードを通る。発行は `POST /v1/dev/tokens` |
| 4 | 時刻 | dev clock | `POST /v1/dev/clock`・`POST /v1/dev/jobs/{job}/run` |

**3 つの層**

| 層 | 書くもの | 実行方法 | 依存 |
|---|---|---|---|
| UT（`unit`） | DB も外部も使わない純粋なロジックで、分岐や計算が多いものだけ。Go のテーブル駆動テスト（P-TST-5）。Platform の対象：sharedkernel の計算（期間・更新日・税・割引・日付）、JWT の検証、カーソルの暗号化、Problem Details の組み立て、`Router.Mount` の拡張属性の検査、`Dispatch` の振り分けと FIFO の順序（ハンドラは入力として渡す関数で、依存の差し替えではない）、`auditctx` のマスキング、YAML・`errors.yaml` の検証器。lint・生成の差分・archtest の静的検査もこの層のジョブで流す | `go test ./...`（タグなし） | なし |
| 結合テスト（`integration`） | E2E では狙って起こせないものだけを、本物の PostgreSQL で（P-TST-6）。Platform の対象：テーブルごとの RLS とモジュールロール、並行処理（Relay の SKIP LOCKED、Idempotency の同時リクエスト、`orc-collection` の同時実行数）、Outbox・Inbox、`sqsconsumer`（可視性の延長・DLQ）、ジョブの重複排除とカーソルからの再開、マイグレーション。処理の対象にするハンドラ・ジョブ・ルートは、テストの中で登録するテスト用のもの（本物の部品を差し替えるものではない） | `go test -tags integration ./...` | testcontainers：`postgres:16`（MiniStack の RDS と同じタグ・できればダイジェスト。パッケージごとに 1 コンテナ、テストごとにテンプレート DB から複製）、EventBridge・SQS が要るものは MiniStack コンテナ |
| E2E（`e2e`） | API とプロセスをまたぐ振る舞いのすべて。2 種類に分ける（P-TST-8）：課金の流れを通すシナリオ（E2E-001〜022、`tests/scenarios/`）と、ドメインごとの API テスト（各 TL の e2e の受け入れ条件、`tests/<module>/`）。**PoC の完了条件は両方**。確かめる中心は API のレスポンスで、API から見えない不変条件（二重課金がない、Outbox が送信済み、監査の記録など）は `sp_e2e_reader` で DB を読んで確かめる（P-TST-3） | Playwright（TypeScript）。`e2e/`（pnpm workspace の 1 つ） | compose（MiniStack・`webhook-sink`・3 プロセス）＋ Terraform（local）＋ migrate（devtools を含む） |

**`e2e/` の構成**

```
e2e/
├── package.json            # @sp/e2e。依存: @playwright/test, pg, @sp/api-client（web/packages/api-client）
├── playwright.config.ts
├── .env.e2e                # task up が書き出す（API の URL、sink の URL、sp_e2e_reader の接続文字列）
├── fixtures/
│   ├── index.ts            # test = base.extend<{world, tokens, clock, wait, db, sink, mail, logs, api}>()
│   ├── api.ts  tokens.ts  world.ts  clock.ts  wait.ts  db.ts  sink.ts  mail.ts  logs.ts
├── selftest/               # わざと失敗するテスト（task e2e:selftest だけが流す。parallel・clock・perf の対象外）
├── helpers/                # isolation.ts（別テナントで 404 を確かめる）、webhook-signature.ts（integration §4.3 の検証）
└── tests/                # P-TST-8：2 種類に分ける
    ├── scenarios/          # 課金の流れを通すシナリオ E2E-001〜022（TL-Orchestration）
    │   ├── e2e-001-signup-card.spec.ts
    │   └── e2e-007-renewal-dunning.clock.spec.ts
    ├── identity/  catalog/  subscription/  billing/  payments/  integration/  audit/
    └── platform/           # ドメインごとの API テスト（各 TL の e2e の受け入れ条件。platform は本書の AC）
        ├── auth.spec.ts
        └── dev-clock.clock.spec.ts
    # 計測（P-TST-10）：tests/scenarios/e2e-020-renewal-throughput.perf.ts
```

**ファイル名の規約（P-TST-9）**

| 規約 | 内容 |
|---|---|
| 時刻を進めるテスト | dev clock を進める、または時刻に依存するジョブを手動で実行するテストは、ファイル名の末尾を **`.clock.spec.ts`** にする |
| 混在の禁止 | 時刻を進めるテストと進めないテストを、同じファイルに混ぜない。`.clock.spec.ts` 以外のファイルで `clock` fixture の `advance`・`setTo`・`runJob` を呼ぶと例外になり、`task lint` でも検出する |
| 適用範囲 | `tests/scenarios/` と `tests/<module>/` の両方（P-TST-8） |
| 計測のテスト | 末尾を **`.perf.ts`** にする（`.spec.ts` ではないので `parallel`・`clock` には拾われない）。`perf` project だけで流す（P-TST-10） |

| 設定（`playwright.config.ts`） | 値 |
|---|---|
| project `parallel` | `testMatch: /\.spec\.ts$/`、`testIgnore: /\.clock\.spec\.ts$/`（`tests/scenarios/` と `tests/<module>/` の両方）。`fullyParallel: true`、workers はローカル 50%・CI 4。申込ページのテストは同じ project で Chromium を使う |
| project `clock` | `testMatch: /\.clock\.spec\.ts$/`（両方のディレクトリ）。**workers = 1**、`fullyParallel: false`、`dependencies: ['parallel']`（parallel がすべて終わってから直列に流す。dev clock はシステム全体で 1 つなので、並列のテストと同時に時刻を進めない。P-TST-4・P-TST-9） |
| project `perf` | `testMatch: '**/*.perf.ts'`、workers = 1、テストのタイムアウト 60 分。**既定の実行には含めない**（`pnpm e2e` は `--project=parallel --project=clock`。`pnpm e2e:perf` で `--project=perf` だけを流し、CI では手動実行のワークフロー）。dev clock を動かすので、`clock` と同時には流さない。E2E-020（TL-Orchestration）が使う |
| タイムアウト | テストごと 60 秒（`clock` は 180 秒）、`expect` 10 秒 |
| リトライ | 0（不安定なテストを隠さない） |
| 記録 | `trace: 'retain-on-failure'`、`screenshot: 'only-on-failure'`、reporter は `html` と `junit` |
| API の型 | `@sp/api-client`（OpenAPI から生成した TS のクライアント）を Playwright の APIRequestContext の上で使う。手書きの型を作らない |

**共通の fixture**

| fixture | 提供するもの | 規則 |
|---|---|---|
| `api` | audience ごとの型付きクライアント（me・console・tenant・ops・checkout・dev）。**すべての呼び出し（dev のジョブ手動実行・dev clock を含む）に `logs` fixture の相関 ID を `X-Correlation-Id` で付ける**。ブラウザのテストでは `page.route` で API への要求に同じヘッダーを付ける | ベース URL は `.env.e2e` |
| `tokens` | `issue({pool, sub?, email?, groups?, ttl?})` → 開発用 JWT（`POST /v1/dev/tokens`）。sub・email は省略するとテストごとに一意な値 | — |
| `world` | `createTenant({approve?: true})`（ops で作成・承認、owner のスタッフと staff のトークン、API キーを発行）、`createStaff(tenant, role)`、`createConsumer()`（consumer のトークン → `GET /v1/me` で JIT 作成）、`createPaymentMethod(consumer, scenario)`（StubAdapter のトークン）、`seedSubscriptions(tenant, plan, count)`（`POST /v1/dev/seed/subscriptions` → 完了まで待つ。`perf` と、件数の要るテストで使う） | **各テストは自分専用のテナント・契約者を作る**。他のテストのデータを参照しない。名前にテスト ID を入れる。後片付けはしない（CI は毎回まっさらな環境） |
| `clock` | `advance(duration)`（`effective_after` を過ぎるまで待つ）、`setTo(time)`、`runJob(name, {asOf?, params?, force?})`（`wait` 付きで完了まで待ち、`failed` なら例外） | `.clock.spec.ts` 以外のファイル（＝`clock` project の外）で使うと例外にする（`testInfo.file` で判定。P-TST-9） |
| `wait` | `until(fn, {timeout, interval})`：fn が真になるか値を返すまで polling。既定は間隔 200ms・上限 15 秒（`clock` project は 60 秒）。上限に達したら最後の値を添えて失敗。`untilEvent(type, {aggregateId})`：その集約の Outbox に該当イベントがあり送信済みになるまで待つ（db を使う） | 固定の sleep を禁止する（lint で `waitForTimeout` を検出） |
| `logs` | 自動で使われる fixture（`{auto: true}`）。テストごとに相関 ID `e2e-<testId>-<乱数 6 文字>` を作り、`api` に渡す。**テストが失敗したら**、`.local/bin/devlogs --corr <ID>` の `--format=json` と `--format=timeline` の結果を、`logs.jsonl` と `timeline.txt` としてレポートに添付する。あわせて、テストの開始時刻からの `--errors` の結果を `errors.txt` として添付する（相関 ID が途切れた場合の手がかり）。テストの中から `logs.query(filter)` で自分のログを読める（ログの仕組み自体の受け入れ条件で使う） | 添付の前に 500ms 待ち、プロセスの書き込みを取りこぼさない |
| `db` | `query<T>(sql, params)`（`sp_e2e_reader` で接続。読み取り専用）、よく使う確認のヘルパー（例 `countSucceededAttempts(invoiceId)`、`auditRecords(tenantId)`） | 書き込みはできない（ロールで保証）。DB の確認は API で見えない不変条件に限る |
| `sink` | `create()`・`setBehavior()`・`requests()`・`waitForRequests()`・`stats()`（§10.3。WS-1〜8） | `sink_id` はテストごとに作る |
| `mail` | `notifications({dedupeKey? , templateKey?, customerAccountId?})`：`sp_e2e_reader` で `integration.email_notifications` を読む（status・dedupe_key・template_key・variables・subject・provider_message_id）。`waitForSent(dedupeKey)`。MiniStack の SES から取り出す API が確認できたら `sesMessages()` を足す（integration §11.3 の A。要検証） | メールの確認は DB の行で行うのが既定 |

**CI（3 つのジョブ。P-TST-7）**

| ジョブ | 内容 | 失敗時に残すもの |
|---|---|---|
| ① lint と UT | `task generate && git diff --exit-code`（`messaging.yaml`・`schedules.yaml`・`errors.yaml` の検証を含む）、`task lint`（golangci-lint・depguard）、archtest、`go test ./...`、`pnpm -r lint typecheck`（`e2e/` を含む） | テストの出力 |
| ② 結合テスト | `go test -tags integration ./...`（testcontainers で `postgres:16` と MiniStack） | テストの出力・コンテナのログ |
| ③ E2E | `docker compose --profile app up -d --wait`（MiniStack・`webhook-sink`・devtools ビルドの 3 プロセス）→ `terraform -chdir=infra/envs/local apply` → `cmd/migrate bootstrap && up`（devtools のマイグレーションを含む）→ `devlogs` のビルド → 3 プロセスの `/healthz`・`/readyz` を待つ → `task verify:ministack`・`task verify:local` → `pnpm --filter @sp/e2e exec playwright test`（`tests/scenarios/` と `tests/<module>/` の全部を、`parallel` → `clock` の順に流す。レポートは種類（scenarios／モジュール）ごとに分けて表示）→ `task e2e:selftest` | 成果物 **`playwright-report`**（HTML レポート・トレース・スクリーンショット。失敗したテストには `logs.jsonl`・`timeline.txt` が添付される）、`.local/logs`（成果物 `local-logs`。成否に関係なく保存）、全コンテナのログ |

- PoC の完了条件は、③でシナリオ（E2E-001〜022）とドメインごとの API テストがすべて通ること（憲章 §5・P-TST-8）。①②も通すこと。
- ③は実行ごとにコンテナを新しく起動するので、データは毎回まっさらな状態から始まる。ローカルでは `task e2e:reset` で作り直せる。

**テナント分離テストの共通の仕組み**

| 層 | 仕組み | 失敗の条件 |
|---|---|---|
| DB（メタ）・integration | `AssertAllTenantTablesHaveRLS(t, db)`：`tenant_id` 列を持つ全テーブルについて、RLS の ENABLE・FORCE と `tenant_isolation` ポリシーを pg_catalog で確認 | 新しいテーブルに RLS を付け忘れたら落ちる |
| DB（振る舞い）・integration | `tenancy.Run(t, seed, query)`：テナント A・B を作り、A のデータを入れ、B のスコープで読む・更新する | B のスコープで 1 行でも見えたら落ちる |
| HTTP・e2e | `tests/<module>/` の Playwright のテストで `helpers/isolation.ts`：`expectIsolated(op, ownerWorld, otherWorld)` で「B の主体で A のリソース ID を指定 → 404」を確かめる。テストには `@isolation:<operationId>` のタグを付ける | — |
| 宣言と実テストの突き合わせ・unit | `x-sp-isolation: tested` の全 operationId に、`@isolation:<operationId>` のタグのテストがあるかを、OpenAPI と `e2e/tests` を読む検査（`task lint`）で確かめる | 宣言だけでテストがなければ落ちる |

### 10.3 `webhook-sink`（テスト用の Webhook 受信コンテナ。P-TST-2 ②）

要件は integration.md §11.2 の **WS-1〜8 を正とする**。本書は実装する側として、構成・API のパス・fixture を決める。

| 項目 | 実装 |
|---|---|
| 置き場所 | `tools/webhook-sink`（Go の標準ライブラリだけ。本番のビルドには含めない）。状態はメモリだけ（再起動で消える） |
| WS-8 コンテナ | compose のサービス名 **`webhook-sink`**、HTTP だけ、コンテナ内ポート **8080**、ホストへの公開ポート **18080**（api の 8080 と衝突しないため）。送信先の URL は CI（プロセスがコンテナ）では `http://webhook-sink:8080/hooks/{sink_id}`、ローカル（`go run`）では `http://localhost:18080/hooks/{sink_id}`。worker-integration と api に `WEBHOOK_INSECURE_ALLOWED_HOSTS=localhost,127.0.0.1,webhook-sink` を渡す（§8.5） |
| WS-1 受信 | `POST /hooks/{sink_id}[/任意のパス]` |
| WS-2 記録 | `sink_id`・`seq`・`received_at`（sink の実時刻、RFC 3339 ミリ秒）・`method`・`path`・`headers`（すべて）・`body_base64`・`body_text`・`response{status, rule}` |
| WS-3 最大同時数 | 受け取った瞬間の同じ `sink_id` の処理中の数を数え、`max_in_flight` を保持する |
| WS-4 指示 | `PUT /_control/sinks/{sink_id}/behavior` 本文 `{rules: [...]}`。規則は WS-4 の 6 種類（`{status,times}`・`{delay_ms,status,times}`・`{hang:true,times}`・`{close:true,times}`・`{status:302,location,times}`・`{status:200,body_bytes,times}`）。使い切ったら 200・`ok` |
| WS-5 取得・消去 | `GET /_control/sinks/{sink_id}/requests?after=<seq>&wait_ms=<最大 10000>`（連番順。`wait_ms` は新しい記録が来るまで待つ long polling。WS-5 への追加）、`GET /_control/sinks/{sink_id}/stats` → `{count, max_in_flight}`、`DELETE /_control/sinks/{sink_id}`。死活は `GET /_control/healthz` |
| WS-6 回数で指示 | 時刻による指示の API は作らない（dev clock と sink の時刻は無関係） |
| WS-7 署名 | sink は検証しない。テスト側の `e2e/helpers/webhook-signature.ts`（`node:crypto` で HMAC-SHA256。integration §4.3 の仕様から独立に実装）で検証する |

**Playwright の fixture `sink`**

| メソッド | 内容 |
|---|---|
| `sink.create()` | テストごとに一意な `sink_id` を作り、`{sinkId, url}` を返す（`url` は CI とローカルで切り替わる。`.env.e2e` の `SINK_TARGET_BASE`）。テスト終了時に `DELETE` |
| `sink.setBehavior(sinkId, rules)` | WS-4 |
| `sink.requests(sinkId, {after?})`・`sink.waitForRequests(sinkId, n, {timeout?})` | WS-5（long polling を使い、上限は `wait` fixture と同じ） |
| `sink.stats(sinkId)` | `{count, max_in_flight}`（WS-3） |
| `verifySignature(request, secret)` | `helpers/webhook-signature.ts` を呼ぶ（WS-7） |

## 11. 受け入れ条件

レベル：`unit`＝純粋なロジックの Go のテスト（lint・生成の差分・archtest の静的検査を含み、CI の ① で流す）、`integration`＝本物の PostgreSQL（と MiniStack）を使う Go のテスト（CI の ②）、`e2e`＝Playwright（CI の ③）。フェイク・モックを前提にした条件は置かない（§10.2）。

| ID | Given | When | Then | 要件 | レベル |
|---|---|---|---|---|---|
| AC-PLT-001 | `ID[SubTag]` を受ける関数 | `ID[InvTag]` を渡すコードをビルドする | コンパイルエラー（testdata のパッケージを go/types で検査） | NFR-006 | unit |
| AC-PLT-002 | — | `NewID` を 1,000 回／`ParseID("xyz")` | すべて version 7 で単調増加／`ErrInvalidID` | 憲章 §5 | unit |
| AC-PLT-003 | — | `NewMoney(-1)`／`100.Sub(101)`／`100.SubClamped(101)` | `ErrNegativeMoney`／`ErrNegativeMoney`／0 円 | D-08, D-10 | unit |
| AC-PLT-004 | 税率 10% | `InclusiveTax` に 1,100／1,000／1／1,000,000 円 | 100／90／0／90,909 円（D-09） | D-09, NFR-012 | unit |
| AC-PLT-005 | — | `ParseTaxRate("0.100")`／`("1.000")`／`("-0.1")` | 1/10／エラー／エラー | D-09 | unit |
| AC-PLT-006 | percent 15 ／ amount 500 | `Apply` に 980 円／300 円 | 割引 147・残り 833 ／ 割引 300・残り 0 | D-10 | unit |
| AC-PLT-007 | — | `DateOf(2026-10-11T15:00:00Z)`／`(…14:59:59Z)` | 2026-10-12／2026-10-11 | NFR-016 | unit |
| AC-PLT-008 | anchor 31 | 2027-01-31 から `AddMonthsClamped(1..3, 31)`／2028-01-31 から 1 か月 | 2027-02-28, 03-31, 04-30／2028-02-29 | FR-019 | unit |
| AC-PLT-009 | 2027-01-31 開始の月額（anchor 31） | `FirstPeriod` から `Next` を 3 回 | 期間 01-31〜02-27, 02-28〜03-30, 03-31〜04-29, 04-30〜05-30。更新日 02-28, 03-31, 04-30, 05-31 | FR-019, D-11 | unit |
| AC-PLT-010 | 2028-02-29 開始の年額（anchor 29） | `Next` を 4 回 | 更新日 2029-02-28, 2030-02-28, 2031-02-28, 2032-02-29 | FR-019 | unit |
| AC-PLT-011 | 2026-10-15 申込・トライアルなし・月額 | `FirstPeriod(10-15, month, 15)` | 10-15〜11-14、`NextStart` = 11-15 | D-11 | unit |
| AC-PLT-012 | 2026-10-01 申込・trial_days 14 | `DaysPeriod(10-01, 14)` | 10-01〜10-14、`NextStart` = 10-15（初回課金日、anchor 15） | D-12, FR-014 | unit |
| AC-PLT-013 | 任意の開始日（1900〜2100 年）・anchor | `Next` を 100 回（fuzz） | 期間が隙間なく連続し、各開始日の日 = min(anchor, 月の日数) | FR-019 | unit |
| AC-PLT-014 | tenant スコープ | fn が業務の行と Outbox を書いた後にエラーを返す | どちらも残らない | FR-020 | integration |
| AC-PLT-015 | テナント A・B のデータ | B のスコープで A の行を SELECT／A の tenant_id で INSERT | 0 行／42501 で拒否 | NFR-008 | integration |
| AC-PLT-016 | スコープを設定しない context | `InTx` | `ErrNoScope`、fn は呼ばれない | NFR-008 | integration |
| AC-PLT-017 | `tenant_id` 列があり RLS がないテーブルを作る | `AssertAllTenantTablesHaveRLS` | テストが失敗する | NFR-008 | integration |
| AC-PLT-018 | billing の `ModuleDB` | `subscription.subscriptions` を SELECT | 42501（permission denied） | NFR-008 | integration |
| AC-PLT-019 | 3 スキーマに未送信 1,000 件（各 334 件）、MiniStack の EventBridge → SQS | Relay を 2 つ同時に動かす | SQS に全件届き（event_id で重複 0）、全件 `published_at` あり | FR-020, NFR-005 | integration |
| AC-PLT-020 | 未送信 10 件、EventBridge に届かない状態（到達できないエンドポイント。MiniStack は存在しないバスへの PutEvents も受け付けるため。P-IMP-4） | Relay の 1 周 → バスを作り直して次の周回 | 1 周目は 10 件とも `attempts=1`・`next_attempt_at` が未来で、業務の行はそのまま。作り直した後の周回で 10 件とも送信済み | NFR-005 | integration |
| AC-PLT-021 | 同じ event_id のメッセージを MiniStack の SQS に 2 回送る | テスト用の購読者（行を 1 つ書く）を `inbox.Wrap` で動かす | 業務の書き込みは 1 回、2 回目は成功扱いで削除される | FR-020, NFR-006 | integration |
| AC-PLT-022 | 常にエラーを返すテスト用の購読者（max_receive 5） | `sqsconsumer` で処理 | 削除されず、バックオフ付きで再配信され、5 回で DLQ に移る。その間 `processed_messages` は増えない | NFR-005 | integration |
| AC-PLT-023 | VT 10 秒のキュー | 処理に 25 秒かかるテスト用の購読者 | 処理中に再配信されない（呼び出しは 1 回） | FR-020 | integration |
| AC-PLT-024 | 解析できない本文 | 受信 | すぐ DLQ に移り、元のキューから消える | NFR-013 | integration |
| AC-PLT-025 | `world` で作ったテナントと 4 種類の主体 | 4 入口それぞれを、正しい主体／別の主体／トークンなしで呼ぶ | 200 系／403 `forbidden`／401 `unauthenticated` | FR-011, NFR-009 | e2e |
| AC-PLT-026 | 期限切れ・iss 違い・aud 違い・`alg:none`・HS256 の JWT | `TokenVerifier.Verify` | すべてエラー（API では 401 `unauthenticated` になる） | NFR-009 | unit |
| AC-PLT-027 | テナント B に所属していない staff | `X-Tenant-Id: B` で console を呼ぶ | 403 `forbidden`。`x-sp-permission` を書き込みにした停止中テナントは 403 `tenant_not_active` | FR-002, NFR-008 | e2e |
| AC-PLT-028 | `x-sp-permission`（console）や `x-sp-isolation` がない操作を含む spec | `Router.Mount` | 起動時にエラー | NFR-008 | unit |
| AC-PLT-029 | 契約者（`world`） | `POST /v1/me/payment-methods` を同じキー・同じ本文で 2 回 | 2 回目は同じ status・本文・`Idempotent-Replayed: true`。DB（`sp_e2e_reader`）で支払方法が 1 件だけ | NFR-006, FR-013 | e2e |
| AC-PLT-030 | 完了済みのキー | 違う本文で再送 | 422 `idempotency_key_reused` | NFR-006 | e2e |
| AC-PLT-031 | テスト用のルート（処理に 2 秒かかる）に Idempotency を付けて同じキーで 2 リクエストを同時に送る | 1 つ目の処理中に 2 つ目が届く | 2 つ目は 409 `request_in_progress`、処理は 1 回 | NFR-006 | integration |
| AC-PLT-032 | 500 を返すテスト用のルート／`MarkTransient` 付きの 202 を返すテスト用のルート | 同じキーで再送 | どちらも処理が再び実行され、`platform.idempotency_keys` に completed の行が残らない | NFR-006 | integration |
| AC-PLT-033 | 契約者（`world`） | `POST /v1/me/payment-methods` を `Idempotency-Key` なしで呼ぶ | 400 `validation_failed`、`errors[0].field = "Idempotency-Key"` | NFR-006 | e2e |
| AC-PLT-034 | 想定外のエラー | `WriteProblem` で応答を作る | 500・`application/problem+json`・`code=internal_error`・`request_id` あり・内部メッセージなし | NFR-013 | unit |
| AC-PLT-035 | `api/errors.yaml` に重複コード／`CamelCase` のコード | `task generate` | 失敗する | — | unit |
| AC-PLT-036 | — | `ParsePage` に `limit=101`／`limit=0`／改ざんした `starting_after`／指定なし | エラー／エラー／エラー／limit 20（API では 400 `validation_failed`） | NFR-003 | unit |
| AC-PLT-037 | 同じ日の同じテスト用ジョブのスケジュールメッセージが 2 通 | Runner が処理 | `job_runs` は 1 件、ジョブの実行は 1 回 | FR-020 | integration |
| AC-PLT-038 | 1 回目だけ失敗するテスト用ジョブ | Runner | 同じ run が `attempts=2` で `succeeded` になる | NFR-014 | integration |
| AC-PLT-039 | `tests/platform/dev-clock.clock.spec.ts` | `clock.advance("72h")` の後にジョブを実行 | `/v1/dev/clock` は書き込み後すぐ返り、`effective_after` 以降に実行したジョブの `as_of` が 3 日進む | 憲章 §5, P-ARC-8 | e2e |
| AC-PLT-040 | dev clock の現在時刻 | `POST /v1/dev/clock {to: 現在より前}` | 422 `clock_backward_not_allowed`（時刻は変わらない） | 憲章 §5 | e2e |
| AC-PLT-041 | `.clock.spec.ts` のテスト。登録済みジョブ／未登録のジョブ | `clock.runJob(name)` | 200 `status=succeeded`（`job_runs` にジョブを持つ worker が実行した記録あり）／404 `not_found` | 憲章 §5 | e2e |
| AC-PLT-042 | タグなしでビルドした api | `/v1/dev/clock` を呼ぶ・`AUTH_MODE=dev` で起動 | 404／起動を拒否。devtools ビルドを `APP_ENV=prod` で起動しても拒否 | 憲章 §5 | unit |
| AC-PLT-043 | `X-Correlation-Id: C` 付きの申込（`world`） | 後続のイベントが購読者まで届くのを `wait.untilEvent` で待つ | 申込で書かれた Outbox の行と、購読者が出した後続のイベントがすべて同じ C を持ち、後続の `causation_id` は受け取った event_id（`sp_e2e_reader` で確認） | NFR-013, NFR-017 | e2e |
| AC-PLT-044 | `authorization`・`cookie`・`x-api-key`・`card_token` のキーを含むログ | 出力 | 値がすべて `[REDACTED]` | NFR-015, NFR-017 | unit |
| AC-PLT-045 | `DATABASE_URL` と `CURSOR_KEY` がない | プロセス起動 | 2 つとも列挙して非 0 で終了 | — | unit |
| AC-PLT-046 | 何もない状態（`DB_MODE=rds`） | CI の E2E ジョブの準備（compose → terraform → migrate → 3 プロセス） | MiniStack の RDS に PostgreSQL 16 が作られ、`migrate` が成功し、3 プロセスの `/readyz` が 200 になり、`tests/platform/smoke.spec.ts`（テストイベントが購読者まで届く）が通る | C-06, P-TST-7 | e2e |
| AC-PLT-047 | `MESSAGING_MODE=direct`／`DB_MODE=container` | AC-PLT-046 と同じ手順（ローカルで `task verify:fallback`） | どちらでも smoke が通る（フォールバックが動く） | C-06 | e2e |
| AC-PLT-048 | billing から `payments/internal` ／ `payments` から `billing/contracts` ／ `modules/x` から `modules/y`（ルート）を import | `task lint` と archtest | すべて失敗する | ADR-001 | unit |
| AC-PLT-049 | `modules/` で `time.Now()` を使う | `task lint` | forbidigo で失敗する | NFR-016 | unit |
| AC-PLT-050 | OpenAPI を変えて生成し忘れた | CI の差分チェック | 失敗する | — | unit |
| AC-PLT-051 | `x-sp-isolation: tested` で、`@isolation:<operationId>` のテストがない操作 | `task lint` | 失敗する | NFR-008 | unit |
| AC-PLT-052 | admin（`world`） | console で価格を 1,000 円から 1,200 円に改定する | 2xx。`audit.audit_records` に actor（staff・subject・role）・tenant_id・route テンプレート・request_id・before.amount=1000・after.amount=1200・`outcome=succeeded` の行が 1 件（`sp_e2e_reader` で確認） | FR-025 | e2e |
| AC-PLT-053 | viewer（`world`） | console の書き込み系を呼ぶ／トークンなしで呼ぶ | 403 で `outcome=denied` の行が 1 件／401 では 0 件 | FR-025 | e2e |
| AC-PLT-054 | 注釈を書かないテスト用の変更系ルート（console）／`/v1/me` の変更系 | 2xx で応答する | 本物の `AuditWriter`（audit モジュール）で `action=http.post` の行が 1 件／0 件 | FR-025 | integration |
| AC-PLT-055 | before に `email`・`secret` を含む注釈 | 記録する | 値が `"***"` に置き換わる | FR-025, NFR-015 | unit |
| AC-PLT-056 | 袋のない context（ワーカー・ジョブ） | `Annotate` を呼ぶ | 何も起きない（panic もしない） | FR-025 | unit |
| AC-PLT-057 | `audit.audit_records` への INSERT 権限を一時的に剥がした DB | 注釈付きのテスト用ルートが 2xx で終わる | 応答は 2xx のまま、監査の内容を含む error ログが出る | FR-025 | integration |
| AC-PLT-058 | `api/errors.yaml` | `task generate` | `provider_unavailable`（503）を含む共通コード 11 種類の定数が生成される | P-PAY-4 | unit |
| AC-PLT-059 | `InTx` の中で `AfterCommit` を 2 つ登録 | コミットする／fn がエラーを返す | 登録順に 1 回ずつ呼ばれる／どちらも呼ばれない | FR-020 | integration |
| AC-PLT-060 | tenant スコープの `InTx` | 実行される SQL を記録する | 先頭は `set_config` を 5 つ含む 1 文だけで、`SET` 文はない。`current_user` が `sp_<module>`、`app.tenant_id` が設定済み | NFR-008, P-ARC-11 ⑤ | integration |
| AC-PLT-061 | FIFO の受信バッチ 6 件（グループ G1: 1・2・3、G2: 4・5・6）で 2 件目のハンドラがエラー | `Dispatch`（ハンドラは入力の関数） | 2・3 は再試行の側（3 のハンドラは呼ばれない）。1・4〜6 は成功の側 | FR-020, C-01 | unit |
| AC-PLT-062 | 1,200 件を処理するテスト用ジョブが 500 件ごとに `Cursors.Save` を呼び、2 回目の保存の後にプロセスを止める | 再配信で Runner が同じ run を実行 | 1,001 件目から再開し、同じ run が `attempts=2` で `succeeded`。処理は合計 1,200 件で重複 0。完了後に同じ scope_key で再実行しても何もしない | NFR-001, NFR-004, P-ARC-11 ⑥, P-X-7 | integration |
| AC-PLT-063 | `orc-collection` の `concurrency: 4` で 100 件が溜まっている | テスト用の購読者（同時実行数の最大を記録）を 1 つの consumer で受信 | 同時実行は常に 4 以下 | NFR-001, P-ARC-11 ② | integration |
| AC-PLT-064 | 処理中 3 件の `sqsconsumer` | context を取り消す（SIGTERM と同じ経路） | 新しい受信をせず、3 件の完了を待ってから終了する（30 秒以内） | NFR-004 | integration |
| AC-PLT-065 | 業務の書き込みと `Cursors.Save` を同じトランザクションで行い、fn がエラーを返す | ロールバック | カーソルも進まない（業務とカーソルが食い違わない） | FR-020, P-X-7 | integration |
| AC-PLT-066 | `sp_e2e_reader` | `db` fixture で INSERT・UPDATE を試す／RLS のかかった表を SELECT | 書き込みは拒否される（read-only）／全テナントの行が読める。本番ビルドの `cmd/migrate up` の後には `sp_e2e_reader` が存在しない | P-TST-3 | integration |
| AC-PLT-067 | `parallel` project の 2 つのテストが同時に `world.createTenant()` を呼ぶ | それぞれ契約一覧を取得 | 互いのデータが見えない（自分のテナントのデータだけ） | P-TST-4, NFR-008 | e2e |
| AC-PLT-068 | `.clock.spec.ts` ではないファイルのテスト | `clock.advance()` を呼ぶ／`task lint` | fixture が例外を投げ、dev clock は変わらない／lint で検出される。`.clock.spec.ts` のファイルは `clock` project だけで 1 件ずつ直列に流れる | P-TST-4, P-TST-9 | e2e |
| AC-PLT-069 | `sink.create()` の送信先に `[{status:500, times:2}, {hang:true, times:1}]` を設定 | 契約のイベントを起こし、`.clock.spec.ts` で再送の時刻まで進める | 記録は 500・500・タイムアウト・200 の 4 件で、どれも同じ `SP-Event-Id` を持ち、`verifySignature` で検証できる。`stats.max_in_flight = 1` | FR-022, P-TST-2 | e2e |
| AC-PLT-070 | `wait.until` に 15 秒経っても偽を返す関数 | 待つ | 15 秒で失敗し、エラーに最後の値が含まれる | P-TST-4 | e2e |
| AC-PLT-071 | E2E のジョブで 1 件のテストが失敗する | CI の ③ | ジョブが失敗し、HTML レポート・トレース・全コンテナのログが成果物として残る | P-TST-7 | e2e |
| AC-PLT-072 | `world` で作ったテナントと、トライアルなしの月額プラン | `POST /v1/dev/seed/subscriptions {count: 100}` の完了を待つ | 100 件の契約が active で、初回の請求が paid、更新日がすべて同じ日。Outbox に `subscription.started` が 100 件（`sp_e2e_reader`）。同じ `seed_id` で再送しても増えない。タグなしのビルドでは 404 | P-TST-10, D-07 | e2e |
| AC-PLT-073 | `logs` fixture の相関 ID で、申込（api）→ 決済の結果の購読（worker-billing）→ Webhook の配信（worker-integration）が起きるテスト | `devlogs --corr <ID> --format=json` | api・worker-billing・worker-integration の 3 つの service の行が `ts` の昇順で並び、`http.request.completed`・`event.published`・`event.consumed`・`domain.state_changed`・`http.outbound.completed` を含む。`--format=timeline` の先頭行に相関 ID がある | NFR-017 | e2e |
| AC-PLT-074 | わざと失敗するテスト（`e2e/selftest/`。CI の ③ で `task e2e:selftest` として流し、結果の JSON レポートを検査する） | Playwright が失敗を記録する | そのテストに `logs.jsonl`・`timeline.txt`・`errors.txt` が添付され、`logs.jsonl` の全行がそのテストの相関 ID を持つ。CI の成果物に `local-logs` がある | NFR-017, P-OBS-7 | e2e |
| AC-PLT-075 | `4242 4242 4242 4242`（Luhn を満たす）・`4242424242424241`（満たさない）・`Bearer abc`・`sk_live_x`・`whsec_x`・`tok_stub_success_4242_1229_ab12cd34`・`taro@example.com`・`{name: "山田太郎", tenant_name: "ACME"}` を含む値 | `Redact` と `ReplaceAttr` を通す | 1 つ目は `[REDACTED_PAN]`、2 つ目はそのまま、Bearer・sk_・whsec_・tok_ は `[REDACTED]`、メールは `t***@example.com`、氏名は `山***`、`tenant_name` はそのまま | NFR-007, NFR-017 | unit |
| AC-PLT-076 | `LOG_HTTP_BODY=redacted` で、StubAdapter のトークンでカードを登録し、申込し、API キーで事業者 API を呼ぶテスト | テストの後に `.local/logs/*.jsonl` の全行を読む | 使ったトークン・API キーの平文・`Authorization` の値・テスト用のカード番号が 1 か所も出てこない。`http.request.completed` には除去済みの本文が載っている | NFR-007, NFR-017, P-OBS-6 | e2e |
| AC-PLT-077 | Fluent Bit と MiniStack の CloudWatch Logs が動いている | 相関 ID C で API を 1 回呼び、`devlogs cwl 'fields @timestamp, @log, @message | sort @timestamp desc | limit 10000'` を 30 秒まで繰り返し、`@message` を手元で照合する（MiniStack は `filter` が効かない。P-OBS-10） | C を持つ `http.request.completed` の行が `@log=/sp/local/api` で返る | NFR-017, P-OBS-3 | e2e |
| AC-PLT-078 | 契約者（`world`） | トライアルなしの申込が成功する | `devlogs --corr <ID> --msg domain.state_changed` に、`aggregate=subscription`・`from=pending_initial`・`to=active` と `aggregate=invoice`・`to=paid` の行がある | NFR-017, P-OBS-5 | e2e |
| AC-PLT-079 | `DB_SLOW_QUERY_MS=200` | `pg_sleep(0.3)` を含むテスト用のクエリを実行する | `db.slow_query` が 1 行出て、クエリ名と duration_ms を持ち、引数は含まない | NFR-017 | integration |
| AC-PLT-080 | `LOG_LEVEL=info,billing=debug` | billing と catalog のロガーで debug を出す／`LOG_LEVEL=info,billing=loud` で起動 | billing だけ出る／起動時にエラー | NFR-017 | unit |
| AC-PLT-081 | `X-Correlation-Id: e2e-x-abc123`／`X-Correlation-Id: <200 文字>` | 存在しないリソースを GET する | 404 の応答ヘッダーと Problem Details の `correlation_id` が `e2e-x-abc123`、`request_id` がヘッダーの `X-Request-Id` と一致／長すぎる値は捨てられ、新しい UUID が返る | NFR-017, P-OBS-4 | e2e |
| AC-PLT-082 | 相関 ID C | `.clock.spec.ts` で `clock.runJob(name)` を呼ぶ | `job.started`・`job.completed` とジョブが出したイベントの `correlation_id` が C（スケジュール起動のときは `job:<job>:<run_id>` になることは integration で確かめる） | NFR-017, P-OBS-4 | e2e |
| AC-PLT-083 | 2 つの service の JSONL（ローテーション済みのファイルを含む）を testdata に置く | `devlogs --corr`・`--since`・`--msg job.`・`--errors`・`--format=timeline` | 条件どおりに絞られ、ファイルをまたいで `ts` の順に並ぶ | NFR-017, P-OBS-7 | unit |
| AC-PLT-084 | `APP_ENV=prod` | `LOG_HTTP_BODY=redacted`／`LOG_SINKS=stdout,file` で起動 | どちらも起動を拒否する | NFR-017, P-OBS-6 | unit |

## 12. PdM への確認事項

| # | 論点 | 結論 |
|---|---|---|
| Q1 | Idempotency の表の置き場所 | **解決済み（P-PLT-1）**：A。`platform.idempotency_keys` に置く（§6.4）。設計書 §4 は PdM 側で変更 |
| Q2 | ベースラインへの追加（`actor`、sharedkernel の ID 型と `Discount`、共通エラーコード） | **解決済み（P-PLT-2）**：すべて承認。baseline §5.1・§5.6 に反映済み。`provider_unavailable` も追加（P-PAY-4） |
| Q3 | 依存ルールの検査ツール | **解決済み（P-PLT-3）**：depguard と archtest を併用（§10.1）。設計書 §1.2 は PdM 側で変更 |

未解決の確認事項はない。

## 13. 変更履歴

| 日付 | 変更内容 | 変更者 |
|---|---|---|
| 2026-10-12 | 初版作成（Draft） | TL-Platform（Claude） |
| 2026-10-12 | PdM レビューを反映（P-PLT-1〜4、P-X-2、P-PAY-4、P-CAT-5）。`auditctx` と監査ミドルウェア・`AuditWriter` を追加、worker-projection を削除、`provider_unavailable` を追加、Discount を D-10 の唯一の実装に確定、§12 を解決済みに更新。ステータスを Review に変更 | TL-Platform（Claude） |
| 2026-10-12 | PdM の最終確認を経て Fixed とする | PdM |
| 2026-10-12 | 実行基盤を Lambda 中心に変更（C-05・C-06、P-ARC-1〜7、アーキテクチャレビュー §7.3）。§2・§3.2・§4・§5・§8.1〜§8.5・§9・§10・§10.1・§10.2・§11 を改訂：cmd を lambda / devserver / migrate / internal/wiring に、Outbox を `Relay.Flush`＋`Relay.Sweep`＋`db.AfterCommit` に、SQS の受信を `Dispatch`＋`lambdasqs`／`sqspoll`（部分的なバッチ失敗・FIFO）に、ジョブに継続（10 分で区切る）を追加、RDS Proxy を本番必須にしスコープ設定を `set_config` 1 文に、ローカル DB を MiniStack の RDS（`DB_MODE=container` をフォールバック）に、devserver と MiniStack の Lambda の 2 モード、関数の一覧（`orc-collection` を含む）。devclock の LISTEN と ack をやめた（§6.4 の `dev_clock_acks` を削除、§7.3 の `/v1/dev/clock` を変更）。AC-PLT-019・020・022〜024・039・041・046・047 を修正し、059〜070 を追加。ステータスを Review に変更 | TL-Platform（Claude） |
| 2026-10-12 | 実行基盤の Lambda 化の反映について PdM の最終確認を経て、再び Fixed とする | PdM |
| 2026-10-13 | 実行基盤を ECS Fargate に戻す（P-ARC-10・P-ARC-11、憲章 C-05 取り消し）。cmd を api / worker-billing / worker-integration / migrate に戻し（`cmd/internal/wiring` は残す）、Outbox の中継を worker-billing の常駐ループ（SKIP LOCKED。`AfterCommit` による同じプロセス内のキックは任意）に、SQS の受信を常駐ループ `sqsconsumer`（同時実行数・可視性の延長・DLQ。`Dispatch` は共通部分として残す）に、ジョブを EventBridge Scheduler → プロセスごとの scheduler キュー → worker に戻した。RDS Proxy は任意。関数の一覧をプロセスの一覧に戻し、`deploy/functions.yaml`・`lambdasqs`・`lambdahttp`・`lambdax`・devserver・MiniStack の Lambda モードを削除。ローカルは 1 モード（go run または compose のコンテナ）にし、D-07 もこのモードで計測。残したもの：MiniStack の RDS（`DB_MODE=container` はフォールバック）、dev clock の 1 秒キャッシュ、`set_config` の 1 文、ジョブのカーソル保存と再開（10 分の区切りは削除。P-X-7 によりカーソルは共通の表 `platform.job_cursors` に統一）、`orc-collection`（受信の同時実行数を固定）。サーキットブレーカーなど複数タスクで共有する状態は DB に持つ方針を規約 M10 に追記（P-X-7）。AC-PLT-019・020・022〜024・039・041・046・047 を戻し、059〜070 を 059〜065 に置き換え（Lambda のアダプターの AC を削除）。ステータスを Review に変更 | TL-Platform（Claude） |
| 2026-10-13 | 実行基盤を ECS Fargate に戻した改訂（P-ARC-10・11、P-X-7）について PdM の最終確認を経て、Fixed とする | PdM |
| 2026-10-13 | テスト戦略を Playwright 中心・モックなしに改訂（P-TST-1〜9、憲章 §5）。§10.2 を全面改訂（モックを使わない原則と例外 4 つ、UT・Go の結合テスト・Playwright の E2E の範囲、`e2e/` の構成、`tests/scenarios/`（E2E-001〜022）と `tests/<module>/`（ドメインごとの API テスト）の 2 種類と完了条件、`.clock.spec.ts` によるファイル名の規約と 2 つの project（`parallel`・`clock` workers=1）、共通の fixture（api・tokens・world・clock・wait・db・sink・mail）、CI の 3 ジョブ、テナント分離テストの HTTP 層を Playwright に）。Go の E2E ハーネスを削除。§10.3 `webhook-sink` を integration §11.2 WS-1〜8 に合わせて新設し compose に追加。§6.1 に `sp_e2e_reader`（devtools のマイグレーションのみ）、§8.5 に `WEBHOOK_INSECURE_ALLOWED_HOSTS`・`MAIL_MODE` を追加、メールの確認は `email_notifications`。§4 のメモリ上のバス（テスト用）を削除。§11 のレベルを付け直し（025・027・029・030・033・040・052・053 を e2e、054・057 を integration）、フェイク前提の記述を書き換え、066〜071 を追加。ステータスを Review に変更 | TL-Platform（Claude） |
| 2026-10-13 | dev の一括シード API `POST /v1/dev/seed/subscriptions` を §7.3 に追加（P-TST-10。devtools のみ、contracts だけを通して作る devtools 専用ジョブとして実装）。Playwright の `perf` project（`*.perf.ts`、既定の実行には含めない）を §10.2 に追加。AC-PLT-072 を追加 | TL-Platform（Claude） |
| 2026-10-13 | テスト戦略の改訂（Playwright の E2E を中心にし、モックは原則使わない。P-TST-1〜10、P-X-8・9）について PdM の最終確認を経て、Fixed とする | PdM |
| 2026-10-24 | ログと調査の仕組みを追加（NFR-017、P-OBS-1〜8、設計書 §1.4・ADR-009）。§8.6 を新設：observability（共通の項目、`LOG_SINKS`・ファイルのローテーション、モジュールごとの `LOG_LEVEL`、秘密情報の除去、`LOG_HTTP_BODY`）、標準のログの種類と出す箇所（外向きの HTTP の共通ラッパー、`domain.state_changed` の補助関数、pgx のトレーサーなど）、相関 ID の引き継ぎ、Fluent Bit → MiniStack の CloudWatch Logs、`cmd/devlogs` と `task logs`・`logs:cwl`・`logs:clean`・`db:reader`・`test:unit`・`test:integration`。相関 ID を任意の文字列にした（EventEnvelope・`outbox_events` の型を text に）。ヘッダー名を `X-Request-Id`・`X-Correlation-Id` にそろえ、Problem Details に `correlation_id` を追加。Playwright の `logs` fixture（相関 ID の付与と、失敗時の `logs.jsonl`・`timeline.txt` の添付）、CI の成果物 `local-logs`、compose の `fluent-bit`、Terraform のロググループを追加。AC-PLT-043・044 を修正し、073〜084 を追加。作らないもの（P-OBS-8）を §1 に明記。ステータスを Review に変更 | TL-Platform（Claude） |
| 2026-10-24 | ログと調査の仕組みの追加（NFR-017、P-OBS-1〜9）について PdM の最終確認を経て、Fixed とする | PdM |
| 2026-10-24 | 実装で確かめた MiniStack の制約と、実装時の決定を反映（P-OBS-10、P-IMP-1〜6）：ローカルは `SCHEDULER_MODE=internal`、結合テストと profile `db` の DB は `postgres:16-alpine`、Fluent Bit のための `ministack-tls`、AC-PLT-020 の障害の起こし方、配線の確認用の `platform-smoke`・`/v1/dev/smoke`・`platform-noop`、`platform` の SQL 関数をマイグレーションで作る、AC-PLT-077 の確かめ方。実装の状況は `docs/poc/impl/platform.md` | PdM |
| 2026-10-24 | identity の実装時の決定を反映（P-IMP-7〜10）：§6.1 の `sp_owner` に BYPASSRLS、§4 に `db.WithUnscoped`。コード生成（§10.1）は `internal/tools/apigen`（oapi-codegen をライブラリとして呼ぶ）と sqlc のコンテナで動かす | PdM |
| 2026-10-24 | catalog の実装時の決定を反映（P-IMP-12）：sharedkernel に `SubscriptionID`、`/v1/checkout/*` の任意の認証（consumer のトークンがあれば主体を置く）、監査注釈の安全網の例外（`product_name`・`plan_name`・`coupon_name`）、archtest の結合テストの例外、設定 `CATALOG_COUPON_RESERVATION_TTL` | PdM |
