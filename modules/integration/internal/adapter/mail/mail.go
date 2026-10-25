// Package mail はメールの文面の組み立て（text/template）と送信（SES・ログ）。integration.md §8.3。
package mail

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	sestypes "github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/aws/smithy-go"

	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
)

//go:embed templates/*.txt
var templateFS embed.FS

var templates = template.Must(template.New("").Option("missingkey=zero").Funcs(template.FuncMap{
	"yen":      Yen,
	"date":     jpDate,
	"datetime": jpDateTime,
}).ParseFS(templateFS, "templates/*.txt"))

// Yen は金額を "1,980円" にする。
func Yen(v any) string {
	var n int64
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int32:
		n = int64(x)
	case int64:
		n = x
	case float64:
		n = int64(x)
	default:
		return fmt.Sprint(v)
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String() + "円"
}

// jpDate は "2026-10-15" を "2026年10月15日" にする。
func jpDate(v any) string {
	s, _ := v.(string)
	d, err := sk.ParseDate(s)
	if err != nil {
		return s
	}
	return fmt.Sprintf("%d年%d月%d日", d.Year(), d.Month(), d.Day())
}

// jpDateTime は RFC 3339 の時刻を JST の "2026年10月15日 10:00" にする。
func jpDateTime(v any) string {
	s, _ := v.(string)
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	t = t.In(sk.JST)
	return fmt.Sprintf("%d年%d月%d日 %02d:%02d", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute())
}

// Render はテンプレートから件名と本文を作る（1 行目が件名）。
func Render(templateKey string, vars map[string]any) (subject, body string, err error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, templateKey+".txt", vars); err != nil {
		return "", "", fmt.Errorf("mail: render %s: %w", templateKey, err)
	}
	subject, body, _ = strings.Cut(buf.String(), "\n")
	return strings.TrimSpace(subject), strings.TrimLeft(body, "\n"), nil
}

// Message は送るメール 1 通。
type Message struct {
	To      string
	Subject string
	Body    string
}

// ErrPermanent は再送しても成功しない送信の失敗（MessageRejected・宛先の形式不正）。
var ErrPermanent = errors.New("mail: permanent failure")

// Mailer は送信のポート。
type Mailer interface {
	Send(ctx context.Context, m Message) (messageID string, err error)
}

// SESMailer は SES（ローカルは MiniStack）で送る。
type SESMailer struct {
	Client *ses.Client
	From   string
}

func (s SESMailer) Send(ctx context.Context, m Message) (string, error) {
	out, err := s.Client.SendEmail(ctx, &ses.SendEmailInput{
		Source:      aws.String(s.From),
		Destination: &sestypes.Destination{ToAddresses: []string{m.To}},
		Message: &sestypes.Message{
			Subject: &sestypes.Content{Data: aws.String(m.Subject), Charset: aws.String("UTF-8")},
			Body:    &sestypes.Body{Text: &sestypes.Content{Data: aws.String(m.Body), Charset: aws.String("UTF-8")}},
		},
	})
	if err != nil {
		var ae smithy.APIError
		if errors.As(err, &ae) {
			switch ae.ErrorCode() {
			case "MessageRejected", "InvalidParameterValue", "MailFromDomainNotVerifiedException":
				return "", fmt.Errorf("%w: %s", ErrPermanent, ae.ErrorCode())
			}
		}
		return "", fmt.Errorf("mail: ses: %w", err)
	}
	return aws.ToString(out.MessageId), nil
}

// LogMailer は標準出力のログに書くだけ（MAIL_MODE=log。手動確認用。テストでは使わない）。
type LogMailer struct{}

func (LogMailer) Send(ctx context.Context, m Message) (string, error) {
	id := "log-" + sk.NewID[sk.EventTag]().String()
	observability.Logger("integration.mail").InfoContext(ctx, "mail.logged", slog.String("message_id", id), slog.String("subject", m.Subject),
		slog.Int("body_bytes", len(m.Body)))
	return id, nil
}
