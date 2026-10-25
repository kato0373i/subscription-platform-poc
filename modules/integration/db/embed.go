// Package db は integration スキーマのマイグレーションと sqlc のクエリ。
package db

import "embed"

// Migrations は integration スキーマのマイグレーション。
//
//go:embed migrations/*.sql
var Migrations embed.FS
