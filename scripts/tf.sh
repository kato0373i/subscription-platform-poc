#!/usr/bin/env bash
# terraform をローカルのバイナリで、なければ公式イメージのコンテナで実行する（compose のネットワークから MiniStack に届く）。
set -euo pipefail
cd "$(dirname "$0")/.."
TF_IMAGE=hashicorp/terraform:1.16.5
if command -v terraform >/dev/null 2>&1; then
  exec terraform -chdir=infra/envs/local "$@"
fi
args=("$@")
if [[ "${1:-}" == "apply" || "${1:-}" == "plan" || "${1:-}" == "destroy" ]]; then
  args+=("-var" "aws_endpoint=http://ministack:4566")
fi
exec docker run --rm -i --network sp_default \
  -v "$PWD:/w" -w /w/infra/envs/local \
  -v sp-terraform-plugins:/root/.terraform.d/plugin-cache \
  -e TF_PLUGIN_CACHE_DIR=/root/.terraform.d/plugin-cache \
  -e TF_IN_AUTOMATION=1 \
  "$TF_IMAGE" "${args[@]}"
