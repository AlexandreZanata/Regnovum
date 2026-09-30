package eligibility_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/eligibility"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

const (
	accountFixture = "00000000-0000-0000-0000-0000000000b1"
	arenaFixture   = "00000000-0000-0000-0000-0000000000b2"
)

// accountReader answers with one stored account or one error, which is all the
// gate asks of the identity module.
type accountReader struct {
	account *identitydomain.Account
	err     error
}

func (reader accountReader) GetAccountByID(_ context.Context, accountID identitydomain.AccountID) (*identitydomain.Account, error) {
	if reader.err != nil {
		return nil, reader.err
	}
	if reader.account == nil || reader.account.ID() != accountID {
		return nil, identityapp.ErrAccountNotFound
	}
	return reader.account, nil
}

// arenaReader answers with one stored Arena or one error.
type arenaReader struct {
	arena *arenasdomain.Arena
	err   error
}

func (reader arenaReader) GetArenaByID(_ context.Context, arenaID arenasdomain.ArenaID) (*arenasdomain.Arena, error) {
	if reader.err != nil {
		return nil, reader.err
	}
	if reader.arena == nil || reader.arena.ID() != arenaID {
		return nil, arenasapp.ErrArenaNotFound
	}
	return reader.arena, nil
}

// arena builds a stored Arena in one status.
func arena(t *testing.T, status arenasdomain.ArenaStatus) *arenasdomain.Arena {
	t.Helper()

	statement, err := arenasdomain.ParseStatement("O debate público melhora com argumentos verificáveis.", arenasdomain.DefaultStatementPolicy())
	if err != nil {
		t.Fatalf("ParseStatement() error = %v", err)
	}
	category, err := arenasdomain.ParseCategory("tecnologia")
	if err != nil {
		t.Fatalf("ParseCategory() error = %v", err)
	}
	language, err := arenasdomain.ParseLanguage("pt-BR")
	if err != nil {
		t.Fatalf("ParseLanguage() error = %v", err)
	}

	// A draft is stored without a slug and without a publication instant;
	// every other status carries both.
	slug := arenasdomain.Slug{}
	published := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	var publishedAt *time.Time
	if status != arenasdomain.ArenaStatusDraft {
		parsed, err := arenasdomain.ParseSlug("debate-publico")
		if err != nil {
			t.Fatalf("ParseSlug() error = %v", err)
		}
		slug = parsed
		publishedAt = &published
	}

	built, err := arenasdomain.ReconstituteArena(
		arenasdomain.ArenaID(arenaFixture),
		arenasdomain.CreatorID("00000000-0000-0000-0000-0000000000b3"),
		statement,
		arenasdomain.Context{},
		category,
		language,
		status,
		slug,
		1,
		published,
		publishedAt,
		nil,
	)
	if err != nil {
		t.Fatalf("ReconstituteArena() error = %v", err)
	}
	return built
}

// account builds a stored account in one status.
func account(t *testing.T, status identitydomain.AccountStatus) *identitydomain.Account {
	t.Helper()

	email, err := identitydomain.ParseEmail("publisher@example.test")
	if err != nil {
		t.Fatalf("ParseEmail() error = %v", err)
	}
	created := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	var verifiedAt *time.Time
	if status == identitydomain.AccountStatusActive {
		verifiedAt = &created
	}

	built, err := identitydomain.ReconstituteAccount(identitydomain.AccountID(accountFixture), email, status, verifiedAt, created, created)
	if err != nil {
		t.Fatalf("ReconstituteAccount() error = %v", err)
	}
	return built
}

func gateFor(t *testing.T, accounts eligibility.AccountReader, arenas eligibility.ArenaReader) *eligibility.Gate {
	t.Helper()

	gate, err := eligibility.New(accounts, arenas)
	if err != nil {
		t.Fatalf("eligibility.New() error = %v", err)
	}
	return gate
}

// TestEnsureEligibleTranslatesTheAccountLifecycle is the contract of the
// account gate in the arguments vocabulary: the refusals are this module's own
// errors, and an unrecognized state is a refusal rather than an approval.
func TestEnsureEligibleTranslatesTheAccountLifecycle(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  identitydomain.AccountStatus
		want    error
		wantNil bool
	}{
		{name: "active publishes", status: identitydomain.AccountStatusActive, wantNil: true},
		{name: "pending does not", status: identitydomain.AccountStatusPending, want: argumentsapp.ErrAccountNotEligible},
		{name: "deleted does not", status: identitydomain.AccountStatusDeleted, want: argumentsapp.ErrAccountNotEligible},
		{name: "suspended is named", status: identitydomain.AccountStatusSuspended, want: argumentsapp.ErrAccountSuspended},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			gate := gateFor(t, accountReader{account: account(t, testCase.status)}, arenaReader{arena: arena(t, arenasdomain.ArenaStatusPublished)})
			id, err := argumentsdomain.ParseAccountID(accountFixture)
			if err != nil {
				t.Fatalf("ParseAccountID() error = %v", err)
			}

			err = gate.EnsureEligible(context.Background(), id)
			if testCase.wantNil {
				if err != nil {
					t.Fatalf("EnsureEligible(active) error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, testCase.want) {
				t.Errorf("EnsureEligible(%s) error = %v, want %v", testCase.status, err, testCase.want)
			}
		})
	}
}

