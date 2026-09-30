package domain

import (
	"errors"
	"testing"
	"time"
)

func termsDeadline() time.Time {
	return time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
}

func termsReq() ProposalRequest {
	return ProposalRequest{
		Key: "caso-alpha", Version: 1, Object: "entrega do lote 7",
		Claimant: "requerente", Respondent: "requerida", ValueMilli: 20000,
		EscrowRef: "caucao-alpha", Rite: "rito-acordo-v1", Evidence: "regras-prova-v1",
		Costs: CostsSplit, ExpiresAt: termsDeadline(), Execution: "liberar caucao ao adimplente",
	}
}

func mustPropose(t *testing.T, req ProposalRequest) Proposal {
	t.Helper()
	proposal, err := Propose(req)
	if err != nil {
		t.Fatalf("Propose(%+v): %v", req, err)
	}
	if err := proposal.VerifyTermsHash(); err != nil {
		t.Fatalf("VerifyTermsHash: %v", err)
	}
	return proposal
}

func TestBilateralAcceptanceBindsExactTerms(t *testing.T) {
	proposal := mustPropose(t, termsReq())
	now := termsDeadline().Add(-time.Hour)
	once, err := proposal.Accept("requerente", now)
	if err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	if once.Bound() {
		t.Fatal("single acceptance binds: want both parties before any obligation")
	}
	replay, err := once.Accept("requerente", now)
	if err != nil || replay.Accepted != once.Accepted {
		t.Fatalf("replay = %+v/%v, want the proposal unchanged", replay, err)
	}
	bound, err := once.Accept("requerida", now)
	if err != nil || !bound.Bound() {
		t.Fatalf("second acceptance = %+v/%v, want bound terms", bound, err)
	}
	if bound.ValueMilli != 20000 || bound.EscrowRef != "caucao-alpha" || bound.Costs != CostsSplit {
		t.Fatalf("bound = %+v, want value, escrow and costs intact", bound)
	}
	if _, err := bound.Accept("estranha", now); !errors.Is(err, ErrTermsNotParty) {
		t.Fatalf("stranger Accept = %v, want ErrTermsNotParty", err)
	}
	for _, raw := range []ProposalRequest{
		{Version: 1, Object: "x", Claimant: "a", Respondent: "b", Rite: "r", Evidence: "e", Costs: CostsSplit, ExpiresAt: termsDeadline(), Execution: "x"},
		{Key: "k", Version: 0, Object: "x", Claimant: "a", Respondent: "b", Rite: "r", Evidence: "e", Costs: CostsSplit, ExpiresAt: termsDeadline(), Execution: "x"},
		{Key: "k", Version: 1, Object: "x", Claimant: "socia", Respondent: "socia", Rite: "r", Evidence: "e", Costs: CostsSplit, ExpiresAt: termsDeadline(), Execution: "x"},
		{Key: "k", Version: 1, Object: "x", Claimant: "a", Respondent: "b", Rite: "r", Evidence: "e", Costs: "perdedor", ExpiresAt: termsDeadline(), Execution: "x"},
		{Key: "k", Version: 1, Object: "x", Claimant: "a", Respondent: "b", ValueMilli: -1, Rite: "r", Evidence: "e", Costs: CostsSplit, ExpiresAt: termsDeadline(), Execution: "x"},
	} {
		if _, err := Propose(raw); !errors.Is(err, ErrInvalidTerms) {
			t.Fatalf("Propose(%+v) = %v, want ErrInvalidTerms", raw, err)
		}
	}
}

func TestAmendmentAfterFirstAcceptanceStartsOver(t *testing.T) {
	first := mustPropose(t, termsReq())
	now := termsDeadline().Add(-time.Hour)
	once, err := first.Accept("requerente", now)
	if err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	amendedReq := termsReq()
	amendedReq.Version = 2
	amendedReq.Object = "entrega do lote 7 com multa"
	second := mustPropose(t, amendedReq)
	if second.Bound() {
		t.Fatal("fresh amendment binds: want both acceptances starting over")
	}
	if _, err := second.Accept("requerente", now); err != nil {
		t.Fatalf("fresh first Accept: %v", err)
	}
	if once.Bound() {
		t.Fatal("old revision binds: the first acceptance never travels to amended terms")
	}
	tampered := once
	tampered.Object = amendedReq.Object
	if err := tampered.VerifyTermsHash(); !errors.Is(err, ErrInvalidTerms) {
		t.Fatalf("tampered Verify = %v, want ErrInvalidTerms", err)
	}
	if _, err := tampered.Accept("requerida", now); !errors.Is(err, ErrInvalidTerms) {
		t.Fatalf("tampered Accept = %v, want ErrInvalidTerms before any party", err)
	}
}

func TestExpiredTermsNeverOpenACase(t *testing.T) {
	proposal := mustPropose(t, termsReq())
	deadline := termsDeadline()
	if _, err := proposal.Accept("requerente", deadline.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("tick-1ns Accept: %v", err)
	}
	if _, err := proposal.Accept("requerente", deadline); !errors.Is(err, ErrTermsExpired) {
		t.Fatalf("exact-tick Accept = %v, want ErrTermsExpired: the deadline is exclusive", err)
	}
	if _, err := proposal.Accept("requerente", deadline.Add(time.Hour)); !errors.Is(err, ErrTermsExpired) {
		t.Fatalf("late Accept = %v, want ErrTermsExpired", err)
	}
	born := termsReq()
	born.ExpiresAt = deadline.Add(-2 * time.Hour)
	dead := mustPropose(t, born)
	if _, err := dead.Accept("requerente", deadline.Add(-time.Hour)); !errors.Is(err, ErrTermsExpired) {
		t.Fatalf("dead-on-arrival Accept = %v, want ErrTermsExpired", err)
	}
}

func TestSilentRelationBindsNothing(t *testing.T) {
	proposal := mustPropose(t, termsReq())
	now := termsDeadline().Add(-time.Hour)
	once, err := proposal.Accept("requerente", now)
	if err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	if once.Bound() {
		t.Fatal("one-sided relation binds: want the respondent consent recorded too")
	}
}
