package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/postgres"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/domain"
)

// Collect は 1 回の決済（§3・§9）。
//
//	Tx1：冪等キー・支払方法・請求の進行中/成功済みを確かめ、送信前の試行（processing）を保存してコミットする
//	代行に送る（代行への冪等キーは attempt_id）
//	Tx2：結果を保存し、イベントを Outbox に書く。Tx2 に失敗したら unknown を返し、照合ジョブが確定する（E-06）
func (s *Service) Collect(ctx context.Context, cmd c.CollectCmd) (c.AttemptResult, error) {
	if !domain.ValidAmount(cmd.Amount) {
		return c.AttemptResult{}, c.ErrInvalidAmount
	}
	seq, err := domain.ParseIdempotencyKey(cmd.IdempotencyKey, cmd.Payable)
	if err != nil {
		return c.AttemptResult{}, err
	}
	hash := domain.RequestHash(cmd)
	// 試行は事業者ごと（tenant の RLS）、支払方法は契約者ごと（customer の RLS）なので両方を設定する
	ctx = db.WithCustomerScope(db.WithTenantScope(ctx, cmd.TenantID), cmd.CustomerAccountID)

	if r, ok, err := s.replay(ctx, cmd.IdempotencyKey, hash); ok || err != nil {
		return r, err
	}

	var (
		attempt  *domain.Attempt
		p        provider.PaymentProvider
		ref      domain.ProviderRef
		finished *c.AttemptResult
	)
	err = s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		// 同じキーの試行が他の実行で先に作られていたら、その試行を返す（E-01。進行中の判定より先に見る）
		if a, err := repo.GetAttemptByKey(ctx, cmd.IdempotencyKey); err == nil {
			if a.State().RequestHash != hash {
				return c.ErrIdempotencyKeyReused
			}
			r := a.Result(true)
			finished = &r
			return nil
		} else if !errors.Is(err, postgres.ErrNotFound) {
			return err
		}
		m, err := repo.GetMethod(ctx, cmd.PaymentMethodID)
		if err != nil {
			return mapNotFound(err, c.ErrPaymentMethodNotFound)
		}
		if m.State().CustomerAccountID != cmd.CustomerAccountID {
			return c.ErrPaymentMethodNotFound // E-08：他人の支払方法
		}
		if id, st, err := repo.BlockingAttempt(ctx, cmd.Payable); err != nil {
			return err
		} else if id != nil {
			// 同じキーの同時実行が、キーの確認の直後にコミットした（READ COMMITTED）。その試行を返す
			if b, err := repo.GetAttempt(ctx, *id, postgres.NoLock); err == nil && b.State().IdempotencyKey == cmd.IdempotencyKey {
				if b.State().RequestHash != hash {
					return c.ErrIdempotencyKeyReused
				}
				r := b.Result(true)
				finished = &r
				return nil
			}
			if st == c.AttemptSucceeded {
				return &c.PayableAlreadyPaidError{AttemptID: *id} // E-04
			}
			return &c.AttemptInProgressError{AttemptID: *id} // E-03・E-05
		}
		base := domain.AttemptState{
			ID: sk.NewID[sk.PaymentAttemptTag](), TenantID: cmd.TenantID, Payable: cmd.Payable, CustomerAccountID: cmd.CustomerAccountID,
			PaymentMethodID: cmd.PaymentMethodID, MethodType: m.State().MethodType, TenantShopID: cmd.TenantShopID, Amount: cmd.Amount,
			IdempotencyKey: cmd.IdempotencyKey, CollectionSeq: seq, RequestHash: hash, RequestedAt: s.d.Clock.Now(),
		}
		var found bool
		ref, found = m.RefFor(cmd.TenantShopID)
		if len(m.State().Refs) > 0 {
			base.Provider = m.State().Refs[0].Provider
		}
		// E-07：削除済み・退会済みの支払方法は、代行に送らずに失敗にする（P-PAY-3）。E-11：参照がなければ再入力が必要
		if !m.Active() || !found {
			code := domain.FailPaymentMethodUnavailable
			if m.Active() {
				code = domain.FailMethodReentryRequired
			}
			a := domain.NewAttempt(base)
			if err := repo.InsertAttempt(ctx, a); err != nil {
				return err
			}
			if err := a.Fail(code, code.DefaultRetryable(), "", s.d.Clock.Now(), domain.ViaSync); err != nil {
				return err
			}
			if err := s.save(ctx, tx, repo, a, c.AttemptProcessing); err != nil {
				return err
			}
			r := a.Result(false)
			finished = &r
			return nil
		}
		base.Provider = ref.Provider
		p, err = s.d.Registry.Get(provider.Key{Method: base.MethodType, Provider: provider.ProviderID(ref.Provider)})
		if err != nil {
			return err
		}
		orderRef := p.OrderRefFor(base.ID.UUID())
		base.ProviderOrderRef = &orderRef
		attempt = domain.NewAttempt(base)
		return repo.InsertAttempt(ctx, attempt)
	})
	switch {
	case errors.Is(err, postgres.ErrDuplicateKey):
		// 同じキーの同時実行（E-01）。先に保存された試行を返す
		r, _, err := s.replay(ctx, cmd.IdempotencyKey, hash)
		return r, err
	case errors.Is(err, postgres.ErrInflightExists), errors.Is(err, postgres.ErrSucceededExists):
		// 同じキーの同時実行が先に保存した場合も、こちらの一意制約に当たることがある
		if r, ok, err := s.replay(ctx, cmd.IdempotencyKey, hash); ok || err != nil {
			return r, err
		}
		return c.AttemptResult{}, s.blockingError(ctx, cmd.Payable)
	case err != nil:
		return c.AttemptResult{}, err
	case finished != nil:
		return *finished, nil
	}

	st := attempt.State()
	out, sendErr := p.Collect(ctx, provider.CollectInput{
		AttemptID: st.ID.UUID(), ProviderOrderRef: *st.ProviderOrderRef, IdempotencyKey: st.ID.String(), Amount: st.Amount, Ref: ref, TenantShopID: st.TenantShopID,
	})
	if errors.Is(sendErr, provider.ErrUnavailable) {
		// E-10：送信していない。試行を残さず、回数を消費しない（P-PAY-4）
		_ = s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error { return postgres.New(tx).DeleteProcessingAttempt(ctx, st.ID) })
		return c.AttemptResult{}, c.ErrProviderUnavailable
	}
	var result c.AttemptResult
	err = s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		a, err := repo.GetAttempt(ctx, st.ID, postgres.ForUpdate)
		if err != nil {
			return err
		}
		from := a.State().Status
		switch {
		case errors.Is(sendErr, provider.ErrNotSent):
			err = a.Fail(domain.FailProcessingError, true, "", s.d.Clock.Now(), domain.ViaSync)
		case sendErr != nil || out.Status == provider.OutcomeUnknown || out.Status == provider.OutcomeNotFound:
			err = a.MarkUnknown() // 送ったかもしれない
		default:
			_, _, err = applyOutcome(a, out, domain.ViaSync, s.d.Clock)
		}
		if err != nil && !errors.Is(err, domain.ErrSameOutcome) {
			return err
		}
		result = a.Result(false)
		if from == a.State().Status {
			return nil // 照合・Webhook が先に確定した
		}
		return s.save(ctx, tx, repo, a, from)
	})
	if err != nil {
		observability.Logger("payments.collect").LogAttrs(ctx, slog.LevelError, "payments.collect_save_failed",
			slog.String("attempt_id", st.ID.String()), observability.Err(err))
		return c.AttemptResult{AttemptID: st.ID, Status: c.AttemptUnknown}, nil // E-06
	}
	return result, nil
}

