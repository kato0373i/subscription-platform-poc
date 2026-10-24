//go:build devtools

// Package devseed は devtools ビルドだけの一括シード（platform.md §7.3、P-TST-10）。
// 申込と同じ順で各モジュールの contracts だけを呼んで契約を作る（DB に直接書かない。archtest が contracts 以外の import を禁止する）。
//
// 段階 1（P-IMP-14）：customer → catalog → subscription（CreateForSignup → Activate）を通す。
// 段階 2 の前半（P-IMP-15）：支払方法を payments.RegisterPaymentMethod（StubAdapter のトークン）で登録し、
// 初回の決済を payments.Collect で行う。請求は billing の実装前なので、請求 ID は seed_id から決まる合成 ID
// （冪等キー invoice:{合成 ID}:1）。決済が成功した契約だけ Activate する。billing の実装時に IssueInvoice・RecordPaymentOutcome を挟む。
package devseed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	catalogcontracts "subscription-platform/modules/catalog/contracts"
	customercontracts "subscription-platform/modules/customer/contracts"
	paymentscontracts "subscription-platform/modules/payments/contracts"
	subcontracts "subscription-platform/modules/subscription/contracts"
)

// JobName は一括シードのジョブ（worker-billing が実行する。API は投入するだけ）。
const JobName = "platform-dev-seed-subscriptions"

// Deps は一括シードの依存（すべて contracts）。
type Deps struct {
	Customer     customercontracts.Service
	Catalog      catalogcontracts.Service
	Subscription subcontracts.Service
	Payments     paymentscontracts.Service
	Clock        sk.Clock
	Scheduler    *scheduler.Service
}

// Params はジョブの params。
type Params struct {
	TenantID        sk.TenantID `json:"tenant_id"`
	PlanID          sk.PlanID   `json:"plan_id"`
	Count           int         `json:"count"`
	SeedID          string      `json:"seed_id"`
	PaymentScenario string      `json:"payment_scenario"` // StubAdapter のシナリオ（既定 ok）
}

// Register は POST /v1/dev/seed/subscriptions を登録する（api プロセス）。
func Register(r *httpx.Router, d Deps) error {
	spec, err := openapi.Load("dev/seed.yaml")
	if err != nil {
		return err
	}
	return r.Mount(spec, func(m httpx.ServeMux) {
		m.HandleFunc("POST /v1/dev/seed/subscriptions", func(w http.ResponseWriter, req *http.Request) { seed(w, req, d) })
	})
}

func seed(w http.ResponseWriter, req *http.Request, d Deps) {
	var p Params
	if err := json.NewDecoder(req.Body).Decode(&p); err != nil {
		httpx.WriteProblem(w, req, httpx.Error(errcodes.ValidationFailed, "invalid body"))
		return
	}
	plan, err := d.Catalog.GetPurchasablePlan(req.Context(), p.TenantID, p.PlanID)
	if err != nil {
		httpx.WriteProblem(w, req, httpx.Error(errcodes.ValidationFailed, "plan must be published in the tenant: "+err.Error(), httpx.FieldError{Field: "plan_id", Code: "invalid"}))
		return
	}
	if plan.TrialDays > 0 || plan.Interval != sk.IntervalMonth {
		httpx.WriteProblem(w, req, httpx.Error(errcodes.ValidationFailed, "plan must be monthly without trial", httpx.FieldError{Field: "plan_id", Code: "unsupported"}))
		return
	}
	if p.SeedID == "" {
		p.SeedID = uuid.NewString()[:8]
	}
	if p.PaymentScenario == "" {
		p.PaymentScenario = "ok"
	}
	params, _ := json.Marshal(p)
	res, err := d.Scheduler.Enqueue(req.Context(), JobName, scheduler.EnqueueOptions{Trigger: scheduler.TriggerDev, Params: params, Force: true})
	if errors.Is(err, scheduler.ErrUnknownJob) {
		httpx.WriteProblem(w, req, httpx.Error(errcodes.NotFound, "seed job is not registered"))
		return
	}
	if err != nil {
		httpx.WriteProblem(w, req, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"seed_id": p.SeedID, "run_id": res.RunID})
}

// Job は一括シードのジョブ（worker-billing）。
func Job(d Deps) scheduler.Job { return job{d: d} }

type job struct{ d Deps }

func (job) Name() string { return JobName }

// namespace は合成 ID の名前空間（同じ seed_id の再実行で同じ ID になり、契約が増えない）。
var namespace = uuid.MustParse("6f2d5c8e-6b1a-4c55-9a3e-2f6c1d0b7a11")

