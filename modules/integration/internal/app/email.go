package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
	customer "subscription-platform/modules/customer/contracts"
	"subscription-platform/modules/integration/internal/adapter/mail"
	"subscription-platform/modules/integration/internal/adapter/postgres"
	"subscription-platform/modules/integration/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/integration/internal/domain"
	tenant "subscription-platform/modules/tenant/contracts"
)

const (
	emailLease      = 60 * time.Second // 送信中の行を隠す時間（§8.3）
	emailRetryEvery = 10 * time.Second
	emailRetryBatch = 50
)

var errSkip = errors.New("integration: recipient unavailable")

// errTemporary は一時エラー（InvoiceSummary 未着など）。
type errTemporary struct{ reason string }

func (e errTemporary) Error() string { return "integration: temporary: " + e.reason }

func (s *Service) logEmailError(ctx context.Context, id uuid.UUID, err error) {
	observability.Logger("integration.email").ErrorContext(ctx, "email.send_failed", slog.String("email_id", id.String()), observability.Err(err))
}

// SendEmail は行の宛先・変数を解決して送り、結果を記録する。呼び出し元が行を取り出し済み（locked_until を設定済み）であること。
func (s *Service) SendEmail(ctx context.Context, id uuid.UUID) error {
	var row sqlcgen.IntegrationEmailNotification
	if err := s.d.DB.ReadTx(db.WithSystemScope(ctx, "email send: load"), func(ctx context.Context, tx db.Tx) error {
		var err error
		row, err = sqlcgen.New(tx).GetEmail(ctx, id)
		return postgres.NotFound(err)
	}); err != nil {
		return err
	}
	if row.Status != "pending" {
		return nil
	}
	tenantID := sk.IDFromUUID[sk.TenantTag](row.TenantID)
	vars := map[string]any{}
	if err := json.Unmarshal(row.Variables, &vars); err != nil {
		return err
	}
	msgs, resolved, err := s.compose(ctx, tenantID, row, vars)
	now := s.d.Clock.Now()
	res := sqlcgen.UpdateEmailResultParams{ID: row.ID, AttemptCount: row.AttemptCount, UpdatedAt: now, Variables: row.Variables}
	var tmp errTemporary
	switch {
	case errors.Is(err, errSkip):
		res.Status, res.LastError = "skipped", strPtr("recipient_unavailable")
	case errors.As(err, &tmp):
		s.retryOrFail(&res, row, now, tmp.reason)
	case err != nil:
		return err
	default:
		var msgID string
		var sendErr error
		for _, m := range msgs {
			var mid string
			if mid, sendErr = s.d.Mailer.Send(ctx, m); sendErr != nil {
				break
			}
			if msgID == "" {
				msgID = mid
			}
		}
		res.AttemptCount++
		switch {
		case errors.Is(sendErr, mail.ErrPermanent):
			res.Status, res.LastError = "failed", strPtr(sendErr.Error())
		case sendErr != nil:
			res.AttemptCount--
			s.retryOrFail(&res, row, now, sendErr.Error())
		default:
			// 送信時に解決した個人情報でない変数と件名を残す（§11.3 の B）。氏名・宛先は残さない
			merged := maps.Clone(vars)
			maps.Copy(merged, resolved)
			b, err := json.Marshal(merged)
			if err != nil {
				return err
			}
			res.Status, res.ProviderMessageID, res.SentAt, res.Subject, res.Variables = "sent", &msgID, &now, &msgs[0].Subject, b
		}
	}
	return s.d.DB.InTx(db.WithTenantScope(ctx, tenantID), func(ctx context.Context, tx db.Tx) error {
		return sqlcgen.New(tx).UpdateEmailResult(ctx, res)
	})
}

func (s *Service) retryOrFail(res *sqlcgen.UpdateEmailResultParams, row sqlcgen.IntegrationEmailNotification, now time.Time, reason string) {
	res.AttemptCount++
	res.LastError = strPtr(truncate(reason, 500))
	if next, ok := domain.EmailNextAttempt(row.CreatedAt, now, int(res.AttemptCount)); ok {
		res.Status, res.NextAttemptAt = "pending", &next
		return
	}
	res.Status = "failed"
}

