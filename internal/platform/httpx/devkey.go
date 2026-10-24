package httpx

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// LoadDevKey は開発用 JWT の鍵（ES256 の PKCS#8 PEM、deploy/local/dev-jwt.pem）を読む。
func LoadDevKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path) //nolint:gosec // 設定（DEV_JWT_KEY_PATH）で指定した開発用の鍵
	if err != nil {
		return nil, fmt.Errorf("httpx: read dev key: %w", err)
	}
	return ParseDevKey(b)
}

// ParseDevKey は PEM を解析する。
func ParseDevKey(b []byte) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("httpx: dev key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		if ek, err2 := x509.ParseECPrivateKey(blk.Bytes); err2 == nil {
			return ek, nil
		}
		return nil, fmt.Errorf("httpx: parse dev key: %w", err)
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("httpx: dev key must be ECDSA P-256")
	}
	return ek, nil
}
