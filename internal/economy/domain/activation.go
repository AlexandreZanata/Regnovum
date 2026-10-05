package domain

import "strings"

// ActivationCohort names one rollout cohort. The vocabulary is
// closed: only the synthetic canary is authorized in this phase,
// pilot and public cohorts exist to be refused until P45 and a
// separate authorization.
type ActivationCohort string

const (
	// ActivationCanarySynthetic is the only cohort that may open
	// first: disposable test books, no real holder, no real money.
	ActivationCanarySynthetic ActivationCohort = "canary-synthetic"
	// ActivationPilotInternal is a named-but-refused cohort: staff
	// pilot without a separate authorization.
	ActivationPilotInternal ActivationCohort = "pilot-internal"
	// ActivationPublic is a named-but-refused cohort: general
	// availability, never before P45 certification.
	ActivationPublic ActivationCohort = "public"
)

// ParseActivationCohort validates one cohort name. Matching is exact.
func ParseActivationCohort(raw string) (ActivationCohort, error) {
	cohort := ActivationCohort(raw)
	switch cohort {
	case ActivationCanarySynthetic, ActivationPilotInternal, ActivationPublic:
		return cohort, nil
	default:
		return "", ErrUnknownActivationCohort
	}
}

// IsAuthorizedCohort reports whether the cohort may activate first.
// Only the synthetic canary returns true; every other named cohort
// is refused by a separate control.
func IsAuthorizedCohort(cohort ActivationCohort) bool {
	return cohort == ActivationCanarySynthetic
}

// ReversalPlan is the compensating-only way back: the journal is
// never edited or erased, rights are preserved, and the note names
// the fix for the audit trail.
type ReversalPlan struct {
	PreservesJournal bool
	PreservesRights  bool
	CompensatingOnly bool
	Note             string
}

func checkReversalFlags(plan ReversalPlan) error {
	if !plan.PreservesJournal || !plan.PreservesRights || !plan.CompensatingOnly {
		return ErrActivationWithoutReversal
	}
	return nil
}

func checkReversalNote(plan ReversalPlan) error {
	if strings.TrimSpace(plan.Note) == "" {
		return ErrActivationWithoutReversal
	}
	return nil
}

// CheckReversalPlan refuses any way back that edits the journal,
// drops rights, settles without compensation, or forgets the note.
func CheckReversalPlan(plan ReversalPlan) error {
	if err := checkReversalFlags(plan); err != nil {
		return err
	}
	return checkReversalNote(plan)
}

func checkCohortAuthorized(cohort ActivationCohort) error {
	if !IsAuthorizedCohort(cohort) {
		return ErrCohortNotAuthorized
	}
	return nil
}

func checkActivationOpen(frozen bool) error {
	if frozen {
		return ErrActivationFrozen
	}
	return nil
}

func checkActivationHealth(report MonetaryHealthReport) error {
	if !report.Green() {
		return ErrActivationHealthNotGreen
	}
	return nil
}

// AuthorizeCanaryActivation judges the four separate controls in
// order: cohort authorization, open book, green health, reversal
// plan. Each control refuses with its own error, so a single pass
// can never merge two reasons into one green.
func AuthorizeCanaryActivation(cohort ActivationCohort, report MonetaryHealthReport, frozen bool, plan ReversalPlan) error {
	if err := checkCohortAuthorized(cohort); err != nil {
		return err
	}
	if err := checkActivationOpen(frozen); err != nil {
		return err
	}
	if err := checkActivationHealth(report); err != nil {
		return err
	}
	return CheckReversalPlan(plan)
}

// KillSwitchTrips reports whether the guard must freeze new
// mutations now: any freeze-required break, or any critical finding.
// High backlog or lag alone alerts without tripping; a critical
// without freeze-required still trips, so no critical rides along.
func KillSwitchTrips(report MonetaryHealthReport) bool {
	if report.FreezeRequired {
		return true
	}
	for _, finding := range report.Findings {
		if finding.Severity == "critical" {
			return true
		}
	}
	return false
}
