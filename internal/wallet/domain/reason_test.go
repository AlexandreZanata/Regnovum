package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

func TestParseReasonValidCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain text", input: "correção manual de saldo", want: "correção manual de saldo"},
		{name: "accents and punctuation", input: "Ajuste aprovado no ticket #42 — cliente reportou erro.", want: "Ajuste aprovado no ticket #42 — cliente reportou erro."},
		{name: "surrounding whitespace trimmed", input: "  refund de moderação  ", want: "refund de moderação"},
		{name: "maximum length in runes", input: strings.Repeat("á", 500), want: strings.Repeat("á", 500)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, err := domain.ParseReason(tc.input)
			if err != nil {
				t.Fatalf("ParseReason(%q) error = %v", tc.input, err)
			}
			if reason.String() != tc.want {
				t.Errorf("String() = %q, want %q", reason.String(), tc.want)
			}
			if reason.IsZero() {
				t.Error("IsZero() = true, want false")
			}
		})
	}
}

func TestParseReasonInvalidCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyReason},
		{name: "blank", input: " \t ", want: domain.ErrEmptyReason},
		{name: "too long in runes", input: strings.Repeat("á", 501), want: domain.ErrReasonTooLong},
		{name: "inner newline", input: "primeira linha\nsegunda", want: domain.ErrInvalidReason},
		{name: "nul byte", input: "motivo\x00", want: domain.ErrInvalidReason},
		{name: "bidi override", input: "motivo\u202E", want: domain.ErrInvalidReason},
		{name: "invalid utf-8", input: string([]byte{0xff, 0xfe}), want: domain.ErrInvalidReason},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, err := domain.ParseReason(tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseReason(%q) error = %v, want %v", tc.input, err, tc.want)
			}
			if !reason.IsZero() {
				t.Fatal("failed parse must yield the zero reason")
			}
		})
	}
}

func TestReasonValueSemantics(t *testing.T) {
	var zero domain.Reason
	if !zero.IsZero() || zero.String() != "" {
		t.Error("zero Reason must report zero and render empty")
	}

	left, err := domain.ParseReason("motivo a")
	if err != nil {
		t.Fatalf("parse left: %v", err)
	}
	same, err := domain.ParseReason("motivo a")
	if err != nil {
		t.Fatalf("parse same: %v", err)
	}
	other, err := domain.ParseReason("motivo b")
	if err != nil {
		t.Fatalf("parse other: %v", err)
	}

	if !left.Equals(same) {
		t.Error("identical reasons must be equal")
	}
	if left.Equals(other) || left.Equals(zero) {
		t.Error("distinct reasons must not be equal")
	}
}

func TestOperationTypeIsAdmin(t *testing.T) {
	adminTypes := []domain.OperationType{domain.OperationCreditAdmin, domain.OperationDebitAdmin}
	for _, operationType := range adminTypes {
		if !operationType.IsAdmin() {
			t.Errorf("%q must be administrative", operationType)
		}
	}

	regularTypes := []domain.OperationType{
		domain.OperationCreditFree,
		domain.OperationCreditMember,
		domain.OperationCreditPurchase,
		domain.OperationCreditRefund,
		domain.OperationDebitArgument,
		domain.OperationExpireFree,
	}
	for _, operationType := range regularTypes {
		if operationType.IsAdmin() {
			t.Errorf("%q must not be administrative", operationType)
		}
	}

	if domain.OperationType("mint_ink").IsAdmin() {
		t.Error("unknown types must not claim to be administrative")
	}
}
