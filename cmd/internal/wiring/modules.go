package wiring

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/google/uuid"

	"subscription-platform/internal/platform/config"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/audit"
	"subscription-platform/modules/billing"
	billingcontracts "subscription-platform/modules/billing/contracts"
	"subscription-platform/modules/catalog"
	catalogcontracts "subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/customer"
	customercontracts "subscription-platform/modules/customer/contracts"
	"subscription-platform/modules/integration"
	"subscription-platform/modules/orchestration"
	"subscription-platform/modules/payments"
	paymentscontracts "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/subscription"
	subscriptioncontracts "subscription-platform/modules/subscription/contracts"
	"subscription-platform/modules/tenant"
	tenantcontracts "subscription-platform/modules/tenant/contracts"
)

// ModuleSet は 1 モジュール分の組み立ての結果（規約 M7 の形）。各ドメインの TL がモジュールを実装したら、modules() に追加する。
type ModuleSet struct {
	Name       string // スキーマ名
	Migrations fs.FS  // db/migrations
	HasOutbox  bool   // create_messaging_tables を呼んでいる（Relay の対象）
	MountHTTP  func(*httpx.Router) error
	Consumers  []messaging.ConsumerSpec
	Jobs       []scheduler.Job
	Ports      func(*httpx.Ports) // httpx に注入する関数（identity・audit）
	Loops      []Loop             // 常駐ループ（Process のプロセスだけが動かす）
}

// Loop はプロセスに常駐するループ（配信ループなど）。
type Loop struct {
	Process string
	Run     func(context.Context) error
}

// Services は組み立てたモジュールの contracts。
type Services struct {
	Tenant       tenantcontracts.Service
	Customer     customercontracts.Service
	Catalog      catalogcontracts.Service
	Subscription subscriptioncontracts.Service
	Billing      billingcontracts.Service
	Payments     paymentscontracts.Service
}

// modules はプロセスに組み込むモジュール。マイグレーションの順序（tenant → … → audit）で並べる。
func modules(_ context.Context, b *Base, process string) ([]ModuleSet, error) {
	codec, err := httpx.NewCursorCodec(b.Cfg.CursorKey)
	if err != nil {
		return nil, fmt.Errorf("cursor codec: %w", err)
	}
	t := tenant.New(tenant.Deps{Pool: b.Pool, Clock: b.Clock, Cursors: codec})
	c := customer.New(customer.Deps{Pool: b.Pool, Clock: b.Clock})
	cat := catalog.New(catalog.Deps{Pool: b.Pool, Clock: b.Clock, Cursors: codec, Tenants: t.Service(), ReservationTTL: b.Cfg.CatalogCouponReservationTTL})
	sub := subscription.New(subscription.Deps{Pool: b.Pool, Clock: b.Clock, Cursors: codec, Catalog: cat.Service()})
	bil := billing.New(billing.Deps{Pool: b.Pool, Clock: b.Clock, Cursors: codec, Lease: b.Cfg.BillingCollectionLease})
	pay, err := payments.New(payments.Deps{
		Pool: b.Pool, Clock: b.Clock, Cursors: codec, Methods: b.Cfg.PaymentsMethods, EnableStub: config.DevtoolsBuild, StubSecret: b.Cfg.StubWebhookSecret,
		FincodeAPIBaseURL: b.Cfg.FincodeAPIBaseURL, FincodeSecretKey: b.Cfg.FincodeSecretKey, FincodePublicKey: b.Cfg.FincodePublicKey,
		FincodeWebhookSignature: b.Cfg.FincodeWebhookSignature, FincodeSharedCustomer: b.Cfg.FincodeSharedCustomer,
		SendProviderEvent: func(ctx context.Context, body string) error {
			return b.SendToQueue(ctx, "payments-provider-events", body)
		},
	})
	if err != nil {
		return nil, err
	}
	fifo := messaging.NewSQSCommandSender(b.SQS, b.Cfg.QueueURLPrefix)
	sched := b.SchedulerService()
	orc := orchestration.New(orchestration.Deps{
		Pool: b.Pool, Clock: b.Clock, Tenant: t.Service(), Customer: c.Service(), Catalog: cat.Service(), Subscription: sub.Service(),
		Billing: bil.Service(), Payments: pay.Service(),
		SendRenewal: func(ctx context.Context, msgs []messaging.FIFOMessage) error {
			return fifo.SendFIFOBatch(ctx, "orc-renewal.fifo", msgs)
		},
		EnqueueJob: func(ctx context.Context, job string, asOf sk.Date, params []byte) (uuid.UUID, error) {
			res, err := sched.Enqueue(ctx, job, scheduler.EnqueueOptions{Trigger: scheduler.TriggerOps, AsOf: &asOf, Params: params, Force: true})
			return res.RunID, err
		},
	})
	var mailer integration.Mailer = integration.SESMailer{Client: ses.NewFromConfig(b.AWS), From: b.Cfg.MailFrom}
	if b.Cfg.MailMode == "log" {
		mailer = integration.LogMailer{}
	}
	var intg *integration.Module
	if process == "api" || process == "worker-integration" {
		w := b.Cfg.Webhook
		intg, err = integration.New(integration.Deps{
			Pool: b.Pool, Clock: b.Clock, Cursors: codec, Customer: c.Service(), Tenant: t.Service(), Mailer: mailer, SecretEncKey: w.SecretEncKey,
			InsecureHosts: b.Cfg.WebhookInsecureAllowedHosts, RetrySchedule: w.RetrySchedule, GiveUpAfter: w.GiveUpAfter, RetryJitter: w.RetryJitter,
			BreakerThreshold: w.BreakerThreshold, BreakerOpenFor: w.BreakerOpenFor, Parallelism: w.Parallelism,
			URLs: integration.URLs{PaymentMethodUpdate: b.Cfg.PaymentMethodUpdateURLTemplate, Mypage: b.Cfg.MypageURL, Console: b.Cfg.ConsoleURL},
		})
		if err != nil {
			return nil, fmt.Errorf("integration: %w", err)
		}
	}
	aud := audit.New(b.Pool, codec)
	b.Services = Services{Tenant: t.Service(), Customer: c.Service(), Catalog: cat.Service(), Subscription: sub.Service(), Billing: bil.Service(), Payments: pay.Service()}
	b.devMounts = append(b.devMounts, pay.MountDev)
	return []ModuleSet{
		{Name: tenant.Name, HasOutbox: true, MountHTTP: t.MountHTTP, Ports: tenantPorts(t.Service())},
		{Name: customer.Name, HasOutbox: true, MountHTTP: c.MountHTTP, Ports: func(p *httpx.Ports) { p.ResolveConsumer = c.ResolveConsumer }},
		{Name: catalog.Name, HasOutbox: true, MountHTTP: cat.MountHTTP, Jobs: cat.Jobs()},
		{Name: subscription.Name, HasOutbox: true, MountHTTP: sub.MountHTTP, Jobs: sub.Jobs()},
		{Name: billing.Name, HasOutbox: true, MountHTTP: bil.MountHTTP, Consumers: bil.Consumers(), Jobs: bil.Jobs()},
		{Name: payments.Name, HasOutbox: true, MountHTTP: pay.MountHTTP, Consumers: pay.Consumers(), Jobs: pay.Jobs()},
		{Name: orchestration.Name, HasOutbox: true, MountHTTP: orc.MountHTTP, Consumers: orc.Consumers(), Jobs: orc.Jobs()},
		integrationSet(intg),
		{Name: audit.Name, MountHTTP: aud.MountHTTP, Ports: func(p *httpx.Ports) { p.AuditWriter = aud }},
	}, nil
}

