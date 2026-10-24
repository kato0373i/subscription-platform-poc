// Package db は catalog スキーマのマイグレーションと sqlc のクエリ。
package db

import "embed"

// Migrations は catalog スキーマのマイグレーション。
//
//go:embed migrations/*.sql
var Migrations embed.FS