// replay は同じ冪等キーの試行があれば返す（E-01）。中身が違えば ErrIdempotencyKeyReused（E-02）。
func (s *Service) replay(ctx context.Context, key, hash string) (c.AttemptResult, bool, error) {
	var a *domain.Attempt
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		a, err = postgres.New(tx).GetAttemptByKey(ctx, key)
		return err
	})
	if errors.Is(err, postgres.ErrNotFound) {
		return c.AttemptResult{}, false, nil
	}
	if err != nil {
		return c.AttemptResult{}, false, err
	}
	if a.State().RequestHash != hash {
		return c.AttemptResult{}, true, c.ErrIdempotencyKeyReused
	}
	return a.Result(true), true, nil
}

func (s *Service) blockingError(ctx context.Context, p c.PayableRef) error {
	var id *sk.PaymentAttemptID
	var st c.AttemptStatus
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		id, st, err = postgres.New(tx).BlockingAttempt(ctx, p)
		return err
	})
	if err != nil {
		return err
	}
	if id == nil {
		return fmt.Errorf("payments: concurrent attempt finished; retry")
	}
	if st == c.AttemptSucceeded {
		return &c.PayableAlreadyPaidError{AttemptID: *id}
	}
	return &c.AttemptInProgressError{AttemptID: *id}
}
