-- name: InsertTenant :exec
INSERT INTO tenant.tenants (id, name, status, version, created_at, updated_at)
VALUES ($1, $2, 'pending_review', 1, $3, $3);

-- name: GetTenant :one
SELECT * FROM tenant.tenants WHERE id = $1;

-- name: GetTenantForUpdate :one
SELECT * FROM tenant.tenants WHERE id = $1 FOR UPDATE;

-- name: UpdateTenant :execrows
UPDATE tenant.tenants
SET status = $2, fincode_shop_id = $3, approved_at = $4, suspended_at = $5, suspend_reason = $6,
    version = version + 1, updated_at = $7
WHERE id = $1 AND version = $8;

-- name: ListTenants :many
SELECT * FROM tenant.tenants
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('after_created_at')::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $1;

-- name: InsertStaff :exec
INSERT INTO tenant.staff_members (id, tenant_id, subject, email, display_name, role, status, invited_at, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9, $9);

-- name: GetStaffByEmail :one
SELECT * FROM tenant.staff_members WHERE tenant_id = $1 AND email = $2;

-- 招待中の再送・有効化・最終利用日時（version は事業者の編集の楽観ロックなので上げない）
-- name: UpdateStaffLifecycle :exec
UPDATE tenant.staff_members SET status = $2, invited_at = $3, last_seen_at = $4, updated_at = $5 WHERE id = $1;

-- 所属する事業者の一覧（全事業者を横断する。system スコープで呼ぶ）
-- name: ListMembershipsBySubject :many
SELECT s.id, s.tenant_id, s.role, s.status, s.invited_at, s.last_seen_at, t.name AS tenant_name, t.status AS tenant_status
  FROM tenant.staff_members s JOIN tenant.tenants t ON t.id = s.tenant_id
 WHERE s.subject = $1
 ORDER BY t.name, t.id;

-- name: GetStaff :one
SELECT * FROM tenant.staff_members WHERE id = $1;

-- name: GetStaffBySubject :one
SELECT * FROM tenant.staff_members WHERE tenant_id = $1 AND subject = $2;

-- name: UpdateStaffRole :execrows
UPDATE tenant.staff_members SET role = $2, version = version + 1, updated_at = $3 WHERE id = $1 AND version = $4;

-- name: DeleteStaff :execrows
DELETE FROM tenant.staff_members WHERE id = $1;

-- 最後のオーナーの判定は有効なオーナーだけで数える（招待中は数えない。business-console §3）
-- name: CountOwners :one
SELECT count(*) FROM tenant.staff_members WHERE tenant_id = $1 AND role = 'owner' AND status = 'active';

-- name: ListStaff :many
SELECT * FROM tenant.staff_members
WHERE tenant_id = $1
  AND (sqlc.narg('after_created_at')::timestamptz IS NULL
       OR (created_at, id) > (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY created_at, id
LIMIT $2;

-- name: ListOwners :many
SELECT * FROM tenant.staff_members WHERE tenant_id = $1 AND role = 'owner' ORDER BY created_at, id;

-- name: InsertApiKey :exec
INSERT INTO tenant.api_keys (id, tenant_id, name, key_prefix, key_hash, created_by, created_at, version)
VALUES ($1, $2, $3, $4, $5, $6, $7, 1);

-- name: GetApiKey :one
SELECT * FROM tenant.api_keys WHERE id = $1;

-- name: RevokeApiKey :execrows
UPDATE tenant.api_keys SET revoked_at = $2, revoked_by = $3, version = version + 1 WHERE id = $1 AND revoked_at IS NULL;

-- name: CountActiveApiKeys :one
SELECT count(*) FROM tenant.api_keys WHERE tenant_id = $1 AND revoked_at IS NULL;

-- name: ListApiKeys :many
SELECT * FROM tenant.api_keys
WHERE tenant_id = $1
  AND (sqlc.narg('status')::text IS NULL
       OR (sqlc.narg('status') = 'active' AND revoked_at IS NULL)
       OR (sqlc.narg('status') = 'revoked' AND revoked_at IS NOT NULL))
  AND (sqlc.narg('after_created_at')::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: FindApiKeyByPrefix :one
SELECT f.id::uuid AS id, f.tenant_id::uuid AS tenant_id, f.key_hash::bytea AS key_hash, (f.revoked_at IS NOT NULL)::boolean AS revoked
FROM tenant.find_api_key_by_prefix(sqlc.arg(prefix)::text) f;
