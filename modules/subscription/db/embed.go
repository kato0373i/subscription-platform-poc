// Package db は subscription スキーマのマイグレーションと sqlc のクエリ。
package db

import "embed"

// Migrations は subscription スキーマのマイグレーション。
//
//go:embed migrations/*.sql
var Migrations embed.FS
