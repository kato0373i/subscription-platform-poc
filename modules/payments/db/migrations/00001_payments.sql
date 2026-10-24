-- payments スキーマ（payments.md §6・付録 C）
-- +goose Up
SELECT platform.create_messaging_tables('payments');

CREATE TABLE payments.payment_methods (
  id                  uuid PRIMARY KEY,
  customer_account_id uuid NOT NULL,
  method_type         text NOT NULL CHECK (method_type IN ('card')),
  status              text NOT NULL CHECK (status IN ('active','deleted')),
  display             jsonb NOT NULL,
  registration_key    text NULL,
  deleted_at          timestamptz NULL,
  version             int NOT NULL DEFAULT 1,
  created_at          timestamptz NOT NULL,
  updated_at          timestamptz NOT NULL,
  UNIQUE (customer_account_id, registration_key)
);
CREATE INDEX payment_methods_active_idx ON payments.payment_methods (customer_account_id, created_at DESC, id) WHERE status = 'active';

CREATE TABLE payments.payment_method_provider_refs (
  id                  uuid PRIMARY KEY,
  payment_method_id   uuid NOT NULL REFERENCES payments.payment_methods(id),
  customer_account_id uuid NOT NULL,
  provider            text NOT NULL,
  tenant_shop_id      text NULL,
  customer_ref        text NOT NULL,
  method_ref          text NOT NULL,
  created_at          timestamptz NOT NULL
);
CREATE UNIQUE INDEX payment_method_provider_refs_uq ON payments.payment_method_provider_refs (payment_method_id, provider, COALESCE(tenant_shop_id, ''));

CREATE TABLE payments.payment_attempts (
  id                  uuid PRIMARY KEY,
  tenant_id           uuid NOT NULL,
  payable_type        text NOT NULL CHECK (payable_type IN ('invoice')),
  payable_id          uuid NOT NULL,
  customer_account_id uuid NOT NULL,
  payment_method_id   uuid NOT NULL REFERENCES payments.payment_methods(id),
  method_type         text NOT NULL,
  provider            text NOT NULL,
  tenant_shop_id      text NULL,
  amount              bigint NOT NULL CHECK (amount BETWEEN 1 AND 1000000),
  idempotency_key     text NOT NULL UNIQUE,
  collection_seq      int NOT NULL CHECK (collection_seq >= 1),
  request_hash        text NOT NULL,
  provider_order_ref  text NULL UNIQUE,
  status              text NOT NULL CHECK (status IN ('processing','pending_async','succeeded','failed','unknown')),
  failure_code        text NULL,
  failure_retryable   boolean NULL,
  provider_raw_code   text NULL,
  next_action         jsonb NULL,
  resolution          text NULL CHECK (resolution IN ('sync','webhook','reconcile','manual')),
  resolved_by         text NULL,
  resolve_note        text NULL,
  check_count         int NOT NULL DEFAULT 0,
  last_checked_at     timestamptz NULL,
  requested_at        timestamptz NOT NULL,
  finalized_at        timestamptz NULL,
  version             int NOT NULL DEFAULT 1,
  created_at          timestamptz NOT NULL,
  updated_at          timestamptz NOT NULL,
  CHECK ((status = 'failed') = (failure_code IS NOT NULL AND failure_retryable IS NOT NULL))
);
CREATE UNIQUE INDEX uq_attempt_succeeded ON payments.payment_attempts (payable_type, payable_id) WHERE status = 'succeeded';
CREATE UNIQUE INDEX uq_attempt_inflight ON payments.payment_attempts (payable_type, payable_id) WHERE status IN ('processing','pending_async','unknown');
CREATE INDEX ix_attempt_unresolved ON payments.payment_attempts (status, requested_at) WHERE status IN ('processing','unknown','pending_async');
CREATE INDEX ix_attempt_requested ON payments.payment_attempts (provider, requested_at);

CREATE TABLE payments.provider_events (
  provider           text NOT NULL,
  provider_event_id  text NOT NULL,
  provider_order_ref text NULL,
  payload            jsonb NOT NULL,
  received_at        timestamptz NOT NULL,
  status             text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processed','ignored','failed')),
  attempts           int NOT NULL DEFAULT 0,
  next_attempt_at    timestamptz NOT NULL,
  last_error         text NULL,
  processed_at       timestamptz NULL,
  PRIMARY KEY (provider, provider_event_id)
);
CREATE INDEX provider_events_pending_idx ON payments.provider_events (next_attempt_at) WHERE status = 'pending';

CREATE TABLE payments.provider_circuit_breakers (
  provider             text PRIMARY KEY,
  consecutive_failures int NOT NULL DEFAULT 0,
  open_until           timestamptz NULL,
  updated_at           timestamptz NOT NULL
);

CREATE TABLE payments.reconciliation_discrepancies (
  id                 uuid PRIMARY KEY,
  business_date      date NOT NULL,
  provider           text NOT NULL,
  kind               text NOT NULL CHECK (kind IN ('provider_succeeded_local_not','local_succeeded_provider_not','amount_mismatch','orphan_provider_charge','conflicting_final_outcome')),
  attempt_id         uuid NULL,
  tenant_id          uuid NULL,
  provider_order_ref text NOT NULL,
  detail             jsonb NOT NULL,
  status             text NOT NULL DEFAULT 'open' CHECK (status IN ('open','acknowledged')),
  detected_at        timestamptz NOT NULL,
  UNIQUE (business_date, provider, kind, provider_order_ref)
);
CREATE INDEX reconciliation_discrepancies_list_idx ON payments.reconciliation_discrepancies (business_date DESC, detected_at, id);

-- 試行は事業者ごと。支払方法は契約者ごと（事業者のデータではないので tenant の RLS をかけない。§6）
SELECT platform.enable_tenant_rls('payments.payment_attempts');
-- 照合の結果は運営者・ワーカー（BYPASSRLS）だけが触る。tenant_id を持つので念のため tenant の RLS もかける
SELECT platform.enable_tenant_rls('payments.reconciliation_discrepancies');
SELECT platform.enable_customer_rls('payments.payment_attempts', 'customer_account_id');
SELECT platform.enable_customer_rls('payments.payment_methods', 'customer_account_id');
SELECT platform.enable_customer_rls('payments.payment_method_provider_refs', 'customer_account_id');

-- +goose Down
DROP TABLE payments.reconciliation_discrepancies;
DROP TABLE payments.provider_circuit_breakers;
DROP TABLE payments.provider_events;
DROP TABLE payments.payment_attempts;
DROP TABLE payments.payment_method_provider_refs;
DROP TABLE payments.payment_methods;
DROP TABLE payments.processed_messages;
DROP TABLE payments.outbox_events;
