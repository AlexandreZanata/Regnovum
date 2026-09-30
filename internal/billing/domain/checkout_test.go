package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// TestCheckoutIntentStatusVocabularyMatchesSchema pins the local lifecycle
// against the CHECK constraint of migration 00020 and against the transition
// table the database trigger enforces: only settled, expired and failed are
// terminal, even under a delayed provider event.
func TestCheckoutIntentStatusVocabularyMatchesSchema(t *testing.T) {
	t.Parallel()

	want := []string{"created", "open", "paid", "expired", "failed"}
	statuses := domain.AllCheckoutIntentStatuses()
	if len(statuses) != len(want) {
		t.Fatalf("vocabulary size = %d, want %d", len(statuses), len(want))
	}
	for index, raw := range want {
		if statuses[index].String() != raw {
			t.Fatalf("status %d = %q, want %q", index, statuses[index].String(), raw)
		}
		parsed, err := domain.ParseCheckoutIntentStatus(raw)
		if err != nil {
			t.Fatalf("ParseCheckoutIntentStatus(%q) error = %v", raw, err)
		}
		if parsed != statuses[index] {
			t.Fatalf("ParseCheckoutIntentStatus(%q) = %q", raw, parsed)
		}
	}

	settled := map[string]bool{"paid": true}
	terminal := map[string]bool{"paid": true, "expired": true, "failed": true}
	for _, status := range statuses {
		if got := status.IsSettled(); got != settled[status.String()] {
			t.Errorf("IsSettled(%q) = %v", status.String(), got)
		}
		if got := status.IsTerminal(); got != terminal[status.String()] {
			t.Errorf("IsTerminal(%q) = %v", status.String(), got)
		}
	}

	// The provider's own statuses are a different vocabulary: a session that
	// can no longer be paid is not a local lifecycle value.
	for _, raw := range []string{"", "settled", "complete", "cancelled", "PAID"} {
		if _, err := domain.ParseCheckoutIntentStatus(raw); !errors.Is(err, domain.ErrInvalidCheckoutIntentStatus) {
			t.Fatalf("ParseCheckoutIntentStatus(%q) error = %v", raw, err)
		}
	}
}

// TestIdempotencyKeyValueObject covers the retry key: trimmed, printable ASCII,
// bounded, and never the uninitialized value.
func TestIdempotencyKeyValueObject(t *testing.T) {
	t.Parallel()

	key, err := domain.ParseIdempotencyKey("  operation-42  ")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey error = %v", err)
	}
	// The printable range edges are valid key bytes (mutation gate:
	// idempotency_key.go:34).
	edged, err := domain.ParseIdempotencyKey("edge!~key")
	if err != nil || edged.String() != "edge!~key" {
		t.Fatalf("edge key = %q, %v; want edge!~key", edged.String(), err)
	}
	if key.String() != "operation-42" || key.IsZero() {
		t.Fatalf("key = %q", key.String())
	}
	same, err := domain.ParseIdempotencyKey("operation-42")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey error = %v", err)
	}
	other, err := domain.ParseIdempotencyKey("operation-43")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey error = %v", err)
	}
	if !key.Equals(same) || key.Equals(other) || key.Equals(domain.IdempotencyKey{}) {
		t.Error("idempotency key equality semantics are inconsistent")
	}

	invalid := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyIdempotencyKey},
		{name: "blank", input: "   ", want: domain.ErrEmptyIdempotencyKey},
		{name: "too long", input: strings.Repeat("a", 201), want: domain.ErrIdempotencyKeyTooLong},
		{name: "inner space", input: "operation 42", want: domain.ErrInvalidIdempotencyKey},
		{name: "inner DEL", input: "operation\x7f42", want: domain.ErrInvalidIdempotencyKey},
		{name: "newline", input: "operation\n42", want: domain.ErrInvalidIdempotencyKey},
		{name: "non ascii", input: "operação-42", want: domain.ErrInvalidIdempotencyKey},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			rejected, err := domain.ParseIdempotencyKey(testCase.input)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("ParseIdempotencyKey(%q) error = %v, want %v", testCase.input, err, testCase.want)
			}
			if !rejected.IsZero() {
				t.Fatal("a refused key must yield the zero value")
			}
		})
	}

	// The bound leaves room for the account-namespaced key the use case
	// derives, which is what the provider accepts.
	longest, err := domain.ParseIdempotencyKey(strings.Repeat("a", 200))
	if err != nil {
		t.Fatalf("boundary key error = %v", err)
	}
	if len("checkout:"+strings.Repeat("f", 36)+":"+longest.String()) > 255 {
		t.Fatal("the derived provider key must stay inside the provider bound")
	}
}
