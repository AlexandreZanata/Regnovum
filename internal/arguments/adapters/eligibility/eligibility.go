// Package eligibility answers the participation gates of the arguments module
// (P18-T07B): whether an account may publish, and whether an Arena accepts new
// arguments.
//
// It is the arguments counterpart of positions/adapters/eligibility, and it
// exists separately for the reason the ports do: each module owns the
// vocabulary of its own refusals, so the same identity answer becomes
// "arguments: account may not publish" here and "positions: account may not
// participate" there. The gate translates and decides nothing: a state it does
// not recognize is a refusal, never an approval.
package eligibility

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// AccountReader is the narrow identity port this gate asks: the stored account
// of one identifier.
type AccountReader interface {
	GetAccountByID(ctx context.Context, accountID identitydomain.AccountID) (*identitydomain.Account, error)
}

// ArenaReader is the narrow arenas port this gate asks: the stored Arena of one
// identifier.
type ArenaReader interface {
	GetArenaByID(ctx context.Context, arenaID arenasdomain.ArenaID) (*arenasdomain.Arena, error)
}

// Gate answers both eligibility ports of the arguments module.
type Gate struct {
	accounts AccountReader
	arenas   ArenaReader
}

// New composes the gate, failing closed on a missing dependency: a gate without
// the account reader would approve every caller.
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
		return nil, fmt.Errorf("arguments eligibility: missing %s", strings.Join(missing, ", "))
	}
	return &Gate{accounts: accounts, arenas: arenas}, nil
}

// EnsureEligible answers whether the account may publish arguments.
func (gate *Gate) EnsureEligible(ctx context.Context, accountID argumentsdomain.AccountID) error {
	account, err := gate.accounts.GetAccountByID(ctx, identitydomain.AccountID(accountID.String()))
	if err != nil {
		if errors.Is(err, identityapp.ErrAccountNotFound) {
			return argumentsapp.ErrAccountNotFound
		}
		return err
	}

	switch account.Status() {
	case identitydomain.AccountStatusActive:
		return nil
	case identitydomain.AccountStatusSuspended:
		return argumentsapp.ErrAccountSuspended
	default:
		return argumentsapp.ErrAccountNotEligible
	}
}

// EnsureAcceptsArguments answers whether the Arena accepts new arguments. The
// lifecycle rule is the arenas domain's, so the gate asks it.
func (gate *Gate) EnsureAcceptsArguments(ctx context.Context, arenaID argumentsdomain.ArenaID) error {
	arena, err := gate.arenas.GetArenaByID(ctx, arenasdomain.ArenaID(arenaID.String()))
	if err != nil {
		if errors.Is(err, arenasapp.ErrArenaNotFound) {
			return argumentsapp.ErrArenaNotFound
		}
		return err
	}

	if err := arena.EnsureAcceptsParticipation(); err != nil {
		if errors.Is(err, arenasdomain.ErrArenaNotOpen) {
			return argumentsapp.ErrArenaNotOpen
		}
		return err
	}
	return nil
}
