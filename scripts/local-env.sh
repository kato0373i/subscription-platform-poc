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

# スタッフの Cognito のプール（事業者コンソール。business-console §5.2・§5.3）。Terraform の出力から読む
# 契約者のプール（マイページ。consumer-mypage §5.4）も同じく読む
STAFF_POOL_ID="" STAFF_CLIENT_ID="" CONSUMER_POOL_ID="" CONSUMER_CLIENT_ID="" OPERATOR_POOL_ID="" OPERATOR_CLIENT_ID=""
if cog="$(./scripts/tf.sh output -json cognito 2>/dev/null)" && [[ -n "$cog" ]]; then
  pool() { printf '%s' "$cog" | python3 -c "import sys,json;print(json.load(sys.stdin).get('$1',{}).get('$2',''))" 2>/dev/null || true; }
  STAFF_POOL_ID="$(pool staff id)"
  STAFF_CLIENT_ID="$(pool staff client_id)"
  CONSUMER_POOL_ID="$(pool consumer id)"
  CONSUMER_CLIENT_ID="$(pool consumer client_id)"
  OPERATOR_POOL_ID="$(pool operator id)"
  OPERATOR_CLIENT_ID="$(pool operator client_id)"
fi

# MiniStack は、パスワードの方針で指定しなかった項目を「必須」として扱う（本物の Cognito は「不要」）。Terraform の provider は false の項目を
# 送らないので、契約者のプールだけ方針を明示して上書きする（consumer_auth の定義と同じ値。ローカル専用の回避策）
if [[ -n "$CONSUMER_POOL_ID" ]]; then
  curl -fsS -o /dev/null -X POST http://localhost:4566/ -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.UpdateUserPool' \
    -H 'Authorization: AWS4-HMAC-SHA256 Credential=test/20260101/ap-northeast-1/cognito-idp/aws4_request, SignedHeaders=host, Signature=local' \
    -d "{\"UserPoolId\":\"$CONSUMER_POOL_ID\",\"Policies\":{\"PasswordPolicy\":{\"MinimumLength\":8,\"RequireLowercase\":true,\"RequireNumbers\":true,\"RequireUppercase\":false,\"RequireSymbols\":false}}}" \
    || echo "local-env: 契約者のプールのパスワードの方針を上書きできませんでした" >&2
fi

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
CONSOLE_URL=http://localhost:8090/console/
MYPAGE_URL=http://localhost:8090/mypage/
PAYMENT_METHOD_UPDATE_URL_TEMPLATE=http://localhost:8090/mypage/subscriptions/{subscription_id}/payment-method
ENV
  if [[ -n "$STAFF_POOL_ID" ]]; then
    # MiniStack の ID トークンの iss は本物の AWS の URL なので、JWKS の取得先だけ MiniStack に向ける（下の行で環境ごとに書く）
    echo "COGNITO_STAFF_ISSUER=https://cognito-idp.ap-northeast-1.amazonaws.com/$STAFF_POOL_ID"
    echo "COGNITO_STAFF_CLIENT_ID=$STAFF_CLIENT_ID"
    echo "COGNITO_STAFF_USER_POOL_ID=$STAFF_POOL_ID"
  fi
  if [[ -n "$CONSUMER_POOL_ID" ]]; then
    echo "COGNITO_CONSUMER_ISSUER=https://cognito-idp.ap-northeast-1.amazonaws.com/$CONSUMER_POOL_ID"
    echo "COGNITO_CONSUMER_CLIENT_ID=$CONSUMER_CLIENT_ID"
    echo "COGNITO_CONSUMER_USER_POOL_ID=$CONSUMER_POOL_ID"
  fi
  if [[ -n "$OPERATOR_POOL_ID" ]]; then
    echo "COGNITO_OPERATOR_ISSUER=https://cognito-idp.ap-northeast-1.amazonaws.com/$OPERATOR_POOL_ID"
    echo "COGNITO_OPERATOR_CLIENT_ID=$OPERATOR_CLIENT_ID"
    echo "COGNITO_OPERATOR_USER_POOL_ID=$OPERATOR_POOL_ID"
  fi
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
  [[ -n "$STAFF_POOL_ID" ]] && echo "COGNITO_STAFF_JWKS_URL=http://localhost:4566/$STAFF_POOL_ID/.well-known/jwks.json"
  [[ -n "$CONSUMER_POOL_ID" ]] && echo "COGNITO_CONSUMER_JWKS_URL=http://localhost:4566/$CONSUMER_POOL_ID/.well-known/jwks.json"
  [[ -n "$OPERATOR_POOL_ID" ]] && echo "COGNITO_OPERATOR_JWKS_URL=http://localhost:4566/$OPERATOR_POOL_ID/.well-known/jwks.json"
  true
} > .env.local

