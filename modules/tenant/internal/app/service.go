// Package app は tenant モジュールのユースケース（ops・console の操作と contracts.Service の実装）。
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/outbox"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/tenant/contracts"
	"subscription-platform/modules/tenant/internal/adapter/identity"
	"subscription-platform/modules/tenant/internal/adapter/postgres"
	"subscription-platform/modules/tenant/internal/domain"
)

// Service はユースケースの入口。
type Service struct {
	db     *db.ModuleDB
	outbox *outbox.Writer
	clock  sk.Clock
	rand   io.Reader
	idp    identity.Provider
}

// New は Service を作る。rand は API キーの乱数源（本番は crypto/rand）。idp はスタッフの認証基盤（nil なら identity.Dev）。
func New(mdb *db.ModuleDB, w *outbox.Writer, clock sk.Clock, rand io.Reader, idp identity.Provider) *Service {
	if idp == nil {
		idp = identity.Dev{}
	}
	return &Service{db: mdb, outbox: w, clock: clock, rand: rand, idp: idp}
}

var _ contracts.Service = (*Service)(nil)

// ErrStaffNotFound・ErrAPIKeyNotFound は console で指定した ID がない（他テナントの ID も RLS で見えないので同じ）。
var (
	ErrStaffNotFound  = errors.New("tenant: staff not found")
	ErrAPIKeyNotFound = errors.New("tenant: api key not found")
)

func tenantTarget(id sk.TenantID) auditctx.Target {
	return auditctx.Target{Type: "tenant", ID: id.String()}
}

// ---- ops（運営者。router が system スコープを設定する） ----

// RegisterTenant は O1。
func (s *Service) RegisterTenant(ctx context.Context, name string) (domain.TenantState, error) {
	t, err := domain.RegisterTenant(sk.NewID[sk.TenantTag](), name, s.clock.Now())
	if err != nil {
		return domain.TenantState{}, err
	}
	err = s.db.InTx(ctx, func(ctx context.Context, tx db.Tx) error { return postgres.New(tx).InsertTenant(ctx, t) })
	if err != nil {
		return domain.TenantState{}, err
	}
	st := t.State()
	auditctx.Annotate(ctx, "tenant.create", tenantTarget(st.ID), nil, map[string]any{"tenant_name": st.Name}, auditctx.WithTenant(st.ID))
	return st, nil
}

// GetTenantState は O3（ops）と内部の参照。スコープは呼び出し元のもの。
func (s *Service) GetTenantState(ctx context.Context, id sk.TenantID) (domain.TenantState, error) {
	var st domain.TenantState
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		t, err := postgres.New(tx).GetTenant(ctx, id, false)
		if err != nil {
			return mapNotFound(err, contracts.ErrTenantNotFound)
		}
		st = t.State()
		return nil
	})
	return st, err
}

// ListTenants は O2。
func (s *Service) ListTenants(ctx context.Context, status *string, after *postgres.TenantCursor, limit int) ([]domain.TenantState, bool, error) {
	var out []domain.TenantState
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		ts, err := postgres.New(tx).ListTenants(ctx, status, after, limit+1)
		if err != nil {
			return err
		}
		for _, t := range ts {
			out = append(out, t.State())
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// ApproveTenant は O4。同じ shop ID での再実行は 200（イベントなし）。
func (s *Service) ApproveTenant(ctx context.Context, id sk.TenantID, shopID string) (domain.TenantState, error) {
	var st domain.TenantState
	var ev *domain.Approved
	err := s.db.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		t, err := repo.GetTenant(ctx, id, false)
		if err != nil {
			return mapNotFound(err, contracts.ErrTenantNotFound)
		}
		from := t.Status()
		ev, err = t.Approve(shopID, s.clock.Now())
		if err != nil {
			return err
		}
		st = t.State()
		if ev == nil {
			return nil
		}
		if err := repo.UpdateTenant(ctx, t, from, "approved", ev.At); err != nil {
			return err
		}
		env, err := sk.NewEnvelope(contracts.EventTenantApproved, "tenant", id.UUID(), int64(st.Version+1), &id, ev.At,
			contracts.TenantApprovedPayload{TenantID: id, FincodeShopID: ev.ShopID, ApprovedAt: ev.At.UTC()})
		if err != nil {
			return err
		}
		return s.outbox.Append(ctx, tx, env)
	})
	if err != nil {
		return domain.TenantState{}, err
	}
	if ev != nil {
		st.Version++
		auditctx.Annotate(ctx, "tenant.approve", tenantTarget(id),
			map[string]any{"status": string(contracts.TenantPendingReview)},
			map[string]any{"status": string(st.Status), "fincode_shop_id": ev.ShopID}, auditctx.WithTenant(id))
	}
	return st, nil
}

