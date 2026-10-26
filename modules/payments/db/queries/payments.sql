-- ---- payment_methods ----

-- name: InsertPaymentMethod :exec
INSERT INTO payments.payment_methods (id, customer_account_id, method_type, status, display, registration_key, version, created_at, updated_at)
VALUES ($1, $2, $3, 'active', $4, $5, 1, $6, $6);

-- name: InsertProviderRef :exec
INSERT INTO payments.payment_method_provider_refs (id, payment_method_id, customer_account_id, provider, tenant_shop_id, customer_ref, method_ref, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetPaymentMethod :one
SELECT * FROM payments.payment_methods WHERE id = $1;

-- name: GetPaymentMethodByRegistrationKey :one
SELECT * FROM payments.payment_methods WHERE customer_account_id = $1 AND registration_key = $2;

-- name: ListProviderRefs :many
SELECT * FROM payments.payment_method_provider_refs WHERE payment_method_id = $1 ORDER BY created_at, id;

-- name: CountActivePaymentMethods :one
SELECT count(*) FROM payments.payment_methods WHERE customer_account_id = $1 AND status = 'active';

-- name: ListActivePaymentMethods :many
SELECT * FROM payments.payment_methods
WHERE customer_account_id = $1 AND status = 'active'
  AND (sqlc.narg('after_created_at')::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: UpdatePaymentMethod :execrows
UPDATE payments.payment_methods SET status = $2, display = $3, deleted_at = $4, version = version + 1, updated_at = $5
WHERE id = $1 AND version = $6;

-- ---- payment_attempts ----

-- name: InsertAttempt :exec
INSERT INTO payments.payment_attempts (
  id, tenant_id, payable_type, payable_id, customer_account_id, payment_method_id, method_type, provider, tenant_shop_id,
  amount, idempotency_key, collection_seq, request_hash, provider_order_ref, status, requested_at, version, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'processing', $15, 1, $15, $15);

-- name: GetAttempt :one
SELECT * FROM payments.payment_attempts WHERE id = $1;

-- name: GetAttemptForUpdate :one
SELECT * FROM payments.payment_attempts WHERE id = $1 FOR UPDATE;

-- name: GetAttemptForUpdateSkipLocked :one
SELECT * FROM payments.payment_attempts WHERE id = $1 FOR UPDATE SKIP LOCKED;

-- name: GetAttemptByIdempotencyKey :one
SELECT * FROM payments.payment_attempts WHERE idempotency_key = $1;

-- name: GetAttemptByOrderRef :one
SELECT * FROM payments.payment_attempts WHERE provider_order_ref = $1;

-- name: GetAttemptByOrderRefForUpdate :one
SELECT * FROM payments.payment_attempts WHERE provider_order_ref = $1 FOR UPDATE;

-- 請求の成功済み・進行中の試行（E-03〜E-05 の判定）
-- name: FindBlockingAttempt :one
SELECT id, status FROM payments.payment_attempts
WHERE payable_type = $1 AND payable_id = $2 AND status IN ('succeeded','processing','pending_async','unknown')
ORDER BY (status = 'succeeded') DESC
LIMIT 1;

-- name: UpdateAttempt :execrows
UPDATE payments.payment_attempts SET
  status = $2, failure_code = $3, failure_retryable = $4, provider_raw_code = $5, resolution = $6, resolved_by = $7,
  resolve_note = $8, check_count = $9, last_checked_at = $10, finalized_at = $11, version = version + 1, updated_at = $12
WHERE id = $1 AND version = $13;

-- name: DeleteProcessingAttempt :execrows
DELETE FROM payments.payment_attempts WHERE id = $1 AND status = 'processing';

-- 照合の対象（§8.1）。古い順に最大 lim 件
-- name: ListUnresolvedAttempts :many
SELECT id FROM payments.payment_attempts
WHERE (status = 'unknown' AND requested_at < sqlc.arg(unknown_before)::timestamptz)
   OR (status = 'processing' AND requested_at < sqlc.arg(processing_before)::timestamptz)
   OR (status = 'pending_async' AND requested_at < sqlc.arg(pending_before)::timestamptz
       AND (last_checked_at IS NULL OR last_checked_at < sqlc.arg(pending_before)::timestamptz))
ORDER BY requested_at, id
LIMIT sqlc.arg(lim)::int;

-- name: ListAttemptsInRange :many
SELECT * FROM payments.payment_attempts
WHERE provider = $1 AND requested_at >= sqlc.arg(from_time)::timestamptz AND requested_at < sqlc.arg(to_time)::timestamptz;

-- name: GetAttemptsByOrderRefs :many
SELECT * FROM payments.payment_attempts WHERE provider_order_ref = ANY(sqlc.arg(refs)::text[]);

-- ---- provider_events（Webhook の Inbox） ----

-- name: InsertProviderEvent :execrows
INSERT INTO payments.provider_events (provider, provider_event_id, provider_order_ref, payload, received_at, next_attempt_at)
VALUES ($1, $2, $3, $4, $5, $5)
ON CONFLICT DO NOTHING;

-- name: LockPendingProviderEvent :one
SELECT * FROM payments.provider_events WHERE provider = $1 AND provider_event_id = $2 AND status = 'pending' FOR UPDATE SKIP LOCKED;

-- name: CountProviderEvents :one
SELECT count(*) FROM payments.provider_events WHERE provider = $1 AND provider_event_id = $2;

-- name: FinishProviderEvent :exec
UPDATE payments.provider_events SET status = $3, processed_at = $4, last_error = $5 WHERE provider = $1 AND provider_event_id = $2;

-- name: RetryProviderEvent :exec
UPDATE payments.provider_events SET attempts = attempts + 1, next_attempt_at = $3, last_error = $4,
  status = CASE WHEN attempts + 1 >= 10 THEN 'failed' ELSE status END
WHERE provider = $1 AND provider_event_id = $2;

-- name: ListDueProviderEvents :many
SELECT provider, provider_event_id FROM payments.provider_events
WHERE status = 'pending' AND next_attempt_at <= sqlc.arg(now)::timestamptz AND received_at < sqlc.arg(received_before)::timestamptz
ORDER BY next_attempt_at
LIMIT 500;

-- ---- provider_circuit_breakers ----

-- name: GetBreaker :one
SELECT * FROM payments.provider_circuit_breakers WHERE provider = $1;

-- name: RecordBreakerFailure :one
INSERT INTO payments.provider_circuit_breakers (provider, consecutive_failures, open_until, updated_at)
VALUES ($1, 1, NULL, sqlc.arg(now)::timestamptz)
ON CONFLICT (provider) DO UPDATE SET consecutive_failures = payments.provider_circuit_breakers.consecutive_failures + 1, updated_at = EXCLUDED.updated_at
RETURNING consecutive_failures;

-- name: OpenBreaker :exec
UPDATE payments.provider_circuit_breakers SET open_until = $2, consecutive_failures = 0, updated_at = $3 WHERE provider = $1;

-- name: ResetBreaker :exec
INSERT INTO payments.provider_circuit_breakers (provider, consecutive_failures, open_until, updated_at)
VALUES ($1, 0, NULL, $2)
ON CONFLICT (provider) DO UPDATE SET consecutive_failures = 0, open_until = NULL, updated_at = EXCLUDED.updated_at;

-- ---- reconciliation_discrepancies ----

-- name: InsertDiscrepancy :execrows
INSERT INTO payments.reconciliation_discrepancies (id, business_date, provider, kind, attempt_id, tenant_id, provider_order_ref, detail, detected_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT DO NOTHING;

-- name: ListDiscrepancies :many
SELECT * FROM payments.reconciliation_discrepancies
WHERE (sqlc.narg('business_date')::date IS NULL OR business_date = sqlc.narg('business_date')::date)
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('after_id')::uuid IS NULL OR id > sqlc.narg('after_id')::uuid)
ORDER BY id
LIMIT $1;

-- name: ListUnresolvedAttemptsPage :many
-- 運営者コンソールの確定していない試行の一覧（operator-console §5.1。ix_attempt_unresolved を使う）
SELECT * FROM payments.payment_attempts
WHERE status = ANY(sqlc.arg(statuses)::text[]) AND requested_at < sqlc.arg(requested_before)::timestamptz
  AND (sqlc.narg('after_requested_at')::timestamptz IS NULL
       OR (requested_at, id) > (sqlc.narg('after_requested_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY requested_at, id
LIMIT sqlc.arg(lim)::int;

-- name: GetDiscrepancy :one
SELECT * FROM payments.reconciliation_discrepancies WHERE id = $1;

-- name: AcknowledgeDiscrepancy :execrows
UPDATE payments.reconciliation_discrepancies SET status = 'acknowledged' WHERE id = $1 AND status = 'open';
