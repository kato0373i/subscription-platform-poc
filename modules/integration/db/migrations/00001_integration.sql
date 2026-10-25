-- integration スキーマ（integration.md §6・付録 A）
-- +goose Up
SELECT platform.create_messaging_tables('integration');

CREATE TABLE integration.webhook_endpoints (
  id                           uuid PRIMARY KEY,
  tenant_id                    uuid NOT NULL,
  url                          text NOT NULL CHECK (char_length(url) <= 2048),
  description                  text NOT NULL DEFAULT '' CHECK (char_length(description) <= 256),
  event_types                  text[] NOT NULL CHECK (cardinality(event_types) BETWEEN 1 AND 13),
  status                       text NOT NULL CHECK (status IN ('enabled','disabled')),
  api_version                  text NOT NULL,
  secret_key_id                text NOT NULL,            -- 暗号化に使った鍵の ID（鍵の入れ替えに備える）
  secret_current_ciphertext    bytea NOT NULL,           -- AES-256-GCM（nonce を含む）
  secret_previous_ciphertext   bytea NULL,
  previous_expires_at          timestamptz NULL,
  secret_rotated_at            timestamptz NULL,
  breaker_consecutive_failures integer NOT NULL DEFAULT 0 CHECK (breaker_consecutive_failures >= 0),  -- §8.6
  breaker_open_until           timestamptz NULL,
  dispatch_lease_until         timestamptz NULL,         -- 配信ループのリース（§8.1）
  dispatch_lease_owner         text NULL,
  deleted_at                   timestamptz NULL,
  version                      integer NOT NULL DEFAULT 1,
  created_at                   timestamptz NOT NULL,
  updated_at                   timestamptz NOT NULL,
  CONSTRAINT webhook_endpoints_previous CHECK ((secret_previous_ciphertext IS NULL) = (previous_expires_at IS NULL)),
  -- https のみ。ローカル例外（U3）のホストだけは DB でも許す。環境の判定はアプリが行う
  CONSTRAINT webhook_endpoints_https CHECK (url LIKE 'https://%' OR url ~ '^http://(localhost|127\.0\.0\.1|webhook-sink)(:[0-9]{1,5})?(/|$)')
);
CREATE INDEX webhook_endpoints_tenant ON integration.webhook_endpoints (tenant_id, created_at, id) WHERE deleted_at IS NULL;

CREATE TABLE integration.webhook_events (
  id                text PRIMARY KEY CHECK (id ~ '^evt_[0-9A-HJKMNP-TV-Z]{26}$'),
  tenant_id         uuid NOT NULL,
  source_event_id   uuid NOT NULL UNIQUE,
  event_type        text NOT NULL,
  aggregate_type    text NOT NULL,
  aggregate_id      uuid NOT NULL,
  aggregate_version integer NOT NULL,
  occurred_at       timestamptz NOT NULL,
  payload           text NOT NULL CHECK (octet_length(payload) <= 65536),  -- 送るバイト列そのもの（再送でも同じ）
  created_at        timestamptz NOT NULL
);
CREATE INDEX webhook_events_tenant ON integration.webhook_events (tenant_id, created_at DESC);

CREATE TABLE integration.webhook_deliveries (
  id                 uuid PRIMARY KEY,
  tenant_id          uuid NOT NULL,
  endpoint_id        uuid NOT NULL REFERENCES integration.webhook_endpoints(id),
  webhook_event_id   text NOT NULL REFERENCES integration.webhook_events(id),
  trigger            text NOT NULL CHECK (trigger IN ('auto','manual')),
  redelivery_of      uuid NULL REFERENCES integration.webhook_deliveries(id),
  status             text NOT NULL CHECK (status IN ('pending','succeeded','failed','abandoned')),
  attempt_count      integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  next_attempt_at    timestamptz NULL,
  first_attempted_at timestamptz NULL,
  last_attempted_at  timestamptz NULL,
  deadline_at        timestamptz NULL,
  last_status_code   integer NULL,
  last_error         text NULL,
  completed_at       timestamptz NULL,
  version            integer NOT NULL DEFAULT 1,
  created_at         timestamptz NOT NULL,
  updated_at         timestamptz NOT NULL,
  CONSTRAINT webhook_deliveries_pending CHECK ((status = 'pending') = (next_attempt_at IS NOT NULL)),
  CONSTRAINT webhook_deliveries_manual CHECK ((trigger = 'manual') = (redelivery_of IS NOT NULL))
);
CREATE UNIQUE INDEX webhook_deliveries_one_auto ON integration.webhook_deliveries (endpoint_id, webhook_event_id) WHERE trigger = 'auto';
CREATE UNIQUE INDEX webhook_deliveries_one_pending ON integration.webhook_deliveries (endpoint_id, webhook_event_id) WHERE status = 'pending';
CREATE INDEX webhook_deliveries_due ON integration.webhook_deliveries (endpoint_id, next_attempt_at, id) WHERE status = 'pending';
CREATE INDEX webhook_deliveries_tenant ON integration.webhook_deliveries (tenant_id, created_at DESC, id);
CREATE INDEX webhook_deliveries_endpoint ON integration.webhook_deliveries (endpoint_id, created_at DESC, id);

