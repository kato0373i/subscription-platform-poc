package domain

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// テンプレートの種類（§5.3）。
const (
	TmplSignupCompleted       = "signup_completed"
	TmplTrialWillEnd          = "trial_will_end"
	TmplRenewalSucceeded      = "renewal_succeeded"
	TmplPaymentFailed         = "payment_failed"
	TmplCancellationScheduled = "cancellation_scheduled"
	TmplSubscriptionCanceled  = "subscription_canceled"
	TmplWebhookDeliveryFailed = "webhook_delivery_failed"
	TmplStaffAdded            = "staff_added" // 既存の Cognito アカウントの人を事業者に追加した（business-console FR-006）
)

// EmailEventTypes は integration-email が購読する内部の event_type（deploy/messaging.yaml と一致させる）。
var EmailEventTypes = []string{
	"subscription.started", "subscription.trial_will_end", "subscription.renewed", "subscription.past_due",
	"subscription.cancellation_scheduled", "subscription.canceled", "billing.invoice_issued", "billing.invoice_payment_failed", "customer.withdrawn",
	"tenant.staff_added",
}

// Recipient は宛先の種類。
type Recipient string

const (
	RecipientCustomer    Recipient = "customer"
	RecipientTenantOwner Recipient = "tenant_owner"
	RecipientStaff       Recipient = "staff"
)

// EmailPlan は購読処理が作るメールの行（個人情報を含めない変数だけ）。
type EmailPlan struct {
	TemplateKey    string
	DedupeKey      string
	Recipient      Recipient
	TenantID       sk.TenantID
	CustomerID     *sk.CustomerAccountID
	SubscriptionID *sk.SubscriptionID
	InvoiceID      *sk.InvoiceID
	EndpointID     *EndpointID
	StaffID        *string
	Variables      map[string]any
}

type subSnapshot struct {
	SubscriptionID    sk.SubscriptionID    `json:"subscription_id"`
	TenantID          sk.TenantID          `json:"tenant_id"`
	CustomerAccountID sk.CustomerAccountID `json:"customer_account_id"`
	Status            string               `json:"status"`
	TrialEnd          *sk.Date             `json:"trial_end"`
	NextRenewalDate   *sk.Date             `json:"next_renewal_date"`
	CanceledAt        *time.Time           `json:"canceled_at"`
	CurrentPeriod     struct {
		Start sk.Date `json:"start"`
		End   sk.Date `json:"end"`
	} `json:"current_period"`
	Cancellation *struct {
		EffectiveDate sk.Date `json:"effective_date"`
	} `json:"cancellation"`
}

type subPayload struct {
	Subscription  *subSnapshot  `json:"subscription"`
	InvoiceID     *sk.InvoiceID `json:"invoice_id"`
	Reason        string        `json:"reason"`
	EffectiveDate *sk.Date      `json:"effective_date"`
	TrialEnd      *sk.Date      `json:"trial_end"`
}