// SuspendTenant は O5。suspended への再実行は 200（イベントなし、reason は変えない）。
func (s *Service) SuspendTenant(ctx context.Context, id sk.TenantID, reason string) (domain.TenantState, error) {
	var st domain.TenantState
	var ev *domain.Suspended
	err := s.db.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		t, err := repo.GetTenant(ctx, id, false)
		if err != nil {
			return mapNotFound(err, contracts.ErrTenantNotFound)
		}
		from := t.Status()
		ev, err = t.Suspend(reason, s.clock.Now())
		if err != nil {
			return err
		}
		st = t.State()
		if ev == nil {
			return nil
		}
		if err := repo.UpdateTenant(ctx, t, from, "suspended", ev.At); err != nil {
			return err
		}
		env, err := sk.NewEnvelope(contracts.EventTenantSuspended, "tenant", id.UUID(), int64(st.Version+1), &id, ev.At,
			contracts.TenantSuspendedPayload{TenantID: id, Reason: ev.Reason, SuspendedAt: ev.At.UTC()})
		if err != nil {
			return err
		}
		return s.outbox.Append(ctx, tx, env)
	})
	if err != nil {
		return domain.TenantState{}, err
	}
	if ev != nil {
		st.Version++
		auditctx.Annotate(ctx, "tenant.suspend", tenantTarget(id),
			map[string]any{"status": string(contracts.TenantActive)},
			map[string]any{"status": string(st.Status), "reason": ev.Reason}, auditctx.WithTenant(id))
	}
	return st, nil
}

// CreateStaff は O6（初期スタッフの作成）。
func (s *Service) CreateStaff(ctx context.Context, tenantID sk.TenantID, subject, email, displayName string, role contracts.Role) (domain.StaffState, error) {
	var st domain.StaffState
	err := s.db.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		t, err := repo.GetTenant(ctx, tenantID, false)
		if err != nil {
			return mapNotFound(err, contracts.ErrTenantNotFound)
		}
		m, err := domain.NewStaffMember(t, sk.NewID[domain.StaffTag](), subject, email, displayName, role, s.clock.Now())
		if err != nil {
			return err
		}
		if err := repo.InsertStaff(ctx, m); err != nil {
			return err
		}
		st = m.State()
		return nil
	})
	if err != nil {
		return domain.StaffState{}, err
	}
	auditctx.Annotate(ctx, "staff.create", auditctx.Target{Type: "staff", ID: st.ID.String()}, nil,
		map[string]any{"staff_id": st.ID.String(), "role": string(st.Role)}, auditctx.WithTenant(tenantID))
	return st, nil
}

// ---- console（スタッフ。router が tenant スコープを設定する） ----

// ListStaff は C1。
func (s *Service) ListStaff(ctx context.Context, tenantID sk.TenantID, after *postgres.StaffCursor, limit int) ([]domain.StaffState, bool, error) {
	var out []domain.StaffState
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		ms, err := postgres.New(tx).ListStaff(ctx, tenantID, after, limit+1)
		if err != nil {
			return err
		}
		for _, m := range ms {
			out = append(out, m.State())
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// ChangeStaffRole は C2。テナント行をロックしてから owner 数を数える（E1）。
// expectedVersion は If-Match（任意）。違えば ErrVersionConflict（FR-015）。
func (s *Service) ChangeStaffRole(ctx context.Context, tenantID sk.TenantID, staffID domain.StaffID, role contracts.Role, expectedVersion *int) (domain.StaffState, error) {
	var st domain.StaffState
	var before contracts.Role
	var changed bool
	err := s.db.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		if _, err := repo.GetTenant(ctx, tenantID, true); err != nil {
			return mapNotFound(err, contracts.ErrTenantNotFound)
		}
		m, err := repo.GetStaff(ctx, staffID)
		if err != nil {
			return mapNotFound(err, ErrStaffNotFound)
		}
		if expectedVersion != nil && *expectedVersion != m.State().Version {
			return postgres.ErrVersionConflict
		}
		before = m.Role()
		owners, err := repo.CountOwners(ctx, tenantID)
		if err != nil {
			return err
		}
		changed, err = m.ChangeRole(role, owners)
		if err != nil {
			return err
		}
		st = m.State()
		if !changed {
			return nil
		}
		return repo.UpdateStaffRole(ctx, m, s.clock.Now())
	})
	if err != nil {
		return domain.StaffState{}, err
	}
	if changed {
		auditctx.Annotate(ctx, "staff.update_role", auditctx.Target{Type: "staff", ID: staffID.String()},
			map[string]any{"role": string(before)}, map[string]any{"role": string(st.Role)})
	}
	return st, nil
}

