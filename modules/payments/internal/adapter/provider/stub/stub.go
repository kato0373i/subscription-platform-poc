// Package stub は PoC の標準の決済代行 StubAdapter（payments.md §4.5）。状態を payments.stub_methods・stub_charges に保存し、
// api と worker-billing の両方から同じ結果を照会できる。トークン・特別な金額・dev の挙動上書きで結果を切り替える。
// 本番のビルドでは登録しない（登録は cmd/internal/wiring の devtools 側。P-IMP-15）。
package stub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/domain"
)

// Adapter は StubAdapter。ID を変えて複数登録できる（AC-PAY-031 の stub_alt）。
type Adapter struct {
	id     provider.ProviderID
	db     *db.ModuleDB
	clock  sk.Clock
	secret []byte
}

// New は StubAdapter を作る。secret は Webhook の署名鍵（STUB_WEBHOOK_SECRET）。
func New(id string, m *db.ModuleDB, clock sk.Clock, secret string) *Adapter {
	return &Adapter{id: provider.ProviderID(id), db: m, clock: clock, secret: []byte(secret)}
}

var (
	_ provider.PaymentProvider  = (*Adapter)(nil)
	_ provider.MethodRegistrar  = (*Adapter)(nil)
	_ provider.WebhookParser    = (*Adapter)(nil)
	_ provider.SettlementLister = (*Adapter)(nil)
	_ provider.MethodDisabler   = (*Adapter)(nil)
)

func (a *Adapter) Key() provider.Key { return provider.Key{Method: c.MethodCard, Provider: a.id} }

func (a *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{AsyncSettlement: true, RefSharing: provider.RefShared, RequiresStoredMethod: true}
}

func (a *Adapter) OrderRefFor(attemptID uuid.UUID) string {
	return string(a.id) + "_" + attemptID.String()
}

func (a *Adapter) scope(ctx context.Context) context.Context {
	return db.WithUnscoped(ctx, "stub_provider")
}

// ---- シナリオ（§4.5 の表） ----

type rule struct {
	collect   provider.OutcomeStatus // Collect の返り値
	truth     string                 // 代行側の真の状態（succeeded / failed / pending / ""=記録しない）
	code      domain.FailureCode
	retryable bool
	brand     string
}

var rules = map[string]rule{
	"ok":           {collect: provider.OutcomeSucceeded, truth: "succeeded", brand: "visa"},
	"decline":      {collect: provider.OutcomeFailed, truth: "failed", code: domain.FailCardDeclined, retryable: true, brand: "visa"},
	"insufficient": {collect: provider.OutcomeFailed, truth: "failed", code: domain.FailInsufficientFunds, retryable: true, brand: "mastercard"},
	"expired":      {collect: provider.OutcomeFailed, truth: "failed", code: domain.FailCardExpired, brand: "visa"},
	"invalid":      {collect: provider.OutcomeFailed, truth: "failed", code: domain.FailInvalidCard, brand: "jcb"},
	"timeout":      {collect: provider.OutcomeUnknown, truth: "succeeded", brand: "visa"},
	"timeout_fail": {collect: provider.OutcomeUnknown, truth: "failed", code: domain.FailCardDeclined, retryable: true, brand: "visa"},
	"timeout_lost": {collect: provider.OutcomeUnknown, truth: "", brand: "visa"},
	"async_ok":     {collect: provider.OutcomePendingAsync, truth: "pending", brand: "visa"},
	"async_fail":   {collect: provider.OutcomePendingAsync, truth: "pending", brand: "visa"},
}

// amountScenario は特別な金額（999,4xx）で決まるシナリオ。
var amountScenario = map[int64]string{
	999401: "decline", 999402: "insufficient", 999403: "expired", 999422: "invalid",
	999408: "timeout", 999409: "timeout_fail", 999410: "timeout_lost", 999202: "async_ok", 999203: "async_fail",
}

// Decide は結果を決める規則（上書き > 金額 > トークン。AC-PAY-010）。override が "" なら上書きなし。
func Decide(override string, amount int64, tokenScenario string) string {
	if override != "" {
		return override
	}
	if s, ok := amountScenario[amount]; ok {
		return s
	}
	if _, ok := rules[tokenScenario]; ok {
		return tokenScenario
	}
	return "ok"
}

