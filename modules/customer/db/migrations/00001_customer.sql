-- customer スキーマ（identity.md §6.2・§6.3）
-- +goose Up
SELECT platform.create_messaging_tables('customer');

-- テナントに属さない。customer モジュールのロールだけが権限を持つ（RLS なし）
CREATE TABLE customer.customer_accounts (
  id            uuid PRIMARY KEY,
  subject       text NOT NULL UNIQUE CHECK (char_length(subject) BETWEEN 1 AND 255),
  email         text NULL CHECK (email = lower(email) AND char_length(email) <= 254),
  name          text NULL CHECK (char_length(name) <= 100),
  status        text NOT NULL CHECK (status IN ('active','withdrawn')),
  withdrawn_at  timestamptz NULL,
  version       int NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL,
  CHECK ( (status = 'active'    AND email IS NOT NULL AND withdrawn_at IS NULL)
       OR (status = 'withdrawn' AND email IS NULL AND name IS NULL AND withdrawn_at IS NOT NULL) )
);
CREATE UNIQUE INDEX customer_accounts_email_active_uq ON customer.customer_accounts (email) WHERE status = 'active';

CREATE TABLE customer.tenant_customer_links (
  id                   text PRIMARY KEY CHECK (id ~ '^cus_[0-9A-Za-z]{24}$'),
  tenant_id            uuid NOT NULL,
  customer_account_id  uuid NOT NULL REFERENCES customer.customer_accounts(id),
  created_at           timestamptz NOT NULL,
  UNIQUE (tenant_id, customer_account_id)
);
CREATE INDEX tenant_customer_links_account_idx ON customer.tenant_customer_links (customer_account_id);

SELECT platform.enable_tenant_rls('customer.tenant_customer_links');

-- +goose Down
DROP TABLE customer.tenant_customer_links;
DROP TABLE customer.customer_accounts;
DROP TABLE customer.processed_messages;
DROP TABLE customer.outbox_events;