CREATE TABLE integration.webhook_delivery_attempts (
  id               uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  delivery_id      uuid NOT NULL REFERENCES integration.webhook_deliveries(id),
  attempt_no       integer NOT NULL CHECK (attempt_no >= 1),
  requested_at     timestamptz NOT NULL,
  duration_ms      integer NOT NULL,
  status_code      integer NULL,
  error_kind       text NULL CHECK (error_kind IN ('timeout','connection','tls','ssrf_blocked','http_status','response_too_slow')),
  response_excerpt text NULL CHECK (octet_length(response_excerpt) <= 1024),
  CONSTRAINT webhook_delivery_attempts_no UNIQUE (delivery_id, attempt_no)
);
-- 試行の履歴は追記だけ
REVOKE UPDATE, DELETE ON integration.webhook_delivery_attempts FROM sp_integration, sp_integration_sys;

CREATE TABLE integration.email_notifications (
  id                  uuid PRIMARY KEY,
  tenant_id           uuid NOT NULL,
  template_key        text NOT NULL,
  dedupe_key          text NOT NULL UNIQUE,
  recipient_type      text NOT NULL CHECK (recipient_type IN ('customer','tenant_owner')),
  customer_account_id uuid NULL,
  subscription_id     uuid NULL,
  invoice_id          uuid NULL,
  endpoint_id         uuid NULL,
  source_event_id     uuid NULL,
  variables           jsonb NOT NULL DEFAULT '{}',   -- 個人情報を入れない
  subject             text NULL,                     -- 送信後に入れる（§11.3）
  status              text NOT NULL CHECK (status IN ('pending','sent','failed','skipped')),
  attempt_count       integer NOT NULL DEFAULT 0,
  next_attempt_at     timestamptz NULL,
  locked_until        timestamptz NULL,
  provider_message_id text NULL,
  last_error          text NULL,
  sent_at             timestamptz NULL,
  created_at          timestamptz NOT NULL,
  updated_at          timestamptz NOT NULL,
  CONSTRAINT email_notifications_pending CHECK ((status = 'pending') = (next_attempt_at IS NOT NULL)),
  CONSTRAINT email_notifications_customer CHECK (recipient_type <> 'customer' OR customer_account_id IS NOT NULL)
);
CREATE INDEX email_notifications_due ON integration.email_notifications (next_attempt_at) WHERE status = 'pending';
CREATE INDEX email_notifications_customer_pending ON integration.email_notifications (customer_account_id) WHERE status = 'pending';

CREATE TABLE integration.invoice_summaries (
  invoice_id         uuid PRIMARY KEY,
  tenant_id          uuid NOT NULL,
  subscription_id    uuid NOT NULL,
  total              integer NOT NULL CHECK (total >= 0),
  next_collection_at timestamptz NULL,
  updated_at         timestamptz NOT NULL
);

SELECT platform.enable_tenant_rls('integration.webhook_endpoints');
SELECT platform.enable_tenant_rls('integration.webhook_events');
SELECT platform.enable_tenant_rls('integration.webhook_deliveries');
SELECT platform.enable_tenant_rls('integration.webhook_delivery_attempts');
SELECT platform.enable_tenant_rls('integration.email_notifications');
SELECT platform.enable_tenant_rls('integration.invoice_summaries');

-- +goose Down
DROP TABLE integration.invoice_summaries;
DROP TABLE integration.email_notifications;
DROP TABLE integration.webhook_delivery_attempts;
DROP TABLE integration.webhook_deliveries;
DROP TABLE integration.webhook_events;
DROP TABLE integration.webhook_endpoints;
DROP TABLE integration.processed_messages;
DROP TABLE integration.outbox_events;
