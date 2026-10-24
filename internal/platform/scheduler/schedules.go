// Package scheduler はジョブの実行（job_runs・重複排除・続きからの再開）を受け持つ（platform.md §3.2・§4・§8.3）。
package scheduler

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Schedules は deploy/schedules.yaml。
type Schedules struct {
	Jobs []JobSpec `yaml:"jobs"`
}

// JobSpec はジョブ 1 つ分の定義。Schedule が空なら手動実行（dev・ops）専用。
type JobSpec struct {
	Name     string `yaml:"name"`
	Process  string `yaml:"process"`
	Schedule string `yaml:"schedule"`
	Dedupe   string `yaml:"dedupe"` // daily | hourly | none
	Devtools bool   `yaml:"devtools"`
}

var jobNamePattern = regexp.MustCompile(`^[a-z]+(-[a-z0-9]+)+$`)

// SchedulerQueue はプロセスの scheduler キューの名前。
func SchedulerQueue(process string) string {
	return "scheduler-" + strings.TrimPrefix(process, "worker-")
}

// LoadSchedules は fsys の schedules.yaml を読み、検証する。
func LoadSchedules(fsys fs.FS) (Schedules, error) {
	b, err := fs.ReadFile(fsys, "schedules.yaml")
	if err != nil {
		return Schedules{}, fmt.Errorf("scheduler: read schedules: %w", err)
	}
	return ParseSchedules(b)
}

// ParseSchedules は YAML を解析して検証する。
func ParseSchedules(b []byte) (Schedules, error) {
	var s Schedules
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return Schedules{}, fmt.Errorf("scheduler: parse schedules: %w", err)
	}
	var errs []error
	seen := map[string]bool{}
	for i := range s.Jobs {
		j := &s.Jobs[i]
		if j.Dedupe == "" {
			j.Dedupe = "daily"
		}
		if !jobNamePattern.MatchString(j.Name) {
			errs = append(errs, fmt.Errorf("job %q: name must be <module>-<name> in kebab-case", j.Name))
		}
		if seen[j.Name] {
			errs = append(errs, fmt.Errorf("job %q: duplicated", j.Name))
		}
		seen[j.Name] = true
		if j.Process != "worker-billing" && j.Process != "worker-integration" {
			errs = append(errs, fmt.Errorf("job %q: process must be worker-billing or worker-integration", j.Name))
		}
		switch j.Dedupe {
		case "daily", "hourly", "none":
		default:
			errs = append(errs, fmt.Errorf("job %q: dedupe must be daily, hourly or none", j.Name))
		}
		if j.Schedule != "" {
			if _, err := ParseCron(j.Schedule); err != nil {
				errs = append(errs, fmt.Errorf("job %q: %w", j.Name, err))
			}
		}
	}
	return s, errors.Join(errs...)
}

// Job は名前でジョブの定義を探す。
func (s Schedules) Job(name string) (JobSpec, bool) {
	for _, j := range s.Jobs {
		if j.Name == name {
			return j, true
		}
	}
	return JobSpec{}, false
}

// For は process のジョブ。
func (s Schedules) For(process string) []JobSpec {
	var out []JobSpec
	for _, j := range s.Jobs {
		if j.Process == process {
			out = append(out, j)
		}
	}
	return out
}
