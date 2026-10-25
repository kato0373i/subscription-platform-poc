package domain

import (
	"net/netip"
	"net/url"
	"slices"
	"strings"
)

// MaxURLLength は送信先の URL の上限（U1）。
const MaxURLLength = 2048

// URL の検証のエラーコード（errors[].code）。
const (
	CodeURLInvalid       = "url_invalid"
	CodeURLNotHTTPS      = "url_not_https"
	CodeURLForbiddenHost = "url_forbidden_host"
)

// Resolver はホスト名を IP に解決する（登録時の U5。テストでは表を渡す）。
type Resolver func(host string) ([]netip.Addr, error)

// forbidden は SSRF の拒否リスト（§8.4）。
var forbidden = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16",
		"224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "fc00::/7", "fe80::/10",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// ForbiddenIP は接続してはいけない IP か（IPv4 射影アドレスは IPv4 に戻してから判定する）。
func ForbiddenIP(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range forbidden {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// InsecureHosts は WEBHOOK_INSECURE_ALLOWED_HOSTS（U3。http と SSRF の拒否リストの例外）。
type InsecureHosts []string

// Allows は host が例外のホストか。
func (h InsecureHosts) Allows(host string) bool {
	return slices.Contains(h, strings.ToLower(host))
}

// DialAllowed は配信ループの Dialer.Control が使う判定（AC-INT-020）：例外のホストへの接続か、拒否リストにない IP なら真。
func DialAllowed(host string, ip netip.Addr, insecure InsecureHosts) bool {
	return insecure.Allows(host) || !ForbiddenIP(ip)
}

// URLError は送信先の URL の検証の失敗。
type URLError struct{ Code string }

func (e *URLError) Error() string { return "integration: " + e.Code }

// ValidateURL は送信先の URL の検証（§3.1.1 の U1〜U5）。resolve が nil なら U5 を行わない。
func ValidateURL(raw string, insecure InsecureHosts, resolve Resolver) error {
	if len(raw) > MaxURLLength || raw == "" {
		return &URLError{CodeURLInvalid}
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") {
		return &URLError{CodeURLInvalid}
	}
	host := u.Hostname()
	switch u.Scheme {
	case "https":
	case "http":
		if !insecure.Allows(host) {
			return &URLError{CodeURLNotHTTPS}
		}
	default:
		return &URLError{CodeURLNotHTTPS}
	}
	if insecure.Allows(host) {
		return nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ForbiddenIP(ip) {
			return &URLError{CodeURLForbiddenHost}
		}
		return nil
	}
	if resolve == nil {
		return nil
	}
	ips, err := resolve(host)
	if err != nil || len(ips) == 0 {
		return nil // 解決できなければ許可する（送信時に再判定する。U5）
	}
	for _, ip := range ips {
		if !ForbiddenIP(ip) {
			return nil
		}
	}
	return &URLError{CodeURLForbiddenHost}
}
