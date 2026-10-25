-- reporting スキーマ（console-dashboard 設計 §4）。イベントから作る集計用の読み取りデータ。Outbox は使わず Inbox だけ
-- +goose Up
SELECT platform.create_messaging_tables('reporting');

-- 契約ごとの集計の状態（設計 §3）
CREATE TABLE reporting.subscription_facts (
  subscription_id  uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  status           text NOT NULL CHECK (status IN ('pending_initial','trialing','active','past_due','canceled')),
  mrr              bigint NOT NULL CHECK (mrr >= 0),
  price_version_id uuid NOT NULL,
  billing_interval text NOT NULL CHECK (billing_interval IN ('month','year')),
  quantity         integer NOT NULL CHECK (quantity >= 1),
  started_in_trial boolean NOT NULL,
  has_been_paying  boolean NOT NULL,
  trial_end        date NULL,
  last_version     integer NOT NULL,
  updated_at       timestamptz NOT NULL,
  CONSTRAINT subscription_facts_mrr CHECK (mrr = 0 OR status IN ('active','past_due'))
);
-- トライアル終了間近の数（要件 FR-006）
CREATE INDEX subscription_facts_trial_end ON reporting.subscription_facts (tenant_id, trial_end) WHERE status = 'trialing';

-- catalog の価格バージョンの金額の写し（価格バージョンは追記のみで変わらない。ADR-R03）
CREATE TABLE reporting.price_amounts (
  price_version_id uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  amount           bigint NOT NULL CHECK (amount >= 0),
  created_at       timestamptz NOT NULL
);

-- 事業者 × 日（JST）の増減（ADR-R02）。ストックはこの差分の累積で求める
CREATE TABLE reporting.daily_metrics (
  tenant_id         uuid NOT NULL,
  day               date NOT NULL,
  mrr_delta         bigint NOT NULL DEFAULT 0,
  paying_delta      integer NOT NULL DEFAULT 0,
  trialing_delta    integer NOT NULL DEFAULT 0,
  past_due_delta    integer NOT NULL DEFAULT 0,
  new_mrr           bigint NOT NULL DEFAULT 0 CHECK (new_mrr >= 0),
  expansion_mrr     bigint NOT NULL DEFAULT 0 CHECK (expansion_mrr >= 0),
  contraction_mrr   bigint NOT NULL DEFAULT 0 CHECK (contraction_mrr >= 0),
  churned_mrr       bigint NOT NULL DEFAULT 0 CHECK (churned_mrr >= 0),
  new_subscriptions integer NOT NULL DEFAULT 0 CHECK (new_subscriptions >= 0),
  canceled_total    integer NOT NULL DEFAULT 0 CHECK (canceled_total >= 0),
  canceled_paying   integer NOT NULL DEFAULT 0 CHECK (canceled_paying >= 0),
  canceled_customer integer NOT NULL DEFAULT 0 CHECK (canceled_customer >= 0),
  canceled_dunning  integer NOT NULL DEFAULT 0 CHECK (canceled_dunning >= 0),
  canceled_tenant   integer NOT NULL DEFAULT 0 CHECK (canceled_tenant >= 0),
  trial_converted   integer NOT NULL DEFAULT 0 CHECK (trial_converted >= 0),
  trial_lost        integer NOT NULL DEFAULT 0 CHECK (trial_lost >= 0),
  recovered         integer NOT NULL DEFAULT 0 CHECK (recovered >= 0),
  dunning_lost      integer NOT NULL DEFAULT 0 CHECK (dunning_lost >= 0),
  revenue_initial   bigint NOT NULL DEFAULT 0 CHECK (revenue_initial >= 0),
  revenue_renewal   bigint NOT NULL DEFAULT 0 CHECK (revenue_renewal >= 0),
  updated_at        timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, day)
);

-- 月ごとの MRR の目標（要件 FR-002。month は月初の日）
CREATE TABLE reporting.mrr_goals (
  tenant_id  uuid NOT NULL,
  month      date NOT NULL CHECK (extract(day FROM month) = 1),
  amount     bigint NOT NULL CHECK (amount BETWEEN 1 AND 1000000000),
  version    integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL,
  updated_by text NOT NULL,
  PRIMARY KEY (tenant_id, month)
);

SELECT platform.enable_tenant_rls('reporting.subscription_facts');
SELECT platform.enable_tenant_rls('reporting.price_amounts');
SELECT platform.enable_tenant_rls('reporting.daily_metrics');
SELECT platform.enable_tenant_rls('reporting.mrr_goals');

-- +goose Down
DROP TABLE reporting.mrr_goals;
DROP TABLE reporting.daily_metrics;
DROP TABLE reporting.price_amounts;
DROP TABLE reporting.subscription_facts;
DROP TABLE reporting.processed_messages;
DROP TABLE reporting.outbox_events;
