package observability

import (
	"fmt"
	"log/slog"
	"strings"
)

// levelSpec は LOG_LEVEL（例 "info,billing=debug,platform.db=warn"）を解析した結果。
type levelSpec struct {
	def       slog.Level
	overrides map[string]slog.Level
}

func parseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.TrimSpace(s))); err != nil {
		return 0, fmt.Errorf("invalid log level %q", s)
	}
	return l, nil
}

// ParseLevelSpec は LOG_LEVEL を解析する。不正な指定はエラー（AC-PLT-080）。
func ParseLevelSpec(s string) (levelSpec, error) {
	spec := levelSpec{def: slog.LevelInfo, overrides: map[string]slog.Level{}}
	if strings.TrimSpace(s) == "" {
		return spec, nil
	}
	for i, part := range strings.Split(s, ",") {
		name, lvl, hasEq := strings.Cut(part, "=")
		if !hasEq {
			if i != 0 {
				return spec, fmt.Errorf("LOG_LEVEL: default level must come first: %q", part)
			}
			l, err := parseLevel(name)
			if err != nil {
				return spec, fmt.Errorf("LOG_LEVEL: %w", err)
			}
			spec.def = l
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return spec, fmt.Errorf("LOG_LEVEL: empty module name in %q", part)
		}
		l, err := parseLevel(lvl)
		if err != nil {
			return spec, fmt.Errorf("LOG_LEVEL: %w", err)
		}
		spec.overrides[name] = l
	}
	return spec, nil
}

// levelFor は "billing" や "platform.db" のレベルを返す。完全一致 → モジュール名 → 既定 の順。
func (s levelSpec) levelFor(name string) slog.Level {
	if l, ok := s.overrides[name]; ok {
		return l
	}
	if mod, _, ok := strings.Cut(name, "."); ok {
		if l, ok := s.overrides[mod]; ok {
			return l
		}
	}
	return s.def
}
