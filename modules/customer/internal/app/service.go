// Package app は customer モジュールのユースケース（contracts.Service の実装）。
package app

import (
	"context"
	"errors"
	"log/slog"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/outbox"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/customer/contracts"
	"subscription-platform/modules/customer/internal/adapter/postgres"
	"subscription-platform/modules/customer/internal/domain"
)

// Service はユースケースの入口。
type Service struct {
	db     *db.ModuleDB
	outbox *outbox.Writer
	clock  sk.Clock
}

func New(mdb *db.ModuleDB, w *outbox.Writer, clock sk.Clock) *Service {
	return &Service{db: mdb, outbox: w, clock: clock}
}

var _ contracts.Service = (*Service)(nil)

const maxLinkIDAttempts = 3

// errInsertRace は初回作成の挿入が email の一意制約に当たった（同じ subject の同時作成か、別の subject の J4）。
var errInsertRace = errors.New("customer: insert raced on email")

// log は呼び出し時のロガー（Setup の後の設定を使う）。
func log() *slog.Logger { return observability.Logger("customer.app") }

func mapNotFound(err, to error) error {
	if errors.Is(err, postgres.ErrNotFound) {
		return to
	}
	return err
}

// ResolveOrCreateAccount は §7.3 の JIT 規則（J1〜J5）。
func (s *Service) ResolveOrCreateAccount(ctx context.Context, in contracts.AuthIdentity) (contracts.AccountView, error) {
	v, err := s.resolveOrCreate(ctx, in)
	if errors.Is(err, errInsertRace) {
		// 同じ subject の同時作成で、email の一意制約に先に当たった（E8）。やり直すと subject で見つかる
		v, err = s.resolveOrCreate(ctx, in)
		if errors.Is(err, errInsertRace) {
			return contracts.AccountView{}, contracts.ErrEmailAlreadyRegistered
		}
	}
	if errors.Is(err, postgres.ErrEmailTaken) && in.Email != "" {
		// J1 で更新しようとした email が、確認の後に他のアカウントに使われた。email を変えずにやり直す
		log().LogAttrs(ctx, slog.LevelWarn, "customer.email_sync_skipped", slog.String("reason", "email_taken"))
		in.Email = ""
		return s.resolveOrCreate(ctx, in)
	}
	return v, err
}

func (s *Service) resolveOrCreate(ctx context.Context, in contracts.AuthIdentity) (contracts.AccountView, error) {
	var view contracts.AccountView
	err := s.db.InTx(db.WithUnscoped(ctx, "customer_jit"), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		a, err := repo.GetAccountBySubject(ctx, in.Subject)
		switch {
		case err == nil:
			view, err = s.syncExisting(ctx, repo, a, in)
			return err
		case !errors.Is(err, postgres.ErrNotFound):
			return err
		}
		// J3〜J5: 初回ログイン
		email, ok := domain.VerifiedEmail(in)
		if !ok {
			return contracts.ErrEmailUnverified
		}
		taken, err := repo.EmailUsedByOther(ctx, email, in.Subject)
		if err != nil {
			return err
		}
		if taken {
			return contracts.ErrEmailAlreadyRegistered
		}
		a, err = domain.ProvisionAccount(sk.NewID[sk.CustomerAccountTag](), in, s.clock.Now())
		if err != nil {
			return err
		}
		created, err := repo.InsertAccountIfAbsent(ctx, a)
		if errors.Is(err, postgres.ErrEmailTaken) {
			return errInsertRace
		}
		if err != nil {
			return err
		}
		if created {
			log().LogAttrs(ctx, slog.LevelInfo, "customer.account_provisioned", slog.String("customer_account_id", a.ID().String()))
			view = a.View()
			return nil
		}
		// 同時に作られた（E8）。作られた方を返す
		a, err = repo.GetAccountBySubject(ctx, in.Subject)
		if err != nil {
			return err
		}
		if a.Status() == contracts.AccountWithdrawn {
			return contracts.ErrAccountWithdrawn
		}
		view = a.View()
		return nil
	})
	return view, err
}

// syncExisting は J1・J2。
func (s *Service) syncExisting(ctx context.Context, repo *postgres.Repo, a *domain.Account, in contracts.AuthIdentity) (contracts.AccountView, error) {
	prevEmail := a.State().Email
	emailChanged, nameChanged, err := a.SyncProfile(in)
	if err != nil {
		return contracts.AccountView{}, err // J2: withdrawn
	}
	if emailChanged {
		taken, err := repo.EmailUsedByOther(ctx, *a.State().Email, in.Subject)
		if err != nil {
			return contracts.AccountView{}, err
		}
		if taken {
			log().LogAttrs(ctx, slog.LevelWarn, "customer.email_sync_skipped", slog.String("customer_account_id", a.ID().String()), slog.String("reason", "email_taken"))
			a.RevertEmail(prevEmail)
			emailChanged = false
		}
	}
	if emailChanged || nameChanged {
		err := repo.UpdateAccount(ctx, a, a.Status(), s.clock.Now())
		if errors.Is(err, postgres.ErrVersionConflict) {
			// 同時のログインが先に同期した。保存済みの値を返す
			cur, err := repo.GetAccountBySubject(ctx, in.Subject)
			if err != nil {
				return contracts.AccountView{}, err
			}
			return cur.View(), nil
		}
		if err != nil {
			return contracts.AccountView{}, err
		}
	}
	return a.View(), nil
}

