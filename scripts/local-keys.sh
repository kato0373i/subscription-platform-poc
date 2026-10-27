#!/usr/bin/env bash
# ローカル・CI 専用の鍵を、なければ作る（Git には入れない。deploy/local/README.md）。
#   deploy/local/dev-jwt.pem   開発用 JWT（AUTH_MODE=dev）の ES256 の鍵（PKCS#8）
#   deploy/local/tls/*.pem     Fluent Bit → MiniStack の TLS を終端する ministack-tls の自己署名の証明書
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ ! -f deploy/local/dev-jwt.pem ]]; then
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out deploy/local/dev-jwt.pem 2>/dev/null
  chmod 644 deploy/local/dev-jwt.pem # コンテナ（nonroot）から読む
  echo "local-keys: deploy/local/dev-jwt.pem を作りました"
fi
if [[ ! -f deploy/local/tls/key.pem || ! -f deploy/local/tls/cert.pem ]]; then
  mkdir -p deploy/local/tls
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj "/CN=ministack-tls" -addext "subjectAltName=DNS:ministack-tls" \
    -keyout deploy/local/tls/key.pem -out deploy/local/tls/cert.pem 2>/dev/null
  chmod 644 deploy/local/tls/key.pem deploy/local/tls/cert.pem
  echo "local-keys: deploy/local/tls/ の証明書を作りました"
fi