// RuleFor はシナリオの規則（テスト用に公開）。provider_down は試行を作らない。
func RuleFor(scenario string) (collect provider.OutcomeStatus, code domain.FailureCode, retryable bool, ok bool) {
	r, found := rules[scenario]
	return r.collect, r.code, r.retryable, found
}

// ---- トークン ----

var tokenPattern = regexp.MustCompile(`^tok_stub_([a-z_]+)_(\d{4})_(\d{4})_([a-z0-9]{8})$`)

// Token はトークンの中身。
type Token struct {
	Scenario string
	Last4    string
	ExpMonth int
	ExpYear  int
}

// ParseToken は `tok_stub_<scenario>_<last4>_<MMYY>_<rand8>` を読む。
func ParseToken(s string) (Token, error) {
	m := tokenPattern.FindStringSubmatch(s)
	if m == nil {
		return Token{}, c.ErrInvalidToken
	}
	mm, _ := strconv.Atoi(m[3][:2])
	yy, _ := strconv.Atoi(m[3][2:])
	if mm < 1 || mm > 12 {
		return Token{}, c.ErrInvalidToken
	}
	return Token{Scenario: m[1], Last4: m[2], ExpMonth: mm, ExpYear: 2000 + yy}, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RegisterMethod は stub_methods にトークンのシナリオを記録する。同じトークンの再使用・reg_reject は ErrInvalidToken。
func (a *Adapter) RegisterMethod(ctx context.Context, in provider.RegisterInput) (provider.RegisteredMethod, error) {
	t, err := ParseToken(in.Token)
	if err != nil || t.Scenario == "reg_reject" {
		return provider.RegisteredMethod{}, c.ErrInvalidToken
	}
	ref := domain.ProviderRef{Provider: string(a.id), CustomerRef: "stub_cus_" + in.CustomerAccountID.String(), MethodRef: "stub_pm_" + randHex(8)}
	err = a.db.InTx(a.scope(ctx), func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO payments.stub_methods (method_ref, token, scenario, created_at) VALUES ($1, $2, $3, $4)`,
			ref.MethodRef, in.Token, t.Scenario, a.clock.Now())
		return err
	})
	if db.IsUniqueViolation(err) {
		return provider.RegisteredMethod{}, c.ErrInvalidToken
	}
	if err != nil {
		return provider.RegisteredMethod{}, fmt.Errorf("%w: %v", provider.ErrNotSent, err) //nolint:errorlint // 原因は詳細として残す
	}
	brand := rules[t.Scenario].brand
	if brand == "" {
		brand = "visa"
	}
	return provider.RegisteredMethod{Ref: ref, Display: domain.CardDisplay{Brand: brand, Last4: t.Last4, ExpMonth: t.ExpMonth, ExpYear: t.ExpYear}}, nil
}

func outcomeOf(truth string, code *string) provider.Outcome {
	switch truth {
	case "succeeded":
		return provider.Outcome{Status: provider.OutcomeSucceeded}
	case "failed":
		fc := domain.FailCardDeclined
		if code != nil {
			fc = domain.FailureCode(*code)
		}
		return provider.Outcome{Status: provider.OutcomeFailed, FailureCode: fc, Retryable: fc.DefaultRetryable(), RawCode: "stub_" + string(fc)}
	case "pending":
		return provider.Outcome{Status: provider.OutcomePendingAsync}
	}
	return provider.Outcome{Status: provider.OutcomeNotFound}
}

// Collect は結果を決めて stub_charges に記録する。同じ代行冪等キーの再送は記録済みの真の状態を返す。
func (a *Adapter) Collect(ctx context.Context, in provider.CollectInput) (provider.Outcome, error) {
	var out provider.Outcome
	down := false
	err := a.db.InTx(a.scope(ctx), func(ctx context.Context, tx db.Tx) error {
		var truth string
		var code *string
		err := tx.QueryRow(ctx, `SELECT true_status, failure_code FROM payments.stub_charges WHERE idempotency_key = $1`, in.IdempotencyKey).Scan(&truth, &code)
		if err == nil {
			out = outcomeOf(truth, code)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var tokenScenario, override string
		var remaining int
		var ov *string
		if err := tx.QueryRow(ctx, `SELECT scenario, behavior_override, override_remaining FROM payments.stub_methods WHERE method_ref = $1 FOR UPDATE`, in.Ref.MethodRef).
			Scan(&tokenScenario, &ov, &remaining); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if ov != nil && remaining > 0 {
			override = *ov
			if _, err := tx.Exec(ctx, `UPDATE payments.stub_methods SET override_remaining = override_remaining - 1 WHERE method_ref = $1`, in.Ref.MethodRef); err != nil {
				return err
			}
		}
		scenario := Decide(override, in.Amount.Int64(), tokenScenario)
		if scenario == "provider_down" {
			down = true // 上書きの残り回数を減らした分はコミットする
			return nil
		}
		r := rules[scenario]
		out = provider.Outcome{Status: r.collect, FailureCode: r.code, Retryable: r.retryable}
		if r.code != "" {
			out.RawCode = "stub_" + string(r.code)
		}
		if r.truth == "" {
			return nil // 代行に届かなかった（timeout_lost）
		}
		var fc *string
		if r.truth == "failed" {
			s := string(r.code)
			fc = &s
		}
		_, err = tx.Exec(ctx, `INSERT INTO payments.stub_charges (order_ref, provider, idempotency_key, method_ref, amount, true_status, failure_code, scenario, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, in.ProviderOrderRef, string(a.id), in.IdempotencyKey, in.Ref.MethodRef, in.Amount.Int64(), r.truth, fc, scenario, a.clock.Now())
		return err
	})
	if err != nil {
		return provider.Outcome{Status: provider.OutcomeUnknown}, nil // 記録できたか分からない＝送ったかもしれない
	}
	if down {
		return provider.Outcome{}, provider.ErrUnavailable
	}
	return out, nil
}