// EnsureTenantCustomerID はリンクがあれば返し、なければ作る（E9・E10・E11）。
func (s *Service) EnsureTenantCustomerID(ctx context.Context, tenantID sk.TenantID, id sk.CustomerAccountID) (contracts.TenantCustomerID, error) {
	var out contracts.TenantCustomerID
	err := s.db.InTx(db.WithTenantScope(ctx, tenantID), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		// 退会（FOR UPDATE）と直列にし、退会後に新しいリンクを作らない（E11）
		a, err := repo.GetAccount(ctx, id, postgres.ForShare)
		if err != nil {
			return mapNotFound(err, contracts.ErrAccountNotFound)
		}
		if a.Status() == contracts.AccountWithdrawn {
			return contracts.ErrAccountWithdrawn
		}
		for range maxLinkIDAttempts {
			if out, err = repo.GetLink(ctx, tenantID, id); err == nil {
				return nil
			} else if !errors.Is(err, postgres.ErrNotFound) {
				return err
			}
			if err := repo.InsertLinkIfAbsent(ctx, domain.NewTenantCustomerID(), tenantID, id, s.clock.Now()); err != nil {
				return err
			}
		}
		if out, err = repo.GetLink(ctx, tenantID, id); err == nil {
			return nil
		}
		return errors.New("customer: could not allocate tenant customer id")
	})
	return out, err
}

// ResolveTenantCustomerID は作らない。withdrawn でも返す。
func (s *Service) ResolveTenantCustomerID(ctx context.Context, tenantID sk.TenantID, id sk.CustomerAccountID) (contracts.TenantCustomerID, error) {
	var out contracts.TenantCustomerID
	err := s.db.ReadTx(db.WithTenantScope(ctx, tenantID), func(ctx context.Context, tx db.Tx) error {
		var err error
		out, err = postgres.New(tx).GetLink(ctx, tenantID, id)
		return mapNotFound(err, contracts.ErrLinkNotFound)
	})
	return out, err
}

// GetContact は withdrawn なら ErrAccountWithdrawn（E12）。
func (s *Service) GetContact(ctx context.Context, id sk.CustomerAccountID) (contracts.Contact, error) {
	var c contracts.Contact
	err := s.db.ReadTx(db.WithCustomerScope(ctx, id), func(ctx context.Context, tx db.Tx) error {
		a, err := postgres.New(tx).GetAccount(ctx, id, postgres.NoLock)
		if err != nil {
			return mapNotFound(err, contracts.ErrAccountNotFound)
		}
		c, err = a.Contact()
		return err
	})
	return c, err
}

// GetAccount は契約者自身のアカウント（GET /v1/me）。
func (s *Service) GetAccount(ctx context.Context, id sk.CustomerAccountID) (contracts.AccountView, error) {
	var v contracts.AccountView
	err := s.db.ReadTx(db.WithCustomerScope(ctx, id), func(ctx context.Context, tx db.Tx) error {
		a, err := postgres.New(tx).GetAccount(ctx, id, postgres.NoLock)
		if err != nil {
			return mapNotFound(err, contracts.ErrAccountNotFound)
		}
		v = a.View()
		return nil
	})
	return v, err
}

// Withdraw は退会させる。withdrawn なら nil（Outbox は増えない。AC-IDN-030）。
func (s *Service) Withdraw(ctx context.Context, id sk.CustomerAccountID) error {
	return s.db.InTx(db.WithCustomerScope(ctx, id), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		a, err := repo.GetAccount(ctx, id, postgres.ForUpdate)
		if err != nil {
			return mapNotFound(err, contracts.ErrAccountNotFound)
		}
		from := a.Status()
		now := s.clock.Now()
		if !a.Withdraw(now) {
			return nil
		}
		if err := repo.UpdateAccount(ctx, a, from, now); err != nil {
			return err
		}
		env, err := sk.NewEnvelope(contracts.EventCustomerWithdrawn, "customer_account", id.UUID(), int64(a.State().Version+1), nil, now,
			contracts.CustomerWithdrawnPayload{CustomerAccountID: id, WithdrawnAt: now.UTC()})
		if err != nil {
			return err
		}
		return s.outbox.Append(ctx, tx, env)
	})
}

// GetTenantCustomerProfile はそのテナントとのリンクがなければ ErrLinkNotFound（AC-IDN-032）。
func (s *Service) GetTenantCustomerProfile(ctx context.Context, tenantID sk.TenantID, id sk.CustomerAccountID) (contracts.TenantCustomerProfile, error) {
	var p contracts.TenantCustomerProfile
	err := s.db.ReadTx(db.WithTenantScope(ctx, tenantID), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		link, err := repo.GetLink(ctx, tenantID, id)
		if err != nil {
			return mapNotFound(err, contracts.ErrLinkNotFound)
		}
		a, err := repo.GetAccount(ctx, id, postgres.NoLock)
		if err != nil {
			return mapNotFound(err, contracts.ErrAccountNotFound)
		}
		p = contracts.TenantCustomerProfile{TenantCustomerID: link, Withdrawn: a.Status() == contracts.AccountWithdrawn}
		if c, err := a.Contact(); err == nil {
			p.Email, p.Name = c.Email, c.Name
		}
		return nil
	})
	return p, err
}