// integrationSet は integration の組み立て（api と worker-integration だけが鍵を持つ。worker-billing はマイグレーションと Relay の対象だけ）。
func integrationSet(m *integration.Module) ModuleSet {
	set := ModuleSet{Name: integration.Name, HasOutbox: true}
	if m == nil {
		return set
	}
	set.MountHTTP, set.Consumers, set.Jobs = m.MountHTTP, m.Consumers(), m.Jobs()
	for _, l := range m.Loops() {
		set.Loops = append(set.Loops, Loop{Process: "worker-integration", Run: l})
	}
	return set
}

// tenantPorts は認証・認可のミドルウェアに tenant の contracts を渡す（§4.3）。
func tenantPorts(svc tenantcontracts.Service) func(*httpx.Ports) {
	return func(p *httpx.Ports) {
		p.AuthenticateAPIKey = svc.AuthenticateAPIKey
		p.GetStaffRole = func(ctx context.Context, t sk.TenantID, sub string) (string, error) {
			r, err := svc.GetStaffRole(ctx, t, sub)
			if errors.Is(err, tenantcontracts.ErrStaffNotFound) {
				return "", nil
			}
			return string(r), err
		}
		p.Allows = func(role, perm string) bool {
			return tenantcontracts.Allows(tenantcontracts.Role(role), tenantcontracts.Permission(perm))
		}
		p.IsWritePermission = func(perm string) bool { return tenantcontracts.Permission(perm).IsWrite() }
		p.TenantWritable = func(ctx context.Context, t sk.TenantID) (bool, error) {
			info, err := svc.GetTenant(ctx, t)
			if errors.Is(err, tenantcontracts.ErrTenantNotFound) {
				return false, nil
			}
			return err == nil && info.Status != tenantcontracts.TenantSuspended, err
		}
	}
}

// ports は httpx が使う modules の関数を集める。
func ports(mods []ModuleSet) httpx.Ports {
	var p httpx.Ports
	for _, m := range mods {
		if m.Ports != nil {
			m.Ports(&p)
		}
	}
	return p
}
