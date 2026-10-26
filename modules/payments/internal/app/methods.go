package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/postgres"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/domain"
)

// 支払方法（契約者ごと。customer スコープの RLS で守る。§6）。

func customerScope(ctx context.Context, id sk.CustomerAccountID) context.Context {
	return db.WithCustomerScope(ctx, id)
}

// RegisterPaymentMethod はトークンから支払方法を登録する。同じ Idempotency-Key の再送は同じ支払方法を返す。
func (s *Service) RegisterPaymentMethod(ctx context.Context, cmd c.RegisterPaymentMethodCmd) (c.PaymentMethodView, error) {
	ctx = customerScope(ctx, cmd.CustomerAccountID)
	today := s.d.Clock.Today()
	var key *string
	if cmd.IdempotencyKey != "" {
		key = &cmd.IdempotencyKey
		var existing *domain.PaymentMethod
		err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
			var err error
			existing, err = postgres.New(tx).GetMethodByRegistrationKey(ctx, cmd.CustomerAccountID, cmd.IdempotencyKey)
			return err
		})
		if err == nil {
			return existing.View(today), nil
		}
		if !errors.Is(err, postgres.ErrNotFound) {
			return c.PaymentMethodView{}, err
		}
	}
	p, err := s.d.Registry.ForRegistration(cmd.MethodType, cmd.Provider)
	if err != nil {
		return c.PaymentMethodView{}, err
	}
	reg, ok := provider.Capability[provider.MethodRegistrar](p)
	if !ok {
		return c.PaymentMethodView{}, c.ErrUnsupportedMethod
	}
	// 上限は代行に送る前に確かめる（作ったトークンを無駄にしない）。最終的な判定は保存のトランザクションで行う
	if err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		n, err := postgres.New(tx).CountActiveMethods(ctx, cmd.CustomerAccountID)
		if err == nil && n >= domain.MaxActiveMethods {
			return c.ErrPaymentMethodLimitExceeded
		}
		return err
	}); err != nil {
		return c.PaymentMethodView{}, err
	}
	if err := s.guard(ctx, p); err != nil {
		if errors.Is(err, provider.ErrUnavailable) {
			return c.PaymentMethodView{}, c.ErrProviderUnavailable // 503 provider_unavailable（§7.2）
		}
		return c.PaymentMethodView{}, err
	}
	rm, err := reg.RegisterMethod(ctx, provider.RegisterInput{CustomerAccountID: cmd.CustomerAccountID, Token: cmd.Token, IdempotencyKey: cmd.IdempotencyKey})
	switch {
	case errors.Is(err, c.ErrInvalidToken):
		return c.PaymentMethodView{}, c.ErrInvalidToken
	case errors.Is(err, provider.ErrUnavailable):
		return c.PaymentMethodView{}, c.ErrProviderUnavailable
	case err != nil:
		return c.PaymentMethodView{}, err
	}
	m := domain.NewPaymentMethod(sk.NewID[sk.PaymentMethodTag](), cmd.CustomerAccountID, cmd.MethodType, rm.Display, rm.Ref, s.d.Clock.Now())
	err = s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		n, err := repo.CountActiveMethods(ctx, cmd.CustomerAccountID)
		if err != nil {
			return err
		}
		if n >= domain.MaxActiveMethods {
			return c.ErrPaymentMethodLimitExceeded
		}
		return repo.InsertMethod(ctx, m, key)
	})
	if errors.Is(err, postgres.ErrDuplicateRegKey) {
		// 同じキーの同時実行。先に保存された方を返す
		var existing *domain.PaymentMethod
		err = s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
			var err error
			existing, err = postgres.New(tx).GetMethodByRegistrationKey(ctx, cmd.CustomerAccountID, cmd.IdempotencyKey)
			return err
		})
		if err != nil {
			return c.PaymentMethodView{}, err
		}
		return existing.View(today), nil
	}
	if err != nil {
		return c.PaymentMethodView{}, err
	}
	return m.View(today), nil
}

// guard は登録の前にブレーカーを確かめる（決済と同じく、開いていれば送らない）。
func (s *Service) guard(ctx context.Context, p provider.PaymentProvider) error {
	if g, ok := p.(interface {
		Allow(context.Context) error
	}); ok {
		return g.Allow(ctx)
	}
	return nil
}

func (s *Service) GetPaymentMethod(ctx context.Context, customer sk.CustomerAccountID, id sk.PaymentMethodID) (c.PaymentMethodView, error) {
	var v c.PaymentMethodView
	err := s.d.DB.ReadTx(customerScope(ctx, customer), func(ctx context.Context, tx db.Tx) error {
		m, err := postgres.New(tx).GetMethod(ctx, id)
		if err != nil {
			return mapNotFound(err, c.ErrPaymentMethodNotFound)
		}
		if m.State().CustomerAccountID != customer {
			return c.ErrPaymentMethodNotFound
		}
		v = m.View(s.d.Clock.Today())
		return nil
	})
	return v, err
}

