package domain

import (
	"errors"
	"testing"
	"time"
)

func settlementAt() time.Time {
	return time.Date(2026, time.October, 4, 14, 0, 0, 0, time.UTC)
}

func settlementFinalAt() time.Time {
	return rulingAppealDue().Add(time.Hour)
}

func mustOpenEscrow(t *testing.T) Escrow {
	t.Helper()
	custody, err := OpenEscrow(mustBoundProposal(t))
	if err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}
	if custody.BalanceMilli != 20000 || custody.EscrowRef != "caucao-alpha" || custody.Closed() {
		t.Fatalf("custody = %+v, want the pledged balance held, never moved", custody)
	}
	return custody
}

func mustFinalDecision(t *testing.T) Decision {
	t.Helper()
	return mustDecide(t, mustFileDefenses(t, mustOpenHearing(t, "arbitro-1")))
}

func TestConcurrentClaimsReleaseOnce(t *testing.T) {
	custody := mustOpenEscrow(t)
	first, err := custody.ReleaseByAgreement("claim-a", "requerente", 15000, [2]string{"requerente", "requerida"}, settlementAt())
	if err != nil {
		t.Fatalf("first ReleaseByAgreement: %v", err)
	}
	if first.BalanceMilli != 5000 || first.ReleasedTotal() != 15000 || first.Closed() {
		t.Fatalf("first = %+v, want the remainder held in custody", first)
	}
	if _, err := first.ReleaseByAgreement("claim-b", "requerida", 10000, [2]string{"requerente", "requerida"}, settlementAt()); !errors.Is(err, ErrBeyondContract) {
		t.Fatalf("competing claim = %v, want ErrBeyondContract: concurrent claims never overdraw custody", err)
	}
	if first.BalanceMilli != 5000 {
		t.Fatalf("balance = %d, want custody preserved after the refused claim", first.BalanceMilli)
	}
	drained, err := first.ReleaseByAgreement("claim-b", "requerida", 5000, [2]string{"requerida", "requerente"}, settlementAt())
	if err != nil || !drained.Closed() || drained.ReleasedTotal() != 20000 {
		t.Fatalf("drained = %+v/%v, want custody closed exactly at the declared value", drained, err)
	}
	replay, err := drained.ReleaseByAgreement("claim-b", "requerida", 5000, [2]string{"requerente", "requerida"}, settlementAt())
	if err != nil || len(replay.Claims) != 2 || replay.BalanceMilli != 0 {
		t.Fatalf("replay = %+v/%v, want the custody unchanged, never a second release", replay, err)
	}
	if _, err := drained.ReleaseByAgreement("claim-c", "requerente", 1, [2]string{"requerente", "requerida"}, settlementAt()); !errors.Is(err, ErrAlreadyReleased) {
		t.Fatalf("post-drain claim = %v, want ErrAlreadyReleased: escrow releases once", err)
	}
}

func TestCrashReplayAfterDecision(t *testing.T) {
	custody := mustOpenEscrow(t)
	final := mustFinalDecision(t)
	released, err := custody.ReleaseByRuling("claim-r", final, "requerente", settlementFinalAt())
	if err != nil {
		t.Fatalf("ReleaseByRuling: %v", err)
	}
	if released.BalanceMilli != 10000 || released.ReleasedTotal() != 10000 {
		t.Fatalf("released = %+v, want the award released with the remainder held", released)
	}
	replay, err := custody.ReleaseByRuling("claim-r", final, "requerente", settlementFinalAt())
	if err != nil || replay.BalanceMilli != 10000 || len(replay.Claims) != 1 {
		t.Fatalf("crash replay = %+v/%v, want the same authorization, never a double release", replay, err)
	}
	if _, err := released.ReleaseByAgreement("claim-r", "requerida", 10000, [2]string{"requerente", "requerida"}, settlementAt()); !errors.Is(err, ErrSettlementConflict) {
		t.Fatalf("divergent key reuse = %v, want ErrSettlementConflict", err)
	}
	if _, err := custody.ReleaseByRuling("claim-s", final, "requerente", rulingDecidedAt()); !errors.Is(err, ErrNotFinal) {
		t.Fatalf("pre-window release = %v, want ErrNotFinal: failure before finality releases nothing", err)
	}
	appealed, err := final.Appeal("requerida", "reexame do lote 7", rulingDecidedAt().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("Appeal: %v", err)
	}
	if _, err := custody.ReleaseByRuling("claim-t", appealed, "requerente", settlementFinalAt()); !errors.Is(err, ErrNotFinal) {
		t.Fatalf("appealed release = %v, want ErrNotFinal", err)
	}
}

