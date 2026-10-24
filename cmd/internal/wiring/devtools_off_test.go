//go:build !devtools

package wiring

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"subscription-platform/internal/platform/httpx"
	sk "subscription-platform/internal/sharedkernel"
)

// AC-PLT-042：タグなしのビルドでは /v1/dev/* が 404 になり、業務時刻は実時刻
func TestDevAPIIsAbsentWithoutDevtools(t *testing.T) {
	r := httpx.NewRouter(httpx.RouterConfig{}, nil, httpx.Ports{}, nil)
	if err := registerDev(r, &Base{}, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/dev/clock", "/v1/dev/tokens", "/v1/dev/smoke"} {
		rec := httptest.NewRecorder()
		r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d", path, rec.Code)
		}
	}
	if _, ok := newClock(nil).(sk.SystemClock); !ok {
		t.Fatal("タグなしのビルドで dev clock が使われている")
	}
}
