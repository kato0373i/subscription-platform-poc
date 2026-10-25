package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// Handler は購読者の処理。
type Handler func(ctx context.Context, env sk.EventEnvelope) error

// ConsumerSpec は各 module.go の Consumers() が返す購読者。
type ConsumerSpec struct {
	Name    string
	Handler Handler
}

// Publisher はイベントをバスに送る。
type Publisher interface {
	Publish(ctx context.Context, evs []sk.EventEnvelope) ([]PublishResult, error)
}

// PublishResult は 1 件ごとの送信結果。
type PublishResult struct {
	EventID sk.EventID
	Err     error
}

// CommandSender は FIFO キューへのジョブ投入（C-01）。
type CommandSender interface {
	SendFIFO(ctx context.Context, queue string, env sk.EventEnvelope, groupID, dedupID string) error
}

// Message は受信したメッセージ。
type Message struct {
	ID            string
	Body          string
	GroupID       string
	ReceiptHandle string
	ReceiveCount  int
}

// Outcome は Dispatch の結果。
type Outcome struct {
	Succeeded, Retry, Permanent []Message
	// Delay は RetryAfter で再配信までの時間を指定したメッセージ（ID → 時間）。なければ受信回数による既定の間隔。
	Delay map[string]time.Duration
}

type retryAfterError struct {
	err   error
	delay time.Duration
}

func (r retryAfterError) Error() string { return fmt.Sprintf("retry after %s: %v", r.delay, r.err) }
func (r retryAfterError) Unwrap() error { return r.err }

// RetryAfter は消さずに残し、delay 後に再配信させる（例：同じ契約に進行中のプロセスがあるので 60 秒後。orchestration.md §2）。
func RetryAfter(err error, delay time.Duration) error {
	if err == nil {
		return nil
	}
	return retryAfterError{err: err, delay: delay}
}

// RetryDelay は RetryAfter で指定した時間。指定がなければ 0。
func RetryDelay(err error) time.Duration {
	var r retryAfterError
	if errors.As(err, &r) {
		return r.delay
	}
	return 0
}

type permanentError struct{ err error }

func (p permanentError) Error() string { return "permanent: " + p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent は再試行しても直らないエラー（解析不能・契約違反）にする。DLQ へ送られる。
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

// IsPermanent は Permanent で包んだエラーか。
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// eventBridgeEvent は SQS の本文（EventBridge のイベント JSON）。direct モードでも同じ形で送る。
type eventBridgeEvent struct {
	Version    string          `json:"version,omitempty"`
	ID         string          `json:"id,omitempty"`
	Source     string          `json:"source"`
	DetailType string          `json:"detail-type"`
	Time       string          `json:"time,omitempty"`
	Detail     json.RawMessage `json:"detail"`
}

// EncodeEventBridgeBody は envelope を EventBridge 形式の本文にする（direct モード用）。
func EncodeEventBridgeBody(env sk.EventEnvelope) (string, error) {
	detail, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("messaging: marshal envelope: %w", err)
	}
	b, err := json.Marshal(eventBridgeEvent{Version: "0", ID: env.EventID.String(), Source: "sp." + env.Module(), DetailType: env.EventType, Time: env.OccurredAt.UTC().Format("2006-01-02T15:04:05Z"), Detail: detail})
	if err != nil {
		return "", fmt.Errorf("messaging: marshal body: %w", err)
	}
	return string(b), nil
}

// DecodeBody は本文を envelope に戻す。EventBridge 形式（detail に envelope）と、FIFO の生の envelope の両方を受け付ける。
func DecodeBody(body string) (sk.EventEnvelope, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return sk.EventEnvelope{}, fmt.Errorf("messaging: body is not JSON: %w", err)
	}
	raw := []byte(body)
	if d, ok := probe["detail"]; ok {
		if _, isEB := probe["detail-type"]; isEB {
			raw = d
			// EventBridge が detail を文字列のまま運ぶ実装に備える
			var s string
			if json.Unmarshal(d, &s) == nil {
				raw = []byte(s)
			}
		}
	}
	var env sk.EventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return sk.EventEnvelope{}, fmt.Errorf("messaging: decode envelope: %w", err)
	}
	var missing []string
	if env.EventID.IsZero() {
		missing = append(missing, "event_id")
	}
	if !strings.Contains(env.EventType, ".") {
		missing = append(missing, "event_type")
	}
	if env.CorrelationID == "" {
		missing = append(missing, "correlation_id")
	}
	if env.OccurredAt.IsZero() {
		missing = append(missing, "occurred_at")
	}
	if len(missing) > 0 {
		return sk.EventEnvelope{}, fmt.Errorf("messaging: envelope misses %s", strings.Join(missing, ", "))
	}
	return env, nil
}
