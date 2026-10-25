-- name: GetFactForUpdate :one
SELECT * FROM reporting.subscription_facts WHERE subscription_id = $1 FOR UPDATE;

-- name: UpsertFact :exec
INSERT INTO reporting.subscription_facts
  (subscription_id, tenant_id, status, mrr, price_version_id, billing_interval, quantity, started_in_trial, has_been_paying, trial_end, last_version, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (subscription_id) DO UPDATE SET
  status = EXCLUDED.status, mrr = EXCLUDED.mrr, price_version_id = EXCLUDED.price_version_id, billing_interval = EXCLUDED.billing_interval,
  quantity = EXCLUDED.quantity, started_in_trial = EXCLUDED.started_in_trial, has_been_paying = EXCLUDED.has_been_paying,
  trial_end = EXCLUDED.trial_end, last_version = EXCLUDED.last_version, updated_at = EXCLUDED.updated_at;

-- name: GetPriceAmount :one
SELECT amount FROM reporting.price_amounts WHERE price_version_id = $1;

-- name: InsertPriceAmount :exec
INSERT INTO reporting.price_amounts (price_version_id, tenant_id, amount, created_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (price_version_id) DO NOTHING;

-- name: AddDailyMetrics :exec
INSERT INTO reporting.daily_metrics AS d
  (tenant_id, day, mrr_delta, paying_delta, trialing_delta, past_due_delta, new_mrr, expansion_mrr, contraction_mrr, churned_mrr,
   new_subscriptions, canceled_total, canceled_paying, canceled_customer, canceled_dunning, canceled_tenant,
   trial_converted, trial_lost, recovered, dunning_lost, revenue_initial, revenue_renewal, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
ON CONFLICT (tenant_id, day) DO UPDATE SET
  mrr_delta = d.mrr_delta + EXCLUDED.mrr_delta,
  paying_delta = d.paying_delta + EXCLUDED.paying_delta,
  trialing_delta = d.trialing_delta + EXCLUDED.trialing_delta,
  past_due_delta = d.past_due_delta + EXCLUDED.past_due_delta,
  new_mrr = d.new_mrr + EXCLUDED.new_mrr,
  expansion_mrr = d.expansion_mrr + EXCLUDED.expansion_mrr,
  contraction_mrr = d.contraction_mrr + EXCLUDED.contraction_mrr,
  churned_mrr = d.churned_mrr + EXCLUDED.churned_mrr,
  new_subscriptions = d.new_subscriptions + EXCLUDED.new_subscriptions,
  canceled_total = d.canceled_total + EXCLUDED.canceled_total,
  canceled_paying = d.canceled_paying + EXCLUDED.canceled_paying,
  canceled_customer = d.canceled_customer + EXCLUDED.canceled_customer,
  canceled_dunning = d.canceled_dunning + EXCLUDED.canceled_dunning,
  canceled_tenant = d.canceled_tenant + EXCLUDED.canceled_tenant,
  trial_converted = d.trial_converted + EXCLUDED.trial_converted,
  trial_lost = d.trial_lost + EXCLUDED.trial_lost,
  recovered = d.recovered + EXCLUDED.recovered,
  dunning_lost = d.dunning_lost + EXCLUDED.dunning_lost,
  revenue_initial = d.revenue_initial + EXCLUDED.revenue_initial,
  revenue_renewal = d.revenue_renewal + EXCLUDED.revenue_renewal,
  updated_at = EXCLUDED.updated_at;

-- name: ListDailyMetrics :many
SELECT * FROM reporting.daily_metrics WHERE day <= $1 ORDER BY day;

-- name: LastUpdated :one
SELECT coalesce(max(updated_at), 'epoch'::timestamptz)::timestamptz AS last_updated FROM reporting.daily_metrics;

-- name: CountTrialsEnding :one
SELECT count(*) FROM reporting.subscription_facts WHERE status = 'trialing' AND trial_end IS NOT NULL AND trial_end <= $1;

-- name: CountPastDue :one
SELECT count(*) FROM reporting.subscription_facts WHERE status = 'past_due';

-- name: GetGoal :one
SELECT * FROM reporting.mrr_goals WHERE month = $1;

-- name: InsertGoal :exec
INSERT INTO reporting.mrr_goals (tenant_id, month, amount, version, updated_at, updated_by) VALUES ($1, $2, $3, 1, $4, $5);

-- name: UpdateGoal :execrows
UPDATE reporting.mrr_goals SET amount = $2, version = version + 1, updated_at = $3, updated_by = $4
WHERE month = $1 AND (sqlc.narg('expected')::integer IS NULL OR version = sqlc.narg('expected')::integer);

-- name: DeleteGoal :exec
DELETE FROM reporting.mrr_goals WHERE month = $1;

-- name: SumFacts :one
SELECT coalesce(sum(mrr), 0)::bigint AS mrr, count(*) FILTER (WHERE status IN ('active','past_due')) AS paying FROM reporting.subscription_facts;
