// Package messaging はイベントの送信（EventBridge／direct）と受信（Dispatch・sqsconsumer）を受け持つ（platform.md §3.2・§4・§5・§8.1）。
package messaging

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Topology は deploy/messaging.yaml。Terraform と Go の両方が読む配線の正本。
type Topology struct {
	Bus    string      `yaml:"bus"`
	Queues []QueueSpec `yaml:"queues"`
}

// QueueSpec は購読者 1 つ分のキュー。
type QueueSpec struct {
	Name               string   `yaml:"name"`
	Process            string   `yaml:"process"`
	Consumer           string   `yaml:"consumer"`
	Events             []string `yaml:"events"`
	FIFO               bool     `yaml:"fifo"`
	Concurrency        int      `yaml:"concurrency"`
	VisibilityTimeoutS int      `yaml:"visibility_timeout_s"`
	MaxReceive         int      `yaml:"max_receive"`
	Kind               string   `yaml:"kind"` // events（既定）| scheduler
}

const (
	KindEvents    = "events"
	KindScheduler = "scheduler"
)

var (
	queueNamePattern    = regexp.MustCompile(`^[a-z0-9-]+(\.fifo)?$`)
	consumerNamePattern = regexp.MustCompile(`^[a-z]+\.[a-z_]+$`)
	eventTypePattern    = regexp.MustCompile(`^[a-z]+\.[a-z_0-9]+$`)
	eventPrefixPattern  = regexp.MustCompile(`^[a-z]+\.[a-z_0-9]*$`)
	processes           = map[string]bool{"api": true, "worker-billing": true, "worker-integration": true, "worker-projection": true}
)

// LoadTopology は fsys の messaging.yaml を読み、検証する。
func LoadTopology(fsys fs.FS) (Topology, error) {
	b, err := fs.ReadFile(fsys, "messaging.yaml")
	if err != nil {
		return Topology{}, fmt.Errorf("messaging: read topology: %w", err)
	}
	return ParseTopology(b)
}

// ParseTopology は YAML を解析し、既定値を入れて検証する。
func ParseTopology(b []byte) (Topology, error) {
	var t Topology
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return Topology{}, fmt.Errorf("messaging: parse topology: %w", err)
	}
	for i := range t.Queues {
		q := &t.Queues[i]
		if q.Concurrency == 0 {
			q.Concurrency = 1
		}
		if q.VisibilityTimeoutS == 0 {
			q.VisibilityTimeoutS = 60
		}
		if q.MaxReceive == 0 {
			q.MaxReceive = 5
		}
		if q.Kind == "" {
			q.Kind = KindEvents
		}
	}
	return t, t.Validate()
}

// Validate はトポロジーの規則を検査する。
func (t Topology) Validate() error {
	var errs []error
	if t.Bus == "" {
		errs = append(errs, errors.New("bus is required"))
	}
	seenQ, seenC := map[string]bool{}, map[string]bool{}
	for _, q := range t.Queues {
		where := "queue " + q.Name
		if !queueNamePattern.MatchString(q.Name) {
			errs = append(errs, fmt.Errorf("%s: invalid name", where))
		}
		if seenQ[q.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicated", where))
		}
		seenQ[q.Name] = true
		if !processes[q.Process] {
			errs = append(errs, fmt.Errorf("%s: unknown process %q", where, q.Process))
		}
		if q.FIFO != strings.HasSuffix(q.Name, ".fifo") {
			errs = append(errs, fmt.Errorf("%s: fifo flag and .fifo suffix must agree", where))
		}
		switch q.Kind {
		case KindEvents:
			if !consumerNamePattern.MatchString(q.Consumer) {
				errs = append(errs, fmt.Errorf("%s: consumer must be <module>.<name>: %q", where, q.Consumer))
			}
			if seenC[q.Consumer] {
				errs = append(errs, fmt.Errorf("%s: consumer %q is used by another queue", where, q.Consumer))
			}
			seenC[q.Consumer] = true
		case KindScheduler:
			if len(q.Events) > 0 || q.FIFO {
				errs = append(errs, fmt.Errorf("%s: scheduler queue takes no events and is not fifo", where))
			}
		default:
			errs = append(errs, fmt.Errorf("%s: unknown kind %q", where, q.Kind))
		}
		if q.FIFO && len(q.Events) > 0 {
			errs = append(errs, fmt.Errorf("%s: fifo queue must not subscribe to events (sent directly. C-01)", where))
		}
		for _, e := range q.Events {
			if p, ok := strings.CutPrefix(e, "prefix:"); ok {
				if !eventPrefixPattern.MatchString(p) {
					errs = append(errs, fmt.Errorf("%s: invalid prefix %q", where, e))
				}
			} else if !eventTypePattern.MatchString(e) {
				errs = append(errs, fmt.Errorf("%s: invalid event type %q", where, e))
			}
		}
		if q.Concurrency < 1 || q.VisibilityTimeoutS < 1 || q.MaxReceive < 1 {
			errs = append(errs, fmt.Errorf("%s: concurrency, visibility_timeout_s and max_receive must be positive", where))
		}
	}
	return errors.Join(errs...)
}

// Queue は名前でキューを探す。
func (t Topology) Queue(name string) (QueueSpec, bool) {
	for _, q := range t.Queues {
		if q.Name == name {
			return q, true
		}
	}
	return QueueSpec{}, false
}

// QueuesFor は process が受信するキュー。
func (t Topology) QueuesFor(process string) []QueueSpec {
	var out []QueueSpec
	for _, q := range t.Queues {
		if q.Process == process {
			out = append(out, q)
		}
	}
	return out
}

// Matches は event_type がキューの events に合うか（完全一致か prefix:）。
func (q QueueSpec) Matches(eventType string) bool {
	for _, e := range q.Events {
		if p, ok := strings.CutPrefix(e, "prefix:"); ok {
			if strings.HasPrefix(eventType, p) {
				return true
			}
		} else if e == eventType {
			return true
		}
	}
	return false
}

// Subscribers は event_type を受け取るキュー（direct モードの配り先）。
func (t Topology) Subscribers(eventType string) []QueueSpec {
	var out []QueueSpec
	for _, q := range t.Queues {
		if q.Kind == KindEvents && !q.FIFO && q.Matches(eventType) {
			out = append(out, q)
		}
	}
	return out
}

// DLQName は DLQ の名前（"<name>-dlq"。FIFO は "<base>-dlq.fifo"）。
func DLQName(queue string) string {
	if base, ok := strings.CutSuffix(queue, ".fifo"); ok {
		return base + "-dlq.fifo"
	}
	return queue + "-dlq"
}

// QueueURL は QUEUE_URL_PREFIX とキュー名から URL を作る。
func QueueURL(prefix, name string) string {
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix + name
}
