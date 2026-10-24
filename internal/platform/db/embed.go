package db

import "embed"

// PlatformMigrations は platform スキーマのマイグレーション。
//
//go:embed migrations/*.sql
var PlatformMigrations embed.FS
