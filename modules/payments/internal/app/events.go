package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/postgres"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/domain"
)

// 決済代行からの通知：Webhook は Inbox（provider_events）に保存してから SQS に送り、worker-billing の受信ループで処理する（§5.2・§7.9・§8.2）。

// ErrUnknownProvider は Webhook の provider が登録されていない（404）。
var ErrUnknownProvider = errors.New("payments: unknown webhook provider")

// IngestWebhook は署名を検証して通知を保存し、新しく保存できたものだけ処理の依頼を送る。試行はここでは更新しない。
func (s *Service) IngestWebhook(ctx context.Context, providerName string, h http.Header, body []byte) error {
	p, ok := s.d.Registry.ByProvider(providerName)
	if !ok {
		return ErrUnknownProvider
	}
	parser, ok := provider.Capability[provider.WebhookParser](p)
	if !ok {
		return ErrUnknownProvider
	}
	events, err := parser.ParseWebhook(ctx, h, body)
	if err != nil {
		return err
	}
	var fresh []string
	err = s.d.DB.InTx(system(ctx, "webhook_ingest"), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		for _, e := range events {
			payload, _ := json.Marshal(map[string]any{"order_ref": e.ProviderOrderRef, "status": e.Outcome.Status, "failure_code": e.Outcome.FailureCode,
				"retryable": e.Outcome.Retryable, "raw_code": e.Outcome.RawCode, "occurred_at": e.OccurredAt})
			created, err := repo.InsertProviderEvent(ctx, providerName, e.ProviderEventID, e.ProviderOrderRef, payload, s.d.Clock.Now())
			if err != nil {
				return err
			}
			if created {
				fresh = append(fresh, e.ProviderEventID)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, id := range fresh {
		s.kick(ctx, providerName, id) // 送れなくても Inbox に残り、掃除が送り直す
	}
	return nil
}

func (s *Service) kick(ctx context.Context, providerName, eventID string) {
	if s.d.SendProviderEvent == nil {
		return
	}
	body, err := kickBody(providerName, eventID, s.d.Clock)
	if err == nil {
		err = s.d.SendProviderEvent(ctx, body)
	}
	if err != nil {
		observability.Logger("payments.webhook").LogAttrs(ctx, slog.LevelWarn, "payments.provider_event_kick_failed",
			slog.String("provider", providerName), slog.String("provider_event_id", eventID), observability.Err(err))
	}
}

// storedOutcome は provider_events.payload の結果。
type storedOutcome struct {
	OrderRef    string                 `json:"order_ref"`
	Status      provider.OutcomeStatus `json:"status"`
	FailureCode domain.FailureCode     `json:"failure_code"`
	Retryable   bool                   `json:"retryable"`
	RawCode     string                 `json:"raw_code"`
}

// ProviderEventKey は処理の依頼の本文（EventEnvelope の payload）。
type ProviderEventKey struct {
	Provider        string `json:"provider"`
	ProviderEventID string `json:"provider_event_id"`
}

// ProcessProviderEvent は通知 1 件を処理する（§8.2 の受信ループの手順 1〜5）。
// 業務エラー（ロック待ちのタイムアウト・楽観ロックの衝突）は再試行の時刻を延ばして nil を返す（メッセージは消す。再試行は掃除に任せる）。
// DB に書けないエラーは error を返す（SQS が再配信する）。
func (s *Service) ProcessProviderEvent(ctx context.Context, key ProviderEventKey) error {
	ctx = system(ctx, "provider_event")
	err := s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		ev, err := repo.LockPendingEvent(ctx, key.Provider, key.ProviderEventID)
		if errors.Is(err, postgres.ErrNotFound) {
			return nil // ロック中・処理済み（重複配信の吸収）
		}
		if err != nil {
			return err
		}
		now := s.d.Clock.Now()
		var so storedOutcome
		if err := json.Unmarshal(ev.Payload, &so); err != nil || so.OrderRef == "" {
			msg := "payload has no order_ref"
			return repo.FinishEvent(ctx, key.Provider, key.ProviderEventID, "ignored", &msg, now)
		}
		a, err := repo.GetAttemptByOrderRef(ctx, so.OrderRef, true)
		if errors.Is(err, postgres.ErrNotFound) {
			msg := "no attempt for order_ref"
			return repo.FinishEvent(ctx, key.Provider, key.ProviderEventID, "ignored", &msg, now) // 照合の orphan の候補
		}
		if err != nil {
			return err
		}
		from := a.State().Status
		_, conflict, err := applyOutcome(a, provider.Outcome{Status: so.Status, FailureCode: so.FailureCode, Retryable: so.Retryable, RawCode: so.RawCode}, domain.ViaWebhook, s.d.Clock)
		if err != nil {
			return err
		}
		if conflict {
			st := a.State()
			if _, err := repo.InsertDiscrepancy(ctx, postgres.Discrepancy{
				BusinessDate: s.d.Clock.Today(), Provider: key.Provider, Kind: "conflicting_final_outcome", AttemptID: &st.ID, TenantID: &st.TenantID,
				ProviderOrderRef: so.OrderRef, Detail: map[string]any{"local": st.Status, "provider": so.Status, "provider_event_id": key.ProviderEventID},
			}, now); err != nil {
				return err
			}
			observability.Logger("payments.webhook").LogAttrs(ctx, slog.LevelError, "payments.reconciliation_discrepancy",
				slog.String("kind", "conflicting_final_outcome"), slog.String("attempt_id", st.ID.String()))
		}
		if a.State().Status != from {
			if err := s.save(ctx, tx, repo, a, from); err != nil {
				return err
			}
		}
		return repo.FinishEvent(ctx, key.Provider, key.ProviderEventID, "processed", nil, now)
	}, db.WithLockTimeout(2*time.Second))
	if err == nil {
		return nil
	}
	if isBusinessRetry(err) {
		backoffErr := s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
			return s.retryLater(ctx, postgres.New(tx), key, err)
		})
		if backoffErr == nil {
			return nil
		}
		return backoffErr
	}
	return err
}

