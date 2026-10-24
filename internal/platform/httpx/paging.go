package httpx

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"subscription-platform/internal/platform/httpx/errcodes"
)

// CursorCodec はページングのカーソルを AES-GCM で暗号化する（改ざんを検出する）。
type CursorCodec struct{ aead cipher.AEAD }

// NewCursorCodec は 32 バイトの鍵（CURSOR_KEY）から作る。
func NewCursorCodec(key []byte) (*CursorCodec, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("httpx: cursor key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("httpx: cursor gcm: %w", err)
	}
	return &CursorCodec{aead: aead}, nil
}

// Encode は key（任意の JSON にできる値）をカーソル文字列にする。
func (c *CursorCodec) Encode(key any) string {
	plain, err := json.Marshal(key)
	if err != nil {
		panic(fmt.Sprintf("httpx: cursor key is not JSON: %v", err))
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(c.aead.Seal(nonce, nonce, plain, nil))
}

// ErrBadCursor は改ざん・形式不正のカーソル。
var ErrBadCursor = errors.New("httpx: bad cursor")

// Decode はカーソル文字列を key に戻す。
func (c *CursorCodec) Decode(s string, key any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) < c.aead.NonceSize() {
		return ErrBadCursor
	}
	plain, err := c.aead.Open(nil, b[:c.aead.NonceSize()], b[c.aead.NonceSize():], nil)
	if err != nil {
		return ErrBadCursor
	}
	if err := json.Unmarshal(plain, key); err != nil {
		return ErrBadCursor
	}
	return nil
}

// PageRequest はページングの要求。After は復号済みのカーソル（JSON）。
type PageRequest struct {
	Limit int
	After []byte
}

// Page は一覧の応答。
type Page[T any] struct {
	Data       []T     `json:"data"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

// ParsePage は limit（既定 20・上限 100）と starting_after を読む。誤りは validation_failed（AC-PLT-036）。
func ParsePage(r *http.Request, codec *CursorCodec) (PageRequest, error) {
	q := r.URL.Query()
	pr := PageRequest{Limit: 20}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			return pr, Error(errcodes.ValidationFailed, "limit must be between 1 and 100", FieldError{Field: "limit", Code: "out_of_range"})
		}
		pr.Limit = n
	}
	if s := q.Get("starting_after"); s != "" {
		var raw json.RawMessage
		if err := codec.Decode(s, &raw); err != nil {
			return pr, Error(errcodes.ValidationFailed, "starting_after is invalid", FieldError{Field: "starting_after", Code: "invalid"})
		}
		pr.After = raw
	}
	return pr, nil
}