func TestPartialAgreementPreservesCustody(t *testing.T) {
	custody := mustOpenEscrow(t)
	partial, err := custody.ReleaseByAgreement("claim-parcial", "requerida", 6000, [2]string{"requerente", "requerida"}, settlementAt())
	if err != nil {
		t.Fatalf("partial ReleaseByAgreement: %v", err)
	}
	if partial.BalanceMilli != 14000 || partial.ReleasedTotal() != 6000 {
		t.Fatalf("partial = %+v, want the remainder preserved in custody", partial)
	}
	if _, err := custody.ReleaseByAgreement("claim-solo", "requerente", 6000, [2]string{"requerente", "requerente"}, settlementAt()); !errors.Is(err, ErrCaseNeedsConsent) {
		t.Fatalf("one-sided agreement = %v, want ErrCaseNeedsConsent", err)
	}
	if _, err := custody.ReleaseByAgreement("claim-fora", "requerente", 6000, [2]string{"requerente", "estranha"}, settlementAt()); !errors.Is(err, ErrCaseNotParty) {
		t.Fatalf("foreign approval = %v, want ErrCaseNotParty", err)
	}
	rest, err := partial.ReleaseByAgreement("claim-resto", "requerente", 14000, [2]string{"requerente", "requerida"}, settlementAt())
	if err != nil || !rest.Closed() || rest.ReleasedTotal() != 20000 {
		t.Fatalf("rest = %+v/%v, want partial releases summing exactly to the declared value", rest, err)
	}
}

func TestStrangerPreservesCustody(t *testing.T) {
	custody := mustOpenEscrow(t)
	before := custody.BalanceMilli
	if _, err := custody.ReleaseByAgreement("claim-x", "estranha", 10000, [2]string{"requerente", "requerida"}, settlementAt()); !errors.Is(err, ErrCaseNotParty) {
		t.Fatalf("stranger payee = %v, want ErrCaseNotParty", err)
	}
	otherReq := termsReq()
	otherReq.Key = "caso-beta"
	other := mustPropose(t, otherReq)
	now := termsDeadline().Add(-time.Hour)
	once, err := other.Accept("requerente", now)
	if err != nil {
		t.Fatalf("other first Accept: %v", err)
	}
	boundOther, err := once.Accept("requerida", now)
	if err != nil {
		t.Fatalf("other second Accept: %v", err)
	}
	foreign, err := OpenEscrow(boundOther)
	if err != nil {
		t.Fatalf("foreign OpenEscrow: %v", err)
	}
	final := mustFinalDecision(t)
	if _, err := foreign.ReleaseByRuling("claim-y", final, "requerente", settlementFinalAt()); !errors.Is(err, ErrInvalidSettlement) {
		t.Fatalf("foreign ruling release = %v, want ErrInvalidSettlement: external obligations never confiscate", err)
	}
	bareReq := termsReq()
	bareReq.EscrowRef = ""
	bare, err := Propose(bareReq)
	if err != nil {
		t.Fatalf("bare Propose: %v", err)
	}
	onceBare, err := bare.Accept("requerente", now)
	if err != nil {
		t.Fatalf("bare first Accept: %v", err)
	}
	boundBare, err := onceBare.Accept("requerida", now)
	if err != nil {
		t.Fatalf("bare second Accept: %v", err)
	}
	if _, err := OpenEscrow(boundBare); !errors.Is(err, ErrNoEscrow) {
		t.Fatalf("escrowless OpenEscrow = %v, want ErrNoEscrow", err)
	}
	silent := mustPropose(t, termsReq())
	if _, err := OpenEscrow(silent); !errors.Is(err, ErrCaseNeedsConsent) {
		t.Fatalf("one-sided OpenEscrow = %v, want ErrCaseNeedsConsent", err)
	}
	if custody.BalanceMilli != before || len(custody.Claims) != 0 {
		t.Fatalf("custody = %+v, want third-party attempts to leave custody untouched", custody)
	}
	for _, raw := range []string{"", "acordo ", "confisco"} {
		if _, err := ParseSettlementOrigin(raw); !errors.Is(err, ErrInvalidSettlement) {
			t.Fatalf("ParseSettlementOrigin(%q) = %v, want ErrInvalidSettlement", raw, err)
		}
	}
}
