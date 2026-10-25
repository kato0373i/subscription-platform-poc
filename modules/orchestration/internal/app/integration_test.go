//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/messaging"
	sk "subscription-platform/internal/sharedkernel"
	billingc "subscription-platform/modules/billing/contracts"
	"subscription-platform/modules/orchestration/internal/app"
	subc "subscription-platform/modules/subscription/contracts"
)

var errCrash = errors.New("crash (test hook)")

func renewalDate() sk.Date { return sk.MustDate(2026, 12, 15) }

// toRenewal は更新日の 00:30 JST に進める。
func (w *world) toRenewal() {
	w.clock.Set(renewalDate().AtJST(0, 30))
}

func (w *world) dispatch() {
	w.t.Helper()
	if _, err := w.runJob(w.svc.RenewalDispatchJob(), renewalDate(), 1); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) subStatus(id sk.SubscriptionID) subc.Status {
	w.t.Helper()
	v, err := w.sub.Service().Get(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return v.Status
}

// 申込の通し：201 active、請求 paid（caller_managed）、試行 1 件（invoice:{id}:1）、プロセス completed(paid)、サガ completed。
func TestCheckout_Succeeds(t *testing.T) {
	w := newWorld(t)
	ft, fc := w.newTenant(980, 0), w.newCustomer("ok")
	res := w.checkout(ft, fc)
	if res.Status != 201 || res.Subscription.Status != subc.StatusActive || res.Invoice == nil || res.Invoice.Status != billingc.StatusPaid {
		t.Fatalf("result = %+v", res)
	}
	if n := w.count(`SELECT count(*) FROM payments.payment_attempts WHERE idempotency_key = $1 AND status = 'succeeded'`, "invoice:"+res.Invoice.ID.String()+":1"); n != 1 {
		t.Errorf("attempts = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind = 'signup' AND outcome = 'paid'`, res.Subscription.ID.UUID()); n != 1 {
		t.Errorf("process = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.checkout_sagas WHERE subscription_id = $1 AND status = 'completed' AND result_status = 201`, res.Subscription.ID.UUID()); n != 1 {
		t.Errorf("saga = %d", n)
	}
}

// AC-ORC-014：IssueInvoice の直後に落ちる → 同じキーの再送で、同じ subscription_id・キー invoice:{id}:1 で再開。試行は 1 件。
func TestCheckout_ResumeAfterCrash(t *testing.T) {
	w := newWorld(t)
	ft, fc := w.newTenant(980, 0), w.newCustomer("ok")
	crashed := false
	w.hooks.AfterIssueInvoice = func(context.Context) error {
		if crashed {
			return nil
		}
		crashed = true
		return errCrash
	}
	in := w.checkoutInput(ft, fc, "key-resume")
	if _, err := w.svc.Checkout(context.Background(), in); !errors.Is(err, errCrash) {
		t.Fatalf("first: %v", err)
	}
	res, err := w.svc.Checkout(context.Background(), in)
	if err != nil || res.Status != 201 {
		t.Fatalf("resume: %+v %v", res, err)
	}
	if n := w.count(`SELECT count(*) FROM subscription.subscriptions WHERE customer_account_id = $1`, fc.id.UUID()); n != 1 {
		t.Errorf("subscriptions = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM payments.payment_attempts WHERE idempotency_key = $1`, "invoice:"+res.Invoice.ID.String()+":1"); n != 1 {
		t.Errorf("attempts = %d", n)
	}
	// 同じキー・違う本文は 422 idempotency_key_reused（サガの照合）
	other := in
	other.RequestHash = []byte("different")
	if _, err := w.svc.Checkout(context.Background(), other); !errors.Is(err, app.ErrIdempotencyKeyReused) {
		t.Errorf("reused: %v", err)
	}
}

// AC-ORC-044：申込の途中で止まったプロセスを sweeper が同じキーで再開する。resume_count が 5 なら failed。
func TestSweeper_SignupCollecting(t *testing.T) {
	w := newWorld(t)
	ft, fc := w.newTenant(980, 0), w.newCustomer("ok")
	w.hooks.BeforeSignupCollect = func(context.Context) error { return errCrash }
	if _, err := w.svc.Checkout(context.Background(), w.checkoutInput(ft, fc, "key-sweep")); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.billing_cycle_processes WHERE kind = 'signup' AND state = 'collecting'`); n != 1 {
		t.Fatalf("collecting = %d", n)
	}
	w.hooks.BeforeSignupCollect = nil
	w.clock.Advance(16 * time.Minute)
	if _, err := w.runJob(w.svc.SweeperJob(), w.clock.Today(), 1); err != nil {
		t.Fatal(err)
	}
	w.mustPump()
	if n := w.count(`SELECT count(*) FROM subscription.subscriptions WHERE customer_account_id = $1 AND status = 'active'`, fc.id.UUID()); n != 1 {
		t.Errorf("active = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.billing_cycle_processes WHERE kind = 'signup' AND outcome = 'paid' AND resume_count = 1`); n != 1 {
		t.Errorf("resumed process = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.checkout_sagas WHERE idempotency_key = 'key-sweep' AND status = 'completed'`); n != 1 {
		t.Errorf("saga = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM payments.payment_attempts WHERE customer_account_id = $1`, fc.id.UUID()); n != 1 {
		t.Errorf("attempts = %d", n)
	}

	// 5 回再開しても進まないプロセスは failed
	fc2 := w.newCustomer("ok")
	w.hooks.BeforeSignupCollect = func(context.Context) error { return errCrash }
	if _, err := w.svc.Checkout(context.Background(), w.checkoutInput(ft, fc2, "key-stuck")); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	w.d.AsSuper(t, `UPDATE orchestration.billing_cycle_processes SET resume_count = 5 WHERE state NOT IN ('completed','failed')`)
	w.clock.Advance(16 * time.Minute)
	if _, err := w.runJob(w.svc.SweeperJob(), w.clock.Today(), 1); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.billing_cycle_processes WHERE state = 'failed'`); n != 1 {
		t.Errorf("failed = %d", n)
	}
}

// AC-ORC-014：IssueInvoice の直後に落ちたサガは、sweeper が契約の状態（pending_initial・請求あり）から再開する。
func TestSweeper_StaleSaga(t *testing.T) {
	w := newWorld(t)
	ft, fc := w.newTenant(980, 0), w.newCustomer("ok")
	w.hooks.AfterIssueInvoice = func(context.Context) error { return errCrash }
	if _, err := w.svc.Checkout(context.Background(), w.checkoutInput(ft, fc, "key-saga")); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	w.hooks.AfterIssueInvoice = nil
	w.clock.Advance(6 * time.Minute)
	if _, err := w.runJob(w.svc.SweeperJob(), w.clock.Today(), 1); err != nil {
		t.Fatal(err)
	}
	w.mustPump()
	if n := w.count(`SELECT count(*) FROM orchestration.checkout_sagas WHERE idempotency_key = 'key-saga' AND status = 'completed' AND result_status = 201`); n != 1 {
		t.Errorf("saga = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM subscription.subscriptions WHERE customer_account_id = $1 AND status = 'active'`, fc.id.UUID()); n != 1 {
		t.Errorf("active = %d", n)
	}
}

// AC-ORC-017・019：更新メッセージを 2 回投入しても請求 1・成功した試行 1・プロセス 1。
func TestRenewal_DuplicateMessage(t *testing.T) {
	w := newWorld(t)
	ft, fc := w.newTenant(980, 0), w.newCustomer("ok")
	sub := w.checkout(ft, fc).Subscription
	w.toRenewal()
	w.dispatch()
	w.mu.Lock()
	dup := append([]messaging.FIFOMessage(nil), w.renewals...)
	w.renewals = append(w.renewals, dup...) // 同じ本文を別メッセージとしてもう 1 回（event_id は同じ → Inbox でも弾かれるので、event_id を変えた複製も足す）
	for _, m := range dup {
		m.Env.EventID = sk.NewID[sk.EventTag]()
		w.renewals = append(w.renewals, m)
	}
	w.mu.Unlock()
	w.mustPump()
	if st := w.subStatus(sub.ID); st != subc.StatusActive {
		t.Fatalf("status = %s", st)
	}
	if n := w.count(`SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal' AND collection_mode = 'scheduled' AND status = 'paid'`, sub.ID.UUID()); n != 1 {
		t.Errorf("renewal invoices = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM payments.payment_attempts WHERE customer_account_id = $1 AND status = 'succeeded'`, fc.id.UUID()); n != 2 {
		t.Errorf("succeeded attempts = %d (signup + renewal)", n)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind = 'renewal'`, sub.ID.UUID()); n != 1 {
		t.Errorf("renewal processes = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind = 'renewal' AND outcome = 'paid'`, sub.ID.UUID()); n != 1 {
		t.Errorf("renewal outcome")
	}
}

func dueEnvelope(t *testing.T, tenant sk.TenantID, sub sk.SubscriptionID, inv sk.InvoiceID, seq int) sk.EventEnvelope {
	t.Helper()
	env, err := sk.NewEnvelope(billingc.EventInvoiceCollectionDue, "invoice", inv.UUID(), 9, &tenant, time.Now(), map[string]any{
		"invoice_id": inv, "subscription_id": sub, "tenant_id": tenant, "amount": 980, "collection_seq": seq, "dunning_stage": 0, "reason": "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	env.CorrelationID = "test-" + uuid.NewString()[:8]
	return env
}

// AC-ORC-021・022：同じ seq の再送は試行を増やさない。古い seq は何もしない。
func TestCollection_SameAndStaleSeq(t *testing.T) {
	w := newWorld(t)
	ft, fc := w.newTenant(980, 0), w.newCustomer("ok")
	sub := w.checkout(ft, fc).Subscription
	// 更新の回収は結果不明（timeout_lost）で止める
	if err := w.payments.StubSetBehavior(context.Background(), fc.pm, "timeout_lost", 99); err != nil {
		t.Fatal(err)
	}
	w.toRenewal()
	w.dispatch()
	w.mustPump()
	var inv sk.InvoiceID
	invU := uuid.UUID{}
	_ = w.d.SuperPool(t).QueryRow(context.Background(), `SELECT id FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'`, sub.ID.UUID()).Scan(&invU)
	inv = sk.IDFromUUID[sk.InvoiceTag](invU)
	attempts := func() int64 {
		return w.count(`SELECT count(*) FROM payments.payment_attempts WHERE payable_id = $1`, invU)
	}
	if attempts() != 1 {
		t.Fatalf("attempts = %d", attempts())
	}
	h := w.handlers["orchestration.collection"]
	if err := h(context.Background(), dueEnvelope(t, ft.id, sub.ID, inv, 1)); err != nil {
		t.Fatal(err)
	}
	if attempts() != 1 {
		t.Errorf("same seq: attempts = %d", attempts())
	}
	// seq=2（新しい要求）→ 新しいキーで Collect。結果待ちの試行があるので試行は増えず、プロセスは seq=2 で待つ
	if err := h(context.Background(), dueEnvelope(t, ft.id, sub.ID, inv, 2)); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.billing_cycle_processes WHERE invoice_id = $1 AND collection_seq = 2 AND state = 'awaiting_outcome'`, invU); n != 1 {
		t.Errorf("advanced process = %d", n)
	}
	// 遅れて届いた seq=1 は何もしない
	if err := h(context.Background(), dueEnvelope(t, ft.id, sub.ID, inv, 1)); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.billing_cycle_processes WHERE invoice_id = $1 AND collection_seq = 2`, invU); n != 1 {
		t.Errorf("stale changed the process")
	}
	if attempts() != 1 {
		t.Errorf("attempts = %d", attempts())
	}
}

// AC-ORC-030・046：契約に進行中のプロセスがあるとき、支払方法の変更の即時再決済は 60 秒後に再配信。別 kind のプロセスは作れない。
func TestRecovery_WaitsForInFlight(t *testing.T) {
	w := newWorld(t)
	ft, fc := w.newTenant(980, 0), w.newCustomer("ok")
	sub := w.checkout(ft, fc).Subscription
	if err := w.payments.StubSetBehavior(context.Background(), fc.pm, "decline", 1); err != nil {
		t.Fatal(err)
	}
	w.toRenewal()
	w.dispatch()
	w.mustPump()
	if st := w.subStatus(sub.ID); st != subc.StatusPastDue {
		t.Fatalf("status = %s", st)
	}
	// 進行中のプロセス（retry の回収中）を作る：1 日後の回収をタイムアウトで止める
	if err := w.payments.StubSetBehavior(context.Background(), fc.pm, "timeout_lost", 1); err != nil {
		t.Fatal(err)
	}
	w.clock.Set(renewalDate().AddDays(1).AtJST(10, 0))
	if _, err := w.runJobBilling(); err != nil {
		t.Fatal(err)
	}
	w.mustPump()
	if n := w.count(`SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind = 'retry' AND state = 'awaiting_outcome'`, sub.ID.UUID()); n != 1 {
		t.Fatalf("retry in flight = %d", n)
	}
	env, _ := sk.NewEnvelope(subc.EventPaymentMethodChanged, "subscription", sub.ID.UUID(), 9, &ft.id, time.Now(),
		map[string]any{"subscription_id": sub.ID, "tenant_id": ft.id, "payment_method_id": fc.pm, "status": "past_due"})
	env.CorrelationID = "test-recovery"
	err := w.handlers["orchestration.events"](context.Background(), env)
	if messaging.RetryDelay(err) != 60*time.Second {
		t.Fatalf("err = %v", err)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind = 'recovery'`, sub.ID.UUID()); n != 0 {
		t.Errorf("recovery created = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM payments.payment_attempts WHERE customer_account_id = $1 AND status IN ('processing','unknown','pending_async')`, fc.id.UUID()); n != 1 {
		t.Errorf("in-flight attempts = %d", n)
	}
	// 更新のメッセージも同じ理由で再配信になる
	m := app.RenewalMessage{SubscriptionID: sub.ID, TenantID: ft.id, RunDate: renewalDate().AddDays(30)}
	renv, _ := sk.NewEnvelope(app.EventRenewalRequested, "subscription", sub.ID.UUID(), 1, &ft.id, time.Now(), m)
	renv.CorrelationID = "test-renewal"
	if err := w.handlers["orchestration.renewal"](context.Background(), renv); messaging.RetryDelay(err) != 60*time.Second {
		t.Errorf("renewal: %v", err)
	}
}

func (w *world) runJobBilling() (any, error) {
	for _, j := range w.billing.Jobs() {
		return w.runJob(j, w.clock.Today(), 1)
	}
	return nil, nil
}

// AC-ORC-045：同じ event_id を 2 回配信 → 2 回目は命令が呼ばれず、契約の version が変わらない。
func TestInbox_Duplicate(t *testing.T) {
	w := newWorld(t)
	ft, fc := w.newTenant(980, 0), w.newCustomer("ok")
	sub := w.checkout(ft, fc).Subscription
	env, _ := sk.NewEnvelope(subc.EventCanceled, "subscription", sub.ID.UUID(), 9, &ft.id, time.Now(), map[string]any{"subscription": map[string]any{"subscription_id": sub.ID}})
	env.CorrelationID = "test-dup"
	h := w.handlers["orchestration.events"]
	for range 2 {
		if err := h(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.count(`SELECT count(*) FROM orchestration.processed_messages WHERE message_id = $1`, env.EventID.UUID()); n != 1 {
		t.Errorf("processed = %d", n)
	}
}

// AC-ORC-038：停止中の事業者の pending_initial 契約が照合で成功 → active かつ期間終了で解約（tenant_suspended）。
func TestActivate_SuspendedTenant(t *testing.T) {
	w := newWorld(t)
	ft, fc := w.newTenant(980, 0), w.newCustomer("timeout")
	res, err := w.svc.Checkout(context.Background(), w.checkoutInput(ft, fc, "key-timeout"))
	if err != nil || res.Status != 202 {
		t.Fatalf("checkout: %+v %v", res, err)
	}
	w.mustPump()
	w.d.AsSuper(t, `UPDATE tenant.tenants SET status = 'suspended', suspended_at = now(), suspend_reason = 'test' WHERE id = '`+ft.id.String()+`'`)
	w.clock.Advance(10 * time.Minute)
	if _, err := w.payments.Service().ResolveUnknownAttempts(context.Background(), time.Minute); err != nil {
		t.Fatal(err)
	}
	w.mustPump()
	v, err := w.sub.Service().Get(context.Background(), res.Subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != subc.StatusActive || v.Cancellation == nil || v.Cancellation.Reason != subc.CancelByTenantSuspended {
		t.Fatalf("subscription = %s %+v", v.Status, v.Cancellation)
	}
	// 同じキーの再送は 201（サガの pending_outcome から作り直す）
	again, err := w.svc.Checkout(context.Background(), w.checkoutInput(ft, fc, "key-timeout"))
	if err != nil || again.Status != 201 {
		t.Errorf("replay: %+v %v", again, err)
	}
}

// AC-ORC-035〜037：一括解約。同じ停止を 2 回配信してもジョブは 1 つ。途中で落ちても件数は二重に数えない。
func TestBulkCancellation(t *testing.T) {
	w := newWorld(t)
	ft := w.newTenant(980, 0)
	trialPlan := w.newTenantPlan(ft, 14)
	active := w.checkout(ft, w.newCustomer("ok")).Subscription
	trialing := w.checkoutPlan(ft, trialPlan, w.newCustomer("ok")).Subscription
	pdCustomer := w.newCustomer("ok")
	pastDue := w.checkout(ft, pdCustomer).Subscription
	pending, err := w.svc.Checkout(context.Background(), w.checkoutInput(ft, w.newCustomer("timeout_lost"), "key-pending"))
	if err != nil || pending.Status != 202 {
		t.Fatalf("pending: %v", err)
	}
	// past_due にする（active の契約は更新しない：更新日はすべて同じなので、past_due にする契約だけ decline で落とす）
	if err := w.payments.StubSetBehavior(context.Background(), pdCustomer.pm, "decline", 1); err != nil {
		t.Fatal(err)
	}
	w.toRenewal()
	w.dispatch()
	w.mustPump()
	if st := w.subStatus(pastDue.ID); st != subc.StatusPastDue {
		t.Fatalf("past_due = %s", st)
	}
	w.d.AsSuper(t, `UPDATE tenant.tenants SET status = 'suspended', suspended_at = now(), suspend_reason = 'test' WHERE id = '`+ft.id.String()+`'`)
	env, _ := sk.NewEnvelope("tenant.suspended", "tenant", ft.id.UUID(), 3, &ft.id, time.Now(), map[string]any{"tenant_id": ft.id, "reason": "test", "suspended_at": time.Now()})
	env.CorrelationID = "test-suspend"
	h := w.handlers["orchestration.events"]
	if err := h(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	env2 := env
	env2.EventID = sk.NewID[sk.EventTag]() // Inbox を通らない重複（別メッセージ・同じ停止）。source_event_id が違うのでジョブは running の部分ユニークで 1 つ
	if err := h(context.Background(), env2); err != nil {
		t.Fatal(err)
	}
	if err := h(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.bulk_cancellation_jobs WHERE tenant_id = $1`, ft.id.UUID()); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
	// 2 件目の処理で落ちる → 再実行
	crashed := false
	w.hooks.AfterBulkSubscriber = func(_ context.Context, n int) error {
		if n == 2 && !crashed {
			crashed = true
			return errCrash
		}
		return nil
	}
	if _, err := w.runJob(w.svc.BulkCancellationJob(), w.clock.Today(), 1); !errors.Is(err, errCrash) {
		t.Fatalf("crash: %v", err)
	}
	w.clock.Advance(6 * time.Minute) // リース切れ
	for range 3 {
		if _, err := w.runJob(w.svc.BulkCancellationJob(), w.clock.Today(), 1); err != nil {
			t.Fatal(err)
		}
	}
	w.mustPump()
	var processed, scheduled, canceled, skipped, round int64
	var status string
	_ = w.d.SuperPool(t).QueryRow(context.Background(), `SELECT processed_count, scheduled_count, canceled_count, skipped_count, sweep_round, status FROM orchestration.bulk_cancellation_jobs WHERE tenant_id = $1`, ft.id.UUID()).
		Scan(&processed, &scheduled, &canceled, &skipped, &round, &status)
	if status != "completed" || round != 2 || scheduled != 2 || canceled != 1 || skipped != 1 || processed != 4 {
		t.Errorf("job = %s round=%d processed=%d scheduled=%d canceled=%d skipped=%d", status, round, processed, scheduled, canceled, skipped)
	}
	if st := w.subStatus(pastDue.ID); st != subc.StatusCanceled {
		t.Errorf("past_due → %s", st)
	}
	for _, id := range []sk.SubscriptionID{active.ID, trialing.ID} {
		v, _ := w.sub.Service().Get(context.Background(), id)
		if v.Cancellation == nil || v.Cancellation.Reason != subc.CancelByTenantSuspended {
			t.Errorf("%s: cancellation = %+v", id, v.Cancellation)
		}
	}
	// past_due の請求は subscription.canceled のハンドラで void
	if n := w.count(`SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND status = 'void' AND void_reason = 'subscription_canceled'`, pastDue.ID.UUID()); n != 1 {
		t.Errorf("void = %d", n)
	}
}

func (w *world) newTenantPlan(ft fixtureTenant, trialDays int) sk.PlanID {
	w.t.Helper()
	plan, pv := uuid.New(), uuid.New()
	var prod uuid.UUID
	_ = w.d.SuperPool(w.t).QueryRow(context.Background(), `SELECT product_id FROM catalog.plans WHERE id = $1`, ft.plan.UUID()).Scan(&prod)
	now := w.clock.Now().Add(-24 * time.Hour)
	w.d.AsSuper(w.t, `INSERT INTO catalog.plans (id, tenant_id, product_id, name, billing_interval, trial_days, pricing_scheme, status, published_at, created_at, updated_at) VALUES ('`+
		plan.String()+`', '`+ft.id.String()+`', '`+prod.String()+`', 'TR', 'month', `+itoa(trialDays)+`, 'fixed', 'published', '`+now.Format(time.RFC3339)+`', '`+now.Format(time.RFC3339)+`', '`+now.Format(time.RFC3339)+`')`,
		`INSERT INTO catalog.price_versions (id, tenant_id, plan_id, version_no, pricing_scheme, amount, tax_rate, effective_from, created_at) VALUES ('`+
			pv.String()+`', '`+ft.id.String()+`', '`+plan.String()+`', 1, 'fixed', 980, 0.100, '`+now.Format(time.RFC3339)+`', '`+now.Format(time.RFC3339)+`')`)
	return sk.IDFromUUID[sk.PlanTag](plan)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (w *world) checkoutPlan(ft fixtureTenant, plan sk.PlanID, fc fixtureCustomer) app.CheckoutResult {
	w.t.Helper()
	in := w.checkoutInput(ft, fc, uuid.NewString())
	in.PlanID = plan
	res, err := w.svc.Checkout(context.Background(), in)
	if err != nil {
		w.t.Fatal(err)
	}
	w.mustPump()
	return res
}

// AC-ORC-042：同じ date の BillingRun が dispatching なら rerun は 409。未来の date は検証エラー。
func TestRerun_InProgress(t *testing.T) {
	w := newWorld(t)
	if _, err := w.svc.Rerun(context.Background(), w.clock.Today(), nil, "op"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Rerun(context.Background(), w.clock.Today(), nil, "op"); !errors.Is(err, app.ErrBillingRunInProgress) {
		t.Errorf("second: %v", err)
	}
	if _, err := w.svc.Rerun(context.Background(), w.clock.Today().AddDays(1), nil, "op"); !errors.Is(err, app.ErrInvalidDate) {
		t.Errorf("future: %v", err)
	}
	if len(w.enqueued) != 1 || w.enqueued[0] != app.RenewalDispatchJobName {
		t.Errorf("enqueued = %v", w.enqueued)
	}
}

// AC-ORC-047・048：投入の途中で落ちる（チャンクの投入後・カーソルの保存前）→ 再実行で保存済みのカーソルから再開。
// 重複して投入された契約も請求 1・成功 1。
func TestRenewalDispatch_ResumeFromCursor(t *testing.T) {
	w := newWorld(t, func(d *app.Deps) { d.DispatchPageSize = 5 })
	ft := w.newTenant(980, 0)
	var subs []sk.SubscriptionID
	for range 12 {
		subs = append(subs, w.checkout(ft, w.newCustomer("ok")).Subscription.ID)
	}
	w.toRenewal()
	crashed := false
	w.hooks.AfterDispatchChunk = func(_ context.Context, enqueued int) error {
		if enqueued == 10 && !crashed {
			crashed = true
			return errCrash
		}
		return nil
	}
	if _, err := w.runJob(w.svc.RenewalDispatchJob(), renewalDate(), 1); !errors.Is(err, errCrash) {
		t.Fatalf("crash: %v", err)
	}
	st, err := w.runJob(w.svc.RenewalDispatchJob(), renewalDate(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if st["resumed"] != true {
		t.Errorf("stats = %v", st)
	}
	var enq, resume int64
	var status string
	_ = w.d.SuperPool(t).QueryRow(context.Background(), `SELECT enqueued_count, resume_count, status FROM orchestration.billing_runs WHERE run_date = $1`, renewalDate()).Scan(&enq, &resume, &status)
	if enq != 12 || resume != 1 || status != "dispatched" {
		t.Errorf("run: enqueued=%d resume=%d status=%s", enq, resume, status)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.billing_runs`); n != 1 {
		t.Errorf("runs = %d", n)
	}
	w.mustPump()
	for _, id := range subs {
		if n := w.count(`SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'`, id.UUID()); n != 1 {
			t.Errorf("%s: renewal invoices = %d", id, n)
		}
	}
	if n := w.count(`SELECT count(*) FROM payments.payment_attempts a JOIN billing.invoices i ON i.id = a.payable_id WHERE i.kind = 'renewal' AND a.status = 'succeeded'`); n != 12 {
		t.Errorf("succeeded renewal attempts = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM orchestration.billing_cycle_processes WHERE kind = 'renewal' AND outcome = 'paid'`); n != 12 {
		t.Errorf("paid processes = %d", n)
	}
}
