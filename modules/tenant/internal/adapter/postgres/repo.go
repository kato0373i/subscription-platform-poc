// Package postgres は tenant スキーマのリポジトリ（sqlc の生成コードを包み、domain の型に変換する）。
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/tenant/contracts"
	"subscription-platform/modules/tenant/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/tenant/internal/domain"
)

var (
	ErrNotFound        = errors.New("tenant: row not found")
	ErrVersionConflict = errors.New("tenant: version conflict")
	ErrShopIDTaken     = errors.New("tenant: fincode shop id taken")
	ErrStaffExists     = errors.New("tenant: staff already exists")
)

// Repo は 1 トランザクション分のリポジトリ。
type Repo struct{ q *sqlcgen.Queries }

// New は tx を使うリポジトリを作る。
func New(tx db.Tx) *Repo { return &Repo{q: sqlcgen.New(tx)} }

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func constraint(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.ConstraintName
	}
	return ""
}

// ---- Tenant ----

func toTenant(r sqlcgen.TenantTenant) *domain.Tenant {
	return domain.RestoreTenant(domain.TenantState{
		ID: sk.IDFromUUID[sk.TenantTag](r.ID), Name: r.Name, Status: contracts.TenantStatus(r.Status),
		FincodeShopID: r.FincodeShopID, ApprovedAt: utcPtr(r.ApprovedAt), SuspendedAt: utcPtr(r.SuspendedAt), SuspendReason: r.SuspendReason,
		Version: int(r.Version), CreatedAt: r.CreatedAt.UTC(),
	})
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// GetTenant は事業者を読む。forUpdate なら行をロックする（OwnerGuard・キーの上限。E1・E2）。
func (r *Repo) GetTenant(ctx context.Context, id sk.TenantID, forUpdate bool) (*domain.Tenant, error) {
	var row sqlcgen.TenantTenant
	var err error
	if forUpdate {
		row, err = r.q.GetTenantForUpdate(ctx, id.UUID())
	} else {
		row, err = r.q.GetTenant(ctx, id.UUID())
	}
	if err != nil {
		return nil, notFound(err)
	}
	return toTenant(row), nil
}

func (r *Repo) InsertTenant(ctx context.Context, t *domain.Tenant) error {
	s := t.State()
	return r.q.InsertTenant(ctx, sqlcgen.InsertTenantParams{ID: s.ID.UUID(), Name: s.Name, CreatedAt: s.CreatedAt})
}

// UpdateTenant は楽観ロックで保存する（E4）。from は保存前の状態（domain.state_changed のログ）。
func (r *Repo) UpdateTenant(ctx context.Context, t *domain.Tenant, from contracts.TenantStatus, reason string, now time.Time) error {
	s := t.State()
	n, err := r.q.UpdateTenant(ctx, sqlcgen.UpdateTenantParams{
		ID: s.ID.UUID(), Status: string(s.Status), FincodeShopID: s.FincodeShopID, ApprovedAt: s.ApprovedAt,
		SuspendedAt: s.SuspendedAt, SuspendReason: s.SuspendReason, UpdatedAt: now, Version: int32(s.Version),
	})
	if err != nil {
		if constraint(err) == "tenants_fincode_shop_id_uq" {
			return ErrShopIDTaken
		}
		return fmt.Errorf("tenant: update tenant: %w", err)
	}
	if n == 0 {
		return ErrVersionConflict
	}
	observability.StateChanged(ctx, "tenant", s.ID.String(), string(from), string(s.Status), reason)
	return nil
}

// TenantCursor は一覧の位置（作成日時の新しい順）。
type TenantCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

func (r *Repo) ListTenants(ctx context.Context, status *string, after *TenantCursor, limit int) ([]*domain.Tenant, error) {
	p := sqlcgen.ListTenantsParams{Limit: int32(limit), Status: status}
	if after != nil {
		p.AfterCreatedAt, p.AfterID = &after.CreatedAt, &after.ID
	}
	rows, err := r.q.ListTenants(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("tenant: list tenants: %w", err)
	}
	out := make([]*domain.Tenant, len(rows))
	for i, row := range rows {
		out[i] = toTenant(row)
	}
	return out, nil
}

// ---- StaffMember ----

func toStaff(r sqlcgen.TenantStaffMember) *domain.StaffMember {
	return domain.RestoreStaff(domain.StaffState{
		ID: sk.IDFromUUID[domain.StaffTag](r.ID), TenantID: sk.IDFromUUID[sk.TenantTag](r.TenantID), Subject: r.Subject,
		Email: r.Email, DisplayName: r.DisplayName, Role: contracts.Role(r.Role), Version: int(r.Version), CreatedAt: r.CreatedAt.UTC(),
	})
}

func (r *Repo) InsertStaff(ctx context.Context, m *domain.StaffMember) error {
	s := m.State()
	err := r.q.InsertStaff(ctx, sqlcgen.InsertStaffParams{
		ID: s.ID.UUID(), TenantID: s.TenantID.UUID(), Subject: s.Subject, Email: s.Email, DisplayName: s.DisplayName, Role: string(s.Role), CreatedAt: s.CreatedAt,
	})
	if db.IsUniqueViolation(err) {
		return ErrStaffExists
	}
	return err
}

func (r *Repo) GetStaff(ctx context.Context, id domain.StaffID) (*domain.StaffMember, error) {
	row, err := r.q.GetStaff(ctx, id.UUID())
	if err != nil {
		return nil, notFound(err)
	}
	return toStaff(row), nil
}

func (r *Repo) GetStaffBySubject(ctx context.Context, t sk.TenantID, subject string) (*domain.StaffMember, error) {
	row, err := r.q.GetStaffBySubject(ctx, sqlcgen.GetStaffBySubjectParams{TenantID: t.UUID(), Subject: subject})
	if err != nil {
		return nil, notFound(err)
	}
	return toStaff(row), nil
}

func (r *Repo) UpdateStaffRole(ctx context.Context, m *domain.StaffMember, now time.Time) error {
	s := m.State()
	n, err := r.q.UpdateStaffRole(ctx, sqlcgen.UpdateStaffRoleParams{ID: s.ID.UUID(), Role: string(s.Role), UpdatedAt: now, Version: int32(s.Version)})
	if err != nil {
		return fmt.Errorf("tenant: update staff: %w", err)
	}
	if n == 0 {
		return ErrVersionConflict
	}
	return nil
}

func (r *Repo) DeleteStaff(ctx context.Context, id domain.StaffID) error {
	n, err := r.q.DeleteStaff(ctx, id.UUID())
	if err != nil {
		return fmt.Errorf("tenant: delete staff: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) CountOwners(ctx context.Context, t sk.TenantID) (int, error) {
	n, err := r.q.CountOwners(ctx, t.UUID())
	return int(n), err
}

// StaffCursor は一覧の位置（作成順）。
type StaffCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

func (r *Repo) ListStaff(ctx context.Context, t sk.TenantID, after *StaffCursor, limit int) ([]*domain.StaffMember, error) {
	p := sqlcgen.ListStaffParams{TenantID: t.UUID(), Limit: int32(limit)}
	if after != nil {
		p.AfterCreatedAt, p.AfterID = &after.CreatedAt, &after.ID
	}
	rows, err := r.q.ListStaff(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("tenant: list staff: %w", err)
	}
	out := make([]*domain.StaffMember, len(rows))
	for i, row := range rows {
		out[i] = toStaff(row)
	}
	return out, nil
}

func (r *Repo) ListOwners(ctx context.Context, t sk.TenantID) ([]*domain.StaffMember, error) {
	rows, err := r.q.ListOwners(ctx, t.UUID())
	if err != nil {
		return nil, fmt.Errorf("tenant: list owners: %w", err)
	}
	out := make([]*domain.StaffMember, len(rows))
	for i, row := range rows {
		out[i] = toStaff(row)
	}
	return out, nil
}

// ---- ApiKey ----

func staffPtr(u *uuid.UUID) *domain.StaffID {
	if u == nil {
		return nil
	}
	id := sk.IDFromUUID[domain.StaffTag](*u)
	return &id
}

func uuidPtr(id *domain.StaffID) *uuid.UUID {
	if id == nil {
		return nil
	}
	u := id.UUID()
	return &u
}

func toAPIKey(r sqlcgen.TenantApiKey) *domain.APIKey {
	return domain.RestoreAPIKey(domain.APIKeyState{
		ID: sk.IDFromUUID[domain.APIKeyTag](r.ID), TenantID: sk.IDFromUUID[sk.TenantTag](r.TenantID), Name: r.Name, Prefix: r.KeyPrefix,
		Hash: r.KeyHash, CreatedBy: staffPtr(r.CreatedBy), RevokedBy: staffPtr(r.RevokedBy), CreatedAt: r.CreatedAt.UTC(), RevokedAt: utcPtr(r.RevokedAt),
	})
}

func (r *Repo) InsertAPIKey(ctx context.Context, k *domain.APIKey) error {
	s := k.State()
	return r.q.InsertApiKey(ctx, sqlcgen.InsertApiKeyParams{
		ID: s.ID.UUID(), TenantID: s.TenantID.UUID(), Name: s.Name, KeyPrefix: s.Prefix, KeyHash: s.Hash, CreatedBy: uuidPtr(s.CreatedBy), CreatedAt: s.CreatedAt,
	})
}

func (r *Repo) GetAPIKey(ctx context.Context, id domain.APIKeyID) (*domain.APIKey, error) {
	row, err := r.q.GetApiKey(ctx, id.UUID())
	if err != nil {
		return nil, notFound(err)
	}
	return toAPIKey(row), nil
}

func (r *Repo) RevokeAPIKey(ctx context.Context, k *domain.APIKey) error {
	s := k.State()
	_, err := r.q.RevokeApiKey(ctx, sqlcgen.RevokeApiKeyParams{ID: s.ID.UUID(), RevokedAt: s.RevokedAt, RevokedBy: uuidPtr(s.RevokedBy)})
	return err
}

func (r *Repo) CountActiveAPIKeys(ctx context.Context, t sk.TenantID) (int, error) {
	n, err := r.q.CountActiveApiKeys(ctx, t.UUID())
	return int(n), err
}

// APIKeyCursor は一覧の位置（作成日時の新しい順）。
type APIKeyCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

func (r *Repo) ListAPIKeys(ctx context.Context, t sk.TenantID, status *string, after *APIKeyCursor, limit int) ([]*domain.APIKey, error) {
	p := sqlcgen.ListApiKeysParams{TenantID: t.UUID(), Limit: int32(limit), Status: status}
	if after != nil {
		p.AfterCreatedAt, p.AfterID = &after.CreatedAt, &after.ID
	}
	rows, err := r.q.ListApiKeys(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("tenant: list api keys: %w", err)
	}
	out := make([]*domain.APIKey, len(rows))
	for i, row := range rows {
		out[i] = toAPIKey(row)
	}
	return out, nil
}

// KeyLookup は SECURITY DEFINER 関数で引いた認証用の情報。
type KeyLookup struct {
	ID       domain.APIKeyID
	TenantID sk.TenantID
	Hash     []byte
	Revoked  bool
}

// FindAPIKeyByPrefix はテナント未確定のまま prefix でキーを引く（tenant.find_api_key_by_prefix）。
func (r *Repo) FindAPIKeyByPrefix(ctx context.Context, prefix string) (*KeyLookup, error) {
	row, err := r.q.FindApiKeyByPrefix(ctx, prefix)
	if err != nil {
		return nil, notFound(err)
	}
	return &KeyLookup{ID: sk.IDFromUUID[domain.APIKeyTag](row.ID), TenantID: sk.IDFromUUID[sk.TenantTag](row.TenantID), Hash: row.KeyHash, Revoked: row.Revoked}, nil
}
