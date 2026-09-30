package domain_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

func TestBucketVocabulary(t *testing.T) {
	valid := []struct {
		bucket   domain.Bucket
		priority int
	}{
		{bucket: domain.BucketFree, priority: 1},
		{bucket: domain.BucketPurchased, priority: 2},
	}
	for _, tc := range valid {
		parsed, err := domain.ParseBucket(tc.bucket.String())
		if err != nil {
			t.Fatalf("ParseBucket(%q) error = %v", tc.bucket, err)
		}
		if parsed != tc.bucket {
			t.Errorf("ParseBucket(%q) = %q", tc.bucket, parsed)
		}
		if !parsed.IsValid() {
			t.Errorf("%q must be valid", parsed)
		}
		if parsed.Priority() != tc.priority {
			t.Errorf("%q priority = %d, want %d", parsed, parsed.Priority(), tc.priority)
		}
	}

	for _, raw := range []string{"", "free_ink", "Free", "purchased_ink", "GOLD_INK", "FREE", "PURCHASED"} {
		if _, err := domain.ParseBucket(raw); !errors.Is(err, domain.ErrInvalidBucket) {
			t.Errorf("ParseBucket(%q) error = %v, want ErrInvalidBucket", raw, err)
		}
	}

	if domain.Bucket("GOLD_INK").Priority() != 0 {
		t.Error("invalid buckets must never report a spendable priority")
	}
	if !domain.BucketFree.IsValid() || !domain.BucketPurchased.IsValid() {
		t.Error("the two ledger buckets must be valid")
	}
}

// TestOperationTypeVocabularyMirrorsLedgerCheck locks the domain vocabulary
// to the CHECK constraint of app.wallet_operations (migration 00008): any
// divergence must be a deliberate schema migration plus this test.
func TestOperationTypeVocabularyMirrorsLedgerCheck(t *testing.T) {
	expected := []string{
		"credit_free",
		"credit_member",
		"credit_purchase",
		"credit_refund",
		"credit_admin",
		"debit_argument",
		"debit_admin",
		"debit_refund",
		"debit_conversion",
		"expire_free",
	}

	all := domain.AllOperationTypes()
	if len(all) != len(expected) {
		t.Fatalf("AllOperationTypes() has %d entries, want %d", len(all), len(expected))
	}
	for i, raw := range expected {
		if all[i].String() != raw {
			t.Errorf("AllOperationTypes()[%d] = %q, want %q", i, all[i], raw)
		}
		parsed, err := domain.ParseOperationType(raw)
		if err != nil {
			t.Fatalf("ParseOperationType(%q) error = %v", raw, err)
		}
		if parsed.String() != raw || !parsed.IsValid() {
			t.Errorf("ParseOperationType(%q) = %q/%v", raw, parsed, parsed.IsValid())
		}
	}

	for _, raw := range []string{"", "mint_ink", "CREDIT_FREE", "credit_free ", " credit_free", "expire"} {
		if _, err := domain.ParseOperationType(raw); !errors.Is(err, domain.ErrInvalidOperationType) {
			t.Errorf("ParseOperationType(%q) error = %v, want ErrInvalidOperationType", raw, err)
		}
	}
}

func TestOperationTypeDirections(t *testing.T) {
	credits := []domain.OperationType{
		domain.OperationCreditFree,
		domain.OperationCreditMember,
		domain.OperationCreditPurchase,
		domain.OperationCreditRefund,
		domain.OperationCreditAdmin,
	}
	for _, operationType := range credits {
		direction, err := operationType.Direction()
		if err != nil {
			t.Fatalf("Direction(%q) error = %v", operationType, err)
		}
		if direction != domain.DirectionCredit {
			t.Errorf("%q direction = %v, want credit", operationType, direction)
		}
		if !operationType.IsCredit() || operationType.IsDebit() {
			t.Errorf("%q credit/debit flags are inconsistent", operationType)
		}
	}

	debits := []domain.OperationType{
		domain.OperationDebitArgument,
		domain.OperationDebitAdmin,
		domain.OperationDebitRefund,
		domain.OperationDebitConversion,
		domain.OperationExpireFree,
	}
	for _, operationType := range debits {
		direction, err := operationType.Direction()
		if err != nil {
			t.Fatalf("Direction(%q) error = %v", operationType, err)
		}
		if direction != domain.DirectionDebit {
			t.Errorf("%q direction = %v, want debit", operationType, direction)
		}
		if operationType.IsCredit() || !operationType.IsDebit() {
			t.Errorf("%q credit/debit flags are inconsistent", operationType)
		}
	}

	unknown := domain.OperationType("mint_ink")
	if _, err := unknown.Direction(); !errors.Is(err, domain.ErrInvalidOperationType) {
		t.Errorf("Direction(unknown) error = %v, want ErrInvalidOperationType", err)
	}
	if unknown.IsCredit() || unknown.IsDebit() {
		t.Error("unknown operation types must not claim a direction")
	}
}

