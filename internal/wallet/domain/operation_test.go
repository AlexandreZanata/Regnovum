package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

func TestAccountIDValueObject(t *testing.T) {
	var zero domain.AccountID
	if !zero.IsZero() || zero.String() != "" {
		t.Error("zero AccountID must report zero and render empty")
	}

	id := domain.AccountID("018f6b2a-0000-7000-8000-000000000001")
	if id.IsZero() {
		t.Error("a set AccountID must not report zero")
	}
	if id.String() != "018f6b2a-0000-7000-8000-000000000001" {
		t.Errorf("String() = %q", id.String())
	}
	if domain.AccountID("   ").IsZero() != true {
		t.Error("blank AccountID must report zero")
	}
}

func TestIdempotencyKeyValueObject(t *testing.T) {
	valid := []struct {
		input string
		want  string
	}{
		{input: "free:2026-09:018f6b2a", want: "free:2026-09:018f6b2a"},
		{input: "stripe:evt_1Pabcdefghijklmnop", want: "stripe:evt_1Pabcdefghijklmnop"},
		{input: "  argument-publish:42  ", want: "argument-publish:42"},
		{input: strings.Repeat("k", 200), want: strings.Repeat("k", 200)},
		{input: "edge!~key", want: "edge!~key"},
	}
	for _, tc := range valid {
		key, err := domain.ParseIdempotencyKey(tc.input)
		if err != nil {
			t.Fatalf("ParseIdempotencyKey(%q) error = %v", tc.input, err)
		}
		if key.String() != tc.want {
			t.Errorf("ParseIdempotencyKey(%q) = %q, want %q", tc.input, key.String(), tc.want)
		}
		if key.IsZero() {
			t.Errorf("ParseIdempotencyKey(%q) is zero", tc.input)
		}
	}

	invalid := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyIdempotencyKey},
		{name: "blank", input: " \t ", want: domain.ErrEmptyIdempotencyKey},
		{name: "too long", input: strings.Repeat("k", 201), want: domain.ErrIdempotencyKeyTooLong},
		{name: "inner space", input: "free 2026-09", want: domain.ErrInvalidIdempotencyKey},
		{name: "inner DEL", input: "free:\x7f2026", want: domain.ErrInvalidIdempotencyKey},
		{name: "inner newline", input: "free:\n2026", want: domain.ErrInvalidIdempotencyKey},
		{name: "nul byte", input: "free:\x002026", want: domain.ErrInvalidIdempotencyKey},
		{name: "non-ascii", input: "chave:ação", want: domain.ErrInvalidIdempotencyKey},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			key, err := domain.ParseIdempotencyKey(tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseIdempotencyKey(%q) error = %v, want %v", tc.input, err, tc.want)
			}
			if !key.IsZero() {
				t.Fatalf("ParseIdempotencyKey(%q) returned a non-zero key on error", tc.input)
			}
		})
	}
}

func TestIdempotencyKeyValueSemantics(t *testing.T) {
	left, err := domain.ParseIdempotencyKey("free:1")
	if err != nil {
		t.Fatalf("parse left: %v", err)
	}
	same, err := domain.ParseIdempotencyKey("free:1")
	if err != nil {
		t.Fatalf("parse same: %v", err)
	}
	other, err := domain.ParseIdempotencyKey("free:2")
	if err != nil {
		t.Fatalf("parse other: %v", err)
	}

	if !left.Equals(same) {
		t.Error("identical keys must be equal")
	}
	if left.Equals(other) || left.Equals(domain.IdempotencyKey{}) {
		t.Error("distinct keys must not be equal")
	}
}

func mustOperation(t *testing.T, id string) *domain.Operation {
	t.Helper()
	key, err := domain.ParseIdempotencyKey("operation-key-1")
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	reference, err := domain.ParseReference("free:2026-09")
	if err != nil {
		t.Fatalf("parse reference: %v", err)
	}
	operation, err := domain.ReconstituteOperation(
		domain.OperationID(id),
		domain.AccountID("018f6b2a-0000-7000-8000-000000000002"),
		domain.OperationCreditFree,
		key,
		reference,
		domain.Reason{},
		domain.AccountID(""),
		time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("ReconstituteOperation: %v", err)
	}
	return operation
}

