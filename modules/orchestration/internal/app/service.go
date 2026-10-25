// Package app は Orchestration のユースケース（申込・支払方法の変更と削除・退会・更新の投入と処理・回収・結果の反映・一括解約・再開・ops）。
// モジュールをまたぐ呼び出しはそれぞれ別トランザクション。自分のスキーマ（プロセス・サガ）を各段階の前に更新し、落ちたら記録から再開する（§4 共通）。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
	billing "subscription-platform/modules/billing/contracts"
	catalog "subscription-platform/modules/catalog/contracts"
	customer "subscription-platform/modules/customer/contracts"
	"subscription-platform/modules/orchestration/internal/adapter/postgres"
	"subscription-platform/modules/orchestration/internal/domain"
	payments "subscription-platform/modules/payments/contracts"
	subscription "subscription-platform/modules/subscription/contracts"
	tenant "subscription-platform/modules/tenant/contracts"
)

// Deps はユースケースの依存。
type Deps struct {
	DB           *db.ModuleDB
	Clock        sk.Clock
	Tenant       tenant.Service
	Customer     customer.Service
	Catalog      catalog.Service
	Subscription subscription.Service
	Billing      billing.Service
	Payments     payments.Service
	// SendRenewal は orc-renewal.fifo に送る（SendMessageBatch。§4.2）。
	SendRenewal func(ctx context.Context, msgs []messaging.FIFOMessage) error
	// EnqueueJob は scheduler キューにジョブを入れる（rerun。§4.9）。重複排除しない。
	EnqueueJob func(ctx context.Context, job string, asOf sk.Date, params []byte) (uuid.UUID, error)
	// StalledAfter は sweeper の停止の判定（既定 15 分）。SagaStaleAfter はサガの判定（既定 5 分）。
	StalledAfter, SagaStaleAfter time.Duration
	// DispatchPageSize は renewal-dispatch の 1 チャンクの件数（既定 500）。BulkPageSize は一括解約（既定 100）。
	DispatchPageSize, BulkPageSize int
	// Hooks はテスト用の割り込み（途中停止の再現。本番では nil）。
	Hooks Hooks
}

// Hooks はテストで途中停止を起こす（AC-ORC-014・036・047・048）。エラーを返すとその場で処理をやめる。
type Hooks struct {
	AfterIssueInvoice   func(ctx context.Context) error
	BeforeSignupCollect func(ctx context.Context) error               // 申込のプロセスを collecting にした後・Collect の前
	AfterDispatchChunk  func(ctx context.Context, enqueued int) error // 投入の後・カーソルの保存の前
	AfterBulkSubscriber func(ctx context.Context, n int) error
}

// Service はユースケースの入口。
type Service struct{ d Deps }

func New(d Deps) *Service {
	if d.StalledAfter <= 0 {
		d.StalledAfter = 15 * time.Minute
	}
	if d.SagaStaleAfter <= 0 {
		d.SagaStaleAfter = 5 * time.Minute
	}
	if d.DispatchPageSize <= 0 {
		d.DispatchPageSize = 500
	}
	if d.BulkPageSize <= 0 {
		d.BulkPageSize = 100
	}
	return &Service{d: d}
}

// 申込などの API のエラー（HTTP の code への対応は adapter/http）。
var (
	ErrTenantNotActive             = errors.New("orchestration: tenant not active")
	ErrPlanUnavailable             = errors.New("orchestration: plan unavailable")
	ErrPaymentMethodUnavailable    = errors.New("orchestration: payment method unavailable")
	ErrPaymentMethodReentry        = errors.New("orchestration: payment method reentry required")
	ErrIdempotencyKeyReused        = errors.New("orchestration: idempotency key reused")
	ErrNotFound                    = errors.New("orchestration: not found")
	ErrSubscriptionNotModifiable   = errors.New("orchestration: subscription not modifiable")
	ErrPaymentMethodInUse          = errors.New("orchestration: payment method in use")
	ErrHasOpenSubscriptions        = errors.New("orchestration: has open subscriptions")
	ErrBillingRunInProgress        = errors.New("orchestration: billing run in progress")
	ErrConflict                    = errors.New("orchestration: conflict")
	ErrInvalidDate                 = errors.New("orchestration: invalid date")
	errActiveProcessOnSubscription = errors.New("orchestration: another active process on subscription")
)

// PaymentDeclinedError は初回の決済の拒否（422 payment_declined）。
type PaymentDeclinedError struct{ Category string }

func (e *PaymentDeclinedError) Error() string {
	return "orchestration: payment declined: " + e.Category
}

// retryDelay は「同じ契約に進行中のプロセスがある」ときの再配信の間隔（§4.2・§4.3・§4.4）。
const retryDelay = 60 * time.Second

func (s *Service) now() time.Time { return s.d.Clock.Now() }

// system は BYPASSRLS のスコープ（ジョブ・sweeper・ops・サガ）。呼び出し元のスコープは外す。
func system(ctx context.Context, reason string) context.Context {
	return db.WithSystemScope(db.ClearScope(ctx), reason)
}

// unscoped は他モジュールの contracts を呼ぶための context（contracts が自分でスコープを決めるので、
// 呼び出し元の tenant / customer スコープを引き継ぐと system スコープと衝突する）。
func unscoped(ctx context.Context) context.Context { return db.ClearScope(ctx) }