// compose は宛先と文面を作る。resolved は送信時に解決した個人情報でない変数（行に書き足す）。
func (s *Service) compose(ctx context.Context, t sk.TenantID, row sqlcgen.IntegrationEmailNotification, vars map[string]any) ([]mail.Message, map[string]any, error) {
	cctx := db.ClearScope(ctx)
	info, err := s.d.Tenant.GetTenant(cctx, t)
	if err != nil {
		return nil, nil, errTemporary{"tenant: " + err.Error()}
	}
	resolved := map[string]any{"tenant_name": info.Name}
	if row.SubscriptionID != nil {
		resolved["mypage_url"] = s.d.URLs.Mypage
		resolved["payment_method_update_url"] = strings.ReplaceAll(s.d.URLs.PaymentMethodUpdate, "{subscription_id}", row.SubscriptionID.String())
	}
	if row.InvoiceID != nil && (row.TemplateKey == domain.TmplRenewalSucceeded || row.TemplateKey == domain.TmplPaymentFailed) {
		sum, err := s.invoiceSummary(ctx, t, *row.InvoiceID)
		if err != nil {
			return nil, nil, err
		}
		resolved["amount"] = sum.Total
		if row.TemplateKey == domain.TmplPaymentFailed && sum.NextCollectionAt != nil {
			resolved["next_collection_at"] = sum.NextCollectionAt.UTC().Format(time.RFC3339)
		}
	}
	var to []string
	personal := map[string]any{}
	switch domain.Recipient(row.RecipientType) {
	case domain.RecipientCustomer:
		c, err := s.d.Customer.GetContact(cctx, sk.IDFromUUID[sk.CustomerAccountTag](*row.CustomerAccountID))
		switch {
		case errors.Is(err, customer.ErrAccountWithdrawn), errors.Is(err, customer.ErrAccountNotFound):
			return nil, nil, errSkip // E15
		case err != nil:
			return nil, nil, errTemporary{"customer: " + err.Error()}
		case c.Email == "":
			return nil, nil, errSkip
		}
		to = []string{c.Email}
		personal["customer_name"] = c.Name
		if c.Name == "" {
			personal["customer_name"] = "お客"
		}
	case domain.RecipientStaff:
		c, err := s.d.Tenant.GetStaffContact(cctx, t, row.StaffID.String())
		switch {
		case errors.Is(err, tenant.ErrStaffNotFound):
			return nil, nil, errSkip // 送る前に所属を削除された
		case err != nil:
			return nil, nil, errTemporary{"staff: " + err.Error()}
		}
		to = []string{c.Email}
		personal["staff_name"] = c.DisplayName
		resolved["console_url"] = s.d.URLs.Console
		resolved["role_label"] = roleLabel(fmt.Sprint(vars["role"]))
	case domain.RecipientTenantOwner:
		owners, err := s.d.Tenant.ListOwnerContacts(cctx, t)
		if err != nil {
			return nil, nil, errTemporary{"owners: " + err.Error()}
		}
		for _, o := range owners {
			if o.Email != "" {
				to = append(to, o.Email)
			}
		}
		if len(to) == 0 {
			return nil, nil, errSkip
		}
		if row.EndpointID != nil {
			if err := s.webhookFailedVars(ctx, t, *row.EndpointID, resolved); err != nil {
				return nil, nil, err
			}
		}
		resolved["console_url"] = s.d.URLs.Console
	}
	all := maps.Clone(vars)
	maps.Copy(all, resolved)
	maps.Copy(all, personal)
	subject, body, err := mail.Render(row.TemplateKey, all)
	if err != nil {
		return nil, nil, err
	}
	msgs := make([]mail.Message, len(to))
	for i, addr := range to {
		msgs[i] = mail.Message{To: addr, Subject: subject, Body: body}
	}
	return msgs, resolved, nil
}

func (s *Service) invoiceSummary(ctx context.Context, t sk.TenantID, invoice uuid.UUID) (sqlcgen.IntegrationInvoiceSummary, error) {
	var sum sqlcgen.IntegrationInvoiceSummary
	err := s.d.DB.ReadTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		var err error
		sum, err = sqlcgen.New(tx).GetInvoiceSummary(ctx, invoice)
		return postgres.NotFound(err)
	})
	if errors.Is(err, postgres.ErrNotFound) {
		return sum, errTemporary{"invoice summary not yet received"} // E14
	}
	return sum, err
}

func (s *Service) webhookFailedVars(ctx context.Context, t sk.TenantID, ep uuid.UUID, out map[string]any) error {
	return s.d.DB.ReadTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		q := sqlcgen.New(tx)
		row, err := q.GetEndpoint(ctx, ep)
		if err != nil {
			return postgres.NotFound(err)
		}
		n, err := q.CountFailedSince(ctx, sqlcgen.CountFailedSinceParams{EndpointID: ep, CompletedAt: ptrTime(s.d.Clock.Today().StartJST())})
		if err != nil {
			return err
		}
		out["endpoint_url"], out["failed_count"] = row.Url, n
		return nil
	})
}

// RunEmailRetry はメールの再送ループ（10 秒ごと。§8.3）。受信処理が送信の途中で落ちた行も、リースが切れたらここで拾う。
func (s *Service) RunEmailRetry(ctx context.Context) error {
	t := time.NewTicker(emailRetryEvery)
	defer t.Stop()
	for {
		if err := s.RetryEmailsOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			observability.Logger("integration.email").ErrorContext(ctx, "email.retry_failed", observability.Err(err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// RetryEmailsOnce は時刻を迎えた pending を最大 50 件取り出して送る。
func (s *Service) RetryEmailsOnce(ctx context.Context) error {
	now := s.d.Clock.Now()
	var rows []sqlcgen.ClaimDueEmailsRow
	if err := s.d.DB.InTx(db.WithSystemScope(ctx, "email retry: claim"), func(ctx context.Context, tx db.Tx) error {
		var err error
		rows, err = sqlcgen.New(tx).ClaimDueEmails(ctx, sqlcgen.ClaimDueEmailsParams{Until: now.Add(emailLease), Now: now, Max: emailRetryBatch})
		return err
	}); err != nil {
		return fmt.Errorf("integration: claim emails: %w", err)
	}
	for _, r := range rows {
		if err := s.SendEmail(ctx, r.ID); err != nil {
			s.logEmailError(ctx, r.ID, err)
		}
	}
	return nil
}

func strPtr(s string) *string        { return &s }
func ptrTime(t time.Time) *time.Time { return &t }
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func roleLabel(role string) string {
	switch role {
	case "owner":
		return "オーナー"
	case "admin":
		return "管理者"
	default:
		return "閲覧者"
	}
}
