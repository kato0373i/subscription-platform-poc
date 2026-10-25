// Package fincode は FincodeCardAdapter（payments.md §4.5、P-PAY-5・P-IMP-21）。fincode API 1.4.0（docs.fincode.jp の OpenAPI 仕様）に合わせる。
//
//   - 決済は「登録（POST /v1/payments、オーダー ID を自社で指定）→ 実行（PUT /v1/payments/{id}、顧客 ID・カード ID）」の 2 段階を 1 つの Collect にまとめる。
//     登録だけ成功して実行で不明になった場合も unknown として扱い、照会（GET /v1/payments/{id}）で確定する。
//   - テナントショップは `Tenant-Shop-Id` ヘッダー、冪等キーは `idempotent_key` ヘッダー（UUIDv4、有効 30 分）。30 分を過ぎた再送は、
//     オーダー ID の重複（E0110025014）と決済済み（E9993134002）のエラーを照会に切り替えて判定する。
//   - 顧客・カードはプラットフォームで共有する契約（shared_customer_flag=1）だけに対応する（RefShared）。共有しない契約では起動しない。
//   - Webhook は `Fincode-Signature` ヘッダーに、Webhook 設定で決めた署名の文字列がそのまま入る（定数時間で比較する）。
package fincode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/observability"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/domain"
)

// Config は fincode の設定（環境変数 FINCODE_*）。
type Config struct {
	APIBaseURL       string // テスト環境 https://api.test.fincode.jp、本番 https://api.fincode.jp
	SecretKey        string
	PublicKey        string // ブラウザのトークナイザー（fincode JS）用。申込ページの payment-config で返す
	WebhookSignature string // Webhook 設定の signature（Fincode-Signature ヘッダーの値）
	SharedCustomer   bool   // プラットフォームの shared_customer_flag=1（顧客・カードをテナント間で共有する）
	HTTPClient       *http.Client
}

// Configured は認証情報があるか。
func (c Config) Configured() bool { return c.SecretKey != "" && c.APIBaseURL != "" }

// ErrPerTenantUnsupported は顧客を共有しない契約（shared_customer_flag=0）。PoC では対応しない（P-IMP-21）。
var ErrPerTenantUnsupported = errors.New("fincode: shared_customer_flag=0 is not supported")

// Adapter は FincodeCardAdapter。
type Adapter struct {
	cfg    Config
	client *http.Client
}

// New はアダプターを作る。顧客を共有しない契約ではエラー。
func New(cfg Config) (*Adapter, error) {
	if !cfg.SharedCustomer {
		return nil, ErrPerTenantUnsupported
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Adapter{cfg: cfg, client: observability.NewHTTPClient("fincode", hc)}, nil
}

var (
	_ provider.PaymentProvider = (*Adapter)(nil)
	_ provider.MethodRegistrar = (*Adapter)(nil)
	_ provider.WebhookParser   = (*Adapter)(nil)
	_ provider.MethodDisabler  = (*Adapter)(nil)
	_ provider.WebhookAcker    = (*Adapter)(nil)
)

func (a *Adapter) Key() provider.Key { return provider.Key{Method: c.MethodCard, Provider: "fincode"} }

func (a *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{AsyncSettlement: false, RefSharing: provider.RefShared, RequiresStoredMethod: true}
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// OrderRefFor はオーダー ID（30 文字まで）。"o" + 試行 ID の 128 ビットを Crockford base32 にした 26 文字。
func (a *Adapter) OrderRefFor(attemptID uuid.UUID) string {
	var hi, lo uint64
	for i := range 8 {
		hi = hi<<8 | uint64(attemptID[i])
		lo = lo<<8 | uint64(attemptID[8+i])
	}
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | (hi&31)<<59
		hi >>= 5
	}
	return "o" + string(out)
}

// idempotentKey は fincode の冪等キー（UUIDv4 の形）。試行と処理の種類から決まる値にし、再送で同じキーになるようにする。
func idempotentKey(base, purpose string) string {
	sum := sha256.Sum256([]byte(base + ":" + purpose))
	var u uuid.UUID
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x40 // version 4
	u[8] = (u[8] & 0x3f) | 0x80 // variant RFC 4122
	return u.String()
}

// CustomerID は契約者アカウントに対応する fincode の顧客 ID（60 文字まで）。
func CustomerID(account uuid.UUID) string {
	return "c_" + strings.ReplaceAll(account.String(), "-", "")
}

// ---- HTTP ----

type apiError struct {
	Status int
	Codes  []string
}

func (e *apiError) Error() string { return fmt.Sprintf("fincode: http %d %v", e.Status, e.Codes) }

func (e *apiError) has(code string) bool {
	for _, c := range e.Codes {
		if c == code {
			return true
		}
	}
	return false
}

// sendError は送信の途中の失敗（タイムアウト・接続の切断）。相手に届いたか分からない。
type sendError struct{ err error }

func (e *sendError) Error() string { return "fincode: send: " + e.err.Error() }
func (e *sendError) Unwrap() error { return e.err }

type request struct {
	method, path string
	query        string
	body         any
	tenantShop   *string
	idemKey      string
}

func (a *Adapter) do(ctx context.Context, r request, out any) error {
	var body io.Reader
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	url := strings.TrimRight(a.cfg.APIBaseURL, "/") + r.path
	if r.query != "" {
		url += "?" + r.query
	}
	req, err := http.NewRequestWithContext(ctx, r.method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.SecretKey)
	req.Header.Set("Api-Version", "20211001")
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	}
	if r.tenantShop != nil && *r.tenantShop != "" {
		req.Header.Set("Tenant-Shop-Id", *r.tenantShop)
	}
	if r.idemKey != "" {
		req.Header.Set("idempotent_key", r.idemKey)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return &sendError{err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &sendError{err}
	}
	if resp.StatusCode >= 300 {
		ae := &apiError{Status: resp.StatusCode}
		var eb struct {
			Errors []struct {
				Code string `json:"error_code"`
			} `json:"errors"`
		}
		if json.Unmarshal(raw, &eb) == nil {
			for _, e := range eb.Errors {
				ae.Codes = append(ae.Codes, e.Code)
			}
		}
		return ae
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("fincode: decode response: %w", err)
		}
	}
	return nil
}

