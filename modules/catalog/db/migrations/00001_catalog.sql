-- catalog スキーマ（catalog.md §6）
-- +goose Up
SELECT platform.create_messaging_tables('catalog');

CREATE TABLE catalog.products (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 1000),
  version int NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, id)
);
CREATE INDEX products_list_idx ON catalog.products (tenant_id, created_at, id);

CREATE TABLE catalog.plans (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  product_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 1000),
  billing_interval text NOT NULL CHECK (billing_interval IN ('month','year')),
  trial_days int NOT NULL CHECK (trial_days BETWEEN 0 AND 365),
  pricing_scheme text NOT NULL CHECK (pricing_scheme IN ('fixed')),
  status text NOT NULL CHECK (status IN ('draft','published','unpublished')),
  published_at timestamptz NULL,
  version int NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, product_id) REFERENCES catalog.products (tenant_id, id)
);
CREATE INDEX plans_product_idx ON catalog.plans (tenant_id, product_id, created_at, id);
CREATE INDEX plans_status_idx ON catalog.plans (tenant_id, status, created_at, id);
CREATE INDEX plans_list_idx ON catalog.plans (tenant_id, created_at, id);

CREATE TABLE catalog.price_versions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  version_no int NOT NULL CHECK (version_no >= 1),
  pricing_scheme text NOT NULL CHECK (pricing_scheme IN ('fixed')),
  amount bigint NULL CHECK (amount BETWEEN 1 AND 1000000),
  scheme_params jsonb NOT NULL DEFAULT '{}'::jsonb,
  tax_rate numeric(4,3) NOT NULL CHECK (tax_rate >= 0 AND tax_rate < 1),
  effective_from timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  CHECK (pricing_scheme <> 'fixed' OR amount IS NOT NULL),
  UNIQUE (plan_id, version_no),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES catalog.plans (tenant_id, id)
);

-- 価格版は追記のみ（AC-CAT-008）
-- +goose StatementBegin
CREATE FUNCTION catalog.reject_price_version_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'catalog.price_versions is append-only (% on %)', TG_OP, OLD.id USING ERRCODE = 'restrict_violation';
END $$;
-- +goose StatementEnd
CREATE TRIGGER price_versions_append_only BEFORE UPDATE OR DELETE ON catalog.price_versions
  FOR EACH ROW EXECUTE FUNCTION catalog.reject_price_version_change();

CREATE TABLE catalog.coupons (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{3,32}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  discount_type text NOT NULL CHECK (discount_type IN ('amount','percent')),
  discount_value bigint NOT NULL,
  duration_cycles smallint NOT NULL CHECK (duration_cycles BETWEEN 1 AND 24),
  applies_to text NOT NULL CHECK (applies_to IN ('all_plans','selected_plans')),
  expires_at timestamptz NULL,
  max_redemptions int NULL CHECK (max_redemptions BETWEEN 1 AND 1000000),
  redeemed_count int NOT NULL DEFAULT 0 CHECK (redeemed_count >= 0),
  status text NOT NULL CHECK (status IN ('active','inactive')),
  version int NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CHECK ((discount_type = 'amount'  AND discount_value BETWEEN 1 AND 1000000)
      OR (discount_type = 'percent' AND discount_value BETWEEN 1 AND 100)),
  CHECK (max_redemptions IS NULL OR redeemed_count <= max_redemptions),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, id)
);
CREATE INDEX coupons_list_idx ON catalog.coupons (tenant_id, created_at, id);

