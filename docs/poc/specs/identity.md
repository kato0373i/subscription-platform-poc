# 詳細要件: Identity（tenant / customer）

| 項目 | 内容 |
|---|---|
| ステータス | **Fixed**（2026-10-13 PdM 承認。テスト戦略：Playwright の E2E が中心） |
| 担当 | TL-Identity |
| 作成日 | 2026-10-12 |
| 上位文書 | `00-poc-charter.md`（最優先）、`01-contracts-baseline.md`、`docs/design/subscription-platform.md`、`docs/requirements/subscription-platform.md` |
| 対象モジュール | `modules/tenant`、`modules/customer` |

## 1. 概要と PoC の範囲

### 1.1 対象の要件

| 要件 ID | 本書で扱う範囲 |
|---|---|
| FR-001 | Tenant 集約、ops API での登録・承認、`tenant.approved` |
| FR-002 | StaffMember、ロール、全 console API 共通の認可の表、最後のオーナーの保護 |
| FR-008 / NFR-009 | ApiKey の発行・失効・`AuthenticateAPIKey` |
| FR-009 | 事業者に渡す個人情報の範囲（`GetTenantCustomerProfile`） |
| FR-011 / D-05 | CustomerAccount（初回ログイン時の作成）、TenantCustomerLink（`cus_`）、`Withdraw` |
| FR-026 | Tenant の停止、`tenant.suspended`、停止中のテナントの操作制限 |
| NFR-008 | tenant スキーマ・customer スキーマの RLS |
| NFR-015 | 退会時のマスキング |
| FR-025 | tenant の変更操作で監査注釈を書く（記録の仕組みは TL-Platform、P-X-2） |
| FR-022 / FR-023 | Integration の通知用に事業者名・オーナーの宛先を提供（P-INT-1） |

### 1.2 作るもの・作らないもの

| 作る | 作らない（PoC 対象外） |
|---|---|
| ops API での事業者の登録・承認・停止・初期スタッフ作成 | 事業者の自己申請画面、審査フロー、KYC、申請の却下 |
| console API でのスタッフ一覧・ロール変更・削除 | 招待メール、console からのスタッフ追加、MFA |
| API キーの発行（平文 1 回）・一覧・失効、認証 | キーのスコープ・有効期限・最終利用日時の記録 |
| ロール × 権限の表（コード上の定数）と判定関数 | カスタムロール、権限の DB 管理 |
| 初回ログイン時のアカウント作成（JIT）、プロフィールの同期 | プロフィール編集 API、IdP 側ユーザーの削除 |
| `cus_` ID の払い出しと解決、退会・マスキング | 7 年保持・個人情報の開示請求対応 |
| 停止した事業者の再開（停止は終端） | — |

認証方式（開発用 JWT・Cognito の検証）は TL-Platform の範囲。本書は「認証済みの主体（subject）」を受け取った後の、アカウント・ロール・テナントの解決を扱う。

## 2. 用語

| 用語 | 意味 |
|---|---|
| subject | `Authenticator`（TL-Platform）が返す、主体の種類（consumer / staff）ごとに一意で不変な識別子。JWT の `sub` に相当 |
| テナント指定 | console API で、操作対象のテナントを指定するヘッダー `X-Tenant-Id`（P-IDN-1） |
| Permission | console API の操作の種類。各エンドポイントに 1 つ付ける（§3.4） |
| JIT 作成 | consumer が初めて認証付きで API を呼んだときに CustomerAccount を作ること |
| tenant_customer_id | 事業者ごとの契約者 ID（`cus_` + base62 24 文字）。事業者に渡す唯一の契約者識別子 |
| マスキング | 退会時に、氏名・メールアドレスを不可逆に消すこと（NULL 化） |
| 監査注釈 | 変更操作のユースケースが `internal/platform/auditctx` で context に書く「何を・変更前・変更後」。監査ミドルウェア（TL-Platform）がリクエスト単位で記録する（P-X-2） |

## 3. ドメインモデル

### 3.1 Tenant（集約ルート）

| フィールド | 型 | 説明 |
|---|---|---|
| id | `TenantID` | UUIDv7 |
| name | string | 1〜100 文字（前後空白は除去） |
| status | `TenantStatus` | `pending_review` / `active` / `suspended` |
| fincodeShopID | `*FincodeShopID` | 承認時に設定。`^[A-Za-z0-9_-]{1,64}$`。全テナントで一意 |
| approvedAt / suspendedAt | `*time.Time` | |
| suspendReason | `*string` | 1〜500 文字 |
| version | int | 楽観ロック |

**不変条件**
- `active` / `suspended` のときは `fincodeShopID` が必ずある。
- `suspended` は終端。どの状態にも戻らない。
- 販売できる（`CanSell`）のは `active` のときだけ。書き込み系の console 操作ができる（`AllowsStaffWrite`）のは `suspended` 以外。

| 現在 \ 操作 | Approve(shopID) | Suspend(reason) |
|---|---|---|
| pending_review | → active（`tenant.approved`） | `ErrTenantStateConflict` |
| active | 同じ shopID なら何もしない（イベントなし）／違えば `ErrTenantStateConflict` | → suspended（`tenant.suspended`） |
| suspended | `ErrTenantStateConflict` | 何もしない（イベントなし、reason は上書きしない） |

| メソッド | 発行イベント | 監査注釈（ユースケースが書く） |
|---|---|---|
| `RegisterTenant(id, name, now)` | なし | `tenant.register`：後 = {name} |
| `Approve(shopID, now)` | `tenant.approved` | `tenant.approve`：前 = {status}、後 = {status, fincode_shop_id}（状態が変わったときのみ） |
| `Suspend(reason, now)` | `tenant.suspended` | `tenant.suspend`：前 = {status}、後 = {status, reason}（状態が変わったときのみ） |
| `CanSell() bool` / `AllowsStaffWrite() bool` | — | — |

### 3.2 StaffMember（集約ルート）

| フィールド | 型 | 説明 |
|---|---|---|
| id | `ID[StaffMember]` | |
| tenantID | `TenantID` | |
| subject | string | staff の subject。1〜255 文字 |
| email | `Email` | 小文字に正規化。最大 254 文字 |
| displayName | string | 1〜100 文字 |
| role | `Role` | `owner` / `admin` / `viewer` |
| version | int | |