// unavailable は代行が受け付けていない（流量制限の 403、メンテナンスの 503）。
func unavailable(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && (ae.Status == http.StatusForbidden || ae.Status == http.StatusServiceUnavailable)
}

// ---- 決済 ----

type payment struct {
	ID        string `json:"id"`
	AccessID  string `json:"access_id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code"`
	Amount    int64  `json:"amount"`
}

// outcomeOf は決済の状態を結果にする。
func outcomeOf(p payment) provider.Outcome {
	switch p.Status {
	case "CAPTURED", "AUTHORIZED":
		return provider.Outcome{Status: provider.OutcomeSucceeded, RawCode: p.Status}
	case "CANCELED", "EXPIRED", "FAILED":
		return failure(p.ErrorCode)
	case "UNPROCESSED", "AUTHENTICATED", "CHECKED":
		if p.ErrorCode != "" {
			return failure(p.ErrorCode) // 実行して拒否された（状態は未処理のまま）
		}
		return provider.Outcome{Status: provider.OutcomeNotFound, RawCode: p.Status} // 登録だけで実行していない
	default:
		return provider.Outcome{Status: provider.OutcomeUnknown, RawCode: p.Status}
	}
}

// failure はカード会社の拒否（E9993*）などのエラーコードを失敗の分類にする（docs.fincode.jp のエラーコード一覧）。
func failure(code string) provider.Outcome {
	f := domain.FailCardDeclined
	switch code {
	case "E9993134010", "E9993134011": // 残高不足・限度額超過
		f = domain.FailInsufficientFunds
	case "E9993134032": // 有効期限の誤り
		f = domain.FailCardExpired
	case "E9993134005", "E9993134007", "E9993134016", "E0006120002", "E0008120002": // カード番号の誤り・利用できないカード
		f = domain.FailInvalidCard
	case "E9993134006", "E9993134008", "E9993134009", "E9993134999": // しばらく時間をあけて再度
		f = domain.FailProcessingError
	case "E0007011002", "E0009011002", "E0002003002", "E0003003002": // カード・顧客が存在しない
		f = domain.FailPaymentMethodUnavailable
	}
	return provider.Outcome{Status: provider.OutcomeFailed, FailureCode: f, Retryable: f.DefaultRetryable(), RawCode: code}
}

