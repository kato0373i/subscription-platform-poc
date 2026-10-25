-- audit スキーマ（integration.md §3.6・§6・付録 A）
-- +goose Up
CREATE TABLE audit.audit_records (
  id             uuid PRIMARY KEY,
  occurred_at    timestamptz NOT NULL,
  tenant_id      uuid NULL,
  actor_type     text NOT NULL CHECK (actor_type IN ('operator','staff','tenant_api','system')),
  actor_id       text NOT NULL,
  actor_role     text NOT NULL DEFAULT '',
  action         text NOT NULL,
  target_type    text NOT NULL DEFAULT '',
  target_id      text NOT NULL DEFAULT '',
  before         jsonb NULL,
  after          jsonb NULL,
  outcome        text NOT NULL CHECK (outcome IN ('succeeded','denied')),
  request_id     text NOT NULL,
  correlation_id text NOT NULL DEFAULT '',
  http_method    text NOT NULL,
  route          text NOT NULL,
  source_ip      inet NULL
);
CREATE INDEX audit_records_tenant ON audit.audit_records (tenant_id, occurred_at DESC, id);
CREATE INDEX audit_records_action ON audit.audit_records (action, occurred_at DESC, id);
CREATE INDEX audit_records_occurred ON audit.audit_records (occurred_at DESC, id);

-- 自分の事業者の行だけ（tenant_id が NULL の行は運営者のロール = sp_audit_sys でだけ読める）
SELECT platform.enable_tenant_rls('audit.audit_records');

-- 追記のみ（1）：ランタイムのロールには INSERT と SELECT だけを与える
REVOKE UPDATE, DELETE, TRUNCATE ON audit.audit_records FROM sp_audit, sp_audit_sys;
-- 追記のみ（2）：所有者のロールでも更新・削除できないようにする（安全網）
-- +goose StatementBegin
CREATE FUNCTION audit.forbid_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'audit_records is append-only';
END $$;
-- +goose StatementEnd
CREATE TRIGGER audit_records_append_only BEFORE UPDATE OR DELETE ON audit.audit_records
  FOR EACH ROW EXECUTE FUNCTION audit.forbid_mutation();
CREATE TRIGGER audit_records_no_truncate BEFORE TRUNCATE ON audit.audit_records
  FOR EACH STATEMENT EXECUTE FUNCTION audit.forbid_mutation();

-- +goose Down
DROP TABLE audit.audit_records;
DROP FUNCTION audit.forbid_mutation();
