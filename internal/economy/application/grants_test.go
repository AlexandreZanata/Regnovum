package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubGrantPolicy answers funding facts from canned values: the guard
// logic under test never touches a database through it.
type stubGrantPolicy struct {
	genesis bool
	stock   domain.MilliInk
	err     error
}

func (s *stubGrantPolicy) GenesisHappened(_ context.Context, _ domain.SeasonKey) (bool, error) {
	return s.genesis, s.err
}

func (s *stubGrantPolicy) TreasuryStock(_ context.Context, _ domain.SeasonKey) (domain.MilliInk, error) {
	return s.stock, s.err
}

func TestGrantGuardUseCaseDecides(t *testing.T) {
	t.Parallel()

	stock, err := domain.NewMilliInk(1000)
	if err != nil {
		t.Fatalf("NewMilliInk(1000): %v", err)
	}
	tests := []struct {
		name   string
		policy stubGrantPolicy
		millis int64
		want   domain.GrantSource
		err    error
	}{
		{"legacy before genesis", stubGrantPolicy{}, 400, domain.GrantSourceLegacy, nil},
		{"treasury when funded", stubGrantPolicy{genesis: true, stock: stock}, 400, domain.GrantSourceTreasury, nil},
		{"unavailable on stockout", stubGrantPolicy{genesis: true, stock: stock}, 1001, domain.GrantSourceUnavailable, nil},
		{"negative amount", stubGrantPolicy{}, -5, "", domain.ErrNegativeMilliInk},
		{"zero amount", stubGrantPolicy{}, 0, "", domain.ErrInvalidGrant},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			policy := test.policy
			useCase := application.NewGrantGuardUseCase(&policy)
			got, err := useCase.Execute(context.Background(), application.GrantCommand{Millis: test.millis})
			if !errors.Is(err, test.err) {
				t.Fatalf("Execute = %v, want %v", err, test.err)
			}
			if err == nil && got != test.want {
				t.Fatalf("Execute = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGrantGuardPropagatesPolicyFailure(t *testing.T) {
	t.Parallel()

	probe := errors.New("probe")
	policy := &stubGrantPolicy{genesis: true, err: probe}
	useCase := application.NewGrantGuardUseCase(policy)
	if _, err := useCase.Execute(context.Background(), application.GrantCommand{Millis: 10}); !errors.Is(err, probe) {
		t.Fatalf("Execute = %v, want the policy failure", err)
	}
}