// RemoveStaff は C3。削除済み・他テナントは ErrStaffNotFound。
func (s *Service) RemoveStaff(ctx context.Context, tenantID sk.TenantID, staffID domain.StaffID) error {
	var removed domain.StaffState
	err := s.db.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		if _, err := repo.GetTenant(ctx, tenantID, true); err != nil {
			return mapNotFound(err, contracts.ErrTenantNotFound)
		}
		m, err := repo.GetStaff(ctx, staffID)
		if err != nil {
			return mapNotFound(err, ErrStaffNotFound)
		}
		owners, err := repo.CountOwners(ctx, tenantID)
		if err != nil {
			return err
		}
		if err := m.CanRemove(owners); err != nil {
			return err
		}
		removed = m.State()
		return mapNotFound(repo.DeleteStaff(ctx, staffID), ErrStaffNotFound)
	})
	if err != nil {
		return err
	}
	auditctx.Annotate(ctx, "staff.delete", auditctx.Target{Type: "staff", ID: staffID.String()},
		map[string]any{"staff_id": staffID.String(), "role": string(removed.Role)}, nil)
	return nil
}

// IssuedKey は C4 の結果。Raw は平文（この応答でだけ返す）。
type IssuedKey struct {
	State domain.APIKeyState
	Raw   string
}

// IssueAPIKey は C4。テナント行をロックして有効なキーの数を数える（E2）。actorSubject は発行したスタッフの subject。
func (s *Service) IssueAPIKey(ctx context.Context, tenantID sk.TenantID, name, actorSubject string) (IssuedKey, error) {
	var out IssuedKey
	err := s.db.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		t, err := repo.GetTenant(ctx, tenantID, true)
		if err != nil {
			return mapNotFound(err, contracts.ErrTenantNotFound)
		}
		by := s.staffIDOf(ctx, repo, tenantID, actorSubject)
		active, err := repo.CountActiveAPIKeys(ctx, tenantID)
		if err != nil {
			return err
		}
		k, raw, err := domain.IssueAPIKey(t, sk.NewID[domain.APIKeyTag](), name, by, active, s.rand, s.clock.Now())
		if err != nil {
			return err
		}
		if err := repo.InsertAPIKey(ctx, k); err != nil {
			return fmt.Errorf("tenant: insert api key: %w", err)
		}
		out = IssuedKey{State: k.State(), Raw: raw}
		return nil
	})
	if err != nil {
		return IssuedKey{}, err
	}
	st := out.State
	auditctx.Annotate(ctx, "api_key.create", auditctx.Target{Type: "api_key", ID: st.ID.String()}, nil,
		map[string]any{"api_key_id": st.ID.String(), "key_prefix": st.Prefix, "key_name": st.Name})
	return out, nil
}

// staffIDOf は操作したスタッフの ID（作成者・失効者の記録用）。見つからなければ nil。
func (s *Service) staffIDOf(ctx context.Context, repo *postgres.Repo, tenantID sk.TenantID, subject string) *domain.StaffID {
	if subject == "" {
		return nil
	}
	m, err := repo.GetStaffBySubject(ctx, tenantID, subject)
	if err != nil {
		return nil
	}
	id := m.ID()
	return &id
}

