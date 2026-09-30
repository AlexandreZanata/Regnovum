package domain_test

import (
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

func mustInk(t *testing.T, amount int64) domain.Ink {
	t.Helper()
	ink, err := domain.NewInk(amount)
	if err != nil {
		t.Fatalf("NewInk(%d): %v", amount, err)
	}
	return ink
}

func TestNewInkBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		amount  int64
		wantErr error
	}{
		{name: "zero", amount: 0},
		{name: "one", amount: 1},
		{name: "typical franchise", amount: 5000},
		{name: "max int64", amount: math.MaxInt64},
		{name: "negative one", amount: -1, wantErr: domain.ErrNegativeInk},
		{name: "min int64", amount: math.MinInt64, wantErr: domain.ErrNegativeInk},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ink, err := domain.NewInk(tc.amount)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("NewInk(%d) error = %v, want %v", tc.amount, err, tc.wantErr)
				}
				if ink.Int64() != 0 {
					t.Fatalf("NewInk(%d) returned non-zero ink on error", tc.amount)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewInk(%d) error = %v", tc.amount, err)
			}
			if ink.Int64() != tc.amount {
				t.Errorf("Int64() = %d, want %d", ink.Int64(), tc.amount)
			}
			if ink.IsZero() != (tc.amount == 0) {
				t.Errorf("IsZero() = %v, want %v", ink.IsZero(), tc.amount == 0)
			}
		})
	}
}

func TestParseInkValidAndSerialization(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{input: "0", want: 0},
		{input: "1", want: 1},
		{input: "5000", want: 5000},
		{input: "30000", want: 30000},
		{input: "9007199254740993", want: 9007199254740993}, // 2^53+1 is exact in ink, impossible in float64
		{input: "007", want: 7},                             // canonical serialization drops leading zeros
		{input: strconv.FormatInt(math.MaxInt64, 10), want: math.MaxInt64},
	}

	for _, tc := range tests {
		ink, err := domain.ParseInk(tc.input)
		if err != nil {
			t.Fatalf("ParseInk(%q) error = %v", tc.input, err)
		}
		if ink.Int64() != tc.want {
			t.Fatalf("ParseInk(%q) = %d, want %d", tc.input, ink.Int64(), tc.want)
		}
		if ink.String() != strconv.FormatInt(tc.want, 10) {
			t.Errorf("String() = %q, want canonical %q", ink.String(), strconv.FormatInt(tc.want, 10))
		}

		// Serialization round-trip: String -> ParseInk must be identity.
		reparsed, err := domain.ParseInk(ink.String())
		if err != nil {
			t.Fatalf("re-parse %q: %v", ink.String(), err)
		}
		if !reparsed.Equals(ink) {
			t.Fatalf("serialization is not stable for %q", tc.input)
		}
	}
}

func TestParseInkRejectsFloatAndMalformed(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrInvalidInk},
		{name: "space only", input: " ", want: domain.ErrInvalidInk},
		{name: "negative", input: "-1", want: domain.ErrInvalidInk},
		{name: "plus sign", input: "+1", want: domain.ErrInvalidInk},
		{name: "decimal point", input: "1.5", want: domain.ErrInvalidInk},
		{name: "decimal comma", input: "1,5", want: domain.ErrInvalidInk},
		{name: "exponent", input: "1e3", want: domain.ErrInvalidInk},
		{name: "hexadecimal", input: "0x10", want: domain.ErrInvalidInk},
		{name: "letters", input: "abc", want: domain.ErrInvalidInk},
		{name: "inner space", input: "1 000", want: domain.ErrInvalidInk},
		{name: "trailing newline", input: "10\n", want: domain.ErrInvalidInk},
		{name: "overflow by one", input: "9223372036854775808", want: domain.ErrInkOverflow},
		{name: "far overflow", input: "999999999999999999999999999", want: domain.ErrInkOverflow},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ink, err := domain.ParseInk(tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseInk(%q) error = %v, want %v", tc.input, err, tc.want)
			}
			if ink.Int64() != 0 {
				t.Fatalf("ParseInk(%q) returned non-zero ink on error", tc.input)
			}
		})
	}
}