// Collect は登録 → 実行。登録で失敗したら決済は起きていない（ErrNotSent）。実行を送った後の不明は unknown。
func (a *Adapter) Collect(ctx context.Context, in provider.CollectInput) (provider.Outcome, error) {
	var reg payment
	err := a.do(ctx, request{
		method: http.MethodPost, path: "/v1/payments", tenantShop: in.TenantShopID, idemKey: idempotentKey(in.IdempotencyKey, "regist"),
		body: map[string]any{"id": in.ProviderOrderRef, "pay_type": "Card", "job_code": "CAPTURE", "amount": strconv.FormatInt(in.Amount.Int64(), 10)},
	}, &reg)
	var ae *apiError
	switch {
	case err == nil:
	case errors.As(err, &ae) && ae.has("E0110025014"):
		// 冪等キーの期限（30 分）を過ぎた再送：同じオーダー ID は登録済み。照会して続きを決める
		cur, gerr := a.get(ctx, in.ProviderOrderRef, in.TenantShopID)
		if gerr != nil {
			return provider.Outcome{Status: provider.OutcomeUnknown}, nil
		}
		if o := outcomeOf(cur); o.Status != provider.OutcomeNotFound {
			return o, nil
		}
		reg = cur
	case unavailable(err):
		return provider.Outcome{}, provider.ErrUnavailable
	default:
		return provider.Outcome{}, fmt.Errorf("%w: %w", provider.ErrNotSent, err) // 登録は決済を起こさない
	}

	var res payment
	err = a.do(ctx, request{
		method: http.MethodPut, path: "/v1/payments/" + in.ProviderOrderRef, tenantShop: in.TenantShopID, idemKey: idempotentKey(in.IdempotencyKey, "exec"),
		body: map[string]any{"pay_type": "Card", "access_id": reg.AccessID, "customer_id": in.Ref.CustomerRef, "card_id": in.Ref.MethodRef, "method": "1"},
	}, &res)
	var se *sendError
	switch {
	case err == nil:
		return outcomeOf(res), nil
	case errors.As(err, &ae) && ae.has("E9993134002"):
		// この取引はすでに決済が終了している（前の実行が届いていた）
		cur, gerr := a.get(ctx, in.ProviderOrderRef, in.TenantShopID)
		if gerr != nil {
			return provider.Outcome{Status: provider.OutcomeUnknown}, nil
		}
		return outcomeOf(cur), nil
	case errors.As(err, &ae) && ae.Status < 500 && len(ae.Codes) > 0:
		return failure(ae.Codes[0]), nil
	case errors.As(err, &se), errors.As(err, &ae):
		return provider.Outcome{Status: provider.OutcomeUnknown}, nil // 5xx・タイムアウト：実行されたか分からない
	default:
		return provider.Outcome{Status: provider.OutcomeUnknown}, nil
	}
}

func (a *Adapter) get(ctx context.Context, orderRef string, tenantShop *string) (payment, error) {
	var p payment
	err := a.do(ctx, request{method: http.MethodGet, path: "/v1/payments/" + orderRef, query: "pay_type=Card", tenantShop: tenantShop}, &p)
	return p, err
}

// GetStatus はオーダー ID で照会する。存在しなければ not_found。
func (a *Adapter) GetStatus(ctx context.Context, in provider.StatusInput) (provider.Outcome, error) {
	p, err := a.get(ctx, in.ProviderOrderRef, in.TenantShopID)
	var ae *apiError
	switch {
	case err == nil:
		return outcomeOf(p), nil
	case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
		return provider.Outcome{Status: provider.OutcomeNotFound}, nil
	case errors.As(err, &ae) && ae.Status == http.StatusBadRequest && len(ae.Codes) > 0 && strings.HasSuffix(ae.Codes[0], "002"):
		return provider.Outcome{Status: provider.OutcomeNotFound, RawCode: ae.Codes[0]}, nil // 決済情報が存在しない
	case unavailable(err):
		return provider.Outcome{}, provider.ErrUnavailable
	default:
		return provider.Outcome{Status: provider.OutcomeUnknown}, nil
	}
}

// ---- 支払方法 ----

type card struct {
	ID     string `json:"id"`
	CardNo string `json:"card_no"`
	Expire string `json:"expire"` // YYMM
	Brand  string `json:"brand"`
}

