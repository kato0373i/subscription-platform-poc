package httpx

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// UserPool は JWT の発行元の区分。
type UserPool string

const (
	PoolConsumer UserPool = "consumer"
	PoolStaff    UserPool = "staff"
	PoolOperator UserPool = "operator"
)

// Pools は全プール。
var Pools = []UserPool{PoolConsumer, PoolStaff, PoolOperator}

// Claims は検証済みの JWT の内容。
type Claims struct {
	Issuer, Subject, Email string
	EmailVerified          *bool // クレームがなければ nil
	Name                   string
	Groups                 []string
	ExpiresAt              time.Time
}

// TokenVerifier は JWT を検証する（DevJWTVerifier / CognitoVerifier）。
type TokenVerifier interface {
	Verify(ctx context.Context, raw string, pool UserPool) (Claims, error)
}

// ErrInvalidToken は検証に失敗した（理由は詳細ログだけに出す。E16）。
var ErrInvalidToken = errors.New("httpx: invalid token")

const clockSkew = 60 * time.Second

// allowedAlgs は署名アルゴリズムの許可リスト（alg:none・HS256 を拒否する）。
var allowedAlgs = []string{jwa.ES256().String(), jwa.RS256().String()}

// PoolSettings はプールごとの iss・aud。
type PoolSettings struct {
	Issuer   string
	Audience string // Cognito の client_id
}

func claimsFrom(tok jwt.Token) Claims {
	c := Claims{}
	c.Issuer, _ = tok.Issuer()
	c.Subject, _ = tok.Subject()
	c.ExpiresAt, _ = tok.Expiration()
	var s string
	if tok.Get("email", &s) == nil {
		c.Email = s
	}
	var b bool
	if tok.Get("email_verified", &b) == nil {
		c.EmailVerified = &b
	}
	if tok.Get("name", &s) == nil {
		c.Name = s
	}
	var groups []string
	if tok.Get("cognito:groups", &groups) == nil || tok.Get("groups", &groups) == nil {
		c.Groups = groups
	} else {
		var anyGroups []any
		if tok.Get("cognito:groups", &anyGroups) == nil || tok.Get("groups", &anyGroups) == nil {
			for _, g := range anyGroups {
				if s, ok := g.(string); ok {
					c.Groups = append(c.Groups, s)
				}
			}
		}
	}
	return c
}

func checkAlg(raw string) error {
	msgAlg, err := headerAlg(raw)
	if err != nil {
		return err
	}
	if !slices.Contains(allowedAlgs, msgAlg) {
		return fmt.Errorf("%w: alg %q is not allowed", ErrInvalidToken, msgAlg)
	}
	return nil
}

func verifyWith(raw string, set PoolSettings, keyOpt jwt.ParseOption) (Claims, error) {
	if err := checkAlg(raw); err != nil {
		return Claims{}, err
	}
	tok, err := jwt.ParseString(raw, keyOpt,
		jwt.WithValidate(true),
		jwt.WithIssuer(set.Issuer),
		jwt.WithAudience(set.Audience),
		jwt.WithAcceptableSkew(clockSkew),
		jwt.WithRequiredClaim("exp"),
		jwt.WithRequiredClaim("sub"),
	)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, err) //nolint:errorlint // 理由は詳細ログだけに出す
	}
	return claimsFrom(tok), nil
}

// DevJWTVerifier は開発用 JWT（ES256・deploy/local/dev-jwt.pem）を検証する。本番と同じ検証の規則を通る。
type DevJWTVerifier struct {
	pub   *ecdsa.PublicKey
	pools map[UserPool]PoolSettings
}

// DevIssuer は開発用 JWT の iss。aud はプール名。
const DevIssuer = "https://sp.local/dev"

// DevPoolSettings は開発用 JWT のプールの設定。
func DevPoolSettings(p UserPool) PoolSettings {
	return PoolSettings{Issuer: DevIssuer, Audience: "sp-dev-" + string(p)}
}

func NewDevJWTVerifier(pub *ecdsa.PublicKey) *DevJWTVerifier {
	pools := map[UserPool]PoolSettings{}
	for _, p := range Pools {
		pools[p] = DevPoolSettings(p)
	}
	return &DevJWTVerifier{pub: pub, pools: pools}
}

func (v *DevJWTVerifier) Verify(_ context.Context, raw string, pool UserPool) (Claims, error) {
	set, ok := v.pools[pool]
	if !ok {
		return Claims{}, fmt.Errorf("%w: unknown pool %q", ErrInvalidToken, pool)
	}
	return verifyWith(raw, set, jwt.WithKey(jwa.ES256(), v.pub))
}

// CognitoVerifier はプールごとの JWKS（{issuer}/.well-known/jwks.json）で検証する。鍵はキャッシュする。
type CognitoVerifier struct {
	pools map[UserPool]PoolSettings
	mu    sync.Mutex
	cache *jwk.Cache
	urls  map[UserPool]string
}

// NewCognitoVerifier は JWKS のキャッシュを作る。jwksURL が空ならプールの issuer から作る。
func NewCognitoVerifier(ctx context.Context, pools map[UserPool]PoolSettings, jwksURLs map[UserPool]string) (*CognitoVerifier, error) {
	c, err := jwk.NewCache(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("httpx: jwk cache: %w", err)
	}
	v := &CognitoVerifier{pools: pools, cache: c, urls: map[UserPool]string{}}
	for p, set := range pools {
		u := jwksURLs[p]
		if u == "" {
			u = strings.TrimSuffix(set.Issuer, "/") + "/.well-known/jwks.json"
		}
		v.urls[p] = u
		if err := c.Register(ctx, u); err != nil {
			return nil, fmt.Errorf("httpx: register jwks %s: %w", u, err)
		}
	}
	return v, nil
}

func (v *CognitoVerifier) Verify(ctx context.Context, raw string, pool UserPool) (Claims, error) {
	set, ok := v.pools[pool]
	if !ok {
		return Claims{}, fmt.Errorf("%w: unknown pool %q", ErrInvalidToken, pool)
	}
	keys, err := v.cache.Lookup(ctx, v.urls[pool])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: jwks: %v", ErrInvalidToken, err) //nolint:errorlint
	}
	return verifyWith(raw, set, jwt.WithKeySet(keys, jws.WithInferAlgorithmFromKey(true)))
}
