#!/usr/bin/env bash
# 4 プロセスを devtools ビルドで並行に起動する（task dev）。Ctrl-C で全部止める。
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; source .env.local; set +a
mkdir -p .local/bin .local/logs
go build -tags devtools -o .local/bin/ ./cmd/api ./cmd/worker-billing ./cmd/worker-integration ./cmd/worker-projection
pids=()
SERVICE_NAME=api HTTP_ADDR=:8080 .local/bin/api & pids+=($!)
SERVICE_NAME=worker-billing HEALTH_ADDR=:8081 .local/bin/worker-billing & pids+=($!)
SERVICE_NAME=worker-integration HEALTH_ADDR=:8082 .local/bin/worker-integration & pids+=($!)
SERVICE_NAME=worker-projection HEALTH_ADDR=:8083 .local/bin/worker-projection & pids+=($!)
stop() { kill -TERM "${pids[@]}" 2>/dev/null || true; wait; exit 0; }
trap stop INT TERM
# どれか 1 つが落ちたら全部止める（macOS の bash 3.2 には wait -n がない）
while :; do
  for p in "${pids[@]}"; do
    if ! kill -0 "$p" 2>/dev/null; then echo "dev: プロセス $p が終了したので全部止めます" >&2; stop; fi
  done
  sleep 1
done