// ListAPIKeys は C5。
func (s *Service) ListAPIKeys(ctx context.Context, tenantID sk.TenantID, status *string, after *postgres.APIKeyCursor, limit int) ([]domain.APIKeyState, bool, error) {
	var out []domain.APIKeyState
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		ks, err := postgres.New(tx).ListAPIKeys(ctx, tenantID, status, after, limit+1)
		if err != nil {
			return err
		}
		for _, k := range ks {
			out = append(out, k.State())
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// RevokeAPIKey は C6。失効済みも成功（監査注釈は状態が変わったときだけ）。
func (s *Service) RevokeAPIKey(ctx context.Context, tenantID sk.TenantID, id domain.APIKeyID, actorSubject string) error {
	var revoked *domain.APIKeyState
	err := s.db.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		k, err := repo.GetAPIKey(ctx, id)
		if err != nil {
			return mapNotFound(err, ErrAPIKeyNotFound)
		}
		if !k.Revoke(s.staffIDOf(ctx, repo, tenantID, actorSubject), s.clock.Now()) {
			return nil
		}
		if err := repo.RevokeAPIKey(ctx, k); err != nil {
			return fmt.Errorf("tenant: revoke api key: %w", err)
		}
		st := k.State()
		revoked = &st
		return nil
	})
	if err != nil {
		return err
	}
	if revoked != nil {
		auditctx.Annotate(ctx, "api_key.revoke", auditctx.Target{Type: "api_key", ID: id.String()},
			map[string]any{"key_prefix": revoked.Prefix, "status": "active"}, nil)
	}
	return nil
}

// ---- contracts.Service ----

// GetTenant は他モジュール向け。worker からも呼ばれるので、自分で tenant スコープを設定する（RLS の範囲内）。
func (s *Service) GetTenant(ctx context.Context, tenantID sk.TenantID) (contracts.TenantInfo, error) {
	st, err := s.GetTenantState(db.WithTenantScope(ctx, tenantID), tenantID)
	if err != nil {
		return contracts.TenantInfo{}, err
	}
	info := contracts.TenantInfo{TenantID: st.ID, Name: st.Name, Status: st.Status}
	if st.FincodeShopID != nil {
		info.FincodeShopID = *st.FincodeShopID
	}
	return info, nil
}

// dummyHash は存在しない prefix のときにも比較を行い、応答時間の差を小さくするための値（E7）。
var dummyHash = domain.HashKey("sk_000000000000_0000000000000000000000000000000000000000000")

// AuthenticateAPIKey はテナント未確定のまま SECURITY DEFINER 関数でキーを引く（AC-IDN-017・018）。
func (s *Service) AuthenticateAPIKey(ctx context.Context, rawKey string) (sk.TenantID, error) {
	prefix, err := domain.ParseKey(rawKey)
	if err != nil {
		return sk.TenantID{}, contracts.ErrInvalidAPIKey
	}
	var found *postgres.KeyLookup
	err = s.db.ReadTx(db.WithUnscoped(ctx, "api_key_auth"), func(ctx context.Context, tx db.Tx) error {
		k, err := postgres.New(tx).FindAPIKeyByPrefix(ctx, prefix)
		if errors.Is(err, postgres.ErrNotFound) {
			return nil
		}
		found = k
		return err
	})
	if err != nil {
		return sk.TenantID{}, err
	}
	if found == nil {
		domain.MatchesHash(dummyHash, rawKey)
		return sk.TenantID{}, contracts.ErrInvalidAPIKey
	}
	if !domain.MatchesHash(found.Hash, rawKey) || found.Revoked {
		return sk.TenantID{}, contracts.ErrInvalidAPIKey
	}
	return found.TenantID, nil
}

// GetStaffRole は認可のミドルウェア向け。所属していなければ ErrStaffNotFound。
// 招待中なら有効にし、最終利用日時を 1 時間に 1 回まで更新する（ADR-C05。更新が要るときだけ書き込みのトランザクションを開く）。
func (s *Service) GetStaffRole(ctx context.Context, tenantID sk.TenantID, subject string) (contracts.Role, error) {
	var role contracts.Role
	var m *domain.StaffMember
	ctx = db.WithTenantScope(ctx, tenantID)
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		m, err = postgres.New(tx).GetStaffBySubject(ctx, tenantID, subject)
		if err != nil {
			return mapNotFound(err, contracts.ErrStaffNotFound)
		}
		role = m.Role()
		return nil
	})
	if err != nil {
		return role, err
	}
	now := s.clock.Now()
	if activated, touched := m.Activate(), m.Touch(now); activated || touched {
		if err := s.db.InTx(ctx, func(ctx context.Context, tx db.Tx) error { return postgres.New(tx).SaveStaffLifecycle(ctx, m, now) }); err != nil {
			return role, err
		}
	}
	return role, nil
}

