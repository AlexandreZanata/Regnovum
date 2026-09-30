package domain

import (
	"errors"
	"testing"
	"time"
)

func caseOpenedAt() time.Time {
	return time.Date(2026, time.October, 4, 13, 0, 0, 0, time.UTC)
}

func mustBoundProposal(t *testing.T) Proposal {
	t.Helper()
	proposal := mustPropose(t, termsReq())
	now := termsDeadline().Add(-time.Hour)
	once, err := proposal.Accept("requerente", now)
	if err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	bound, err := once.Accept("requerida", now)
	if err != nil {
		t.Fatalf("second Accept: %v", err)
	}
	return bound
}

func TestParseCaseKindMatrix(t *testing.T) {
	for _, kind := range []CaseKind{CaseChallenge, CaseAgreement, CaseArbitration, CaseSafetyReport} {
		parsed, err := ParseCaseKind(string(kind))
		if err != nil || parsed != kind {
			t.Fatalf("ParseCaseKind(%q) = %v/%v, want %v", string(kind), parsed, err, kind)
		}
		wantPrivate := kind != CaseSafetyReport
		if parsed.Private() != wantPrivate {
			t.Fatalf("Private(%q) = %v, want %v", string(kind), parsed.Private(), wantPrivate)
		}
	}
	for _, raw := range []string{"", "Challenge", " CHALLENGE", "challenge ", "duelo", "safety_report", "sentenca"} {
		if _, err := ParseCaseKind(raw); !errors.Is(err, ErrInvalidCase) {
			t.Fatalf("ParseCaseKind(%q) = %v, want ErrInvalidCase", raw, err)
		}
	}
	if CaseChallenge.ConsentBased() != true || CaseAgreement.ConsentBased() != true || CaseArbitration.ConsentBased() != true {
		t.Fatal("consent kinds must be consent-based")
	}
	if CaseSafetyReport.ConsentBased() || CaseSafetyReport.Private() {
		t.Fatal("safety report must follow the separate rite: never consent-based, never private")
	}
}

func TestConsentCasesRequireBilateralTerms(t *testing.T) {
	bound := mustBoundProposal(t)
	at := caseOpenedAt()
	for _, kind := range []CaseKind{CaseChallenge, CaseAgreement, CaseArbitration} {
		opened, err := OpenConsentCase(kind, bound, "requerente", at)
		if err != nil {
			t.Fatalf("OpenConsentCase(%q): %v", string(kind), err)
		}
		if opened.Status != StatusOpen || !opened.IsPrivate() || opened.HasSentence() {
			t.Fatalf("opened(%q) = %+v, want open private entry without sentence", string(kind), opened)
		}
		if opened.Key != "caso-alpha" || opened.Claimant != "requerente" || opened.Respondent != "requerida" {
			t.Fatalf("opened(%q) = %+v, want negotiation key and both parties", string(kind), opened)
		}
		if _, err := OpenConsentCase(kind, bound, "estranha", at); !errors.Is(err, ErrCaseNotParty) {
			t.Fatalf("stranger OpenConsentCase(%q) = %v, want ErrCaseNotParty", string(kind), err)
		}
		if _, err := OpenConsentCase(kind, bound, "requerente", time.Time{}); !errors.Is(err, ErrInvalidCase) {
			t.Fatalf("zero-instant OpenConsentCase(%q) = %v, want ErrInvalidCase", string(kind), err)
		}
	}

	silent := mustPropose(t, termsReq())
	for _, kind := range []CaseKind{CaseChallenge, CaseAgreement, CaseArbitration} {
		if _, err := OpenConsentCase(kind, silent, "requerente", at); !errors.Is(err, ErrCaseNeedsConsent) {
			t.Fatalf("one-sided OpenConsentCase(%q) = %v, want ErrCaseNeedsConsent", string(kind), err)
		}
	}
	if _, err := OpenConsentCase(CaseSafetyReport, bound, "requerente", at); !errors.Is(err, ErrInvalidCase) {
		t.Fatalf("safety via OpenConsentCase = %v, want ErrInvalidCase: separate rite", err)
	}
	tampered := bound
	tampered.Object = "objeto trocado"
	if _, err := OpenConsentCase(CaseArbitration, tampered, "requerente", at); !errors.Is(err, ErrInvalidTerms) {
		t.Fatalf("tampered OpenConsentCase = %v, want ErrInvalidTerms before competence", err)
	}
}

