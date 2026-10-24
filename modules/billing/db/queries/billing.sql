-- ---- invoices ----

-- name: InsertInvoice :exec
INSERT INTO billing.invoices (
  id, tenant_id, subscription_id, customer_account_id, kind, collection_mode, period_start, period_end, status,
  subtotal_amount, discount_amount, total_amount, tax_amount, tax_breakdown, issued_at, paid_at, paid_attempt_id, paid_via,
  next_collection_at, collection_seq, pending_request, collection_lease_until, version, created_at, updated_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, 1, $15, $15
);

-- name: InsertInvoiceLine :exec
INSERT INTO billing.invoice_lines (
  invoice_id, line_no, tenant_id, kind, description, price_version_id, discount_snapshot, quantity, unit_amount, amount,
  tax_rate_num, tax_rate_den, period_start, period_end
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14);

-- name: UpdateInvoice :execrows
UPDATE billing.invoices SET
  status = $2, paid_at = $3, paid_attempt_id = $4, paid_via = $5, uncollectible_at = $6, voided_at = $7, void_reason = $8,
  void_requested_at = $9, void_requested_reason = $10, attempt_count = $11, dunning_stage = $12, dunning_schedule = $13,
  next_collection_at = $14, collection_seq = $15, pending_request = $16, collection_lease_until = $17,
  last_attempt_id = $18, last_failure_code = $19, last_failure_retryable = $20,
  version = version + 1, updated_at = $21
WHERE id = $1 AND version = $22;

-- name: GetInvoice :one
SELECT * FROM billing.invoices WHERE id = $1;

-- name: GetInvoiceForUpdate :one
SELECT * FROM billing.invoices WHERE id = $1 FOR UPDATE;

-- name: GetInvoiceForUpdateSkipLocked :one
SELECT * FROM billing.invoices WHERE id = $1 FOR UPDATE SKIP LOCKED;

-- name: GetInvoiceByPeriod :one
SELECT * FROM billing.invoices WHERE subscription_id = $1 AND period_start = $2;

-- name: GetUnsettledInvoice :one
SELECT * FROM billing.invoices WHERE subscription_id = $1 AND status IN ('open','processing','payment_unknown');

-- name: GetUnsettledInvoiceForUpdate :one
SELECT * FROM billing.invoices WHERE subscription_id = $1 AND status IN ('open','processing','payment_unknown') FOR UPDATE;

-- name: GetInvoiceTenant :one
SELECT tenant_id FROM billing.invoices WHERE id = $1;

-- name: GetSubscriptionTenant :one
SELECT tenant_id FROM billing.invoices WHERE subscription_id = $1 LIMIT 1;

-- name: ListInvoiceLines :many
SELECT * FROM billing.invoice_lines WHERE invoice_id = $1 ORDER BY line_no;

-- name: ListInvoiceLinesFor :many
SELECT * FROM billing.invoice_lines WHERE invoice_id = ANY(sqlc.arg('ids')::uuid[]) ORDER BY invoice_id, line_no;

-- name: ListSubscriptionInvoices :many
SELECT * FROM billing.invoices
WHERE subscription_id = $1
  AND (sqlc.narg('after_period_start')::date IS NULL
       OR (period_start, id) < (sqlc.narg('after_period_start')::date, sqlc.narg('after_id')::uuid))
ORDER BY period_start DESC, id DESC
LIMIT $2;

-- name: ListDueInvoices :many
-- 回収時刻ジョブの走査（BYPASSRLS のロールで実行する。§8）
SELECT id, tenant_id, next_collection_at FROM billing.invoices
WHERE status = 'open' AND collection_mode = 'scheduled' AND next_collection_at <= sqlc.arg('now')::timestamptz
  AND (collection_lease_until IS NULL OR collection_lease_until <= sqlc.arg('now')::timestamptz)
  AND (sqlc.narg('after_at')::timestamptz IS NULL OR (next_collection_at, id) > (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY next_collection_at, id
LIMIT sqlc.arg('lim');

-- ---- invoice_payment_records ----

-- name: InsertPaymentRecord :execrows
INSERT INTO billing.invoice_payment_records (attempt_id, outcome, invoice_id, tenant_id, amount, failure_code, retryable, source, applied, anomaly, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (attempt_id, outcome) DO NOTHING;

-- name: AttemptFinalized :one
SELECT EXISTS (SELECT 1 FROM billing.invoice_payment_records WHERE attempt_id = $1 AND outcome IN ('succeeded','failed'));

-- ---- dunning_policies ----

-- name: GetDunningPolicy :one
SELECT * FROM billing.dunning_policies WHERE tenant_id = $1;

-- name: UpsertDunningPolicy :one
INSERT INTO billing.dunning_policies (tenant_id, retry_offsets_days, version, updated_by, created_at, updated_at)
VALUES ($1, $2, 1, $3, $4, $4)
ON CONFLICT (tenant_id) DO UPDATE SET
  retry_offsets_days = EXCLUDED.retry_offsets_days, version = billing.dunning_policies.version + 1,
  updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at
RETURNING *;
