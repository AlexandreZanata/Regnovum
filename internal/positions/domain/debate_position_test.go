package domain_test

import (
	"errors"
	"testing"
	"testing/quick"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

func TestConfirmInitialPositionStartsTheProjection(t *testing.T) {
	arenaID := testArenaID(t)
	accountID := testAccountID(t)
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	initial := mustPosition(t, domain.PositionUndecided)

	position, err := domain.ConfirmInitialPosition(arenaID, accountID, initial, at)
	if err != nil {
		t.Fatalf("ConfirmInitialPosition() error = %v", err)
	}
	if !position.InitialPosition().Equals(initial) || !position.CurrentPosition().Equals(initial) {
		t.Fatal("confirmation must start current equal to initial")
	}
	if position.Version() != 1 {
		t.Fatalf("version = %d, want 1", position.Version())
	}
	if !position.CreatedAt().Equal(at) || !position.UpdatedAt().Equal(at) {
		t.Fatal("confirmation must stamp both instants")
	}

	probes := []struct {
		name      string
		arenaID   domain.ArenaID
		accountID domain.AccountID
		position  domain.Position
		at        time.Time
		want      error
	}{
		{name: "empty arena", arenaID: domain.ArenaID{}, accountID: accountID, position: initial, at: at, want: domain.ErrEmptyArenaID},
		{name: "empty account", arenaID: arenaID, accountID: domain.AccountID{}, position: initial, at: at, want: domain.ErrEmptyAccountID},
		{name: "empty position", arenaID: arenaID, accountID: accountID, position: domain.Position{}, at: at, want: domain.ErrEmptyPosition},
		{name: "zero instant", arenaID: arenaID, accountID: accountID, position: initial, want: domain.ErrInvalidInstant},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			if _, err := domain.ConfirmInitialPosition(probe.arenaID, probe.accountID, probe.position, probe.at); !errors.Is(err, probe.want) {
				t.Fatalf("error = %v, want %v", err, probe.want)
			}
		})
	}
}

func TestReconstituteDebatePositionValidatesStoredState(t *testing.T) {
	arenaID := testArenaID(t)
	accountID := testAccountID(t)
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	agree := mustPosition(t, domain.PositionAgree)
	disagree := mustPosition(t, domain.PositionDisagree)

	position, err := domain.ReconstituteDebatePosition(arenaID, accountID, agree, disagree, 2, at, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("ReconstituteDebatePosition() error = %v", err)
	}
	if !position.InitialPosition().Equals(agree) || !position.CurrentPosition().Equals(disagree) {
		t.Fatal("reconstitution lost the stored positions")
	}

	probes := []struct {
		name      string
		initial   domain.Position
		current   domain.Position
		version   int32
		createdAt time.Time
		updatedAt time.Time
		want      error
	}{
		{name: "zero version", initial: agree, current: agree, version: 0, createdAt: at, updatedAt: at, want: domain.ErrInvalidVersion},
		{name: "zero created", initial: agree, current: agree, version: 1, updatedAt: at, want: domain.ErrInvalidInstant},
		{name: "zero updated", initial: agree, current: agree, version: 1, createdAt: at, want: domain.ErrInvalidInstant},
		{name: "updated before created", initial: agree, current: disagree, version: 2, createdAt: at, updatedAt: at.Add(-time.Minute), want: domain.ErrInvalidInstant},
		{name: "version one diverging from initial", initial: agree, current: disagree, version: 1, createdAt: at, updatedAt: at, want: domain.ErrBrokenChain},
		{name: "empty initial", initial: domain.Position{}, current: agree, version: 1, createdAt: at, updatedAt: at, want: domain.ErrEmptyPosition},
		{name: "empty current", initial: agree, current: domain.Position{}, version: 2, createdAt: at, updatedAt: at, want: domain.ErrEmptyPosition},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := domain.ReconstituteDebatePosition(arenaID, accountID, probe.initial, probe.current, probe.version, probe.createdAt, probe.updatedAt)
			if !errors.Is(err, probe.want) {
				t.Fatalf("error = %v, want %v", err, probe.want)
			}
		})
	}
}

