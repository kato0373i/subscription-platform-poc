// Package db は tenant スキーマのマイグレーションと sqlc のクエリ。
package db

import "embed"

// Migrations は tenant スキーマのマイグレーション。
//
//go:embed migrations/*.sql
var Migrations embed.FS
