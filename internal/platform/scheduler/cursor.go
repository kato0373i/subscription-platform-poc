package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/db"
)

// CursorStore はジョブ名に束ねた platform.job_cursors（P-X-7）。scopeKey は「何の続きか」を表すジョブごとのキー。
type CursorStore interface {
	// Load は行がなければ {Cursor: nil, Completed: false} を返す。
	Load(ctx context.Context, scopeKey string) (CursorState, error)
	// Save は業務の書き込みと同じトランザクションで保存できる（tx が nil なら単独）。
	Save(ctx context.Context, tx db.Tx, scopeKey string, cursor json.RawMessage) error
	// Complete は完了済みにする。以後の再実行は Load で Completed=true を見て何もしない。
	Complete(ctx context.Context, tx db.Tx, scopeKey string) error
}

// CursorState はカーソルの状態。
type CursorState struct {
	Cursor    json.RawMessage
	Completed bool
	UpdatedAt time.Time
}

type cursorStore struct {
	platform *db.ModuleDB
	job      string
	runID    uuid.UUID
}

// NewCursorStore はジョブ名に束ねた CursorStore を作る（Runner 以外から使う場合）。
func NewCursorStore(pool *db.Pool, job string, runID uuid.UUID) CursorStore {
	return &cursorStore{platform: pool.Platform(), job: job, runID: runID}
}

func (s *cursorStore) Load(ctx context.Context, scopeKey string) (CursorState, error) {
	var st CursorState
	err := s.platform.ReadTx(db.WithSystemScope(ctx, "scheduler"), func(ctx context.Context, tx db.Tx) error {
		var completed *time.Time
		err := tx.QueryRow(ctx, `SELECT cursor, completed_at, updated_at FROM platform.job_cursors WHERE job = $1 AND scope_key = $2`, s.job, scopeKey).
			Scan(&st.Cursor, &completed, &st.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		st.Completed = completed != nil
		return err
	})
	if err != nil {
		return CursorState{}, fmt.Errorf("scheduler: load cursor: %w", err)
	}
	return st, nil
}

func (s *cursorStore) write(ctx context.Context, tx db.Tx, sql string, args ...any) error {
	if tx != nil {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("scheduler: write cursor: %w", err)
		}
		return nil
	}
	return s.platform.InTx(db.WithSystemScope(ctx, "scheduler"), func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("scheduler: write cursor: %w", err)
		}
		return nil
	})
}

func (s *cursorStore) Save(ctx context.Context, tx db.Tx, scopeKey string, cursor json.RawMessage) error {
	return s.write(ctx, tx, `INSERT INTO platform.job_cursors (job, scope_key, cursor, run_id, updated_at) VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (job, scope_key) DO UPDATE SET cursor = EXCLUDED.cursor, run_id = EXCLUDED.run_id, updated_at = now()`, s.job, scopeKey, cursor, s.runID)
}

func (s *cursorStore) Complete(ctx context.Context, tx db.Tx, scopeKey string) error {
	return s.write(ctx, tx, `INSERT INTO platform.job_cursors (job, scope_key, run_id, completed_at, updated_at) VALUES ($1, $2, $3, now(), now())
		ON CONFLICT (job, scope_key) DO UPDATE SET completed_at = now(), run_id = EXCLUDED.run_id, updated_at = now()`, s.job, scopeKey, s.runID)
}

// anyCursor はこの run が保存したカーソルがあるか（job.started の cursor_resumed）。
func (s *cursorStore) anyCursor(ctx context.Context) (bool, error) {
	var n int
	err := s.platform.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.job_cursors WHERE job = $1 AND run_id = $2 AND completed_at IS NULL`, s.job, s.runID).Scan(&n)
	})
	return n > 0, err //nolint:wrapcheck
}