func (s *Service) retryLater(ctx context.Context, repo *postgres.Repo, key ProviderEventKey, cause error) error {
	var attempts int
	_ = s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT attempts FROM payments.provider_events WHERE provider = $1 AND provider_event_id = $2`, key.Provider, key.ProviderEventID).Scan(&attempts)
	})
	delay := time.Duration(1<<min(attempts, 10)) * time.Second
	if attempts+1 >= 10 {
		observability.Logger("payments.webhook").LogAttrs(ctx, slog.LevelError, "payments.provider_event_failed",
			slog.String("provider", key.Provider), slog.String("provider_event_id", key.ProviderEventID), observability.Err(cause))
	}
	return repo.RetryEvent(ctx, key.Provider, key.ProviderEventID, s.d.Clock.Now().Add(delay), cause.Error())
}

// isBusinessRetry はロック待ちのタイムアウト・楽観ロックの衝突など、時間をおけば通るエラーか。
func isBusinessRetry(err error) bool {
	if errors.Is(err, postgres.ErrVersionConflict) {
		return true
	}
	var pe interface{ SQLState() string }
	if errors.As(err, &pe) {
		switch pe.SQLState() {
		case "55P03", "40001", "40P01": // lock_not_available・serialization_failure・deadlock
			return true
		}
	}
	return false
}

// ProviderEventsSweepJobName は送り損ねた通知・再試行待ちの通知を送り直すジョブ（1 分ごと）。
const ProviderEventsSweepJobName = "payments-provider-events-sweep"

type sweepJob struct{ s *Service }

func (s *Service) ProviderEventsSweepJob() scheduler.Job { return sweepJob{s: s} }
func (sweepJob) Name() string                            { return ProviderEventsSweepJobName }

func (j sweepJob) Run(ctx context.Context, _ scheduler.Run) (scheduler.Stats, error) {
	now := j.s.d.Clock.Now()
	var keys []postgres.EventKey
	if err := j.s.d.DB.ReadTx(system(ctx, "provider_events_sweep"), func(ctx context.Context, tx db.Tx) error {
		var err error
		keys, err = postgres.New(tx).DueEvents(ctx, now, now.Add(-time.Minute))
		return err
	}); err != nil {
		return nil, err
	}
	for _, k := range keys {
		j.s.kick(ctx, k.Provider, k.EventID)
	}
	return scheduler.Stats{"resent": int64(len(keys))}, nil
}

// ---- 申込ページの設定 ----

// PaymentMethodOption は申込ページがトークナイザーを選ぶための情報（§7.1）。
type PaymentMethodOption struct {
	MethodType string
	Provider   string
	PublicKey  *string
}

// PaymentConfig は有効な手段の一覧。
func (s *Service) PaymentConfig(publicKeys map[string]string) []PaymentMethodOption {
	var out []PaymentMethodOption
	for _, k := range s.d.Registry.Enabled() {
		opt := PaymentMethodOption{MethodType: string(k.Method), Provider: string(k.Provider)}
		if pk, ok := publicKeys[string(k.Provider)]; ok && pk != "" {
			opt.PublicKey = &pk
		}
		out = append(out, opt)
	}
	return out
}

// InflightOrderRef は請求の進行中の試行の provider_order_ref（dev の settle 用）。なければ ErrAttemptNotFound。
func (s *Service) InflightOrderRef(ctx context.Context, invoiceID string) (string, string, error) {
	var ref, prov string
	err := s.d.DB.ReadTx(system(ctx, "dev_settle"), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		id, st, err := repo.BlockingAttempt(ctx, c.PayableRef{Type: c.PayableInvoice, ID: invoiceID})
		if err != nil {
			return err
		}
		if id == nil || st == c.AttemptSucceeded {
			return c.ErrAttemptNotFound
		}
		a, err := repo.GetAttempt(ctx, *id, postgres.NoLock)
		if err != nil {
			return err
		}
		if a.State().ProviderOrderRef == nil {
			return c.ErrAttemptNotFound
		}
		ref, prov = *a.State().ProviderOrderRef, a.State().Provider
		return nil
	})
	return ref, prov, err
}

// MethodRef は支払方法の代行側の参照（dev の挙動上書き用）。
func (s *Service) MethodRef(ctx context.Context, id sk.PaymentMethodID, providerName string) (string, error) {
	var ref string
	err := s.d.DB.ReadTx(system(ctx, "dev_behavior"), func(ctx context.Context, tx db.Tx) error {
		m, err := postgres.New(tx).GetMethod(ctx, id)
		if err != nil {
			return mapNotFound(err, c.ErrPaymentMethodNotFound)
		}
		for _, r := range m.State().Refs {
			if r.Provider == providerName {
				ref = r.MethodRef
				return nil
			}
		}
		return c.ErrPaymentMethodNotFound
	})
	return ref, err
}
