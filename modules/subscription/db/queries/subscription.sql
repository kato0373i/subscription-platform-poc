-- name: InsertSubscription :exec
INSERT INTO subscription.subscriptions (
  id, tenant_id, customer_account_id, tenant_customer_id, signup_request_id, product_id, plan_id, price_version_id,
  billing_interval, quantity, status, signup_date, current_period_start, current_period_end, next_renewal_date,
  billing_anchor_day, trial_start, trial_end, trial_will_end_notified, pending_plan_id, pending_price_version_id,
  pending_plan_interval, pending_plan_effective_date, cancel_at_period_end, cancellation_source, cancellation_reason,
  cancellation_effective_date, pending_renewal, applied_discount, payment_method_id, past_due_invoice_id,
  last_paid_invoice_id, cancel_reason, canceled_at, signup_failure_reason, version, created_at, updated_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24,
  $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, 1, $36, $36
);

-- name: UpdateSubscription :execrows
UPDATE subscription.subscriptions SET
  product_id = $2, plan_id = $3, price_version_id = $4, billing_interval = $5, quantity = $6, status = $7,
  current_period_start = $8, current_period_end = $9, next_renewal_date = $10, trial_start = $11, trial_end = $12,
  trial_will_end_notified = $13, pending_plan_id = $14, pending_price_version_id = $15, pending_plan_interval = $16,
  pending_plan_effective_date = $17, cancel_at_period_end = $18, cancellation_source = $19, cancellation_reason = $20,
  cancellation_effective_date = $21, pending_renewal = $22, applied_discount = $23, payment_method_id = $24,
  past_due_invoice_id = $25, last_paid_invoice_id = $26, cancel_reason = $27, canceled_at = $28,
  signup_failure_reason = $29, version = version + 1, updated_at = $30
WHERE id = $1 AND version = $31;

-- name: GetSubscription :one
SELECT * FROM subscription.subscriptions WHERE id = $1;

-- name: GetBySignupRequest :one
SELECT * FROM subscription.subscriptions WHERE customer_account_id = $1 AND signup_request_id = $2;

-- name: ListDueForRenewal :many
SELECT id, tenant_id, status, next_renewal_date, trial_end FROM subscription.subscriptions
WHERE status IN ('trialing','active') AND next_renewal_date <= sqlc.arg(date)::date
  AND (sqlc.narg('after_date')::date IS NULL OR (next_renewal_date, id) > (sqlc.narg('after_date')::date, sqlc.narg('after_id')::uuid))
ORDER BY next_renewal_date, id
LIMIT $1;

-- name: ListTrialEndingOn :many
SELECT id, tenant_id, status, next_renewal_date, trial_end FROM subscription.subscriptions
WHERE status = 'trialing' AND trial_end = sqlc.arg(date)::date
  AND (sqlc.narg('after_id')::uuid IS NULL OR id > sqlc.narg('after_id')::uuid)
ORDER BY id
LIMIT $1;

-- name: ListOpenByTenant :many
SELECT id, tenant_id, status, next_renewal_date, trial_end FROM subscription.subscriptions
WHERE tenant_id = sqlc.arg(tenant_id) AND status IN ('pending_initial','trialing','active','past_due')
  AND (sqlc.narg('after_id')::uuid IS NULL OR id > sqlc.narg('after_id')::uuid)
ORDER BY id
LIMIT $1;

-- name: HasOpenSubscriptions :one
SELECT EXISTS (SELECT 1 FROM subscription.subscriptions WHERE customer_account_id = $1 AND status IN ('pending_initial','trialing','active','past_due'));

-- name: IsPaymentMethodInUse :one
SELECT EXISTS (SELECT 1 FROM subscription.subscriptions WHERE payment_method_id = $1 AND status IN ('pending_initial','trialing','active','past_due'));

-- 一覧（作成日時の新しい順）。me は consumer_self、console・tenant は tenant_isolation の RLS で絞られる
-- name: ListSubscriptions :many
SELECT * FROM subscription.subscriptions
WHERE (sqlc.narg('statuses')::text[] IS NULL OR status = ANY(sqlc.narg('statuses')::text[]))
  AND (sqlc.narg('plan_id')::uuid IS NULL OR plan_id = sqlc.narg('plan_id')::uuid)
  AND (sqlc.narg('tenant_customer_id')::text IS NULL OR tenant_customer_id = sqlc.narg('tenant_customer_id')::text)
  AND (sqlc.narg('after_created_at')::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $1;

-- name: ListByTenantCustomer :many
SELECT id, product_id, status FROM subscription.subscriptions
WHERE tenant_customer_id = $1 AND (sqlc.narg('product_id')::uuid IS NULL OR product_id = sqlc.narg('product_id')::uuid)
ORDER BY created_at, id;

-- name: TenantCustomerHasSubscriptions :one
SELECT EXISTS (SELECT 1 FROM subscription.subscriptions WHERE tenant_customer_id = $1);

-- name: InsertTrialUsage :exec
INSERT INTO subscription.trial_usages (customer_account_id, product_id, tenant_id, subscription_id, used_on, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: HasTrialUsage :one
SELECT EXISTS (SELECT 1 FROM subscription.trial_usages WHERE customer_account_id = $1 AND product_id = $2);
