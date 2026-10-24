-- subscription スキーマ（subscription.md §6）
-- +goose Up
SELECT platform.create_messaging_tables('subscription');

CREATE TABLE subscription.subscriptions (
  id                          uuid PRIMARY KEY,
  tenant_id                   uuid NOT NULL,
  customer_account_id         uuid NOT NULL,
  tenant_customer_id          text NOT NULL,
  signup_request_id           text NOT NULL CHECK (char_length(signup_request_id) BETWEEN 1 AND 255),
  product_id                  uuid NOT NULL,
  plan_id                     uuid NOT NULL,
  price_version_id            uuid NOT NULL,
  billing_interval            text NOT NULL CHECK (billing_interval IN ('month','year')),
  quantity                    integer NOT NULL DEFAULT 1 CHECK (quantity >= 1),
  status                      text NOT NULL CHECK (status IN ('pending_initial','trialing','active','past_due','canceled','signup_failed')),
  signup_date                 date NOT NULL,
  current_period_start        date NOT NULL,
  current_period_end          date NOT NULL,
  next_renewal_date           date NULL,
  billing_anchor_day          smallint NOT NULL CHECK (billing_anchor_day BETWEEN 1 AND 31),
  trial_start                 date NULL,
  trial_end                   date NULL,
  trial_will_end_notified     boolean NOT NULL DEFAULT false,
  pending_plan_id             uuid NULL,
  pending_price_version_id    uuid NULL,
  pending_plan_interval       text NULL CHECK (pending_plan_interval IN ('month','year')),
  pending_plan_effective_date date NULL,
  cancel_at_period_end        boolean NOT NULL DEFAULT false,
  cancellation_source         text NULL CHECK (cancellation_source IN ('customer','system')),
  cancellation_reason         text NULL CHECK (cancellation_reason IN ('customer','tenant_suspended')),
  cancellation_effective_date date NULL,
  pending_renewal             jsonb NULL,
  applied_discount            jsonb NULL,
  payment_method_id           uuid NOT NULL,
  past_due_invoice_id         uuid NULL,
  last_paid_invoice_id        uuid NULL,
  cancel_reason               text NULL CHECK (cancel_reason IN ('customer','dunning','tenant_suspended')),
  canceled_at                 timestamptz NULL,
  signup_failure_reason       text NULL,
  version                     integer NOT NULL DEFAULT 1,
  created_at                  timestamptz NOT NULL,
  updated_at                  timestamptz NOT NULL,
  UNIQUE (customer_account_id, signup_request_id),
  CHECK (current_period_start <= current_period_end),
  CHECK (cancel_at_period_end = (cancellation_source IS NOT NULL)),
  CHECK ((cancellation_source IS NULL) = (cancellation_effective_date IS NULL)),
  CHECK ((pending_plan_id IS NULL) = (pending_plan_effective_date IS NULL)),
  CHECK (NOT (cancel_at_period_end AND pending_plan_id IS NOT NULL)),
  CHECK (status NOT IN ('canceled','signup_failed')
         OR (next_renewal_date IS NULL AND pending_renewal IS NULL AND NOT cancel_at_period_end AND pending_plan_id IS NULL)),
  CHECK (status <> 'canceled' OR (cancel_reason IS NOT NULL AND canceled_at IS NOT NULL)),
  CHECK (status <> 'trialing' OR trial_end IS NOT NULL),
  CHECK (status <> 'past_due' OR (pending_renewal IS NOT NULL AND past_due_invoice_id IS NOT NULL))
);
CREATE INDEX subscriptions_due_idx ON subscription.subscriptions (next_renewal_date, id) WHERE status IN ('trialing','active');
CREATE INDEX subscriptions_trial_end_idx ON subscription.subscriptions (trial_end, id) WHERE status = 'trialing';
CREATE INDEX subscriptions_console_idx ON subscription.subscriptions (tenant_id, status, created_at DESC, id);
CREATE INDEX subscriptions_tenant_list_idx ON subscription.subscriptions (tenant_id, created_at DESC, id);
CREATE INDEX subscriptions_tenant_customer_idx ON subscription.subscriptions (tenant_id, tenant_customer_id, created_at DESC, id);
CREATE INDEX subscriptions_customer_idx ON subscription.subscriptions (customer_account_id, created_at DESC, id);
CREATE INDEX subscriptions_open_by_tenant_idx ON subscription.subscriptions (tenant_id, id) WHERE status IN ('pending_initial','trialing','active','past_due');
CREATE INDEX subscriptions_payment_method_idx ON subscription.subscriptions (payment_method_id) WHERE status IN ('pending_initial','trialing','active','past_due');

CREATE TABLE subscription.trial_usages (
  customer_account_id uuid NOT NULL,
  product_id          uuid NOT NULL,
  tenant_id           uuid NOT NULL,
  subscription_id     uuid NOT NULL,
  used_on             date NOT NULL,
  created_at          timestamptz NOT NULL,
  PRIMARY KEY (customer_account_id, product_id)
);

-- tenant_isolation（console・tenant API・テナントが分かっているコマンド）と consumer_self（/v1/me、事業者をまたぐ一覧）
SELECT platform.enable_tenant_rls('subscription.subscriptions');
SELECT platform.enable_customer_rls('subscription.subscriptions', 'customer_account_id');
SELECT platform.enable_tenant_rls('subscription.trial_usages');
SELECT platform.enable_customer_rls('subscription.trial_usages', 'customer_account_id');

-- +goose Down
DROP TABLE subscription.trial_usages;
DROP TABLE subscription.subscriptions;
DROP TABLE subscription.processed_messages;
DROP TABLE subscription.outbox_events;
