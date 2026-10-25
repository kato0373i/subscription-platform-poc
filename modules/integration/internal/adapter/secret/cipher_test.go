package secret_test

import (
	"bytes"
	"testing"

	"subscription-platform/modules/integration/internal/adapter/secret"
)

func TestRoundTrip(t *testing.T) {
	c, err := secret.NewAESGCM(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	id, ct, err := c.Encrypt([]byte("whsec_abc"))
	if err != nil || bytes.Contains(ct, []byte("whsec_abc")) {
		t.Fatalf("encrypt: %v", err)
	}
	plain, err := c.Decrypt(id, ct)
	if err != nil || string(plain) != "whsec_abc" {
		t.Fatalf("decrypt: %v %s", err, plain)
	}
	other, _ := secret.NewAESGCM(bytes.Repeat([]byte{8}, 32))
	if _, err := other.Decrypt(id, ct); err == nil {
		t.Fatal("別の鍵で復号できた")
	}
	ct[len(ct)-1] ^= 1
	if _, err := c.Decrypt(id, ct); err == nil {
		t.Fatal("改ざんを検出しない")
	}
}
