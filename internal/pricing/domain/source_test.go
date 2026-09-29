package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

func TestParseSourceID(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"a", "fonte-1", "bitcoin-brasil-02", "abc123"} {
		source, err := domain.ParseSourceID(raw)
		if err != nil {
			t.Errorf("ParseSourceID(%q): %v", raw, err)
		} else if source.String() != raw {
			t.Errorf("ParseSourceID(%q).String() = %q", raw, source.String())
		}
	}
	long := strings.Repeat("a", 65)
	for _, raw := range []string{"", "A", "fonte_1", "fonte 1", " fonte", "fonte ", "fonte.1", "fonte/1", "á", long} {
		if _, err := domain.ParseSourceID(raw); !errors.Is(err, domain.ErrInvalidSource) {
			t.Errorf("ParseSourceID(%q) = %v, want ErrInvalidSource", raw, err)
		}
	}
}

func TestNewPriceMinor(t *testing.T) {
	t.Parallel()

	price, err := domain.NewPriceMinor(35000000)
	if err != nil {
		t.Fatalf("NewPriceMinor: %v", err)
	}
	if price.Int64() != 35000000 {
		t.Fatalf("price = %d, want 35000000", price.Int64())
	}
	for _, millis := range []int64{0, -1, -35000000} {
		if _, err := domain.NewPriceMinor(millis); !errors.Is(err, domain.ErrInvalidPrice) {
			t.Errorf("NewPriceMinor(%d) = %v, want ErrInvalidPrice", millis, err)
		}
	}
}
