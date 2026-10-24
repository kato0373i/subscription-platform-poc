-- StubAdapter の状態（payments.md §4.5・付録 C）。devtools ビルドの migrate だけが流す（バージョン表は payments.goose_devtools_version）
-- +goose Up
CREATE TABLE payments.stub_methods (
  method_ref         text PRIMARY KEY,
  token              text NOT NULL UNIQUE,
  scenario           text NOT NULL,
  behavior_override  text NULL,
  override_remaining int NOT NULL DEFAULT 0,
  created_at         timestamptz NOT NULL
);
CREATE TABLE payments.stub_charges (
  order_ref       text PRIMARY KEY,
  provider        text NOT NULL,
  idempotency_key text NOT NULL UNIQUE,
  method_ref      text NOT NULL,
  amount          bigint NOT NULL,
  true_status     text NOT NULL CHECK (true_status IN ('succeeded','failed','pending')),
  failure_code    text NULL,
  scenario        text NOT NULL,
  created_at      timestamptz NOT NULL,
  settled_at      timestamptz NULL
);
CREATE INDEX stub_charges_created_idx ON payments.stub_charges (provider, created_at, order_ref);

-- +goose Down
DROP TABLE payments.stub_charges;
DROP TABLE payments.stub_methods;
