package deploy_test

import (
	"testing"

	"subscription-platform/deploy"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/scheduler"
)

// messaging.yaml・schedules.yaml の検証（CI の ①）
func TestDeployFilesAreValid(t *testing.T) {
	tp, err := messaging.LoadTopology(deploy.FS)
	if err != nil {
		t.Fatal(err)
	}
	s, err := scheduler.LoadSchedules(deploy.FS)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range s.Jobs {
		q, ok := tp.Queue(scheduler.SchedulerQueue(j.Process))
		if !ok || q.Kind != messaging.KindScheduler {
			t.Errorf("job %s: scheduler queue for %s is missing", j.Name, j.Process)
		}
	}
}