**不変条件**
- `(tenantID, subject)` と `(tenantID, email)` はそれぞれ一意。
- テナントの owner は常に 1 名以上（作成直後の 0 名状態を除く。最初の 1 名は ops API で作る）。owner を減らす操作は、同じテナントの owner 数を **Tenant 行のロック下で**数えてから判定する（ドメインサービス `OwnerGuard`）。

| メソッド | 条件 | 発行イベント | 監査注釈（ユースケースが書く） |
|---|---|---|---|
| `NewStaffMember(tenant, subject, email, name, role)` | テナントが suspended でない | なし | `staff.add`：後 = {staff_id, role} |
| `ChangeRole(newRole, ownerCount)` | owner → 他ロールで `ownerCount == 1` なら `ErrLastOwner`。同じロールなら何もしない | なし | `staff.change_role`：前 = {role}、後 = {role}（変化時のみ） |
| `Remove(ownerCount)` | owner で `ownerCount == 1` なら `ErrLastOwner` | なし | `staff.remove`：前 = {staff_id, role} |

監査注釈に email・subject は入れない（個人情報の最小化。staff_id で辿れる）。

自分自身のロール変更・削除も、上の条件を満たせば許す。

### 3.3 ApiKey（集約ルート）

| フィールド | 型 | 説明 |
|---|---|---|
| id | `ID[ApiKey]` | |
| tenantID | `TenantID` | |
| name | string | 1〜100 文字 |
| prefix | `KeyPrefix` | `sk_` + base62 12 文字。一意。検索キー |
| hash | `KeyHash` | 平文全体の SHA-256（32 バイト） |
| createdBy / revokedBy | `*ID[StaffMember]` | |
| createdAt / revokedAt | time / `*time.Time` | |

**平文の形式**：`sk_<prefix12>_<secret43>`（secret は crypto/rand の 32 バイトを base62 化）。平文は `Issue` の戻り値でだけ返し、DB・ログに出さない。

| 状態 | Revoke | 認証 |
|---|---|---|
| 有効（revokedAt = null） | → 失効 | 成功 |
| 失効 | 何もしない（イベントなし） | 失敗。元に戻せない |

| メソッド | 条件 | 発行イベント | 監査注釈（ユースケースが書く） |
|---|---|---|---|
| `IssueApiKey(tenant, name, activeCount, rand)` → `(*ApiKey, rawKey)` | テナントが suspended でない。`activeCount < 10` | なし | `api_key.issue`：後 = {api_key_id, key_prefix, name}（平文・hash は入れない） |
| `Revoke(by, now)` | — | なし | `api_key.revoke`：前 = {key_prefix, status: active}（失効済みへの再実行では書かない） |
| `Matches(raw) bool` | `subtle.ConstantTimeCompare` で比較 | — |

### 3.4 スタッフの認可の表（全モジュールの console API に適用）

各 console エンドポイントは Permission を 1 つ宣言する（OpenAPI 拡張 `x-sp-permission`）。判定は「ロールの許可」かつ「書き込み系なら テナントが suspended でない」。

| Permission | 書き込み | 対象 API（例） | owner | admin | viewer |
|---|---|---|---|---|---|
| `console.read` | | console のすべての GET（商品・プラン・クーポン・リトライ方針・契約・請求・Webhook 送信先（鍵は除く）・配信履歴・スタッフ・API キー一覧） | ✅ | ✅ | ✅ |
| `catalog.write` | ✅ | products / plans の POST・PATCH、price-versions、publish / unpublish | ✅ | ✅ | ❌ |
| `coupon.write` | ✅ | coupons の POST・PATCH | ✅ | ✅ | ❌ |
| `dunning.write` | ✅ | PUT dunning-policy | ✅ | ✅ | ❌ |
| `subscription.operate` | ✅ | console からの契約操作（PoC では該当 API なし。予約） | ✅ | ✅ | ❌ |
| `webhook.redeliver` | ✅ | POST webhook-deliveries/{id}/redeliver | ✅ | ✅ | ❌ |
| `webhook.manage` | ✅ | webhook-endpoints の POST・PATCH・DELETE、rotate-secret | ✅ | ❌ | ❌ |
| `apikey.manage` | ✅ | api-keys の POST・DELETE | ✅ | ❌ | ❌ |
| `staff.manage` | ✅ | staff の PATCH・DELETE | ✅ | ❌ | ❌ |

- 新しい console API を作るときは、この表のどれかを付ける。付いていないルートは起動時にエラーにする（TL-Platform のミドルウェアで検査）。
- `webhook.redeliver` を admin に許すのは、配信失敗の一次対応を運用担当（admin）が行えるようにするため。送信先・鍵の変更は owner のみ。
- テナントの状態と console の可否：`pending_review` はすべて可（公開は Catalog が `GetTenant` で拒否）。`suspended` は `console.read` のみ可（P-IDN-4）。事業者 API（`/v1/tenant/*`）の照会も引き続き可。

### 3.5 CustomerAccount（集約ルート）

| フィールド | 型 | 説明 |
|---|---|---|
| id | `CustomerAccountID` | UUIDv7。プラットフォーム内部だけで使う |
| subject | string | consumer の subject。一意 |
| email | `*Email` | active のとき必須。小文字に正規化。退会で NULL |
| name | `*string` | JWT の `name` クレーム（任意、最大 100 文字）。退会で NULL |
| status | `AccountStatus` | `active` / `withdrawn` |
| withdrawnAt | `*time.Time` | |
| version | int | |

**不変条件**
- active なアカウントの email は一意（withdrawn は対象外）。
- withdrawn は終端。マスキングした値は戻せない。withdrawn の subject では新しいアカウントを作らない（P-IDN-2）。

| 現在 \ 操作 | SyncProfile(email, name) | Withdraw(now) |
|---|---|---|
| active | 値が変われば更新（イベントなし） | → withdrawn・email/name を NULL（`customer.withdrawn`） |
| withdrawn | `ErrAccountWithdrawn` | 何もしない（イベントなし） |

| メソッド | 発行イベント |
|---|---|
| `ProvisionAccount(id, identity, now)` | なし（個人情報を含むイベントは出さない） |
| `SyncProfile(email, name)` | なし |
| `Withdraw(now)` | `customer.withdrawn`（監査注釈は DELETE /v1/me を受ける Orchestration のユースケースが書く） |
| `Contact() (Contact, error)` | — |

### 3.6 TenantCustomerLink（集約ルート）

| フィールド | 型 | 説明 |
|---|---|---|
| id | `TenantCustomerID` | `cus_` + base62 24 文字（crypto/rand、約 143 ビット）。連番・ハッシュ由来にしない |
| tenantID | `TenantID` | |
| customerAccountID | `CustomerAccountID` | |
| createdAt | time | |

