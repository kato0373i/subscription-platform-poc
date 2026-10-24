package observability

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 秘密情報の除去（platform.md §8.6.1、P-OBS-6）。出力の直前に、すべての項目と本文に適用する。

const (
	redacted    = "[REDACTED]"
	redactedPAN = "[REDACTED_PAN]"
)

var (
	secretValuePattern = regexp.MustCompile(`(?i)\bbearer\s+\S+|\b(?:sk|whsec|tok)_[A-Za-z0-9_\-]+`)
	panCandidate       = regexp.MustCompile(`\b\d(?:[ \-]?\d){12,18}\b`)
	emailPattern       = regexp.MustCompile(`([A-Za-z0-9._%+\-])[A-Za-z0-9._%+\-]*@([A-Za-z0-9.\-]+\.[A-Za-z]{2,})`)
	nameKeys           = map[string]bool{"name": true, "full_name": true, "customer_name": true, "holder_name": true, "contact_name": true}
	secretKeysExact    = map[string]bool{"authorization": true, "cookie": true, "set-cookie": true, "x-api-key": true, "api_key": true, "password": true, "signing_key": true}
)

func isSecretKey(k string) bool {
	k = strings.ToLower(k)
	return secretKeysExact[k] || strings.Contains(k, "token") || strings.Contains(k, "secret")
}

func isNameKey(k string) bool { return nameKeys[strings.ToLower(k)] }

// RedactString は文字列の値に、秘密の形・カード番号・メールアドレスの規則を適用する。
func RedactString(s string) string {
	s = secretValuePattern.ReplaceAllString(s, redacted)
	s = panCandidate.ReplaceAllStringFunc(s, func(m string) string {
		if luhn(m) {
			return redactedPAN
		}
		return m
	})
	return emailPattern.ReplaceAllString(s, "$1***@$2")
}

func maskName(s string) string {
	if s == "" {
		return s
	}
	r, _ := utf8.DecodeRuneInString(s)
	return string(r) + "***"
}

func luhn(s string) bool {
	var digits []int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			digits = append(digits, int(c-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	for i := range digits {
		v := digits[len(digits)-1-i]
		if i%2 == 1 {
			v *= 2
			if v > 9 {
				v -= 9
			}
		}
		sum += v
	}
	return sum%10 == 0
}

// redactKV はキー名と値の組に規則を適用する。
func redactKV(key string, v any) any {
	if isSecretKey(key) {
		return redacted
	}
	if s, ok := v.(string); ok && isNameKey(key) {
		return maskName(s)
	}
	return Redact(v)
}

// Redact は値に除去の規則を適用する。map・slice は中まで、構造体は JSON にしてから適用する。
func Redact(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return RedactString(x)
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = redactKV(k, val)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = redactKV(k, val)
		}
		return out
	case map[string][]string: // http.Header
		out := make(map[string]any, len(x))
		for k, vals := range x {
			out[k] = redactKV(k, strings.Join(vals, ", "))
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = Redact(val)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = RedactString(val)
		}
		return out
	case error:
		return RedactString(x.Error())
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return RedactString(slog.AnyValue(x).String())
		}
		var generic any
		if err := json.Unmarshal(b, &generic); err != nil {
			return RedactString(string(b))
		}
		return Redact(generic)
	}
}

// RedactJSONBody は本文を JSON として解析して除去を適用する。JSON でなければ大きさだけを返す。8KB で切り詰める。
func RedactJSONBody(b []byte) any {
	const limit = 8 << 10
	if len(b) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return map[string]any{"non_json_bytes": len(b)}
	}
	r := Redact(v)
	out, _ := json.Marshal(r)
	if len(out) > limit {
		return map[string]any{"truncated": true, "bytes": len(b), "head": RedactString(string(out[:limit]))}
	}
	return r
}

// replaceAttr は slog の ReplaceAttr。組み込みの ts・level・msg 以外のすべての項目に除去を適用する。
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 {
		switch a.Key {
		case slog.TimeKey:
			return slog.String("ts", a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z"))
		case slog.LevelKey, slog.MessageKey:
			return a
		}
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.Any(a.Key, redactKV(a.Key, v.String()))
	case slog.KindAny:
		return slog.Any(a.Key, redactKV(a.Key, v.Any()))
	case slog.KindGroup:
		return a
	default:
		if isSecretKey(a.Key) {
			return slog.String(a.Key, redacted)
		}
		return a
	}
}
