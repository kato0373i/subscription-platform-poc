package app

import (
	"context"

	"subscription-platform/internal/platform/inbox"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/modules/integration/internal/adapter/postgres/sqlcgen"
)

// テストだけに公開する（外部テストパッケージの結合テストが使う）。

// LeaseEndpointsForTest はリースを取るだけで送らない（タスクが落ちた状況を作る。AC-INT-037）。
func (s *Service) LeaseEndpointsForTest(ctx context.Context, free int) ([]sqlcgen.LeaseEndpointsRow, error) {
	return s.leaseEndpoints(ctx, free)
}

// EmailRowsOnlyHandlerForTest は行を作るだけで送らない受信処理（送る前に落ちた状況を作る。AC-INT-040）。
func (s *Service) EmailRowsOnlyHandlerForTest(consumer string) messaging.Handler {
	return inbox.Wrap(s.d.DB, consumer, s.handleEmail, inbox.AllowSystemScope("test"))
}