**不変条件**：`(tenantID, customerAccountID)` に 1 つだけ。作成後は変更・削除しない（退会後も残す。取引記録の参照に使うため）。active なアカウントに対してだけ新規作成できる。

### 3.7 イベント payload（`source` = `sp.tenant` / `sp.customer`）

| event_type | aggregate_type | envelope の tenant_id | payload |
|---|---|---|---|
| `tenant.approved` | tenant | 当該 | tenant_id, fincode_shop_id, approved_at |
| `tenant.suspended` | tenant | 当該 | tenant_id, reason, suspended_at |
| `customer.withdrawn` | customer_account | null（ベースライン §5.1） | customer_account_id, withdrawn_at |

- 発行するのはベースラインの 3 種だけ。監査のためだけのイベントは作らない（P-X-2）。
- 操作者は EventEnvelope の任意項目 `actor`（ベースライン §5.1）に入れる。ops の操作は `operator`、退会は `consumer`。

## 4. 公開する契約（contracts）

前提：`TenantID` と `CustomerAccountID` は `sharedkernel` の型を使う（ベースライン §5.1、P-PLT-2）。

### 4.1 `modules/tenant/contracts`

```go
type TenantStatus string // "pending_review" | "active" | "suspended"
type Role string         // "owner" | "admin" | "viewer"
type Permission string   // §3.4 の値

type TenantInfo struct {
    TenantID      sharedkernel.TenantID
    Name          string
    Status        TenantStatus
    FincodeShopID string // pending_review のときは ""
}

type OwnerContact struct { // P-INT-1。Integration のメール・アラート通知の宛先
    StaffID     string
    Email       string
    DisplayName string
}

type Service interface {
    // 呼び出し元: orchestration（申込可否・停止判定・ショップ ID）, billing, catalog（公開可否、P-CAT-1）, integration（事業者名、P-INT-1）
    GetTenant(ctx context.Context, tenantID sharedkernel.TenantID) (TenantInfo, error)
    // 呼び出し元: platform/httpx（cmd で配線）。失効・形式不正・不一致はすべて ErrInvalidAPIKey
    AuthenticateAPIKey(ctx context.Context, rawKey string) (sharedkernel.TenantID, error)
    // 呼び出し元: platform/httpx（cmd で配線）。所属していなければ ErrStaffNotFound
    GetStaffRole(ctx context.Context, tenantID sharedkernel.TenantID, subject string) (Role, error)
    // 呼び出し元: integration（P-INT-1）。owner 全員を作成順で返す。テナントが無ければ ErrTenantNotFound
    ListOwnerContacts(ctx context.Context, tenantID sharedkernel.TenantID) ([]OwnerContact, error)
}

// 純粋関数（I/O なし）。httpx の認可ミドルウェアが cmd 経由で使う
func Allows(r Role, p Permission) bool
func (p Permission) IsWrite() bool

var (
    ErrTenantNotFound = errors.New("tenant: not found")
    ErrInvalidAPIKey  = errors.New("tenant: invalid api key")
    ErrStaffNotFound  = errors.New("tenant: staff not found")
)
```

- 依存の追加（ベースライン §5.2）：catalog → tenant/contracts（`GetTenant`、P-CAT-1）、integration → tenant/contracts（`TenantInfo.Name`・`ListOwnerContacts`、P-INT-1）。
- `ListOwnerContacts` は suspended のテナントでも返す（停止の通知・Webhook 配信失敗の通知に使うため。P-INT-4）。owner は常に 1 名以上いるので、空の配列は返らない。
- 呼び出しは worker からも来るので、tenant の app 層が `app.tenant_id = tenantID` を設定したトランザクションで読む（RLS の範囲内）。

### 4.2 `modules/customer/contracts`

```go
type TenantCustomerID string // "cus_" + 24 文字
type AccountStatus string    // "active" | "withdrawn"

type Contact struct{ Email, Name string } // Name は "" のことがある

type AuthIdentity struct { // Authenticator が検証した consumer のクレーム
    Subject       string
    Email         string
    EmailVerified *bool // クレームが無ければ nil
    Name          string
}

type AccountView struct {
    ID        sharedkernel.CustomerAccountID
    Email     string
    Name      string
    Status    AccountStatus
    CreatedAt time.Time
}

type TenantCustomerProfile struct { // 事業者に渡してよい項目だけ（FR-009）
    TenantCustomerID TenantCustomerID
    Email, Name      string // 退会済みなら ""
    Withdrawn        bool
}

type Service interface {
    // ベースライン
    EnsureTenantCustomerID(ctx context.Context, tenantID sharedkernel.TenantID, id sharedkernel.CustomerAccountID) (TenantCustomerID, error)
    ResolveTenantCustomerID(ctx context.Context, tenantID sharedkernel.TenantID, id sharedkernel.CustomerAccountID) (TenantCustomerID, error)
    GetContact(ctx context.Context, id sharedkernel.CustomerAccountID) (Contact, error)
    Withdraw(ctx context.Context, id sharedkernel.CustomerAccountID) error
    // 追加（P-IDN-3、ベースライン §5.3）
    ResolveOrCreateAccount(ctx context.Context, in AuthIdentity) (AccountView, error) // 呼び出し元: platform/httpx（cmd で配線）
    GetTenantCustomerProfile(ctx context.Context, tenantID sharedkernel.TenantID, id sharedkernel.CustomerAccountID) (TenantCustomerProfile, error) // 呼び出し元: console の契約詳細を組み立てる側
}

var (
    ErrAccountNotFound        = errors.New("customer: account not found")
    ErrAccountWithdrawn       = errors.New("customer: account withdrawn")
    ErrLinkNotFound           = errors.New("customer: tenant customer link not found")
    ErrEmailAlreadyRegistered = errors.New("customer: email already registered")
    ErrEmailUnverified        = errors.New("customer: email missing or unverified")
)
```

| メソッド | 振る舞い |
|---|---|
| `EnsureTenantCustomerID` | リンクがあれば返す。無ければ作る。アカウントが withdrawn なら `ErrAccountWithdrawn`、無ければ `ErrAccountNotFound` |
| `ResolveTenantCustomerID` | 作らない。無ければ `ErrLinkNotFound`。withdrawn でも返す（退会前の取引のイベント変換に使うため） |
| `GetContact` | withdrawn なら `ErrAccountWithdrawn`（メールを送らせない） |
| `Withdraw` | 退会の可否は判定しない（D-05 の判定は Orchestration）。withdrawn なら nil |
| `ResolveOrCreateAccount` | §7.3 の JIT 規則に従う |
| `GetTenantCustomerProfile` | そのテナントとのリンクが無ければ `ErrLinkNotFound`（他テナントの契約者の個人情報を返さない） |

