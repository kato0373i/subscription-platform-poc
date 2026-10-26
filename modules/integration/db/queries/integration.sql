-- integration のクエリ（sqlc）。

-- ---- 送信先 ----

-- name: InsertEndpoint :exec
INSERT INTO integration.webhook_endpoints (id, tenant_id, url, description, event_types, status, api_version, secret_key_id,
  secret_current_ciphertext, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10);

-- name: GetEndpoint :one
SELECT * FROM integration.webhook_endpoints WHERE id = $1;

-- name: GetEndpointForUpdate :one
SELECT * FROM integration.webhook_endpoints WHERE id = $1 FOR UPDATE;

-- name: CountActiveEndpoints :one
SELECT count(*)::int FROM integration.webhook_endpoints WHERE tenant_id = $1 AND deleted_at IS NULL;

-- name: ListEndpoints :many
SELECT * FROM integration.webhook_endpoints
 WHERE deleted_at IS NULL
   AND (sqlc.narg(after_created)::timestamptz IS NULL OR (created_at, id) > (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::uuid))
 ORDER BY created_at, id
 LIMIT $1;

-- name: UpdateEndpoint :execrows
UPDATE integration.webhook_endpoints
   SET url = $2, description = $3, event_types = $4, status = $5, secret_key_id = $6, secret_current_ciphertext = $7,
       secret_previous_ciphertext = $8, previous_expires_at = $9, secret_rotated_at = $10, deleted_at = $11, updated_at = $12, version = version + 1
 WHERE id = $1 AND version = $13;

-- name: SubscribedEndpoints :many
SELECT * FROM integration.webhook_endpoints
 WHERE status = 'enabled' AND deleted_at IS NULL AND sqlc.arg(event_type)::text = ANY (event_types)
 ORDER BY created_at, id;

-- name: UpdateBreaker :exec
UPDATE integration.webhook_endpoints SET breaker_consecutive_failures = $2, breaker_open_until = $3 WHERE id = $1;

-- 時刻を迎えた配信を持つ送信先のリースを取る（§8.1。送信先ごとの同時実行を 1 にする）。
-- name: LeaseEndpoints :many
UPDATE integration.webhook_endpoints e
   SET dispatch_lease_until = sqlc.arg(until)::timestamptz, dispatch_lease_owner = sqlc.arg(owner)::text
 WHERE e.id IN (
   SELECT e2.id FROM integration.webhook_endpoints e2
    WHERE (e2.dispatch_lease_until IS NULL OR e2.dispatch_lease_until <= sqlc.arg(now)::timestamptz)
      AND EXISTS (SELECT 1 FROM integration.webhook_deliveries d
                   WHERE d.endpoint_id = e2.id AND d.status = 'pending' AND d.next_attempt_at <= sqlc.arg(now)::timestamptz)
    ORDER BY e2.dispatch_lease_until NULLS FIRST
    LIMIT sqlc.arg(slots)::int
    FOR UPDATE SKIP LOCKED)
RETURNING e.id, e.tenant_id;

-- name: ExtendLease :execrows
UPDATE integration.webhook_endpoints SET dispatch_lease_until = $3 WHERE id = $1 AND dispatch_lease_owner = $2;

-- 解放した時刻を残す（NULLS FIRST の順で、まだ処理していない送信先を先に取らせる。E5c）。
-- name: ReleaseLease :exec
UPDATE integration.webhook_endpoints SET dispatch_lease_until = sqlc.arg(released_at)::timestamptz, dispatch_lease_owner = NULL
 WHERE id = sqlc.arg(id) AND dispatch_lease_owner = sqlc.arg(owner)::text;

-- name: CleanupExpiredSecrets :execrows
UPDATE integration.webhook_endpoints SET secret_previous_ciphertext = NULL, previous_expires_at = NULL
 WHERE previous_expires_at < $1;

-- ---- 外部イベント ----

