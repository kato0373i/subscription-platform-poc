# deploy/local

ローカル・CI 専用のファイル。本番では使わない。

| ファイル | 用途 |
|---|---|
| `dev-jwt.pem` | 開発用 JWT（`AUTH_MODE=dev`）の ES256 の鍵。devtools ビルドの `POST /v1/dev/tokens` が署名し、api が検証する。**ローカル専用の公開してよい鍵**で、本番のビルドでは `AUTH_MODE=dev` 自体を拒否する |
| `fluent-bit.conf` | `.local/logs/*.jsonl` を MiniStack の CloudWatch Logs（`/sp/local/<service>`）へ送る（platform.md §8.6.4） |
| `tls/`・`ministack-tls.conf` | Fluent Bit の `cloudwatch_logs` 出力は TLS を強制するので、自己署名の証明書で TLS を終端して MiniStack に渡す nginx（`ministack-tls`）の設定。ローカル専用 |
