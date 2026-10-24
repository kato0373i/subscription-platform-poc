package sharedkernel

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Actor は操作した主体（トレース用）。Type は consumer | staff | tenant_api | operator | system。
type Actor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// EventEnvelope はモジュール間で流れるイベントの封筒（baseline §5.1）。
type EventEnvelope struct {
	EventID          EventID         `json:"event_id"`
	EventType        string          `json:"event_type"`
	OccurredAt       time.Time       `json:"occurred_at"`
	TenantID         *TenantID       `json:"tenant_id"`
	AggregateType    string          `json:"aggregate_type"`
	AggregateID      uuid.UUID       `json:"aggregate_id"`
	AggregateVersion int64           `json:"aggregate_version"`
	CorrelationID    string          `json:"correlation_id"`
	CausationID      *string         `json:"causation_id"`
	Actor            *Actor          `json:"actor,omitempty"`
	Payload          json.RawMessage `json:"payload"`
}

// NewEnvelope は封筒を作る。event_id は新しく採番し、相関 ID などは outbox.Writer が context から埋める。
func NewEnvelope[P any](eventType, aggType string, aggID uuid.UUID, ver int64, tenant *TenantID, at time.Time, p P) (EventEnvelope, error) {
	if !strings.Contains(eventType, ".") {
		return EventEnvelope{}, fmt.Errorf("sharedkernel: event_type must be <module>.<name>: %q", eventType)
	}
	b, err := json.Marshal(p)
	if err != nil {
		return EventEnvelope{}, fmt.Errorf("sharedkernel: marshal payload: %w", err)
	}
	return EventEnvelope{
		EventID:          NewID[EventTag](),
		EventType:        eventType,
		OccurredAt:       at.UTC(),
		TenantID:         tenant,
		AggregateType:    aggType,
		AggregateID:      aggID,
		AggregateVersion: ver,
		Payload:          b,
	}, nil
}

// DecodePayload はペイロードを型に戻す。未知の項目は無視する（前方互換）。
func DecodePayload[P any](e EventEnvelope) (P, error) {
	var p P
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return p, fmt.Errorf("sharedkernel: decode payload of %s: %w", e.EventType, err)
	}
	return p, nil
}

// Module は event_type の "." より前。EventBridge の source は "sp." + Module()。
func (e EventEnvelope) Module() string {
	m, _, _ := strings.Cut(e.EventType, ".")
	return m
}
