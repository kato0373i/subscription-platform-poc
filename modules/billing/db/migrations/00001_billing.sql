-- billing スキーマ（billing.md §6）
-- +goose Up
SELECT platform.create_messaging_tables('billing');

CREATE TABLE billing.invoices (
  id                     uuid PRIMARY KEY,
  tenant_id              uuid NOT NULL,
  subscription_id        uuid NOT NULL,
  customer_account_id    uuid NOT NULL,
  kind                   text NOT NULL CHECK (kind IN ('initial','renewal')),
  collection_mode        text NOT NULL CHECK (collection_mode IN ('scheduled','caller_managed')),
  period_start           date NOT NULL,
  period_end             date NOT NULL CHECK (period_end >= period_start),
  status                 text NOT NULL CHECK (status IN ('open','processing','payment_unknown','paid','uncollectible','void')),
  currency               char(3) NOT NULL DEFAULT 'JPY' CHECK (currency = 'JPY'),
  subtotal_amount        integer NOT NULL CHECK (subtotal_amount BETWEEN 1 AND 1000000),
  discount_amount        integer NOT NULL CHECK (discount_amount >= 0),
  total_amount           integer NOT NULL CHECK (total_amount >= 0),
  tax_amount             integer NOT NULL CHECK (tax_amount >= 0),
  tax_breakdown          jsonb NOT NULL,            -- [{rate_num, rate_den, taxable_amount, tax_amount}]
  issued_at              timestamptz NOT NULL,
  paid_at                timestamptz NULL,
  paid_attempt_id        uuid NULL,
  paid_via               text NULL CHECK (paid_via IN ('payment','zero_amount')),
  uncollectible_at       timestamptz NULL,
  voided_at              timestamptz NULL,
  void_reason            text NULL CHECK (void_reason IN ('signup_failed','subscription_canceled','tenant_suspended')),
  void_requested_at      timestamptz NULL,
  void_requested_reason  text NULL,
  attempt_count          integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  dunning_stage          integer NOT NULL DEFAULT 0 CHECK (dunning_stage BETWEEN 0 AND 6),
  dunning_schedule       jsonb NULL,                -- {offsets_days, policy_source, first_failed_on, snapshotted_at}
  next_collection_at     timestamptz NULL,
  collection_seq         integer NOT NULL DEFAULT 0 CHECK (collection_seq >= 0),
  pending_request        jsonb NULL,                -- {seq, reason, requested_by, requested_at}
  collection_lease_until timestamptz NULL,
  last_attempt_id        uuid NULL,
  last_failure_code      text NULL,
  last_failure_retryable boolean NULL,
  version                integer NOT NULL DEFAULT 1,
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT invoices_total CHECK (total_amount = subtotal_amount - discount_amount),
  CONSTRAINT invoices_paid CHECK ((status = 'paid') = (paid_at IS NOT NULL)),
  CONSTRAINT invoices_void CHECK ((status = 'void') = (voided_at IS NOT NULL)),
  CONSTRAINT invoices_caller_managed CHECK (collection_mode = 'scheduled' OR (next_collection_at IS NULL AND dunning_schedule IS NULL AND collection_seq = 1)),
  CONSTRAINT invoices_period UNIQUE (subscription_id, period_start)
);
CREATE UNIQUE INDEX invoices_one_unsettled ON billing.invoices (subscription_id) WHERE status IN ('open','processing','payment_unknown');
CREATE INDEX invoices_due ON billing.invoices (next_collection_at, id) WHERE status = 'open' AND collection_mode = 'scheduled';
CREATE INDEX invoices_tenant_sub ON billing.invoices (tenant_id, subscription_id, period_start DESC, id);
CREATE INDEX invoices_customer_sub ON billing.invoices (customer_account_id, subscription_id, period_start DESC, id);
CREATE INDEX invoices_tenant_issued ON billing.invoices (tenant_id, issued_at DESC, id);

CREATE TABLE billing.invoice_lines (
  invoice_id        uuid NOT NULL REFERENCES billing.invoices(id),
  line_no           smallint NOT NULL,
  tenant_id         uuid NOT NULL,
  kind              text NOT NULL CHECK (kind IN ('plan','discount')),
  description       text NOT NULL,
  price_version_id  uuid NULL,
  discount_snapshot jsonb NULL,                     -- {type, value, coupon_redemption_id}
  quantity          integer NOT NULL DEFAULT 1 CHECK (quantity >= 1),
  unit_amount       integer NOT NULL CHECK (unit_amount >= 0),
  amount            integer NOT NULL CHECK (amount >= 0),
  tax_rate_num      integer NOT NULL,
  tax_rate_den      integer NOT NULL CHECK (tax_rate_den > 0),
  period_start      date NOT NULL,
  period_end        date NOT NULL,
  PRIMARY KEY (invoice_id, line_no),
  CHECK ((kind = 'plan') = (price_version_id IS NOT NULL)),
  CHECK ((kind = 'discount') = (discount_snapshot IS NOT NULL))
);

CREATE TABLE billing.invoice_payment_records (
  attempt_id   uuid NOT NULL,
  outcome      text NOT NULL CHECK (outcome IN ('succeeded','failed','pending','unknown')),
  invoice_id   uuid NOT NULL REFERENCES billing.invoices(id),
  tenant_id    uuid NOT NULL,
  amount       integer NOT NULL,
  failure_code text NULL,
  retryable    boolean NULL,
  source       text NOT NULL CHECK (source IN ('sync','event')),
  applied      boolean NOT NULL,
  anomaly      text NULL CHECK (anomaly IN ('duplicate_success','late_success','paid_after_void_request','amount_mismatch')),
  recorded_at  timestamptz NOT NULL,
  PRIMARY KEY (attempt_id, outcome)
);
CREATE INDEX invoice_payment_records_invoice ON billing.invoice_payment_records (invoice_id, recorded_at);
CREATE INDEX invoice_payment_records_anomaly ON billing.invoice_payment_records (recorded_at) WHERE anomaly IS NOT NULL;
-- 結果の記録は追記だけ（§6）
REVOKE UPDATE, DELETE ON billing.invoice_payment_records FROM sp_billing, sp_billing_sys;

CREATE TABLE billing.dunning_policies (
  tenant_id          uuid PRIMARY KEY,
  retry_offsets_days integer[] NOT NULL CHECK (cardinality(retry_offsets_days) <= 5),
  version            integer NOT NULL DEFAULT 1,
  updated_by         text NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);
-- 各要素の範囲と昇順はドメインで検証する（DB は個数だけ守る）

SELECT platform.enable_tenant_rls('billing.invoices');
SELECT platform.enable_tenant_rls('billing.invoice_lines');
SELECT platform.enable_tenant_rls('billing.invoice_payment_records');
SELECT platform.enable_tenant_rls('billing.dunning_policies');
-- 契約者は自分の請求を読むだけ（SELECT のみ。明細は親の請求で判定する。§6 consumer_read）
CREATE POLICY consumer_read ON billing.invoices FOR SELECT USING (customer_account_id = platform.current_customer_id());
CREATE POLICY consumer_read ON billing.invoice_lines FOR SELECT USING (EXISTS (SELECT 1 FROM billing.invoices i WHERE i.id = invoice_id));

-- +goose Down
DROP TABLE billing.dunning_policies;
DROP TABLE billing.invoice_payment_records;
DROP TABLE billing.invoice_lines;
DROP TABLE billing.invoices;
DROP TABLE billing.processed_messages;
DROP TABLE billing.outbox_events;
