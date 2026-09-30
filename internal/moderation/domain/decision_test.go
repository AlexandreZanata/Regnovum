package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

func TestRuleAndJustificationBounds(t *testing.T) {
	t.Parallel()

	if _, err := domain.ParseRule("MOD-3:spam"); err != nil {
		t.Fatalf("valid rule: %v", err)
	}
	if _, err := domain.ParseRule("   "); !errors.Is(err, domain.ErrInvalidRule) {
		t.Fatalf("blank rule error = %v, want ErrInvalidRule", err)
	}
	if _, err := domain.ParseRule(strings.Repeat("r", 201)); !errors.Is(err, domain.ErrInvalidRule) {
		t.Fatalf("long rule error = %v, want ErrInvalidRule", err)
	}
	if _, err := domain.ParseJustification("Measured justification with scope"); err != nil {
		t.Fatalf("valid justification: %v", err)
	}
	if _, err := domain.ParseJustification(""); !errors.Is(err, domain.ErrInvalidJustification) {
		t.Fatalf("empty justification error = %v, want ErrInvalidJustification", err)
	}
	if _, err := domain.ParseJustification(strings.Repeat("j", 2001)); !errors.Is(err, domain.ErrInvalidJustification) {
		t.Fatalf("long justification error = %v, want ErrInvalidJustification", err)
	}
}

func TestDecisionExpiryCoherence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	past := now.Add(-time.Hour)

	if _, err := domain.DecisionExpiry(domain.ActionSuspension, &future, now); err != nil {
		t.Fatalf("suspension with future expiry: %v", err)
	}
	if _, err := domain.DecisionExpiry(domain.ActionSuspension, nil, now); !errors.Is(err, domain.ErrInvalidExpiry) {
		t.Fatalf("suspension without expiry error = %v, want ErrInvalidExpiry", err)
	}
	if _, err := domain.DecisionExpiry(domain.ActionSuspension, &past, now); !errors.Is(err, domain.ErrInvalidExpiry) {
		t.Fatalf("past expiry error = %v, want ErrInvalidExpiry", err)
	}
	if _, err := domain.DecisionExpiry(domain.ActionWarning, &future, now); !errors.Is(err, domain.ErrInvalidExpiry) {
		t.Fatalf("warning with expiry error = %v, want ErrInvalidExpiry", err)
	}
	if expiry, err := domain.DecisionExpiry(domain.ActionWarning, nil, now); err != nil || expiry != nil {
		t.Fatalf("warning without expiry = %v, %v; want nil, nil", expiry, err)
	}
}

func TestRuleAndJustificationExactBoundsAccept(t *testing.T) {
	t.Parallel()

	// The bound itself belongs to the valid side: a rule of exactly
	// MaxRuleLength and a justification of exactly MaxJustificationLength
	// parse (mutation gate: decision.go:24,34).
	if _, err := domain.ParseRule(strings.Repeat("r", domain.MaxRuleLength)); err != nil {
		t.Fatalf("200-char rule: %v", err)
	}
	if _, err := domain.ParseJustification(strings.Repeat("j", domain.MaxJustificationLength)); err != nil {
		t.Fatalf("2000-char justification: %v", err)
	}
}