CREATE TABLE catalog.coupon_plans (
  tenant_id uuid NOT NULL,
  coupon_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  PRIMARY KEY (coupon_id, plan_id),
  FOREIGN KEY (tenant_id, coupon_id) REFERENCES catalog.coupons (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES catalog.plans (tenant_id, id)
);

CREATE TABLE catalog.coupon_redemptions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  coupon_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  customer_account_id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('reserved','confirmed','released')),
  release_reason text NULL CHECK (release_reason IN ('caller','expired')),
  over_limit boolean NOT NULL DEFAULT false,
  reserved_at timestamptz NOT NULL,
  reservation_expires_at timestamptz NOT NULL,
  confirmed_at timestamptz NULL,
  released_at timestamptz NULL,
  version int NOT NULL DEFAULT 1,
  CHECK ((status = 'released') = (release_reason IS NOT NULL AND released_at IS NOT NULL)),
  CHECK ((status = 'confirmed') = (confirmed_at IS NOT NULL)),
  FOREIGN KEY (tenant_id, coupon_id) REFERENCES catalog.coupons (tenant_id, id)
);
-- 1 人 1 回（解放済みは除く。P-CAT-4）・1 契約に 1 つ・掃除ジョブ
CREATE UNIQUE INDEX coupon_redemptions_customer_uq ON catalog.coupon_redemptions (coupon_id, customer_account_id) WHERE status IN ('reserved','confirmed');
CREATE UNIQUE INDEX coupon_redemptions_subscription_uq ON catalog.coupon_redemptions (subscription_id) WHERE status IN ('reserved','confirmed');
CREATE INDEX coupon_redemptions_expiry_idx ON catalog.coupon_redemptions (reservation_expires_at) WHERE status = 'reserved';

SELECT platform.enable_tenant_rls('catalog.products');
SELECT platform.enable_tenant_rls('catalog.plans');
SELECT platform.enable_tenant_rls('catalog.price_versions');
SELECT platform.enable_tenant_rls('catalog.coupons');
SELECT platform.enable_tenant_rls('catalog.coupon_plans');
SELECT platform.enable_tenant_rls('catalog.coupon_redemptions');

-- テナントをまたぐ読み取りは次の関数だけ（返すのは ID だけ。中身は改めて RLS 下で読む）。所有者は sp_owner（P-IMP-7）
-- 未認証の公開 API：published / unpublished のプランの tenant_id。draft・存在しないなら NULL
-- +goose StatementBegin
CREATE FUNCTION catalog.resolve_checkout_plan_tenant(p_plan_id uuid) RETURNS uuid
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = catalog, pg_temp
AS $$
  SELECT p.tenant_id FROM catalog.plans p WHERE p.id = p_plan_id AND p.status IN ('published','unpublished')
$$;
-- +goose StatementEnd
-- 掃除ジョブ：期限を過ぎた予約
-- +goose StatementBegin
CREATE FUNCTION catalog.list_expired_reservations(p_now timestamptz, p_limit int)
  RETURNS TABLE (tenant_id uuid, redemption_id uuid)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = catalog, pg_temp
AS $$
  SELECT r.tenant_id, r.id FROM catalog.coupon_redemptions r
  WHERE r.status = 'reserved' AND r.reservation_expires_at <= p_now
  ORDER BY r.reservation_expires_at, r.id
  LIMIT p_limit
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION catalog.resolve_checkout_plan_tenant(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION catalog.list_expired_reservations(timestamptz, int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION catalog.resolve_checkout_plan_tenant(uuid) TO sp_catalog, sp_catalog_sys;
GRANT EXECUTE ON FUNCTION catalog.list_expired_reservations(timestamptz, int) TO sp_catalog, sp_catalog_sys;

-- +goose Down
DROP FUNCTION catalog.list_expired_reservations(timestamptz, int);
DROP FUNCTION catalog.resolve_checkout_plan_tenant(uuid);
DROP TABLE catalog.coupon_redemptions;
DROP TABLE catalog.coupon_plans;
DROP TABLE catalog.coupons;
DROP TABLE catalog.price_versions;
DROP FUNCTION catalog.reject_price_version_change();
DROP TABLE catalog.plans;
DROP TABLE catalog.products;
DROP TABLE catalog.processed_messages;
DROP TABLE catalog.outbox_events;
