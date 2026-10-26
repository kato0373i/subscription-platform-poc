package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/integration/internal/adapter/postgres"
	"subscription-platform/modules/integration/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/integration/internal/adapter/webhook"
	"subscription-platform/modules/integration/internal/domain"
)

// 配信ループの値（§8.1）。
const (
	dispatchPoll  = time.Second
	dispatchLease = 30 * time.Second
	perLease      = 20 // 1 回のリースで送る件数（ほかの送信先を待たせない）
)

func dlog() *slog.Logger { return observability.Logger("integration.webhook") }

// RunDispatch は配信ループ（worker-integration に常駐）。空いている枠の数だけ送信先のリースを取り、送信先ごとに 1 つの goroutine で順に送る。
func (s *Service) RunDispatch(ctx context.Context) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, s.d.Parallelism)
	t := time.NewTicker(dispatchPoll)
	defer t.Stop()
	for {
		if free := cap(slots) - len(slots); free > 0 {
			leased, err := s.leaseEndpoints(ctx, free)
			if err != nil && !errors.Is(err, context.Canceled) {
				dlog().ErrorContext(ctx, "webhook.lease_failed", observability.Err(err))
			}
			for _, l := range leased {
				slots <- struct{}{}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-slots }()
					s.ProcessEndpoint(context.WithoutCancel(ctx), l.ID, sk.IDFromUUID[sk.TenantTag](l.TenantID))
				}()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (s *Service) leaseEndpoints(ctx context.Context, free int) ([]sqlcgen.LeaseEndpointsRow, error) {
	now := s.d.Clock.Now()
	var rows []sqlcgen.LeaseEndpointsRow
	err := s.d.DB.InTx(db.WithSystemScope(ctx, "webhook dispatch: lease"), func(ctx context.Context, tx db.Tx) error {
		var err error
		rows, err = sqlcgen.New(tx).LeaseEndpoints(ctx, sqlcgen.LeaseEndpointsParams{Until: now.Add(dispatchLease), Owner: s.d.TaskID, Now: now, Slots: int32(free)})
		return err
	})
	return rows, err
}

// DispatchOnce はリースを 1 回取り、取れた送信先を処理し終えるまで待つ（結合テスト用）。
func (s *Service) DispatchOnce(ctx context.Context) (int, error) {
	leased, err := s.leaseEndpoints(ctx, s.d.Parallelism)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, l := range leased {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.ProcessEndpoint(ctx, l.ID, sk.IDFromUUID[sk.TenantTag](l.TenantID))
		}()
	}
	wg.Wait()
	return len(leased), nil
}

// ProcessEndpoint はリースを持つ送信先の配信を順に送る（§8.1 の手順 1〜7）。
func (s *Service) ProcessEndpoint(ctx context.Context, endpointID uuid.UUID, t sk.TenantID) {
	ctx = db.WithTenantScope(db.ClearScope(ctx), t)
	defer s.releaseLease(ctx, endpointID)
	for range perLease {
		cont, err := s.deliverNext(ctx, endpointID)
		if err != nil {
			dlog().ErrorContext(ctx, "webhook.deliver_failed", slog.String("endpoint_id", endpointID.String()), observability.Err(err))
			return
		}
		if !cont {
			return
		}
	}
}

func (s *Service) releaseLease(ctx context.Context, endpointID uuid.UUID) {
	err := s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		return sqlcgen.New(tx).ReleaseLease(ctx, sqlcgen.ReleaseLeaseParams{ID: endpointID, Owner: s.d.TaskID, ReleasedAt: s.d.Clock.Now()})
	})
	if err != nil {
		dlog().ErrorContext(ctx, "webhook.release_failed", slog.String("endpoint_id", endpointID.String()), observability.Err(err))
	}
}