### 4.3 TL-Platform との取り決め（要合意）

| 項目 | 内容 |
|---|---|
| 認可の仕組み | httpx の staff 認可ミドルウェアが、`X-Tenant-Id` → `GetStaffRole` → `Allows(role, perm)` → 書き込みなら `GetTenant().Status != suspended` の順に判定する。httpx は modules を import しないので、cmd/api が関数を注入する |
| context に入れる値 | staff：`{tenant_id, staff_id, role}`、consumer：`{customer_account_id}`、tenant_api：`{tenant_id, api_key_id}` |
| envelope | `customer.withdrawn` は `tenant_id = null` で出す（ベースライン §5.1 で決定済み）。`actor` を設定する |
| 監査 | tenant の変更系ユースケース（O1・O4〜O6・C2〜C4・C6）は `auditctx` に注釈を書く。記録は監査ミドルウェアが行う（P-X-2・P-PLT-4） |

### 4.4 個人情報の最小化（事業者に渡す項目）

| 項目 | console（FR-009） | 事業者 API（FR-008） | Webhook |
|---|---|---|---|
| tenant_customer_id（`cus_`） | ✅ | ✅ | ✅ |
| 氏名・メールアドレス | ✅（退会後は空） | ❌ | ❌ |
| customer_account_id・subject | ❌ | ❌ | ❌ |
| 他テナントとの契約・リンク | ❌ | ❌ | ❌ |
| 支払方法の表示情報 | ❌ | ❌ | ❌ |

## 5. 購読するイベントと処理

tenant・customer はどのイベントも購読しない。退会は Orchestration からの同期コマンド（`Withdraw`）で受け、停止の波及は `tenant.suspended` を Orchestration が購読して行う。

発行側の冪等性：状態が変わったときだけイベントを Outbox に書く（同じ操作の再実行ではイベントを出さない）。集約の保存と Outbox への書き込みは同じトランザクションで行う。

## 6. 永続化

共通の `outbox_events`・`processed_messages` は TL-Platform の定義に従い、両スキーマに作る。時刻は `timestamptz`、ID は UUIDv7。

### 6.1 tenant スキーマ

```sql
CREATE TABLE tenant.tenants (
  id               uuid PRIMARY KEY,
  name             text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  status           text NOT NULL CHECK (status IN ('pending_review','active','suspended')),
  fincode_shop_id  text NULL CHECK (fincode_shop_id ~ '^[A-Za-z0-9_-]{1,64}$'),
  approved_at      timestamptz NULL,
  suspended_at     timestamptz NULL,
  suspend_reason   text NULL CHECK (char_length(suspend_reason) BETWEEN 1 AND 500),
  version          int NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL,
  updated_at       timestamptz NOT NULL,
  CHECK (status = 'pending_review' OR (fincode_shop_id IS NOT NULL AND approved_at IS NOT NULL)),
  CHECK (status <> 'suspended' OR (suspended_at IS NOT NULL AND suspend_reason IS NOT NULL))
);
CREATE UNIQUE INDEX tenants_fincode_shop_id_uq ON tenant.tenants (fincode_shop_id) WHERE fincode_shop_id IS NOT NULL;
CREATE INDEX tenants_list_idx ON tenant.tenants (status, created_at DESC, id);

CREATE TABLE tenant.staff_members (
  id            uuid PRIMARY KEY,
  tenant_id     uuid NOT NULL REFERENCES tenant.tenants(id),
  subject       text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 255),
  email         text NOT NULL CHECK (email = lower(email) AND char_length(email) <= 254),
  display_name  text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 100),
  role          text NOT NULL CHECK (role IN ('owner','admin','viewer')),
  version       int NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL,
  UNIQUE (tenant_id, subject),
  UNIQUE (tenant_id, email)
);
CREATE INDEX staff_members_list_idx ON tenant.staff_members (tenant_id, created_at, id);
CREATE INDEX staff_members_owner_idx ON tenant.staff_members (tenant_id) WHERE role = 'owner';

CREATE TABLE tenant.api_keys (
  id          uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL REFERENCES tenant.tenants(id),
  name        text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  key_prefix  text NOT NULL UNIQUE CHECK (key_prefix ~ '^sk_[0-9A-Za-z]{12}$'),
  key_hash    bytea NOT NULL CHECK (octet_length(key_hash) = 32),
  created_by  uuid NULL,
  revoked_by  uuid NULL,
  created_at  timestamptz NOT NULL,
  revoked_at  timestamptz NULL,
  version     int NOT NULL DEFAULT 1
);
CREATE INDEX api_keys_list_idx ON tenant.api_keys (tenant_id, created_at DESC, id);
CREATE INDEX api_keys_active_idx ON tenant.api_keys (tenant_id) WHERE revoked_at IS NULL;
```

- 削除：staff は物理削除（履歴は監査イベントで残す）。tenants・api_keys は削除しない。
- 設計書 §4 の `staff_members.cognito_sub UNIQUE` は、`UNIQUE(tenant_id, subject)` に変える（P-IDN-1。設計書 §4 は PdM 側で修正）。

### 6.2 customer スキーマ

```sql
CREATE TABLE customer.customer_accounts (
  id            uuid PRIMARY KEY,
  subject       text NOT NULL UNIQUE CHECK (char_length(subject) BETWEEN 1 AND 255),
  email         text NULL CHECK (email = lower(email) AND char_length(email) <= 254),
  name          text NULL CHECK (char_length(name) <= 100),
  status        text NOT NULL CHECK (status IN ('active','withdrawn')),
  withdrawn_at  timestamptz NULL,
  version       int NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL,
  CHECK ( (status = 'active'    AND email IS NOT NULL AND withdrawn_at IS NULL)
       OR (status = 'withdrawn' AND email IS NULL AND name IS NULL AND withdrawn_at IS NOT NULL) )
);
CREATE UNIQUE INDEX customer_accounts_email_active_uq ON customer.customer_accounts (email) WHERE status = 'active';

CREATE TABLE customer.tenant_customer_links (
  id                   text PRIMARY KEY CHECK (id ~ '^cus_[0-9A-Za-z]{24}$'),
  tenant_id            uuid NOT NULL,
  customer_account_id  uuid NOT NULL REFERENCES customer.customer_accounts(id),
  created_at           timestamptz NOT NULL,
  UNIQUE (tenant_id, customer_account_id)
);
CREATE INDEX tenant_customer_links_account_idx ON customer.tenant_customer_links (customer_account_id);
```

