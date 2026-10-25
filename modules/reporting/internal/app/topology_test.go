package app_test

import (
	"slices"
	"testing"

	"subscription-platform/deploy"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/modules/reporting/internal/app"
)

// deploy/messaging.yaml の reporting-projection の購読イベントが、コードの一覧と一致する。
func TestQueueMatchesCode(t *testing.T) {
	top, err := messaging.LoadTopology(deploy.FS)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range top.QueuesFor("worker-projection") {
		if q.Consumer != "reporting.projection" {
			continue
		}
		got, want := slices.Clone(q.Events), slices.Clone(app.ProjectionEventTypes)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("yaml %v, code %v", got, want)
		}
		return
	}
	t.Error("reporting-projection のキューがない")
}
