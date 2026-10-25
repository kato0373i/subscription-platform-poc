package app_test

import (
	"slices"
	"testing"

	"subscription-platform/deploy"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/modules/integration/internal/domain"
)

// deploy/messaging.yaml の購読イベントが、コードの対応表（§4.1・§5.3）と一致する。
func TestQueuesMatchCode(t *testing.T) {
	top, err := messaging.LoadTopology(deploy.FS)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"integration.webhook": domain.WebhookEventTypes(), "integration.email": domain.EmailEventTypes}
	for _, q := range top.QueuesFor("worker-integration") {
		w, ok := want[q.Consumer]
		if !ok {
			continue
		}
		got := slices.Clone(q.Events)
		slices.Sort(got)
		w = slices.Clone(w)
		slices.Sort(w)
		if !slices.Equal(got, w) {
			t.Errorf("%s: yaml %v, code %v", q.Name, got, w)
		}
		delete(want, q.Consumer)
	}
	if len(want) > 0 {
		t.Errorf("キューがない: %v", want)
	}
}