### 6.3 RLS とロール

| テーブル | RLS | ポリシー |
|---|---|---|
| tenant.tenants | ENABLE + FORCE | `id = NULLIF(current_setting('app.tenant_id', true), '')::uuid` |
| tenant.staff_members / tenant.api_keys | ENABLE + FORCE | `tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid`（USING と WITH CHECK） |
| customer.tenant_customer_links | ENABLE + FORCE | 同上 |
| customer.customer_accounts | なし | テナントに属さない。customer モジュールの DB ロールだけが権限を持つ |

- `app.tenant_id` が未設定なら NULL になり、0 行になる（安全側）。
- ops API と `GetTenant` の ops 用途は、設計書 §4 の運営者用ロール（BYPASSRLS）を使う。
- `AuthenticateAPIKey` はテナントが未確定のため、`SECURITY DEFINER` 関数 `tenant.find_api_key_by_prefix(p_prefix text) RETURNS (id, tenant_id, key_hash, revoked_at)` だけで検索する（関数の所有者は BYPASSRLS のマイグレーション用ロール、`SET search_path = tenant, pg_temp`）。アプリのロールには EXECUTE だけを与える。

## 7. API

共通：エラーは Problem Details、一覧はカーソル方式（`limit` 既定 20・上限 100、`starting_after`）。

### 7.1 ops（主体：operator）

| # | メソッド・パス | リクエスト | 成功 | エラー | 冪等性 |
|---|---|---|---|---|---|
| O1 | POST /v1/ops/tenants | `name` | 201 Tenant（pending_review） | validation_failed | なし（重複登録は運営で管理） |
| O2 | GET /v1/ops/tenants | `status?`, `limit`, `starting_after` | 200 `{data: Tenant[], has_more, next_cursor}` | validation_failed | — |
| O3 | GET /v1/ops/tenants/{id} | — | 200 Tenant | not_found | — |
| O4 | POST /v1/ops/tenants/{id}/approve | `fincode_shop_id` | 200 Tenant | not_found, validation_failed, tenant_state_conflict, fincode_shop_id_taken | 同じ shop ID での再実行は 200（イベントなし） |
| O5 | POST /v1/ops/tenants/{id}/suspend | `reason`（1〜500） | 200 Tenant | not_found, validation_failed, tenant_state_conflict | suspended への再実行は 200（イベントなし） |
| O6 | POST /v1/ops/tenants/{id}/staff | `subject`, `email`, `display_name`, `role` | 201 Staff | not_found, validation_failed, tenant_not_active, staff_already_exists | なし（同じ subject は 409） |

Tenant = `{id, name, status, fincode_shop_id, approved_at, suspended_at, suspend_reason, created_at}`

### 7.2 console（主体：staff、ヘッダー `X-Tenant-Id` 必須）

| # | メソッド・パス | Permission | リクエスト | 成功 | エラー | 冪等性 |
|---|---|---|---|---|---|---|
| C1 | GET /v1/console/staff | console.read | `limit`, `starting_after` | 200 Staff[] | — | — |
| C2 | PATCH /v1/console/staff/{id} | staff.manage | `role` | 200 Staff | not_found, validation_failed, last_owner_required, conflict | 同じロールは 200（イベントなし） |
| C3 | DELETE /v1/console/staff/{id} | staff.manage | — | 204 | not_found, last_owner_required | 削除済みは 404 |
| C4 | POST /v1/console/api-keys | apikey.manage | `name` | 201 `{id, name, key_prefix, api_key, created_at}`（`api_key` は平文。この応答だけ） | validation_failed, api_key_limit_exceeded | なし（再送で 2 本できうる。上限で抑止） |
| C5 | GET /v1/console/api-keys | console.read | `status?=active\|revoked`, `limit`, `starting_after` | 200 `{id, name, key_prefix, status, created_at, revoked_at}[]` | — | — |
| C6 | DELETE /v1/console/api-keys/{id} | apikey.manage | — | 204 | not_found | 失効済みも 204 |

Staff = `{id, subject, email, display_name, role, created_at}`

console 共通のエラー（ミドルウェア）：`X-Tenant-Id` が無い・UUID でない → 400 validation_failed／所属していない・テナントが存在しない → 403 forbidden（存在の有無を区別しない）／ロールに権限が無い → 403 forbidden／suspended で書き込み → 403 tenant_not_active。他テナントのリソース ID の指定は RLS により 404。

### 7.3 me（主体：consumer）と JIT 作成

| # | メソッド・パス | リクエスト | 成功 | エラー |
|---|---|---|---|---|
| M1 | GET /v1/me | — | 200 `{id, email, name, status, created_at}` | account_withdrawn, email_unverified, email_already_registered |
| — | DELETE /v1/me | Orchestration の担当（D-05 の判定後に `Withdraw` を呼ぶ）。customer は `Withdraw` を提供するだけ | | |

**JIT の規則**（`/v1/me/*` のすべてで、consumer 認証の直後にミドルウェアが `ResolveOrCreateAccount` を呼ぶ）

| # | 条件 | 結果 |
|---|---|---|
| J1 | subject のアカウントが active | アカウントを返す。email（検証済みのとき）・name が変わっていれば更新する。email の更新が他の active と衝突したら更新せずに続行し、警告ログを出す |
| J2 | subject のアカウントが withdrawn | 403 `account_withdrawn` |
| J3 | subject が無い・email クレームが無いか `email_verified = false` | 403 `email_unverified` |
| J4 | subject が無い・email が他の active アカウントで使用中 | 409 `email_already_registered`（自動で紐づけない。乗っ取り防止） |
| J5 | subject が無い・上記以外 | active で作成して返す。同時作成は `ON CONFLICT (subject) DO NOTHING` の後に再読込し、1 件に収束させる |

### 7.4 本書で追加するエラーコード