-- name: InsertWebhookEvent :execrows
INSERT INTO integration.webhook_events (id, tenant_id, source_event_id, event_type, aggregate_type, aggregate_id, aggregate_version, occurred_at, payload, created_at, correlation_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (source_event_id) DO NOTHING;

-- name: GetWebhookEvent :one
SELECT * FROM integration.webhook_events WHERE id = $1;

-- ---- 配信 ----

-- name: InsertDelivery :exec
INSERT INTO integration.webhook_deliveries (id, tenant_id, endpoint_id, webhook_event_id, trigger, redelivery_of, status, next_attempt_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9);

-- name: GetDelivery :one
SELECT * FROM integration.webhook_deliveries WHERE id = $1;

-- name: NextDueDelivery :one
SELECT * FROM integration.webhook_deliveries
 WHERE endpoint_id = $1 AND status = 'pending' AND next_attempt_at <= $2
 ORDER BY next_attempt_at, id
 LIMIT 1;

-- name: DueDeliveries :many
SELECT * FROM integration.webhook_deliveries
 WHERE endpoint_id = $1 AND status = 'pending' AND next_attempt_at <= $2
 ORDER BY next_attempt_at, id;

-- name: PendingDeliveries :many
SELECT * FROM integration.webhook_deliveries WHERE endpoint_id = $1 AND status = 'pending' ORDER BY next_attempt_at, id;

-- name: UpdateDelivery :execrows
UPDATE integration.webhook_deliveries
   SET status = $2, attempt_count = $3, next_attempt_at = $4, first_attempted_at = $5, last_attempted_at = $6, deadline_at = $7,
       last_status_code = $8, last_error = $9, completed_at = $10, updated_at = $11, version = version + 1
 WHERE id = $1 AND version = $12;

-- name: ListDeliveries :many
SELECT d.*, ev.event_type, ev.created_at AS event_created_at, ev.occurred_at AS event_occurred_at
  FROM integration.webhook_deliveries d JOIN integration.webhook_events ev ON ev.id = d.webhook_event_id
 WHERE (sqlc.narg(endpoint_id)::uuid IS NULL OR d.endpoint_id = sqlc.narg(endpoint_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR d.status = sqlc.narg(status)::text)
   AND (sqlc.narg(event_id)::text IS NULL OR d.webhook_event_id = sqlc.narg(event_id)::text)
   AND (sqlc.narg(event_type)::text IS NULL OR ev.event_type = sqlc.narg(event_type)::text)
   AND (sqlc.narg(only_tenant)::uuid IS NULL OR d.tenant_id = sqlc.narg(only_tenant)::uuid)
   AND (sqlc.narg(before_created)::timestamptz IS NULL OR (d.created_at, d.id) < (sqlc.narg(before_created)::timestamptz, sqlc.narg(before_id)::uuid))
 ORDER BY d.created_at DESC, d.id DESC
 LIMIT $1;

-- name: InsertAttempt :exec
INSERT INTO integration.webhook_delivery_attempts (id, tenant_id, delivery_id, attempt_no, requested_at, duration_ms, status_code, error_kind, response_excerpt)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListAttempts :many
SELECT * FROM integration.webhook_delivery_attempts WHERE delivery_id = $1 ORDER BY attempt_no;

-- name: CountFailedSince :one
SELECT count(*)::int FROM integration.webhook_deliveries WHERE endpoint_id = $1 AND status = 'failed' AND completed_at >= $2;

-- ---- メール ----

-- name: InsertEmail :execrows
INSERT INTO integration.email_notifications (id, tenant_id, template_key, dedupe_key, recipient_type, customer_account_id, subscription_id, invoice_id,
  endpoint_id, source_event_id, variables, status, next_attempt_at, locked_until, created_at, updated_at, staff_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending', $12, $13, $12, $12, $14)
ON CONFLICT (dedupe_key) DO NOTHING;

-- name: GetEmail :one
SELECT * FROM integration.email_notifications WHERE id = $1;

-- 再送ループの取り出し（§8.3）。取り出した行は locked_until で他のタスク・受信処理から隠す。
-- name: ClaimDueEmails :many
UPDATE integration.email_notifications n SET locked_until = sqlc.arg(until)::timestamptz
 WHERE n.id IN (
   SELECT e.id FROM integration.email_notifications e
    WHERE e.status = 'pending' AND e.next_attempt_at <= sqlc.arg(now)::timestamptz
      AND (e.locked_until IS NULL OR e.locked_until < sqlc.arg(now)::timestamptz)
    ORDER BY e.next_attempt_at
    LIMIT sqlc.arg(max)::int
    FOR UPDATE SKIP LOCKED)
RETURNING n.id, n.tenant_id;

-- name: UpdateEmailResult :exec
UPDATE integration.email_notifications
   SET status = $2, attempt_count = $3, next_attempt_at = $4, locked_until = NULL, provider_message_id = $5, last_error = $6, sent_at = $7,
       subject = $8, variables = $9, updated_at = $10
 WHERE id = $1;

-- name: SkipPendingEmailsForCustomer :execrows
UPDATE integration.email_notifications
   SET status = 'skipped', next_attempt_at = NULL, locked_until = NULL, last_error = 'customer_withdrawn', updated_at = $2
 WHERE customer_account_id = $1 AND status = 'pending';

-- ---- 請求の投影 ----

-- name: UpsertInvoiceSummary :exec
INSERT INTO integration.invoice_summaries (invoice_id, tenant_id, subscription_id, total, next_collection_at, updated_at)
VALUES ($1, $2, $3, $4, NULL, $5)
ON CONFLICT (invoice_id) DO UPDATE SET total = EXCLUDED.total, updated_at = EXCLUDED.updated_at;

-- name: UpdateInvoiceNextCollection :exec
UPDATE integration.invoice_summaries SET next_collection_at = $2, updated_at = $3 WHERE invoice_id = $1;

-- name: GetInvoiceSummary :one
SELECT * FROM integration.invoice_summaries WHERE invoice_id = $1;
