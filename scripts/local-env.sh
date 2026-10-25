#!/usr/bin/env bash
# DB の準備（rds_superuser 相当の sp_admin を作る）と、.env.local・.env.container・e2e/.env.e2e の書き出し（platform.md §8.4）。
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p e2e
DB_MODE="${DB_MODE:-rds}"
MESSAGING_MODE="${MESSAGING_MODE:-eventbridge}"
# MiniStack の Scheduler は ScheduleExpressionTimezone を無視して UTC で評価するので、ローカルの既定は internal（verify:ministack）
SCHEDULER_MODE="${SCHEDULER_MODE:-internal}"

if [[ "$DB_MODE" == "container" ]]; then
  DB_CONTAINER=sp-postgres-1
else
  DB_CONTAINER="$(docker ps --format '{{.Names}}' | grep -E '^ministack-rds-.*-instance-sp-local$' | head -1 || true)"
fi
if [[ -z "$DB_CONTAINER" ]]; then
  echo "local-env: PostgreSQL のコンテナが見つかりません（DB_MODE=$DB_MODE）" >&2
  exit 1
fi
HOST_PORT="$(docker port "$DB_CONTAINER" 5432/tcp | head -1 | sed 's/.*://')"

for _ in $(seq 1 60); do
  docker exec "$DB_CONTAINER" pg_isready -U spmaster -d sp >/dev/null 2>&1 && break
  sleep 1
done

# rds_superuser 相当に絞った準備用のロール（スーパーユーザーではない）。本番の権限との差で誤りを見逃さないため
docker exec -i "$DB_CONTAINER" psql -v ON_ERROR_STOP=1 -q -U spmaster -d sp <<'SQL'
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sp_admin') THEN
    CREATE ROLE sp_admin LOGIN CREATEROLE BYPASSRLS PASSWORD 'sp_admin_local';
  END IF;
END $$;
GRANT CREATE, CONNECT ON DATABASE sp TO sp_admin;
SQL

if [[ -f .env.local ]] && grep -q '^CURSOR_KEY=' .env.local; then
  CURSOR_KEY="$(grep '^CURSOR_KEY=' .env.local | cut -d= -f2-)"
else
  CURSOR_KEY="$(openssl rand -base64 32)"
fi

common() {
  cat <<ENV
APP_ENV=local
DB_MODE=$DB_MODE
MESSAGING_MODE=$MESSAGING_MODE
SCHEDULER_MODE=$SCHEDULER_MODE
AUTH_MODE=dev
AWS_REGION=ap-northeast-1
AWS_ACCESS_KEY_ID=test
AWS_SECRET_ACCESS_KEY=test
EVENT_BUS_NAME=sp-events
CURSOR_KEY=$CURSOR_KEY
SP_APP_PASSWORD=sp_app_local
E2E_READER_PASSWORD=sp_e2e_reader_local
WEBHOOK_INSECURE_ALLOWED_HOSTS=localhost,127.0.0.1,webhook-sink
MAIL_MODE=ses
LOG_LEVEL=debug
LOG_SINKS=stdout,file
LOG_HTTP_BODY=redacted
DEV_JWT_KEY_PATH=deploy/local/dev-jwt.pem
BILLING_COLLECTION_LEASE=1m
WEBHOOK_RETRY_JITTER=0
ENV
}

{
  echo "# task up が書き出す（手で編集しない）。go run で動かすプロセスと task のコマンドが読む"
  common
  echo "DB_CONTAINER=$DB_CONTAINER"
  echo "DATABASE_URL=postgres://sp_app:sp_app_local@localhost:$HOST_PORT/sp?sslmode=disable"
  echo "MIGRATE_DATABASE_URL=postgres://sp_admin:sp_admin_local@localhost:$HOST_PORT/sp?sslmode=disable"
  echo "E2E_READER_URL=postgres://sp_e2e_reader:sp_e2e_reader_local@localhost:$HOST_PORT/sp?sslmode=disable"
  echo "AWS_ENDPOINT_URL=http://localhost:4566"
  echo "QUEUE_URL_PREFIX=http://localhost:4566/000000000000/"
} > .env.local

{
  echo "# task up が書き出す（compose の profile app のコンテナが読む）"
  common
  echo "DATABASE_URL=postgres://sp_app:sp_app_local@$DB_CONTAINER:5432/sp?sslmode=disable"
  echo "AWS_ENDPOINT_URL=http://ministack:4566"
  echo "QUEUE_URL_PREFIX=http://ministack:4566/000000000000/"
} > .env.container

{
  echo "# task up が書き出す（Playwright が読む）"
  echo "API_BASE_URL=http://localhost:8080"
  echo "SINK_CONTROL_URL=http://localhost:18080"
  echo "SINK_TARGET_BASE=${SINK_TARGET_BASE:-http://localhost:18080}"
  echo "E2E_READER_URL=postgres://sp_e2e_reader:sp_e2e_reader_local@localhost:$HOST_PORT/sp?sslmode=disable"
  echo "DEVLOGS_BIN=../.local/bin/devlogs"
  echo "LOG_DIR=../.local/logs"
} > e2e/.env.e2e

echo "local-env: DB=$DB_CONTAINER (localhost:$HOST_PORT)、.env.local・.env.container・e2e/.env.e2e を書き出しました"
