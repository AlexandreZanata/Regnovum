package domain

import (
	"errors"
	"testing"
	"time"
)

func executeAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func executeAct() RoyalAct {
	anchor := executeAnchor()
	return RoyalAct{
		ID: "decreto-20", Author: "rainha-1", Season: "temporada-1",
		Reign: 2, Competence: "patrimonial", Kind: ActEconomic,
		Reason: "reparacao devida com origem identificada",
		Target: "conta/ana", Effect: "transferir 1000 de tesouro-livre sem mint",
		DecreedAt: anchor, Effective: anchor.Add(time.Hour),
		EndsAt:  anchor.Add(30 * 24 * time.Hour),
		Charter: "v3", Origin: "tesouro-livre", Amount: 1000,
	}
}

func executeCurrent() CurrentReign {
	anchor := executeAnchor()
	return CurrentReign{
		Season: "temporada-1", Holder: "rainha-1", Reign: 2,
		StartsAt: anchor, EndsAt: anchor.Add(7776000 * time.Second), Open: true,
	}
}

func executeClearance(act RoyalAct, now time.Time) Clearance {
	digest, err := ActDigest(act)
	if err != nil {
		panic(err)
	}
	checked := now.Add(-10 * time.Minute)
	first := Approval{
		ID: "conf-1", Act: act.ID, Checker: "auditor-1",
		Season: act.Season, Reign: act.Reign, Digest: digest,
		CheckedAt: checked, ExpiresAt: now.Add(time.Hour),
	}
	second := Approval{
		ID: "conf-2", Act: act.ID, Checker: "auditor-2",
		Season: act.Season, Reign: act.Reign, Digest: digest,
		CheckedAt: checked, ExpiresAt: now.Add(time.Hour),
	}
	clearance, err := RequireIndependentCheck(act, first, second, executeCurrent(), now)
	if err != nil {
		panic(err)
	}
	return clearance
}

func executeCustody() CustodyView {
	return CustodyView{
		Origin: "tesouro-livre", Beneficiary: "ana",
		Available: 5000,
	}
}

func TestPlanExecutionMovesThroughFreeTreasury(t *testing.T) {
	act := executeAct()
	now := executeAnchor().Add(2 * time.Hour)
	order, err := PlanExecution(act, executeClearance(act, now), executeCurrent(), executeCustody(), now)
	if err != nil {
		t.Fatalf("PlanExecution: %v", err)
	}
	if order.Amount != 1000 || order.Origin != "tesouro-livre" || !order.Personal {
		t.Fatalf("order = %+v, want the conserved personal grant", order)
	}
	receipt, err := SealReceipt(order)
	if err != nil {
		t.Fatalf("SealReceipt: %v", err)
	}
	if receipt.Act != act.ID || receipt.Season != act.Season || receipt.Reign != act.Reign || receipt.Digest != order.Digest {
		t.Fatalf("receipt = %+v, want the same act/book/reign/digest", receipt)
	}
	if receipt.Destination != "ana" || !receipt.Personal {
		t.Fatalf("receipt = %+v, want the personal destination", receipt)
	}
}

func TestPlanExecutionMovesInstitutionalPocketWithoutPersonalWealth(t *testing.T) {
	act := executeAct()
	now := executeAnchor().Add(2 * time.Hour)
	custody := executeCustody()
	custody.Beneficiary = ""
	custody.Vault = "caixa-operacional"
	order, err := PlanExecution(act, executeClearance(act, now), executeCurrent(), custody, now)
	if err != nil {
		t.Fatalf("PlanExecution: %v", err)
	}
	if order.Personal {
		t.Fatalf("order = %+v, want Personal false for a pocket move", order)
	}
	receipt, err := SealReceipt(order)
	if err != nil {
		t.Fatalf("SealReceipt: %v", err)
	}
	if receipt.Personal || receipt.Destination != "cofre/caixa-operacional" {
		t.Fatalf("receipt = %+v, want the institutional pocket without personal wealth", receipt)
	}
}

