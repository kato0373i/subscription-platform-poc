package wiring

import (
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
	billingdb "subscription-platform/modules/billing/db"
	catalogdb "subscription-platform/modules/catalog/db"
	customerdb "subscription-platform/modules/customer/db"
	paymentsdb "subscription-platform/modules/payments/db"
	subscriptiondb "subscription-platform/modules/subscription/db"
	tenantdb "subscription-platform/modules/tenant/db"
)

// MigrationSources は platform → tenant → … → audit の順のマイグレーション（§6.4）。
// 各ドメインの TL がモジュールを実装したら、ここに db/migrations を足す。
func MigrationSources() []migrate.Source {
	return append([]migrate.Source{
		{Schema: "platform", FS: migrate.SubFS(db.PlatformMigrations, "migrations")},
		{Schema: "tenant", FS: migrate.SubFS(tenantdb.Migrations, "migrations")},
		{Schema: "customer", FS: migrate.SubFS(customerdb.Migrations, "migrations")},
		{Schema: "catalog", FS: migrate.SubFS(catalogdb.Migrations, "migrations")},
		{Schema: "subscription", FS: migrate.SubFS(subscriptiondb.Migrations, "migrations")},
		{Schema: "billing", FS: migrate.SubFS(billingdb.Migrations, "migrations")},
		{Schema: "payments", FS: migrate.SubFS(paymentsdb.Migrations, "migrations")},
	}, devMigrationSources()...)
}

// paymentsDevtoolsSource は StubAdapter の表（devtools ビルドの migrate だけが流す。バージョン表を分ける）。
func paymentsDevtoolsSource() migrate.Source {
	return migrate.Source{Schema: "payments", Table: "payments.goose_devtools_version", FS: migrate.SubFS(paymentsdb.DevtoolsMigrations, "migrations_devtools")}
}
