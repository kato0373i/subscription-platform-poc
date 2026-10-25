package mail_test

import (
	"strings"
	"testing"

	"subscription-platform/modules/integration/internal/adapter/mail"
	"subscription-platform/modules/integration/internal/domain"
)

// AC-INT-033：tenant_suspended は事業者の停止による終了の文面（取り消しの案内なし）で effective_date を含む。customer は通常の文面。
func TestCancellationScheduledWording(t *testing.T) {
	base := map[string]any{"customer_name": "山田", "tenant_name": "ヨガ教室", "effective_date": "2026-11-14", "mypage_url": "http://localhost/mypage"}
	vars := func(reason string) map[string]any {
		m := map[string]any{"reason": reason}
		for k, v := range base {
			m[k] = v
		}
		return m
	}
	subj, body, err := mail.Render(domain.TmplCancellationScheduled, vars("tenant_suspended"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(subj, "サービス提供終了") || !strings.Contains(body, "ヨガ教室 のサービス提供終了に伴い、ご契約は 2026年11月14日 で終了します") ||
		strings.Contains(body, "取り消し") || strings.Contains(body, "mypage") {
		t.Fatalf("tenant_suspended:\n%s\n%s", subj, body)
	}
	subj, body, _ = mail.Render(domain.TmplCancellationScheduled, vars("customer"))
	if !strings.Contains(subj, "解約を受け付けました") || !strings.Contains(body, "2026年11月14日 まで引き続きご利用いただけます") || !strings.Contains(body, "取り消し") {
		t.Fatalf("customer:\n%s\n%s", subj, body)
	}
}

// FR-023 の必須の変数（事業者名・金額・支払方法更新ページのリンク）が文面に入る。
func TestPaymentFailedWording(t *testing.T) {
	subj, body, err := mail.Render(domain.TmplPaymentFailed, map[string]any{
		"customer_name": "山田", "tenant_name": "ヨガ教室", "amount": int64(1980), "next_collection_at": "2026-10-18T01:00:00Z",
		"payment_method_update_url": "http://localhost:8080/checkout/?subscription=s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ヨガ教室", "1,980円", "http://localhost:8080/checkout/?subscription=s1", "2026年10月18日 10:00"} {
		if !strings.Contains(subj+body, want) {
			t.Errorf("%s がない:\n%s\n%s", want, subj, body)
		}
	}
}

func TestAllTemplatesRender(t *testing.T) {
	vars := map[string]any{
		"customer_name": "山田", "tenant_name": "T", "status": "active", "current_period": map[string]any{"start": "2026-10-15", "end": "2026-11-14"},
		"trial_end": "2026-10-21", "first_charge_date": "2026-10-22", "amount": 980.0, "next_renewal_date": "2026-11-15", "reason": "dunning",
		"canceled_on": "2026-10-20", "effective_date": "2026-11-14", "endpoint_url": "https://e", "failed_count": 2, "console_url": "http://c",
		"mypage_url": "http://m", "payment_method_update_url": "http://p", "staff_name": "佐藤", "role_label": "管理者",
	}
	for _, k := range []string{domain.TmplSignupCompleted, domain.TmplTrialWillEnd, domain.TmplRenewalSucceeded, domain.TmplPaymentFailed,
		domain.TmplCancellationScheduled, domain.TmplSubscriptionCanceled, domain.TmplWebhookDeliveryFailed, domain.TmplStaffAdded} {
		subj, body, err := mail.Render(k, vars)
		if err != nil || subj == "" || body == "" || strings.Contains(subj+body, "<no value>") {
			t.Errorf("%s: err=%v\n%s\n%s", k, err, subj, body)
		}
	}
}

func TestYen(t *testing.T) {
	for in, want := range map[int64]string{0: "0円", 980: "980円", 1980: "1,980円", 1000000: "1,000,000円"} {
		if got := mail.Yen(in); got != want {
			t.Errorf("%d: %s", in, got)
		}
	}
}