func TestDirectionApply(t *testing.T) {
	amount := mustInk(t, 5000)

	credited, err := domain.DirectionCredit.Apply(amount)
	if err != nil || credited != 5000 {
		t.Errorf("credit Apply(5000) = %d, %v; want 5000", credited, err)
	}
	debited, err := domain.DirectionDebit.Apply(amount)
	if err != nil || debited != -5000 {
		t.Errorf("debit Apply(5000) = %d, %v; want -5000", debited, err)
	}

	max := mustInk(t, math.MaxInt64)
	if delta, err := domain.DirectionDebit.Apply(max); err != nil || delta != -math.MaxInt64 {
		t.Errorf("debit Apply(MaxInt64) = %d, %v; want -MaxInt64", delta, err)
	}

	for _, direction := range []domain.Direction{domain.DirectionCredit, domain.DirectionDebit} {
		if _, err := direction.Apply(mustInk(t, 0)); !errors.Is(err, domain.ErrZeroAmount) {
			t.Errorf("%v Apply(0) error = %v, want ErrZeroAmount", direction, err)
		}
	}

	var invalid domain.Direction
	if invalid.IsValid() {
		t.Error("zero direction must be invalid")
	}
	if _, err := invalid.Apply(amount); !errors.Is(err, domain.ErrInvalidDirection) {
		t.Errorf("invalid Apply error = %v, want ErrInvalidDirection", err)
	}
	if domain.DirectionCredit.String() != "credit" || domain.DirectionDebit.String() != "debit" || invalid.String() != "invalid" {
		t.Error("direction names are not stable")
	}
}

func TestReferenceValueObject(t *testing.T) {
	valid := []struct {
		input string
		want  string
	}{
		{input: "argument:018f6b2a-0000-7000-8000-000000000001", want: "argument:018f6b2a-0000-7000-8000-000000000001"},
		{input: "free:2026-09", want: "free:2026-09"},
		{input: "stripe:evt_1Pabcdefghijklmnop", want: "stripe:evt_1Pabcdefghijklmnop"},
		{input: "  moderation:case-42  ", want: "moderation:case-42"},
		{input: strings.Repeat("a", 200), want: strings.Repeat("a", 200)},
		{input: "edge!~ref", want: "edge!~ref"},
	}
	for _, tc := range valid {
		reference, err := domain.ParseReference(tc.input)
		if err != nil {
			t.Fatalf("ParseReference(%q) error = %v", tc.input, err)
		}
		if reference.String() != tc.want {
			t.Errorf("ParseReference(%q) = %q, want %q", tc.input, reference.String(), tc.want)
		}
		if reference.IsZero() {
			t.Errorf("ParseReference(%q) is zero", tc.input)
		}
	}

	invalid := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyReference},
		{name: "blank", input: "   ", want: domain.ErrEmptyReference},
		{name: "too long", input: strings.Repeat("a", 201), want: domain.ErrReferenceTooLong},
		{name: "inner space", input: "argument: 42", want: domain.ErrInvalidReference},
		{name: "inner DEL", input: "argument:\x7f42", want: domain.ErrInvalidReference},
		{name: "inner newline", input: "argument:\n42", want: domain.ErrInvalidReference},
		{name: "nul byte", input: "argument:\x0042", want: domain.ErrInvalidReference},
		{name: "non-ascii", input: "argumento:ção", want: domain.ErrInvalidReference},
		{name: "tab", input: "argument:\t42", want: domain.ErrInvalidReference},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			reference, err := domain.ParseReference(tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseReference(%q) error = %v, want %v", tc.input, err, tc.want)
			}
			if !reference.IsZero() {
				t.Fatalf("ParseReference(%q) returned a non-zero reference on error", tc.input)
			}
		})
	}
}

func TestReferenceValueSemantics(t *testing.T) {
	var zero domain.Reference
	if !zero.IsZero() || zero.String() != "" {
		t.Error("zero Reference must render as empty and report zero")
	}

	left, err := domain.ParseReference("argument:1")
	if err != nil {
		t.Fatalf("parse left: %v", err)
	}
	same, err := domain.ParseReference("argument:1")
	if err != nil {
		t.Fatalf("parse same: %v", err)
	}
	other, err := domain.ParseReference("argument:2")
	if err != nil {
		t.Fatalf("parse other: %v", err)
	}

	if !left.Equals(same) {
		t.Error("identical references must be equal")
	}
	if left.Equals(other) || left.Equals(zero) {
		t.Error("distinct references must not be equal")
	}
}
