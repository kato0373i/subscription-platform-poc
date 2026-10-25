package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	billing "subscription-platform/modules/billing/contracts"
	catalog "subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/orchestration/internal/adapter/postgres"
	"subscription-platform/modules/orchestration/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/orchestration/internal/domain"
	payments "subscription-platform/modules/payments/contracts"
	subscription "subscription-platform/modules/subscription/contracts"
	tenant "subscription-platform/modules/tenant/contracts"
)

// CheckoutInput は POST /v1/me/subscriptions（§4.1・§7.1）。
type CheckoutInput struct {
	CustomerAccountID sk.CustomerAccountID
	IdempotencyKey    string
	RequestHash       []byte // 本文の SHA-256
	TenantID          sk.TenantID
	PlanID            sk.PlanID
	PaymentMethodID   sk.PaymentMethodID
	CouponCode        string
	ExpectTrial       bool
}

// CheckoutResult は 201 / 202 の応答の元。
type CheckoutResult struct {
	Status       int // 201 | 202
	Subscription subscription.SubscriptionView
	Invoice      *billing.InvoiceView
}

// StoredReplyError は完了済みのサガの 4xx の応答（保存した要約から作り直す）。
type StoredReplyError struct{ Code, Category string }

func (e *StoredReplyError) Error() string { return "orchestration: stored reply: " + e.Code }

// ErrProviderUnavailable は申込の決済で決済代行が使えなかった（補償済み。503。P-PAY-4）。
var ErrProviderUnavailable = payments.ErrProviderUnavailable

// サガの step（§6 の checkout_sagas.step）。
const (
	stepStarted       = "started"
	stepCouponHeld    = "coupon_reserved"
	stepSubscription  = "subscription_created"
	stepInvoiceIssued = "invoice_issued"
	stepDone          = "done"
)

type saga = sqlcgen.OrchestrationCheckoutSaga

func (s *Service) sagaTx(ctx context.Context, fn func(ctx context.Context, q *sqlcgen.Queries) error) error {
	// サガの表は RLS の対象外（主キーに customer_account_id を含めて分離する）。呼び出し元のスコープに関係なく同じロールで触る
	return s.d.DB.InTx(system(ctx, "checkout_saga"), func(ctx context.Context, tx db.Tx) error { return fn(unscoped(ctx), sqlcgen.New(tx)) })
}

func (s *Service) updateSaga(ctx context.Context, g *saga) error {
	g.UpdatedAt = s.now()
	return s.sagaTx(ctx, func(ctx context.Context, q *sqlcgen.Queries) error {
		return q.UpdateSaga(ctx, sqlcgen.UpdateSagaParams{
			CustomerAccountID: g.CustomerAccountID, IdempotencyKey: g.IdempotencyKey, Attempt: g.Attempt, Status: g.Status, Step: g.Step,
			PlanTenantID: g.PlanTenantID, SubscriptionID: g.SubscriptionID, RedemptionID: g.RedemptionID, InvoiceID: g.InvoiceID,
			ResultStatus: g.ResultStatus, ResultCode: g.ResultCode, UpdatedAt: g.UpdatedAt,
		})
	})
}

func (s *Service) finishSaga(ctx context.Context, g *saga, status domain.SagaStatus, result int16, code string) {
	g.Status, g.Step = string(status), stepDone
	if result != 0 {
		g.ResultStatus = &result
	}
	if code != "" {
		g.ResultCode = &code
	}
	if err := s.updateSaga(ctx, g); err != nil {
		warn(ctx, "orchestration.saga_update_failed", err) // 失敗しても sweeper が契約の状態から閉じる
	}
}

