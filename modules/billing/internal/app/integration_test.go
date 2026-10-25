//go:build integration

package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
	"subscription-platform/internal/platform/inbox"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/scheduler"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/billing/contracts"
	billingdb "subscription-platform/modules/billing/db"
	"subscription-platform/modules/billing/internal/adapter/postgres"
	"subscription-platform/modules/billing/internal/app"
)

func TestMain(m *testing.M) {
	pgtest.Main(m, migrate.Source{Schema: "billing", FS: migrate.SubFS(billingdb.Migrations, "migrations")})
}

type fixture struct {
	d     *pgtest.DB
	svc   *app.Service
	clock *sk.ManualClock
}

func setup(t *testing.T) *fixture {
	t.Helper()
	d := pgtest.New(t)
	f := &fixture{d: d, clock: sk.NewManualClock(time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC))}
	f.svc = app.New(app.Deps{DB: d.Pool.Module("billing"), Outbox: outbox.NewWriter("billing", f.clock), Clock: f.clock, Lease: 6 * time.Hour, DuePageSize: 2})
	return f
}

func (f *fixture) count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	return f.d.QueryInt(t, sql, args...)
}

var rate10 = func() sk.TaxRate { r, _ := sk.NewTaxRate(10, 100); return r }()

func newCmd(mode c.CollectionMode) c.IssueInvoiceCmd {
	return c.IssueInvoiceCmd{
		TenantID: sk.NewID[sk.TenantTag](), SubscriptionID: sk.NewID[sk.SubscriptionTag](), CustomerAccountID: sk.NewID[sk.CustomerAccountTag](),
		Period:   sk.BillingPeriod{Start: sk.MustDate(2026, 11, 15), End: sk.MustDate(2026, 12, 14)},
		PlanLine: c.PlanLine{PriceVersionID: sk.NewID[sk.PriceVersionTag](), Amount: sk.MustMoney(980), TaxRate: rate10, Description: "スタンダード"},
		Kind:     c.KindRenewal, CollectionMode: mode,
	}
}

func yes() *bool { b := true; return &b }

// AC-BIL-006・008：同じ cmd の再実行・同時実行で請求 1 件、invoice_issued 1 件。AC-BIL-009：別期間は ErrUnsettledInvoiceExists。
func TestIssueIdempotentAndConcurrent(t *testing.T) {
	f := setup(t)
	cmd := newCmd(c.ModeScheduled)
	var wg sync.WaitGroup
	ids := make([]sk.InvoiceID, 8)
	errs := make([]error, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := f.svc.IssueInvoice(context.Background(), cmd)
			ids[i], errs[i] = v.ID, err
		}()
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil || ids[i] != ids[0] {
			t.Fatalf("%d: id=%s err=%v (want %s)", i, ids[i], errs[i], ids[0])
		}
	}
	again, err := f.svc.IssueInvoice(context.Background(), cmd)
	if err != nil || again.ID != ids[0] {
		t.Fatalf("again: %v %v", again.ID, err)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.invoices`); n != 1 {
		t.Errorf("invoices = %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.outbox_events WHERE event_type = 'billing.invoice_issued'`); n != 1 {
		t.Errorf("issued = %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.invoice_lines`); n != 1 {
		t.Errorf("lines = %d", n)
	}
	diff := cmd
	diff.PlanLine.Amount = sk.MustMoney(990)
	if _, err := f.svc.IssueInvoice(context.Background(), diff); !errors.Is(err, c.ErrIdempotencyConflict) {
		t.Errorf("conflict: %v", err)
	}
	next := cmd
	next.Period = sk.BillingPeriod{Start: sk.MustDate(2026, 12, 15), End: sk.MustDate(2027, 1, 14)}
	if _, err := f.svc.IssueInvoice(context.Background(), next); !errors.Is(err, c.ErrUnsettledInvoiceExists) {
		t.Errorf("unsettled: %v", err)
	}
}

