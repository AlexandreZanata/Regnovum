package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

func TestReconciliationKindVocabulary(t *testing.T) {
	t.Parallel()

	expected := []string{
		"missing_local", "missing_remote", "amount_mismatch",
		"currency_mismatch", "status_mismatch", "unprocessed_event",
	}
	all := domain.AllReconciliationKinds()
	if len(all) != len(expected) {
		t.Fatalf("AllReconciliationKinds has %d entries, want %d", len(all), len(expected))
	}
	for i, raw := range expected {
		if all[i].String() != raw {
			t.Errorf("AllReconciliationKinds()[%d] = %q, want %q", i, all[i], raw)
		}
		parsed, err := domain.ParseReconciliationKind(raw)
		if err != nil || !parsed.IsValid() {
			t.Fatalf("ParseReconciliationKind(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", "missing", "MISSING_REMOTE", "amount mismatch"} {
		if _, err := domain.ParseReconciliationKind(raw); !errors.Is(err, domain.ErrInvalidReconciliationKind) {
			t.Errorf("ParseReconciliationKind(%q) error = %v, want ErrInvalidReconciliationKind", raw, err)
		}
	}
}

func TestReconciliationWindowValidation(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	window, err := domain.NewReconciliationWindow(start, end)
	if err != nil {
		t.Fatalf("NewReconciliationWindow: %v", err)
	}
	if !window.Start().Equal(start) || !window.End().Equal(end) {
		t.Errorf("window = %v/%v, want %v/%v", window.Start(), window.End(), start, end)
	}

	// Non-UTC instants normalize to UTC without changing the instant.
	local := time.Date(2026, 9, 10, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	normalized, err := domain.NewReconciliationWindow(local, end)
	if err != nil {
		t.Fatalf("NewReconciliationWindow local: %v", err)
	}
	if normalized.Start().Location() != time.UTC || !normalized.Start().Equal(local) {
		t.Errorf("start not normalized to UTC: %v", normalized.Start())
	}

	for _, tc := range []struct {
		name  string
		start time.Time
		end   time.Time
	}{
		{name: "zero start", start: time.Time{}, end: end},
		{name: "zero end", start: start, end: time.Time{}},
		{name: "equal", start: start, end: start},
		{name: "reversed", start: end, end: start},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := domain.NewReconciliationWindow(tc.start, tc.end); !errors.Is(err, domain.ErrInvalidReconciliationWindow) {
				t.Fatalf("error = %v, want ErrInvalidReconciliationWindow", err)
			}
		})
	}
}
