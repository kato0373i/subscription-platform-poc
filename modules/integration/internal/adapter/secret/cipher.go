// Package secret は署名鍵の暗号化（SecretCipher ポートの PoC 実装。integration.md §6）。本番では KMS の実装に差し替える。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// Cipher は鍵の ID と暗号文の組で保存する（鍵の入れ替えに備える）。
type Cipher interface {
	Encrypt(plain []byte) (keyID string, ciphertext []byte, err error)
	Decrypt(keyID string, ciphertext []byte) ([]byte, error)
}

// AESGCM は環境変数 WEBHOOK_SECRET_ENC_KEY（32 バイト）の AES-256-GCM。暗号文は nonce を先頭に含む。
type AESGCM struct {
	id   string
	aead cipher.AEAD
}

// NewAESGCM は 32 バイトの鍵から作る。鍵の ID は鍵の SHA-256 の先頭 8 バイト（鍵そのものは出さない）。
func NewAESGCM(key []byte) (*AESGCM, error) {
	if len(key) != 32 {
		return nil, errors.New("secret: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	sum := sha256.Sum256(key)
	return &AESGCM{id: "aesgcm:" + hex.EncodeToString(sum[:8]), aead: aead}, nil
}

func (c *AESGCM) Encrypt(plain []byte) (string, []byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, fmt.Errorf("secret: nonce: %w", err)
	}
	return c.id, c.aead.Seal(nonce, nonce, plain, nil), nil
}

// ErrUnknownKey は保存時と違う鍵で復号しようとした。
var ErrUnknownKey = errors.New("secret: unknown key id")

func (c *AESGCM) Decrypt(keyID string, ct []byte) ([]byte, error) {
	if keyID != c.id {
		return nil, ErrUnknownKey
	}
	n := c.aead.NonceSize()
	if len(ct) < n {
		return nil, errors.New("secret: ciphertext too short")
	}
	plain, err := c.aead.Open(nil, ct[:n], ct[n:], nil)
	if err != nil {
		return nil, fmt.Errorf("secret: decrypt: %w", err)
	}
	return plain, nil
}