// deliverNext は時刻を迎えた配信を 1 件送る。続けてよければ true。
func (s *Service) deliverNext(ctx context.Context, endpointID uuid.UUID) (bool, error) {
	now := s.d.Clock.Now()
	var (
		ep  *domain.Endpoint
		d   *domain.Delivery
		ev  sqlcgen.IntegrationWebhookEvent
		end bool
	)
	// 1〜3：送信直前に送信先を読み直し（E3）、無効・削除なら abandoned、ブレーカーが開いていれば見送る
	err := s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		r := s.repo(tx)
		var err error
		if ep, err = r.GetEndpoint(ctx, sk.IDFromUUID[domain.EndpointTag](endpointID), false); err != nil {
			return err
		}
		switch {
		case !ep.Deliverable():
			end = true
			return s.closeAll(ctx, r, endpointID, func(d *domain.Delivery) error { return d.Abandon(now) }, false)
		case !ep.BreakerAllows(now):
			end = true
			until := *ep.BreakerOpenUntil
			return s.closeAll(ctx, r, endpointID, func(d *domain.Delivery) error { return d.Postpone(until, now) }, true)
		}
		row, err := r.Q.NextDueDelivery(ctx, sqlcgen.NextDueDeliveryParams{EndpointID: endpointID, NextAttemptAt: &now})
		if err != nil {
			if errors.Is(postgres.NotFound(err), postgres.ErrNotFound) {
				end = true
				return nil
			}
			return err
		}
		d = postgres.DeliveryOf(row)
		ev, err = r.Q.GetWebhookEvent(ctx, d.EventID)
		return err
	})
	if err != nil || end {
		return false, err
	}

	// 4：署名して送る（トランザクションの外。試行ごとに署名し直す）。ログは元のイベントの相関 ID で追えるようにする（AC-PLT-073）
	if ev.CorrelationID != nil {
		cause := ev.SourceEventID.String()
		ctx = observability.WithCorrelation(ctx, *ev.CorrelationID, &cause)
	}
	ctx = observability.WithFields(ctx, slog.String("delivery_id", d.ID.String()), slog.String("event_id", ev.ID))
	sentAt := s.d.Clock.Now()
	res, err := s.d.Sender.Send(ctx, webhook.Request{
		URL: ep.URL, Body: []byte(ev.Payload), Secrets: ep.ActiveSecrets(sentAt), EventID: ev.ID, EventType: ev.EventType, DeliveryID: d.ID.String(),
		AttemptNo: d.AttemptCount + 1, SignedAt: sentAt, RequestStart: sentAt,
	})
	if err != nil {
		return false, err
	}

	// 5：試行の追記・配信の状態・ブレーカーを 1 つのトランザクションで更新し、リースを延長する
	done := s.d.Clock.Now()
	loaded := d.Version
	keep := true
	var failedEmail *uuid.UUID
	err = s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		r := s.repo(tx)
		if err := d.Record(res, done, s.d.Schedule); err != nil {
			return err
		}
		if err := r.InsertAttempt(ctx, d, res); err != nil {
			return err
		}
		if err := r.SaveDelivery(ctx, d, loaded); err != nil {
			return err
		}
		ep.RecordDeliveryResult(res.OK(), done, s.d.Breaker)
		if err := r.SaveBreaker(ctx, ep); err != nil {
			return err
		}
		n, err := r.Q.ExtendLease(ctx, sqlcgen.ExtendLeaseParams{ID: endpointID, DispatchLeaseOwner: &s.d.TaskID, DispatchLeaseUntil: ptrTime(done.Add(dispatchLease))})
		if err != nil {
			return err
		}
		keep = n == 1 // E5b：別のタスクがリースを取ったら、記録だけしてやめる
		if d.Status == domain.DeliveryFailed {
			// 6：オーナー宛ての通知（1 送信先 1 日 1 通）
			id, inserted, err := s.insertEmail(ctx, r.Q, domain.WebhookFailedPlan(ep.TenantID, ep.ID, done), nil, done)
			if err != nil {
				return err
			}
			if inserted {
				failedEmail = &id
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	dlog().InfoContext(ctx, "webhook.attempted", slog.String("delivery_id", d.ID.String()), slog.String("event_id", ev.ID), slog.Int("attempt", d.AttemptCount),
		slog.String("status", string(d.Status)), slog.String("error_kind", string(res.ErrorKind)))
	if failedEmail != nil {
		if err := s.SendEmail(db.ClearScope(ctx), *failedEmail); err != nil {
			s.logEmailError(ctx, *failedEmail, err)
		}
	}
	return keep, nil
}

// closeAll は pending の配信（dueOnly なら時刻を迎えたものだけ）に f を適用する。
func (s *Service) closeAll(ctx context.Context, r *postgres.Repo, endpointID uuid.UUID, f func(*domain.Delivery) error, dueOnly bool) error {
	var rows []sqlcgen.IntegrationWebhookDelivery
	var err error
	if dueOnly {
		now := s.d.Clock.Now()
		rows, err = r.Q.DueDeliveries(ctx, sqlcgen.DueDeliveriesParams{EndpointID: endpointID, NextAttemptAt: &now})
	} else {
		rows, err = r.Q.PendingDeliveries(ctx, endpointID)
	}
	if err != nil {
		return err
	}
	for _, row := range rows {
		d := postgres.DeliveryOf(row)
		loaded := d.Version
		if err := f(d); err != nil {
			return err
		}
		if err := r.SaveDelivery(ctx, d, loaded); err != nil {
			return err
		}
	}
	return nil
}
