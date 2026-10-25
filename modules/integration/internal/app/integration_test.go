//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/google/uuid"

	"subscription-platform/internal/platform/awsx"
	"subscription-platform/internal/platform/db/migrate"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/testsupport/awstest"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/customer"
	customerc "subscription-platform/modules/customer/contracts"
	customerdb "subscription-platform/modules/customer/db"
	integrationdb "subscription-platform/modules/integration/db"
	"subscription-platform/modules/integration/internal/adapter/mail"
	"subscription-platform/modules/integration/internal/adapter/secret"
	"subscription-platform/modules/integration/internal/adapter/webhook"
	"subscription-platform/modules/integration/internal/app"
	"subscription-platform/modules/integration/internal/domain"
	"subscription-platform/modules/tenant"
	tenantdb "subscription-platform/modules/tenant/db"
)

// 本物の PostgreSQL（testcontainers）と、本物の tenant・customer モジュールで組み立てる（P-TST-2）。
// 送信先は 127.0.0.1 で待ち受ける HTTP サーバー（webhook-sink と同じく、受け取った要求を記録し、指示した応答を返す）。
// メールは MiniStack の SES に送る。
func TestMain(m *testing.M) {
	pgtest.AtExit(awstest.Terminate)
	pgtest.Main(m,
		migrate.Source{Schema: "tenant", FS: migrate.SubFS(tenantdb.Migrations, "migrations")},
		migrate.Source{Schema: "customer", FS: migrate.SubFS(customerdb.Migrations, "migrations")},
		migrate.Source{Schema: "integration", FS: migrate.SubFS(integrationdb.Migrations, "migrations")},
	)
}

