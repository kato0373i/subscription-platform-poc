# 実装状況: Identity（tenant / customer）

| 項目 | 内容 |
|---|---|
| 対象 | `docs/poc/specs/identity.md`（Fixed） |
| 更新日 | 2026-10-24 |
| 実装時の決定 | `docs/poc/02-pdm-decisions.md` の P-IMP-7〜11 |
| コード | `modules/tenant`、`modules/customer`、`api/openapi/{ops,console}/tenant.yaml`、`api/openapi/me/customer.yaml` |

## 1. 作ったもの

| 層 | tenant | customer |
|---|---|---|
| contracts | `Service`（`GetTenant`・`AuthenticateAPIKey`・`GetStaffRole`・`ListOwnerContacts`）、`Allows`・`Permission.IsWrite`、イベントの payload | `Service`（`EnsureTenantCustomerID`・`ResolveTenantCustomerID`・`GetContact`・`Withdraw`・`ResolveOrCreateAccount`・`GetTenantCustomerProfile`）、`customer.withdrawn` の payload |
| domain | Tenant（遷移表）、StaffMember（最後の owner の判定）、ApiKey（生成・解析・定数時間の比較） | Account（JIT・同期・退会のマスキング）、`cus_` ID |
| DB | `tenant` スキーマ、RLS、`tenant.find_api_key_by_prefix`（SECURITY DEFINER） | `customer` スキーマ、リンクの RLS |
| API | ops O1〜O6、console C1〜C6 | `GET /v1/me`（JIT は認証のミドルウェアで `ResolveOrCreateAccount`） |
| 配線 | `cmd/internal/wiring/modules.go`：認証・認可のポート（API キー・スタッフのロール・`Allows`・書き込みの可否）、マイグレーションの順序 | 同：`ResolveConsumer`（JIT のエラーを `account_withdrawn` などの Problem にする） |

コード生成（P-IMP-9）：SQL は sqlc（`modules/<m>/db/queries` → `internal/adapter/postgres/sqlcgen`）、HTTP は oapi-codegen の strict server（`internal/tools/apigen` → `internal/adapter/http/gen/<audience>`）。どちらも `task generate` で作り直し、生成物はコミットする。

## 2. 確かめたこと

| 確認 | 結果 |
|---|---|
| `go test ./...`（unit：遷移表・認可の表・API キーの形式・境界値・console の全ルートの Permission が表にあること） | 通る |
| `go test -tags integration ./modules/...`（testcontainers の PostgreSQL 16） | 通る。同時実行の確認（最後の owner・キーの上限・初回ログイン・リンクの作成）は 5 回繰り返しても安定 |
| `task reset` → `task up` → `task dev:container` → `task verify:local` → E2E（`e2e/tests/identity/` 27 件＋Platform 11 件） | 空の DB から 38 件とも通る。`task reset` が MiniStack の RDS のデータボリューム（`ministack-rds-*-data`）を消していなかったので直した（以前の「まっさらから」はこのボリュームが残っていた） |
| golangci-lint・e2elint・archtest | 0 件 |
| Outbox の中継 | `tenant.approved`・`tenant.suspended` が EventBridge に送られる（`published_at` が入る） |

## 3. 受け入れ条件

✅ = 自動テストで確かめた。⏳ = 依存するモジュールの実装後に確かめる。

| AC | 状況 | 確かめている場所 |
|---|---|---|
| 001 | ✅（監査記録は ⏳ audit） | `ops-tenants.spec.ts`。`tenant.create` の注釈は書いている |
| 002〜004・006 | ✅ | `ops-tenants.spec.ts`、`modules/tenant/internal/app/integration_test.go` |
| 005・008・037 | ✅ | `modules/tenant/internal/domain/domain_test.go`、`modules/tenant/contracts/contracts_test.go` |
| 007・012・014・017・018・034 | ✅ | `modules/tenant/internal/app/integration_test.go` |
| 009 | ✅ | `e2e/tests/catalog/authorization.spec.ts`（viewer のプランの PATCH が 403、プランは変わらない） |
| 010 | 一部 ✅ | admin の C2・C4 の 403 は `console-authz.spec.ts`、catalog.write の成功は `e2e/tests/catalog/authorization.spec.ts`。webhook-endpoints の 403 は integration の実装後 |
| 011・013・015・016・020 | ✅ | `console-staff.spec.ts`・`console-authz.spec.ts`・`console-api-keys.spec.ts` |
| 019 | ✅ | `e2e/tests/subscription/tenant-subscriptions.spec.ts`（失効したキーで `GET /v1/tenant/subscriptions` が 401、別のキーは 200）。console のルートでの確認（`console-api-keys.spec.ts`）も残す |
| 021・023〜025 | ✅ | `me-account.spec.ts` |
| 022・026〜028・030〜032 | ✅ | `modules/customer/internal/app/integration_test.go`（031 は退会の API が orchestration なので、結合テストで確かめている） |
| 029 | 一部 ✅ | `Withdraw` のマスキング・`customer.withdrawn`（tenant_id = null）・リンクが残ることは結合テスト。`DELETE /v1/me` の E2E（`me-withdraw.spec.ts`）は orchestration の実装後 |
| 033 | ⏳ orchestration | E2E-018 |
| 035・036 | ✅（監査記録は ⏳ audit） | `console-staff.spec.ts`・`console-api-keys.spec.ts`。`staff.update_role`・`api_key.create` の注釈は書いている（キー名は P-IMP-8） |

Platform の AC で identity を待っていたもの（`docs/poc/impl/platform.md` も更新済み）：AC-PLT-027 は ✅。AC-PLT-025 は tenant_api の入口（`/v1/tenant/*`）が subscription の実装後、AC-PLT-067 は契約一覧が subscription の実装後。

## 4. 他の TL への引き継ぎ

- **認可**：console のルートには `x-sp-permission` に §3.4 の値を付ける（`contracts_test.go` が表にない値を検出する）。書き込み系は停止中のテナントで自動的に `tenant_not_active` になる。
- **TL-Catalog・Billing・Orchestration・Integration**：`tenant/contracts.Service.GetTenant` は自分で tenant スコープを設定するので、worker からそのまま呼べる。`ListOwnerContacts` は停止中のテナントでも返す。
- **TL-Orchestration**：`DELETE /v1/me` は D-05 の判定の後に `customer.Service.Withdraw` を呼ぶ（`me-withdraw.spec.ts`・AC-IDN-029・033）。申込では、契約を作る前に `EnsureTenantCustomerID` を呼ぶ（E11）。`tenant.suspended` を購読して停止を波及させる。
- **TL-Subscription**：`/v1/tenant/*` を作ったら、AC-IDN-019 の E2E を `GET /v1/tenant/subscriptions` に置き換える（P-IMP-11）。
- **TL-Audit**：`AuditWriter` を実装したら、AC-IDN-001・035・036 の監査記録の DB 確認を E2E に足す。
- **JIT**：`/v1/me/*` のすべてで認証の直後にアカウントを作る・同期する。ハンドラは `httpx.ConsumerID(ctx)` でアカウント ID を取る。
- **テナント未確定の読み取り**：`db.WithUnscoped` を使う（P-IMP-10）。RLS のある表は 0 行になるので、必要なら SECURITY DEFINER 関数を作る（所有者は `sp_owner`。P-IMP-7）。