// ListOwnerContacts は Integration の通知用（P-INT-1）。suspended のテナントでも返す。
func (s *Service) ListOwnerContacts(ctx context.Context, tenantID sk.TenantID) ([]contracts.OwnerContact, error) {
	var out []contracts.OwnerContact
	err := s.db.ReadTx(db.WithTenantScope(ctx, tenantID), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		if _, err := repo.GetTenant(ctx, tenantID, false); err != nil {
			return mapNotFound(err, contracts.ErrTenantNotFound)
		}
		owners, err := repo.ListOwners(ctx, tenantID)
		if err != nil {
			return err
		}
		for _, o := range owners {
			st := o.State()
			out = append(out, contracts.OwnerContact{StaffID: st.ID.String(), Email: st.Email, DisplayName: st.DisplayName})
		}
		return nil
	})
	return out, err
}

func mapNotFound(err, to error) error {
	if errors.Is(err, postgres.ErrNotFound) {
		return to
	}
	return err
}

// Now は業務時刻（ハンドラがカーソルなどに使う）。
func (s *Service) Now() time.Time { return s.clock.Now() }

// ---- 招待と所属（business-console の設計 §3・§5） ----

// ErrStaffAlreadyMember は同じ事業者に同じメールアドレスのスタッフがいる。
var ErrStaffAlreadyMember = errors.New("tenant: staff already member")

// InviteStaff はメールアドレスでスタッフを招待する（console の C0・ops の最初のオーナー）。アカウントがなければ Cognito で作って招待メールを送り、
// invited で所属を作る。有効なアカウントの人は active で追加し、通知メールのためのイベントを出す。
func (s *Service) InviteStaff(ctx context.Context, tenantID sk.TenantID, email, displayName string, role contracts.Role) (domain.StaffState, error) {
	e, err := domain.NormalizeEmail("email", email)
	if err != nil {
		return domain.StaffState{}, err
	}
	if !role.Valid() {
		return domain.StaffState{}, &domain.ValidationError{Field: "role", Code: "invalid"}
	}
	// 認証基盤を呼ぶ前に、事業者の状態と重複を確かめる（無駄なアカウントを作らない。最終的な判定は保存のトランザクション）
	if err := s.db.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		t, err := repo.GetTenant(ctx, tenantID, false)
		if err != nil {
			return mapNotFound(err, contracts.ErrTenantNotFound)
		}
		if !t.AllowsStaffWrite() {
			return domain.ErrTenantNotWritable
		}
		if _, err := repo.GetStaffByEmail(ctx, tenantID, e); err == nil {
			return ErrStaffAlreadyMember
		} else if !errors.Is(err, postgres.ErrNotFound) {
			return err
		}
		return nil
	}); err != nil {
		return domain.StaffState{}, err
	}
	sub, acct, err := s.idp.EnsureUser(ctx, e, displayName)
	if err != nil {
		return domain.StaffState{}, err
	}
	var st domain.StaffState
	err = s.db.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		t, err := repo.GetTenant(ctx, tenantID, false)
		if err != nil {
			return mapNotFound(err, contracts.ErrTenantNotFound)
		}
		m, err := domain.InviteStaffMember(t, sk.NewID[domain.StaffTag](), sub, e, displayName, role, acct, s.clock.Now())
		if err != nil {
			return err
		}
		if err := repo.InsertStaff(ctx, m); err != nil {
			if errors.Is(err, postgres.ErrStaffExists) {
				return ErrStaffAlreadyMember
			}
			return err
		}
		st = m.State()
		return s.appendStaffEvent(ctx, tx, st, false)
	})
	if err != nil {
		return domain.StaffState{}, err
	}
	auditctx.Annotate(ctx, "staff.invite", auditctx.Target{Type: "staff", ID: st.ID.String()}, nil,
		map[string]any{"staff_id": st.ID.String(), "role": string(st.Role), "status": string(st.Status)}, auditctx.WithTenant(tenantID))
	return st, nil
}

func (s *Service) appendStaffEvent(ctx context.Context, tx db.Tx, st domain.StaffState, resent bool) error {
	var env sk.EventEnvelope
	var err error
	if st.Status == domain.StaffActive && !resent {
		env, err = sk.NewEnvelope(contracts.EventStaffAdded, "staff_member", st.ID.UUID(), int64(st.Version), &st.TenantID, s.clock.Now(),
			contracts.StaffAddedPayload{TenantID: st.TenantID, StaffID: st.ID.String(), Role: st.Role})
	} else {
		env, err = sk.NewEnvelope(contracts.EventStaffInvited, "staff_member", st.ID.UUID(), int64(st.Version), &st.TenantID, s.clock.Now(),
			contracts.StaffInvitedPayload{TenantID: st.TenantID, StaffID: st.ID.String(), Resent: resent})
	}
	if err != nil {
		return err
	}
	return s.outbox.Append(ctx, tx, env)
}