// RegisterMethod は顧客（なければ作る）にトークンのカードを登録する。顧客はプラットフォームで共有する。
func (a *Adapter) RegisterMethod(ctx context.Context, in provider.RegisterInput) (provider.RegisteredMethod, error) {
	cid := CustomerID(in.CustomerAccountID.UUID())
	err := a.do(ctx, request{method: http.MethodPost, path: "/v1/customers", idemKey: idempotentKey(cid, "customer"), body: map[string]any{"id": cid}}, nil)
	var ae *apiError
	switch {
	case err == nil, errors.As(err, &ae) && ae.has("E0001003014"): // 作った、または登録済み
	case unavailable(err):
		return provider.RegisteredMethod{}, provider.ErrUnavailable
	default:
		return provider.RegisteredMethod{}, err
	}
	var cd card
	err = a.do(ctx, request{
		method: http.MethodPost, path: "/v1/customers/" + cid + "/cards", idemKey: idempotentKey(in.IdempotencyKey+in.Token, "card"),
		body: map[string]any{"token": in.Token, "default_flag": "1"},
	}, &cd)
	switch {
	case err == nil:
	case errors.As(err, &ae) && (ae.has("E0006102002") || ae.has("E0006102A01") || ae.has("E0006102A02") || ae.has("E0006120002")):
		return provider.RegisteredMethod{}, c.ErrInvalidToken // 存在しない・利用済み・期限切れのトークン、誤ったカード番号
	case unavailable(err):
		return provider.RegisteredMethod{}, provider.ErrUnavailable
	default:
		return provider.RegisteredMethod{}, err
	}
	disp := domain.CardDisplay{Brand: strings.ToLower(cd.Brand)}
	if n := len(cd.CardNo); n >= 4 {
		disp.Last4 = cd.CardNo[n-4:]
	}
	if len(cd.Expire) == 4 {
		yy, _ := strconv.Atoi(cd.Expire[:2])
		mm, _ := strconv.Atoi(cd.Expire[2:])
		disp.ExpYear, disp.ExpMonth = 2000+yy, mm
	}
	return provider.RegisteredMethod{Ref: domain.ProviderRef{Provider: "fincode", CustomerRef: cid, MethodRef: cd.ID}, Display: disp}, nil
}

// DisableMethod は代行側のカードを削除する（退会時のベストエフォート）。
func (a *Adapter) DisableMethod(ctx context.Context, ref domain.ProviderRef) error {
	err := a.do(ctx, request{method: http.MethodDelete, path: "/v1/customers/" + ref.CustomerRef + "/cards/" + ref.MethodRef}, nil)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// ---- Webhook ----

// WebhookAck は fincode が受信成功と判断する応答の本文。
func (a *Adapter) WebhookAck() []byte { return []byte(`{"receive":"0"}`) }

type webhookBody struct {
	Event           string `json:"event"`
	OrderID         string `json:"order_id"`
	AccessID        string `json:"access_id"`
	Status          string `json:"status"`
	ErrorCode       string `json:"error_code"`
	TransactionDate string `json:"transaction_date"`
}

// ParseWebhook は署名を確かめ、カード決済の実行・売上確定の通知だけを正規化する（ほかのイベントは捨てる）。
func (a *Adapter) ParseWebhook(_ context.Context, h http.Header, body []byte) ([]provider.ProviderEvent, error) {
	sig := h.Get("Fincode-Signature")
	if a.cfg.WebhookSignature == "" || subtle.ConstantTimeCompare([]byte(sig), []byte(a.cfg.WebhookSignature)) != 1 {
		return nil, provider.ErrInvalidSignature
	}
	var b webhookBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, fmt.Errorf("fincode: webhook body: %w", err)
	}
	switch b.Event {
	case "payments.card.exec", "payments.card.capture":
	default:
		return nil, nil
	}
	o := outcomeOf(payment{ID: b.OrderID, Status: b.Status, ErrorCode: b.ErrorCode})
	if o.Status != provider.OutcomeSucceeded && o.Status != provider.OutcomeFailed {
		return nil, nil
	}
	at, err := time.ParseInLocation("2006/01/02 15:04:05.000", b.TransactionDate, jst)
	if err != nil {
		at = time.Time{}
	}
	// fincode の通知はイベント ID を持たないので、オーダー ID・イベント・状態から決める（再送では同じ値になる）
	id := b.Event + ":" + b.OrderID + ":" + b.Status + ":" + b.ErrorCode
	return []provider.ProviderEvent{{ProviderEventID: id, ProviderOrderRef: b.OrderID, Outcome: o, OccurredAt: at, Raw: json.RawMessage(body)}}, nil
}

var jst = time.FixedZone("JST", 9*60*60)
