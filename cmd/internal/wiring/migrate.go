package wiring

import (
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
	customerdb "subscription-platform/modules/customer/db"
	tenantdb "subscription-platform/modules/tenant/db"
)

// MigrationSources は platform → tenant → … → audit の順のマイグレーション（§6.4）。
// 各ドメインの TL がモジュールを実装したら、ここに db/migrations を足す。
func MigrationSources() []migrate.Source {
	return []migrate.Source{
		{Schema: "platform", FS: migrate.SubFS(db.PlatformMigrations, "migrations")},
		{Schema: "tenant", FS: migrate.SubFS(tenantdb.Migrations, "migrations")},
		{Schema: "customer", FS: migrate.SubFS(customerdb.Migrations, "migrations")},
	}
}
