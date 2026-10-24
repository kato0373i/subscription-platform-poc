package domain

import (
	"time"

	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
)

// MaxActiveMethods は 1 契約者の active な支払方法の上限。
const MaxActiveMethods = 10

// ProviderRef は決済代行側の参照。TenantShopID が nil なら全事業者で共有する参照。
type ProviderRef struct {
	Provider     string
	TenantShopID *string
	CustomerRef  string
	MethodRef    string
}

// CardDisplay はカードの表示用の情報（カード番号・CVC を持たない。last4 だけ）。
type CardDisplay struct {
	Brand    string `json:"brand"`
	Last4    string `json:"last4"`
	ExpMonth int    `json:"exp_month"`
	ExpYear  int    `json:"exp_year"`
}

// MethodState は PaymentMethod の永続化される状態。
type MethodState struct {
	ID                sk.PaymentMethodID
	CustomerAccountID sk.CustomerAccountID
	MethodType        c.MethodType
	Status            string // active | deleted
	Display           CardDisplay
	Refs              []ProviderRef
	CreatedAt         time.Time
	DeletedAt         *time.Time
	Version           int
}

// PaymentMethod は契約者の支払方法（集約ルート。プラットフォーム全体で持つ）。
type PaymentMethod struct{ s MethodState }

func RestoreMethod(s MethodState) *PaymentMethod { return &PaymentMethod{s: s} }

// NewPaymentMethod は登録する（refs は 1 件以上）。
func NewPaymentMethod(id sk.PaymentMethodID, customer sk.CustomerAccountID, mt c.MethodType, display CardDisplay, ref ProviderRef, now time.Time) *PaymentMethod {
	return &PaymentMethod{s: MethodState{ID: id, CustomerAccountID: customer, MethodType: mt, Status: "active", Display: display, Refs: []ProviderRef{ref}, CreatedAt: now, Version: 1}}
}

func (m *PaymentMethod) State() MethodState { return m.s }
func (m *PaymentMethod) Active() bool       { return m.s.Status == "active" }

func sameShop(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// RefFor は事業者ショップ向けの参照を探し、なければ共有参照（tenant_shop_id = nil）を返す。
func (m *PaymentMethod) RefFor(shop *string) (ProviderRef, bool) {
	for _, r := range m.s.Refs {
		if shop != nil && sameShop(r.TenantShopID, shop) {
			return r, true
		}
	}
	for _, r := range m.s.Refs {
		if r.TenantShopID == nil {
			return r, true
		}
	}
	return ProviderRef{}, false
}

// AddProviderRef は事業者ショップ向けの参照を足す。同じ (provider, shop) があれば false。
func (m *PaymentMethod) AddProviderRef(ref ProviderRef) bool {
	for _, r := range m.s.Refs {
		if r.Provider == ref.Provider && sameShop(r.TenantShopID, ref.TenantShopID) {
			return false
		}
	}
	m.s.Refs = append(m.s.Refs, ref)
	return true
}

// Delete は削除する。削除済みなら false（冪等）。
func (m *PaymentMethod) Delete(now time.Time) bool {
	if !m.Active() {
		return false
	}
	m.s.Status, m.s.DeletedAt = "deleted", &now
	return true
}

// DeleteOnWithdrawal は退会に伴う削除（表示もマスキングする。NFR-015）。
func (m *PaymentMethod) DeleteOnWithdrawal(now time.Time) bool {
	if !m.Delete(now) {
		return false
	}
	m.s.Display = CardDisplay{Brand: "", Last4: "****"}
	return true
}

// Expired は有効期限が業務時刻の月より前か（E-16。登録・決済の可否には使わない）。
func (m *PaymentMethod) Expired(today sk.Date) bool {
	d := m.s.Display
	if d.ExpYear == 0 {
		return false
	}
	return d.ExpYear < today.Year() || (d.ExpYear == today.Year() && d.ExpMonth < int(today.Month()))
}

// View は contracts の形。
func (m *PaymentMethod) View(today sk.Date) c.PaymentMethodView {
	d := m.s.Display
	return c.PaymentMethodView{
		ID: m.s.ID, MethodType: m.s.MethodType, Status: m.s.Status, Expired: m.Expired(today), CreatedAt: m.s.CreatedAt,
		Display: map[string]any{"brand": d.Brand, "last4": d.Last4, "exp_month": d.ExpMonth, "exp_year": d.ExpYear},
	}
}
