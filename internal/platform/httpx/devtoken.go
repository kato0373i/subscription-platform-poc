package httpx

import (
	"crypto/ecdsa"
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// DevTokenRequest は開発用 JWT の発行の要求（POST /v1/dev/tokens）。
type DevTokenRequest struct {
	Pool          UserPool `json:"pool"`
	Sub           string   `json:"sub"`
	Email         string   `json:"email,omitempty"`
	EmailVerified *bool    `json:"email_verified,omitempty"`
	Name          string   `json:"name,omitempty"`
	Groups        []string `json:"groups,omitempty"`
	TTLSeconds    int      `json:"ttl_seconds,omitempty"`
}

// IssueDevToken は開発用 JWT を発行する。検証は DevJWTVerifier（本番と同じ規則）が行う。
// 発行の入口（/v1/dev/tokens）は devtools ビルドにだけ含まれる。
func IssueDevToken(key *ecdsa.PrivateKey, req DevTokenRequest, now time.Time) (string, time.Time, error) {
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	exp := now.Add(ttl)
	set := DevPoolSettings(req.Pool)
	b := jwt.NewBuilder().Issuer(set.Issuer).Audience([]string{set.Audience}).Subject(req.Sub).IssuedAt(now).NotBefore(now).Expiration(exp)
	if req.Email != "" {
		b = b.Claim("email", req.Email)
		verified := true
		if req.EmailVerified != nil {
			verified = *req.EmailVerified
		}
		b = b.Claim("email_verified", verified)
	}
	if req.Name != "" {
		b = b.Claim("name", req.Name)
	}
	if len(req.Groups) > 0 {
		b = b.Claim("cognito:groups", req.Groups)
	}
	tok, err := b.Build()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("httpx: build token: %w", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES256(), key))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("httpx: sign token: %w", err)
	}
	return string(signed), exp, nil
}