func TestReconstituteOperation(t *testing.T) {
	operation := mustOperation(t, "operation-1")
	if operation.ID().String() != "operation-1" {
		t.Errorf("ID() = %q", operation.ID())
	}
	if operation.AccountID().String() != "018f6b2a-0000-7000-8000-000000000002" {
		t.Errorf("AccountID() = %q", operation.AccountID())
	}
	if operation.Type() != domain.OperationCreditFree {
		t.Errorf("Type() = %q", operation.Type())
	}
	if operation.IdempotencyKey().String() != "operation-key-1" {
		t.Errorf("IdempotencyKey() = %q", operation.IdempotencyKey())
	}
	if operation.Reference().String() != "free:2026-09" {
		t.Errorf("Reference() = %q", operation.Reference())
	}
	if !operation.CreatedAt().Equal(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAt() = %v", operation.CreatedAt())
	}
	if !operation.IsCredit() || operation.IsDebit() {
		t.Error("credit operation flags are inconsistent")
	}

	debitKey, _ := domain.ParseIdempotencyKey("operation-key-2")
	debitReference, _ := domain.ParseReference("argument:1")
	debit, err := domain.ReconstituteOperation(
		domain.OperationID("operation-2"),
		domain.AccountID("018f6b2a-0000-7000-8000-000000000002"),
		domain.OperationDebitArgument,
		debitKey,
		debitReference,
		domain.Reason{},
		domain.AccountID(""),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("ReconstituteOperation(debit): %v", err)
	}
	if debit.IsCredit() || !debit.IsDebit() {
		t.Error("debit operation flags are inconsistent")
	}
}

func TestReconstituteOperationValidatesInvariants(t *testing.T) {
	key, _ := domain.ParseIdempotencyKey("operation-key-1")
	reference, _ := domain.ParseReference("free:2026-09")
	accountID := domain.AccountID("018f6b2a-0000-7000-8000-000000000002")

	tests := []struct {
		name          string
		id            domain.OperationID
		accountID     domain.AccountID
		operationType domain.OperationType
		key           domain.IdempotencyKey
		reference     domain.Reference
		reason        domain.Reason
		actor         domain.AccountID
		want          error
	}{
		{
			name:          "empty id",
			id:            "",
			accountID:     accountID,
			operationType: domain.OperationCreditFree,
			key:           key,
			reference:     reference,
			want:          domain.ErrEmptyOperationID,
		},
		{
			name:          "empty account",
			id:            "operation-1",
			accountID:     "",
			operationType: domain.OperationCreditFree,
			key:           key,
			reference:     reference,
			want:          domain.ErrEmptyAccountID,
		},
		{
			name:          "invalid type",
			id:            "operation-1",
			accountID:     accountID,
			operationType: domain.OperationType("mint_ink"),
			key:           key,
			reference:     reference,
			want:          domain.ErrInvalidOperationType,
		},
		{
			name:          "empty key",
			id:            "operation-1",
			accountID:     accountID,
			operationType: domain.OperationCreditFree,
			key:           domain.IdempotencyKey{},
			reference:     reference,
			want:          domain.ErrEmptyIdempotencyKey,
		},
		{
			name:          "empty reference",
			id:            "operation-1",
			accountID:     accountID,
			operationType: domain.OperationCreditFree,
			key:           key,
			reference:     domain.Reference{},
			want:          domain.ErrEmptyReference,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			operation, err := domain.ReconstituteOperation(
				tc.id, tc.accountID, tc.operationType, tc.key, tc.reference, tc.reason, tc.actor, time.Now(),
			)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if operation != nil {
				t.Fatal("expected nil operation on error")
			}
		})
	}
}
