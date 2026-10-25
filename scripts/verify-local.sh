#!/usr/bin/env bash
# テストイベントを 1 件発行し、Outbox → EventBridge → SQS → 購読者まで届くことを確かめる（task verify:local）。
set -euo pipefail
API="${API_BASE_URL:-http://localhost:8080}"
CORR="verify-local-$(date +%s)"
id="$(curl -fsS -X POST -H "X-Correlation-Id: $CORR" "$API/v1/dev/smoke" | sed -E 's/.*"event_id":"([^"]+)".*/\1/')"
for i in $(seq 1 60); do
  st="$(curl -fsS -H "X-Correlation-Id: $CORR" "$API/v1/dev/smoke/$id")"
  if [[ "$st" == *'"consumed":true'* ]]; then
    echo "verify:local: OK（event_id=${id}、相関 ID=${CORR}）"
    exit 0
  fi
  sleep 0.5
done
echo "verify:local: 30 秒以内に購読者まで届きませんでした（${st}）。task logs -- --corr ${CORR} で調べてください" >&2
exit 1
