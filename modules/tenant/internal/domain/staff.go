package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/tenant/contracts"
)

// StaffTag は StaffMember の ID の型。
type StaffTag struct{}

// StaffID はスタッフの ID。
type StaffID = sk.ID[StaffTag]

// StaffState は StaffMember の永続化される状態。
type StaffState struct {
	ID          StaffID
	TenantID    sk.TenantID
	Subject     string
	Email       string
	DisplayName string
	Role        contracts.Role
	Version     int
	CreatedAt   time.Time
}

// StaffMember は事業者のスタッフ（集約ルート）。
type StaffMember struct{ s StaffState }

// RestoreStaff は保存済みの状態から復元する。
func RestoreStaff(s StaffState) *StaffMember { return &StaffMember{s: s} }

// NormalizeEmail は小文字にし、最大 254 文字・@ を含むことを確かめる。
func NormalizeEmail(field, s string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(s))
	switch {
	case e == "":
		return "", invalid(field, "required")
	case utf8.RuneCountInString(e) > 254:
		return "", invalid(field, "too_long")
	case !strings.Contains(e, "@") || strings.HasPrefix(e, "@") || strings.HasSuffix(e, "@"):
		return "", invalid(field, "invalid")
	}
	return e, nil
}

// NewStaffMember はスタッフを作る。テナントが suspended なら ErrTenantNotWritable。
func NewStaffMember(t *Tenant, id StaffID, subject, email, displayName string, role contracts.Role, now time.Time) (*StaffMember, error) {
	if !t.AllowsStaffWrite() {
		return nil, ErrTenantNotWritable
	}
	sub := strings.TrimSpace(subject)
	if sub == "" {
		return nil, invalid("subject", "required")
	}
	if utf8.RuneCountInString(sub) > 255 {
		return nil, invalid("subject", "too_long")
	}
	e, err := NormalizeEmail("email", email)
	if err != nil {
		return nil, err
	}
	name, err := textInRange("display_name", displayName, 100)
	if err != nil {
		return nil, err
	}
	if !role.Valid() {
		return nil, invalid("role", "invalid")
	}
	return &StaffMember{s: StaffState{ID: id, TenantID: t.ID(), Subject: sub, Email: e, DisplayName: name, Role: role, Version: 1, CreatedAt: now}}, nil
}

func (m *StaffMember) State() StaffState    { return m.s }
func (m *StaffMember) ID() StaffID          { return m.s.ID }
func (m *StaffMember) Role() contracts.Role { return m.s.Role }

// ChangeRole はロールを変える。owner を他のロールにするとき、owner が 1 名なら ErrLastOwner。同じロールなら false。
// ownerCount はテナント行をロックした状態で数えた値（OwnerGuard。E1）。
func (m *StaffMember) ChangeRole(newRole contracts.Role, ownerCount int) (changed bool, err error) {
	if !newRole.Valid() {
		return false, invalid("role", "invalid")
	}
	if newRole == m.s.Role {
		return false, nil
	}
	if m.s.Role == contracts.RoleOwner && ownerCount <= 1 {
		return false, ErrLastOwner
	}
	m.s.Role = newRole
	return true, nil
}

// CanRemove は削除できるか。owner で owner が 1 名なら ErrLastOwner。
func (m *StaffMember) CanRemove(ownerCount int) error {
	if m.s.Role == contracts.RoleOwner && ownerCount <= 1 {
		return ErrLastOwner
	}
	return nil
}
