#!/usr/bin/env bash
# MiniStack で使う機能を確かめる（platform.md §8.4 の表。CI でも毎回流す）。結果は .local/verify-ministack.txt にも残す。
set -uo pipefail
cd "$(dirname "$0")/.."
set -a; [[ -f .env.local ]] && source .env.local; set +a
E="${AWS_ENDPOINT_URL:-http://localhost:4566}"
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=ap-northeast-1
out=.local/verify-ministack.txt
mkdir -p .local
: > "$out"
ok=0
# OK 以外でも、フォールバックが決まっているもの（→ を含む・「なし」）は失敗にしない
report() { printf '%-28s %s\n' "$1" "$2" | tee -a "$out"; [[ "$2" == OK* || "$2" == *"→"* || "$2" == なし* || "$2" == SKIP* ]] || ok=1; }
aws_() { aws --endpoint-url "$E" "$@"; }

# RDS: PostgreSQL 16、PG16 の GRANT 構文（bootstrap が通ったか）
if [[ -n "${DB_CONTAINER:-}" ]]; then
  v="$(docker exec "$DB_CONTAINER" psql -Atq -U spmaster -d sp -c 'show server_version_num' 2>/dev/null)"
  [[ "$v" == 16* ]] && report "RDS PostgreSQL 16" "OK ($v)" || report "RDS PostgreSQL 16" "NG ($v)"
  g="$(docker exec "$DB_CONTAINER" psql -Atq -U spmaster -d sp -c "select count(*) from pg_auth_members m join pg_roles r on r.oid=m.member where r.rolname='sp_app' and m.set_option and not m.inherit_option" 2>/dev/null)"
  [[ "${g:-0}" -gt 0 ]] && report "GRANT ... SET TRUE" "OK ($g roles)" || report "GRANT ... SET TRUE" "NG"
else
  report "RDS PostgreSQL 16" "SKIP (DB_CONTAINER 未設定)"
fi

# EventBridge の prefix の絞り込み → SQS
q="verify-$(date +%s)"
url="$(aws_ sqs create-queue --queue-name "$q" --query QueueUrl --output text)"
arn="$(aws_ sqs get-queue-attributes --queue-url "$url" --attribute-names QueueArn --query Attributes.QueueArn --output text)"
aws_ events put-rule --event-bus-name sp-events --name "$q" --event-pattern '{"detail-type":[{"prefix":"verify.match"}]}' >/dev/null
aws_ events put-targets --event-bus-name sp-events --rule "$q" --targets "Id=1,Arn=$arn" >/dev/null
aws_ events put-events --entries '[{"EventBusName":"sp-events","Source":"sp.verify","DetailType":"verify.matched","Detail":"{}"},{"EventBusName":"sp-events","Source":"sp.verify","DetailType":"verify.other","Detail":"{}"}]' >/dev/null
sleep 1
n="$(aws_ sqs receive-message --queue-url "$url" --max-number-of-messages 10 --wait-time-seconds 2 --query 'length(Messages)' --output text 2>/dev/null)"
[[ "$n" == "1" ]] && report "EventBridge prefix → SQS" "OK" || report "EventBridge prefix → SQS" "NG (received $n)"
aws_ events remove-targets --event-bus-name sp-events --rule "$q" --ids 1 >/dev/null; aws_ events delete-rule --event-bus-name sp-events --name "$q" >/dev/null; aws_ sqs delete-queue --queue-url "$url" >/dev/null

# EventBridge Scheduler（Asia/Tokyo）→ SQS：1 分後の 1 回限りのスケジュールが届くか
q="verify-sched-$(date +%s)"
url="$(aws_ sqs create-queue --queue-name "$q" --query QueueUrl --output text)"
arn="$(aws_ sqs get-queue-attributes --queue-url "$url" --attribute-names QueueArn --query Attributes.QueueArn --output text)"
at="$(TZ=Asia/Tokyo date -v+1M '+%Y-%m-%dT%H:%M:00' 2>/dev/null || TZ=Asia/Tokyo date -d '+1 min' '+%Y-%m-%dT%H:%M:00')"
aws_ scheduler create-schedule --name "$q" --schedule-expression "at($at)" --schedule-expression-timezone Asia/Tokyo --flexible-time-window Mode=OFF \
  --target "{\"Arn\":\"$arn\",\"RoleArn\":\"arn:aws:iam::000000000000:role/sp-scheduler-to-sqs\",\"Input\":\"{\\\"job\\\":\\\"verify\\\"}\"}" >/dev/null 2>&1
n=0
for _ in $(seq 1 45); do
  n="$(aws_ sqs receive-message --queue-url "$url" --wait-time-seconds 2 --query 'length(Messages)' --output text 2>/dev/null)"
  [[ "$n" == "1" ]] && break
done
[[ "$n" == "1" ]] && report "Scheduler (Asia/Tokyo) → SQS" "OK" || report "Scheduler (Asia/Tokyo) → SQS" "NG（タイムゾーンを無視して UTC で評価する）→ SCHEDULER_MODE=internal"
aws_ scheduler delete-schedule --name "$q" >/dev/null 2>&1; aws_ sqs delete-queue --queue-url "$url" >/dev/null

# SES の送信と、送ったメールを取り出す API
mid="$(aws_ ses send-email --from noreply@sp.local --destination ToAddresses=verify@example.com --message 'Subject={Data=verify},Body={Text={Data=verify}}' --query MessageId --output text 2>/dev/null)"
[[ -n "$mid" ]] && report "SES send" "OK" || report "SES send" "NG"
code="$(curl -s -o /dev/null -w '%{http_code}' "$E/_ministack/ses/messages" 2>/dev/null)"
[[ "$code" == "200" ]] && report "SES 取り出し API" "OK（GET /_ministack/ses/messages）" || report "SES 取り出し API" "なし（E2E は integration.email_notifications で確かめる）"

# CloudWatch Logs（Fluent Bit の出力先・Logs Insights）
g="$(aws_ logs describe-log-groups --log-group-name-prefix /sp/local/ --query 'length(logGroups)' --output text 2>/dev/null)"
[[ "${g:-0}" -ge 4 ]] && report "CloudWatch Logs groups" "OK ($g)" || report "CloudWatch Logs groups" "NG ($g)"
s="$(aws_ logs describe-log-streams --log-group-name /sp/local/api --query 'length(logStreams)' --output text 2>/dev/null)"
[[ "${s:-0}" -ge 1 ]] && report "Fluent Bit → CloudWatch Logs" "OK" || report "Fluent Bit → CloudWatch Logs" "NG（api を起動してリクエストを送ってから再実行）"
r="$(.local/bin/devlogs cwl 'fields @timestamp | filter correlation_id = "verify-no-such-id" | limit 5' --since 24h 2>/dev/null | grep -c '@timestamp=')"
[[ "${r:-0}" -eq 0 ]] && report "Logs Insights filter" "OK" || report "Logs Insights filter" "NG（filter が効かない）→ 調査は task logs（P-OBS-10）"

echo "結果: $out"
exit $ok