// ResendInvitation は招待を再送する（invited のときだけ。期限を再送から 7 日にする）。アカウントがすでに有効なら所属を active にする。
func (s *Service) ResendInvitation(ctx context.Context, tenantID sk.TenantID, staffID domain.StaffID) (domain.StaffState, error) {
	var email string
	if err := s.db.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		m, err := postgres.New(tx).GetStaff(ctx, staffID)
		if err != nil {
			return mapNotFound(err, ErrStaffNotFound)
		}
		if m.State().Status != domain.StaffInvited {
			return domain.ErrNotInvited
		}
		email = m.State().Email
		return nil
	}); err != nil {
		return domain.StaffState{}, err
	}
	acct, err := s.idp.ResendInvitation(ctx, email)
	if err != nil {
		return domain.StaffState{}, err
	}
	var st domain.StaffState
	err = s.db.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		m, err := repo.GetStaff(ctx, staffID)
		if err != nil {
			return mapNotFound(err, ErrStaffNotFound)
		}
		now := s.clock.Now()
		if acct == domain.AccountActive {
			m.Activate()
		} else if err := m.MarkInvitationResent(now); err != nil {
			return err
		}
		if err := repo.SaveStaffLifecycle(ctx, m, now); err != nil {
			return err
		}
		st = m.State()
		if st.Status == domain.StaffInvited {
			return s.appendStaffEvent(ctx, tx, st, true)
		}
		return nil
	})
	if err != nil {
		return domain.StaffState{}, err
	}
	auditctx.Annotate(ctx, "staff.resend_invitation", auditctx.Target{Type: "staff", ID: staffID.String()}, nil, map[string]any{"status": string(st.Status)})
	return st, nil
}

// MembershipView は所属する事業者の一覧の 1 件。Permissions はそのロールで使える操作（画面のボタンの出し分け用。§5.5）。
type MembershipView struct {
	TenantID     sk.TenantID
	TenantName   string
	TenantStatus contracts.TenantStatus
	Role         contracts.Role
	StaffStatus  domain.StaffStatus
	Permissions  []contracts.Permission
}

// ListMemberships はログイン中のスタッフ（subject）が所属する事業者。招待中の所属はここで有効にする（ADR-C05）。
func (s *Service) ListMemberships(ctx context.Context, subject string) ([]MembershipView, error) {
	sys := db.WithSystemScope(db.ClearScope(ctx), "staff memberships")
	var out []MembershipView
	err := s.db.InTx(sys, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		ms, err := repo.ListMembershipsBySubject(ctx, subject)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		for _, m := range ms {
			activated, touched := m.Staff.Activate(), m.Staff.Touch(now)
			if activated || touched {
				if err := repo.SaveStaffLifecycle(ctx, m.Staff, now); err != nil {
					return err
				}
			}
			st := m.Staff.State()
			var perms []contracts.Permission
			for _, p := range contracts.Permissions {
				if contracts.Allows(st.Role, p) {
					perms = append(perms, p)
				}
			}
			out = append(out, MembershipView{TenantID: st.TenantID, TenantName: m.TenantName, TenantStatus: m.TenantStatus, Role: st.Role, StaffStatus: st.Status, Permissions: perms})
		}
		return nil
	})
	return out, err
}

// GetStaffContact は contracts.Service（integration のスタッフ宛ての通知）。
func (s *Service) GetStaffContact(ctx context.Context, tenantID sk.TenantID, staffID string) (contracts.OwnerContact, error) {
	id, err := sk.ParseID[domain.StaffTag](staffID)
	if err != nil {
		return contracts.OwnerContact{}, contracts.ErrStaffNotFound
	}
	var out contracts.OwnerContact
	err = s.db.ReadTx(db.WithTenantScope(ctx, tenantID), func(ctx context.Context, tx db.Tx) error {
		m, err := postgres.New(tx).GetStaff(ctx, id)
		if err != nil {
			return mapNotFound(err, contracts.ErrStaffNotFound)
		}
		st := m.State()
		out = contracts.OwnerContact{StaffID: st.ID.String(), Email: st.Email, DisplayName: st.DisplayName}
		return nil
	})
	return out, err
}