| code | HTTP | 発生箇所 |
|---|---|---|
| tenant_state_conflict | 409 | O4, O5（許されない状態遷移） |
| fincode_shop_id_taken | 409 | O4 |
| tenant_not_active | 403 | O6、console の書き込み（suspended）。設計書の既存コードを流用 |
| staff_already_exists | 409 | O6（subject または email の重複） |
| last_owner_required | 409 | C2, C3 |
| api_key_limit_exceeded | 409 | C4（有効なキーは 10 本まで） |
| account_withdrawn | 403 | JIT |
| email_unverified | 403 | JIT |
| email_already_registered | 409 | JIT |

`/v1/tenant/*` の API キー認証の失敗は、理由を区別せず 401 `unauthenticated`。

## 8. ジョブ・非同期処理

なし（イベントの購読・定期ジョブを持たない）。Outbox の中継は TL-Platform の共通の仕組みを使う。

## 9. 異常系・境界値・競合

| # | 状況 | 扱い |
|---|---|---|
| E1 | 2 人の owner が同時にお互いを降格・削除 | Tenant 行を `SELECT … FOR UPDATE` してから owner 数を数える。後の方が `last_owner_required` |
| E2 | API キーの同時発行で上限を超える | E1 と同じく Tenant 行をロックして有効数を数える |
| E3 | 同じ shop ID で 2 テナントを同時承認 | 部分ユニーク違反 → `fincode_shop_id_taken` |
| E4 | 承認と停止が同時 | `version` の楽観ロック。負けた方は `conflict`（運営者が再実行） |
| E5 | 停止の直前に発行済みの API キー | 停止後も読み取りに使える（P-IDN-4）。失効は owner が行う |
| E6 | 失効の直前に認証済みのリクエスト | そのリクエストは完了させる。次のリクエストから 401。PoC では認証結果をキャッシュしない |
| E7 | API キーの形式不正・存在しない prefix・ハッシュ不一致・失効済み | すべて `ErrInvalidAPIKey`。存在しない prefix でもダミーのハッシュ比較を行い、応答時間の差を小さくする |
| E8 | 初回ログインの同時リクエスト | J5 のとおり 1 件に収束 |
| E9 | `cus_` ID の衝突 | PK 違反なら ID を作り直す（最大 3 回、超えたら internal_error）。`(tenant, account)` の衝突は既存を返す |
| E10 | `EnsureTenantCustomerID` の同時呼び出し | `ON CONFLICT (tenant_id, customer_account_id) DO NOTHING` → 再読込で同じ ID を返す |
| E11 | 退会の判定後に、同じ契約者の申込が割り込む | `Withdraw` はアカウント行を `FOR UPDATE` で更新し、`EnsureTenantCustomerID` は `FOR SHARE` で active を確認するので、退会後の新規リンクは作れない。ただし判定と契約作成の間の割り込みは Orchestration 側の順序で防ぐ必要がある（TL-Orchestration に引き継ぐ：申込は Ensure を契約作成より前に呼ぶ） |
| E12 | 退会済みの契約者宛てのメール | `GetContact` が `ErrAccountWithdrawn` を返すので、Integration は送らずに記録する |
| E13 | 退会後に届く、退会前の契約のイベント | `ResolveTenantCustomerID` は withdrawn でも返すので、Webhook の変換はできる |
| E14 | 境界値 | name・display_name：0 文字と 101 文字は拒否、1・100 文字は可。reason：0・501 文字は拒否。email：255 文字以上は拒否。API キーの name：同上。有効なキー：10 本目は可、11 本目は拒否 |
| E15 | `X-Tenant-Id` に他テナントの ID | 所属していないので 403 forbidden |

## 10. 拡張ポイント

| 拡張 | PoC での扱い | 将来の追加方法 |
|---|---|---|
| スタッフの招待 | ops API で直接作成 | `staff_invitations` を追加し、受諾時に `NewStaffMember` を呼ぶ。既存の集約は変えない |
| MFA | なし | IdP（Cognito）の設定。認可の表は変わらない |
| 1 人が複数テナントに所属 | データ構造は対応済み（`UNIQUE(tenant_id, subject)`・`X-Tenant-Id`） | 所属一覧の API を足すだけ |
| カスタムロール | 定数の表 | `Allows` の実装を DB 参照に差し替える（呼び出し側は変えない） |
| API キーのスコープ・期限・最終利用日時 | なし | `api_keys` に列を足し、`AuthenticateAPIKey` の戻り値に scope を足す |
| 複数の IdP・IdP の切り替え | subject 1 つ | `customer_auth_identities(issuer, subject)` を分けて持つ |
| 事業者の再開・審査の却下 | 停止は終端、却下なし | `TenantStatus` に遷移を足す（exhaustive で漏れを検出） |
| 退会時の IdP ユーザー削除 | しない | `customer.withdrawn` を購読する処理を TL-Platform 側に足す |
| 事業者プロフィール（連絡先・住所） | name だけ | 列の追加 |

## 11. 受け入れ条件

テストのレベル（P-TST-1〜7）。モック・フェイクは使わない。

| レベル | 書き方 | 本書での対象 |
|---|---|---|
| unit | Go のテーブル駆動テスト。DB も外部も使わない純粋なロジックだけ | 認可の表（`Allows`）、API キーの形式、Tenant の状態遷移 |
| integration | Go のテスト。testcontainers-go の本物の PostgreSQL | 最後のオーナーの競合、RLS、SECURITY DEFINER 関数、アカウント・リンクの同時作成、API を持たない contracts（`GetTenant`・`EnsureTenantCustomerID` など） |
| e2e | Playwright（APIRequestContext）。課金の流れを通すものは `e2e/tests/scenarios/`（E2E-001〜022）、ドメイン単体の API テストは `e2e/tests/identity/*.spec.ts`（P-TST-8）。全プロセスを起動し、認証は開発用 JWT（`AUTH_MODE=dev`） | ops・console・me の API。各テストは ops API で自分専用のテナント・スタッフ・契約者を作る（P-TST-4）。API から見えない不変条件（Outbox・監査記録・平文の非保存）は `sp_e2e_reader` で DB を確かめる（P-TST-3）。時刻は進めないので `parallel` project で流す |

