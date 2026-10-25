// Package contracts は tenant モジュールが他のモジュールと cmd に公開する部分（identity.md §4.1）。
// 標準ライブラリと sharedkernel だけに依存する。
package contracts

import (
	"context"
	"errors"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// TenantStatus は事業者の状態。
type TenantStatus string

const (
	TenantPendingReview TenantStatus = "pending_review"
	TenantActive        TenantStatus = "active"
	TenantSuspended     TenantStatus = "suspended"
)

// Role はスタッフのロール。
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

// Valid は定義済みのロールか。
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleViewer:
		return true
	}
	return false
}

// Permission は console API の操作の種類（§3.4）。各 console エンドポイントが x-sp-permission で 1 つ宣言する。
type Permission string

const (
	PermConsoleRead         Permission = "console.read"
	PermCatalogWrite        Permission = "catalog.write"
	PermCouponWrite         Permission = "coupon.write"
	PermDunningWrite        Permission = "dunning.write"
	PermSubscriptionOperate Permission = "subscription.operate"
	PermWebhookRedeliver    Permission = "webhook.redeliver"
	PermWebhookManage       Permission = "webhook.manage"
	PermAPIKeyManage        Permission = "apikey.manage"
	PermStaffManage         Permission = "staff.manage"
	PermGoalWrite           Permission = "goal.write" // ダッシュボードの MRR の目標（console-dashboard FR-002）
)

// Permissions は全 Permission（表駆動テストと起動時の検査に使う）。
var Permissions = []Permission{
	PermConsoleRead, PermCatalogWrite, PermCouponWrite, PermDunningWrite, PermSubscriptionOperate,
	PermWebhookRedeliver, PermWebhookManage, PermAPIKeyManage, PermStaffManage, PermGoalWrite,
}

// allowed は §3.4 の表。カスタムロールを入れるときは Allows の実装だけを差し替える（§10）。
var allowed = map[Permission][]Role{
	PermConsoleRead:         {RoleOwner, RoleAdmin, RoleViewer},
	PermCatalogWrite:        {RoleOwner, RoleAdmin},
	PermCouponWrite:         {RoleOwner, RoleAdmin},
	PermDunningWrite:        {RoleOwner, RoleAdmin},
	PermSubscriptionOperate: {RoleOwner, RoleAdmin},
	PermWebhookRedeliver:    {RoleOwner, RoleAdmin},
	PermWebhookManage:       {RoleOwner},
	PermAPIKeyManage:        {RoleOwner},
	PermStaffManage:         {RoleOwner},
	PermGoalWrite:           {RoleOwner, RoleAdmin},
}

// Allows はロールが Permission を持つか（純粋関数）。未知の Permission・ロールは false。
func Allows(r Role, p Permission) bool {
	for _, x := range allowed[p] {
		if x == r {
			return true
		}
	}
	return false
}

// IsWrite は書き込み系か（suspended のテナントでは拒否する。P-IDN-4）。
func (p Permission) IsWrite() bool { return p != PermConsoleRead }

// Known は定義済みの Permission か。
func (p Permission) Known() bool {
	_, ok := allowed[p]
	return ok
}

// TenantInfo は他のモジュールに渡す事業者の情報。
type TenantInfo struct {
	TenantID      sk.TenantID
	Name          string
	Status        TenantStatus
	FincodeShopID string // pending_review のときは ""
}

// OwnerContact は Integration のメール・アラート通知の宛先（P-INT-1）。
type OwnerContact struct {
	StaffID     string
	Email       string
	DisplayName string
}

// Service は tenant モジュールの公開 API。
type Service interface {
	// GetTenant は orchestration・billing・catalog・integration から呼ばれる。なければ ErrTenantNotFound。
	GetTenant(ctx context.Context, tenantID sk.TenantID) (TenantInfo, error)
	// AuthenticateAPIKey は platform/httpx（cmd で配線）から呼ばれる。失効・形式不正・不一致はすべて ErrInvalidAPIKey。
	AuthenticateAPIKey(ctx context.Context, rawKey string) (sk.TenantID, error)
	// GetStaffRole は platform/httpx（cmd で配線）から呼ばれる。所属していなければ ErrStaffNotFound。
	GetStaffRole(ctx context.Context, tenantID sk.TenantID, subject string) (Role, error)
	// ListOwnerContacts は integration から呼ばれる。owner 全員を作成順で返す。テナントがなければ ErrTenantNotFound。
	ListOwnerContacts(ctx context.Context, tenantID sk.TenantID) ([]OwnerContact, error)
	// GetStaffContact は integration から呼ばれる（スタッフ宛ての通知の宛先。business-console §2）。なければ ErrStaffNotFound。
	GetStaffContact(ctx context.Context, tenantID sk.TenantID, staffID string) (OwnerContact, error)
}

var (
	ErrTenantNotFound = errors.New("tenant: not found")
	ErrInvalidAPIKey  = errors.New("tenant: invalid api key")
	ErrStaffNotFound  = errors.New("tenant: staff not found")
)

// イベントの種類（source = sp.tenant）。
const (
	EventTenantApproved  = "tenant.approved"
	EventTenantSuspended = "tenant.suspended"
	// EventStaffAdded は有効な Cognito アカウントの人を事業者に追加した（integration が通知メールを送る。business-console §3）
	EventStaffAdded = "tenant.staff_added"
	// EventStaffInvited は新しく招待した・招待を再送した（拡張口。招待メールは Cognito が送る）
	EventStaffInvited = "tenant.staff_invited"
)

// StaffAddedPayload は tenant.staff_added の payload（メールアドレスは入れない。宛先は GetStaffContact で解決する）。
type StaffAddedPayload struct {
	TenantID sk.TenantID `json:"tenant_id"`
	StaffID  string      `json:"staff_id"`
	Role     Role        `json:"role"`
}

// StaffInvitedPayload は tenant.staff_invited の payload。
type StaffInvitedPayload struct {
	TenantID sk.TenantID `json:"tenant_id"`
	StaffID  string      `json:"staff_id"`
	Resent   bool        `json:"resent"`
}

// TenantApprovedPayload は tenant.approved の payload。
type TenantApprovedPayload struct {
	TenantID      sk.TenantID `json:"tenant_id"`
	FincodeShopID string      `json:"fincode_shop_id"`
	ApprovedAt    time.Time   `json:"approved_at"`
}

// TenantSuspendedPayload は tenant.suspended の payload。
type TenantSuspendedPayload struct {
	TenantID    sk.TenantID `json:"tenant_id"`
	Reason      string      `json:"reason"`
	SuspendedAt time.Time   `json:"suspended_at"`
}
