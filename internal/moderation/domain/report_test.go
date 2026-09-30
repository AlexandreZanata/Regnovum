package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

func TestTargetTypeVocabulary(t *testing.T) {
	t.Parallel()

	for _, target := range []domain.TargetType{domain.TargetArena, domain.TargetArgument, domain.TargetProfile} {
		parsed, err := domain.ParseTargetType(target.String())
		if err != nil || parsed != target || !parsed.IsValid() {
			t.Fatalf("ParseTargetType(%q) = %q, %v", target, parsed, err)
		}
	}
	if _, err := domain.ParseTargetType("comment"); !errors.Is(err, domain.ErrInvalidTargetType) {
		t.Fatalf("comment error = %v, want ErrInvalidTargetType", err)
	}
	if len(domain.AllTargetTypes()) != 3 {
		t.Fatalf("AllTargetTypes has %d entries, want 3", len(domain.AllTargetTypes()))
	}
}

func TestReasonVocabulary(t *testing.T) {
	t.Parallel()

	if len(domain.AllReasons()) != 11 {
		t.Fatalf("AllReasons has %d entries, want 11", len(domain.AllReasons()))
	}
	for _, reason := range domain.AllReasons() {
		parsed, err := domain.ParseReason(reason.String())
		if err != nil || parsed != reason {
			t.Fatalf("ParseReason(%q) = %q, %v", reason, parsed, err)
		}
	}
	// Spam is a valid report category, never a rejection.
	if _, err := domain.ParseReason("spam"); err != nil {
		t.Fatalf("spam must be accepted: %v", err)
	}
	if _, err := domain.ParseReason("politics"); !errors.Is(err, domain.ErrInvalidReason) {
		t.Fatalf("politics error = %v, want ErrInvalidReason", err)
	}
}

func TestReportContextBounds(t *testing.T) {
	t.Parallel()

	if context, err := domain.ParseReportContext(""); err != nil || context != "" {
		t.Fatalf("empty context = %q, %v; want absent", context, err)
	}
	if _, err := domain.ParseReportContext("   "); err != nil {
		t.Fatalf("blank context means absent, got %v", err)
	}
	if _, err := domain.ParseReportContext(strings.Repeat("x", 2001)); !errors.Is(err, domain.ErrInvalidContext) {
		t.Fatalf("oversized error = %v, want ErrInvalidContext", err)
	}
	if _, err := domain.ParseReportContext("valid context with reason"); err != nil {
		t.Fatalf("valid context: %v", err)
	}
}

func TestReportContextExactBoundAndControlBytes(t *testing.T) {
	t.Parallel()

	// A context of exactly MaxReportContextLength is valid; NUL and DEL
	// are never content, even beside the allowed tab and newline
	// (mutation gate: report.go:126,130).
	if _, err := domain.ParseReportContext(strings.Repeat("x", domain.MaxReportContextLength)); err != nil {
		t.Fatalf("2000-char context: %v", err)
	}
	for _, input := range []string{"with\x00nul", "with\x7fdel"} {
		if _, err := domain.ParseReportContext(input); !errors.Is(err, domain.ErrInvalidContext) {
			t.Fatalf("context %q error = %v, want ErrInvalidContext", input, err)
		}
	}
	if _, err := domain.ParseReportContext("tab\there and\nnewline"); err != nil {
		t.Fatalf("tab/newline context: %v", err)
	}
}
