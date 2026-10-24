package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func runOut(t *testing.T, args ...string) string {
	t.Helper()
	var b bytes.Buffer
	now := time.Date(2026, 10, 24, 3, 6, 0, 0, time.UTC)
	if err := run(append([]string{"--dir", "testdata/logs"}, args...), &b, now); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// AC-PLT-083
func TestFilterAndOrderAcrossFiles(t *testing.T) {
	out := runOut(t, "--corr", "C1", "--format=json")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	want := []string{"http.request.completed", "event.published", "event.failed", "domain.state_changed"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %q", lines)
	}
	for i, w := range want {
		if !strings.Contains(lines[i], `"msg":"`+w+`"`) {
			t.Errorf("#%d = %s, want %s", i, lines[i], w)
		}
	}
	tl := runOut(t, "--corr", "C1")
	if !strings.HasPrefix(tl, "# correlation_id=C1 (4 lines)") || !strings.Contains(tl, "03:00:00.350 worker-billing") || !strings.Contains(tl, "from=pending_initial to=active") {
		t.Fatalf("timeline = %s", tl)
	}
	if out := runOut(t, "--msg", "job.", "--format=json"); strings.Count(out, "\n") != 2 {
		t.Fatalf("--msg job. = %s", out)
	}
	if out := runOut(t, "--since", "10m", "--msg", "job.", "--format=json"); strings.Count(out, "\n") != 1 {
		t.Fatalf("--since = %s", out)
	}
	if out := runOut(t, "--errors"); !strings.Contains(out, "error.code=conflict") || strings.Count(out, "\n") != 2 {
		t.Fatalf("--errors = %s", out)
	}
	if out := runOut(t, "--service", "api", "--level", "info", "--format=json"); strings.Count(out, "\n") != 2 {
		t.Fatalf("--service = %s", out)
	}
}
