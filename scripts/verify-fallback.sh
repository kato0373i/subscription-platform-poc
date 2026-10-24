#!/usr/bin/env bash
# フォールバックが動くことを確かめる（AC-PLT-047。task verify:fallback）。
#   MESSAGING_MODE=direct：EventBridge を通さず、FanoutPublisher が messaging.yaml を読んで各キューに直接送る
#   DB_MODE=container は task reset のあと DB_MODE=container task up で確かめる（compose の profile db）
set -euo pipefail
cd "$(dirname "$0")/.."
pkill -TERM -f "scripts/dev.sh" 2>/dev/null || true
sleep 2
MESSAGING_MODE=direct ./scripts/local-env.sh >/dev/null
./scripts/dev.sh > .local/dev-fallback.out 2>&1 &
trap 'pkill -TERM -f "scripts/dev.sh" || true; ./scripts/local-env.sh >/dev/null' EXIT
./scripts/wait-ready.sh
./scripts/verify-local.sh
echo "verify:fallback: MESSAGING_MODE=direct で OK"
