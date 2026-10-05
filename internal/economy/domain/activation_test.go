package domain_test

import (
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func greenActivationReport() domain.MonetaryHealthReport {
	return domain.MonetaryHealthReport{}
}

func reversalPlan() domain.ReversalPlan {
	return domain.ReversalPlan{
		PreservesJournal: true,
		PreservesRights:  true,
		CompensatingOnly: true,
		Note:             "compensate the orphan, keep the journal",
	}
}

// TestActivationCanaryOpensFirst proves only the synthetic canary
// authorizes: pilot and public cohorts are named so they can be
// refused, and unknown cohorts never parse.
func TestActivationCanaryOpensFirst(t *testing.T) {
	t.Parallel()

	if !domain.IsAuthorizedCohort(domain.ActivationCanarySynthetic) {
		t.Fatal("canary-synthetic must authorize first")
	}
	for _, cohort := range []domain.ActivationCohort{
		domain.ActivationPilotInternal,
		domain.ActivationPublic,
	} {
		if domain.IsAuthorizedCohort(cohort) {
			t.Fatalf("%s must not authorize before P45", cohort)
		}
	}
	if _, err := domain.ParseActivationCohort("beta-real"); err == nil {
		t.Fatal("unknown cohort parsed: the vocabulary is not closed")
	}
}

// TestActivationControlsRefuseSeparately falsifies each control once
// while the other three hold: cohort, freeze, health and reversal
// plan each refuse with their own error.
func TestActivationControlsRefuseSeparately(t *testing.T) {
	t.Parallel()

	green := greenActivationReport()
	plan := reversalPlan()
	if err := domain.AuthorizeCanaryActivation(domain.ActivationCanarySynthetic, green, false, plan); err != nil {
		t.Fatalf("canary gate = %v, want open", err)
	}
	if err := domain.AuthorizeCanaryActivation(domain.ActivationPublic, green, false, plan); err != domain.ErrCohortNotAuthorized {
		t.Fatalf("public cohort = %v, want ErrCohortNotAuthorized", err)
	}
	if err := domain.AuthorizeCanaryActivation(domain.ActivationCanarySynthetic, green, true, plan); err != domain.ErrActivationFrozen {
		t.Fatalf("frozen gate = %v, want ErrActivationFrozen", err)
	}
	drifted := greenActivationReport()
	drifted.FreezeRequired = true
	drifted.Findings = []domain.MonetaryFinding{{Code: "supply-drift", Severity: "critical"}}
	if err := domain.AuthorizeCanaryActivation(domain.ActivationCanarySynthetic, drifted, false, plan); err != domain.ErrActivationHealthNotGreen {
		t.Fatalf("drifted gate = %v, want ErrActivationHealthNotGreen", err)
	}
	bare := domain.ReversalPlan{PreservesJournal: true, PreservesRights: true, CompensatingOnly: true}
	if err := domain.AuthorizeCanaryActivation(domain.ActivationCanarySynthetic, green, false, bare); err != domain.ErrActivationWithoutReversal {
		t.Fatalf("note-less reversal = %v, want ErrActivationWithoutReversal", err)
	}
}

// TestKillSwitchTripsOnCritical proves the guard trips on any
// freeze-required break and on any critical finding, while a clean
// book and a high-only backlog stay untripped.
func TestKillSwitchTripsOnCritical(t *testing.T) {
	t.Parallel()

	if domain.KillSwitchTrips(greenActivationReport()) {
		t.Fatal("green report tripped: the guard fires without a break")
	}
	frozen := greenActivationReport()
	frozen.FreezeRequired = true
	frozen.Findings = []domain.MonetaryFinding{{Code: "supply-drift", Severity: "critical"}}
	if !domain.KillSwitchTrips(frozen) {
		t.Fatal("freeze-required break did not trip")
	}
	critical := greenActivationReport()
	critical.Findings = []domain.MonetaryFinding{{Code: "archive-diverged", Severity: "critical"}}
	if !domain.KillSwitchTrips(critical) {
		t.Fatal("critical finding without freeze flag did not trip")
	}
	high := greenActivationReport()
	high.Findings = []domain.MonetaryFinding{{Code: "webhooks-backlog", Severity: "high"}}
	if domain.KillSwitchTrips(high) {
		t.Fatal("high-only backlog tripped: alerts without critical must not freeze")
	}
}

// TestReversalPlanKeepsJournal proves the way back never edits the
// journal: any flag dropped or note missing refuses.
func TestReversalPlanKeepsJournal(t *testing.T) {
	t.Parallel()

	if err := domain.CheckReversalPlan(reversalPlan()); err != nil {
		t.Fatalf("compensating plan = %v, want open", err)
	}
	rewritten := reversalPlan()
	rewritten.PreservesJournal = false
	if err := domain.CheckReversalPlan(rewritten); err != domain.ErrActivationWithoutReversal {
		t.Fatalf("journal rewrite = %v, want ErrActivationWithoutReversal", err)
	}
	silent := reversalPlan()
	silent.Note = "   "
	if err := domain.CheckReversalPlan(silent); err != domain.ErrActivationWithoutReversal {
		t.Fatalf("note-less plan = %v, want ErrActivationWithoutReversal", err)
	}
}
