package observability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileSinkRotation(t *testing.T) {
	dir := t.TempDir()
	s, err := openFileSink(dir, "api")
	if err != nil {
		t.Fatal(err)
	}
	s.rotateAt = 100
	line := []byte(strings.Repeat("x", 60) + "\n")
	for range 8 {
		_, _ = s.Write(line)
	}
	_ = s.Close()
	for _, name := range []string{"api.jsonl", "api.jsonl.1", "api.jsonl.5"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s がない: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "api.jsonl.6")); err == nil {
		t.Error("6 世代目が残っている")
	}
}
