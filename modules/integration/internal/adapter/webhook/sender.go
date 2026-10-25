// Package webhook は事業者の送信先への HTTP 送信（integration.md §8.1・§8.4）。
package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"syscall"
	"time"
	"unicode/utf8"

	"subscription-platform/internal/platform/observability"
	"subscription-platform/modules/integration/contracts/webhooksig"
	"subscription-platform/modules/integration/internal/domain"
)

// 送信の時間の上限（§8.1）。
const (
	ConnectTimeout = 5 * time.Second
	TotalTimeout   = 10 * time.Second
	excerptBytes   = 1024
)

var errSSRF = errors.New("webhook: destination ip is forbidden")

// Sender は署名して POST する。
type Sender struct{ client *http.Client }

// NewSender は SSRF の判定を Dialer.Control で行うクライアントを作る（実際に接続する IP を検査する。DNS リバインディング対策）。
func NewSender(insecure domain.InsecureHosts) *Sender {
	tr := &http.Transport{
		Proxy: nil, // プロキシの環境変数は使わない
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			d := net.Dialer{Timeout: ConnectTimeout, Control: func(_, address string, _ syscall.RawConn) error {
				ap, err := netip.ParseAddrPort(address)
				if err != nil || !domain.DialAllowed(host, ap.Addr(), insecure) {
					return errSSRF
				}
				return nil
			}}
			return d.DialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout:   ConnectTimeout,
		ResponseHeaderTimeout: TotalTimeout,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
	}
	c := &http.Client{
		Transport: tr,
		Timeout:   TotalTimeout,
		// リダイレクトは追わない（3xx は失敗）
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Sender{client: observability.NewHTTPClient("webhook", c)}
}

// Request は 1 回の試行の入力。
type Request struct {
	URL          string
	Body         []byte
	Secrets      []string
	EventID      string
	EventType    string
	DeliveryID   string
	AttemptNo    int
	SignedAt     time.Time // Clock の時刻（署名の t）
	RequestStart time.Time // 試行の記録の requested_at（Clock の時刻）
}

// Send は送信し、結果を返す（送信の失敗も結果として返し、error は署名の失敗だけ）。
func (s *Sender) Send(ctx context.Context, r Request) (domain.AttemptResult, error) {
	sig, err := webhooksig.Sign(r.Secrets, r.SignedAt, r.Body)
	if err != nil {
		return domain.AttemptResult{}, err
	}
	res := domain.AttemptResult{RequestedAt: r.RequestStart}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(r.Body))
	if err != nil {
		res.ErrorKind = domain.ErrConnection
		return res, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SubscriptionPlatform-Webhooks/1.0")
	req.Header.Set(webhooksig.Header, sig)
	req.Header.Set("SP-Event-Id", r.EventID)
	req.Header.Set("SP-Event-Type", r.EventType)
	req.Header.Set("SP-Delivery-Id", r.DeliveryID)
	req.Header.Set("SP-Delivery-Attempt", strconv.Itoa(r.AttemptNo))
	start := time.Now() //nolint:forbidigo // 所要時間の計測は実時間（業務時刻ではない）
	resp, err := s.client.Do(req)
	res.Duration = time.Since(start)
	if err != nil {
		res.ErrorKind = classify(err)
		return res, nil
	}
	defer resp.Body.Close()
	// E11：先頭 1 KB だけ読み、残りは読まずに閉じる
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, excerptBytes))
	for len(buf) > 0 && !utf8.Valid(buf) {
		buf = buf[:len(buf)-1]
	}
	code := resp.StatusCode
	res.StatusCode, res.Excerpt = &code, string(buf)
	if code < 200 || code >= 300 {
		res.ErrorKind = domain.ErrHTTPStatus
	}
	return res, nil
}

func classify(err error) domain.ErrorKind {
	var ne net.Error
	var ce *tls.CertificateVerificationError
	var ue x509.UnknownAuthorityError
	var he x509.HostnameError
	var rh tls.RecordHeaderError
	switch {
	case errors.Is(err, errSSRF):
		return domain.ErrSSRFBlocked
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return domain.ErrTimeout
	case errors.As(err, &ce), errors.As(err, &ue), errors.As(err, &he), errors.As(err, &rh):
		return domain.ErrTLS
	default:
		return domain.ErrConnection
	}
}
