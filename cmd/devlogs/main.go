// Command devlogs は .local/logs/*.jsonl（ローテーション済みを含む）を読み、条件で絞って ts の順に 1 本に並べる（platform.md §8.6.5）。
//
//	devlogs --corr <相関ID> [--format=timeline|json]
//	devlogs --errors --since 10m
//	devlogs cwl '<Logs Insights のクエリ>' [--since 1h]
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*m = append(*m, p)
		}
	}
	return nil
}

// Filter は絞り込みの条件。
type Filter struct {
	Corr, Req, Tenant, Level, MsgPrefix string
	Services                            []string
	Since                               time.Time
	Errors                              bool
}

// Line は 1 行。
type Line struct {
	Raw    string
	Fields map[string]any
	TS     string
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "cwl" {
		if err := runCWL(context.Background(), os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "devlogs cwl:", err) //nolint:forbidigo
			os.Exit(1)
		}
		return
	}
	if err := run(os.Args[1:], os.Stdout, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "devlogs:", err) //nolint:forbidigo
		os.Exit(1)
	}
}

func run(args []string, out io.Writer, now time.Time) error {
	fs := flag.NewFlagSet("devlogs", flag.ContinueOnError)
	var f Filter
	var services multiFlag
	var since, format, dir string
	fs.StringVar(&f.Corr, "corr", "", "相関 ID")
	fs.StringVar(&f.Req, "req", "", "request_id")
	fs.StringVar(&f.Tenant, "tenant", "", "tenant_id")
	fs.StringVar(&f.Level, "level", "", "最低レベル（debug|info|warn|error）")
	fs.Var(&services, "service", "service（複数可）")
	fs.StringVar(&f.MsgPrefix, "msg", "", "msg の前方一致（例 job.）")
	fs.StringVar(&since, "since", "", "期間（例 10m）または RFC 3339")
	fs.BoolVar(&f.Errors, "errors", false, "error 以上か error を持つ行だけ")
	fs.StringVar(&format, "format", "timeline", "timeline | json")
	fs.StringVar(&dir, "dir", ".local/logs", "ログのディレクトリ")
	if err := fs.Parse(args); err != nil {
		return err
	}
	f.Services = services
	if since != "" {
		t, err := parseSince(since, now)
		if err != nil {
			return err
		}
		f.Since = t
	}
	if format != "timeline" && format != "json" {
		return fmt.Errorf("--format must be timeline or json")
	}
	lines, err := Read(dir, f)
	if err != nil {
		return err
	}
	if format == "json" {
		for _, l := range lines {
			fmt.Fprintln(out, l.Raw)
		}
		return nil
	}
	return writeTimeline(out, f, lines)
}

func parseSince(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--since must be a duration (10m) or RFC 3339: %q", s)
	}
	return t, nil
}

var logFile = regexp.MustCompile(`\.jsonl(\.\d+)?$`)

// Read は dir のログを読み、条件で絞って ts の昇順に並べる。
func Read(dir string, f Filter) ([]Line, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Line
	for _, e := range entries {
		if e.IsDir() || !logFile.MatchString(e.Name()) {
			continue
		}
		if err := readFile(filepath.Join(dir, e.Name()), f, &out); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

func readFile(path string, f Filter, out *[]Line) error {
	fh, err := os.Open(path) //nolint:gosec // 開発者が指定したログのディレクトリを読む CLI
	if err != nil {
		return err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		raw := sc.Text()
		if raw == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) != nil {
			continue
		}
		l := Line{Raw: raw, Fields: m, TS: str(m, "ts")}
		if f.match(l) {
			*out = append(*out, l)
		}
	}
	return sc.Err()
}

func str(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		if v != nil {
			return fmt.Sprint(v)
		}
	}
	return ""
}

var levelRank = map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}

func (f Filter) match(l Line) bool {
	m := l.Fields
	if f.Corr != "" && str(m, "correlation_id") != f.Corr {
		return false
	}
	if f.Req != "" && str(m, "request_id") != f.Req {
		return false
	}
	if f.Tenant != "" && str(m, "tenant_id") != f.Tenant {
		return false
	}
	if f.MsgPrefix != "" && !strings.HasPrefix(str(m, "msg"), f.MsgPrefix) {
		return false
	}
	if len(f.Services) > 0 {
		ok := false
		for _, s := range f.Services {
			if str(m, "service") == s {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	lvl := strings.ToUpper(str(m, "level"))
	if f.Level != "" && levelRank[lvl] < levelRank[strings.ToUpper(f.Level)] {
		return false
	}
	if f.Errors {
		if _, hasErr := m["error"]; !hasErr && levelRank[lvl] < levelRank["ERROR"] {
			return false
		}
	}
	if !f.Since.IsZero() {
		t, err := time.Parse(time.RFC3339Nano, l.TS)
		if err != nil || t.Before(f.Since) {
			return false
		}
	}
	return true
}

// timeline に出す主な項目（この順）。
var keyFields = []string{"method", "route", "status", "event_type", "event_id", "queue", "consumer", "duplicate", "aggregate", "id", "from", "to", "reason",
	"job", "job_run_id", "as_of", "attempts", "target", "host", "path", "query", "subscription_id", "invoice_id", "attempt_id", "duration_ms"}

func writeTimeline(out io.Writer, f Filter, lines []Line) error {
	var cond []string
	for _, c := range []struct{ k, v string }{{"correlation_id", f.Corr}, {"request_id", f.Req}, {"tenant_id", f.Tenant}, {"msg", f.MsgPrefix}, {"level", f.Level}} {
		if c.v != "" {
			cond = append(cond, c.k+"="+c.v)
		}
	}
	if f.Errors {
		cond = append(cond, "errors")
	}
	if !f.Since.IsZero() {
		cond = append(cond, "since="+f.Since.UTC().Format(time.RFC3339))
	}
	if len(cond) == 0 {
		cond = append(cond, "all")
	}
	fmt.Fprintf(out, "# %s (%d lines)\n", strings.Join(cond, " "), len(lines))
	for _, l := range lines {
		m := l.Fields
		ts := l.TS
		if t, err := time.Parse(time.RFC3339Nano, l.TS); err == nil {
			ts = t.UTC().Format("15:04:05.000")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s %-18s %-5s %s", ts, str(m, "service"), strings.ToUpper(str(m, "level")), str(m, "msg"))
		for _, k := range keyFields {
			if v := str(m, k); v != "" {
				fmt.Fprintf(&b, " %s=%s", k, v)
			}
		}
		if e, ok := m["error"].(map[string]any); ok {
			if c := str(e, "code"); c != "" {
				fmt.Fprintf(&b, " error.code=%s", c)
			}
			if msg := str(e, "message"); msg != "" {
				fmt.Fprintf(&b, " error.message=%q", msg)
			}
		}
		fmt.Fprintln(out, b.String())
	}
	return nil
}
