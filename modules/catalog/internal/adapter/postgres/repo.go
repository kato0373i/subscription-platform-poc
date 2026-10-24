// Package postgres は catalog スキーマのリポジトリ（sqlc の生成コードを包み、domain の型に変換する）。
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/catalog/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/catalog/internal/domain"
	"subscription-platform/modules/catalog/internal/domain/pricing"
)

var (
	ErrNotFound        = errors.New("catalog: row not found")
	ErrVersionConflict = errors.New("catalog: version conflict")
	ErrCodeTaken       = errors.New("catalog: coupon code taken")
	ErrDuplicateActive = errors.New("catalog: customer or subscription already holds a redemption")
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

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func checkRows(n int64, err error, what string) error {
	if err != nil {
		return fmt.Errorf("catalog: %s: %w", what, err)
	}
	if n == 0 {
		return ErrVersionConflict
	}
	return nil
}

// Cursor は一覧の位置（作成順）。
type Cursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

func (c *Cursor) parts() (*time.Time, *uuid.UUID) {
	if c == nil {
		return nil, nil
	}
	return &c.CreatedAt, &c.ID
}

// ---- products ----

func toProduct(r sqlcgen.CatalogProduct) *domain.Product {
	return domain.RestoreProduct(domain.ProductState{
		ID: sk.IDFromUUID[contracts.ProductTag](r.ID), TenantID: sk.IDFromUUID[sk.TenantTag](r.TenantID), Name: r.Name, Description: r.Description,
		Version: int(r.Version), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	})
}

func (r *Repo) InsertProduct(ctx context.Context, p *domain.Product) error {
	s := p.State()
	return r.q.InsertProduct(ctx, sqlcgen.InsertProductParams{ID: s.ID.UUID(), TenantID: s.TenantID.UUID(), Name: s.Name, Description: s.Description, CreatedAt: s.CreatedAt})
}

func (r *Repo) GetProduct(ctx context.Context, id contracts.ProductID) (*domain.Product, error) {
	row, err := r.q.GetProduct(ctx, id.UUID())
	if err != nil {
		return nil, notFound(err)
	}
	return toProduct(row), nil
}

func (r *Repo) UpdateProduct(ctx context.Context, p *domain.Product, now time.Time) error {
	s := p.State()
	n, err := r.q.UpdateProduct(ctx, sqlcgen.UpdateProductParams{ID: s.ID.UUID(), Name: s.Name, Description: s.Description, UpdatedAt: now, Version: int32(s.Version)})
	return checkRows(n, err, "update product")
}

func (r *Repo) ListProducts(ctx context.Context, after *Cursor, limit int) ([]*domain.Product, error) {
	t, id := after.parts()
	rows, err := r.q.ListProducts(ctx, sqlcgen.ListProductsParams{Limit: int32(limit), AfterCreatedAt: t, AfterID: id})
	if err != nil {
		return nil, fmt.Errorf("catalog: list products: %w", err)
	}
	out := make([]*domain.Product, len(rows))
	for i, row := range rows {
		out[i] = toProduct(row)
	}
	return out, nil
}

// ---- plans・price_versions ----

// priceRow は価格版を読む 3 つのクエリの行（sqlc の行の型と同じ形なので変換できる）。
type priceRow struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	PlanID        uuid.UUID
	VersionNo     int32
	PricingScheme string
	Amount        *int64
	SchemeParams  []byte
	TaxRate       string
	EffectiveFrom time.Time
}

func toPrice(r priceRow) (domain.PriceVersion, error) {
	tr, err := sk.ParseTaxRate(r.TaxRate)
	if err != nil {
		return domain.PriceVersion{}, err
	}
	pv := domain.PriceVersion{
		ID: sk.IDFromUUID[contracts.PriceVersionTag](r.ID), VersionNo: int(r.VersionNo), Scheme: pricing.Kind(r.PricingScheme),
		Params: pricing.Params{}, TaxRate: tr, EffectiveFrom: r.EffectiveFrom.UTC(),
	}
	if r.Amount != nil {
		m, err := sk.NewMoney(*r.Amount)
		if err != nil {
			return domain.PriceVersion{}, err
		}
		pv.Amount = &m
	}
	if len(r.SchemeParams) > 0 {
		if err := json.Unmarshal(r.SchemeParams, &pv.Params); err != nil {
			return domain.PriceVersion{}, fmt.Errorf("catalog: scheme params: %w", err)
		}
	}
	return pv, nil
}

