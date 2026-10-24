//go:build integration

package app_test

import (
	"encoding/json"

	sk "subscription-platform/internal/sharedkernel"
)

func decodeEnvelope(body string) (sk.EventEnvelope, error) {
	var env sk.EventEnvelope
	err := json.Unmarshal([]byte(body), &env)
	return env, err
}