// GetStatus は記録済みの真の状態を返す。記録がなければ not_found。
func (a *Adapter) GetStatus(ctx context.Context, in provider.StatusInput) (provider.Outcome, error) {
	var out provider.Outcome
	err := a.db.ReadTx(a.scope(ctx), func(ctx context.Context, tx db.Tx) error {
		var truth string
		var code *string
		err := tx.QueryRow(ctx, `SELECT true_status, failure_code FROM payments.stub_charges WHERE order_ref = $1`, in.ProviderOrderRef).Scan(&truth, &code)
		if errors.Is(err, pgx.ErrNoRows) {
			out = provider.Outcome{Status: provider.OutcomeNotFound}
			return nil
		}
		out = outcomeOf(truth, code)
		return err
	})
	return out, err
}

// DisableMethod は何もしない。
func (a *Adapter) DisableMethod(context.Context, domain.ProviderRef) error { return nil }

// ---- Webhook（X-Stub-Signature: t=<unix>,v1=<HMAC-SHA256(secret, t + "." + body)>） ----

// webhookBody は stub の通知の本文。
type webhookBody struct {
	EventID     string    `json:"event_id"`
	OrderRef    string    `json:"order_ref"`
	Status      string    `json:"status"` // succeeded | failed
	FailureCode string    `json:"failure_code,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// Sign は署名ヘッダーの値。
func (a *Adapter) Sign(body []byte, at time.Time) string {
	t := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(t + "." + string(body)))
	return "t=" + t + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

const signatureTolerance = 5 * time.Minute

// ParseWebhook は署名を検証して通知を読む（時刻差は実時間で 5 分まで）。
func (a *Adapter) ParseWebhook(_ context.Context, h http.Header, body []byte) ([]provider.ProviderEvent, error) {
	var t, v1 string
	for _, part := range strings.Split(h.Get("X-Stub-Signature"), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			t = v
		case "v1":
			v1 = v
		}
	}
	unix, err := strconv.ParseInt(t, 10, 64)
	if err != nil || v1 == "" {
		return nil, provider.ErrInvalidSignature
	}
	if d := time.Since(time.Unix(unix, 0)); d > signatureTolerance || d < -signatureTolerance { //nolint:forbidigo // 署名の鮮度は実時間で判定する
		return nil, provider.ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(t + "." + string(body)))
	want, _ := hex.DecodeString(v1)
	if !hmac.Equal(mac.Sum(nil), want) {
		return nil, provider.ErrInvalidSignature
	}
	var b webhookBody
	if err := json.Unmarshal(body, &b); err != nil || b.EventID == "" || b.OrderRef == "" {
		return nil, provider.ErrInvalidSignature
	}
	code := b.FailureCode
	var fc *string
	if code != "" {
		fc = &code
	}
	return []provider.ProviderEvent{{ProviderEventID: b.EventID, ProviderOrderRef: b.OrderRef, Outcome: outcomeOf(b.Status, fc), OccurredAt: b.OccurredAt, Raw: body}}, nil
}

// ---- 日次照合 ----

// ListSettlements は期間の stub_charges を order_ref の順に返す（cursor は最後の order_ref）。
func (a *Adapter) ListSettlements(ctx context.Context, from, to time.Time, cursor string, pageSize int) ([]provider.Settlement, string, error) {
	var out []provider.Settlement
	err := a.db.ReadTx(a.scope(ctx), func(ctx context.Context, tx db.Tx) error {
		rows, err := tx.Query(ctx, `SELECT order_ref, amount, true_status FROM payments.stub_charges
			WHERE provider = $1 AND created_at >= $2 AND created_at < $3 AND order_ref > $4 ORDER BY order_ref LIMIT $5`,
			string(a.id), from, to, cursor, pageSize)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref, truth string
			var amount int64
			if err := rows.Scan(&ref, &amount, &truth); err != nil {
				return err
			}
			st := outcomeOf(truth, nil).Status
			out = append(out, provider.Settlement{ProviderOrderRef: ref, Amount: sk.MustMoney(amount), Status: st})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", fmt.Errorf("stub: list settlements: %w", err)
	}
	next := ""
	if len(out) == pageSize {
		next = out[len(out)-1].ProviderOrderRef
	}
	return out, next, nil
}

// ---- dev の操作（§7.8） ----

// ErrNotPending は確定待ちでない決済を確定しようとした。
var ErrNotPending = errors.New("stub: charge is not pending")

// Settle は確定待ちの決済を確定し、署名した通知の本文とヘッダーを返す（呼び出し元が Webhook の経路に流す）。
// result が "" ならシナリオの既定（async_ok → succeeded、async_fail → failed(card_declined)）。
func (a *Adapter) Settle(ctx context.Context, orderRef, result, failureCode string) ([]byte, http.Header, error) {
	var body []byte
	err := a.db.InTx(a.scope(ctx), func(ctx context.Context, tx db.Tx) error {
		var truth, scenario string
		err := tx.QueryRow(ctx, `SELECT true_status, scenario FROM payments.stub_charges WHERE order_ref = $1 FOR UPDATE`, orderRef).Scan(&truth, &scenario)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && truth != "pending") {
			return ErrNotPending
		}
		if err != nil {
			return err
		}
		if result == "" {
			result = "succeeded"
			if scenario == "async_fail" {
				result = "failed"
			}
		}
		var fc *string
		if result == "failed" {
			if failureCode == "" {
				failureCode = string(domain.FailCardDeclined)
			}
			fc = &failureCode
		}
		now := a.clock.Now()
		if _, err := tx.Exec(ctx, `UPDATE payments.stub_charges SET true_status = $2, failure_code = $3, settled_at = $4 WHERE order_ref = $1`, orderRef, result, fc, now); err != nil {
			return err
		}
		body, err = json.Marshal(webhookBody{EventID: "evt_stub_" + randHex(8), OrderRef: orderRef, Status: result, FailureCode: failureCode, OccurredAt: now.UTC()})
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	h := http.Header{}
	h.Set("X-Stub-Signature", a.Sign(body, time.Now())) //nolint:forbidigo // 署名の時刻は実時間
	return body, h, nil
}

// SetBehavior は支払方法の結果を remaining 回だけ scenario で上書きする（0 で解除）。
func (a *Adapter) SetBehavior(ctx context.Context, methodRef, scenario string, remaining int) error {
	if _, ok := rules[scenario]; !ok && scenario != "provider_down" {
		return fmt.Errorf("stub: unknown scenario %q", scenario)
	}
	return a.db.InTx(a.scope(ctx), func(ctx context.Context, tx db.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE payments.stub_methods SET behavior_override = $2, override_remaining = $3 WHERE method_ref = $1`, methodRef, scenario, remaining)
		if err == nil && tag.RowsAffected() == 0 {
			return c.ErrPaymentMethodNotFound
		}
		return err
	})
}
