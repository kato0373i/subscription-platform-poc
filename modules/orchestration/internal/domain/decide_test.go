package domain_test

import (
	"errors"
	"testing"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/orchestration/internal/domain"
)

// AC-ORC-053
func TestCollectKey(t *testing.T) {
	id := sk.NewID[sk.InvoiceTag]()
	k, err := domain.CollectKey(id, 3)
	if err != nil || k != "invoice:"+id.String()+":3" {
		t.Fatalf("key = %q, %v", k, err)
	}
	for _, seq := range []int{0, -1} {
		if _, err := domain.CollectKey(id, seq); !errors.Is(err, domain.ErrInvalidSeq) {
			t.Errorf("seq %d: %v", seq, err)
		}
	}
}

func seq(n int) *int { return &n }

// AC-ORC-052
func TestDecideOnCollectionDue(t *testing.T) {
	cases := []struct {
		name string
		p    *domain.ActiveProcess
		seq  int
		want domain.DueAction
	}{
		{"プロセスなし", nil, 1, domain.DueCreateRetry},
		{"started", &domain.ActiveProcess{State: domain.StateRenewalBegun}, 1, domain.DueCollect},
		{"invoice_issued", &domain.ActiveProcess{State: domain.StateInvoiceIssued}, 1, domain.DueCollect},
		{"同じ", &domain.ActiveProcess{State: domain.StateCollecting, CollectionSeq: seq(3)}, 3, domain.DueRecollect},
		{"大きい", &domain.ActiveProcess{State: domain.StateAwaitingOutcome, CollectionSeq: seq(2)}, 3, domain.DueAdvance},
		{"小さい", &domain.ActiveProcess{State: domain.StateAwaitingOutcome, CollectionSeq: seq(3)}, 2, domain.DueIgnore},
	}
	for _, tc := range cases {
		if got := domain.DecideOnCollectionDue(tc.p, tc.seq); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

// AC-ORC-024〜026・028
func TestDecideOnInvoiceEvent(t *testing.T) {
	paid, failed, unc := domain.EventInvoicePaid, domain.EventInvoicePaymentFailed, domain.EventInvoiceUncollectible
	cases := []struct {
		ev   domain.InvoiceEvent
		st   domain.SubStatus
		want domain.Command
	}{
		{paid, domain.SubPendingInitial, domain.CmdActivate},
		{paid, domain.SubTrialing, domain.CmdConfirmRenewal},
		{paid, domain.SubActive, domain.CmdConfirmRenewal},
		{paid, domain.SubPastDue, domain.CmdRecover},
		{paid, domain.SubCanceled, domain.CmdAlertPaidAfterCancel},
		{paid, domain.SubSignupFailed, domain.CmdAlertPaidAfterFailed},
		{failed, domain.SubPendingInitial, domain.CmdCompensateSignup},
		{failed, domain.SubTrialing, domain.CmdMarkPastDue},
		{failed, domain.SubActive, domain.CmdMarkPastDue},
		{failed, domain.SubPastDue, domain.CmdNone},
		{failed, domain.SubCanceled, domain.CmdNone},
		{unc, domain.SubActive, domain.CmdCancelDunning},
		{unc, domain.SubPastDue, domain.CmdCancelDunning},
		{unc, domain.SubTrialing, domain.CmdCancelDunning},
		{unc, domain.SubPendingInitial, domain.CmdCompensateSignup},
		{unc, domain.SubCanceled, domain.CmdNone},
	}
	for _, tc := range cases {
		if got := domain.DecideOnInvoiceEvent(tc.ev, tc.st); got.Command != tc.want {
			t.Errorf("%s × %s: %s, want %s", tc.ev, tc.st, got.Command, tc.want)
		}
	}
	if d := domain.DecideOnInvoiceEvent(paid, domain.SubPastDue); d.Outcome != domain.OutcomePaid {
		t.Errorf("outcome = %s", d.Outcome)
	}
	if d := domain.DecideOnInvoiceEvent(unc, domain.SubActive); d.Outcome != domain.OutcomeUncollectible {
		t.Errorf("outcome = %s", d.Outcome)
	}
}

// AC-ORC-054
func TestDecideCheckoutReplay(t *testing.T) {
	cases := []struct {
		saga domain.SagaStatus
		sub  domain.SubStatus
		want domain.ReplayAction
	}{
		{domain.SagaInProgress, "", domain.ReplayResume},
		{domain.SagaPendingOutcome, domain.SubPendingInitial, domain.ReplayRespond202},
		{domain.SagaPendingOutcome, domain.SubActive, domain.ReplayRespond201},
		{domain.SagaPendingOutcome, domain.SubSignupFailed, domain.ReplayDeclined},
		{domain.SagaAborted, "", domain.ReplayStartOver},
		{domain.SagaCompleted, "", domain.ReplayStoredReply},
	}
	for _, tc := range cases {
		if got := domain.DecideCheckoutReplay(tc.saga, tc.sub); got != tc.want {
			t.Errorf("%s/%s: %s, want %s", tc.saga, tc.sub, got, tc.want)
		}
	}
}

func TestDecideBulk(t *testing.T) {
	cases := map[domain.SubStatus]domain.BulkAction{
		domain.SubPastDue: domain.BulkCancelNow, domain.SubActive: domain.BulkSchedule, domain.SubTrialing: domain.BulkSchedule,
		domain.SubPendingInitial: domain.BulkSkip, domain.SubCanceled: domain.BulkSkip,
	}
	for st, want := range cases {
		if got := domain.DecideBulk(st); got != want {
			t.Errorf("%s: %s, want %s", st, got, want)
		}
	}
}
