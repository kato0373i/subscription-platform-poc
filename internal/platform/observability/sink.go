package observability

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	rotateBytes  = 50 << 20
	keepRotated  = 5
	reopenPeriod = time.Minute
)

// fileSink は <dir>/<service>.jsonl に 1 行ずつ書く。50MB でローテーションし 5 世代残す。
// 書き込みに失敗したら黙って捨て、1 分ごとに開き直す（E26。処理は止めない）。
type fileSink struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	size     int64
	failedAt time.Time
	rotateAt int64
}

func openFileSink(dir, service string) (*fileSink, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // ローカル・CI のログ（Fluent Bit のコンテナが読む）
		return nil, fmt.Errorf("log dir: %w", err)
	}
	s := &fileSink{path: filepath.Join(dir, service+".jsonl"), rotateAt: rotateBytes}
	if err := s.open(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *fileSink) open() error {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // ローカル・CI のログ（Fluent Bit のコンテナが読む）
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	s.f, s.size = f, st.Size()
	return nil
}

func (s *fileSink) rotate() {
	_ = s.f.Close()
	s.f = nil
	for i := keepRotated - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", s.path, i), fmt.Sprintf("%s.%d", s.path, i+1))
	}
	_ = os.Rename(s.path, s.path+".1")
	if err := s.open(); err != nil {
		s.failedAt = time.Now()
	}
}

func (s *fileSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		if time.Since(s.failedAt) < reopenPeriod {
			return len(p), nil
		}
		if err := s.open(); err != nil {
			s.failedAt = time.Now()
			return len(p), nil
		}
	}
	if s.size+int64(len(p)) > s.rotateAt && s.size > 0 {
		s.rotate()
		if s.f == nil {
			return len(p), nil
		}
	}
	n, err := s.f.Write(p)
	s.size += int64(n)
	if err != nil {
		_ = s.f.Close()
		s.f = nil
		s.failedAt = time.Now()
	}
	return len(p), nil
}

func (s *fileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// lockedWriter は複数のロガーから同じ出力先に書いても行が混ざらないようにする。
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