func alert(ctx context.Context, name string, attrs ...slog.Attr) {
	observability.Logger("orchestration.alert").LogAttrs(ctx, slog.LevelError, name, append([]slog.Attr{slog.Bool("alert", true)}, attrs...)...)
}

func warn(ctx context.Context, name string, err error, attrs ...slog.Attr) {
	observability.Logger("orchestration").LogAttrs(ctx, slog.LevelWarn, name, append(attrs, observability.Err(err))...)
}

// conflictRetry は契約の楽観ロックの衝突を 50ms・200ms・800ms で再試行する（§4 共通）。
func conflictRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	delays := []time.Duration{50 * time.Millisecond, 200 * time.Millisecond, 800 * time.Millisecond}
	for i := 0; ; i++ {
		v, err := fn()
		if !errors.Is(err, subscription.ErrVersionConflict) {
			return v, err
		}
		if i == len(delays) {
			return zero, err
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(delays[i]):
		}
	}
}

// invalidTransition は命令が終端などで受け付けられなかった（記録してスキップ。§4.10）。
func invalidTransition(err error) bool {
	return errors.Is(err, subscription.ErrInvalidTransitionKind) || errors.Is(err, subscription.ErrTerminal)
}

// ---- プロセスの読み書き（自分のスキーマ。事業者のスコープ） ----

func (s *Service) tx(ctx context.Context, t sk.TenantID, fn func(ctx context.Context, r *postgres.Repo) error) error {
	return s.d.DB.InTx(db.WithTenantScope(db.ClearScope(ctx), t), func(ctx context.Context, tx db.Tx) error { return fn(ctx, postgres.New(tx)) })
}

// insertProcess はプロセスを作る。同じ契約に進行中のプロセスがあれば errActiveProcessOnSubscription と既存のプロセスを返す。
func (s *Service) insertProcess(ctx context.Context, p *postgres.Process) (*postgres.Process, error) {
	err := s.tx(ctx, p.TenantID, func(ctx context.Context, r *postgres.Repo) error { return r.InsertProcess(ctx, p, s.now()) })
	if !errors.Is(err, postgres.ErrActiveExists) {
		return nil, err
	}
	// 一意制約の違反でトランザクションは中断しているので、読み直しは別のトランザクションで行う
	var existing *postgres.Process
	if rerr := s.tx(ctx, p.TenantID, func(ctx context.Context, r *postgres.Repo) error {
		var err error
		if existing, err = r.ActiveBySubscription(ctx, p.SubscriptionID); err != nil || existing != nil || p.InvoiceID == nil {
			return err
		}
		existing, err = r.ActiveByInvoice(ctx, *p.InvoiceID)
		return err
	}); rerr != nil {
		return nil, rerr
	}
	return existing, errActiveProcessOnSubscription
}

// save はプロセスを保存する（楽観ロックの衝突は呼び出し元が読み直す）。
func (s *Service) save(ctx context.Context, p *postgres.Process) error {
	return s.tx(ctx, p.TenantID, func(ctx context.Context, r *postgres.Repo) error { return r.UpdateProcess(ctx, p, s.now()) })
}

// complete はプロセスを outcome で閉じる。既に終端なら何もしない。
func (s *Service) complete(ctx context.Context, p *postgres.Process, o domain.Outcome) error {
	if p == nil || p.State.Terminal() {
		return nil
	}
	p.Complete(o)
	err := s.save(ctx, p)
	if errors.Is(err, postgres.ErrVersionConflict) {
		// 別の処理（イベントと再開）が先に進めた。読み直して終端でなければもう一度
		fresh, gerr := s.getProcess(ctx, p.TenantID, p.ID)
		if gerr != nil {
			return gerr
		}
		if fresh.State.Terminal() {
			return nil
		}
		fresh.Complete(o)
		return s.save(ctx, fresh)
	}
	return err
}

func (s *Service) getProcess(ctx context.Context, t sk.TenantID, id uuid.UUID) (*postgres.Process, error) {
	var p *postgres.Process
	err := s.tx(ctx, t, func(ctx context.Context, r *postgres.Repo) error {
		var err error
		p, err = r.GetProcess(ctx, id)
		return err
	})
	return p, err
}

// processForInvoice は請求のプロセス。請求 ID がまだ記録されていない同じ契約の進行中のプロセスも対象にする
// （IssueInvoice の直後にイベントが先に届いた場合。0 円の更新など）。
func (s *Service) processForInvoice(ctx context.Context, t sk.TenantID, sub sk.SubscriptionID, inv sk.InvoiceID) (*postgres.Process, error) {
	var p *postgres.Process
	err := s.tx(ctx, t, func(ctx context.Context, r *postgres.Repo) error {
		var err error
		if p, err = r.ActiveByInvoice(ctx, inv); err != nil || p != nil {
			return err
		}
		a, err := r.ActiveBySubscription(ctx, sub)
		if err != nil || a == nil {
			return err
		}
		if a.InvoiceID == nil || *a.InvoiceID == inv {
			p = a
		}
		return nil
	})
	return p, err
}

func correlation(ctx context.Context) string {
	if id, _ := observability.CorrelationFrom(ctx); id != "" {
		return id
	}
	return observability.NewCorrelationID()
}

func strp(s string) *string { return &s }

func errString(err error) *string {
	if err == nil {
		return nil
	}
	return strp(fmt.Sprint(err))
}