func TestPlanExecutionRefusesForbiddenOrigins(t *testing.T) {
	now := executeAnchor().Add(2 * time.Hour)
	for _, origin := range []string{"escrow-1", "contrato-7", "conta/ana", "tesouro-principal"} {
		act := executeAct()
		act.Origin = origin
		custody := executeCustody()
		custody.Origin = origin
		if _, err := PlanExecution(act, executeClearance(act, now), executeCurrent(), custody, now); !errors.Is(err, ErrForbiddenOrigin) && !errors.Is(err, ErrUnknownOrigin) {
			t.Fatalf("origin %q = %v, want ErrForbiddenOrigin: confiscation, escrow and principal alheio never fund", origin, err)
		}
	}
}

func TestPlanExecutionRefusesSelfGrant(t *testing.T) {
	act := executeAct()
	now := executeAnchor().Add(2 * time.Hour)
	custody := executeCustody()
	custody.Beneficiary = "rainha-1"
	if _, err := PlanExecution(act, executeClearance(act, now), executeCurrent(), custody, now); !errors.Is(err, ErrSelfGrant) {
		t.Fatalf("self-grant = %v, want ErrSelfGrant", err)
	}
}

func TestPlanExecutionRefusesClosedBookInsufficiencyAndFreeze(t *testing.T) {
	act := executeAct()
	now := executeAnchor().Add(2 * time.Hour)
	clearance := executeClearance(act, now)

	closed := executeCurrent()
	closed.Open = false
	if _, err := PlanExecution(act, clearance, closed, executeCustody(), now); !errors.Is(err, ErrSeasonClosed) {
		t.Fatalf("closed book = %v, want ErrSeasonClosed", err)
	}

	short := executeCustody()
	short.Available = 999
	if _, err := PlanExecution(act, clearance, executeCurrent(), short, now); !errors.Is(err, ErrInsufficientTreasury) {
		t.Fatalf("short treasury = %v, want ErrInsufficientTreasury", err)
	}

	frozen := executeCustody()
	frozen.Frozen = true
	if _, err := PlanExecution(act, clearance, executeCurrent(), frozen, now); !errors.Is(err, ErrExecutionFrozen) {
		t.Fatalf("frozen = %v, want ErrExecutionFrozen", err)
	}
}

func TestPlanExecutionRefusesStaleReignAndConflictWithoutReview(t *testing.T) {
	act := executeAct()
	now := executeAnchor().Add(2 * time.Hour)
	clearance := executeClearance(act, now)

	moved := executeCurrent()
	moved.Holder = "rainha-2"
	moved.Reign = 3
	if _, err := PlanExecution(act, clearance, moved, executeCustody(), now); !errors.Is(err, ErrStaleReign) && !errors.Is(err, ErrNotHolder) {
		t.Fatalf("stale reign = %v, want revalidation at the effect", err)
	}

	conflicted := executeCustody()
	conflicted.ConflictPending = true
	if _, err := PlanExecution(act, clearance, executeCurrent(), conflicted, now); !errors.Is(err, ErrConflictedBenefit) {
		t.Fatalf("conflicted = %v, want ErrConflictedBenefit", err)
	}
	repaired := executeCustody()
	repaired.ConflictPending = true
	repaired.IndependentReview = "decisao-5"
	if _, err := PlanExecution(act, clearance, executeCurrent(), repaired, now); err != nil {
		t.Fatalf("reparation with independent procedure: %v", err)
	}
}

func TestPlanExecutionRefusesNonMonetaryAct(t *testing.T) {
	act := executeAct()
	act.Kind = ActOffice
	act.Origin = ""
	act.Amount = 0
	now := executeAnchor().Add(2 * time.Hour)
	if _, err := PlanExecution(act, Clearance{}, executeCurrent(), executeCustody(), now); !errors.Is(err, ErrNonMonetaryAct) {
		t.Fatalf("non-monetary = %v, want ErrNonMonetaryAct", err)
	}
}