func TestChallengeRefusalCreatesNoSentence(t *testing.T) {
	proposal := mustPropose(t, termsReq())
	at := caseOpenedAt()
	declined, err := DeclineChallenge(proposal, "requerida", at)
	if err != nil {
		t.Fatalf("DeclineChallenge: %v", err)
	}
	if declined.Status != StatusDeclined || declined.HasSentence() {
		t.Fatalf("declined = %+v, want declined entry without sentence", declined)
	}
	if !declined.IsPrivate() || declined.Key != "caso-alpha" {
		t.Fatalf("declined = %+v, want the private invitation closed, not a safety notice", declined)
	}
	if _, err := DeclineChallenge(proposal, "estranha", at); !errors.Is(err, ErrCaseNotParty) {
		t.Fatalf("stranger DeclineChallenge = %v, want ErrCaseNotParty", err)
	}
	if _, err := DeclineChallenge(proposal, "requerida", time.Time{}); !errors.Is(err, ErrInvalidCase) {
		t.Fatalf("zero-instant DeclineChallenge = %v, want ErrInvalidCase", err)
	}
	bound := mustBoundProposal(t)
	if _, err := DeclineChallenge(bound, "requerida", at); !errors.Is(err, ErrInvalidCase) {
		t.Fatalf("bound DeclineChallenge = %v, want ErrInvalidCase: bound terms already passed the invitation", err)
	}
	tampered := proposal
	tampered.Object = "objeto trocado"
	if _, err := DeclineChallenge(tampered, "requerida", at); !errors.Is(err, ErrInvalidTerms) {
		t.Fatalf("tampered DeclineChallenge = %v, want ErrInvalidTerms", err)
	}
}

func TestSafetyReportFollowsSeparateRite(t *testing.T) {
	at := caseOpenedAt()
	received, err := OpenSafetyReport("denuncia-7", "vigia", "risco-fraude", at)
	if err != nil {
		t.Fatalf("OpenSafetyReport: %v", err)
	}
	if received.Status != StatusReceived || received.IsPrivate() || received.HasSentence() {
		t.Fatalf("received = %+v, want received institutional notice, never a private case nor a sentence", received)
	}
	if received.Claimant != "" || received.Respondent != "" {
		t.Fatalf("received = %+v, want no private parties: safety never attributes credit", received)
	}

	colliding, err := OpenSafetyReport("caso-alpha", "vigia", "risco-fraude", at)
	if err != nil {
		t.Fatalf("colliding OpenSafetyReport: %v", err)
	}
	if colliding.IsPrivate() || colliding.HasSentence() || colliding.Claimant != "" {
		t.Fatalf("colliding = %+v, want key collision to stay institutional", colliding)
	}

	for _, args := range [][3]string{
		{"", "vigia", "risco-fraude"},
		{"denuncia-7", "", "risco-fraude"},
		{"denuncia-7", "vigia", ""},
		{" denuncia-7", "vigia", "risco-fraude"},
	} {
		if _, err := OpenSafetyReport(args[0], args[1], args[2], at); !errors.Is(err, ErrInvalidCase) {
			t.Fatalf("OpenSafetyReport(%q) = %v, want ErrInvalidCase", args, err)
		}
	}
	if _, err := OpenSafetyReport("denuncia-7", "vigia", "risco-fraude", time.Time{}); !errors.Is(err, ErrInvalidCase) {
		t.Fatalf("zero-instant OpenSafetyReport = %v, want ErrInvalidCase", err)
	}
}
