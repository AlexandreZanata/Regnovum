package domain

import (
	"errors"
	"testing"
	"time"
)

func rulingEvidenceDue() time.Time {
	return caseOpenedAt().Add(time.Hour)
}

func rulingDecidedAt() time.Time {
	return rulingEvidenceDue().Add(time.Hour)
}

func rulingAppealDue() time.Time {
	return rulingDecidedAt().Add(time.Hour)
}

func mustOpenHearing(t *testing.T, arbiter string) Hearing {
	t.Helper()
	bound := mustBoundProposal(t)
	entry, err := OpenConsentCase(CaseArbitration, bound, "requerente", caseOpenedAt())
	if err != nil {
		t.Fatalf("OpenConsentCase: %v", err)
	}
	hearing, err := OpenHearing(entry, bound, arbiter, rulingEvidenceDue())
	if err != nil {
		t.Fatalf("OpenHearing: %v", err)
	}
	return hearing
}

func mustFileDefenses(t *testing.T, hearing Hearing) Hearing {
	t.Helper()
	moment := caseOpenedAt().Add(30 * time.Minute)
	next, err := hearing.SubmitEvidence("requerente", "prova-requerente", moment)
	if err != nil {
		t.Fatalf("claimant SubmitEvidence: %v", err)
	}
	next, err = next.SubmitEvidence("requerida", "prova-requerida", moment)
	if err != nil {
		t.Fatalf("respondent SubmitEvidence: %v", err)
	}
	return next
}

func mustDecide(t *testing.T, hearing Hearing) Decision {
	t.Helper()
	decision, err := hearing.Decide("arbitro-1", VerdictUpholdClaimant, 10000, "aplica o termo aceito ao lote 7", rulingDecidedAt(), rulingAppealDue())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return decision
}

func TestArbiterConflictRefused(t *testing.T) {
	bound := mustBoundProposal(t)
	entry, err := OpenConsentCase(CaseArbitration, bound, "requerente", caseOpenedAt())
	if err != nil {
		t.Fatalf("OpenConsentCase: %v", err)
	}
	for _, party := range []string{"requerente", "requerida"} {
		if _, err := OpenHearing(entry, bound, party, rulingEvidenceDue()); !errors.Is(err, ErrRulingConflict) {
			t.Fatalf("party-arbiter OpenHearing(%q) = %v, want ErrRulingConflict", party, err)
		}
	}
	hearing := mustOpenHearing(t, "arbitro-1")
	instructed := mustFileDefenses(t, hearing)
	if _, err := instructed.Decide("requerente", VerdictUpholdClaimant, 10000, "aplica o termo aceito ao lote 7", rulingDecidedAt(), rulingAppealDue()); !errors.Is(err, ErrRulingConflict) {
		t.Fatalf("party Decide = %v, want ErrRulingConflict", err)
	}
	if _, err := instructed.Decide("arbitro-2", VerdictUpholdClaimant, 10000, "aplica o termo aceito ao lote 7", rulingDecidedAt(), rulingAppealDue()); !errors.Is(err, ErrInvalidRuling) {
		t.Fatalf("stranger-arbiter Decide = %v, want ErrInvalidRuling", err)
	}
	for _, raw := range []string{"", "arbitro 1", "verdade-universal"} {
		if _, err := ParseVerdict(raw); !errors.Is(err, ErrInvalidRuling) {
			t.Fatalf("ParseVerdict(%q) = %v, want ErrInvalidRuling", raw, err)
		}
	}
}

func TestLateAndForeignEvidenceRefused(t *testing.T) {
	hearing := mustOpenHearing(t, "arbitro-1")
	due := rulingEvidenceDue()
	moment, err := hearing.SubmitEvidence("requerente", "prova-requerente", due.Add(-time.Nanosecond))
	if err != nil {
		t.Fatalf("tick-1ns SubmitEvidence: %v", err)
	}
	_ = moment
	if _, err := hearing.SubmitEvidence("requerente", "prova-requerente", due); !errors.Is(err, ErrLateEvidence) {
		t.Fatalf("exact-tick SubmitEvidence = %v, want ErrLateEvidence: the deadline is exclusive", err)
	}
	if _, err := hearing.SubmitEvidence("requerida", "prova-requerida", due.Add(time.Hour)); !errors.Is(err, ErrLateEvidence) {
		t.Fatalf("late SubmitEvidence = %v, want ErrLateEvidence", err)
	}
	if _, err := hearing.SubmitEvidence("estranha", "prova-estranha", caseOpenedAt().Add(30*time.Minute)); !errors.Is(err, ErrCaseNotParty) {
		t.Fatalf("stranger SubmitEvidence = %v, want ErrCaseNotParty: proportional access", err)
	}
	if _, err := hearing.SubmitEvidence("requerente", "", caseOpenedAt().Add(30*time.Minute)); !errors.Is(err, ErrInvalidRuling) {
		t.Fatalf("blank-digest SubmitEvidence = %v, want ErrInvalidRuling", err)
	}
}