func event(t *testing.T, typ string, tenant sk.TenantID, attempt sk.PaymentAttemptID, inv sk.InvoiceID, amount int64, extra map[string]any) sk.EventEnvelope {
	t.Helper()
	p := map[string]any{"attempt_id": attempt, "payable": map[string]any{"type": "invoice", "id": inv.String()}, "amount": amount, "payment_method_id": uuid.New()}
	for k, v := range extra {
		p[k] = v
	}
	env, err := sk.NewEnvelope(typ, "payment_attempt", attempt.UUID(), 2, &tenant, time.Now(), p)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// AC-BIL-012：同期の反映の後に同じ結果のイベント → paid は 1 回、invoice_paid は 1 件。
func TestSyncThenEvent(t *testing.T) {
	f := setup(t)
	cmd := newCmd(c.ModeCallerManaged)
	v, err := f.svc.IssueInvoice(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	a := sk.NewID[sk.PaymentAttemptTag]()
	got, err := f.svc.RecordPaymentOutcome(context.Background(), v.ID, a, c.PaymentOutcome{Status: c.OutcomeSucceeded, Amount: v.Total, Source: c.SourceSync})
	if err != nil || got.Status != c.StatusPaid {
		t.Fatalf("sync: %v %v", got.Status, err)
	}
	h := inbox.Wrap(f.d.Pool.Module("billing"), "billing.payment_outcome", f.svc.HandlePaymentEvent)
	env := event(t, app.EventPaymentSucceeded, cmd.TenantID, a, v.ID, 980, nil)
	for range 2 {
		if err := h(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	}
	// 別メッセージ（event_id が違う）の同じ結果も =
	if err := h(context.Background(), event(t, app.EventPaymentSucceeded, cmd.TenantID, a, v.ID, 980, nil)); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.outbox_events WHERE event_type = 'billing.invoice_paid'`); n != 1 {
		t.Errorf("paid events = %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.invoice_payment_records`); n != 1 {
		t.Errorf("records = %d", n)
	}
	// 請求が見つからないイベントはエラー（再配信に任せる）、invoice 以外は ack
	if err := h(context.Background(), event(t, app.EventPaymentSucceeded, cmd.TenantID, sk.NewID[sk.PaymentAttemptTag](), sk.NewID[sk.InvoiceTag](), 980, nil)); err == nil {
		t.Error("unknown invoice should fail")
	}
	other := event(t, app.EventPaymentSucceeded, cmd.TenantID, sk.NewID[sk.PaymentAttemptTag](), v.ID, 980, map[string]any{"payable": map[string]any{"type": "order", "id": "x"}})
	if err := h(context.Background(), other); err != nil {
		t.Errorf("non-invoice: %v", err)
	}
	// 結果の記録は追記のみ
	err = f.d.Pool.Module("billing").InTx(db.WithTenantScope(context.Background(), cmd.TenantID), func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE billing.invoice_payment_records SET applied = false`)
		return err
	})
	if err == nil {
		t.Error("UPDATE on invoice_payment_records should be denied")
	}
}

// failed のイベント（retryable 欠落は false）・amount_mismatch の異常。
func TestEventFailedAndAnomaly(t *testing.T) {
	f := setup(t)
	cmd := newCmd(c.ModeScheduled)
	v, _ := f.svc.IssueInvoice(context.Background(), cmd)
	h := inbox.Wrap(f.d.Pool.Module("billing"), "billing.payment_outcome", f.svc.HandlePaymentEvent)
	a := sk.NewID[sk.PaymentAttemptTag]()
	if err := h(context.Background(), event(t, app.EventPaymentFailed, cmd.TenantID, a, v.ID, 980, map[string]any{"failure_code": "card_declined"})); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.invoices WHERE dunning_stage = 1 AND last_failure_retryable = false AND status = 'open'`); n != 1 {
		t.Errorf("failed not applied")
	}
	b := sk.NewID[sk.PaymentAttemptTag]()
	if _, err := f.svc.RecordPaymentOutcome(context.Background(), v.ID, b, c.PaymentOutcome{Status: c.OutcomeSucceeded, Amount: sk.MustMoney(900), Source: c.SourceSync}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.invoice_payment_records WHERE anomaly = 'amount_mismatch' AND applied`); n != 1 {
		t.Errorf("amount_mismatch = %d", n)
	}
	if _, err := f.svc.RecordPaymentOutcome(context.Background(), v.ID, b, c.PaymentOutcome{Status: c.OutcomeFailed, Source: c.SourceSync}); !errors.Is(err, c.ErrValidation) {
		t.Errorf("failed without retryable: %v", err)
	}
}

func (f *fixture) runDue(t *testing.T) scheduler.Stats {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	st, err := f.svc.CollectionDueJob().Run(context.Background(), scheduler.Run{ID: id, Job: app.CollectionDueJobName, Cursors: scheduler.NewCursorStore(f.d.Pool, app.CollectionDueJobName, id)})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// AC-BIL-026：回収時刻ジョブを 2 回続けて実行 → collection_due は 1 件。ページをまたいでも全件を処理する。
func TestCollectionDueJob(t *testing.T) {
	f := setup(t)
	var invs []c.InvoiceView
	for range 5 {
		v, err := f.svc.IssueInvoice(context.Background(), newCmd(c.ModeScheduled))
		if err != nil {
			t.Fatal(err)
		}
		invs = append(invs, v)
	}
	// 作成時の collection_due（seq=1）。結果が届かないままリース切れ → 再送（同じ seq）
	f.clock.Advance(7 * time.Hour)
	if st := f.runDue(t); st["emitted"] != int64(5) {
		t.Fatalf("stats = %v", st)
	}
	if st := f.runDue(t); st["emitted"] != int64(0) {
		t.Fatalf("2 回目はリース中: %v", st)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.outbox_events WHERE event_type = 'billing.invoice_collection_due' AND aggregate_id = $1`, invs[0].ID.UUID()); n != 2 {
		t.Errorf("due events = %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.outbox_events WHERE event_type = 'billing.invoice_collection_due' AND (payload->>'collection_seq')::int <> 1`); n != 0 {
		t.Errorf("resend must keep collection_seq=1: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.invoices WHERE collection_seq = 1 AND attempt_count = 0`); n != 5 {
		t.Errorf("seq/attempt changed: %d", n)
	}
}

// AC-BIL-038（サービス側）：ops の再決済は seq+1、リース中は同じ seq。processing は collection_in_progress、paid は not_collectible。
func TestRequestImmediateCollection(t *testing.T) {
	f := setup(t)
	cmd := newCmd(c.ModeScheduled)
	v, _ := f.svc.IssueInvoice(context.Background(), cmd)
	a := sk.NewID[sk.PaymentAttemptTag]()
	if _, err := f.svc.RecordPaymentOutcome(context.Background(), v.ID, a, c.PaymentOutcome{Status: c.OutcomeFailed, FailureCode: "card_declined", Retryable: yes(), Amount: v.Total, Source: c.SourceSync}); err != nil {
		t.Fatal(err)
	}
	r1, err := f.svc.RequestImmediateCollection(context.Background(), v.ID, c.CollectionRequest{Reason: c.ReasonOps, RequestedBy: "op-1"})
	if err != nil || r1.CollectionSeq != 2 {
		t.Fatalf("ops: %v %v", r1.CollectionSeq, err)
	}
	r2, err := f.svc.RequestImmediateCollection(context.Background(), v.ID, c.CollectionRequest{Reason: c.ReasonImmediate, RequestedBy: "system"})
	if err != nil || r2.CollectionSeq != 2 {
		t.Fatalf("lease: %v %v", r2.CollectionSeq, err)
	}
	b := sk.NewID[sk.PaymentAttemptTag]()
	_, _ = f.svc.RecordPaymentOutcome(context.Background(), v.ID, b, c.PaymentOutcome{Status: c.OutcomePending, Amount: v.Total, Source: c.SourceSync})
	if _, err := f.svc.RequestImmediateCollection(context.Background(), v.ID, c.CollectionRequest{Reason: c.ReasonOps, RequestedBy: "op"}); !errors.Is(err, c.ErrCollectionInProgress) {
		t.Errorf("processing: %v", err)
	}
	_, _ = f.svc.RecordPaymentOutcome(context.Background(), v.ID, b, c.PaymentOutcome{Status: c.OutcomeSucceeded, Amount: v.Total, Source: c.SourceEvent})
	if _, err := f.svc.RequestImmediateCollection(context.Background(), v.ID, c.CollectionRequest{Reason: c.ReasonOps, RequestedBy: "op"}); !errors.Is(err, c.ErrInvoiceNotCollectible) {
		t.Errorf("paid: %v", err)
	}
	if _, err := f.svc.RequestImmediateCollection(context.Background(), sk.NewID[sk.InvoiceTag](), c.CollectionRequest{Reason: c.ReasonOps, RequestedBy: "op"}); !errors.Is(err, c.ErrInvoiceNotFound) {
		t.Errorf("not found: %v", err)
	}
}

// VoidOpenInvoices と GetOpenInvoice。
func TestVoidAndGetOpen(t *testing.T) {
	f := setup(t)
	cmd := newCmd(c.ModeCallerManaged)
	v, _ := f.svc.IssueInvoice(context.Background(), cmd)
	open, err := f.svc.GetOpenInvoice(context.Background(), cmd.SubscriptionID)
	if err != nil || open == nil || open.ID != v.ID {
		t.Fatalf("open: %v %v", open, err)
	}
	voided, err := f.svc.VoidOpenInvoices(context.Background(), cmd.SubscriptionID, c.VoidSignupFailed)
	if err != nil || len(voided) != 1 || voided[0].Status != c.StatusVoid {
		t.Fatalf("void: %v %v", voided, err)
	}
	again, err := f.svc.VoidOpenInvoices(context.Background(), cmd.SubscriptionID, c.VoidSignupFailed)
	if err != nil || len(again) != 0 {
		t.Fatalf("void again: %v %v", again, err)
	}
	if open, _ := f.svc.GetOpenInvoice(context.Background(), cmd.SubscriptionID); open != nil {
		t.Errorf("no open after void")
	}
	none, err := f.svc.VoidOpenInvoices(context.Background(), sk.NewID[sk.SubscriptionTag](), c.VoidSubscriptionCanceled)
	if err != nil || len(none) != 0 {
		t.Errorf("unknown subscription: %v %v", none, err)
	}
}

// AC-BIL-030：他テナントの文脈では見えない。契約者は自分の請求と明細だけ読める。
func TestIsolation(t *testing.T) {
	f := setup(t)
	cmd := newCmd(c.ModeScheduled)
	cmd.Discount = &c.AppliedDiscount{Type: sk.DiscountPercent, Value: 50}
	v, _ := f.svc.IssueInvoice(context.Background(), cmd)
	other := db.WithTenantScope(context.Background(), sk.NewID[sk.TenantTag]())
	if _, err := f.svc.GetInvoiceState(other, v.ID); !errors.Is(err, c.ErrInvoiceNotFound) {
		t.Errorf("other tenant: %v", err)
	}
	if st, err := f.svc.GetInvoiceState(db.WithTenantScope(context.Background(), cmd.TenantID), v.ID); err != nil || len(st.Lines) != 2 {
		t.Errorf("own tenant: %v %v", st.Lines, err)
	}
	mine, _, err := f.svc.ListInvoices(db.WithCustomerScope(context.Background(), cmd.CustomerAccountID), cmd.SubscriptionID, postgres.Page{Limit: 20})
	if err != nil || len(mine) != 1 || len(mine[0].Lines) != 2 {
		t.Fatalf("consumer: %v %v", mine, err)
	}
	theirs, _, _ := f.svc.ListInvoices(db.WithCustomerScope(context.Background(), sk.NewID[sk.CustomerAccountTag]()), cmd.SubscriptionID, postgres.Page{Limit: 20})
	if len(theirs) != 0 {
		t.Errorf("other consumer sees %d", len(theirs))
	}
	pgtest.AssertAllTenantTablesHaveRLS(t, f.d)
}

// DunningPolicy の保存と、最初の失敗で請求に保存されること（DP-5）。
func TestDunningPolicy(t *testing.T) {
	f := setup(t)
	cmd := newCmd(c.ModeScheduled)
	p, err := f.svc.Policy(context.Background(), cmd.TenantID)
	if err != nil || !p.IsDefault || len(p.Offsets) != 3 {
		t.Fatalf("default: %+v %v", p, err)
	}
	if _, err := f.svc.UpdatePolicy(context.Background(), cmd.TenantID, []int{2, 5}, "staff-1"); err != nil {
		t.Fatal(err)
	}
	var pe *c.InvalidDunningPolicyError
	if _, err := f.svc.UpdatePolicy(context.Background(), cmd.TenantID, []int{1, 1}, "staff-1"); !errors.As(err, &pe) {
		t.Errorf("invalid: %v", err)
	}
	v, _ := f.svc.IssueInvoice(context.Background(), cmd)
	got, err := f.svc.RecordPaymentOutcome(context.Background(), v.ID, sk.NewID[sk.PaymentAttemptTag](), c.PaymentOutcome{Status: c.OutcomeFailed, FailureCode: "x", Retryable: yes(), Amount: v.Total, Source: c.SourceSync})
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-15 10:00 JST に失敗 → +2 日の 10:00 JST
	if got.NextCollectionAt == nil || !got.NextCollectionAt.Equal(sk.MustDate(2026, 11, 17).AtJST(10, 0)) {
		t.Errorf("next = %v", got.NextCollectionAt)
	}
	if _, err := f.svc.UpdatePolicy(context.Background(), cmd.TenantID, []int{1}, "staff-1"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.invoices WHERE dunning_schedule->'offsets_days' = '[2,5]'::jsonb AND dunning_schedule->>'policy_source' = 'tenant'`); n != 1 {
		t.Errorf("snapshot changed")
	}
}
