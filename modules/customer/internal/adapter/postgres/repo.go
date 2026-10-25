// Package postgres は customer スキーマのリポジトリ（sqlc の生成コードを包み、domain の型に変換する）。
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/customer/contracts"
	"subscription-platform/modules/customer/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/customer/internal/domain"
)

var (
	ErrNotFound        = errors.New("customer: row not found")
	ErrVersionConflict = errors.New("customer: version conflict")
	ErrEmailTaken      = errors.New("customer: email used by another active account")
)

// Lock は行ロックの種類。
type Lock int

const (
	NoLock Lock = iota
	ForShare
	ForUpdate
)

// Repo は 1 トランザクション分のリポジトリ。
type Repo struct{ q *sqlcgen.Queries }

func New(tx db.Tx) *Repo { return &Repo{q: sqlcgen.New(tx)} }

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func toAccount(r sqlcgen.CustomerCustomerAccount) *domain.Account {
	var w *time.Time
	if r.WithdrawnAt != nil {
		u := r.WithdrawnAt.UTC()
		w = &u
	}
	return domain.RestoreAccount(domain.AccountState{
		ID: sk.IDFromUUID[sk.CustomerAccountTag](r.ID), Subject: r.Subject, Email: r.Email, Name: r.Name,
		Status: contracts.AccountStatus(r.Status), WithdrawnAt: w, Version: int(r.Version), CreatedAt: r.CreatedAt.UTC(),
	})
}

func (r *Repo) GetAccount(ctx context.Context, id sk.CustomerAccountID, lock Lock) (*domain.Account, error) {
	var row sqlcgen.CustomerCustomerAccount
	var err error
	switch lock {
	case ForUpdate:
		row, err = r.q.GetAccountForUpdate(ctx, id.UUID())
	case ForShare:
		row, err = r.q.GetAccountForShare(ctx, id.UUID())
	case NoLock:
		row, err = r.q.GetAccount(ctx, id.UUID())
	}
	if err != nil {
		return nil, notFound(err)
	}
	return toAccount(row), nil
}

func (r *Repo) GetAccountBySubject(ctx context.Context, subject string) (*domain.Account, error) {
	row, err := r.q.GetAccountBySubject(ctx, subject)
	if err != nil {
		return nil, notFound(err)
	}
	return toAccount(row), nil
}

// EmailUsedByOther は他の subject の active アカウントが email を使っているか。
func (r *Repo) EmailUsedByOther(ctx context.Context, email, subject string) (bool, error) {
	return r.q.ExistsActiveEmail(ctx, sqlcgen.ExistsActiveEmailParams{Email: &email, Subject: subject})
}

// InsertAccountIfAbsent は subject が無ければ作る（同時作成は 1 件に収束。J5・E8）。作ったら true。
func (r *Repo) InsertAccountIfAbsent(ctx context.Context, a *domain.Account) (bool, error) {
	s := a.State()
	n, err := r.q.InsertAccountIfAbsent(ctx, sqlcgen.InsertAccountIfAbsentParams{ID: s.ID.UUID(), Subject: s.Subject, Email: s.Email, Name: s.Name, CreatedAt: s.CreatedAt})
	if db.IsUniqueViolation(err) {
		return false, ErrEmailTaken
	}
	return n == 1, err
}

// UpdateAccount は楽観ロックで保存する。from は保存前の状態（domain.state_changed のログ）。
func (r *Repo) UpdateAccount(ctx context.Context, a *domain.Account, from contracts.AccountStatus, now time.Time) error {
	s := a.State()
	n, err := r.q.UpdateAccount(ctx, sqlcgen.UpdateAccountParams{
		ID: s.ID.UUID(), Email: s.Email, Name: s.Name, Status: string(s.Status), WithdrawnAt: s.WithdrawnAt, UpdatedAt: now, Version: int32(s.Version),
	})
	if db.IsUniqueViolation(err) {
		return ErrEmailTaken
	}
	if err != nil {
		return fmt.Errorf("customer: update account: %w", err)
	}
	if n == 0 {
		return ErrVersionConflict
	}
	observability.StateChanged(ctx, "customer_account", s.ID.String(), string(from), string(s.Status), "")
	return nil
}

// GetLink は (tenant, account) のリンクの ID を返す。
func (r *Repo) GetLink(ctx context.Context, t sk.TenantID, id sk.CustomerAccountID) (contracts.TenantCustomerID, error) {
	row, err := r.q.GetLink(ctx, sqlcgen.GetLinkParams{TenantID: t.UUID(), CustomerAccountID: id.UUID()})
	if err != nil {
		return "", notFound(err)
	}
	return contracts.TenantCustomerID(row.ID), nil
}

// InsertLinkIfAbsent はリンクを作る。重複（同じ組・まれな id の衝突）は何もしない。
func (r *Repo) InsertLinkIfAbsent(ctx context.Context, linkID contracts.TenantCustomerID, t sk.TenantID, id sk.CustomerAccountID, now time.Time) error {
	_, err := r.q.InsertLinkIfAbsent(ctx, sqlcgen.InsertLinkIfAbsentParams{ID: string(linkID), TenantID: t.UUID(), CustomerAccountID: id.UUID(), CreatedAt: now})
	return err
}

// GetActiveAccountIDByEmail は有効なアカウントの ID（メールアドレスの完全一致。system スコープで呼ぶ）。
func (r *Repo) GetActiveAccountIDByEmail(ctx context.Context, email string) (sk.CustomerAccountID, error) {
	id, err := r.q.GetActiveAccountIDByEmail(ctx, &email)
	if err != nil {
		return sk.CustomerAccountID{}, notFound(err)
	}
	return sk.IDFromUUID[sk.CustomerAccountTag](id), nil
}