// PlanEmail は内部イベントからメールの行を決める（§5.3。AC-INT-025）。メールを作らないイベントは nil。
func PlanEmail(env sk.EventEnvelope) (*EmailPlan, error) {
	var p subPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil, fmt.Errorf("integration: payload: %w", err)
	}
	s := p.Subscription
	if s == nil {
		return nil, nil
	}
	sid, cid := s.SubscriptionID, s.CustomerAccountID
	plan := &EmailPlan{Recipient: RecipientCustomer, TenantID: s.TenantID, CustomerID: &cid, SubscriptionID: &sid, Variables: map[string]any{}}
	period := map[string]any{"start": s.CurrentPeriod.Start.String(), "end": s.CurrentPeriod.End.String()}
	v := plan.Variables
	switch env.EventType {
	case "subscription.started":
		plan.TemplateKey, plan.DedupeKey = TmplSignupCompleted, "signup_completed:"+sid.String()
		v["status"], v["current_period"] = s.Status, period
		if s.TrialEnd != nil {
			v["trial_end"], v["first_charge_date"] = s.TrialEnd.String(), s.TrialEnd.AddDays(1).String() // D-12
		}
	case "subscription.trial_will_end":
		te := s.TrialEnd
		if p.TrialEnd != nil {
			te = p.TrialEnd
		}
		if te == nil {
			return nil, nil
		}
		plan.TemplateKey, plan.DedupeKey = TmplTrialWillEnd, "trial_will_end:"+sid.String()+":"+te.String()
		v["trial_end"], v["first_charge_date"] = te.String(), te.AddDays(1).String()
	case "subscription.renewed":
		if p.InvoiceID == nil {
			return nil, nil
		}
		plan.TemplateKey, plan.DedupeKey, plan.InvoiceID = TmplRenewalSucceeded, "renewal_succeeded:"+p.InvoiceID.String(), p.InvoiceID
		v["current_period"] = period
		if s.NextRenewalDate != nil {
			v["next_renewal_date"] = s.NextRenewalDate.String()
		}
	case "subscription.past_due":
		if p.InvoiceID == nil {
			return nil, nil
		}
		plan.TemplateKey, plan.DedupeKey, plan.InvoiceID = TmplPaymentFailed, "payment_failed:"+p.InvoiceID.String(), p.InvoiceID
	case "subscription.cancellation_scheduled":
		plan.TemplateKey = TmplCancellationScheduled
		plan.DedupeKey = "cancellation_scheduled:" + sid.String() + ":" + strconv.FormatInt(env.AggregateVersion, 10)
		v["reason"] = scheduledReason(p.Reason)
		switch {
		case p.EffectiveDate != nil:
			v["effective_date"] = p.EffectiveDate.String()
		case s.Cancellation != nil:
			v["effective_date"] = s.Cancellation.EffectiveDate.String()
		}
	case "subscription.canceled":
		plan.TemplateKey, plan.DedupeKey = TmplSubscriptionCanceled, "subscription_canceled:"+sid.String()
		v["reason"] = canceledReason(p.Reason)
		if s.CanceledAt != nil {
			v["canceled_on"] = sk.DateOf(*s.CanceledAt).String()
		} else {
			v["canceled_on"] = sk.DateOf(env.OccurredAt).String()
		}
	default:
		return nil, nil
	}
	return plan, nil
}

// scheduledReason は cancellation_scheduled の文面の分岐（reason がなければ customer。P-INT-6）。
func scheduledReason(r string) string {
	if r == "tenant_suspended" {
		return r
	}
	return "customer"
}

func canceledReason(r string) string {
	switch r {
	case "dunning", "tenant_suspended":
		return r
	default:
		return "customer"
	}
}

// WebhookFailedPlan は配信が failed になったときのオーナー宛てのメール（1 送信先 1 日 1 通。JST の日付）。
func WebhookFailedPlan(tenant sk.TenantID, ep EndpointID, now time.Time) *EmailPlan {
	id := ep
	return &EmailPlan{
		TemplateKey: TmplWebhookDeliveryFailed, DedupeKey: "webhook_delivery_failed:" + ep.String() + ":" + sk.DateOf(now).String(),
		Recipient: RecipientTenantOwner, TenantID: tenant, EndpointID: &id, Variables: map[string]any{},
	}
}

// EmailRetryWaits はメールの一時エラーの再送間隔（§8.3。最後を繰り返す）。
var EmailRetryWaits = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// EmailGiveUp は最初の作成からこの時間を過ぎたら failed。
const EmailGiveUp = 24 * time.Hour

// EmailNextAttempt は n 回目（1 始まり）の一時エラーの後の時刻。ok=false なら打ち切り（failed）。
func EmailNextAttempt(createdAt, now time.Time, n int) (time.Time, bool) {
	next := now.Add(EmailRetryWaits[min(n, len(EmailRetryWaits))-1])
	if next.Sub(createdAt) > EmailGiveUp {
		return time.Time{}, false
	}
	return next, true
}

// StaffAddedPlan はスタッフの追加の通知（宛先はスタッフ。1 つの所属に 1 通）。
func StaffAddedPlan(tenant sk.TenantID, staffID, role string) *EmailPlan {
	id := staffID
	return &EmailPlan{
		TemplateKey: TmplStaffAdded, DedupeKey: "staff_added:" + staffID, Recipient: RecipientStaff, TenantID: tenant, StaffID: &id,
		Variables: map[string]any{"role": role},
	}
}
