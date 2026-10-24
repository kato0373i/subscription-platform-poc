-- tenant スキーマ（identity.md §6.1・§6.3）
-- +goose Up
SELECT platform.create_messaging_tables('tenant');

CREATE TABLE tenant.tenants (
  id               uuid PRIMARY KEY,
  name             text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  status           text NOT NULL CHECK (status IN ('pending_review','active','suspended')),
  fincode_shop_id  text NULL CHECK (fincode_shop_id ~ '^[A-Za-z0-9_-]{1,64}$'),
  approved_at      timestamptz NULL,
  suspended_at     timestamptz NULL,
  suspend_reason   text NULL CHECK (char_length(suspend_reason) BETWEEN 1 AND 500),
  version          int NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL,
  updated_at       timestamptz NOT NULL,
  CHECK (status = 'pending_review' OR (fincode_shop_id IS NOT NULL AND approved_at IS NOT NULL)),
  CHECK (status <> 'suspended' OR (suspended_at IS NOT NULL AND suspend_reason IS NOT NULL))
);
CREATE UNIQUE INDEX tenants_fincode_shop_id_uq ON tenant.tenants (fincode_shop_id) WHERE fincode_shop_id IS NOT NULL;
CREATE INDEX tenants_list_idx ON tenant.tenants (status, created_at DESC, id);
-- status を指定しない一覧（O2）
CREATE INDEX tenants_created_idx ON tenant.tenants (created_at DESC, id);

CREATE TABLE tenant.staff_members (
  id            uuid PRIMARY KEY,
  tenant_id     uuid NOT NULL REFERENCES tenant.tenants(id),
  subject       text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 255),
  email         text NOT NULL CHECK (email = lower(email) AND char_length(email) <= 254),
  display_name  text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 100),
  role          text NOT NULL CHECK (role IN ('owner','admin','viewer')),
  version       int NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL,
  UNIQUE (tenant_id, subject),
  UNIQUE (tenant_id, email)
);
CREATE INDEX staff_members_list_idx ON tenant.staff_members (tenant_id, created_at, id);
CREATE INDEX staff_members_owner_idx ON tenant.staff_members (tenant_id) WHERE role = 'owner';

CREATE TABLE tenant.api_keys (
  id          uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL REFERENCES tenant.tenants(id),
  name        text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  key_prefix  text NOT NULL UNIQUE CHECK (key_prefix ~ '^sk_[0-9A-Za-z]{12}$'),
  key_hash    bytea NOT NULL CHECK (octet_length(key_hash) = 32),
  created_by  uuid NULL,
  revoked_by  uuid NULL,
  created_at  timestamptz NOT NULL,
  revoked_at  timestamptz NULL,
  version     int NOT NULL DEFAULT 1
);
CREATE INDEX api_keys_list_idx ON tenant.api_keys (tenant_id, created_at DESC, id);
CREATE INDEX api_keys_active_idx ON tenant.api_keys (tenant_id) WHERE revoked_at IS NULL;

-- tenants は id がテナント ID そのもの
ALTER TABLE tenant.tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant.tenants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant.tenants
  USING (id = platform.current_tenant_id()) WITH CHECK (id = platform.current_tenant_id());
SELECT platform.enable_tenant_rls('tenant.staff_members');
SELECT platform.enable_tenant_rls('tenant.api_keys');

-- API キーの認証はテナントが未確定なので、prefix で 1 件だけ引ける関数を通す（AC-IDN-017）。
-- 所有者は sp_owner（BYPASSRLS。P-IMP-7）。アプリのロールには EXECUTE だけを与える。
-- +goose StatementBegin
CREATE FUNCTION tenant.find_api_key_by_prefix(p_prefix text)
  RETURNS TABLE (id uuid, tenant_id uuid, key_hash bytea, revoked_at timestamptz)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = tenant, pg_temp
AS $$
  SELECT k.id, k.tenant_id, k.key_hash, k.revoked_at FROM tenant.api_keys k WHERE k.key_prefix = p_prefix
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION tenant.find_api_key_by_prefix(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION tenant.find_api_key_by_prefix(text) TO sp_tenant, sp_tenant_sys;

-- +goose Down
DROP FUNCTION tenant.find_api_key_by_prefix(text);
DROP TABLE tenant.api_keys;
DROP TABLE tenant.staff_members;
DROP TABLE tenant.tenants;
DROP TABLE tenant.processed_messages;
DROP TABLE tenant.outbox_events;
