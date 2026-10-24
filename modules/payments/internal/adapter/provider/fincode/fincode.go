// Package fincode は FincodeCardAdapter の骨格（payments.md §4.5、P-PAY-5）。
// 要確認点（顧客・カードの共有、冪等キー、注文 ID、レート制限、Webhook の署名、照会 API、エラーコード）が解消するまで、
// 全メソッドは ErrNotImplemented を返す。認証情報が設定されていなければレジストリに登録しない。
package fincode

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/provider"
)

// Config は fincode の設定（環境変数 FINCODE_*）。
type Config struct {
	APIBaseURL string // テスト環境と本番で別（要確認）
	SecretKey  string
	PublicKey  string // ブラウザのトークナイザー用（申込ページの payment-config で返す）
}

// Configured は認証情報があるか。
func (c Config) Configured() bool { return c.SecretKey != "" && c.APIBaseURL != "" }

// Adapter は FincodeCardAdapter。
type Adapter struct{ cfg Config }

func New(cfg Config) *Adapter { return &Adapter{cfg: cfg} }

var (
	_ provider.PaymentProvider = (*Adapter)(nil)
	_ provider.MethodRegistrar = (*Adapter)(nil)
	_ provider.WebhookParser   = (*Adapter)(nil)
)

func (a *Adapter) Key() provider.Key { return provider.Key{Method: c.MethodCard, Provider: "fincode"} }

// Capabilities は要確認点の解消まで「共有できる」前提にしておく（できなければ per_tenant と EnsureTenantRef を実装する）。
func (a *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{AsyncSettlement: false, RefSharing: provider.RefShared, RequiresStoredMethod: true}
}

// OrderRefFor は TODO（要確認：自社で注文 ID を指定できるか、長さ・文字種の制約）。
func (a *Adapter) OrderRefFor(attemptID uuid.UUID) string { return "fc_" + attemptID.String() }

// Collect は TODO（要確認：決済の登録 → 実行の 2 段階、テナントショップの指定方法、冪等キー）。
func (a *Adapter) Collect(context.Context, provider.CollectInput) (provider.Outcome, error) {
	return provider.Outcome{}, provider.ErrNotImplemented
}

// GetStatus は TODO（要確認：注文 ID での照会 API）。
func (a *Adapter) GetStatus(context.Context, provider.StatusInput) (provider.Outcome, error) {
	return provider.Outcome{}, provider.ErrNotImplemented
}

// RegisterMethod は TODO（要確認：顧客・カードの登録 API と、事業者ショップ間での共有）。
func (a *Adapter) RegisterMethod(context.Context, provider.RegisterInput) (provider.RegisteredMethod, error) {
	return provider.RegisteredMethod{}, provider.ErrNotImplemented
}

// ParseWebhook は TODO（要確認：署名の方式・ヘッダー名・イベントの種類・イベント ID）。
func (a *Adapter) ParseWebhook(context.Context, http.Header, []byte) ([]provider.ProviderEvent, error) {
	return nil, provider.ErrNotImplemented
}
