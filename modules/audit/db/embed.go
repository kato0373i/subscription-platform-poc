// Package db は audit スキーマのマイグレーションと sqlc のクエリ。
package db

import "embed"

// Migrations は audit スキーマのマイグレーション。
//
//go:embed migrations/*.sql
var Migrations embed.FS
