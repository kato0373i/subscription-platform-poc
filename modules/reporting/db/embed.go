// Package db は reporting スキーマのマイグレーションと sqlc のクエリ。
package db

import "embed"

// Migrations は reporting スキーマのマイグレーション。
//
//go:embed migrations/*.sql
var Migrations embed.FS