func TestChangeToAppliesAcceptedTransitionsOnly(t *testing.T) {
	arenaID := testArenaID(t)
	accountID := testAccountID(t)
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	position, err := domain.ConfirmInitialPosition(arenaID, accountID, mustPosition(t, domain.PositionAgree), at)
	if err != nil {
		t.Fatalf("ConfirmInitialPosition: %v", err)
	}

	change, err := position.ChangeTo(mustPosition(t, domain.PositionDisagree), at.Add(time.Minute))
	if err != nil {
		t.Fatalf("ChangeTo() error = %v", err)
	}
	if !change.From().Equals(mustPosition(t, domain.PositionAgree)) || !change.To().Equals(mustPosition(t, domain.PositionDisagree)) {
		t.Fatal("change must record from and to")
	}
	if change.Version() != 2 || position.Version() != 2 {
		t.Fatal("accepted change must advance the projection by one version")
	}
	if !position.CurrentPosition().Equals(mustPosition(t, domain.PositionDisagree)) {
		t.Fatal("accepted change must move the current position")
	}
	if !position.InitialPosition().Equals(mustPosition(t, domain.PositionAgree)) {
		t.Fatal("accepted change must never touch the initial position")
	}
	if !position.UpdatedAt().Equal(at.Add(time.Minute)) {
		t.Fatal("accepted change must stamp updated_at")
	}

	// Same position: rejected without any mutation.
	if _, err := position.ChangeTo(mustPosition(t, domain.PositionDisagree), at.Add(2*time.Minute)); !errors.Is(err, domain.ErrSamePosition) {
		t.Fatalf("same position error = %v, want ErrSamePosition", err)
	}
	if position.Version() != 2 || !position.CurrentPosition().Equals(mustPosition(t, domain.PositionDisagree)) || !position.UpdatedAt().Equal(at.Add(time.Minute)) {
		t.Fatal("rejected change mutated the projection")
	}

	// Empty/unsupported targets and backwards instants are refused.
	if _, err := position.ChangeTo(domain.Position{}, at.Add(3*time.Minute)); !errors.Is(err, domain.ErrEmptyPosition) {
		t.Fatalf("empty target error = %v, want ErrEmptyPosition", err)
	}
	if _, err := position.ChangeTo(mustPosition(t, domain.PositionAgree), time.Time{}); !errors.Is(err, domain.ErrInvalidInstant) {
		t.Fatalf("zero instant error = %v, want ErrInvalidInstant", err)
	}
	if _, err := position.ChangeTo(mustPosition(t, domain.PositionAgree), at); !errors.Is(err, domain.ErrInvalidInstant) {
		t.Fatalf("backwards instant error = %v, want ErrInvalidInstant", err)
	}
	if position.Version() != 2 {
		t.Fatal("refused transitions must not advance the version")
	}
}

// TestChangeSequencesStayDerivableAndImmutable is the P09-T02 property test:
// for any sequence of targets, accepted changes form a contiguous chain from
// the initial position, rejected same-position targets change nothing, and
// the initial position is immutable.
func TestChangeSequencesStayDerivableAndImmutable(t *testing.T) {
	arenaID := testArenaID(t)
	accountID := testAccountID(t)
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	options := []string{domain.PositionAgree, domain.PositionDisagree, domain.PositionUndecided}

	property := func(kinds []uint8) bool {
		initial := mustPosition(t, domain.PositionAgree)
		position, err := domain.ConfirmInitialPosition(arenaID, accountID, initial, base)
		if err != nil {
			return false
		}

		changes := make([]domain.PositionChange, 0, len(kinds))
		for index, kind := range kinds {
			next := mustPosition(t, options[int(kind)%len(options)])
			at := base.Add(time.Duration(index+1) * time.Minute)
			previousCurrent := position.CurrentPosition()
			previousVersion := position.Version()

			change, err := position.ChangeTo(next, at)
			switch {
			case errors.Is(err, domain.ErrSamePosition):
				if !position.CurrentPosition().Equals(previousCurrent) || position.Version() != previousVersion {
					return false
				}
			case err != nil:
				return false
			default:
				if !change.From().Equals(previousCurrent) || !change.To().Equals(next) {
					return false
				}
				if change.Version() != previousVersion+1 || position.Version() != previousVersion+1 {
					return false
				}
				changes = append(changes, change)
			}

			if !position.InitialPosition().Equals(initial) {
				return false
			}
		}

		derived, version, err := domain.DeriveCurrentPosition(initial, changes)
		if err != nil {
			return false
		}
		return derived.Equals(position.CurrentPosition()) &&
			version == position.Version() &&
			version == int32(1+len(changes))
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatalf("sequence property failed: %v", err)
	}
}
