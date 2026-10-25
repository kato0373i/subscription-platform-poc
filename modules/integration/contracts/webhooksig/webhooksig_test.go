package webhooksig_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"subscription-platform/modules/integration/contracts/webhooksig"
)

// AC-INT-007：HMAC-SHA256(key, "t." + body) の hex と一致し、body を 1 バイト変える・t を 301 秒ずらすと失敗する。
func TestSignAndVerify(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	secret := "whsec_" + base64.RawURLEncoding.EncodeToString(key)
	body := []byte(`{"id":"evt_X","type":"subscription.renewed"}`)
	at := time.Unix(1_760_000_000, 0)

	h, err := webhooksig.Sign([]string{secret}, at, body)
	if err != nil {
		t.Fatal(err)
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("1760000000."))
	m.Write(body)
	if want := "t=1760000000,v1=" + hex.EncodeToString(m.Sum(nil)); h != want {
		t.Fatalf("header = %s, want %s", h, want)
	}

	cases := []struct {
		name string
		body []byte
		now  time.Time
		want error
	}{
		{"そのまま", body, at, nil},
		{"300 秒後", body, at.Add(300 * time.Second), nil},
		{"301 秒後", body, at.Add(301 * time.Second), webhooksig.ErrTimestamp},
		{"301 秒前", body, at.Add(-301 * time.Second), webhooksig.ErrTimestamp},
		{"body を 1 バイト変える", append([]byte{'['}, body[1:]...), at, webhooksig.ErrNoMatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := webhooksig.Verify(h, secret, c.body, c.now, webhooksig.DefaultTolerance)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// 移行期間中は v1 が 2 つ（current が先）で、どちらの鍵でも検証できる（AC-INT-005 の Go 側）。
func TestSignWithTwoSecrets(t *testing.T) {
	k1 := "whsec_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	k2 := "whsec_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	at := time.Unix(1_760_000_000, 0)
	h, err := webhooksig.Sign([]string{k2, k1}, at, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(h, "v1=") != 2 {
		t.Fatalf("header = %s", h)
	}
	_, sigs, _ := webhooksig.Parse(h)
	first, _ := webhooksig.Compute(k2, at.Unix(), []byte("{}"))
	if sigs[0] != first {
		t.Fatalf("current が先でない: %v", sigs)
	}
	for _, k := range []string{k1, k2} {
		if err := webhooksig.Verify(h, k, []byte("{}"), at, webhooksig.DefaultTolerance); err != nil {
			t.Fatalf("verify: %v", err)
		}
	}
	k3 := "whsec_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("c", 32)))
	if err := webhooksig.Verify(h, k3, []byte("{}"), at, webhooksig.DefaultTolerance); !errors.Is(err, webhooksig.ErrNoMatch) {
		t.Fatalf("別の鍵: %v", err)
	}
}

func TestMalformed(t *testing.T) {
	if _, err := webhooksig.Key("sk_abc"); !errors.Is(err, webhooksig.ErrMalformedSecret) {
		t.Fatal(err)
	}
	for _, h := range []string{"", "t=abc,v1=00", "t=1", "v1=00"} {
		if _, _, err := webhooksig.Parse(h); !errors.Is(err, webhooksig.ErrMalformedHeader) {
			t.Errorf("%q: %v", h, err)
		}
	}
}
