// Package observability は構造化ログ（slog の JSON）と、相関 ID・標準のログの種類・秘密情報の除去を受け持つ（platform.md §8.6）。
package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// LogConfig は Setup の設定。config パッケージが環境変数から作る。
type LogConfig struct {
	Service  string // api | worker-billing | worker-integration | worker-projection | migrate
	Version  string
	Env      string   // APP_ENV
	Sinks    []string // stdout, file
	Dir      string   // LOG_DIR
	Level    string   // LOG_LEVEL
	Format   string   // json | text（stdout だけ）
	HTTPBody string   // redacted | off
	Stdout   io.Writer
}

// Validate は設定の矛盾を検査する（AC-PLT-084）。
func (c LogConfig) Validate() error {
	var errs []error
	local := c.Env == "local" || c.Env == "test"
	for _, s := range c.Sinks {
		switch s {
		case "stdout":
		case "file":
			if !local {
				errs = append(errs, fmt.Errorf("LOG_SINKS=file is only allowed when APP_ENV is local or test (got %q)", c.Env))
			}
		default:
			errs = append(errs, fmt.Errorf("LOG_SINKS: unknown sink %q", s))
		}
	}
	switch c.HTTPBody {
	case "", "off":
	case "redacted":
		if !local {
			errs = append(errs, fmt.Errorf("LOG_HTTP_BODY=redacted is only allowed when APP_ENV is local or test (got %q)", c.Env))
		}
	default:
		errs = append(errs, fmt.Errorf("LOG_HTTP_BODY: unknown value %q", c.HTTPBody))
	}
	switch c.Format {
	case "", "json", "text":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT: unknown value %q", c.Format))
	}
	if _, err := ParseLevelSpec(c.Level); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Logging はプロセスに 1 つのログの仕組み。
type Logging struct {
	cfg     LogConfig
	spec    levelSpec
	outs    []slog.Handler
	closers []io.Closer
	base    []slog.Attr
	stack   bool

	mu      sync.Mutex
	loggers map[string]*slog.Logger
}

var current atomic.Pointer[Logging]

// Setup は LOG_SINKS・LOG_LEVEL・LOG_HTTP_BODY を読み、出力先を開く。終了時に Close する。
func Setup(cfg LogConfig) (*Logging, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	spec, _ := ParseLevelSpec(cfg.Level)
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if len(cfg.Sinks) == 0 {
		cfg.Sinks = []string{"stdout"}
	}
	l := &Logging{
		cfg:     cfg,
		spec:    spec,
		stack:   cfg.Env == "local" || cfg.Env == "test",
		loggers: map[string]*slog.Logger{},
		base: []slog.Attr{
			slog.String("service", cfg.Service),
			slog.String("version", cfg.Version),
			slog.String("env", cfg.Env),
		},
	}
	opts := &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: replaceAttr}
	for _, s := range slices.Compact(slices.Sorted(slices.Values(cfg.Sinks))) {
		switch s {
		case "stdout":
			w := &lockedWriter{w: cfg.Stdout}
			if cfg.Format == "text" {
				l.outs = append(l.outs, slog.NewTextHandler(w, opts))
			} else {
				l.outs = append(l.outs, slog.NewJSONHandler(w, opts))
			}
		case "file":
			fs, err := openFileSink(cfg.Dir, cfg.Service)
			if err != nil {
				return nil, err
			}
			l.closers = append(l.closers, fs)
			l.outs = append(l.outs, slog.NewJSONHandler(fs, opts))
		}
	}
	current.Store(l)
	slog.SetDefault(l.ForModule("platform"))
	return l, nil
}

// Close は出力先を閉じる。
func (l *Logging) Close() error {
	var errs []error
	for _, c := range l.closers {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// HTTPBodyEnabled は本文を載せるか（LOG_HTTP_BODY=redacted）。
func (l *Logging) HTTPBodyEnabled() bool { return l != nil && l.cfg.HTTPBody == "redacted" }

// Service はプロセス名。
func (l *Logging) Service() string { return l.cfg.Service }

// ForModule はモジュール・部品ごとのロガーを返す。name は "billing" や "platform.db"。
// LOG_LEVEL のモジュール別のレベルが効く。
func (l *Logging) ForModule(name string) *slog.Logger {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lg, ok := l.loggers[name]; ok {
		return lg
	}
	lv := new(slog.LevelVar)
	lv.Set(l.spec.levelFor(name))
	module, component, _ := strings.Cut(name, ".")
	attrs := append(slices.Clone(l.base), slog.String("module", module))
	if component != "" {
		attrs = append(attrs, slog.String("component", component))
	}
	h := &handler{level: lv, outs: l.outs}
	lg := slog.New(h.WithAttrs(attrs))
	l.loggers[name] = lg
	return lg
}

// Current は Setup 済みの Logging（なければ nil）。
func Current() *Logging { return current.Load() }

// logger は Setup 済みならそのロガーを、未設定なら slog.Default を返す。
func logger(name string) *slog.Logger {
	if l := current.Load(); l != nil {
		return l.ForModule(name)
	}
	return slog.Default()
}

// Logger は platform の部品が使うロガー（Setup 前は slog.Default）。
func Logger(name string) *slog.Logger { return logger(name) }

// handler は context の項目を足し、全出力先に同じ内容を書く。
type handler struct {
	level slog.Leveler
	outs  []slog.Handler
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	r.AddAttrs(contextAttrs(ctx)...)
	var errs []error
	for _, o := range h.outs {
		errs = append(errs, o.Handle(ctx, r.Clone()))
	}
	return errors.Join(errs...)
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	outs := make([]slog.Handler, len(h.outs))
	for i, o := range h.outs {
		outs[i] = o.WithAttrs(as)
	}
	return &handler{level: h.level, outs: outs}
}

func (h *handler) WithGroup(name string) slog.Handler {
	outs := make([]slog.Handler, len(h.outs))
	for i, o := range h.outs {
		outs[i] = o.WithGroup(name)
	}
	return &handler{level: h.level, outs: outs}
}

// ErrorCoder はエラーコードを持つエラー（httpx.Problem など）。
type ErrorCoder interface{ ErrorCode() string }

// ErrorKinder はエラーの種類を持つエラー（timeout など）。
type ErrorKinder interface{ ErrorKind() string }

// Err はエラーを error{code, kind, message} の項目にする。
func Err(err error) slog.Attr {
	if err == nil {
		return slog.Attr{}
	}
	attrs := []any{slog.String("message", err.Error())}
	var c ErrorCoder
	if errors.As(err, &c) {
		attrs = append(attrs, slog.String("code", c.ErrorCode()))
	}
	var k ErrorKinder
	if errors.As(err, &k) {
		attrs = append(attrs, slog.String("kind", k.ErrorKind()))
	}
	return slog.Group("error", attrs...)
}

// ErrWithCode はコードを明示したエラーの項目。
func ErrWithCode(code string, err error) slog.Attr {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return slog.Group("error", slog.String("code", code), slog.String("message", msg))
}

// Stack はローカルと CI でだけスタックを返す（それ以外は空の項目）。
func Stack() slog.Attr {
	if l := current.Load(); l != nil && !l.stack {
		return slog.Attr{}
	}
	return slog.String("stack", string(debug.Stack()))
}