{
  echo "# task up が書き出す（compose の profile app のコンテナが読む）"
  common
  echo "DATABASE_URL=postgres://sp_app:sp_app_local@$DB_CONTAINER:5432/sp?sslmode=disable"
  echo "AWS_ENDPOINT_URL=http://ministack:4566"
  echo "QUEUE_URL_PREFIX=http://ministack:4566/000000000000/"
  [[ -n "$STAFF_POOL_ID" ]] && echo "COGNITO_STAFF_JWKS_URL=http://ministack:4566/$STAFF_POOL_ID/.well-known/jwks.json"
  [[ -n "$CONSUMER_POOL_ID" ]] && echo "COGNITO_CONSUMER_JWKS_URL=http://ministack:4566/$CONSUMER_POOL_ID/.well-known/jwks.json"
  [[ -n "$OPERATOR_POOL_ID" ]] && echo "COGNITO_OPERATOR_JWKS_URL=http://ministack:4566/$OPERATOR_POOL_ID/.well-known/jwks.json"
  true
} > .env.container

# 事業者コンソール（SPA）のランタイムの設定（Vite の public。ビルドで dist に入り、nginx が配る。本番は S3 に別に置く）
mkdir -p web/console/public
cat > web/console/public/config.json <<JSON
{"cognito":{"region":"ap-northeast-1","userPoolId":"$STAFF_POOL_ID","clientId":"$STAFF_CLIENT_ID","endpoint":"http://localhost:4566"}}
JSON

# マイページ（SPA）のランタイムの設定（consumer-mypage §7）
mkdir -p web/mypage/public
cat > web/mypage/public/config.json <<JSON
{"cognito":{"region":"ap-northeast-1","userPoolId":"$CONSUMER_POOL_ID","clientId":"$CONSUMER_CLIENT_ID","endpoint":"http://localhost:4566"}}
JSON

# 運営者コンソール（SPA）のランタイムの設定（operator-console §7）
mkdir -p web/ops/public
cat > web/ops/public/config.json <<JSON
{"cognito":{"region":"ap-northeast-1","userPoolId":"$OPERATOR_POOL_ID","clientId":"$OPERATOR_CLIENT_ID","endpoint":"http://localhost:4566"}}
JSON

{
  echo "# task up が書き出す（Playwright が読む）"
  echo "API_BASE_URL=http://localhost:8080"
  echo "SINK_CONTROL_URL=http://localhost:18080"
  echo "CONSOLE_BASE_URL=http://localhost:8090"
  echo "COGNITO_STAFF_USER_POOL_ID=$STAFF_POOL_ID"
  echo "COGNITO_CONSUMER_USER_POOL_ID=$CONSUMER_POOL_ID"
  echo "COGNITO_OPERATOR_USER_POOL_ID=$OPERATOR_POOL_ID"
  echo "AWS_ENDPOINT_URL=http://localhost:4566"
  echo "SINK_TARGET_BASE=${SINK_TARGET_BASE:-http://localhost:18080}"
  echo "E2E_READER_URL=postgres://sp_e2e_reader:sp_e2e_reader_local@localhost:$HOST_PORT/sp?sslmode=disable"
  echo "DEVLOGS_BIN=../.local/bin/devlogs"
  echo "LOG_DIR=../.local/logs"
} > e2e/.env.e2e

echo "local-env: DB=$DB_CONTAINER (localhost:$HOST_PORT)、.env.local・.env.container・e2e/.env.e2e を書き出しました"
