#!/usr/bin/env bash
# 4 プロセスの /readyz が 200 になるまで待つ。
set -euo pipefail
for url in http://localhost:8080/readyz http://localhost:8081/readyz http://localhost:8082/readyz http://localhost:8083/readyz; do
  for i in $(seq 1 120); do
    if curl -fsS -o /dev/null "$url"; then break; fi
    if [[ $i == 120 ]]; then echo "wait-ready: $url が 200 になりません" >&2; exit 1; fi
    sleep 0.5
  done
done
echo "wait-ready: 4 プロセスとも ready"
