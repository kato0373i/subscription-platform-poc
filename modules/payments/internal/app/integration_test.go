//go:build integration

package app_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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
	c "subscription-platform/modules/payments/contracts"
	paymentsdb "subscription-platform/modules/payments/db"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/adapter/provider/stub"
	"subscription-platform/modules/payments/internal/app"
)

func TestMain(m *testing.M) {
	pgtest.Main(m,
		migrate.Source{Schema: "payments", FS: migrate.SubFS(paymentsdb.Migrations, "migrations")},
		migrate.Source{Schema: "payments", Table: "payments.goose_devtools_version", FS: migrate.SubFS(paymentsdb.DevtoolsMigrations, "migrations_devtools")},
	)
}

type fixture struct {
	d     *pgtest.DB
	svc   *app.Service
	stub  *stub.Adapter
	clock *sk.ManualClock
	mu    sync.Mutex
	kicks []string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	d := pgtest.New(t)
	f := &fixture{d: d, clock: sk.NewManualClock(time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC))}
	f.svc, f.stub = f.newService(d.Pool)
	return f
}

// newService は同じ DB に対して別のプールの Service を作れる（api と worker-billing を想定。AC-PAY-027）。
func (f *fixture) newService(pool *db.Pool) (*app.Service, *stub.Adapter) {
	mdb := pool.Module("payments")
	reg, _ := provider.NewRegistry("card:stub")
	reg.WithDecorator(provider.NewGuard(mdb, f.clock, provider.BreakerConfig{}).Wrap)
	st := stub.New("stub", mdb, f.clock, "secret")
	reg.Register(st)
	svc := app.New(app.Deps{DB: mdb, Outbox: outbox.NewWriter("payments", f.clock), Clock: f.clock, Registry: reg, ReconcilePageSize: 10,
		SendProviderEvent: func(_ context.Context, body string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.kicks = append(f.kicks, body)
			return nil
		}})
	return svc, st
}

func token(scenario string) string {
	return fmt.Sprintf("tok_stub_%s_4242_1228_%08x", scenario, rand.Uint32()) //nolint:gosec // テスト用の乱数
}

func (f *fixture) method(t *testing.T, customer sk.CustomerAccountID, scenario string) sk.PaymentMethodID {
	t.Helper()
	v, err := f.svc.RegisterPaymentMethod(context.Background(), c.RegisterPaymentMethodCmd{CustomerAccountID: customer, MethodType: c.MethodCard, Provider: "stub", Token: token(scenario)})
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}

type collectOpt func(*c.CollectCmd)

func (f *fixture) cmd(tenant sk.TenantID, customer sk.CustomerAccountID, pm sk.PaymentMethodID, inv sk.InvoiceID, seq int, opts ...collectOpt) c.CollectCmd {
	cmd := c.CollectCmd{
		TenantID: tenant, CustomerAccountID: customer, Payable: c.PayableRef{Type: c.PayableInvoice, ID: inv.String()},
		Amount: sk.MustMoney(980), PaymentMethodID: pm, IdempotencyKey: c.IdempotencyKeyFor(inv, seq),
	}
	for _, o := range opts {
		o(&cmd)
	}
	return cmd
}

func (f *fixture) count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	return f.d.QueryInt(t, sql, args...)
}

func ids() (sk.TenantID, sk.CustomerAccountID, sk.InvoiceID) {
	return sk.NewID[sk.TenantTag](), sk.NewID[sk.CustomerAccountTag](), sk.NewID[sk.InvoiceTag]()
}

// AC-PAY-011: 同じキーで 10 並列 → 試行 1 件、stub_charges 1 件、全員が同じ attempt_id
func TestSameKeyParallel(t *testing.T) {
	f := setup(t)
	tenant, customer, inv := ids()
	pm := f.method(t, customer, "ok")
	var wg sync.WaitGroup
	results := make([]c.AttemptResult, 10)
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := f.svc.Collect(context.Background(), f.cmd(tenant, customer, pm, inv, 1))
			if err != nil {
				t.Error(err)
			}
			results[i] = r
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r.AttemptID != results[0].AttemptID {
			t.Fatalf("attempt_id がそろわない: %v", results)
		}
	}
	if f.count(t, `SELECT count(*) FROM payments.payment_attempts`) != 1 || f.count(t, `SELECT count(*) FROM payments.stub_charges`) != 1 {
		t.Error("試行・stub_charges が 1 件でない")
	}
	if n := f.count(t, `SELECT count(*) FROM payments.outbox_events WHERE event_type = 'payments.payment_succeeded'`); n != 1 {
		t.Errorf("payment_succeeded = %d", n)
	}
}

