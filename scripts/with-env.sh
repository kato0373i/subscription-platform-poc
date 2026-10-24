#!/usr/bin/env bash
# .env.local を読み込んでからコマンドを実行する（task up の途中で書き出した値を、同じ task の後続で使うため）。
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; [[ -f .env.local ]] && source .env.local; set +a
exec "$@"
