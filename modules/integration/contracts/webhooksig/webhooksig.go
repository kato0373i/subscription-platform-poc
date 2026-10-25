// Package webhooksig は事業者 Webhook の署名と検証（integration.md §4.3）。標準ライブラリだけに依存し、事業者向けの検証サンプルにもなる。
//
//	SP-Signature: t=<UNIX 秒>,v1=<hex>[,v1=<hex>]
//
// 署名対象は "<t>." + 送信する body のバイト列。HMAC-SHA256、鍵は "whsec_" 以降を base64url デコードしたバイト列。
package webhooksig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Header は署名のヘッダー名。
const Header = "SP-Signature"

// SecretPrefix は署名鍵の接頭辞。
const SecretPrefix = "whsec_"

// DefaultTolerance は受信側が許す時刻のずれ（リプレイ対策の推奨値）。
const DefaultTolerance = 300 * time.Second

var (
	ErrMalformedSecret = errors.New("webhooksig: malformed secret")
	ErrMalformedHeader = errors.New("webhooksig: malformed header")
	ErrTimestamp       = errors.New("webhooksig: timestamp outside tolerance")
	ErrNoMatch         = errors.New("webhooksig: no matching signature")
)

// Key は "whsec_..." の鍵を HMAC の鍵のバイト列にする。
func Key(secret string) ([]byte, error) {
	raw, ok := strings.CutPrefix(secret, SecretPrefix)
	if !ok || raw == "" {
		return nil, ErrMalformedSecret
	}
	k, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(raw, "="))
	if err != nil {
		return nil, ErrMalformedSecret
	}
	return k, nil
}

// Compute は 1 つの鍵の署名（hex 小文字）。
func Compute(secret string, t int64, body []byte) (string, error) {
	k, err := Key(secret)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte(strconv.FormatInt(t, 10) + "."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil)), nil
}

// Sign はヘッダーの値を作る。secrets は署名に使う順（current が先、移行期間中は previous が後）。
func Sign(secrets []string, t time.Time, body []byte) (string, error) {
	ts := t.Unix()
	var b strings.Builder
	b.WriteString("t=" + strconv.FormatInt(ts, 10))
	for _, s := range secrets {
		sig, err := Compute(s, ts, body)
		if err != nil {
			return "", err
		}
		b.WriteString(",v1=" + sig)
	}
	return b.String(), nil
}

// Parse はヘッダーの値から t と v1 の一覧を取り出す。
func Parse(header string) (t int64, sigs []string, err error) {
	found := false
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			t, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				return 0, nil, ErrMalformedHeader
			}
			found = true
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if !found || len(sigs) == 0 {
		return 0, nil, ErrMalformedHeader
	}
	return t, sigs, nil
}

// Verify は受信側の検証：時刻のずれが tolerance 以内で、どれか 1 つの v1 が secret の署名と一致すること（定数時間比較）。
func Verify(header, secret string, body []byte, now time.Time, tolerance time.Duration) error {
	t, sigs, err := Parse(header)
	if err != nil {
		return err
	}
	if d := now.Sub(time.Unix(t, 0)); d > tolerance || d < -tolerance {
		return ErrTimestamp
	}
	want, err := Compute(secret, t, body)
	if err != nil {
		return err
	}
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			return nil
		}
	}
	return ErrNoMatch
}
