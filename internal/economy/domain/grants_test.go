package domain_test

import (
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func TestMonetaryGrantSourceDecidesFunding(t *testing.T) {
	t.Parallel()

	stock, err := domain.NewMilliInk(1000)
	if err != nil {
		t.Fatalf("NewMilliInk(1000): %v", err)
	}
	requested, err := domain.NewMilliInk(400)
	if err != nil {
		t.Fatalf("NewMilliInk(400): %v", err)
	}
	source, err := domain.MonetaryGrantSource(false, stock, requested)
	if err != nil || source != domain.GrantSourceLegacy {
		t.Fatalf("pre-genesis = %q, %v; want legacy, nil", source, err)
	}
	source, err = domain.MonetaryGrantSource(true, stock, requested)
	if err != nil || source != domain.GrantSourceTreasury {
		t.Fatalf("funded post-genesis = %q, %v; want treasury, nil", source, err)
	}
	exact, err := domain.NewMilliInk(1000)
	if err != nil {
		t.Fatalf("NewMilliInk(1000): %v", err)
	}
	if source, err := domain.MonetaryGrantSource(true, stock, exact); err != nil || source != domain.GrantSourceTreasury {
		t.Fatalf("exact stock = %q, %v; want treasury, nil", source, err)
	}
	over, err := domain.NewMilliInk(1001)
	if err != nil {
		t.Fatalf("NewMilliInk(1001): %v", err)
	}
	if source, err := domain.MonetaryGrantSource(true, stock, over); err != nil || source != domain.GrantSourceUnavailable {
		t.Fatalf("over stock = %q, %v; want unavailable, nil", source, err)
	}
	zero, err := domain.NewMilliInk(0)
	if err != nil {
		t.Fatalf("NewMilliInk(0): %v", err)
	}
	if _, err := domain.MonetaryGrantSource(true, stock, zero); !errors.Is(err, domain.ErrInvalidGrant) {
		t.Fatalf("zero request = %v, want ErrInvalidGrant", err)
	}
}
