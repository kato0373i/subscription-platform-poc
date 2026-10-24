-- ---- products ----

-- name: InsertProduct :exec
INSERT INTO catalog.products (id, tenant_id, name, description, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, 1, $5, $5);

-- name: GetProduct :one
SELECT * FROM catalog.products WHERE id = $1;

-- name: UpdateProduct :execrows
UPDATE catalog.products SET name = $2, description = $3, version = version + 1, updated_at = $4 WHERE id = $1 AND version = $5;

-- name: ListProducts :many
SELECT * FROM catalog.products
WHERE (sqlc.narg('after_created_at')::timestamptz IS NULL
       OR (created_at, id) > (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY created_at, id
LIMIT $1;

-- ---- plans ----

-- name: InsertPlan :exec
INSERT INTO catalog.plans (id, tenant_id, product_id, name, description, billing_interval, trial_days, pricing_scheme, status, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'draft', 1, $9, $9);

-- name: GetPlan :one
SELECT * FROM catalog.plans WHERE id = $1;

-- name: GetPlanForUpdate :one
SELECT * FROM catalog.plans WHERE id = $1 FOR UPDATE;

-- name: UpdatePlan :execrows
UPDATE catalog.plans
SET name = $2, description = $3, trial_days = $4, status = $5, published_at = $6, version = version + 1, updated_at = $7
WHERE id = $1 AND version = $8;

-- name: ListPlans :many
SELECT * FROM catalog.plans
WHERE (sqlc.narg('product_id')::uuid IS NULL OR product_id = sqlc.narg('product_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('after_created_at')::timestamptz IS NULL
       OR (created_at, id) > (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY created_at, id
LIMIT $1;

-- ---- price_versions ----

-- name: InsertPriceVersion :exec
INSERT INTO catalog.price_versions (id, tenant_id, plan_id, version_no, pricing_scheme, amount, scheme_params, tax_rate, effective_from, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, (sqlc.arg(tax_rate)::text)::numeric, $8, $8);

-- name: GetCurrentPriceVersion :one
SELECT id, tenant_id, plan_id, version_no, pricing_scheme, amount, scheme_params, tax_rate::text AS tax_rate, effective_from
FROM catalog.price_versions WHERE plan_id = $1 ORDER BY version_no DESC LIMIT 1;

-- name: GetPriceVersion :one
SELECT id, tenant_id, plan_id, version_no, pricing_scheme, amount, scheme_params, tax_rate::text AS tax_rate, effective_from
FROM catalog.price_versions WHERE id = $1;

-- name: ListPriceVersions :many
SELECT id, tenant_id, plan_id, version_no, pricing_scheme, amount, scheme_params, tax_rate::text AS tax_rate, effective_from
FROM catalog.price_versions
WHERE plan_id = $1 AND (sqlc.narg('before_version_no')::int IS NULL OR version_no < sqlc.narg('before_version_no'))
ORDER BY version_no DESC
LIMIT $2;

-- ---- coupons ----

-- name: InsertCoupon :exec
INSERT INTO catalog.coupons (id, tenant_id, code, name, discount_type, discount_value, duration_cycles, applies_to, expires_at, max_redemptions, status, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'active', 1, $11, $11);

-- name: InsertCouponPlan :exec
INSERT INTO catalog.coupon_plans (tenant_id, coupon_id, plan_id) VALUES ($1, $2, $3);

-- name: GetCoupon :one
SELECT * FROM catalog.coupons WHERE id = $1;

-- name: GetCouponByCode :one
SELECT * FROM catalog.coupons WHERE code = $1;

-- name: ListCouponPlanIDs :many
SELECT plan_id FROM catalog.coupon_plans WHERE coupon_id = $1 ORDER BY plan_id;

-- name: ListCoupons :many
SELECT * FROM catalog.coupons
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('after_created_at')::timestamptz IS NULL
       OR (created_at, id) > (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY created_at, id
LIMIT $1;

-- name: UpdateCoupon :execrows
UPDATE catalog.coupons SET name = $2, status = $3, version = version + 1, updated_at = $4 WHERE id = $1 AND version = $5;

-- 予約で枠を 1 つ取る（§9.1）。0 行なら期限切れ・上限・無効のどれか
-- name: ClaimCouponSlot :execrows
UPDATE catalog.coupons
SET redeemed_count = redeemed_count + 1, version = version + 1, updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status = 'active'
  AND (expires_at IS NULL OR expires_at > sqlc.arg(now))
  AND (max_redemptions IS NULL OR redeemed_count < max_redemptions);

-- 期限切れで解放された予約の遅れた確定（P-CAT-2）：状態・期限は見ず、上限だけを見る
-- name: ReclaimCouponSlot :execrows
UPDATE catalog.coupons
SET redeemed_count = redeemed_count + 1, version = version + 1, updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND (max_redemptions IS NULL OR redeemed_count < max_redemptions);

-- name: ReturnCouponSlot :exec
UPDATE catalog.coupons
SET redeemed_count = redeemed_count - 1, version = version + 1, updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id);

-- ---- coupon_redemptions ----

-- name: GetRedemption :one
SELECT * FROM catalog.coupon_redemptions WHERE id = $1;

-- name: GetRedemptionForUpdate :one
SELECT * FROM catalog.coupon_redemptions WHERE id = $1 FOR UPDATE;

-- name: GetRedemptionForUpdateSkipLocked :one
SELECT * FROM catalog.coupon_redemptions WHERE id = $1 FOR UPDATE SKIP LOCKED;

-- name: FindActiveRedemption :one
SELECT * FROM catalog.coupon_redemptions
WHERE coupon_id = $1 AND customer_account_id = $2 AND subscription_id = $3 AND status IN ('reserved','confirmed');

-- name: CustomerHoldsCoupon :one
SELECT EXISTS (SELECT 1 FROM catalog.coupon_redemptions WHERE coupon_id = $1 AND customer_account_id = $2 AND status IN ('reserved','confirmed'));

-- name: InsertRedemption :exec
INSERT INTO catalog.coupon_redemptions (id, tenant_id, coupon_id, plan_id, customer_account_id, subscription_id, status, reserved_at, reservation_expires_at, version)
VALUES ($1, $2, $3, $4, $5, $6, 'reserved', $7, $8, 1);

-- name: UpdateRedemption :execrows
UPDATE catalog.coupon_redemptions
SET status = $2, release_reason = $3, over_limit = $4, confirmed_at = $5, released_at = $6, version = version + 1
WHERE id = $1 AND version = $7;

-- ---- SECURITY DEFINER 関数 ----

-- name: ResolveCheckoutPlanTenant :one
SELECT catalog.resolve_checkout_plan_tenant(sqlc.arg(plan_id)::uuid)::uuid AS tenant_id;

-- name: ListExpiredReservations :many
SELECT f.tenant_id::uuid AS tenant_id, f.redemption_id::uuid AS redemption_id
FROM catalog.list_expired_reservations(sqlc.arg(now)::timestamptz, sqlc.arg(lim)::int) f;
