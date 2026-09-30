package eligibility_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/positions/adapters/eligibility"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

const (
	accountFixture = "00000000-0000-0000-0000-0000000000a1"
	arenaFixture   = "00000000-0000-0000-0000-0000000000a2"
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
		arenasdomain.CreatorID("00000000-0000-0000-0000-0000000000a3"),
		statement,
		arenasdomain.Context{},
		category,
		language,
		status,
		slug,
		2,
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

	email, err := identitydomain.ParseEmail("participation@example.test")
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

// TestEnsureEligibleTranslatesTheAccountLifecycle is the whole contract of the
// account gate: one approval, and every other state mapped to the refusal the
// positions vocabulary names.
func TestEnsureEligibleTranslatesTheAccountLifecycle(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  identitydomain.AccountStatus
		want    error
		wantNil bool
	}{
		{name: "active participates", status: identitydomain.AccountStatusActive, wantNil: true},
		{name: "pending does not", status: identitydomain.AccountStatusPending, want: positionsapp.ErrAccountNotEligible},
		{name: "deleted does not", status: identitydomain.AccountStatusDeleted, want: positionsapp.ErrAccountNotEligible},
		{name: "suspended is named", status: identitydomain.AccountStatusSuspended, want: positionsapp.ErrAccountSuspended},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			gate := gateFor(t, accountReader{account: account(t, testCase.status)}, arenaReader{arena: arena(t, arenasdomain.ArenaStatusPublished)})
			id, err := positionsdomain.ParseAccountID(accountFixture)
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
	id, err := positionsdomain.ParseAccountID(accountFixture)
	if err != nil {
		t.Fatalf("ParseAccountID() error = %v", err)
	}

	if err := gate.EnsureEligible(context.Background(), id); !errors.Is(err, positionsapp.ErrAccountNotFound) {
		t.Errorf("EnsureEligible(unknown) error = %v, want ErrAccountNotFound", err)
	}
}

// TestEnsureEligiblePassesThroughAnUnrecognizedFailure is the fail-open guard:
// a repository outage is not an eligibility answer, so it must not be flattened
// into one of the module's refusals.
func TestEnsureEligiblePassesThroughAnUnrecognizedFailure(t *testing.T) {
	t.Parallel()

	outage := errors.New("connection refused")
	gate := gateFor(t, accountReader{err: outage}, arenaReader{arena: arena(t, arenasdomain.ArenaStatusPublished)})
	id, err := positionsdomain.ParseAccountID(accountFixture)
	if err != nil {
		t.Fatalf("ParseAccountID() error = %v", err)
	}

	err = gate.EnsureEligible(context.Background(), id)
	if !errors.Is(err, outage) {
		t.Errorf("EnsureEligible() error = %v, want the storage failure to survive", err)
	}
}

// TestEnsureAcceptsPositionsAsksTheArenaDomain covers the second gate: only a
// published Arena accepts positions, and the refused states are one refusal.
func TestEnsureAcceptsPositionsAsksTheArenaDomain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  arenasdomain.ArenaStatus
		want    error
		wantNil bool
	}{
		{name: "published accepts", status: arenasdomain.ArenaStatusPublished, wantNil: true},
		{name: "draft does not", status: arenasdomain.ArenaStatusDraft, want: positionsapp.ErrArenaNotOpen},
		{name: "closed does not", status: arenasdomain.ArenaStatusClosed, want: positionsapp.ErrArenaNotOpen},
		{name: "restricted does not", status: arenasdomain.ArenaStatusRestricted, want: positionsapp.ErrArenaNotOpen},
		{name: "removed does not", status: arenasdomain.ArenaStatusRemoved, want: positionsapp.ErrArenaNotOpen},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			gate := gateFor(t, accountReader{account: account(t, identitydomain.AccountStatusActive)}, arenaReader{arena: arena(t, testCase.status)})
			id, err := positionsdomain.ParseArenaID(arenaFixture)
			if err != nil {
				t.Fatalf("ParseArenaID() error = %v", err)
			}

			err = gate.EnsureAcceptsPositions(context.Background(), id)
			if testCase.wantNil {
				if err != nil {
					t.Fatalf("EnsureAcceptsPositions(%s) error = %v, want nil", testCase.status, err)
				}
				return
			}
			if !errors.Is(err, testCase.want) {
				t.Errorf("EnsureAcceptsPositions(%s) error = %v, want %v", testCase.status, err, testCase.want)
			}
		})
	}
}

func TestEnsureAcceptsPositionsReportsAnUnknownArena(t *testing.T) {
	t.Parallel()

	gate := gateFor(t, accountReader{account: account(t, identitydomain.AccountStatusActive)}, arenaReader{})
	id, err := positionsdomain.ParseArenaID(arenaFixture)
	if err != nil {
		t.Fatalf("ParseArenaID() error = %v", err)
	}

	if err := gate.EnsureAcceptsPositions(context.Background(), id); !errors.Is(err, positionsapp.ErrArenaNotFound) {
		t.Errorf("EnsureAcceptsPositions(unknown) error = %v, want ErrArenaNotFound", err)
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