// ListPaymentMethods は active な支払方法（作成の新しい順）。cursor は "作成日時（RFC 3339）|ID"（contracts 用。API は暗号化したカーソルを使う）。
func (s *Service) ListPaymentMethods(ctx context.Context, customer sk.CustomerAccountID, cursor string, limit int) (c.Page[c.PaymentMethodView], error) {
	var after *postgres.Cursor
	if cursor != "" {
		ts, id, ok := strings.Cut(cursor, "|")
		t, err1 := time.Parse(time.RFC3339Nano, ts)
		u, err2 := uuid.Parse(id)
		if !ok || err1 != nil || err2 != nil {
			return c.Page[c.PaymentMethodView]{}, fmt.Errorf("payments: bad cursor %q", cursor)
		}
		after = &postgres.Cursor{CreatedAt: t, ID: u}
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	items, more, err := s.ListMethods(ctx, customer, after, limit)
	page := c.Page[c.PaymentMethodView]{Items: items}
	if more {
		last := items[len(items)-1]
		page.NextCursor = last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID.String()
	}
	return page, err
}

// ListMethods は API 用の一覧（カーソルは HTTP 層が暗号化する）。
func (s *Service) ListMethods(ctx context.Context, customer sk.CustomerAccountID, after *postgres.Cursor, limit int) ([]c.PaymentMethodView, bool, error) {
	var out []c.PaymentMethodView
	err := s.d.DB.ReadTx(customerScope(ctx, customer), func(ctx context.Context, tx db.Tx) error {
		ms, err := postgres.New(tx).ListActiveMethods(ctx, customer, after, limit+1)
		for _, m := range ms {
			out = append(out, m.View(s.d.Clock.Today()))
		}
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// DeletePaymentMethod は削除する（使用中かの判定は Orchestration）。他人・存在しないものは ErrPaymentMethodNotFound、削除済みは nil。
func (s *Service) DeletePaymentMethod(ctx context.Context, customer sk.CustomerAccountID, id sk.PaymentMethodID) error {
	return s.d.DB.InTx(customerScope(ctx, customer), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		m, err := repo.GetMethod(ctx, id)
		if err != nil {
			return mapNotFound(err, c.ErrPaymentMethodNotFound)
		}
		if m.State().CustomerAccountID != customer {
			return c.ErrPaymentMethodNotFound
		}
		if !m.Delete(s.d.Clock.Now()) {
			return nil
		}
		return repo.UpdateMethod(ctx, m, s.d.Clock.Now())
	})
}

// EnsureProviderRef は事業者ショップ向けの参照を用意する。共有できる代行（stub）では何もしない。
func (s *Service) EnsureProviderRef(ctx context.Context, cmd c.EnsureProviderRefCmd) error {
	if cmd.TenantShopID == nil {
		return nil
	}
	return s.d.DB.InTx(system(ctx, "ensure_provider_ref"), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		m, err := repo.GetMethod(ctx, cmd.PaymentMethodID)
		if err != nil {
			return mapNotFound(err, c.ErrPaymentMethodNotFound)
		}
		shared, ok := m.RefFor(nil)
		if !ok {
			return c.ErrUnsupportedMethod
		}
		if r, found := m.RefFor(cmd.TenantShopID); found && r.TenantShopID != nil {
			return nil
		}
		p, err := s.d.Registry.Get(provider.Key{Method: m.State().MethodType, Provider: provider.ProviderID(shared.Provider)})
		if err != nil {
			return err
		}
		if p.Capabilities().RefSharing == provider.RefShared {
			return nil
		}
		prov, ok := provider.Capability[provider.TenantRefProvisioner](p)
		if !ok {
			return c.ErrUnsupportedMethod
		}
		ref, err := prov.EnsureTenantRef(ctx, provider.EnsureTenantRefInput{Shared: shared, TenantShopID: *cmd.TenantShopID})
		if err != nil {
			return err
		}
		if m.AddProviderRef(ref) {
			return repo.InsertRef(ctx, m, ref)
		}
		return nil
	})
}

// HandleWithdrawal は customer.withdrawn の処理：active な支払方法をすべて削除し、表示をマスキングする（§5.1）。
// Inbox（processed_messages）と同じトランザクションで呼ばれる。
func (s *Service) HandleWithdrawal(ctx context.Context, tx db.Tx, customer sk.CustomerAccountID) error {
	repo := postgres.New(tx)
	ms, err := repo.ListActiveMethods(ctx, customer, nil, domain.MaxActiveMethods*10)
	if err != nil {
		return err
	}
	now := s.d.Clock.Now()
	for _, m := range ms {
		if !m.DeleteOnWithdrawal(now) {
			continue
		}
		if err := repo.UpdateMethod(ctx, m, now); err != nil {
			return err
		}
		for _, ref := range m.State().Refs {
			p, ok := s.d.Registry.ByProvider(ref.Provider)
			if !ok {
				continue
			}
			if d, ok := provider.Capability[provider.MethodDisabler](p); ok {
				if err := d.DisableMethod(ctx, ref); err != nil {
					observability.Logger("payments.withdrawal").LogAttrs(ctx, slog.LevelWarn, "payments.disable_method_failed", observability.Err(err))
				}
			}
		}
	}
	return nil
}