// loadSaga は手順 0：サガを作るか、既存のサガをロックして読む。created は新しく作ったか。
func (s *Service) loadSaga(ctx context.Context, in CheckoutInput) (*saga, bool, error) {
	var g saga
	created := false
	err := s.sagaTx(ctx, func(ctx context.Context, q *sqlcgen.Queries) error {
		n, err := q.InsertSaga(ctx, sqlcgen.InsertSagaParams{
			CustomerAccountID: in.CustomerAccountID.UUID(), IdempotencyKey: in.IdempotencyKey, RequestHash: in.RequestHash,
			SubscriptionID: uuid.Must(uuid.NewV7()), CreatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		created = n == 1
		g, err = q.GetSagaForUpdate(ctx, sqlcgen.GetSagaForUpdateParams{CustomerAccountID: in.CustomerAccountID.UUID(), IdempotencyKey: in.IdempotencyKey})
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if !bytes.Equal(g.RequestHash, in.RequestHash) {
		return nil, false, ErrIdempotencyKeyReused
	}
	return &g, created, nil
}

// Checkout は申込（§4.1）。
func (s *Service) Checkout(ctx context.Context, in CheckoutInput) (CheckoutResult, error) {
	ctx = unscoped(ctx)
	g, created, err := s.loadSaga(ctx, in)
	if err != nil {
		return CheckoutResult{}, err
	}
	if !created {
		var sub subscription.SubscriptionView
		var subStatus domain.SubStatus
		if domain.SagaStatus(g.Status) == domain.SagaPendingOutcome {
			if sub, err = s.d.Subscription.Get(ctx, sk.IDFromUUID[sk.SubscriptionTag](g.SubscriptionID)); err != nil {
				return CheckoutResult{}, err
			}
			subStatus = domain.SubStatus(sub.Status)
		}
		switch domain.DecideCheckoutReplay(domain.SagaStatus(g.Status), subStatus) {
		case domain.ReplayResume:
		case domain.ReplayStartOver:
			g.Attempt++
			g.SubscriptionID = uuid.Must(uuid.NewV7())
			g.Status, g.Step, g.RedemptionID, g.InvoiceID, g.ResultStatus, g.ResultCode = string(domain.SagaInProgress), stepStarted, nil, nil, nil, nil
			if err := s.updateSaga(ctx, g); err != nil {
				return CheckoutResult{}, err
			}
		case domain.ReplayRespond202:
			return CheckoutResult{Status: 202, Subscription: sub}, nil
		case domain.ReplayRespond201:
			s.finishSaga(ctx, g, domain.SagaCompleted, 201, "")
			return s.reply201(ctx, sub, g)
		case domain.ReplayDeclined:
			s.finishSaga(ctx, g, domain.SagaCompleted, 422, "payment_declined:generic")
			return CheckoutResult{}, &PaymentDeclinedError{Category: "generic"}
		case domain.ReplayStoredReply:
			return s.storedReply(ctx, g)
		}
	}
	return s.runCheckout(ctx, in, g)
}

func (s *Service) reply201(ctx context.Context, sub subscription.SubscriptionView, g *saga) (CheckoutResult, error) {
	res := CheckoutResult{Status: 201, Subscription: sub}
	if g.InvoiceID != nil {
		inv, err := s.d.Billing.GetInvoice(ctx, sk.IDFromUUID[sk.InvoiceTag](*g.InvoiceID))
		if err != nil {
			return CheckoutResult{}, err
		}
		res.Invoice = &inv
	}
	return res, nil
}

func (s *Service) storedReply(ctx context.Context, g *saga) (CheckoutResult, error) {
	if g.ResultStatus != nil && *g.ResultStatus == 201 {
		sub, err := s.d.Subscription.Get(ctx, sk.IDFromUUID[sk.SubscriptionTag](g.SubscriptionID))
		if err != nil {
			return CheckoutResult{}, err
		}
		return s.reply201(ctx, sub, g)
	}
	code := "payment_declined:generic"
	if g.ResultCode != nil {
		code = *g.ResultCode
	}
	c, cat, _ := strings.Cut(code, ":")
	if c == "payment_declined" {
		return CheckoutResult{}, &PaymentDeclinedError{Category: cat}
	}
	return CheckoutResult{}, &StoredReplyError{Code: c}
}

// codeOf はサガに保存する 4xx の code（再実行の応答を作り直すため）。
func codeOf(err error) string {
	var pd *PaymentDeclinedError
	switch {
	case errors.As(err, &pd):
		return "payment_declined:" + pd.Category
	case errors.Is(err, ErrTenantNotActive):
		return "tenant_not_active"
	case errors.Is(err, ErrPlanUnavailable):
		return "plan_unavailable"
	case errors.Is(err, ErrPaymentMethodUnavailable):
		return "payment_method_unavailable"
	case errors.Is(err, ErrPaymentMethodReentry):
		return "payment_method_reentry_required"
	case errors.Is(err, subscription.ErrTrialNotEligible):
		return "trial_not_eligible"
	case errors.Is(err, catalog.ErrCouponExpired):
		return "coupon_expired"
	case errors.Is(err, catalog.ErrCouponExhausted):
		return "coupon_exhausted"
	case errors.Is(err, catalog.ErrCouponAlreadyUsed):
		return "coupon_already_used"
	case errors.Is(err, catalog.ErrCouponNotApplicable):
		return "coupon_not_applicable"
	case errors.Is(err, catalog.ErrCouponNotFound):
		return "coupon_not_found"
	}
	return ""
}

// rejectBeforeContract は契約を作る前の 4xx。サガを completed にして返す（Platform も 4xx の応答を保存する）。
func (s *Service) rejectBeforeContract(ctx context.Context, g *saga, err error) (CheckoutResult, error) {
	if code := codeOf(err); code != "" {
		s.finishSaga(ctx, g, domain.SagaCompleted, 422, code)
	}
	return CheckoutResult{}, err
}

func shopID(info tenant.TenantInfo) *string {
	if info.FincodeShopID == "" {
		return nil
	}
	return &info.FincodeShopID
}

func (s *Service) runCheckout(ctx context.Context, in CheckoutInput, g *saga) (CheckoutResult, error) {
	subID := sk.IDFromUUID[sk.SubscriptionTag](g.SubscriptionID)
	// 1. 事業者
	info, err := s.d.Tenant.GetTenant(ctx, in.TenantID)
	if errors.Is(err, tenant.ErrTenantNotFound) || (err == nil && info.Status != tenant.TenantActive) {
		return s.rejectBeforeContract(ctx, g, ErrTenantNotActive)
	}
	if err != nil {
		return CheckoutResult{}, err
	}
	// 2. プラン
	plan, err := s.d.Catalog.GetPurchasablePlan(ctx, in.TenantID, in.PlanID)
	if errors.Is(err, catalog.ErrPlanUnavailable) || errors.Is(err, catalog.ErrPlanNotFound) {
		return s.rejectBeforeContract(ctx, g, ErrPlanUnavailable)
	}
	if err != nil {
		return CheckoutResult{}, err
	}
	// 3. 支払方法
	pm, err := s.d.Payments.GetPaymentMethod(ctx, in.CustomerAccountID, in.PaymentMethodID)
	if errors.Is(err, payments.ErrPaymentMethodNotFound) || (err == nil && pm.Status != "active") {
		return s.rejectBeforeContract(ctx, g, ErrPaymentMethodUnavailable)
	}
	if err != nil {
		return CheckoutResult{}, err
	}
	// 4. 事業者別の契約者 ID
	cus, err := s.d.Customer.EnsureTenantCustomerID(ctx, in.TenantID, in.CustomerAccountID)
	if err != nil {
		return CheckoutResult{}, err
	}
	// 5. 事業者ショップ向けの参照
	if err := s.d.Payments.EnsureProviderRef(ctx, payments.EnsureProviderRefCmd{PaymentMethodID: in.PaymentMethodID, TenantID: in.TenantID, TenantShopID: shopID(info)}); err != nil {
		if errors.Is(err, payments.ErrProviderUnavailable) {
			return CheckoutResult{}, err
		}
		return s.rejectBeforeContract(ctx, g, fmt.Errorf("%w: %w", ErrPaymentMethodReentry, err))
	}
	tid := in.TenantID.UUID()
	g.PlanTenantID = &tid
	// 6. クーポンの予約
	var discount *subscription.DiscountInput
	var redemption *sk.RedemptionID
	if in.CouponCode != "" {
		res, err := s.d.Catalog.ReserveCoupon(ctx, in.TenantID, in.CouponCode, in.PlanID, in.CustomerAccountID, subID)
		if err != nil {
			return s.rejectBeforeContract(ctx, g, err)
		}
		discount = &subscription.DiscountInput{CouponID: res.CouponID, RedemptionID: res.RedemptionID, Type: res.Discount.Type, Value: res.Discount.Value, DurationCycles: res.DurationCycles}
		redemption = &res.RedemptionID
		u := res.RedemptionID.UUID()
		g.RedemptionID, g.Step = &u, stepCouponHeld
		if err := s.updateSaga(ctx, g); err != nil {
			return CheckoutResult{}, err
		}
	}
	// 7. 契約の作成
	sub, err := s.d.Subscription.CreateForSignup(ctx, subscription.CreateSignupCmd{
		SubscriptionID: subID, TenantID: in.TenantID, CustomerAccountID: in.CustomerAccountID, TenantCustomerID: string(cus),
		SignupRequestID: fmt.Sprintf("%s#%d", in.IdempotencyKey, g.Attempt),
		Plan:            subscription.PlanInput{PlanID: plan.PlanID, ProductID: plan.ProductID, PriceVersionID: plan.PriceVersionID, Interval: plan.Interval, TrialDays: plan.TrialDays},
		PaymentMethodID: in.PaymentMethodID, Discount: discount, SignupDate: s.d.Clock.Today(), AllowTrialFallback: !in.ExpectTrial,
	})
	if errors.Is(err, subscription.ErrTrialNotEligible) {
		s.releaseCoupon(ctx, in.TenantID, redemption)
		return s.rejectBeforeContract(ctx, g, err)
	}
	if err != nil {
		return CheckoutResult{}, err
	}
	return s.afterContract(ctx, g, sub, info, redemption)
}

// afterContract は契約の作成後（手順 8〜10）。再開でも同じ（契約の状態で分岐する）。
func (s *Service) afterContract(ctx context.Context, g *saga, sub subscription.SubscriptionView, info tenant.TenantInfo, redemption *sk.RedemptionID) (CheckoutResult, error) {
	switch sub.Status {
	case subscription.StatusTrialing, subscription.StatusActive, subscription.StatusPastDue:
		// 8a. トライアル：この時点でクーポンを確定（P-ORC-4）。active は再開（確定は冪等）
		if err := s.confirmCoupon(ctx, sub.TenantID, redemption); err != nil {
			return CheckoutResult{}, err
		}
		s.finishSaga(ctx, g, domain.SagaCompleted, 201, "")
		return s.reply201(ctx, sub, g)
	case subscription.StatusSignupFailed, subscription.StatusCanceled:
		s.compensate(ctx, sub.ID, sub.TenantID, redemption)
		s.finishSaga(ctx, g, domain.SagaCompleted, 422, "payment_declined:generic")
		return CheckoutResult{}, &PaymentDeclinedError{Category: "generic"}
	case subscription.StatusPendingInitial:
	}
	// 8b. 初回の請求
	g.Step = stepSubscription
	inv, err := s.issueInitialInvoice(ctx, sub)
	if err != nil {
		return CheckoutResult{}, err
	}
	u := inv.ID.UUID()
	g.InvoiceID, g.Step = &u, stepInvoiceIssued
	if err := s.updateSaga(ctx, g); err != nil {
		return CheckoutResult{}, err
	}
	if h := s.d.Hooks.AfterIssueInvoice; h != nil {
		if err := h(ctx); err != nil {
			return CheckoutResult{}, err
		}
	}
	out, err := s.collectSignup(ctx, sub, inv, info, redemption)
	if err != nil {
		return CheckoutResult{}, err
	}
	switch out.kind {
	case signupPaid:
		s.finishSaga(ctx, g, domain.SagaCompleted, 201, "")
		return CheckoutResult{Status: 201, Subscription: out.sub, Invoice: out.inv}, nil
	case signupDeclined:
		s.finishSaga(ctx, g, domain.SagaCompleted, 422, "payment_declined:"+out.category)
		return CheckoutResult{}, &PaymentDeclinedError{Category: out.category}
	case signupProviderDown:
		s.finishSaga(ctx, g, domain.SagaAborted, 0, "")
		return CheckoutResult{}, ErrProviderUnavailable
	default:
		g.Status = string(domain.SagaPendingOutcome)
		if err := s.updateSaga(ctx, g); err != nil {
			return CheckoutResult{}, err
		}
		return CheckoutResult{Status: 202, Subscription: out.sub}, nil
	}
}

// issueInitialInvoice は初回の請求（caller_managed。金額は QuotePrice）。既にあれば同じ請求が返る（Billing が冪等）。
func (s *Service) issueInitialInvoice(ctx context.Context, sub subscription.SubscriptionView) (billing.InvoiceView, error) {
	terms := sub.InitialTerms
	if terms == nil {
		return billing.InvoiceView{}, fmt.Errorf("orchestration: pending_initial subscription %s has no initial terms", sub.ID)
	}
	q, err := s.d.Catalog.QuotePrice(ctx, sub.TenantID, terms.PriceVersionID, nil)
	if err != nil {
		return billing.InvoiceView{}, err
	}
	return s.d.Billing.IssueInvoice(ctx, billing.IssueInvoiceCmd{
		TenantID: sub.TenantID, SubscriptionID: sub.ID, CustomerAccountID: sub.CustomerAccountID, Period: terms.Period,
		PlanLine: billing.PlanLine{PriceVersionID: terms.PriceVersionID, Amount: q.Amount, TaxRate: q.TaxRate},
		Discount: appliedDiscount(terms.Discount), Kind: billing.KindInitial, CollectionMode: billing.ModeCallerManaged,
	})
}

func appliedDiscount(d *subscription.AppliedDiscount) *billing.AppliedDiscount {
	if d == nil {
		return nil
	}
	r := d.RedemptionID
	return &billing.AppliedDiscount{Type: d.Type, Value: d.Value, CouponRedemptionID: &r}
}

type signupKind int

const (
	signupPaid signupKind = iota
	signupDeclined
	signupPending
	signupProviderDown
)

type signupOutcome struct {
	kind     signupKind
	sub      subscription.SubscriptionView
	inv      *billing.InvoiceView
	category string
}

// signupProcess は申込のプロセスを作るか、既存の進行中のものを使う。
func (s *Service) signupProcess(ctx context.Context, sub subscription.SubscriptionView, inv billing.InvoiceView) (*postgres.Process, error) {
	id := inv.ID
	p := &postgres.Process{TenantID: sub.TenantID, SubscriptionID: sub.ID, InvoiceID: &id, Kind: domain.KindSignup, State: domain.StateInvoiceIssued, CorrelationID: correlation(ctx)}
	existing, err := s.insertProcess(ctx, p)
	if errors.Is(err, errActiveProcessOnSubscription) && existing != nil {
		return existing, nil
	}
	return p, err
}

// collectSignup は初回の決済と結果の反映（手順 9・10、C-02）。sweeper の再開からも呼ぶ。
func (s *Service) collectSignup(ctx context.Context, sub subscription.SubscriptionView, inv billing.InvoiceView, info tenant.TenantInfo, redemption *sk.RedemptionID) (signupOutcome, error) {
	p, err := s.signupProcess(ctx, sub, inv)
	if err != nil {
		return signupOutcome{}, err
	}
	if inv.Status == billing.StatusPaid { // 0 円（決済しない）
		return s.signupSucceeded(ctx, p, sub, inv, nil, redemption)
	}
	seq := inv.CollectionSeq
	key, err := domain.CollectKey(inv.ID, seq)
	if err != nil {
		return signupOutcome{}, err
	}
	p.InvoiceID, p.State, p.CollectionSeq = &inv.ID, domain.StateCollecting, &seq
	if err := s.save(ctx, p); err != nil {
		return signupOutcome{}, err
	}
	if h := s.d.Hooks.BeforeSignupCollect; h != nil {
		if err := h(ctx); err != nil {
			return signupOutcome{}, err
		}
	}
	res, err := s.d.Payments.Collect(ctx, payments.CollectCmd{
		TenantID: sub.TenantID, TenantShopID: shopID(info), CustomerAccountID: sub.CustomerAccountID,
		Payable: payments.PayableRef{Type: payments.PayableInvoice, ID: inv.ID.String()}, Amount: inv.Total, PaymentMethodID: sub.PaymentMethodID, IdempotencyKey: key,
	})
	var inProgress *payments.AttemptInProgressError
	var alreadyPaid *payments.PayableAlreadyPaidError
	switch {
	case errors.As(err, &alreadyPaid):
		res = payments.AttemptResult{AttemptID: alreadyPaid.AttemptID, Status: payments.AttemptSucceeded}
	case errors.As(err, &inProgress):
		res = payments.AttemptResult{AttemptID: inProgress.AttemptID, Status: payments.AttemptProcessing}
	case errors.Is(err, payments.ErrProviderUnavailable):
		p.AttemptStatus = strp("provider_unavailable")
		s.compensateProcess(ctx, p, sub.ID, sub.TenantID, redemption)
		return signupOutcome{kind: signupProviderDown}, nil
	case err != nil:
		p.LastError = errString(err)
		_ = s.save(ctx, p)
		return signupOutcome{}, err // 500（Platform は保存しない。同じキーの再送で手順 10 から再開）
	}
	a := res.AttemptID.UUID()
	p.AttemptID, p.AttemptStatus = &a, strp(string(res.Status))
	outcome := billing.PaymentOutcome{Amount: inv.Total, Source: billing.SourceSync}
	switch res.Status {
	case payments.AttemptSucceeded:
		outcome.Status = billing.OutcomeSucceeded
	case payments.AttemptFailed:
		outcome.Status, outcome.Retryable = billing.OutcomeFailed, res.Retryable
		if outcome.Retryable == nil {
			f := false
			outcome.Retryable = &f
		}
		if res.FailureCode != nil {
			outcome.FailureCode = *res.FailureCode
		}
	case payments.AttemptPendingAsync:
		outcome.Status = billing.OutcomePending
	case payments.AttemptProcessing, payments.AttemptUnknown:
		outcome.Status = billing.OutcomeUnknown
	}
	view, err := s.d.Billing.RecordPaymentOutcome(ctx, inv.ID, res.AttemptID, outcome)
	if err != nil {
		return signupOutcome{}, err
	}
	switch outcome.Status {
	case billing.OutcomeSucceeded:
		return s.signupSucceeded(ctx, p, sub, view, &res.AttemptID, redemption)
	case billing.OutcomeFailed:
		s.compensateProcess(ctx, p, sub.ID, sub.TenantID, redemption)
		cat := "generic"
		if res.ConsumerCategory != nil {
			cat = *res.ConsumerCategory
		}
		return signupOutcome{kind: signupDeclined, category: cat}, nil
	case billing.OutcomePending, billing.OutcomeUnknown:
	}
	p.State = domain.StateAwaitingOutcome
	if err := s.save(ctx, p); err != nil && !errors.Is(err, postgres.ErrVersionConflict) {
		return signupOutcome{}, err
	}
	return signupOutcome{kind: signupPending, sub: sub}, nil
}

// signupSucceeded は Activate → クーポンの確定 → プロセスを閉じる。
func (s *Service) signupSucceeded(ctx context.Context, p *postgres.Process, sub subscription.SubscriptionView, inv billing.InvoiceView, _ *sk.PaymentAttemptID, redemption *sk.RedemptionID) (signupOutcome, error) {
	v, err := conflictRetry(ctx, func() (subscription.SubscriptionView, error) { return s.d.Subscription.Activate(ctx, sub.ID, inv.ID) })
	if invalidTransition(err) {
		v, err = s.d.Subscription.Get(ctx, sub.ID)
	}
	if err != nil {
		return signupOutcome{}, err
	}
	if err := s.confirmCoupon(ctx, sub.TenantID, redemption); err != nil {
		return signupOutcome{}, err
	}
	if err := s.complete(ctx, p, domain.OutcomePaid); err != nil {
		return signupOutcome{}, err
	}
	return signupOutcome{kind: signupPaid, sub: v, inv: &inv}, nil
}

// compensateProcess は compensating にしてから補償し、signup_failed で閉じる（途中で落ちたら sweeper が残りを実行する）。
func (s *Service) compensateProcess(ctx context.Context, p *postgres.Process, sub sk.SubscriptionID, t sk.TenantID, redemption *sk.RedemptionID) {
	p.State = domain.StateCompensating
	if err := s.save(ctx, p); err != nil {
		warn(ctx, "orchestration.process_save_failed", err)
	}
	s.compensate(ctx, sub, t, redemption)
	if err := s.complete(ctx, p, domain.OutcomeSignupFailed); err != nil {
		warn(ctx, "orchestration.process_save_failed", err)
	}
}

// compensate は申込の不成立の補償：MarkSignupFailed → VoidOpenInvoices(signup_failed) → ReleaseCouponRedemption（すべて冪等）。
func (s *Service) compensate(ctx context.Context, sub sk.SubscriptionID, t sk.TenantID, redemption *sk.RedemptionID) {
	if err := s.d.Subscription.MarkSignupFailed(ctx, sub, "payment_failed"); err != nil && !invalidTransition(err) {
		warn(ctx, "orchestration.mark_signup_failed", err, slog.String("subscription_id", sub.String()))
	}
	if _, err := s.d.Billing.VoidOpenInvoices(ctx, sub, billing.VoidSignupFailed); err != nil {
		warn(ctx, "orchestration.void_failed", err, slog.String("subscription_id", sub.String()))
	}
	s.releaseCoupon(ctx, t, redemption)
}

func (s *Service) confirmCoupon(ctx context.Context, t sk.TenantID, redemption *sk.RedemptionID) error {
	if redemption == nil {
		return nil
	}
	err := s.d.Catalog.ConfirmCouponRedemption(ctx, t, *redemption)
	if errors.Is(err, catalog.ErrRedemptionReleased) {
		// 掃除ジョブが先に解放していた（結果不明が長引いた場合）。決済は成功しているので利用は続け、運用に知らせる
		alert(ctx, "orchestration.coupon_released_before_confirm", slog.String("redemption_id", redemption.String()))
		return nil
	}
	return err
}

func (s *Service) releaseCoupon(ctx context.Context, t sk.TenantID, redemption *sk.RedemptionID) {
	if redemption == nil {
		return
	}
	if err := s.d.Catalog.ReleaseCouponRedemption(ctx, t, *redemption); err != nil && !errors.Is(err, catalog.ErrRedemptionAlreadyConfirmed) {
		warn(ctx, "orchestration.coupon_release_failed", err, slog.String("redemption_id", redemption.String()))
	}
}

// subscriptionRedemption は契約に適用中のクーポンの予約（再開・イベントで使う）。
func subscriptionRedemption(v subscription.SubscriptionView) *sk.RedemptionID {
	if v.AppliedDiscount == nil {
		return nil
	}
	r := v.AppliedDiscount.RedemptionID
	return &r
}

// sagaByKey はテストと調査用。
func (s *Service) sagaByKey(ctx context.Context, cus sk.CustomerAccountID, key string) (*saga, error) {
	var g saga
	err := s.sagaTx(ctx, func(ctx context.Context, q *sqlcgen.Queries) error {
		var err error
		g, err = q.GetSaga(ctx, sqlcgen.GetSagaParams{CustomerAccountID: cus.UUID(), IdempotencyKey: key})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &g, err
}
