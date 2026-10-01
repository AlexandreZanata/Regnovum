package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

type fakeCustody struct {
	snapshot CustodySnapshot
	err      error
	calls    int
}

func (f *fakeCustody) Custody(_ context.Context, season domain.SeasonID, origin string) (CustodySnapshot, error) {
	f.calls++
	if f.err != nil {
		return CustodySnapshot{}, f.err
	}
	if season != "temporada-1" || origin != "tesouro-livre" {
		return CustodySnapshot{}, domain.ErrForbiddenOrigin
	}
	return f.snapshot, nil
}

type fakeLedger struct {
	receipts map[string]domain.ExecutionReceipt
	calls    int
}

func (f *fakeLedger) Append(_ context.Context, order domain.ExecutionOrder) (domain.ExecutionReceipt, error) {
	f.calls++
	receipt, err := domain.SealReceipt(order)
	if err != nil {
		return domain.ExecutionReceipt{}, err
	}
	if f.receipts == nil {
		f.receipts = map[string]domain.ExecutionReceipt{}
	}
	key := string(order.Act) + "|" + order.Digest
	if prior, ok := f.receipts[key]; ok {
		if prior.Amount != order.Amount || prior.Destination != receipt.Destination {
			return domain.ExecutionReceipt{}, domain.ErrTamperedAct
		}
		return prior, nil
	}
	f.receipts[key] = receipt
	return receipt, nil
}

func executeAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func executeAct() domain.RoyalAct {
	anchor := executeAnchor()
	return domain.RoyalAct{
		ID: "decreto-20", Author: "rainha-1", Season: "temporada-1",
		Reign: 2, Competence: "patrimonial", Kind: domain.ActEconomic,
		Reason: "reparacao devida com origem identificada",
		Target: "conta/ana", Effect: "transferir 1000 de tesouro-livre sem mint",
		DecreedAt: anchor, Effective: anchor.Add(time.Hour),
		EndsAt:  anchor.Add(30 * 24 * time.Hour),
		Charter: "v3", Origin: "tesouro-livre", Amount: 1000,
	}
}

func executeSnapshot() domain.CurrentReign {
	anchor := executeAnchor()
	return domain.CurrentReign{
		Season: "temporada-1", Holder: "rainha-1", Reign: 2,
		StartsAt: anchor, EndsAt: anchor.Add(7776000 * time.Second), Open: true,
	}
}

func executeClearance(t *testing.T, act domain.RoyalAct, now time.Time) domain.Clearance {
	t.Helper()
	digest, err := domain.ActDigest(act)
	if err != nil {
		t.Fatalf("ActDigest: %v", err)
	}
	checked := now.Add(-10 * time.Minute)
	first := domain.Approval{
		ID: "conf-1", Act: act.ID, Checker: "auditor-1",
		Season: act.Season, Reign: act.Reign, Digest: digest,
		CheckedAt: checked, ExpiresAt: now.Add(time.Hour),
	}
	second := domain.Approval{
		ID: "conf-2", Act: act.ID, Checker: "auditor-2",
		Season: act.Season, Reign: act.Reign, Digest: digest,
		CheckedAt: checked, ExpiresAt: now.Add(time.Hour),
	}
	clearance, err := domain.RequireIndependentCheck(act, first, second, executeSnapshot(), now)
	if err != nil {
		t.Fatalf("RequireIndependentCheck: %v", err)
	}
	return clearance
}

func executeHarness() (*fakeReigns, *fakeCustody, *fakeLedger) {
	reigns := &fakeReigns{current: executeSnapshot()}
	custody := &fakeCustody{snapshot: CustodySnapshot{Available: 5000}}
	ledger := &fakeLedger{}
	return reigns, custody, ledger
}

