// Package db は billing スキーマのマイグレーションと sqlc のクエリ。
package db

import "embed"

// Migrations は billing スキーマのマイグレーション。
//
//go:embed migrations/*.sql
var Migrations embed.FS
