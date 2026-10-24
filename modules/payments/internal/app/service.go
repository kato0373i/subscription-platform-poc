// Package app は payments モジュールのユースケース（contracts.Service・API・Webhook と通知の処理・照合のジョブ）。
// 決済代行は provider のポートとレジストリだけを通して使い、具体的なアダプターを import しない（AC-PAY-031）。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/outbox"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/postgres"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/domain"
)

// Deps はユースケースの依存。
type Deps struct {
	DB       *db.ModuleDB
	Outbox   *outbox.Writer
	Clock    sk.Clock
	Registry *provider.Registry
	// SendProviderEvent は通知の処理を payments-provider-events キューに送る（Webhook の保存のコミット後。§7.9）。
	SendProviderEvent func(ctx context.Context, body string) error
	// ReconcilePageSize は日次照合の 1 ページの件数（既定 100）。
	ReconcilePageSize int
}

// Service はユースケースの入口。
type Service struct{ d Deps }

func New(d Deps) *Service {
	if d.ReconcilePageSize <= 0 {
		d.ReconcilePageSize = 100
	}
	return &Service{d: d}
}

var _ c.Service = (*Service)(nil)

func mapNotFound(err, to error) error {
	if errors.Is(err, postgres.ErrNotFound) {
		return to
	}
	return err
}

// system は照合ジョブ・Webhook 処理・ops の API が使う BYPASSRLS のスコープ（§6）。
func system(ctx context.Context, reason string) context.Context {
	return db.WithSystemScope(ctx, reason)
}

// appendEvents は試行のイベントを Outbox に書く（試行の更新と同じトランザクション。§5.3）。
func (s *Service) appendEvents(ctx context.Context, tx db.Tx, a *domain.Attempt) error {
	st := a.State()
	for _, e := range a.Events() {
		p := c.PaymentEventPayload{
			AttemptID: st.ID, Payable: c.PayableJSON{Type: st.Payable.Type, ID: st.Payable.ID}, Amount: st.Amount.Int64(), PaymentMethodID: st.PaymentMethodID,
		}
		if e.Type != c.EventPaymentOutcomeUnknown {
			cus := st.CustomerAccountID
			p.CustomerAccountID = &cus
			if st.Resolution != nil {
				r := string(*st.Resolution)
				p.Resolution = &r
			}
			p.ResolvedBy = st.ResolvedBy
		}
		if e.Type == c.EventPaymentFailed && st.FailureCode != nil {
			code := string(*st.FailureCode)
			p.FailureCode, p.Retryable = &code, st.Retryable
		}
		env, err := sk.NewEnvelope(e.Type, "payment_attempt", st.ID.UUID(), int64(st.Version+1), &st.TenantID, s.d.Clock.Now(), p)
		if err != nil {
			return err
		}
		if err := s.d.Outbox.Append(ctx, tx, env); err != nil {
			return fmt.Errorf("payments: outbox: %w", err)
		}
	}
	return nil
}

// save は試行を保存してイベントを書く。
func (s *Service) save(ctx context.Context, tx db.Tx, repo *postgres.Repo, a *domain.Attempt, from c.AttemptStatus) error {
	if err := s.appendEvents(ctx, tx, a); err != nil {
		return err
	}
	return repo.UpdateAttempt(ctx, a, from, s.d.Clock.Now())
}

// applyOutcome は代行の結果を試行に当てる。同じ状態への再適用は changed=false。違う終端は conflict=true（不整合として記録する）。
func applyOutcome(a *domain.Attempt, out provider.Outcome, via domain.Resolution, now sk.Clock) (changed, conflict bool, err error) {
	t := now.Now()
	switch out.Status {
	case provider.OutcomeSucceeded:
		err = a.Succeed(t, via)
	case provider.OutcomeFailed:
		code := out.FailureCode
		if !code.Known() {
			code = domain.FailProcessingError
		}
		err = a.Fail(code, out.Retryable, out.RawCode, t, via)
	case provider.OutcomePendingAsync:
		err = a.AcceptAsync()
	case provider.OutcomeUnknown, provider.OutcomeNotFound:
		return false, false, nil
	}
	switch {
	case errors.Is(err, domain.ErrSameOutcome):
		return false, false, nil
	case errors.Is(err, c.ErrAttemptFinalized):
		return false, true, nil
	case err != nil:
		return false, false, err
	}
	return true, false, nil
}

// providerFor は試行の代行のアダプター。
func (s *Service) providerFor(st domain.AttemptState) (provider.PaymentProvider, error) {
	return s.d.Registry.Get(provider.Key{Method: st.MethodType, Provider: provider.ProviderID(st.Provider)})
}

// kickBody は通知の処理の依頼（payments-provider-events キューの本文。EventEnvelope の形で送る）。
func kickBody(providerName, eventID string, now sk.Clock) (string, error) {
	env, err := sk.NewEnvelope("payments.provider_event_received", "provider_event", uuid.NewSHA1(uuid.NameSpaceOID, []byte(providerName+":"+eventID)), 1, nil, now.Now(),
		map[string]string{"provider": providerName, "provider_event_id": eventID})
	if err != nil {
		return "", err
	}
	env.CorrelationID = "provider-event:" + providerName
	b, err := json.Marshal(env)
	return string(b), err
}
