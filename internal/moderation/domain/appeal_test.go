package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

func TestOutcomeVocabulary(t *testing.T) {
	t.Parallel()

	for _, outcome := range []domain.Outcome{domain.OutcomeUpheld, domain.OutcomeModified, domain.OutcomeReversed} {
		parsed, err := domain.ParseOutcome(outcome.String())
		if err != nil || parsed != outcome {
			t.Fatalf("ParseOutcome(%q) = %q, %v", outcome, parsed, err)
		}
	}
	if _, err := domain.ParseOutcome("dismissed"); !errors.Is(err, domain.ErrInvalidOutcome) {
		t.Fatalf("dismissed error = %v, want ErrInvalidOutcome", err)
	}
	if !domain.OutcomeReversed.RestoresProjection() {
		t.Error("reversed must restore projections")
	}
	if domain.OutcomeUpheld.RestoresProjection() || domain.OutcomeModified.RestoresProjection() {
		t.Error("upheld and modified must leave projections untouched")
	}
}

func TestAppealWindowAndEligibility(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	if domain.AppealExpired(now.Add(-time.Hour), now) {
		t.Error("fresh sanction must stay appealable")
	}
	if !domain.AppealExpired(now.Add(-31*24*time.Hour), now) {
		t.Error("sanction past the window must expire")
	}
	if !domain.AppealExpired(now.Add(-domain.AppealWindow-time.Second), now) {
		t.Error("sanction just past the window must expire")
	}

	for _, action := range domain.AllActions() {
		if action == domain.ActionNoAction {
			if domain.Appealable(action) {
				t.Error("no_action sanctions nobody and admits no appeal")
			}
			continue
		}
		if !domain.Appealable(action) {
			t.Errorf("%q must be appealable", action)
		}
	}
}

func TestAppealWindowBoundaryStaysAppealable(t *testing.T) {
	t.Parallel()

	// The window bound itself still appeals: only strictly older actions
	// expire (mutation gate: appeal.go:77).
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	if domain.AppealExpired(now.Add(-domain.AppealWindow), now) {
		t.Error("sanction exactly at the window must stay appealable")
	}
}
