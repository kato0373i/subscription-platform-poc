// Package payments は決済のモジュール（payments.md）。cmd/** からだけ import する（規約 M7）。
// 決済代行のアダプターはここで組み立ててレジストリに登録する（手段・代行を足すときは登録を 1 行足す。§10.1）。
package payments

import (
	"context"
	"errors"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/inbox"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/payments/contracts"
	payhttp "subscription-platform/modules/payments/internal/adapter/http"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/adapter/provider/fincode"
	"subscription-platform/modules/payments/internal/adapter/provider/stub"
	"subscription-platform/modules/payments/internal/app"
)

// Deps はモジュールの依存。
type Deps struct {
	Pool    *db.Pool
	Clock   sk.Clock
	Outbox  []outbox.WriterOption
	Cursors *httpx.CursorCodec
	// Methods は新規登録に使う手段と代行の組（PAYMENTS_METHODS。例 "card:stub"）。
	Methods string
	// EnableStub は StubAdapter を登録するか（devtools ビルドだけ true。本番では登録しない。P-IMP-15）。
	EnableStub bool
	StubSecret string
	// fincode の設定。SecretKey と APIBaseURL がなければ登録しない（P-PAY-5・P-IMP-21）
	FincodeAPIBaseURL, FincodeSecretKey, FincodePublicKey, FincodeWebhookSignature string
	// FincodeSharedCustomer はプラットフォームの shared_customer_flag=1（顧客・カードをテナント間で共有する）。false の契約には対応しない
	FincodeSharedCustomer bool
	Breaker               provider.BreakerConfig
	// SendProviderEvent は通知の処理の依頼を payments-provider-events キューに送る。
	SendProviderEvent func(ctx context.Context, body string) error
}

// Module は payments モジュール。
type Module struct {
	svc        *app.Service
	db         *db.ModuleDB
	cursors    *httpx.CursorCodec
	stub       *stub.Adapter
	publicKeys map[string]string
}

// Name はスキーマ名。
const Name = "payments"

// StubProvider は StubAdapter の代行名。
const StubProvider = "stub"

// Consumer の名前（deploy/messaging.yaml と同じ）。
const (
	ConsumerCustomerWithdrawn = "payments.customer_withdrawn"
	ConsumerProviderEvents    = "payments.provider_events"
)

// New はモジュールを組み立てる。
func New(d Deps) (*Module, error) {
	mdb := d.Pool.Module(Name)
	reg, err := provider.NewRegistry(d.Methods)
	if err != nil {
		return nil, err
	}
	reg.WithDecorator(provider.NewGuard(mdb, d.Clock, d.Breaker).Wrap)
	m := &Module{db: mdb, cursors: d.Cursors, publicKeys: map[string]string{}}
	if d.EnableStub {
		m.stub = stub.New(StubProvider, mdb, d.Clock, d.StubSecret)
		reg.Register(m.stub)
	}
	fc := fincode.Config{APIBaseURL: d.FincodeAPIBaseURL, SecretKey: d.FincodeSecretKey, PublicKey: d.FincodePublicKey,
		WebhookSignature: d.FincodeWebhookSignature, SharedCustomer: d.FincodeSharedCustomer}
	if fc.Configured() {
		a, err := fincode.New(fc)
		if err != nil {
			return nil, err
		}
		reg.Register(a)
		m.publicKeys["fincode"] = fc.PublicKey
	}
	m.svc = app.New(app.Deps{DB: mdb, Outbox: outbox.NewWriter(Name, d.Clock, d.Outbox...), Clock: d.Clock, Registry: reg, SendProviderEvent: d.SendProviderEvent})
	return m, nil
}

// Service は他モジュールと cmd に公開する API。
func (m *Module) Service() contracts.Service { return m.svc }

// MountHTTP は me・checkout・ops の API と Webhook を登録する。
func (m *Module) MountHTTP(r *httpx.Router) error {
	return payhttp.Register(r, m.svc, m.cursors, m.publicKeys)
}

// MountDev は devtools ビルドだけの StubAdapter の操作を登録する（StubAdapter がなければ何もしない）。
func (m *Module) MountDev(r *httpx.Router) error {
	if m.stub == nil {
		return nil
	}
	return payhttp.RegisterDev(r, m.svc, m.stub, StubProvider)
}

// Consumers は退会の購読と、決済代行の通知の処理（worker-billing）。
func (m *Module) Consumers() []messaging.ConsumerSpec {
	return []messaging.ConsumerSpec{
		{Name: ConsumerCustomerWithdrawn, Handler: inbox.Wrap(m.db, ConsumerCustomerWithdrawn, func(ctx context.Context, tx db.Tx, env sk.EventEnvelope) error {
			p, err := sk.DecodePayload[struct {
				CustomerAccountID sk.CustomerAccountID `json:"customer_account_id"`
			}](env)
			if err != nil {
				return messaging.Permanent(err)
			}
			return m.svc.HandleWithdrawal(ctx, tx, p.CustomerAccountID)
		}, inbox.AllowSystemScope("customer_withdrawn"))},
		{Name: ConsumerProviderEvents, Handler: func(ctx context.Context, env sk.EventEnvelope) error {
			key, err := sk.DecodePayload[app.ProviderEventKey](env)
			if err != nil {
				return messaging.Permanent(err)
			}
			return m.svc.ProcessProviderEvent(ctx, key)
		}},
	}
}

// Jobs は照合と掃除のジョブ（worker-billing）。
func (m *Module) Jobs() []scheduler.Job {
	return []scheduler.Job{m.svc.ResolveUnknownJob(), m.svc.ProviderEventsSweepJob(), m.svc.ReconcileDailyJob()}
}

// Problem は payments のエラーを Problem にする（申込 API の配線で使う）。対応がなければ nil。
func Problem(err error) *httpx.Problem { return payhttp.Problem(err) }

// StubSetBehavior は StubAdapter の支払方法の結果を remaining 回だけ上書きする（dev API と他モジュールの結合テスト用。Stub がなければエラー）。
func (m *Module) StubSetBehavior(ctx context.Context, pm sk.PaymentMethodID, scenario string, remaining int) error {
	if m.stub == nil {
		return errors.New("payments: stub adapter is not enabled")
	}
	ref, err := m.svc.MethodRef(ctx, pm, StubProvider)
	if err != nil {
		return err
	}
	return m.stub.SetBehavior(ctx, ref, scenario, remaining)
}
