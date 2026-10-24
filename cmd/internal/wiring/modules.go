package wiring

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"subscription-platform/internal/platform/config"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog"
	catalogcontracts "subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/customer"
	customercontracts "subscription-platform/modules/customer/contracts"
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
}

// Services は組み立てたモジュールの contracts。
type Services struct {
	Tenant       tenantcontracts.Service
	Customer     customercontracts.Service
	Catalog      catalogcontracts.Service
	Subscription subscriptioncontracts.Service
	Payments     paymentscontracts.Service
}

// modules はプロセスに組み込むモジュール。マイグレーションの順序（tenant → … → audit）で並べる。
func modules(_ context.Context, b *Base, _ string) ([]ModuleSet, error) {
	codec, err := httpx.NewCursorCodec(b.Cfg.CursorKey)
	if err != nil {
		return nil, fmt.Errorf("cursor codec: %w", err)
	}
	t := tenant.New(tenant.Deps{Pool: b.Pool, Clock: b.Clock, Cursors: codec})
	c := customer.New(customer.Deps{Pool: b.Pool, Clock: b.Clock})
	cat := catalog.New(catalog.Deps{Pool: b.Pool, Clock: b.Clock, Cursors: codec, Tenants: t.Service(), ReservationTTL: b.Cfg.CatalogCouponReservationTTL})
	sub := subscription.New(subscription.Deps{Pool: b.Pool, Clock: b.Clock, Cursors: codec, Catalog: cat.Service()})
	pay, err := payments.New(payments.Deps{
		Pool: b.Pool, Clock: b.Clock, Cursors: codec, Methods: b.Cfg.PaymentsMethods, EnableStub: config.DevtoolsBuild, StubSecret: b.Cfg.StubWebhookSecret,
		FincodeAPIBaseURL: b.Cfg.FincodeAPIBaseURL, FincodeSecretKey: b.Cfg.FincodeSecretKey, FincodePublicKey: b.Cfg.FincodePublicKey,
		SendProviderEvent: func(ctx context.Context, body string) error {
			return b.SendToQueue(ctx, "payments-provider-events", body)
		},
	})
	if err != nil {
		return nil, err
	}
	b.Services = Services{Tenant: t.Service(), Customer: c.Service(), Catalog: cat.Service(), Subscription: sub.Service(), Payments: pay.Service()}
	b.devMounts = append(b.devMounts, pay.MountDev)
	return []ModuleSet{
		{Name: tenant.Name, HasOutbox: true, MountHTTP: t.MountHTTP, Ports: tenantPorts(t.Service())},
		{Name: customer.Name, HasOutbox: true, MountHTTP: c.MountHTTP, Ports: func(p *httpx.Ports) { p.ResolveConsumer = c.ResolveConsumer }},
		{Name: catalog.Name, HasOutbox: true, MountHTTP: cat.MountHTTP, Jobs: cat.Jobs()},
		{Name: subscription.Name, HasOutbox: true, MountHTTP: sub.MountHTTP, Jobs: sub.Jobs()},
		{Name: payments.Name, HasOutbox: true, MountHTTP: pay.MountHTTP, Consumers: pay.Consumers(), Jobs: pay.Jobs()},
	}, nil
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