// AC-PAY-012・013・014・018
func TestBlockingAttempts(t *testing.T) {
	f := setup(t)
	tenant, customer, inv := ids()
	pending := f.method(t, customer, "async_ok") // 確定待ちのまま残る
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.svc.Collect(context.Background(), f.cmd(tenant, customer, pending, inv, i+1))
		}()
	}
	wg.Wait()
	inProgress := 0
	for _, err := range errs {
		if errors.Is(err, c.ErrAttemptInProgressKind) {
			inProgress++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if inProgress != 1 || f.count(t, `SELECT count(*) FROM payments.stub_charges`) != 1 {
		t.Fatalf("AC-PAY-012: errs = %v", errs)
	}

	// AC-PAY-013
	_, customer2, paid := ids()
	ok := f.method(t, customer2, "ok")
	if r, err := f.svc.Collect(context.Background(), f.cmd(tenant, customer2, ok, paid, 1)); err != nil || r.Status != c.AttemptSucceeded {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := f.svc.Collect(context.Background(), f.cmd(tenant, customer2, ok, paid, 2)); !errors.Is(err, c.ErrPayableAlreadyPaidKind) {
		t.Errorf("AC-PAY-013: %v", err)
	}
	// AC-PAY-014
	if _, err := f.svc.Collect(context.Background(), f.cmd(tenant, customer2, ok, paid, 1, func(cmd *c.CollectCmd) { cmd.Amount = sk.MustMoney(981) })); !errors.Is(err, c.ErrIdempotencyKeyReused) {
		t.Errorf("AC-PAY-014: %v", err)
	}
	// AC-PAY-018: unknown の試行がある請求に別キー
	_, customer3, unknownInv := ids()
	timeout := f.method(t, customer3, "timeout")
	if r, _ := f.svc.Collect(context.Background(), f.cmd(tenant, customer3, timeout, unknownInv, 1)); r.Status != c.AttemptUnknown {
		t.Fatalf("unknown にならない: %s", r.Status)
	}
	if _, err := f.svc.Collect(context.Background(), f.cmd(tenant, customer3, timeout, unknownInv, 2)); !errors.Is(err, c.ErrAttemptInProgressKind) {
		t.Errorf("AC-PAY-018: %v", err)
	}
	// E-08: 他人の支払方法
	if _, err := f.svc.Collect(context.Background(), f.cmd(tenant, customer, ok, sk.NewID[sk.InvoiceTag](), 1)); !errors.Is(err, c.ErrPaymentMethodNotFound) {
		t.Errorf("E-08: %v", err)
	}
}

// AC-PAY-025: 削除済みの支払方法 → 代行に送らず failed(payment_method_unavailable, retryable)
func TestDeletedMethod(t *testing.T) {
	f := setup(t)
	tenant, customer, inv := ids()
	pm := f.method(t, customer, "ok")
	if err := f.svc.DeletePaymentMethod(context.Background(), customer, pm); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.Collect(context.Background(), f.cmd(tenant, customer, pm, inv, 1))
	if err != nil || r.Status != c.AttemptFailed || *r.FailureCode != "payment_method_unavailable" || !*r.Retryable {
		t.Fatalf("%+v %v", r, err)
	}
	if f.count(t, `SELECT count(*) FROM payments.stub_charges`) != 0 {
		t.Error("代行に送った")
	}
}

// 結果不明の照合（§8.1）：timeout は succeeded、timeout_lost は 10 分を超えてから not_processed
func TestResolveUnknown(t *testing.T) {
	f := setup(t)
	tenant, customer, inv := ids()
	charged := f.method(t, customer, "timeout")
	r1, _ := f.svc.Collect(context.Background(), f.cmd(tenant, customer, charged, inv, 1))
	_, customer2, inv2 := ids()
	lost := f.method(t, customer2, "timeout_lost")
	r2, _ := f.svc.Collect(context.Background(), f.cmd(tenant, customer2, lost, inv2, 1))

	f.clock.Advance(9 * time.Minute)
	if _, err := f.svc.ResolveUnknownAttempts(context.Background(), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.svc.GetAttempt(context.Background(), r1.AttemptID); st.Status != c.AttemptSucceeded || string(*st.Resolution) != "reconcile" {
		t.Errorf("timeout: %s", st.Status)
	}
	if st, _ := f.svc.GetAttempt(context.Background(), r2.AttemptID); st.Status != c.AttemptUnknown {
		t.Errorf("timeout_lost は 10 分以内は保留: %s", st.Status)
	}
	f.clock.Advance(2 * time.Minute)
	_, _ = f.svc.ResolveUnknownAttempts(context.Background(), 30*time.Second)
	if st, _ := f.svc.GetAttempt(context.Background(), r2.AttemptID); st.Status != c.AttemptFailed || string(*st.FailureCode) != "not_processed" || !*st.Retryable {
		t.Errorf("timeout_lost: %+v", st)
	}
	if f.count(t, `SELECT count(*) FROM payments.stub_charges WHERE scenario = 'timeout'`) != 1 {
		t.Error("二重に課金した")
	}
}

// settle は確定待ちの決済を確定し、通知を Inbox に入れる（dev の settle と同じ経路）。
func (f *fixture) settle(t *testing.T, attempt sk.PaymentAttemptID, result string) {
	t.Helper()
	st, _ := f.svc.GetAttempt(context.Background(), attempt)
	body, h, err := f.stub.Settle(context.Background(), *st.ProviderOrderRef, result, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.IngestWebhook(context.Background(), "stub", h, body); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) lastKick(t *testing.T) app.ProviderEventKey {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.kicks) == 0 {
		t.Fatal("通知の処理の依頼が送られていない")
	}
	env, err := decodeEnvelope(f.kicks[len(f.kicks)-1])
	if err != nil {
		t.Fatal(err)
	}
	key, err := sk.DecodePayload[app.ProviderEventKey](env)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// AC-PAY-020: 同じ通知を 2 回並行に処理しても、遷移とイベントは 1 回
func TestProviderEventOnce(t *testing.T) {
	f := setup(t)
	tenant, customer, inv := ids()
	pm := f.method(t, customer, "async_ok")
	r, _ := f.svc.Collect(context.Background(), f.cmd(tenant, customer, pm, inv, 1))
	f.settle(t, r.AttemptID, "")
	key := f.lastKick(t)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.svc.ProcessProviderEvent(context.Background(), key); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if st, _ := f.svc.GetAttempt(context.Background(), r.AttemptID); st.Status != c.AttemptSucceeded || string(*st.Resolution) != "webhook" {
		t.Errorf("%s", st.Status)
	}
	if f.count(t, `SELECT count(*) FROM payments.provider_events`) != 1 || f.count(t, `SELECT count(*) FROM payments.outbox_events WHERE event_type = 'payments.payment_succeeded'`) != 1 {
		t.Error("通知・イベントが 1 件でない")
	}
}

// AC-PAY-021: succeeded の試行に failed の通知 → succeeded のまま、conflicting_final_outcome を記録
func TestConflictingOutcome(t *testing.T) {
	f := setup(t)
	tenant, customer, inv := ids()
	pm := f.method(t, customer, "async_ok")
	r, _ := f.svc.Collect(context.Background(), f.cmd(tenant, customer, pm, inv, 1))
	if _, err := f.svc.ResolveManually(context.Background(), r.AttemptID, app.ResolveInput{Succeeded: true, Note: "先に確定", Operator: "op", Force: true}); err != nil {
		t.Fatal(err)
	}
	f.settle(t, r.AttemptID, "failed")
	if err := f.svc.ProcessProviderEvent(context.Background(), f.lastKick(t)); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.svc.GetAttempt(context.Background(), r.AttemptID); st.Status != c.AttemptSucceeded {
		t.Errorf("%s", st.Status)
	}
	if f.count(t, `SELECT count(*) FROM payments.reconciliation_discrepancies WHERE kind = 'conflicting_final_outcome'`) != 1 {
		t.Error("不整合が記録されない")
	}
}

// AC-PAY-035・036: 試行がロックされていたら再試行を延ばしてメッセージは消す。掃除が送り直し、確定する
func TestProviderEventRetryAndSweep(t *testing.T) {
	f := setup(t)
	tenant, customer, inv := ids()
	pm := f.method(t, customer, "async_ok")
	r, _ := f.svc.Collect(context.Background(), f.cmd(tenant, customer, pm, inv, 1))
	f.settle(t, r.AttemptID, "")
	key := f.lastKick(t)

	conn, err := f.d.SuperPool(t).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := conn.Begin(context.Background())
	if _, err := tx.Exec(context.Background(), `SELECT 1 FROM payments.payment_attempts WHERE id = $1 FOR UPDATE`, r.AttemptID.UUID()); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ProcessProviderEvent(context.Background(), key); err != nil {
		t.Fatalf("ロック待ちは業務エラーとして扱う: %v", err)
	}
	_ = tx.Rollback(context.Background())
	conn.Release()
	if f.count(t, `SELECT attempts FROM payments.provider_events`) != 1 {
		t.Error("attempts が増えていない")
	}
	f.mu.Lock()
	f.kicks = nil
	f.mu.Unlock()
	f.clock.Advance(5 * time.Minute)
	if _, err := f.svc.ProviderEventsSweepJob().Run(context.Background(), scheduler.Run{}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ProcessProviderEvent(context.Background(), f.lastKick(t)); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.svc.GetAttempt(context.Background(), r.AttemptID); st.Status != c.AttemptSucceeded {
		t.Errorf("掃除の後に確定しない: %s", st.Status)
	}
}

// AC-PAY-027: 共有のブレーカーが開いていれば、どちらのプロセスからも送らず試行も作らない
func TestSharedBreaker(t *testing.T) {
	f := setup(t)
	tenant, customer, inv := ids()
	pm := f.method(t, customer, "ok")
	other, err := db.Open(context.Background(), db.Config{URL: f.d.AppURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	worker, _ := f.newService(other)
	f.d.AsSuper(t, fmt.Sprintf(`INSERT INTO payments.provider_circuit_breakers (provider, consecutive_failures, open_until, updated_at) VALUES ('stub', 0, '%s', now())`,
		f.clock.Now().Add(30*time.Second).Format(time.RFC3339)))
	for _, svc := range []*app.Service{f.svc, worker} {
		if _, err := svc.Collect(context.Background(), f.cmd(tenant, customer, pm, inv, 1)); !errors.Is(err, c.ErrProviderUnavailable) {
			t.Errorf("ブレーカーが開いているのに: %v", err)
		}
	}
	if f.count(t, `SELECT count(*) FROM payments.payment_attempts`) != 0 {
		t.Error("試行を作った")
	}
	f.clock.Advance(31 * time.Second)
	if r, err := worker.Collect(context.Background(), f.cmd(tenant, customer, pm, inv, 1)); err != nil || r.Status != c.AttemptSucceeded {
		t.Errorf("閉じた後: %+v %v", r, err)
	}
	// provider_down の上書きでも試行を作らない（P-PAY-4）
	_, customer2, inv2 := ids()
	pm2 := f.method(t, customer2, "ok")
	ref := f.count(t, `SELECT count(*) FROM payments.payment_method_provider_refs WHERE payment_method_id = $1`, pm2.UUID())
	if ref != 1 {
		t.Fatal("参照がない")
	}
	var methodRef string
	_ = f.d.SuperPool(t).QueryRow(context.Background(), `SELECT method_ref FROM payments.payment_method_provider_refs WHERE payment_method_id = $1`, pm2.UUID()).Scan(&methodRef)
	if err := f.stub.SetBehavior(context.Background(), methodRef, "provider_down", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Collect(context.Background(), f.cmd(tenant, customer2, pm2, inv2, 1)); !errors.Is(err, c.ErrProviderUnavailable) {
		t.Errorf("provider_down: %v", err)
	}
	if r, err := f.svc.Collect(context.Background(), f.cmd(tenant, customer2, pm2, inv2, 1)); err != nil || r.Status != c.AttemptSucceeded {
		t.Errorf("上書きが切れた後: %+v %v", r, err)
	}
}

// AC-PAY-029: 事業者 A のスコープでは B の試行は 0 件
func TestRLS(t *testing.T) {
	f := setup(t)
	a, customer, inv := ids()
	b := sk.NewID[sk.TenantTag]()
	pm := f.method(t, customer, "ok")
	_, _ = f.svc.Collect(context.Background(), f.cmd(b, customer, pm, inv, 1))
	var n int
	_ = f.d.Pool.Module("payments").ReadTx(db.WithTenantScope(context.Background(), a), func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM payments.payment_attempts`).Scan(&n)
	})
	if n != 0 {
		t.Errorf("他テナントの試行が見えた: %d", n)
	}
	var methods int
	_ = f.d.Pool.Module("payments").ReadTx(db.WithCustomerScope(context.Background(), sk.NewID[sk.CustomerAccountTag]()), func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM payments.payment_methods`).Scan(&methods)
	})
	if methods != 0 {
		t.Errorf("他人の支払方法が見えた: %d", methods)
	}
	pgtest.AssertAllTenantTablesHaveRLS(t, f.d)
}

// AC-PAY-038: 同じ customer.withdrawn を 2 回配信しても処理は 1 回。支払方法はマスキングされて削除
func TestWithdrawalOnce(t *testing.T) {
	f := setup(t)
	_, customer, _ := ids()
	f.method(t, customer, "ok")
	f.method(t, customer, "decline")
	env, _ := sk.NewEnvelope("customer.withdrawn", "customer_account", customer.UUID(), 2, nil, f.clock.Now(), map[string]any{"customer_account_id": customer})
	h := inbox.Wrap(f.d.Pool.Module("payments"), "payments.customer_withdrawn", func(ctx context.Context, tx db.Tx, env sk.EventEnvelope) error {
		p, err := sk.DecodePayload[struct {
			CustomerAccountID sk.CustomerAccountID `json:"customer_account_id"`
		}](env)
		if err != nil {
			return err
		}
		return f.svc.HandleWithdrawal(ctx, tx, p.CustomerAccountID)
	}, inbox.AllowSystemScope("customer_withdrawn"))
	for range 2 {
		if err := h(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	}
	if f.count(t, `SELECT count(*) FROM payments.processed_messages`) != 1 {
		t.Error("Inbox が 1 件でない")
	}
	if n := f.count(t, `SELECT count(*) FROM payments.payment_methods WHERE customer_account_id = $1 AND status = 'deleted' AND display->>'last4' = '****'`, customer.UUID()); n != 2 {
		t.Errorf("マスキングされた削除 = %d", n)
	}
	page, _ := f.svc.ListPaymentMethods(context.Background(), customer, "", 20)
	if len(page.Items) != 0 {
		t.Error("一覧に残っている")
	}
}

// AC-PAY-037: 25 件・ページ 10 件で 2 ページ分のカーソルが保存済みなら、3 ページ目だけを処理する
func TestReconcileResume(t *testing.T) {
	f := setup(t)
	tenant, customer, _ := ids()
	pm := f.method(t, customer, "ok")
	for range 25 {
		if _, err := f.svc.Collect(context.Background(), f.cmd(tenant, customer, pm, sk.NewID[sk.InvoiceTag](), 1)); err != nil {
			t.Fatal(err)
		}
	}
	// 自社側を unknown に戻した 1 件と、自社に試行のない 1 件を作る（provider_succeeded_local_not と orphan）
	f.d.AsSuper(t, `UPDATE payments.payment_attempts SET status = 'unknown', finalized_at = NULL, resolution = NULL WHERE id = (SELECT id FROM payments.payment_attempts ORDER BY provider_order_ref DESC LIMIT 1)`)
	f.d.AsSuper(t, fmt.Sprintf(`INSERT INTO payments.stub_charges (order_ref, provider, idempotency_key, method_ref, amount, true_status, scenario, created_at) VALUES ('stub_zz_orphan', 'stub', '%s', 'm', 980, 'succeeded', 'ok', '%s')`,
		uuid.NewString(), f.clock.Now().Format(time.RFC3339)))
	day := f.clock.Today()
	f.clock.Advance(24 * time.Hour)
	runID := uuid.Must(uuid.NewV7())
	cursors := scheduler.NewCursorStore(f.d.Pool, app.ReconcileDailyJobName, runID)
	// 2 ページ（20 件）処理済みのカーソル：order_ref の 20 件目まで
	var page2Last string
	_ = f.d.SuperPool(t).QueryRow(context.Background(), `SELECT order_ref FROM payments.stub_charges ORDER BY order_ref OFFSET 19 LIMIT 1`).Scan(&page2Last)
	if err := cursors.Save(context.Background(), nil, day.String()+":stub", []byte(fmt.Sprintf(`{"provider_cursor":%q,"pages":2}`, page2Last))); err != nil {
		t.Fatal(err)
	}
	run := scheduler.Run{ID: runID, Job: app.ReconcileDailyJobName, AsOf: f.clock.Today(), Cursors: cursors}
	st, err := f.svc.ReconcileDailyJob().Run(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if st["pages"] != int64(1) {
		t.Errorf("3 ページ目だけを処理するはず: %v", st)
	}
	// 3 ページ目（26 件中の 21〜26 件目）に unknown の 1 件と orphan の 1 件が入っている
	if f.count(t, `SELECT count(*) FROM payments.reconciliation_discrepancies`) != 2 {
		t.Errorf("不整合 = %d", f.count(t, `SELECT count(*) FROM payments.reconciliation_discrepancies`))
	}
	again, _ := f.svc.ReconcileDailyJob().Run(context.Background(), run)
	if again["pages"] != int64(0) {
		t.Errorf("完了済みの再実行は何もしない: %v", again)
	}
}
