// Package app は監査記録の保存と閲覧（integration.md §3.6・§4.4・§7.2）。注釈と監査ミドルウェアは Platform（auditctx・httpx）が持つ。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	"subscription-platform/modules/audit/internal/adapter/postgres/sqlcgen"
)

// Service は監査のユースケース。
type Service struct{ db *db.ModuleDB }

func New(m *db.ModuleDB) *Service { return &Service{db: m} }

// Write は監査ミドルウェアが応答の前に呼ぶ。before/after は保存の前にもう一度マスキングする（§3.6.2 の安全網）。
// 事業者を跨ぐ運営者の操作（tenant_id が NULL）も書くので system スコープで書く。
func (s *Service) Write(ctx context.Context, recs []auditctx.Record) error {
	return s.db.InTx(db.WithSystemScope(db.ClearScope(ctx), "audit write"), func(ctx context.Context, tx db.Tx) error {
		q := sqlcgen.New(tx)
		for _, r := range recs {
			p, err := params(r)
			if err != nil {
				return err
			}
			if err := q.InsertRecord(ctx, p); err != nil {
				return fmt.Errorf("audit: insert: %w", err)
			}
		}
		return nil
	})
}

func params(r auditctx.Record) (sqlcgen.InsertRecordParams, error) {
	p := sqlcgen.InsertRecordParams{
		ID: uuid.Must(uuid.NewV7()), OccurredAt: r.OccurredAt, ActorType: r.ActorType, ActorID: r.ActorID, ActorRole: r.ActorRole, Action: r.Action,
		TargetType: r.Target.Type, TargetID: r.Target.ID, Outcome: r.Outcome, RequestID: r.RequestID, CorrelationID: r.CorrelationID, HttpMethod: r.HTTPMethod,
		Route: r.Route,
	}
	if r.TenantID != nil {
		u := r.TenantID.UUID()
		p.TenantID = &u
	}
	var err error
	if p.Before, err = jsonOrNil(auditctx.Mask(r.Before)); err != nil {
		return p, err
	}
	if p.After, err = jsonOrNil(auditctx.Mask(r.After)); err != nil {
		return p, err
	}
	if ip, err := netip.ParseAddr(r.SourceIP); err == nil {
		p.SourceIp = &ip
	}
	return p, nil
}

func jsonOrNil(m map[string]any) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal: %w", err)
	}
	return b, nil
}

// Filter は閲覧の絞り込み。
type Filter struct {
	TenantID                 *uuid.UUID
	Action, ActorID          *string
	OccurredFrom, OccurredTo *time.Time
	BeforeAt                 *time.Time
	BeforeID                 *uuid.UUID
	Limit                    int
}

// List は運営者の閲覧（新しい順。has_more は limit+1 件で判定する）。
func (s *Service) List(ctx context.Context, f Filter) ([]sqlcgen.ListRecordsRow, bool, error) {
	var rows []sqlcgen.ListRecordsRow
	err := s.db.ReadTx(db.WithSystemScope(db.ClearScope(ctx), "ops audit records"), func(ctx context.Context, tx db.Tx) error {
		var err error
		rows, err = sqlcgen.New(tx).ListRecords(ctx, sqlcgen.ListRecordsParams{
			Limit: int32(f.Limit + 1), TenantID: f.TenantID, Action: f.Action, ActorID: f.ActorID, OccurredFrom: f.OccurredFrom, OccurredTo: f.OccurredTo,
			BeforeAt: f.BeforeAt, BeforeID: f.BeforeID,
		})
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if len(rows) > f.Limit {
		return rows[:f.Limit], true, nil
	}
	return rows, false, nil
}
