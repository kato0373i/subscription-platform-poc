package domain

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// APIKeyTag は ApiKey の ID の型。
type APIKeyTag struct{}

// APIKeyID は API キーの ID。
type APIKeyID = sk.ID[APIKeyTag]

// MaxActiveAPIKeys は有効なキーの上限（NFR-009）。
const MaxActiveAPIKeys = 10

const (
	base62       = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	prefixChars  = 12
	secretChars  = 43 // 32 バイト（256 ビット）を base62 にした最大桁数
	prefixLength = len("sk_") + prefixChars
)

// ErrMalformedKey は平文の形式が正しくない。
var ErrMalformedKey = errors.New("tenant: malformed api key")

var rawKeyPattern = regexp.MustCompile(`^sk_[0-9A-Za-z]{12}_[0-9A-Za-z]{43}$`)

// ParseKey は平文 `sk_<prefix12>_<secret43>` から検索キーの prefix（先頭 15 文字）を取り出す。
func ParseKey(raw string) (prefix string, err error) {
	if !rawKeyPattern.MatchString(raw) {
		return "", ErrMalformedKey
	}
	return raw[:prefixLength], nil
}

// HashKey は平文全体の SHA-256。
func HashKey(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

// GenerateKey は crypto/rand（テストでは固定の乱数源）から平文を作る。
func GenerateKey(rand io.Reader) (raw string, err error) {
	prefix := make([]byte, prefixChars)
	if err := randomBase62(rand, prefix); err != nil {
		return "", err
	}
	secret := make([]byte, 32)
	if _, err := io.ReadFull(rand, secret); err != nil {
		return "", fmt.Errorf("tenant: read random: %w", err)
	}
	return "sk_" + string(prefix) + "_" + encodeBase62(secret, secretChars), nil
}

// randomBase62 は棄却法で偏りのない base62 の文字を埋める。
func randomBase62(rand io.Reader, out []byte) error {
	buf := make([]byte, 1)
	for i := 0; i < len(out); {
		if _, err := io.ReadFull(rand, buf); err != nil {
			return fmt.Errorf("tenant: read random: %w", err)
		}
		if buf[0] >= 248 { // 62*4 = 248。以上は捨てる
			continue
		}
		out[i] = base62[buf[0]%62]
		i++
	}
	return nil
}

// encodeBase62 は b を base62 にし、先頭を 0 で埋めて width 桁にする。
func encodeBase62(b []byte, width int) string {
	n := new(big.Int).SetBytes(b)
	base := big.NewInt(62)
	mod := new(big.Int)
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		n.DivMod(n, base, mod)
		out[i] = base62[mod.Int64()]
	}
	return string(out)
}

// APIKeyState は ApiKey の永続化される状態。平文は持たない。
type APIKeyState struct {
	ID        APIKeyID
	TenantID  sk.TenantID
	Name      string
	Prefix    string
	Hash      []byte
	CreatedBy *StaffID
	RevokedBy *StaffID
	CreatedAt time.Time
	RevokedAt *time.Time
}

// APIKey は事業者 API の認証キー（集約ルート）。
type APIKey struct{ s APIKeyState }

// RestoreAPIKey は保存済みの状態から復元する。
func RestoreAPIKey(s APIKeyState) *APIKey { return &APIKey{s: s} }

// IssueAPIKey はキーを発行する。平文は戻り値でだけ返す（DB・ログに出さない）。
// activeCount はテナント行をロックした状態で数えた有効なキーの数（E2）。
func IssueAPIKey(t *Tenant, id APIKeyID, name string, by *StaffID, activeCount int, rand io.Reader, now time.Time) (*APIKey, string, error) {
	if !t.AllowsStaffWrite() {
		return nil, "", ErrTenantNotWritable
	}
	n, err := textInRange("name", name, 100)
	if err != nil {
		return nil, "", err
	}
	if activeCount >= MaxActiveAPIKeys {
		return nil, "", ErrAPIKeyLimit
	}
	raw, err := GenerateKey(rand)
	if err != nil {
		return nil, "", err
	}
	return &APIKey{s: APIKeyState{ID: id, TenantID: t.ID(), Name: n, Prefix: raw[:prefixLength], Hash: HashKey(raw), CreatedBy: by, CreatedAt: now}}, raw, nil
}

func (k *APIKey) State() APIKeyState { return k.s }
func (k *APIKey) Active() bool       { return k.s.RevokedAt == nil }

// Revoke は失効させる。失効済みなら false（何もしない）。
func (k *APIKey) Revoke(by *StaffID, now time.Time) bool {
	if k.s.RevokedAt != nil {
		return false
	}
	k.s.RevokedAt, k.s.RevokedBy = &now, by
	return true
}

// Matches は平文が一致するか（定数時間の比較）。
func (k *APIKey) Matches(raw string) bool { return MatchesHash(k.s.Hash, raw) }

// MatchesHash は hash が平文の SHA-256 と一致するか（定数時間の比較）。
func MatchesHash(hash []byte, raw string) bool {
	return subtle.ConstantTimeCompare(hash, HashKey(raw)) == 1
}
