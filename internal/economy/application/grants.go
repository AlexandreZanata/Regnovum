package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// GrantCommand asks where one new monetary grant of Millis may come
// from. Passes and contracted subscription benefits are not monetary
// grants: they keep their own contracted behavior, decided elsewhere.
// Season names the book that funds: empty binds the explicitly
// inactive compat-legacy namespace for the legacy path.
type GrantCommand struct {
	Millis int64
	Season string
}

// GrantPolicyRepository reads the funding facts a grant decision needs:
// whether Genesis happened and what Treasury stock holds in one book.
// Both reads are side-effect free. Member never mints S: after Genesis
// every new monetary unit leaves Treasury stock of the current book,
// or the grant does not happen.
type GrantPolicyRepository interface {
	// GenesisHappened reports whether the creation event is attested in one book.
	GenesisHappened(ctx context.Context, season domain.SeasonKey) (bool, error)
	// TreasuryStock returns the current Treasury journal balance of one book.
	TreasuryStock(ctx context.Context, season domain.SeasonKey) (domain.MilliInk, error)
}

// GrantGuardUseCase answers one grant funding decision without moving
// value and without touching legacy books. It is an internal operation:
// no public surface calls it.
type GrantGuardUseCase struct {
	policy GrantPolicyRepository
}

// NewGrantGuardUseCase creates an instance of GrantGuardUseCase.
func NewGrantGuardUseCase(policy GrantPolicyRepository) *GrantGuardUseCase {
	return &GrantGuardUseCase{policy: policy}
}

// Execute decides where the requested grant may come from.
func (uc *GrantGuardUseCase) Execute(ctx context.Context, cmd GrantCommand) (domain.GrantSource, error) {
	requested, err := domain.NewMilliInk(cmd.Millis)
	if err != nil {
		return "", err
	}
	season := domain.SeasonKey(domain.CompatSeasonKey)
	if cmd.Season != "" {
		season, err = domain.ParseSeasonKey(cmd.Season)
		if err != nil {
			return "", err
		}
	}
	genesis, err := uc.policy.GenesisHappened(ctx, season)
	if err != nil {
		return "", err
	}
	if !genesis {
		// Stock is unread and unneeded here: legacy books grant by
		// their own contracts while Genesis never happened.
		return domain.MonetaryGrantSource(false, requested, requested)
	}
	stock, err := uc.policy.TreasuryStock(ctx, season)
	if err != nil {
		return "", err
	}
	return domain.MonetaryGrantSource(true, stock, requested)
}
