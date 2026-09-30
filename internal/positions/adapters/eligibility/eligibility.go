// Package eligibility answers the participation gates of the positions module
// (P18-T07B): whether an account may confirm positions, and whether an Arena
// accepts them.
//
// The bridge lives in the consumer's adapters, the same place
// arguments/adapters/walletdebit occupies, because the ports belong to the
// module that consumes the answer: the positions application never imports
// identity or arenas, and this package is where the three vocabularies meet. It
// translates the provider answers into the consumer errors and decides nothing
// else — a lifecycle state it does not recognize is a refusal, never an
// approval, and an error it does not understand is passed through instead of
// being flattened into one of the module's own.
package eligibility

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// AccountReader is the narrow identity port this gate asks: the stored account
// of one identifier. The composition passes the identity repository, and a test
// passes a double, because the gate needs one question answered and nothing
// else from the module that owns the answer.
type AccountReader interface {
	GetAccountByID(ctx context.Context, accountID identitydomain.AccountID) (*identitydomain.Account, error)
}

// ArenaReader is the narrow arenas port this gate asks: the stored Arena of one
// identifier.
type ArenaReader interface {
	GetArenaByID(ctx context.Context, arenaID arenasdomain.ArenaID) (*arenasdomain.Arena, error)
}

// Gate answers both eligibility ports of the positions module.
type Gate struct {
	accounts AccountReader
	arenas   ArenaReader
}

// New composes the gate. It fails closed on a missing dependency: a gate that
// cannot read the account would answer "eligible" for every caller, which is
// exactly the failure the port exists to prevent.
func New(accounts AccountReader, arenas ArenaReader) (*Gate, error) {
	missing := make([]string, 0, 2)
	if accounts == nil {
		missing = append(missing, "account reader")
	}
	if arenas == nil {
		missing = append(missing, "arena reader")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("positions eligibility: missing %s", strings.Join(missing, ", "))
	}
	return &Gate{accounts: accounts, arenas: arenas}, nil
}

// EnsureEligible answers whether the account may confirm positions.
//
// Only an active account participates: a pending account has not confirmed its
// address and a deleted one is gone, and both answer the same refusal, because
// the positions vocabulary distinguishes absence, suspension and ineligibility
// and nothing else.
func (gate *Gate) EnsureEligible(ctx context.Context, accountID positionsdomain.AccountID) error {
	account, err := gate.accounts.GetAccountByID(ctx, identitydomain.AccountID(accountID.String()))
	if err != nil {
		if errors.Is(err, identityapp.ErrAccountNotFound) {
			return positionsapp.ErrAccountNotFound
		}
		return err
	}

	switch account.Status() {
	case identitydomain.AccountStatusActive:
		return nil
	case identitydomain.AccountStatusSuspended:
		return positionsapp.ErrAccountSuspended
	default:
		return positionsapp.ErrAccountNotEligible
	}
}

// EnsureAcceptsPositions answers whether the Arena accepts new positions. The
// lifecycle rule belongs to the arenas domain, so the gate asks it instead of
// re-reading the status here.
func (gate *Gate) EnsureAcceptsPositions(ctx context.Context, arenaID positionsdomain.ArenaID) error {
	arena, err := gate.arenas.GetArenaByID(ctx, arenasdomain.ArenaID(arenaID.String()))
	if err != nil {
		if errors.Is(err, arenasapp.ErrArenaNotFound) {
			return positionsapp.ErrArenaNotFound
		}
		return err
	}

	if err := arena.EnsureAcceptsParticipation(); err != nil {
		if errors.Is(err, arenasdomain.ErrArenaNotOpen) {
			return positionsapp.ErrArenaNotOpen
		}
		return err
	}
	return nil
}
