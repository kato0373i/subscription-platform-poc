// Package db は orchestration スキーマのマイグレーションと sqlc のクエリ。
package db

import "embed"

// Migrations は orchestration スキーマのマイグレーション。
//
//go:embed migrations/*.sql
var Migrations embed.FS
