package httpx

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// headerAlg は JWT のヘッダーの alg を読む（署名の検証の前に許可リストで弾くため）。
func headerAlg(raw string) (string, error) {
	h, _, ok := strings.Cut(raw, ".")
	if !ok {
		return "", fmt.Errorf("%w: not a JWT", ErrInvalidToken)
	}
	b, err := base64.RawURLEncoding.DecodeString(h)
	if err != nil {
		return "", fmt.Errorf("%w: bad header", ErrInvalidToken)
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(b, &hdr); err != nil {
		return "", fmt.Errorf("%w: bad header", ErrInvalidToken)
	}
	return hdr.Alg, nil
}