// receiver は事業者の送信先の代わり。status を返し（0 なら 200）、delay だけ待つ。同時に処理中の要求の数の最大値を数える。
type receiver struct {
	srv      *httptest.Server
	mu       sync.Mutex
	paths    []string
	status   atomic.Int32
	delay    atomic.Int64
	inFlight atomic.Int32
	maxIn    atomic.Int32
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := r.inFlight.Add(1)
		defer r.inFlight.Add(-1)
		for {
			m := r.maxIn.Load()
			if n <= m || r.maxIn.CompareAndSwap(m, n) {
				break
			}
		}
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		r.mu.Unlock()
		time.Sleep(time.Duration(r.delay.Load()))
		if s := r.status.Load(); s != 0 {
			w.WriteHeader(int(s))
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

func (r *receiver) pathsCopy() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

type world struct {
	t        *testing.T
	d        *pgtest.DB
	clock    *sk.ManualClock
	tenant   *tenant.Module
	customer *customer.Module
	cipher   secret.Cipher
}

func newWorld(t *testing.T) *world {
	t.Helper()
	d := pgtest.New(t)
	c, err := secret.NewAESGCM([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	clock := sk.NewManualClock(time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC))
	return &world{t: t, d: d, clock: clock, tenant: tenant.New(tenant.Deps{Pool: d.Pool, Clock: clock}), customer: customer.New(customer.Deps{Pool: d.Pool, Clock: clock}), cipher: c}
}

// service は配信ループのインスタンス 1 つ（TaskID が違えば別のタスク）。
func (w *world) service(task string, mailer mail.Mailer, parallelism int) *app.Service {
	if mailer == nil {
		mailer = mail.LogMailer{}
	}
	sched := domain.DefaultSchedule()
	sched.Jitter = 0
	insecure := domain.InsecureHosts{"127.0.0.1"}
	return app.New(app.Deps{
		DB: w.d.Pool.Module("integration"), Clock: w.clock, Cipher: w.cipher, Customer: w.customer.Service(), Tenant: w.tenant.Service(), Mailer: mailer,
		Sender: webhook.NewSender(insecure), Insecure: insecure, Schedule: sched, Breaker: domain.DefaultBreaker, Parallelism: parallelism,
		URLs:   app.URLs{PaymentMethodUpdate: "http://localhost/checkout/?subscription={subscription_id}", Mypage: "http://localhost/mypage", Console: "http://localhost/console"},
		TaskID: task,
	})
}

func (w *world) newTenant() sk.TenantID {
	w.t.Helper()
	tid := uuid.New()
	now := w.clock.Now().Add(-time.Hour).Format(time.RFC3339)
	w.d.AsSuper(w.t,
		fmt.Sprintf(`INSERT INTO tenant.tenants (id, name, status, fincode_shop_id, approved_at, created_at, updated_at) VALUES ('%s', 'ヨガ教室', 'active', 'shop_%s', '%s', '%s', '%s')`,
			tid, strings.ReplaceAll(tid.String(), "-", "")[:12], now, now, now),
		fmt.Sprintf(`INSERT INTO tenant.staff_members (id, tenant_id, subject, email, display_name, role, created_at, updated_at) VALUES ('%s', '%s', 'owner-%s', 'owner-%s@example.invalid', 'Owner', 'owner', '%s', '%s')`,
			uuid.New(), tid, tid, tid, now, now),
	)
	return sk.IDFromUUID[sk.TenantTag](tid)
}

func (w *world) newCustomer(t sk.TenantID) (sk.CustomerAccountID, string) {
	w.t.Helper()
	sub := "it-" + uuid.NewString()
	yes := true
	acct, err := w.customer.Service().ResolveOrCreateAccount(context.Background(), customerc.AuthIdentity{Subject: sub, Email: sub + "@example.invalid", EmailVerified: &yes})
	if err != nil {
		w.t.Fatal(err)
	}
	cus, err := w.customer.Service().EnsureTenantCustomerID(context.Background(), t, acct.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return acct.ID, string(cus)
}

func (w *world) endpoint(svc *app.Service, t sk.TenantID, url string, types ...string) *domain.Endpoint {
	w.t.Helper()
	if len(types) == 0 {
		types = []string{"subscription.renewed"}
	}
	ep, _, err := svc.CreateEndpoint(context.Background(), t, app.CreateEndpointInput{URL: url, EventTypes: types, Enabled: true})
	if err != nil {
		w.t.Fatal(err)
	}
	return ep
}

// renewed は subscription.renewed の内部イベント（subscription モジュールの payload の形）。
func renewed(t sk.TenantID, cust sk.CustomerAccountID, sub sk.SubscriptionID, inv sk.InvoiceID, at time.Time) sk.EventEnvelope {
	env, err := sk.NewEnvelope("subscription.renewed", "subscription", sub.UUID(), 3, &t, at, map[string]any{
		"subscription": map[string]any{
			"subscription_id": sub, "tenant_id": t, "customer_account_id": cust, "status": "active", "plan_id": uuid.New(),
			"current_period": map[string]any{"start": "2026-11-15", "end": "2026-12-14"}, "next_renewal_date": "2026-12-15", "version": 3,
		},
		"invoice_id": inv,
	})
	if err != nil {
		panic(err)
	}
	env.CorrelationID = "it-corr"
	return env
}

func pastDue(t sk.TenantID, cust sk.CustomerAccountID, sub sk.SubscriptionID, inv sk.InvoiceID, at time.Time) sk.EventEnvelope {
	env := renewed(t, cust, sub, inv, at)
	env.EventType = "subscription.past_due"
	env.EventID = sk.NewID[sk.EventTag]()
	return env
}

func issued(t sk.TenantID, sub sk.SubscriptionID, inv sk.InvoiceID, total int64, at time.Time) sk.EventEnvelope {
	env, _ := sk.NewEnvelope("billing.invoice_issued", "invoice", inv.UUID(), 1, &t, at, map[string]any{
		"invoice_id": inv, "subscription_id": sub, "total": total, "collection_mode": "scheduled",
	})
	return env
}

func (w *world) count(sql string, args ...any) int64 {
	w.t.Helper()
	return w.d.QueryInt(w.t, sql, args...)
}

func handle(t *testing.T, h messaging.Handler, env sk.EventEnvelope) {
	t.Helper()
	if err := h(messaging.ContextFromEnvelope(context.Background(), env), env); err != nil {
		t.Fatal(err)
	}
}

// RLS：integration・audit の tenant_id を持つ表はすべて RLS の対象。
func TestRLS(t *testing.T) {
	pgtest.AssertAllTenantTablesHaveRLS(t, newWorld(t).d)
}

// AC-INT-011・E1：同じ内部イベントを 2 回処理しても外部イベント 1 つ・配信 1 つ。Inbox をすり抜けても source_event_id で 1 つ。
func TestWebhookInboxDedupe(t *testing.T) {
	w := newWorld(t)
	svc := w.service("a", nil, 8)
	tid := w.newTenant()
	cust, cus := w.newCustomer(tid)
	rcv := newReceiver(t)
	w.endpoint(svc, tid, rcv.srv.URL+"/hook")
	env := renewed(tid, cust, sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag](), w.clock.Now())
	h := svc.WebhookHandler("integration.webhook")
	handle(t, h, env)
	handle(t, h, env)
	handle(t, svc.WebhookHandler("integration.webhook.bypass"), env) // 別の consumer 名で Inbox をすり抜ける
	if n := w.count(`SELECT count(*) FROM integration.webhook_events WHERE source_event_id = $1`, env.EventID.UUID()); n != 1 {
		t.Fatalf("webhook_events = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM integration.webhook_deliveries`); n != 1 {
		t.Fatalf("deliveries = %d", n)
	}
	var payload string
	if err := w.d.SuperPool(t).QueryRow(context.Background(), `SELECT payload FROM integration.webhook_events`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"customer_id":"`+cus+`"`) || strings.Contains(payload, cust.String()) || strings.Contains(payload, "it-corr") {
		t.Fatalf("payload = %s", payload)
	}
}

// AC-INT-019：ブレーカーの状態は DB にあり、別のインスタンスと共有する。開いている間は HTTP を送らず、attempt_count も増えない。
func TestBreakerSharedAcrossInstances(t *testing.T) {
	w := newWorld(t)
	a, b := w.service("task-a", nil, 1), w.service("task-b", nil, 1)
	tid := w.newTenant()
	cust, _ := w.newCustomer(tid)
	rcv := newReceiver(t)
	rcv.status.Store(500)
	ep := w.endpoint(a, tid, rcv.srv.URL+"/hook")
	ctx := context.Background()
	// 5 つの別の配信をインスタンス A で 1 回ずつ失敗させる（連続 5 回でブレーカーが開く）
	for range 5 {
		handle(t, a.WebhookHandler("integration.webhook"), renewed(tid, cust, sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag](), w.clock.Now()))
	}
	if _, err := a.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if rcv.count() != 5 {
		t.Fatalf("A の送信 = %d", rcv.count())
	}
	if n := w.count(`SELECT breaker_consecutive_failures FROM integration.webhook_endpoints WHERE id = $1`, ep.ID.UUID()); n != 5 {
		t.Fatalf("failures = %d", n)
	}
	// 6 件目をインスタンス B で：HTTP を送らずに open_until まで見送る
	handle(t, b.WebhookHandler("integration.webhook"), renewed(tid, cust, sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag](), w.clock.Now()))
	if _, err := b.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if rcv.count() != 5 {
		t.Fatalf("開いている間に送った: %d", rcv.count())
	}
	if n := w.count(`SELECT count(*) FROM integration.webhook_deliveries WHERE attempt_count = 0 AND next_attempt_at = $1`, w.clock.Now().Add(time.Minute)); n != 1 {
		t.Fatalf("見送った配信 = %d", n)
	}
	// 送信先が回復し、open_until の後の最初の 1 件（半開の試し）が成功すると閉じる
	rcv.status.Store(0)
	w.clock.Advance(61 * time.Second)
	if _, err := b.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if rcv.count() != 11 { // 半開の試しが成功して閉じ、時刻を迎えた残りの 5 件も続けて送る
		t.Fatalf("回復後の送信 = %d", rcv.count())
	}
	if n := w.count(`SELECT breaker_consecutive_failures FROM integration.webhook_endpoints WHERE id = $1 AND breaker_open_until IS NULL`, ep.ID.UUID()); n != 0 {
		t.Fatalf("閉じない: %d", n)
	}
}

// AC-INT-037：インスタンス A がリースを持ったまま止まっても、30 秒後に B が引き継いで送る。
func TestLeaseTakeoverAfterCrash(t *testing.T) {
	w := newWorld(t)
	a, b := w.service("task-a", nil, 1), w.service("task-b", nil, 1)
	tid := w.newTenant()
	cust, _ := w.newCustomer(tid)
	rcv := newReceiver(t)
	w.endpoint(a, tid, rcv.srv.URL+"/hook")
	handle(t, a.WebhookHandler("integration.webhook"), renewed(tid, cust, sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag](), w.clock.Now()))
	ctx := context.Background()
	leased, err := a.LeaseEndpointsForTest(ctx, 1) // A はリースを取ったまま止まる（送らない）
	if err != nil || len(leased) != 1 {
		t.Fatalf("lease: %v %d", err, len(leased))
	}
	if n, _ := b.DispatchOnce(ctx); n != 0 {
		t.Fatal("リース中に B が取った")
	}
	w.clock.Advance(31 * time.Second)
	if n, err := b.DispatchOnce(ctx); err != nil || n != 1 {
		t.Fatalf("B が引き継がない: %v %d", err, n)
	}
	if rcv.count() != 1 || w.count(`SELECT count(*) FROM integration.webhook_deliveries WHERE status = 'succeeded'`) != 1 {
		t.Fatalf("送信 = %d", rcv.count())
	}
}

// AC-INT-038・E5a：同じ送信先への pending が 3 件、応答 500ms、2 つのインスタンスを同時に動かしても、送信先への同時実行は 1。
func TestOneInFlightPerEndpoint(t *testing.T) {
	w := newWorld(t)
	a, b := w.service("task-a", nil, 4), w.service("task-b", nil, 4)
	tid := w.newTenant()
	cust, _ := w.newCustomer(tid)
	rcv := newReceiver(t)
	rcv.delay.Store(int64(500 * time.Millisecond))
	w.endpoint(a, tid, rcv.srv.URL+"/hook")
	for range 3 {
		handle(t, a.WebhookHandler("integration.webhook"), renewed(tid, cust, sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag](), w.clock.Now()))
	}
	var wg sync.WaitGroup
	for _, s := range []*app.Service{a, b, a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.DispatchOnce(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if rcv.count() != 3 || rcv.maxIn.Load() != 1 {
		t.Fatalf("count=%d max in flight=%d", rcv.count(), rcv.maxIn.Load())
	}
}

// AC-INT-039・E5c：X に 100 件、Y に 1 件、並列数 1。Y の 1 件は X の 20 件の後までに届く（1 回のリースで 20 件まで）。
func TestFairnessAcrossEndpoints(t *testing.T) {
	w := newWorld(t)
	svc := w.service("task-a", nil, 1)
	tid := w.newTenant()
	cust, _ := w.newCustomer(tid)
	rcv := newReceiver(t)
	w.endpoint(svc, tid, rcv.srv.URL+"/x")
	for range 100 {
		handle(t, svc.WebhookHandler("integration.webhook"), renewed(tid, cust, sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag](), w.clock.Now()))
	}
	// Y の送信先と配信は後から作る（X には既に 100 件がたまっている）
	tid2 := w.newTenant()
	cust2, _ := w.newCustomer(tid2)
	w.endpoint(svc, tid2, rcv.srv.URL+"/y")
	handle(t, svc.WebhookHandler("integration.webhook"), renewed(tid2, cust2, sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag](), w.clock.Now()))
	for rcv.count() < 101 {
		if n, err := svc.DispatchOnce(context.Background()); err != nil || n == 0 {
			t.Fatalf("dispatch: %v %d (sent %d)", err, n, rcv.count())
		}
	}
	paths := rcv.pathsCopy()
	pos := -1
	for i, p := range paths {
		if p == "/y" {
			pos = i
		}
	}
	if pos < 0 || pos > 20 {
		t.Fatalf("Y の位置 = %d", pos)
	}
}

// 送信先を無効にすると、pending の配信は送らずに abandoned になる（E3・E4）。
func TestDisabledEndpointAbandons(t *testing.T) {
	w := newWorld(t)
	svc := w.service("task-a", nil, 1)
	tid := w.newTenant()
	cust, _ := w.newCustomer(tid)
	rcv := newReceiver(t)
	ep := w.endpoint(svc, tid, rcv.srv.URL+"/hook")
	handle(t, svc.WebhookHandler("integration.webhook"), renewed(tid, cust, sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag](), w.clock.Now()))
	off := false
	if _, err := svc.UpdateEndpoint(context.Background(), tid, ep.ID, domain.Patch{Enabled: &off}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rcv.count() != 0 || w.count(`SELECT count(*) FROM integration.webhook_deliveries WHERE status = 'abandoned'`) != 1 {
		t.Fatalf("sent=%d", rcv.count())
	}
}

// ---- メール ----

// sesMailer は SES に送る Mailer。差出人はローカル環境（Terraform の aws_ses_email_identity）と同じく検証済みにする。
func sesMailer(t *testing.T, endpoint string) mail.Mailer {
	cfg := awsx.Static(awstest.Region, endpoint)
	c := ses.NewFromConfig(cfg, func(o *ses.Options) { o.RetryMaxAttempts = 1 })
	if !strings.Contains(endpoint, "127.0.0.1:1") {
		if _, err := c.VerifyEmailIdentity(context.Background(), &ses.VerifyEmailIdentityInput{EmailAddress: aws.String("noreply@sp.local")}); err != nil {
			t.Fatal(err)
		}
	}
	return mail.SESMailer{Client: c, From: "noreply@sp.local"}
}

// AC-INT-024：同じ請求の subscription.past_due を別の event_id で 2 回処理しても payment_failed の行は 1 つ。
func TestEmailDedupeKey(t *testing.T) {
	w := newWorld(t)
	svc := w.service("a", nil, 1)
	tid := w.newTenant()
	cust, _ := w.newCustomer(tid)
	sub, inv := sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag]()
	h := svc.EmailHandler("integration.email")
	handle(t, h, issued(tid, sub, inv, 1980, w.clock.Now()))
	handle(t, h, pastDue(tid, cust, sub, inv, w.clock.Now()))
	handle(t, h, pastDue(tid, cust, sub, inv, w.clock.Now()))
	if n := w.count(`SELECT count(*) FROM integration.email_notifications WHERE dedupe_key = $1`, "payment_failed:"+inv.String()); n != 1 {
		t.Fatalf("rows = %d", n)
	}
	if n := w.count(`SELECT count(*) FROM integration.email_notifications WHERE dedupe_key = $1 AND status = 'sent' AND variables->>'tenant_name' = 'ヨガ教室' AND (variables->>'amount')::int = 1980`, "payment_failed:"+inv.String()); n != 1 {
		t.Fatal("LogMailer で sent にならない、または変数が書き足されない")
	}
}

// AC-INT-023：SES のエンドポイントを閉じたポートに向けると 1 回目は pending（Inbox は記録済み）、MiniStack に戻すと再送ループで sent。
// 宛先は本物の customer モジュールの GetContact の値（§11.3）。
func TestEmailRetryAfterSESOutage(t *testing.T) {
	w := newWorld(t)
	endpoint := awstest.Endpoint(t)
	down := w.service("a", sesMailer(t, "http://127.0.0.1:1"), 1)
	tid := w.newTenant()
	cust, _ := w.newCustomer(tid)
	sub, inv := sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag]()
	h := down.EmailHandler("integration.email")
	handle(t, h, issued(tid, sub, inv, 980, w.clock.Now()))
	env := pastDue(tid, cust, sub, inv, w.clock.Now())
	handle(t, h, env) // 送信の失敗は SQS のメッセージの失敗にしない
	if n := w.count(`SELECT count(*) FROM integration.email_notifications WHERE status = 'pending' AND attempt_count = 1 AND next_attempt_at = $1`, w.clock.Now().Add(time.Minute)); n != 1 {
		t.Fatal("1 回目の失敗で pending（1 分後）にならない")
	}
	if n := w.count(`SELECT count(*) FROM integration.processed_messages WHERE consumer = 'integration.email' AND message_id = $1`, env.EventID.UUID()); n != 1 {
		t.Fatal("Inbox が記録されていない")
	}
	up := w.service("a", sesMailer(t, endpoint), 1)
	w.clock.Advance(time.Minute)
	if err := up.RetryEmailsOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var msgID string
	if err := w.d.SuperPool(t).QueryRow(context.Background(), `SELECT provider_message_id FROM integration.email_notifications WHERE status = 'sent' AND dedupe_key = $1`, "payment_failed:"+inv.String()).Scan(&msgID); err != nil {
		var st, le string
		_ = w.d.SuperPool(t).QueryRow(context.Background(), `SELECT status, coalesce(last_error,'') FROM integration.email_notifications WHERE dedupe_key = $1`, "payment_failed:"+inv.String()).Scan(&st, &le)
		t.Fatalf("sent にならない: %v status=%s last_error=%s", err, st, le)
	}
	contact, err := w.customer.Service().GetContact(context.Background(), cust)
	if err != nil {
		t.Fatal(err)
	}
	to := sesRecipients(t, endpoint, msgID)
	if len(to) != 1 || to[0] != contact.Email {
		t.Fatalf("宛先 = %v, want %s", to, contact.Email)
	}
}

func sesRecipients(t *testing.T, endpoint, msgID string) []string {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint+"/_ministack/ses/messages", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Messages map[string][]struct {
			MessageID string   `json:"MessageId"`
			To        []string `json:"To"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, ms := range body.Messages {
		for _, m := range ms {
			if m.MessageID == msgID {
				return m.To
			}
		}
	}
	return nil
}

// AC-INT-026・E15：退会した契約者の pending のメールは skipped になり、再送ループで送られない。
func TestWithdrawnSkipsPendingEmail(t *testing.T) {
	w := newWorld(t)
	svc := w.service("a", sesMailer(t, "http://127.0.0.1:1"), 1)
	tid := w.newTenant()
	cust, _ := w.newCustomer(tid)
	sub, inv := sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag]()
	h := svc.EmailHandler("integration.email")
	handle(t, h, issued(tid, sub, inv, 980, w.clock.Now()))
	handle(t, h, pastDue(tid, cust, sub, inv, w.clock.Now()))
	wd, err := sk.NewEnvelope(customerc.EventCustomerWithdrawn, "customer_account", cust.UUID(), 2, nil, w.clock.Now(),
		customerc.CustomerWithdrawnPayload{CustomerAccountID: cust, WithdrawnAt: w.clock.Now()})
	if err != nil {
		t.Fatal(err)
	}
	handle(t, h, wd)
	if n := w.count(`SELECT count(*) FROM integration.email_notifications WHERE status = 'skipped' AND customer_account_id = $1`, cust.UUID()); n != 1 {
		t.Fatalf("skipped = %d", n)
	}
	w.clock.Advance(time.Hour)
	if err := w.service("a", mail.LogMailer{}, 1).RetryEmailsOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM integration.email_notifications WHERE status = 'sent'`); n != 0 {
		t.Fatal("退会した契約者に送った")
	}
}

// AC-INT-040：行をコミットしたが送信していない（locked_until が残る）メールは、60 秒後に再送ループが 1 回だけ送る。
func TestEmailLeaseExpiryPickup(t *testing.T) {
	w := newWorld(t)
	endpoint := awstest.Endpoint(t)
	svc := w.service("a", sesMailer(t, endpoint), 1)
	tid := w.newTenant()
	cust, _ := w.newCustomer(tid)
	sub, inv := sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.InvoiceTag]()
	// 受信処理の「行の作成」だけを行い、送る前に落ちたことにする
	inner := svc.EmailRowsOnlyHandlerForTest("integration.email")
	handle(t, inner, issued(tid, sub, inv, 980, w.clock.Now()))
	handle(t, inner, renewed(tid, cust, sub, inv, w.clock.Now()))
	if err := svc.RetryEmailsOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM integration.email_notifications WHERE status = 'pending' AND locked_until IS NOT NULL`); n != 1 {
		t.Fatal("リース中の行を再送ループが拾った")
	}
	w.clock.Advance(61 * time.Second)
	for range 2 {
		if err := svc.RetryEmailsOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.count(`SELECT count(*) FROM integration.email_notifications WHERE status = 'sent' AND attempt_count = 1 AND template_key = 'renewal_succeeded'`); n != 1 {
		t.Fatal("1 回だけ送られて sent にならない")
	}
}