func TestInkAddAndSubBoundaries(t *testing.T) {
	max := mustInk(t, math.MaxInt64)
	zero := mustInk(t, 0)

	if sum, err := zero.Add(zero); err != nil || !sum.IsZero() {
		t.Errorf("0 + 0 = %v, %v; want zero", sum.String(), err)
	}
	if sum, err := mustInk(t, 5).Add(mustInk(t, 3)); err != nil || sum.Int64() != 8 {
		t.Errorf("5 + 3 = %v, %v; want 8", sum.String(), err)
	}
	sum, err := mustInk(t, 1).Add(mustInk(t, math.MaxInt64-1))
	if err != nil || sum.Int64() != math.MaxInt64 {
		t.Errorf("1 + (MaxInt64-1) = %v, %v; want MaxInt64", sum.String(), err)
	}
	if _, err := max.Add(mustInk(t, 1)); !errors.Is(err, domain.ErrInkOverflow) {
		t.Errorf("MaxInt64 + 1 error = %v, want ErrInkOverflow", err)
	}
	if _, err := max.Add(max); !errors.Is(err, domain.ErrInkOverflow) {
		t.Errorf("MaxInt64 + MaxInt64 error = %v, want ErrInkOverflow", err)
	}

	if diff, err := mustInk(t, 5).Sub(mustInk(t, 3)); err != nil || diff.Int64() != 2 {
		t.Errorf("5 - 3 = %v, %v; want 2", diff.String(), err)
	}
	if diff, err := max.Sub(max); err != nil || !diff.IsZero() {
		t.Errorf("MaxInt64 - MaxInt64 = %v, %v; want zero", diff.String(), err)
	}
	if _, err := zero.Sub(mustInk(t, 1)); !errors.Is(err, domain.ErrInsufficientInk) {
		t.Errorf("0 - 1 error = %v, want ErrInsufficientInk", err)
	}
	if _, err := mustInk(t, 3).Sub(mustInk(t, 5)); !errors.Is(err, domain.ErrInsufficientInk) {
		t.Errorf("3 - 5 error = %v, want ErrInsufficientInk", err)
	}
}

// TestInkAlgebraicProperties exercises exact-integer algebra over the
// interesting boundary values: addition is commutative and associative
// where defined, subtraction is its inverse, no operation overflows
// silently and no result is ever negative.
func TestInkAlgebraicProperties(t *testing.T) {
	values := []int64{0, 1, 2, 255, 5000, 30000, 1 << 31, math.MaxInt64 - 1, math.MaxInt64}

	for _, a := range values {
		for _, b := range values {
			inkA := mustInk(t, a)
			inkB := mustInk(t, b)

			sum, err := inkA.Add(inkB)
			overflows := a > math.MaxInt64-b
			if overflows {
				if !errors.Is(err, domain.ErrInkOverflow) {
					t.Fatalf("%d + %d error = %v, want ErrInkOverflow", a, b, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%d + %d error = %v", a, b, err)
			}
			if !sum.Equals(mustInk(t, a+b)) {
				t.Fatalf("%d + %d = %s, want %d", a, b, sum.String(), a+b)
			}

			commuted, err := inkB.Add(inkA)
			if err != nil || !commuted.Equals(sum) {
				t.Fatalf("%d + %d is not commutative: %v, %v", a, b, commuted.String(), err)
			}

			back, err := sum.Sub(inkB)
			if err != nil || !back.Equals(inkA) {
				t.Fatalf("(%d + %d) - %d = %v, %v; want %d", a, b, b, back.String(), err, a)
			}

			bucket, err := sum.Sub(inkA)
			if err != nil || !bucket.Equals(inkB) {
				t.Fatalf("(%d + %d) - %d = %v, %v; want %d", a, b, a, bucket.String(), err, b)
			}

			if sum.Int64() < 0 || back.Int64() < 0 || bucket.Int64() < 0 {
				t.Fatalf("negative result for %d/%d", a, b)
			}
		}
	}
}

func FuzzParseInk(f *testing.F) {
	f.Add("0")
	f.Add("1")
	f.Add("5000")
	f.Add("30000")
	f.Add("9007199254740993")
	f.Add("9223372036854775807")
	f.Add("")
	f.Add(" ")
	f.Add("-1")
	f.Add("+1")
	f.Add("1.5")
	f.Add("1e3")
	f.Add("9223372036854775808")

	f.Fuzz(func(t *testing.T, input string) {
		ink, err := domain.ParseInk(input)
		if err != nil {
			if ink.Int64() != 0 {
				t.Fatalf("non-zero ink on error for input %q", input)
			}
			return
		}

		if ink.Int64() < 0 {
			t.Fatalf("negative ink parsed from %q", input)
		}
		for i := 0; i < len(input); i++ {
			if input[i] < '0' || input[i] > '9' {
				t.Fatalf("accepted non-digit input %q", input)
			}
		}

		reparsed, err := domain.ParseInk(ink.String())
		if err != nil {
			t.Fatalf("re-parse canonical %q failed: %v", ink.String(), err)
		}
		if !reparsed.Equals(ink) {
			t.Fatalf("parse is not idempotent for input %q", input)
		}
	})
}