| ID | Given | When | Then | 要件 | レベル | e2e で確かめる場所 |
|---|---|---|---|---|---|---|
| AC-IDN-001 | 運営者が認証済み | O1 に name を送る | 201、status = pending_review、fincode_shop_id = null。DB 確認：Outbox にイベントが無く、監査記録 `tenant.register` が 1 件 | FR-001 | e2e | `e2e/tests/identity/ops-tenants.spec.ts` |
| AC-IDN-002 | pending_review のテナント | O4 に未使用の shop ID を送る | 200、status = active、fincode_shop_id = 指定値。DB 確認：`tenant.approved{tenant_id}` が Outbox に 1 件 | FR-001 | e2e | `e2e/tests/identity/ops-tenants.spec.ts` |
| AC-IDN-003 | 承認済み（shop ID = S） | 同じ S で O4 を再実行 | 200。DB 確認：`tenant.approved` は 1 件のまま。別の shop ID なら 409 tenant_state_conflict | FR-001 | e2e | `e2e/tests/identity/ops-tenants.spec.ts` |
| AC-IDN-004 | テナント A が shop ID S で承認済み | テナント B を S で承認 | 409 fincode_shop_id_taken、B は pending_review のまま | FR-001 | e2e | `e2e/tests/identity/ops-tenants.spec.ts` |
| AC-IDN-005 | 各状態のテナント | Approve / Suspend を呼ぶ | §3.1 の遷移表のとおり（全 6 通り） | FR-001, FR-026 | unit | — |
| AC-IDN-006 | active のテナント | O5 に reason を送る | 200、status = suspended。再実行も 200 で reason は変わらない。DB 確認：`tenant.suspended{tenant_id, reason}` が 1 件だけ | FR-026 | e2e | `e2e/tests/identity/ops-tenants.spec.ts`（停止後の一括解約は E2E-012） |
| AC-IDN-007 | pending_review / active / suspended のテナント | `GetTenant` を呼ぶ | status と fincode_shop_id が返る。存在しない ID は `ErrTenantNotFound` | FR-001, FR-026 | integration | — |
| AC-IDN-008 | 全ロール × 全 Permission | `Allows` を呼ぶ | §3.4 の表と一致する（表駆動テストで全組み合わせ） | FR-002 | unit | — |
| AC-IDN-009 | viewer のスタッフ | PATCH /v1/console/plans/{id} を呼ぶ | 403 forbidden、プランは変わらない | FR-002 | e2e | `e2e/tests/identity/console-authz.spec.ts` |
| AC-IDN-010 | admin のスタッフ | C2・C4・webhook-endpoints の POST を呼ぶ | すべて 403 forbidden。catalog.write の API は成功する | FR-002 | e2e | `e2e/tests/identity/console-authz.spec.ts` |
| AC-IDN-011 | owner が 1 名のテナント | その owner を admin に変える／削除する | 409 last_owner_required、変更されない | FR-002 | e2e | `e2e/tests/identity/console-staff.spec.ts` |
| AC-IDN-012 | owner が 2 名（X, Y） | X が Y を、Y が X を同時に降格する | 片方だけ成功し、もう片方は 409。owner は 1 名残る | FR-002 | integration | — |
| AC-IDN-013 | テナント A のスタッフ | `X-Tenant-Id` = B で console を呼ぶ／テナント B のスタッフ ID で C2 を呼ぶ | 前者は 403 forbidden、後者は 404 not_found | FR-002, NFR-008 | e2e | `e2e/tests/identity/console-authz.spec.ts`（console の契約詳細の 404 は E2E-016） |
| AC-IDN-014 | `app.tenant_id` = A を設定したトランザクション | staff_members・api_keys・tenant_customer_links を全件 SELECT | テナント A の行だけが返る。未設定なら 0 行 | NFR-008 | integration | — |
| AC-IDN-015 | suspended のテナントの owner | console の GET と C4 を呼ぶ | GET は 200、C4 は 403 tenant_not_active | FR-026 | e2e | `e2e/tests/identity/console-authz.spec.ts` |
| AC-IDN-016 | owner | C4 で発行する | 応答に `sk_` 形式の平文が 1 回だけ含まれ、C5 の応答には無い。DB 確認：api_keys のどの列にも平文が無く、key_hash = SHA-256(平文) | NFR-009 | e2e | `e2e/tests/identity/console-api-keys.spec.ts` |
| AC-IDN-017 | 有効なキー K（テナント A）、`app.tenant_id` 未設定の接続 | `AuthenticateAPIKey(K)` | `tenant.find_api_key_by_prefix` 経由でテナント A の ID が返る。アプリのロールで api_keys を直接 SELECT すると 0 行 | FR-008 | integration | — |
| AC-IDN-018 | 失効済みのキー・形式不正・存在しない prefix・secret の 1 文字違い | `AuthenticateAPIKey` | すべて `ErrInvalidAPIKey` | FR-008, NFR-009 | integration | — |
| AC-IDN-019 | owner がキー K を C6 で失効した | K で GET /v1/tenant/subscriptions を呼ぶ | 401 unauthenticated。失効前に発行した別のキーは使える（ローテーション） | FR-008, NFR-009 | e2e | `e2e/tests/identity/console-api-keys.spec.ts` |
| AC-IDN-020 | 有効なキーが 10 本 | C4 を呼ぶ | 409 api_key_limit_exceeded。1 本失効させると発行できる | NFR-009 | e2e | `e2e/tests/identity/console-api-keys.spec.ts` |
| AC-IDN-021 | 未登録の subject、email = `Foo@Example.com`（検証済み）の開発用 JWT | GET /v1/me | 200、active のアカウントが作られ email は `foo@example.com` | FR-011 | e2e | `e2e/tests/identity/me-account.spec.ts` |
| AC-IDN-022 | 未登録の subject | GET /v1/me を同時に 2 回 | アカウントは 1 件だけ、両方とも同じ id を返す | FR-011 | integration | — |
| AC-IDN-023 | email E の active アカウントがある | 別の subject・email E で GET /v1/me | 409 email_already_registered、アカウントは作られない | FR-011 | e2e | `e2e/tests/identity/me-account.spec.ts` |
| AC-IDN-024 | email クレームが無い、または email_verified = false の開発用 JWT | GET /v1/me | 403 email_unverified | FR-011 | e2e | `e2e/tests/identity/me-account.spec.ts` |
| AC-IDN-025 | active のアカウントの subject で、email・name を変えた開発用 JWT | GET /v1/me | 応答の email・name が新しい値になる | FR-011 | e2e | `e2e/tests/identity/me-account.spec.ts` |
| AC-IDN-026 | 契約者 X、テナント A・B | `EnsureTenantCustomerID(A, X)` を 2 回、`(B, X)` を 1 回 | A の 2 回は同じ ID、B は異なる ID。どちらも `^cus_[0-9A-Za-z]{24}$` | FR-011 | integration | — |
| AC-IDN-027 | リンクが無い | `EnsureTenantCustomerID(A, X)` を同時に 2 回 | リンクは 1 件、同じ ID が返る | FR-011 | integration | — |
| AC-IDN-028 | リンクが無い／X が退会済み | `ResolveTenantCustomerID`／`EnsureTenantCustomerID` | `ErrLinkNotFound`／`ErrAccountWithdrawn` | FR-011 | integration | — |
| AC-IDN-029 | 契約の無い active の契約者 X（テナント A とリンクあり） | DELETE /v1/me、続けて同じ JWT で GET /v1/me | DELETE は成功し、GET は 403 account_withdrawn。DB 確認：status = withdrawn、email・name が NULL、`customer.withdrawn`（tenant_id = null）が Outbox に 1 件、リンクは残る | FR-011, NFR-015 | e2e | `e2e/tests/identity/me-withdraw.spec.ts`（契約が残る場合からの流れは E2E-018） |
| AC-IDN-030 | 退会済みの X（テナント A とリンクあり） | `Withdraw(X)` を再実行、`GetContact(X)`、`ResolveTenantCustomerID(A, X)` | 再実行は nil で Outbox は増えない。`GetContact` は `ErrAccountWithdrawn`、Resolve は従来の ID を返す | FR-011, NFR-015 | integration | — |
| AC-IDN-031 | 退会済みのアカウントが email E を使っていた | 新しい subject・email E で GET /v1/me | 新しいアカウントが作られる（部分ユニーク） | FR-011, NFR-015 | e2e | `e2e/tests/identity/me-account.spec.ts` |
| AC-IDN-032 | X は A とリンクあり、B とは無い | `GetTenantCustomerProfile(B, X)` | `ErrLinkNotFound`（B に X の氏名・メールを返さない） | FR-009, NFR-008 | integration | — |
| AC-IDN-033 | 契約者に active の契約がある | DELETE /v1/me | 409、アカウントは active のまま（判定は Orchestration、本書は Withdraw が呼ばれないことを確認） | FR-011, D-05 | e2e | E2E-018 |
| AC-IDN-034 | owner が 2 名（作成順に X, Y）、admin が 1 名のテナント（suspended を含む） | `ListOwnerContacts` | X, Y の順に {staff_id, email, display_name} が返り、admin は含まれない。存在しないテナントは `ErrTenantNotFound` | FR-022, FR-023 | integration | — |
| AC-IDN-035 | owner が admin のスタッフ S を viewer に変える | C2 を呼ぶ | 200。DB 確認：監査記録が `staff.change_role`・前 = {role: admin}・後 = {role: viewer} で、email・subject を含まない。Outbox にイベントは無い | FR-025 | e2e | `e2e/tests/identity/console-staff.spec.ts` |
| AC-IDN-036 | owner | C4 で API キーを発行する | DB 確認：監査記録 `api_key.issue` に key_prefix と name があり、平文と hash は含まれない | FR-025, NFR-009 | e2e | `e2e/tests/identity/console-api-keys.spec.ts` |
| AC-IDN-037 | 乱数源を固定した生成器 | API キーを生成し、`ParseKey` と `Matches` を呼ぶ | 平文が `^sk_[0-9A-Za-z]{12}_[0-9A-Za-z]{43}$`、prefix が先頭 15 文字。形式不正（区切り欠落・長さ違い・記号混入）は解析エラー、secret の 1 文字違いは `Matches` = false | NFR-009 | unit | — |

