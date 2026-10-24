-- platform スキーマ: 共通の SQL 関数と表（platform.md §6.2・§6.4）
-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION platform.current_tenant_id() RETURNS uuid STABLE LANGUAGE sql
  AS $$ SELECT nullif(current_setting('app.tenant_id', true), '')::uuid $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION platform.current_customer_id() RETURNS uuid STABLE LANGUAGE sql
  AS $$ SELECT nullif(current_setting('app.customer_account_id', true), '')::uuid $$;
-- +goose StatementEnd

-- 各モジュールの最初のマイグレーションで呼ぶ: outbox_events・processed_messages の作成と権限付与
-- +goose StatementBegin
CREATE FUNCTION platform.create_messaging_tables(p_schema text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  EXECUTE format($f$
    CREATE TABLE %1$I.outbox_events (
      id                uuid PRIMARY KEY,
      event_type        text NOT NULL CHECK (event_type ~ '^[a-z]+\.[a-z_0-9]+$'),
      tenant_id         uuid NULL,
      aggregate_type    text NOT NULL,
      aggregate_id      uuid NOT NULL,
      aggregate_version bigint NOT NULL,
      correlation_id    text NOT NULL CHECK (correlation_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
      causation_id      text NULL CHECK (char_length(causation_id) <= 128),
      actor             jsonb NULL,
      payload           jsonb NOT NULL CHECK (octet_length(payload::text) <= 200000),
      occurred_at       timestamptz NOT NULL,
      created_at        timestamptz NOT NULL DEFAULT now(),
      published_at      timestamptz NULL,
      attempts          integer NOT NULL DEFAULT 0,
      next_attempt_at   timestamptz NOT NULL DEFAULT now(),
      last_error        text NULL
    )$f$, p_schema);
  EXECUTE format('CREATE INDEX outbox_events_pending_idx ON %1$I.outbox_events (next_attempt_at, occurred_at, id) WHERE published_at IS NULL', p_schema);
  EXECUTE format('CREATE INDEX outbox_events_published_idx ON %1$I.outbox_events (published_at) WHERE published_at IS NOT NULL', p_schema);
  EXECUTE format($f$
    CREATE TABLE %1$I.processed_messages (
      consumer     text NOT NULL,
      message_id   uuid NOT NULL,
      processed_at timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (consumer, message_id)
    )$f$, p_schema);
  EXECUTE format('CREATE INDEX processed_messages_processed_at_idx ON %1$I.processed_messages (processed_at)', p_schema);
  EXECUTE format('GRANT SELECT, UPDATE, DELETE ON %1$I.outbox_events TO sp_relay', p_schema);
END $$;
-- +goose StatementEnd

-- テナントに属するテーブルに RLS をかける（ENABLE + FORCE + ポリシー）。USING と WITH CHECK に同じ条件
-- +goose StatementBegin
CREATE FUNCTION platform.enable_tenant_rls(p_table regclass) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', p_table);
  EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', p_table);
  EXECUTE format('CREATE POLICY tenant_isolation ON %s USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id())', p_table);
END $$;
-- +goose StatementEnd

-- 契約者に属する行を customer スコープでも読めるようにする（permissive なので tenant_isolation と OR）
-- +goose StatementBegin
CREATE FUNCTION platform.enable_customer_rls(p_table regclass, p_column text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', p_table);
  EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', p_table);
  EXECUTE format('CREATE POLICY customer_access ON %s USING (%I = platform.current_customer_id()) WITH CHECK (%I = platform.current_customer_id())', p_table, p_column, p_column);
END $$;
-- +goose StatementEnd

CREATE TABLE platform.idempotency_keys (
  scope            text NOT NULL,
  key              text NOT NULL CHECK (char_length(key) BETWEEN 1 AND 255),
  request_hash     bytea NOT NULL,
  status           text NOT NULL CHECK (status IN ('in_progress','completed')),
  response_status  smallint NULL,
  response_headers jsonb NULL,
  response_body    bytea NULL,
  locked_until     timestamptz NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  PRIMARY KEY (scope, key)
);
CREATE INDEX idempotency_keys_expires_at_idx ON platform.idempotency_keys (expires_at);

CREATE TABLE platform.job_runs (
  id          uuid PRIMARY KEY,
  job         text NOT NULL,
  trigger     text NOT NULL CHECK (trigger IN ('schedule','dev','ops')),
  dedupe_key  text NULL,
  as_of       date NOT NULL,
  params      jsonb NOT NULL DEFAULT '{}',
  status      text NOT NULL CHECK (status IN ('queued','running','succeeded','failed')),
  attempts    integer NOT NULL DEFAULT 0,
  lease_until timestamptz NULL,
  stats       jsonb NULL,
  error       text NULL,
  correlation_id text NULL,
  queued_at   timestamptz NOT NULL DEFAULT now(),
  started_at  timestamptz NULL,
  finished_at timestamptz NULL
);
CREATE UNIQUE INDEX job_runs_dedupe_idx ON platform.job_runs (job, dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX job_runs_job_queued_idx ON platform.job_runs (job, queued_at DESC);

CREATE TABLE platform.job_cursors (
  job          text NOT NULL,
  scope_key    text NOT NULL,
  cursor       jsonb NULL,
  run_id       uuid NULL,
  completed_at timestamptz NULL,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (job, scope_key)
);

-- 業務の書き込みと同じトランザクションでカーソルを保存できるよう、全モジュールロールに付与する（P-X-7）
GRANT SELECT, INSERT, UPDATE ON platform.job_cursors TO sp_tenant, sp_tenant_sys, sp_customer, sp_customer_sys, sp_catalog, sp_catalog_sys, sp_subscription, sp_subscription_sys, sp_billing, sp_billing_sys, sp_payments, sp_payments_sys, sp_orchestration, sp_orchestration_sys, sp_integration, sp_integration_sys, sp_audit, sp_audit_sys;

CREATE TABLE platform.dev_clock (
  id         smallint PRIMARY KEY CHECK (id = 1),
  offset_ms  bigint NOT NULL CHECK (offset_ms >= 0),
  version    bigint NOT NULL,
  updated_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE platform.dev_clock;
DROP TABLE platform.job_cursors;
DROP TABLE platform.job_runs;
DROP TABLE platform.idempotency_keys;
DROP FUNCTION platform.enable_customer_rls(regclass, text);
DROP FUNCTION platform.enable_tenant_rls(regclass);
DROP FUNCTION platform.create_messaging_tables(text);
DROP FUNCTION platform.current_customer_id();
DROP FUNCTION platform.current_tenant_id();
