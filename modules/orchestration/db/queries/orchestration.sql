-- ---- billing_cycle_processes ----

-- name: InsertProcess :exec
INSERT INTO orchestration.billing_cycle_processes (
  id, tenant_id, subscription_id, invoice_id, kind, state, run_date, billing_run_id, collection_seq, correlation_id, version, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1, $11, $11);

-- name: GetProcess :one
SELECT * FROM orchestration.billing_cycle_processes WHERE id = $1;

-- name: GetActiveProcessBySubscription :one
SELECT * FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND state NOT IN ('completed','failed');

-- name: GetActiveProcessByInvoice :one
SELECT * FROM orchestration.billing_cycle_processes WHERE invoice_id = $1 AND state NOT IN ('completed','failed');

-- name: UpdateProcess :execrows
UPDATE orchestration.billing_cycle_processes SET
  invoice_id = $2, state = $3, outcome = $4, collection_seq = $5, attempt_id = $6, attempt_status = $7, last_error = $8,
  resume_count = $9, completed_at = $10, version = version + 1, updated_at = $11
WHERE id = $1 AND version = $12;

-- name: ListStalledProcesses :many
SELECT * FROM orchestration.billing_cycle_processes
WHERE state NOT IN ('completed','failed') AND updated_at < $1
ORDER BY updated_at, id
LIMIT $2;

-- name: CountRunOutcomes :many
SELECT COALESCE(outcome, CASE WHEN state = 'failed' THEN 'failed' ELSE 'in_progress' END)::text AS outcome, count(*) AS n
FROM orchestration.billing_cycle_processes
WHERE run_date = $1 AND kind = 'renewal'
GROUP BY 1;

-- name: ListProcessesBySubscription :many
SELECT * FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 ORDER BY created_at, id;

-- ---- checkout_sagas ----

-- name: InsertSaga :execrows
INSERT INTO orchestration.checkout_sagas (customer_account_id, idempotency_key, request_hash, attempt, status, step, subscription_id, created_at, updated_at)
VALUES ($1, $2, $3, 1, 'in_progress', 'started', $4, $5, $5)
ON CONFLICT (customer_account_id, idempotency_key) DO NOTHING;

-- name: GetSagaForUpdate :one
SELECT * FROM orchestration.checkout_sagas WHERE customer_account_id = $1 AND idempotency_key = $2 FOR UPDATE;

-- name: GetSaga :one
SELECT * FROM orchestration.checkout_sagas WHERE customer_account_id = $1 AND idempotency_key = $2;

-- name: UpdateSaga :exec
UPDATE orchestration.checkout_sagas SET
  attempt = $3, status = $4, step = $5, plan_tenant_id = $6, subscription_id = $7, redemption_id = $8, invoice_id = $9,
  result_status = $10, result_code = $11, updated_at = $12
WHERE customer_account_id = $1 AND idempotency_key = $2;

-- name: ListStaleSagas :many
SELECT * FROM orchestration.checkout_sagas WHERE status = 'in_progress' AND updated_at < $1 ORDER BY updated_at LIMIT $2;

-- name: DeleteOldSagas :execrows
DELETE FROM orchestration.checkout_sagas WHERE status IN ('completed','aborted') AND updated_at < $1;

-- ---- billing_runs ----

-- name: InsertBillingRun :exec
INSERT INTO orchestration.billing_runs (id, run_date, seq, trigger, only_tenant_id, status, requested_by, started_at)
VALUES ($1, $2, (SELECT COALESCE(max(seq), 0) + 1 FROM orchestration.billing_runs WHERE run_date = $2), $3, $4, 'dispatching', $5, $6);

-- name: GetBillingRun :one
SELECT * FROM orchestration.billing_runs WHERE id = $1;

-- name: GetDispatchingRun :one
SELECT * FROM orchestration.billing_runs WHERE run_date = $1 AND status = 'dispatching';

-- name: ListBillingRuns :many
SELECT * FROM orchestration.billing_runs WHERE run_date = $1 ORDER BY seq;

-- name: SaveRunCursor :exec
UPDATE orchestration.billing_runs SET cursor = $2, enqueued_count = $3 WHERE id = $1;

-- name: FinishRun :exec
UPDATE orchestration.billing_runs SET status = $2, finished_at = $3 WHERE id = $1;

-- name: IncrementRunResume :exec
UPDATE orchestration.billing_runs SET resume_count = resume_count + 1 WHERE id = $1;

-- ---- bulk_cancellation ----

-- name: InsertBulkJob :execrows
INSERT INTO orchestration.bulk_cancellation_jobs (id, tenant_id, source_event_id, status, started_at)
VALUES ($1, $2, $3, 'running', $4)
ON CONFLICT DO NOTHING;

-- name: PickRunningBulkJob :one
SELECT * FROM orchestration.bulk_cancellation_jobs WHERE status = 'running' AND (lease_until IS NULL OR lease_until < $1)
ORDER BY started_at, id LIMIT 1 FOR UPDATE SKIP LOCKED;

-- name: LeaseBulkJob :exec
UPDATE orchestration.bulk_cancellation_jobs SET lease_until = $2 WHERE id = $1;

-- name: SaveBulkJobProgress :exec
UPDATE orchestration.bulk_cancellation_jobs SET status = $2, sweep_round = $3, cursor = $4, lease_until = $5, completed_at = $6, version = version + 1
WHERE id = $1;

-- name: InsertBulkItem :execrows
INSERT INTO orchestration.bulk_cancellation_items (job_id, tenant_id, subscription_id, result, processed_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (job_id, subscription_id) DO NOTHING;

-- name: CountBulkResult :exec
UPDATE orchestration.bulk_cancellation_jobs SET
  processed_count = processed_count + 1,
  scheduled_count = scheduled_count + (CASE WHEN sqlc.arg('result')::text = 'scheduled' THEN 1 ELSE 0 END),
  canceled_count = canceled_count + (CASE WHEN sqlc.arg('result')::text = 'canceled' THEN 1 ELSE 0 END),
  skipped_count = skipped_count + (CASE WHEN sqlc.arg('result')::text = 'skipped' THEN 1 ELSE 0 END)
WHERE id = sqlc.arg('id');

-- name: CountBulkFailure :exec
UPDATE orchestration.bulk_cancellation_jobs SET failed_count = failed_count + 1 WHERE id = $1;

-- name: GetBulkJob :one
SELECT * FROM orchestration.bulk_cancellation_jobs WHERE id = $1;

-- name: ListBulkJobsByTenant :many
SELECT * FROM orchestration.bulk_cancellation_jobs WHERE tenant_id = $1 ORDER BY started_at DESC, id DESC LIMIT 20;

-- name: InsertBulkFailure :exec
INSERT INTO orchestration.bulk_cancellation_failures (job_id, tenant_id, subscription_id, error, occurred_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (job_id, subscription_id) DO UPDATE SET error = EXCLUDED.error, occurred_at = EXCLUDED.occurred_at;

-- name: ListBulkFailures :many
SELECT * FROM orchestration.bulk_cancellation_failures WHERE job_id = $1 ORDER BY occurred_at, subscription_id LIMIT 100;