func synthetic(seed string, n int, kind string) uuid.UUID {
	return uuid.NewSHA1(namespace, fmt.Appendf(nil, "%s:%d:%s", seed, n, kind))
}

func (j job) Run(ctx context.Context, run scheduler.Run) (scheduler.Stats, error) {
	var p Params
	if err := json.Unmarshal(run.Params, &p); err != nil || p.Count < 1 || p.SeedID == "" {
		return nil, fmt.Errorf("devseed: invalid params: %s", run.Params)
	}
	ctx = observability.WithActor(ctx, sk.Actor{Type: "system", ID: JobName})
	plan, err := j.d.Catalog.GetPurchasablePlan(ctx, p.TenantID, p.PlanID)
	if err != nil {
		return nil, err
	}
	today := j.d.Clock.Today()
	var created, failed int64
	var firstRenewal string
	for n := 1; n <= p.Count; n++ {
		v, err := j.one(ctx, p, plan, n, today)
		if err != nil {
			failed++
			observability.Logger("platform.devseed").LogAttrs(ctx, slog.LevelWarn, "devseed.failed", slog.Int("n", n), observability.Err(err))
			continue
		}
		created++
		if firstRenewal == "" && v.NextRenewalDate != nil {
			firstRenewal = v.NextRenewalDate.String()
		}
	}
	stats := scheduler.Stats{"created": created, "failed": failed}
	if firstRenewal != "" {
		stats["first_renewal_date"] = firstRenewal
	}
	return stats, nil
}

func (j job) one(ctx context.Context, p Params, plan catalogcontracts.PlanSnapshot, n int, today sk.Date) (subcontracts.SubscriptionView, error) {
	sub := fmt.Sprintf("seed:%s:%d", p.SeedID, n)
	yes := true
	acct, err := j.d.Customer.ResolveOrCreateAccount(ctx, customercontracts.AuthIdentity{Subject: sub, Email: fmt.Sprintf("seed-%s-%d@example.invalid", p.SeedID, n), EmailVerified: &yes})
	if err != nil {
		return subcontracts.SubscriptionView{}, err
	}
	cus, err := j.d.Customer.EnsureTenantCustomerID(ctx, p.TenantID, acct.ID)
	if err != nil {
		return subcontracts.SubscriptionView{}, err
	}
	pm, err := j.d.Payments.RegisterPaymentMethod(ctx, paymentscontracts.RegisterPaymentMethodCmd{
		CustomerAccountID: acct.ID, MethodType: paymentscontracts.MethodCard, Provider: "stub", IdempotencyKey: "seed:" + sub,
		Token: fmt.Sprintf("tok_stub_%s_4242_1230_%s", p.PaymentScenario, synthetic(p.SeedID, n, "token").String()[:8]),
	})
	if err != nil {
		return subcontracts.SubscriptionView{}, fmt.Errorf("register payment method: %w", err)
	}
	v, err := j.d.Subscription.CreateForSignup(ctx, subcontracts.CreateSignupCmd{
		SubscriptionID: sk.IDFromUUID[sk.SubscriptionTag](synthetic(p.SeedID, n, "subscription")),
		TenantID:       p.TenantID, CustomerAccountID: acct.ID, TenantCustomerID: string(cus), SignupRequestID: sub,
		Plan:            subcontracts.PlanInput{PlanID: plan.PlanID, ProductID: plan.ProductID, PriceVersionID: plan.PriceVersionID, Interval: plan.Interval, TrialDays: plan.TrialDays},
		PaymentMethodID: pm.ID,
		SignupDate:      today,
	})
	if err != nil {
		return subcontracts.SubscriptionView{}, err
	}
	if v.Status != subcontracts.StatusPendingInitial {
		return v, nil // 再実行（作成済み）
	}
	invoice := sk.IDFromUUID[sk.InvoiceTag](synthetic(p.SeedID, n, "invoice"))
	res, err := j.d.Payments.Collect(ctx, paymentscontracts.CollectCmd{
		TenantID: p.TenantID, CustomerAccountID: acct.ID, Payable: paymentscontracts.PayableRef{Type: paymentscontracts.PayableInvoice, ID: invoice.String()},
		Amount: plan.Amount, PaymentMethodID: pm.ID, IdempotencyKey: paymentscontracts.IdempotencyKeyFor(invoice, 1),
	})
	if err != nil {
		return subcontracts.SubscriptionView{}, fmt.Errorf("collect: %w", err)
	}
	if res.Status != paymentscontracts.AttemptSucceeded {
		return subcontracts.SubscriptionView{}, fmt.Errorf("collect: attempt %s is %s", res.AttemptID, res.Status)
	}
	return j.d.Subscription.Activate(ctx, v.ID, invoice)
}