func TestRulingNeedsBothDefenses(t *testing.T) {
	hearing := mustOpenHearing(t, "arbitro-1")
	half, err := hearing.SubmitEvidence("requerente", "prova-requerente", caseOpenedAt().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("claimant SubmitEvidence: %v", err)
	}
	if _, err := half.Decide("arbitro-1", VerdictUpholdClaimant, 10000, "aplica o termo aceito ao lote 7", rulingDecidedAt(), rulingAppealDue()); !errors.Is(err, ErrMissingDefense) {
		t.Fatalf("one-sided Decide = %v, want ErrMissingDefense", err)
	}
	full := mustFileDefenses(t, hearing)
	decision := mustDecide(t, full)
	if decision.AwardMilli != 10000 || decision.TermsHash == "" || decision.Appealed {
		t.Fatalf("decision = %+v, want capped award bound to the sealed terms with appeal still closed", decision)
	}
	if decision.Verdict != VerdictUpholdClaimant {
		t.Fatalf("verdict = %q, want the rite outcome, never a universal truth", string(decision.Verdict))
	}
}

func TestDecisionBeyondContractRefused(t *testing.T) {
	hearing := mustOpenHearing(t, "arbitro-1")
	instructed := mustFileDefenses(t, hearing)
	if _, err := instructed.Decide("arbitro-1", VerdictUpholdClaimant, 20001, "aplica o termo aceito ao lote 7", rulingDecidedAt(), rulingAppealDue()); !errors.Is(err, ErrBeyondContract) {
		t.Fatalf("over-value Decide = %v, want ErrBeyondContract", err)
	}
	if _, err := instructed.Decide("arbitro-1", VerdictUpholdClaimant, 10000, "", rulingDecidedAt(), rulingAppealDue()); !errors.Is(err, ErrInvalidRuling) {
		t.Fatalf("groundless Decide = %v, want ErrInvalidRuling", err)
	}
	tampered := instructed
	tampered.Terms.Object = "objeto trocado"
	if _, err := tampered.Decide("arbitro-1", VerdictUpholdClaimant, 10000, "aplica o termo aceito ao lote 7", rulingDecidedAt(), rulingAppealDue()); !errors.Is(err, ErrInvalidTerms) {
		t.Fatalf("tampered Decide = %v, want ErrInvalidTerms before the rite", err)
	}
	capped, err := instructed.Decide("arbitro-1", VerdictPartial, 20000, "aplica o termo aceito ao lote 7", rulingDecidedAt(), rulingAppealDue())
	if err != nil || capped.AwardMilli != 20000 {
		t.Fatalf("capped Decide = %+v/%v, want the declared value as ceiling", capped, err)
	}
}

func TestDuplicateAppealRefused(t *testing.T) {
	hearing := mustOpenHearing(t, "arbitro-1")
	decision := mustDecide(t, mustFileDefenses(t, hearing))
	moment := rulingDecidedAt().Add(30 * time.Minute)
	first, err := decision.Appeal("requerida", "reexame do lote 7", moment)
	if err != nil || !first.Appealed {
		t.Fatalf("first Appeal = %+v/%v, want the previsto recurso opened once", first, err)
	}
	if _, err := first.Appeal("requerente", "reexame do lote 7", moment); !errors.Is(err, ErrDuplicateAppeal) {
		t.Fatalf("second Appeal = %v, want ErrDuplicateAppeal", err)
	}
	if _, err := decision.Appeal("estranha", "reexame do lote 7", moment); !errors.Is(err, ErrCaseNotParty) {
		t.Fatalf("stranger Appeal = %v, want ErrCaseNotParty", err)
	}
	if _, err := decision.Appeal("requerida", "", moment); !errors.Is(err, ErrInvalidRuling) {
		t.Fatalf("reasonless Appeal = %v, want ErrInvalidRuling", err)
	}
}
