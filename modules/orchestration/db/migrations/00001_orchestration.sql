-- orchestration スキーマ（orchestration.md §6）
-- +goose Up
SELECT platform.create_messaging_tables('orchestration');

CREATE TABLE orchestration.billing_cycle_processes (
  id               uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  subscription_id  uuid NOT NULL,
  invoice_id       uuid NULL,
  kind             text NOT NULL CHECK (kind IN ('signup','renewal','retry','recovery')),
  state            text NOT NULL CHECK (state IN ('started','renewal_begun','invoice_issued','collecting','awaiting_outcome','compensating','completed','failed')),
  outcome          text NULL CHECK (outcome IN ('paid','payment_failed','uncollectible','ended','signup_failed','skipped')),
  run_date         date NULL,
  billing_run_id   uuid NULL,
  collection_seq   int  NULL CHECK (collection_seq >= 1),
  attempt_id       uuid NULL,
  attempt_status   text NULL,
  last_error       text NULL,
  resume_count     int  NOT NULL DEFAULT 0,
  correlation_id   text NOT NULL,
  version          int  NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL,
  updated_at       timestamptz NOT NULL,
  completed_at     timestamptz NULL,
  CHECK ((state = 'completed') = (outcome IS NOT NULL)),
  CHECK (state NOT IN ('collecting','awaiting_outcome') OR (invoice_id IS NOT NULL AND collection_seq IS NOT NULL))
);
CREATE UNIQUE INDEX bcp_active_invoice ON orchestration.billing_cycle_processes (invoice_id)
  WHERE state NOT IN ('completed','failed') AND invoice_id IS NOT NULL;
CREATE UNIQUE INDEX bcp_active_subscription ON orchestration.billing_cycle_processes (subscription_id)
  WHERE state NOT IN ('completed','failed');
CREATE INDEX bcp_stalled ON orchestration.billing_cycle_processes (updated_at) WHERE state NOT IN ('completed','failed');
CREATE INDEX bcp_run ON orchestration.billing_cycle_processes (run_date, outcome);
CREATE INDEX bcp_subscription ON orchestration.billing_cycle_processes (subscription_id, created_at);

CREATE TABLE orchestration.checkout_sagas (
  customer_account_id uuid NOT NULL,
  idempotency_key     text NOT NULL,
  request_hash        bytea NOT NULL,
  attempt             int  NOT NULL DEFAULT 1,
  status              text NOT NULL CHECK (status IN ('in_progress','pending_outcome','completed','aborted')),
  step                text NOT NULL,
  plan_tenant_id      uuid NULL,                     -- §6 の tenant_id。RLS の対象外の表なので名前を変える（P-IMP-17）
  subscription_id     uuid NOT NULL,
  redemption_id       uuid NULL,
  invoice_id          uuid NULL,
  result_status       smallint NULL,
  result_code         text NULL,
  created_at          timestamptz NOT NULL,
  updated_at          timestamptz NOT NULL,
  PRIMARY KEY (customer_account_id, idempotency_key)
);
CREATE INDEX cs_in_progress ON orchestration.checkout_sagas (updated_at) WHERE status = 'in_progress';
CREATE INDEX cs_finished ON orchestration.checkout_sagas (updated_at) WHERE status IN ('completed','aborted');
-- 契約者は事業者をまたぐので RLS の対象外。主キーに customer_account_id を含めて分離する（§6）

CREATE TABLE orchestration.billing_runs (
  id             uuid PRIMARY KEY,
  run_date       date NOT NULL,
  seq            int  NOT NULL,
  trigger        text NOT NULL CHECK (trigger IN ('scheduled','rerun')),
  only_tenant_id uuid NULL,                            -- §6 の tenant_id（rerun の絞り込み）。事業者のデータではないので RLS をかけず、名前を変える（P-IMP-17）
  status         text NOT NULL CHECK (status IN ('dispatching','dispatched','failed')),
  cursor         text NULL,
  enqueued_count int  NOT NULL DEFAULT 0,
  resume_count   int  NOT NULL DEFAULT 0,
  requested_by   text NULL,
  started_at     timestamptz NOT NULL,
  finished_at    timestamptz NULL,
  UNIQUE (run_date, seq)
);
CREATE UNIQUE INDEX br_one_dispatching ON orchestration.billing_runs (run_date) WHERE status = 'dispatching';

CREATE TABLE orchestration.bulk_cancellation_jobs (
  id               uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  source_event_id  uuid NOT NULL UNIQUE,
  status           text NOT NULL CHECK (status IN ('running','completed')),
  sweep_round      smallint NOT NULL DEFAULT 1 CHECK (sweep_round IN (1,2)),
  cursor           text NULL,
  processed_count  int NOT NULL DEFAULT 0,
  scheduled_count  int NOT NULL DEFAULT 0,
  canceled_count   int NOT NULL DEFAULT 0,
  skipped_count    int NOT NULL DEFAULT 0,
  failed_count     int NOT NULL DEFAULT 0,
  lease_until      timestamptz NULL,
  started_at       timestamptz NOT NULL,
  completed_at     timestamptz NULL,
  version          int NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX bcj_one_running ON orchestration.bulk_cancellation_jobs (tenant_id) WHERE status = 'running';

CREATE TABLE orchestration.bulk_cancellation_failures (
  job_id          uuid NOT NULL REFERENCES orchestration.bulk_cancellation_jobs(id),
  tenant_id       uuid NOT NULL,
  subscription_id uuid NOT NULL,
  error           text NOT NULL,
  occurred_at     timestamptz NOT NULL,
  PRIMARY KEY (job_id, subscription_id)
);

-- 一括解約で処理した契約（件数の二重計上を防ぐ。途中で落ちて同じチャンクを再処理しても、初めての行だけを数える。P-IMP-17）
CREATE TABLE orchestration.bulk_cancellation_items (
  job_id          uuid NOT NULL REFERENCES orchestration.bulk_cancellation_jobs(id),
  tenant_id       uuid NOT NULL,
  subscription_id uuid NOT NULL,
  result          text NOT NULL CHECK (result IN ('scheduled','canceled','skipped')),
  processed_at    timestamptz NOT NULL,
  PRIMARY KEY (job_id, subscription_id)
);

-- プロセス・一括解約は事業者ごと。ジョブ・sweeper・ops は BYPASSRLS のロールで横断する（§6）
SELECT platform.enable_tenant_rls('orchestration.billing_cycle_processes');
SELECT platform.enable_tenant_rls('orchestration.bulk_cancellation_jobs');
SELECT platform.enable_tenant_rls('orchestration.bulk_cancellation_failures');
SELECT platform.enable_tenant_rls('orchestration.bulk_cancellation_items');

-- +goose Down
DROP TABLE orchestration.bulk_cancellation_items;
DROP TABLE orchestration.bulk_cancellation_failures;
DROP TABLE orchestration.bulk_cancellation_jobs;
DROP TABLE orchestration.billing_runs;
DROP TABLE orchestration.checkout_sagas;
DROP TABLE orchestration.billing_cycle_processes;
DROP TABLE orchestration.processed_messages;
DROP TABLE orchestration.outbox_events;
