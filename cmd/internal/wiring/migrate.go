package wiring

import (
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
)

// MigrationSources は platform → tenant → … → audit の順のマイグレーション（§6.4）。
// 各ドメインの TL がモジュールを実装したら、ここに db/migrations を足す。
func MigrationSources() []migrate.Source {
	return []migrate.Source{
		{Schema: "platform", FS: migrate.SubFS(db.PlatformMigrations, "migrations")},
	}
}