func TestEnsureEligibleReportsAnUnknownAccount(t *testing.T) {
	t.Parallel()

	gate := gateFor(t, accountReader{}, arenaReader{arena: arena(t, arenasdomain.ArenaStatusPublished)})
	id, err := argumentsdomain.ParseAccountID(accountFixture)
	if err != nil {
		t.Fatalf("ParseAccountID() error = %v", err)
	}

	if err := gate.EnsureEligible(context.Background(), id); !errors.Is(err, argumentsapp.ErrAccountNotFound) {
		t.Errorf("EnsureEligible(unknown) error = %v, want ErrAccountNotFound", err)
	}
}

// TestEnsureEligiblePassesThroughAnUnrecognizedFailure is the fail-open guard:
// a storage outage is not an eligibility answer and must not be flattened into
// one of the module's refusals.
func TestEnsureEligiblePassesThroughAnUnrecognizedFailure(t *testing.T) {
	t.Parallel()

	outage := errors.New("connection refused")
	gate := gateFor(t, accountReader{err: outage}, arenaReader{arena: arena(t, arenasdomain.ArenaStatusPublished)})
	id, err := argumentsdomain.ParseAccountID(accountFixture)
	if err != nil {
		t.Fatalf("ParseAccountID() error = %v", err)
	}

	if err := gate.EnsureEligible(context.Background(), id); !errors.Is(err, outage) {
		t.Errorf("EnsureEligible() error = %v, want the storage failure to survive", err)
	}
}

// TestEnsureAcceptsArgumentsAsksTheArenaDomain covers the second gate: only a
// published Arena accepts arguments.
func TestEnsureAcceptsArgumentsAsksTheArenaDomain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  arenasdomain.ArenaStatus
		want    error
		wantNil bool
	}{
		{name: "published accepts", status: arenasdomain.ArenaStatusPublished, wantNil: true},
		{name: "draft does not", status: arenasdomain.ArenaStatusDraft, want: argumentsapp.ErrArenaNotOpen},
		{name: "closed does not", status: arenasdomain.ArenaStatusClosed, want: argumentsapp.ErrArenaNotOpen},
		{name: "restricted does not", status: arenasdomain.ArenaStatusRestricted, want: argumentsapp.ErrArenaNotOpen},
		{name: "removed does not", status: arenasdomain.ArenaStatusRemoved, want: argumentsapp.ErrArenaNotOpen},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			gate := gateFor(t, accountReader{account: account(t, identitydomain.AccountStatusActive)}, arenaReader{arena: arena(t, testCase.status)})
			id, err := argumentsdomain.ParseArenaID(arenaFixture)
			if err != nil {
				t.Fatalf("ParseArenaID() error = %v", err)
			}

			err = gate.EnsureAcceptsArguments(context.Background(), id)
			if testCase.wantNil {
				if err != nil {
					t.Fatalf("EnsureAcceptsArguments(%s) error = %v, want nil", testCase.status, err)
				}
				return
			}
			if !errors.Is(err, testCase.want) {
				t.Errorf("EnsureAcceptsArguments(%s) error = %v, want %v", testCase.status, err, testCase.want)
			}
		})
	}
}

func TestEnsureAcceptsArgumentsReportsAnUnknownArena(t *testing.T) {
	t.Parallel()

	gate := gateFor(t, accountReader{account: account(t, identitydomain.AccountStatusActive)}, arenaReader{})
	id, err := argumentsdomain.ParseArenaID(arenaFixture)
	if err != nil {
		t.Fatalf("ParseArenaID() error = %v", err)
	}

	if err := gate.EnsureAcceptsArguments(context.Background(), id); !errors.Is(err, argumentsapp.ErrArenaNotFound) {
		t.Errorf("EnsureAcceptsArguments(unknown) error = %v, want ErrArenaNotFound", err)
	}
}

// TestNewFailsClosed proves the composition guard: a gate that cannot read the
// account would approve every caller.
func TestNewFailsClosed(t *testing.T) {
	t.Parallel()

	reader := accountReader{account: account(t, identitydomain.AccountStatusActive)}
	arenas := arenaReader{arena: arena(t, arenasdomain.ArenaStatusPublished)}

	if _, err := eligibility.New(nil, arenas); err == nil || !strings.Contains(err.Error(), "account reader") {
		t.Errorf("New(nil, arenas) error = %v, want a refusal naming the account reader", err)
	}
	if _, err := eligibility.New(reader, nil); err == nil || !strings.Contains(err.Error(), "arena reader") {
		t.Errorf("New(accounts, nil) error = %v, want a refusal naming the arena reader", err)
	}
	if gate, err := eligibility.New(reader, arenas); err != nil || gate == nil {
		t.Errorf("New(accounts, arenas) = (%v, %v), want a composed gate", gate, err)
	}
}
