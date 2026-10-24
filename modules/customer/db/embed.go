// Package db は customer スキーマのマイグレーションと sqlc のクエリ。
package db

import "embed"

// Migrations は customer スキーマのマイグレーション。
//
//go:embed migrations/*.sql
var Migrations embed.FS
