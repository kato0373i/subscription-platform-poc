-- name: GetAccount :one
SELECT * FROM customer.customer_accounts WHERE id = $1;

-- name: GetAccountForUpdate :one
SELECT * FROM customer.customer_accounts WHERE id = $1 FOR UPDATE;

-- name: GetAccountForShare :one
SELECT * FROM customer.customer_accounts WHERE id = $1 FOR SHARE;

-- name: GetAccountBySubject :one
SELECT * FROM customer.customer_accounts WHERE subject = $1;

-- 事業者コンソールのメールアドレス検索（business-console FR-010。system スコープで呼び、リンクで事業者の範囲に絞る）
-- name: GetActiveAccountIDByEmail :one
SELECT id FROM customer.customer_accounts WHERE email = $1 AND status = 'active';

-- name: ExistsActiveEmail :one
SELECT EXISTS (SELECT 1 FROM customer.customer_accounts WHERE email = $1 AND status = 'active' AND subject <> $2);

-- name: InsertAccountIfAbsent :execrows
INSERT INTO customer.customer_accounts (id, subject, email, name, status, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'active', 1, $5, $5)
ON CONFLICT (subject) DO NOTHING;

-- name: UpdateAccount :execrows
UPDATE customer.customer_accounts
SET email = $2, name = $3, status = $4, withdrawn_at = $5, version = version + 1, updated_at = $6
WHERE id = $1 AND version = $7;

-- name: GetLink :one
SELECT * FROM customer.tenant_customer_links WHERE tenant_id = $1 AND customer_account_id = $2;

-- name: InsertLinkIfAbsent :execrows
INSERT INTO customer.tenant_customer_links (id, tenant_id, customer_account_id, created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING; -- (tenant_id, customer_account_id) の重複も、まれな id の衝突も、読み直して判定する（E9・E10）