func TestExecuteUseCaseMovesOnlyThroughPorts(t *testing.T) {
	now := executeAnchor().Add(2 * time.Hour)
	act := executeAct()
	reigns, custody, ledger := executeHarness()
	receipt, err := NewExecuteUseCase(reigns, custody, ledger).Execute(context.Background(), ExecuteCommand{
		Act: act, Clearance: executeClearance(t, act, now),
		Beneficiary: "ana", Now: now,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if receipt.Act != act.ID || receipt.Season != act.Season || receipt.Reign != act.Reign || receipt.Amount != 1000 {
		t.Fatalf("receipt = %+v, want the ledger citing the same act/book/reign", receipt)
	}
	if !receipt.Personal || receipt.Destination != "ana" {
		t.Fatalf("receipt = %+v, want the personal destination", receipt)
	}
	if reigns.calls != 1 || custody.calls != 1 || ledger.calls != 1 {
		t.Fatalf("calls = %d/%d/%d, want one read per port and one append", reigns.calls, custody.calls, ledger.calls)
	}
}

func TestExecuteUseCaseReplaysIdempotentlyAndRefusesConflict(t *testing.T) {
	now := executeAnchor().Add(2 * time.Hour)
	act := executeAct()
	reigns, custody, ledger := executeHarness()
	uc := NewExecuteUseCase(reigns, custody, ledger)
	cmd := ExecuteCommand{
		Act: act, Clearance: executeClearance(t, act, now),
		Beneficiary: "ana", Now: now,
	}
	first, err := uc.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	second, err := uc.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("replay Execute: %v", err)
	}
	if first != second {
		t.Fatalf("replay = %+v vs %+v, want one observable effect", first, second)
	}
	divergent := cmd
	divergent.Beneficiary = "bia"
	if _, err := uc.Execute(context.Background(), divergent); !errors.Is(err, domain.ErrTamperedAct) {
		t.Fatalf("divergent replay = %v, want ErrTamperedAct", err)
	}
}

func TestExecuteUseCaseRefusesWithoutMovement(t *testing.T) {
	now := executeAnchor().Add(2 * time.Hour)
	act := executeAct()
	reigns, custody, ledger := executeHarness()

	self := ExecuteCommand{
		Act: act, Clearance: executeClearance(t, act, now),
		Beneficiary: "rainha-1", Now: now,
	}
	if _, err := NewExecuteUseCase(reigns, custody, ledger).Execute(context.Background(), self); !errors.Is(err, domain.ErrSelfGrant) {
		t.Fatalf("self-grant = %v, want ErrSelfGrant", err)
	}

	frozenCustody := &fakeCustody{snapshot: CustodySnapshot{Available: 5000, Frozen: true}}
	if _, err := NewExecuteUseCase(reigns, frozenCustody, ledger).Execute(context.Background(), ExecuteCommand{
		Act: act, Clearance: executeClearance(t, act, now),
		Beneficiary: "ana", Now: now,
	}); !errors.Is(err, domain.ErrExecutionFrozen) {
		t.Fatalf("frozen = %v, want ErrExecutionFrozen", err)
	}

	shortCustody := &fakeCustody{snapshot: CustodySnapshot{Available: 10}}
	if _, err := NewExecuteUseCase(reigns, shortCustody, ledger).Execute(context.Background(), ExecuteCommand{
		Act: act, Clearance: executeClearance(t, act, now),
		Beneficiary: "ana", Now: now,
	}); !errors.Is(err, domain.ErrInsufficientTreasury) {
		t.Fatalf("short = %v, want ErrInsufficientTreasury", err)
	}
	if ledger.calls != 0 {
		t.Fatalf("ledger calls = %d, want zero: refusals move no INK", ledger.calls)
	}
}

func TestExecuteUseCaseRevalidatesAuthorityAtEffect(t *testing.T) {
	now := executeAnchor().Add(2 * time.Hour)
	act := executeAct()
	moved := executeSnapshot()
	moved.Holder = "rainha-2"
	moved.Reign = 3
	reigns := &fakeReigns{current: moved}
	custody := &fakeCustody{snapshot: CustodySnapshot{Available: 5000}}
	ledger := &fakeLedger{}
	if _, err := NewExecuteUseCase(reigns, custody, ledger).Execute(context.Background(), ExecuteCommand{
		Act: act, Clearance: executeClearance(t, act, now),
		Beneficiary: "ana", Now: now,
	}); !errors.Is(err, domain.ErrStaleReign) && !errors.Is(err, domain.ErrNotHolder) {
		t.Fatalf("stale reign = %v, want revalidation at the effect", err)
	}
	if ledger.calls != 0 {
		t.Fatalf("ledger calls = %d, want zero after succession", ledger.calls)
	}
}
