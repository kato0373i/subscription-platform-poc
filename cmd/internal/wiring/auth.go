package wiring

import (
	"context"
	"crypto/ecdsa"
	"fmt"

	"subscription-platform/internal/platform/httpx"
)

// newVerifier は AUTH_MODE に応じた TokenVerifier を作る。dev のときは発行用の鍵も返す。
func newVerifier(ctx context.Context, b *Base) (httpx.TokenVerifier, *ecdsa.PrivateKey, error) {
	if b.Cfg.AuthMode == "dev" {
		k, err := httpx.LoadDevKey(b.Cfg.DevJWTKeyPath)
		if err != nil {
			return nil, nil, err
		}
		return httpx.NewDevJWTVerifier(&k.PublicKey), k, nil
	}
	pools := map[httpx.UserPool]httpx.PoolSettings{
		httpx.PoolConsumer: {Issuer: b.Cfg.CognitoConsumer.Issuer, Audience: b.Cfg.CognitoConsumer.ClientID},
		httpx.PoolStaff:    {Issuer: b.Cfg.CognitoStaff.Issuer, Audience: b.Cfg.CognitoStaff.ClientID},
		httpx.PoolOperator: {Issuer: b.Cfg.CognitoOperator.Issuer, Audience: b.Cfg.CognitoOperator.ClientID},
	}
	v, err := httpx.NewCognitoVerifier(ctx, pools, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("cognito verifier: %w", err)
	}
	return v, nil, nil
}
