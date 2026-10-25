-- audit のクエリ（sqlc）。

-- name: InsertRecord :exec
INSERT INTO audit.audit_records (id, occurred_at, tenant_id, actor_type, actor_id, actor_role, action, target_type, target_id, before, after,
  outcome, request_id, correlation_id, http_method, route, source_ip)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17);

-- 運営者の閲覧（§7.2）。(occurred_at, id) の新しい順。
-- name: ListRecords :many
SELECT id, occurred_at, tenant_id, actor_type, actor_id, actor_role, action, target_type, target_id, before, after, outcome, request_id,
       correlation_id, http_method, route, coalesce(host(source_ip), '')::text AS source_ip
  FROM audit.audit_records
 WHERE (sqlc.narg(tenant_id)::uuid IS NULL OR tenant_id = sqlc.narg(tenant_id)::uuid)
   AND (sqlc.narg(action)::text IS NULL OR action = sqlc.narg(action)::text)
   AND (sqlc.narg(actor_id)::text IS NULL OR actor_id = sqlc.narg(actor_id)::text)
   AND (sqlc.narg(occurred_from)::timestamptz IS NULL OR occurred_at >= sqlc.narg(occurred_from)::timestamptz)
   AND (sqlc.narg(occurred_to)::timestamptz IS NULL OR occurred_at < sqlc.narg(occurred_to)::timestamptz)
   AND (sqlc.narg(before_at)::timestamptz IS NULL OR (occurred_at, id) < (sqlc.narg(before_at)::timestamptz, sqlc.narg(before_id)::uuid))
 ORDER BY occurred_at DESC, id DESC
 LIMIT $1;