func (r *Repo) currentPrice(ctx context.Context, planID uuid.UUID) (domain.PriceVersion, error) {
	row, err := r.q.GetCurrentPriceVersion(ctx, planID)
	if err != nil {
		return domain.PriceVersion{}, fmt.Errorf("catalog: current price of %s: %w", planID, err)
	}
	return toPrice(priceRow(row))
}

func (r *Repo) toPlan(ctx context.Context, row sqlcgen.CatalogPlan) (*domain.Plan, error) {
	pv, err := r.currentPrice(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return domain.RestorePlan(domain.PlanState{
		ID: sk.IDFromUUID[contracts.PlanTag](row.ID), TenantID: sk.IDFromUUID[sk.TenantTag](row.TenantID), ProductID: sk.IDFromUUID[contracts.ProductTag](row.ProductID),
		Name: row.Name, Description: row.Description, Interval: sk.BillingInterval(row.BillingInterval), TrialDays: int(row.TrialDays),
		Scheme: pricing.Kind(row.PricingScheme), Status: contracts.PlanStatus(row.Status), CurrentPrice: pv, PublishedAt: utc(row.PublishedAt),
		Version: int(row.Version), CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}), nil
}

// InsertPlan はプランと v1 の価格版を保存する。
func (r *Repo) InsertPlan(ctx context.Context, p *domain.Plan) error {
	s := p.State()
	err := r.q.InsertPlan(ctx, sqlcgen.InsertPlanParams{
		ID: s.ID.UUID(), TenantID: s.TenantID.UUID(), ProductID: s.ProductID.UUID(), Name: s.Name, Description: s.Description,
		BillingInterval: string(s.Interval), TrialDays: int32(s.TrialDays), PricingScheme: string(s.Scheme), CreatedAt: s.CreatedAt,
	})
	if err != nil {
		return fmt.Errorf("catalog: insert plan: %w", err)
	}
	return r.InsertPriceVersion(ctx, p, s.CurrentPrice)
}

func (r *Repo) InsertPriceVersion(ctx context.Context, p *domain.Plan, pv domain.PriceVersion) error {
	s := p.State()
	params, _ := json.Marshal(pv.Params)
	var amount *int64
	if pv.Amount != nil {
		a := pv.Amount.Int64()
		amount = &a
	}
	return r.q.InsertPriceVersion(ctx, sqlcgen.InsertPriceVersionParams{
		ID: pv.ID.UUID(), TenantID: s.TenantID.UUID(), PlanID: s.ID.UUID(), VersionNo: int32(pv.VersionNo), PricingScheme: string(pv.Scheme),
		Amount: amount, SchemeParams: params, EffectiveFrom: pv.EffectiveFrom, TaxRate: pv.TaxRate.String(),
	})
}

// GetPlan はプランを読む。forUpdate なら行をロックする（同じプランへの変更の直列化。§7.3）。
func (r *Repo) GetPlan(ctx context.Context, id contracts.PlanID, forUpdate bool) (*domain.Plan, error) {
	var row sqlcgen.CatalogPlan
	var err error
	if forUpdate {
		row, err = r.q.GetPlanForUpdate(ctx, id.UUID())
	} else {
		row, err = r.q.GetPlan(ctx, id.UUID())
	}
	if err != nil {
		return nil, notFound(err)
	}
	return r.toPlan(ctx, row)
}

func (r *Repo) UpdatePlan(ctx context.Context, p *domain.Plan, from contracts.PlanStatus, now time.Time) error {
	s := p.State()
	n, err := r.q.UpdatePlan(ctx, sqlcgen.UpdatePlanParams{
		ID: s.ID.UUID(), Name: s.Name, Description: s.Description, TrialDays: int32(s.TrialDays), Status: string(s.Status),
		PublishedAt: s.PublishedAt, UpdatedAt: now, Version: int32(s.Version),
	})
	if err := checkRows(n, err, "update plan"); err != nil {
		return err
	}
	observability.StateChanged(ctx, "plan", s.ID.String(), string(from), string(s.Status), "")
	return nil
}

// PlanFilter は一覧の絞り込み。
type PlanFilter struct {
	ProductID *uuid.UUID
	Status    *string
}

func (r *Repo) ListPlans(ctx context.Context, f PlanFilter, after *Cursor, limit int) ([]*domain.Plan, error) {
	t, id := after.parts()
	rows, err := r.q.ListPlans(ctx, sqlcgen.ListPlansParams{Limit: int32(limit), ProductID: f.ProductID, Status: f.Status, AfterCreatedAt: t, AfterID: id})
	if err != nil {
		return nil, fmt.Errorf("catalog: list plans: %w", err)
	}
	out := make([]*domain.Plan, 0, len(rows))
	for _, row := range rows {
		p, err := r.toPlan(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *Repo) GetPriceVersion(ctx context.Context, id contracts.PriceVersionID) (domain.PriceVersion, error) {
	row, err := r.q.GetPriceVersion(ctx, id.UUID())
	if err != nil {
		return domain.PriceVersion{}, notFound(err)
	}
	return toPrice(priceRow(row))
}

// ListPriceVersions は version_no の降順。beforeNo より小さい版から返す。
func (r *Repo) ListPriceVersions(ctx context.Context, planID contracts.PlanID, beforeNo *int, limit int) ([]domain.PriceVersion, error) {
	var b *int32
	if beforeNo != nil {
		v := int32(*beforeNo)
		b = &v
	}
	rows, err := r.q.ListPriceVersions(ctx, sqlcgen.ListPriceVersionsParams{PlanID: planID.UUID(), Limit: int32(limit), BeforeVersionNo: b})
	if err != nil {
		return nil, fmt.Errorf("catalog: list price versions: %w", err)
	}
	out := make([]domain.PriceVersion, 0, len(rows))
	for _, row := range rows {
		pv, err := toPrice(priceRow(row))
		if err != nil {
			return nil, err
		}
		out = append(out, pv)
	}
	return out, nil
}

// ResolveCheckoutPlanTenant は未認証の公開 API で、プランの事業者を引く（published / unpublished だけ）。なければ ErrNotFound。
func (r *Repo) ResolveCheckoutPlanTenant(ctx context.Context, planID uuid.UUID) (sk.TenantID, error) {
	t, err := r.q.ResolveCheckoutPlanTenant(ctx, planID)
	if err != nil {
		return sk.TenantID{}, err
	}
	if t == uuid.Nil {
		return sk.TenantID{}, ErrNotFound
	}
	return sk.IDFromUUID[sk.TenantTag](t), nil
}

// ---- coupons ----

func (r *Repo) toCoupon(ctx context.Context, row sqlcgen.CatalogCoupon) (*domain.Coupon, error) {
	s := domain.CouponState{
		ID: sk.IDFromUUID[contracts.CouponTag](row.ID), TenantID: sk.IDFromUUID[sk.TenantTag](row.TenantID), Code: row.Code, Name: row.Name,
		Discount:       sk.Discount{Type: sk.DiscountType(row.DiscountType), Value: row.DiscountValue},
		DurationCycles: int(row.DurationCycles), AppliesTo: domain.AppliesTo(row.AppliesTo), ExpiresAt: utc(row.ExpiresAt),
		RedeemedCount: int(row.RedeemedCount), Status: domain.CouponStatus(row.Status), Version: int(row.Version),
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if row.MaxRedemptions != nil {
		m := int(*row.MaxRedemptions)
		s.MaxRedemptions = &m
	}
	if s.AppliesTo == domain.SelectedPlans {
		ids, err := r.q.ListCouponPlanIDs(ctx, row.ID)
		if err != nil {
			return nil, fmt.Errorf("catalog: coupon plans: %w", err)
		}
		for _, id := range ids {
			s.PlanIDs = append(s.PlanIDs, sk.IDFromUUID[contracts.PlanTag](id))
		}
	}
	return domain.RestoreCoupon(s), nil
}

// InsertCoupon はクーポンと対象プランを保存する。同じコードがあれば ErrCodeTaken、対象プランが見えなければ ErrNotFound。
func (r *Repo) InsertCoupon(ctx context.Context, c *domain.Coupon) error {
	s := c.State()
	var maxR *int32
	if s.MaxRedemptions != nil {
		m := int32(*s.MaxRedemptions)
		maxR = &m
	}
	err := r.q.InsertCoupon(ctx, sqlcgen.InsertCouponParams{
		ID: s.ID.UUID(), TenantID: s.TenantID.UUID(), Code: s.Code, Name: s.Name, DiscountType: string(s.Discount.Type), DiscountValue: s.Discount.Value,
		DurationCycles: int16(s.DurationCycles), AppliesTo: string(s.AppliesTo), ExpiresAt: s.ExpiresAt, MaxRedemptions: maxR, CreatedAt: s.CreatedAt,
	})
	if db.IsUniqueViolation(err) {
		return ErrCodeTaken
	}
	if err != nil {
		return fmt.Errorf("catalog: insert coupon: %w", err)
	}
	for _, pid := range s.PlanIDs {
		if err := r.q.InsertCouponPlan(ctx, sqlcgen.InsertCouponPlanParams{TenantID: s.TenantID.UUID(), CouponID: s.ID.UUID(), PlanID: pid.UUID()}); err != nil {
			return fmt.Errorf("catalog: insert coupon plan: %w", err)
		}
	}
	return nil
}

func (r *Repo) GetCoupon(ctx context.Context, id contracts.CouponID) (*domain.Coupon, error) {
	row, err := r.q.GetCoupon(ctx, id.UUID())
	if err != nil {
		return nil, notFound(err)
	}
	return r.toCoupon(ctx, row)
}

func (r *Repo) GetCouponByCode(ctx context.Context, code string) (*domain.Coupon, error) {
	row, err := r.q.GetCouponByCode(ctx, code)
	if err != nil {
		return nil, notFound(err)
	}
	return r.toCoupon(ctx, row)
}

func (r *Repo) ListCoupons(ctx context.Context, status *string, after *Cursor, limit int) ([]*domain.Coupon, error) {
	t, id := after.parts()
	rows, err := r.q.ListCoupons(ctx, sqlcgen.ListCouponsParams{Limit: int32(limit), Status: status, AfterCreatedAt: t, AfterID: id})
	if err != nil {
		return nil, fmt.Errorf("catalog: list coupons: %w", err)
	}
	out := make([]*domain.Coupon, 0, len(rows))
	for _, row := range rows {
		c, err := r.toCoupon(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *Repo) UpdateCoupon(ctx context.Context, c *domain.Coupon, now time.Time) error {
	s := c.State()
	n, err := r.q.UpdateCoupon(ctx, sqlcgen.UpdateCouponParams{ID: s.ID.UUID(), Name: s.Name, Status: string(s.Status), UpdatedAt: now, Version: int32(s.Version)})
	return checkRows(n, err, "update coupon")
}

// ClaimSlot は条件付き UPDATE で枠を 1 つ取る（§9.1）。取れなければ false。
func (r *Repo) ClaimSlot(ctx context.Context, id contracts.CouponID, now time.Time) (bool, error) {
	n, err := r.q.ClaimCouponSlot(ctx, sqlcgen.ClaimCouponSlotParams{ID: id.UUID(), Now: now})
	return n == 1, err
}

// ReclaimSlot は期限切れ後の遅れた確定で、上限だけを見て枠を取り直す（P-CAT-2）。
func (r *Repo) ReclaimSlot(ctx context.Context, id contracts.CouponID, now time.Time) (bool, error) {
	n, err := r.q.ReclaimCouponSlot(ctx, sqlcgen.ReclaimCouponSlotParams{ID: id.UUID(), Now: now})
	return n == 1, err
}

func (r *Repo) ReturnSlot(ctx context.Context, id contracts.CouponID, now time.Time) error {
	return r.q.ReturnCouponSlot(ctx, sqlcgen.ReturnCouponSlotParams{ID: id.UUID(), Now: now})
}

// ---- coupon_redemptions ----

func toRedemption(row sqlcgen.CatalogCouponRedemption) *domain.Redemption {
	s := domain.RedemptionState{
		ID: sk.IDFromUUID[contracts.RedemptionTag](row.ID), TenantID: sk.IDFromUUID[sk.TenantTag](row.TenantID),
		CouponID: sk.IDFromUUID[contracts.CouponTag](row.CouponID), PlanID: sk.IDFromUUID[contracts.PlanTag](row.PlanID),
		CustomerAccountID: sk.IDFromUUID[sk.CustomerAccountTag](row.CustomerAccountID), SubscriptionID: sk.IDFromUUID[sk.SubscriptionTag](row.SubscriptionID),
		Status: domain.RedemptionStatus(row.Status), OverLimit: row.OverLimit, ReservedAt: row.ReservedAt.UTC(),
		ReservationExpiresAt: row.ReservationExpiresAt.UTC(), ConfirmedAt: utc(row.ConfirmedAt), ReleasedAt: utc(row.ReleasedAt), Version: int(row.Version),
	}
	if row.ReleaseReason != nil {
		rr := domain.ReleaseReason(*row.ReleaseReason)
		s.ReleaseReason = &rr
	}
	return domain.RestoreRedemption(s)
}

// Lock は予約の行ロックの種類。
type Lock int

const (
	NoLock Lock = iota
	ForUpdate
	ForUpdateSkipLocked
)

func (r *Repo) GetRedemption(ctx context.Context, id contracts.RedemptionID, lock Lock) (*domain.Redemption, error) {
	var row sqlcgen.CatalogCouponRedemption
	var err error
	switch lock {
	case ForUpdate:
		row, err = r.q.GetRedemptionForUpdate(ctx, id.UUID())
	case ForUpdateSkipLocked:
		row, err = r.q.GetRedemptionForUpdateSkipLocked(ctx, id.UUID())
	case NoLock:
		row, err = r.q.GetRedemption(ctx, id.UUID())
	}
	if err != nil {
		return nil, notFound(err)
	}
	return toRedemption(row), nil
}

func (r *Repo) FindActiveRedemption(ctx context.Context, coupon contracts.CouponID, customer sk.CustomerAccountID, sub sk.SubscriptionID) (*domain.Redemption, error) {
	row, err := r.q.FindActiveRedemption(ctx, sqlcgen.FindActiveRedemptionParams{CouponID: coupon.UUID(), CustomerAccountID: customer.UUID(), SubscriptionID: sub.UUID()})
	if err != nil {
		return nil, notFound(err)
	}
	return toRedemption(row), nil
}

// CustomerHoldsCoupon は契約者がこのクーポンを reserved / confirmed で持っているか（検証 #4）。
func (r *Repo) CustomerHoldsCoupon(ctx context.Context, coupon contracts.CouponID, customer sk.CustomerAccountID) (bool, error) {
	return r.q.CustomerHoldsCoupon(ctx, sqlcgen.CustomerHoldsCouponParams{CouponID: coupon.UUID(), CustomerAccountID: customer.UUID()})
}

// InsertRedemption は予約を保存する。1 人 1 回・1 契約 1 つの部分一意に当たれば ErrDuplicateActive。
func (r *Repo) InsertRedemption(ctx context.Context, red *domain.Redemption) error {
	s := red.State()
	err := r.q.InsertRedemption(ctx, sqlcgen.InsertRedemptionParams{
		ID: s.ID.UUID(), TenantID: s.TenantID.UUID(), CouponID: s.CouponID.UUID(), PlanID: s.PlanID.UUID(),
		CustomerAccountID: s.CustomerAccountID.UUID(), SubscriptionID: s.SubscriptionID.UUID(), ReservedAt: s.ReservedAt, ReservationExpiresAt: s.ReservationExpiresAt,
	})
	if db.IsUniqueViolation(err) {
		return ErrDuplicateActive
	}
	return err
}

func (r *Repo) UpdateRedemption(ctx context.Context, red *domain.Redemption, from domain.RedemptionStatus) error {
	s := red.State()
	var reason *string
	if s.ReleaseReason != nil {
		v := string(*s.ReleaseReason)
		reason = &v
	}
	n, err := r.q.UpdateRedemption(ctx, sqlcgen.UpdateRedemptionParams{
		ID: s.ID.UUID(), Status: string(s.Status), ReleaseReason: reason, OverLimit: s.OverLimit, ConfirmedAt: s.ConfirmedAt, ReleasedAt: s.ReleasedAt, Version: int32(s.Version),
	})
	if err := checkRows(n, err, "update redemption"); err != nil {
		return err
	}
	why := ""
	if reason != nil {
		why = *reason
	}
	observability.StateChanged(ctx, "coupon_redemption", s.ID.String(), string(from), string(s.Status), why)
	return nil
}

// ExpiredReservation は掃除ジョブの対象。
type ExpiredReservation struct {
	TenantID     sk.TenantID
	RedemptionID contracts.RedemptionID
}

// ListExpiredReservations は期限を過ぎた予約を事業者をまたいで引く（SECURITY DEFINER 関数）。
func (r *Repo) ListExpiredReservations(ctx context.Context, now time.Time, limit int) ([]ExpiredReservation, error) {
	rows, err := r.q.ListExpiredReservations(ctx, sqlcgen.ListExpiredReservationsParams{Now: now, Lim: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("catalog: list expired reservations: %w", err)
	}
	out := make([]ExpiredReservation, len(rows))
	for i, row := range rows {
		out[i] = ExpiredReservation{TenantID: sk.IDFromUUID[sk.TenantTag](row.TenantID), RedemptionID: sk.IDFromUUID[contracts.RedemptionTag](row.RedemptionID)}
	}
	return out, nil
}
