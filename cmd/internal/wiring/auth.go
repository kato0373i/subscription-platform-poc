package wiring

import (
	"context"
	"crypto/ecdsa"
	"fmt"

	"subscription-platform/internal/platform/config"
	"subscription-platform/internal/platform/httpx"
)

// newVerifier は AUTH_MODE に応じた TokenVerifier を作る。dev のときは発行用の鍵も返す。
// dev（devtools ビルドだけ）でも Cognito のプールが設定されていれば、iss で振り分けて両方受ける（business-console ADR-C04）。
func newVerifier(ctx context.Context, b *Base) (httpx.TokenVerifier, *ecdsa.PrivateKey, error) {
	cfg := map[httpx.UserPool]config.CognitoPool{httpx.PoolConsumer: b.Cfg.CognitoConsumer, httpx.PoolStaff: b.Cfg.CognitoStaff, httpx.PoolOperator: b.Cfg.CognitoOperator}
	pools := map[httpx.UserPool]httpx.PoolSettings{}
	jwks := map[httpx.UserPool]string{}
	for p, c := range cfg {
		if c.Issuer == "" {
			continue
		}
		pools[p] = httpx.PoolSettings{Issuer: c.Issuer, Audience: c.ClientID}
		jwks[p] = c.JWKSURL
	}
	var cognito *httpx.CognitoVerifier
	if len(pools) > 0 {
		var err error
		if cognito, err = httpx.NewCognitoVerifier(ctx, pools, jwks); err != nil {
			return nil, nil, fmt.Errorf("cognito verifier: %w", err)
		}
	}
	if b.Cfg.AuthMode != "dev" {
		return cognito, nil, nil
	}
	k, err := httpx.LoadDevKey(b.Cfg.DevJWTKeyPath)
	if err != nil {
		return nil, nil, err
	}
	dev := httpx.NewDevJWTVerifier(&k.PublicKey)
	if cognito == nil {
		return dev, k, nil
	}
	byIssuer := map[string]httpx.TokenVerifier{httpx.DevIssuer: dev}
	for _, iss := range cognito.Issuers() {
		byIssuer[iss] = cognito
	}
	return httpx.NewMultiVerifier(byIssuer), k, nil
}
