// Package integration は事業者 Webhook と契約者メールのモジュール（integration.md）。cmd/** からだけ import する（規約 M7）。
package integration

import (
	"context"
	"errors"
	"time"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	customer "subscription-platform/modules/customer/contracts"
	inthttp "subscription-platform/modules/integration/internal/adapter/http"
	"subscription-platform/modules/integration/internal/adapter/mail"
	"subscription-platform/modules/integration/internal/adapter/secret"
	"subscription-platform/modules/integration/internal/adapter/webhook"
	"subscription-platform/modules/integration/internal/app"
	"subscription-platform/modules/integration/internal/domain"
	tenant "subscription-platform/modules/tenant/contracts"
)

// Deps はモジュールの依存（他モジュールは contracts だけ）。
type Deps struct {
	Pool     *db.Pool
	Clock    sk.Clock
	Cursors  *httpx.CursorCodec
	Customer customer.Service
	Tenant   tenant.Service
	// Mailer はメールの送信（MAIL_MODE に応じて cmd が選ぶ）。
	Mailer mail.Mailer
	// SecretEncKey は署名鍵の暗号化の鍵（32 バイト）。
	SecretEncKey  []byte
	InsecureHosts []string
	// RetrySchedule は WEBHOOK_RETRY_SCHEDULE（空なら本番の値）。
	RetrySchedule    string
	GiveUpAfter      time.Duration
	RetryJitter      float64
	BreakerThreshold int
	BreakerOpenFor   time.Duration
	Parallelism      int
	URLs             app.URLs
}

// URLs はメールのリンク（cmd が設定から作る）。
type URLs = app.URLs

// Mailer はメールの送信のポート（cmd が SES かログを選ぶ）。
type (
	Mailer    = mail.Mailer
	SESMailer = mail.SESMailer
	LogMailer = mail.LogMailer
)

// Module は integration モジュール。
type Module struct {
	svc     *app.Service
	db      *db.ModuleDB
	cursors *httpx.CursorCodec
}

// Name はスキーマ名。
const Name = "integration"

// 購読者の名前（deploy/messaging.yaml と同じ）。
const (
	ConsumerWebhook = "integration.webhook"
	ConsumerEmail   = "integration.email"
)

// New はモジュールを組み立てる。
func New(d Deps) (*Module, error) {
	c, err := secret.NewAESGCM(d.SecretEncKey)
	if err != nil {
		return nil, err
	}
	sched := domain.DefaultSchedule()
	if d.RetrySchedule != "" {
		give := d.GiveUpAfter
		if give == 0 {
			give = sched.GiveUp
		}
		if sched, err = domain.ParseSchedule(d.RetrySchedule, give, d.RetryJitter); err != nil {
			return nil, err
		}
	}
	breaker := domain.DefaultBreaker
	if d.BreakerThreshold > 0 {
		breaker.Threshold = d.BreakerThreshold
	}
	if d.BreakerOpenFor > 0 {
		breaker.OpenFor = d.BreakerOpenFor
	}
	if d.Mailer == nil {
		return nil, errors.New("integration: mailer is required")
	}
	mdb := d.Pool.Module(Name)
	insecure := domain.InsecureHosts(d.InsecureHosts)
	return &Module{db: mdb, cursors: d.Cursors, svc: app.New(app.Deps{
		DB: mdb, Clock: d.Clock, Cipher: c, Customer: d.Customer, Tenant: d.Tenant, Mailer: d.Mailer, Sender: webhook.NewSender(insecure),
		Insecure: insecure, Schedule: sched, Breaker: breaker, Parallelism: d.Parallelism, URLs: d.URLs,
	})}, nil
}

// MountHTTP は console・ops の API を登録する。
func (m *Module) MountHTTP(r *httpx.Router) error { return inthttp.Register(r, m.svc, m.cursors) }

// Consumers は integration-webhook・integration-email（worker-integration）。
func (m *Module) Consumers() []messaging.ConsumerSpec {
	return []messaging.ConsumerSpec{
		{Name: ConsumerWebhook, Handler: m.svc.WebhookHandler(ConsumerWebhook)},
		{Name: ConsumerEmail, Handler: m.svc.EmailHandler(ConsumerEmail)},
	}
}

// Jobs は署名鍵の後片付け（worker-integration）。
func (m *Module) Jobs() []scheduler.Job { return []scheduler.Job{m.svc.SecretCleanupJob()} }

// Loops は worker-integration の常駐ループ（配信ループ・メールの再送ループ）。
func (m *Module) Loops() []func(context.Context) error {
	return []func(context.Context) error{m.svc.RunDispatch, m.svc.RunEmailRetry}
}

// Problem は integration のエラーを Problem にする。
func Problem(err error) *httpx.Problem { return inthttp.Problem(err) }

// WebhookEventTypes・EmailEventTypes は購読するイベント（deploy/messaging.yaml の検査に使う）。
func WebhookEventTypes() []string { return domain.WebhookEventTypes() }
func EmailEventTypes() []string   { return append([]string(nil), domain.EmailEventTypes...) }
