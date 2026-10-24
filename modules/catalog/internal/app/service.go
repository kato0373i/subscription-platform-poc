// Package app は catalog モジュールのユースケース（console・申込ページ用の公開 API・contracts.Service・掃除ジョブ）。
package app

import (
	"context"
	"errors"
	"time"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/outbox"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/catalog/internal/adapter/postgres"
	"subscription-platform/modules/catalog/internal/domain/pricing"
	tenantcontracts "subscription-platform/modules/tenant/contracts"
)

// Deps はユースケースの依存。
type Deps struct {
	DB             *db.ModuleDB
	Outbox         *outbox.Writer
	Clock          sk.Clock
	Schemes        pricing.Registry
	Tenants        tenantcontracts.Service // 事業者の状態（P-CAT-1）
	ReservationTTL time.Duration           // 既定 24h（§8）
}

// Service はユースケースの入口。
type Service struct{ d Deps }

func New(d Deps) *Service {
	if d.ReservationTTL <= 0 {
		d.ReservationTTL = 24 * time.Hour
	}
	if d.Schemes == nil {
		d.Schemes = pricing.Default()
	}
	return &Service{d: d}
}

var _ contracts.Service = (*Service)(nil)

// ErrNotFound は console・公開 API で指定したリソースがない（他の事業者のものも RLS で見えないので同じ）。
var ErrNotFound = errors.New("catalog: not found")

func mapNotFound(err, to error) error {
	if errors.Is(err, postgres.ErrNotFound) {
		return to
	}
	return err
}

// tenantActive は事業者が active か。事業者がなければ false。
func (s *Service) tenantActive(ctx context.Context, t sk.TenantID) (bool, error) {
	info, err := s.d.Tenants.GetTenant(ctx, t)
	if errors.Is(err, tenantcontracts.ErrTenantNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Status == tenantcontracts.TenantActive, nil
}

// page は limit+1 件読んで has_more を決める共通の処理。
func page[T any](items []T, limit int) ([]T, bool) {
	if len(items) > limit {
		return items[:limit], true
	}
	return items, false
}
