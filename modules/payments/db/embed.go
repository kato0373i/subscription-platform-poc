// Package db は payments スキーマのマイグレーションと sqlc のクエリ。
package db

import "embed"

// Migrations は payments スキーマのマイグレーション。
//
//go:embed migrations/*.sql
var Migrations embed.FS

// DevtoolsMigrations は StubAdapter の表（devtools ビルドの migrate だけが流す）。
//
//go:embed migrations_devtools/*.sql
var DevtoolsMigrations embed.FS
