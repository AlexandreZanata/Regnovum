package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

func testArenaID(t *testing.T) domain.ArenaID {
	t.Helper()
	arenaID, err := domain.ParseArenaID("018f6b2a-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	return arenaID
}

func testAccountID(t *testing.T) domain.AccountID {
	t.Helper()
	accountID, err := domain.ParseAccountID("018f6b2a-0000-7000-8000-000000000002")
	if err != nil {
		t.Fatalf("ParseAccountID: %v", err)
	}
	return accountID
}

func mustPosition(t *testing.T, raw string) domain.Position {
	t.Helper()
	position, err := domain.ParsePosition(raw)
	if err != nil {
		t.Fatalf("ParsePosition(%q): %v", raw, err)
	}
	return position
}

func TestNewPositionChangeValidatesTheHistoryEntry(t *testing.T) {
	arenaID := testArenaID(t)
	accountID := testAccountID(t)
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	change, err := domain.NewPositionChange(arenaID, accountID, mustPosition(t, domain.PositionAgree), mustPosition(t, domain.PositionDisagree), 2, at)
	if err != nil {
		t.Fatalf("NewPositionChange() error = %v", err)
	}
	if !change.ArenaID().Equals(arenaID) || !change.AccountID().Equals(accountID) {
		t.Fatal("change lost its identity")
	}
	if !change.From().Equals(mustPosition(t, domain.PositionAgree)) || !change.To().Equals(mustPosition(t, domain.PositionDisagree)) {
		t.Fatal("change lost its transition")
	}
	if change.Version() != 2 || !change.ChangedAt().Equal(at) {
		t.Fatal("change lost its version or instant")
	}

	probes := []struct {
		name      string
		arenaID   domain.ArenaID
		accountID domain.AccountID
		from      domain.Position
		to        domain.Position
		version   int32
		at        time.Time
		want      error
	}{
		{name: "empty arena", arenaID: domain.ArenaID{}, accountID: accountID, from: mustPosition(t, domain.PositionAgree), to: mustPosition(t, domain.PositionDisagree), version: 2, at: at, want: domain.ErrEmptyArenaID},
		{name: "empty account", arenaID: arenaID, accountID: domain.AccountID{}, from: mustPosition(t, domain.PositionAgree), to: mustPosition(t, domain.PositionDisagree), version: 2, at: at, want: domain.ErrEmptyAccountID},
		{name: "empty from", arenaID: arenaID, accountID: accountID, from: domain.Position{}, to: mustPosition(t, domain.PositionDisagree), version: 2, at: at, want: domain.ErrEmptyPosition},
		{name: "empty to", arenaID: arenaID, accountID: accountID, from: mustPosition(t, domain.PositionAgree), to: domain.Position{}, version: 2, at: at, want: domain.ErrEmptyPosition},
		{name: "same position", arenaID: arenaID, accountID: accountID, from: mustPosition(t, domain.PositionAgree), to: mustPosition(t, domain.PositionAgree), version: 2, at: at, want: domain.ErrSamePosition},
		{name: "version one", arenaID: arenaID, accountID: accountID, from: mustPosition(t, domain.PositionAgree), to: mustPosition(t, domain.PositionDisagree), version: 1, at: at, want: domain.ErrInvalidVersion},
		{name: "zero instant", arenaID: arenaID, accountID: accountID, from: mustPosition(t, domain.PositionAgree), to: mustPosition(t, domain.PositionDisagree), version: 2, want: domain.ErrInvalidInstant},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := domain.NewPositionChange(probe.arenaID, probe.accountID, probe.from, probe.to, probe.version, probe.at)
			if !errors.Is(err, probe.want) {
				t.Fatalf("error = %v, want %v", err, probe.want)
			}
		})
	}
}

func TestDeriveCurrentPositionReplaysContiguousChains(t *testing.T) {
	arenaID := testArenaID(t)
	accountID := testAccountID(t)
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	initial := mustPosition(t, domain.PositionAgree)

	// The empty chain is the initial position at version 1.
	current, version, err := domain.DeriveCurrentPosition(initial, nil)
	if err != nil {
		t.Fatalf("DeriveCurrentPosition(nil) error = %v", err)
	}
	if !current.Equals(initial) || version != 1 {
		t.Fatalf("empty chain = (%q, %d), want (%q, 1)", current.String(), version, initial.String())
	}

	// A valid chain: agree -> disagree (v2) -> agree (v3).
	first, err := domain.NewPositionChange(arenaID, accountID, initial, mustPosition(t, domain.PositionDisagree), 2, at)
	if err != nil {
		t.Fatalf("NewPositionChange(first): %v", err)
	}
	second, err := domain.NewPositionChange(arenaID, accountID, mustPosition(t, domain.PositionDisagree), mustPosition(t, domain.PositionAgree), 3, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("NewPositionChange(second): %v", err)
	}
	current, version, err = domain.DeriveCurrentPosition(initial, []domain.PositionChange{first, second})
	if err != nil {
		t.Fatalf("DeriveCurrentPosition(chain) error = %v", err)
	}
	if !current.Equals(mustPosition(t, domain.PositionAgree)) || version != 3 {
		t.Fatalf("derived = (%q, %d), want (agree, 3)", current.String(), version)
	}

	// Discontinuous chains are refused: a version gap and a wrong source.
	if _, _, err := domain.DeriveCurrentPosition(initial, []domain.PositionChange{second}); !errors.Is(err, domain.ErrBrokenChain) {
		t.Fatalf("version gap error = %v, want ErrBrokenChain", err)
	}
	discontinuous, err := domain.NewPositionChange(arenaID, accountID, mustPosition(t, domain.PositionUndecided), mustPosition(t, domain.PositionDisagree), 2, at)
	if err != nil {
		t.Fatalf("NewPositionChange(discontinuous): %v", err)
	}
	if _, _, err := domain.DeriveCurrentPosition(initial, []domain.PositionChange{discontinuous}); !errors.Is(err, domain.ErrBrokenChain) {
		t.Fatalf("wrong source error = %v, want ErrBrokenChain", err)
	}

	// Unsupported initial values are refused.
	if _, _, err := domain.DeriveCurrentPosition(domain.Position{}, nil); !errors.Is(err, domain.ErrEmptyPosition) {
		t.Fatalf("zero initial error = %v, want ErrEmptyPosition", err)
	}
}
