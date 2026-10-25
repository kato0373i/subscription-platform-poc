package domain

import (
	"errors"
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

// StaffStatus は事業者への所属の状態（business-console §3）。
type StaffStatus string

const (
	StaffInvited StaffStatus = "invited" // 招待した。初回のパスワード変更の後の最初の要求で active になる
	StaffActive  StaffStatus = "active"
)

// InvitationTTL は招待（Cognito の仮パスワード）の有効期限。期限は Cognito が守り、ここでは表示の判定に使う。
const InvitationTTL = 7 * 24 * time.Hour

// touchInterval は最終利用日時の更新の間隔（読み取りの経路の書き込みを間引く。ADR-C05）。
const touchInterval = time.Hour

// AccountState は招待するメールアドレスの Cognito のアカウントの状態。
type AccountState string

const (
	AccountCreated AccountState = "created" // 今回作った（招待メールを送った）
	AccountPending AccountState = "pending" // 仮パスワードのまま（他の事業者の招待で作られ、まだ初回のログインをしていない）
	AccountActive  AccountState = "active"  // 有効なアカウント
)

// StaffState は StaffMember の永続化される状態。
type StaffState struct {
	ID          StaffID
	TenantID    sk.TenantID
	Subject     string
	Email       string
	DisplayName string
	Role        contracts.Role
	Status      StaffStatus
	InvitedAt   *time.Time
	LastSeenAt  *time.Time
	Version     int
	CreatedAt   time.Time
}

// InvitationExpired は招待中で、招待から 7 日を過ぎているか。
func (s StaffState) InvitationExpired(now time.Time) bool {
	return s.Status == StaffInvited && s.InvitedAt != nil && now.Sub(*s.InvitedAt) > InvitationTTL
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
	return &StaffMember{s: StaffState{ID: id, TenantID: t.ID(), Subject: sub, Email: e, DisplayName: name, Role: role, Status: StaffActive, Version: 1, CreatedAt: now}}, nil
}

// InviteStaffMember は招待で所属を作る。アカウントが有効なら active、作ったばかり・仮パスワードのままなら invited。
func InviteStaffMember(t *Tenant, id StaffID, subject, email, displayName string, role contracts.Role, acct AccountState, now time.Time) (*StaffMember, error) {
	m, err := NewStaffMember(t, id, subject, email, displayName, role, now)
	if err != nil {
		return nil, err
	}
	if acct != AccountActive {
		m.s.Status, m.s.InvitedAt = StaffInvited, &now
	}
	return m, nil
}

// ErrNotInvited は招待中でないスタッフに招待を再送しようとした。
var ErrNotInvited = errors.New("tenant: staff is not invited")

// Activate は invited を active にする。変わったら true。
func (m *StaffMember) Activate() bool {
	if m.s.Status != StaffInvited {
		return false
	}
	m.s.Status = StaffActive
	return true
}

// MarkInvitationResent は招待の再送で期限を延ばす（invited のときだけ）。
func (m *StaffMember) MarkInvitationResent(now time.Time) error {
	if m.s.Status != StaffInvited {
		return ErrNotInvited
	}
	m.s.InvitedAt = &now
	return nil
}

// Touch は最終利用日時を更新する。前回から 1 時間未満なら何もしない。変わったら true。
func (m *StaffMember) Touch(now time.Time) bool {
	if m.s.LastSeenAt != nil && now.Sub(*m.s.LastSeenAt) < touchInterval {
		return false
	}
	m.s.LastSeenAt = &now
	return true
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
	m.s.Version++
	return true, nil
}

// CanRemove は削除できるか。owner で owner が 1 名なら ErrLastOwner。
func (m *StaffMember) CanRemove(ownerCount int) error {
	if m.s.Role == contracts.RoleOwner && ownerCount <= 1 {
		return ErrLastOwner
	}
	return nil
}