## 12. PdM への確認事項

すべて解決済み。新しい確認事項はない。

| # | 論点 | 状態 | 決定内容と本書への反映 |
|---|---|---|---|
| Q1 | console で操作するテナントの決め方 | 解決済み（P-IDN-1） | A：ヘッダー `X-Tenant-Id` で指定し、`UNIQUE(tenant_id, subject)`。§2・§6.1・§7.2 |
| Q2 | 退会した契約者の、同じログインでの再登録 | 解決済み（P-IDN-2） | PoC ではできない（403 account_withdrawn）。§3.5・§7.3 J2・AC-IDN-030 |
| Q3 | ベースラインへの追記 | 解決済み（P-IDN-3、③は P-X-2 で取り下げ） | ①②④⑤は承認され、ベースライン §5 に反映済み。③の監査用イベント 6 種は作らず、変更系ユースケースが `auditctx` に監査注釈を書く方式に変更。§3.1〜§3.3・§3.7・§4.3 |
| Q4 | 停止した事業者ができること | 解決済み（P-IDN-4） | A：console の閲覧と事業者 API の照会は可、書き込みは不可。§3.4・E5・AC-IDN-015 |
| 追加 | Integration の通知用の事業者名・オーナーの宛先 | 解決済み（P-INT-1） | `TenantInfo.Name`（既存）と `ListOwnerContacts` を tenant/contracts に追加。§4.1・AC-IDN-034 |

## 13. 変更履歴

| 日付 | 変更内容 | 変更者 |
|---|---|---|
| 2026-10-12 | 初版作成（Draft） | TL-Identity |
| 2026-10-12 | PdM レビューを反映（P-IDN-1〜4、P-X-2、P-INT-1、ベースライン v1.1）。監査用イベント 6 種を削除し監査注釈の記載に置換、`ListOwnerContacts`・`OwnerContact` を追加、依存の追加と sharedkernel の ID 型を決定済みとして記載、AC-IDN-034〜036 を追加、§12 を解決済みに更新。ステータスを Review に変更 | TL-Identity |
| 2026-10-12 | PdM の最終確認を経て Fixed とする | PdM |
| 2026-10-13 | テスト方針の変更（P-TST-1〜7）を反映。§11 にレベルの定義を追加し、AC-IDN-001〜036 のレベルを unit / integration / e2e で付け直し（e2e は Playwright と開発用 JWT、DB の確認は `sp_e2e_reader`）。AC-IDN-029・030 を API と contracts で分け直し、AC-IDN-037（API キーの形式、unit）を追加。ステータスを Fixed から Review に戻す | TL-Identity |
| 2026-10-13 | P-TST-8 を反映。§11 の表に「e2e で確かめる場所」の列を追加し、e2e の 22 件に E2E シナリオの番号または `e2e/tests/identity/` の spec ファイル名の案（ops-tenants・console-authz・console-staff・console-api-keys・me-account・me-withdraw）を記載。ステータスは Review のまま | TL-Identity |
| 2026-10-13 | テスト戦略の改訂（Playwright の E2E を中心にし、モックは原則使わない。P-TST-1〜10、P-X-8・9）について PdM の最終確認を経て、Fixed とする | PdM |
