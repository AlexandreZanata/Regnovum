package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// Seed rationale (P24-T02): the position vocabulary is closed
// (agree/disagree/undecided), so the corpus stresses the state machine
// instead — same-value moves (refused), invalid tokens, empty entries,
// overlong chains and hostile Unicode tokens. A panic, a lost initial
// position, a skipped version or a non-deterministic replay persists corpus
// by the native Go fuzz contract.

var fuzzPositionVocabulary = []string{
	domain.PositionAgree,
	domain.PositionDisagree,
	domain.PositionUndecided,
}

func fuzzPositionAt(seed int) domain.Position {
	position, err := domain.ParsePosition(fuzzPositionVocabulary[((seed%3)+3)%3])
	if err != nil {
		panic(err)
	}
	return position
}

// replayFuzzSequence applies one token list to a fresh projection and
// returns the tip. It is the independent replay the fuzz target compares
// against: the same commands must always land on the same state.
func replayFuzzSequence(t *testing.T, initial domain.Position, tokens []string, base time.Time) (domain.Position, int32) {
	t.Helper()
	arenaID, err := domain.ParseArenaID("arena-fuzz")
	if err != nil {
		t.Fatalf("ParseArenaID() failed: %v", err)
	}
	accountID, err := domain.ParseAccountID("account-fuzz")
	if err != nil {
		t.Fatalf("ParseAccountID() failed: %v", err)
	}
	projection, err := domain.ConfirmInitialPosition(arenaID, accountID, initial, base)
	if err != nil {
		t.Fatalf("ConfirmInitialPosition() failed: %v", err)
	}
	for index, token := range tokens {
		next, err := domain.ParsePosition(token)
		if err != nil {
			continue
		}
		at := base.Add(time.Duration(index+1) * time.Second)
		if _, err := projection.ChangeTo(next, at); err != nil {
			continue
		}
	}
	return projection.CurrentPosition(), projection.Version()
}

// FuzzDebatePositionTransitions proves the debate state machine never panics
// on arbitrary move lists and that every run respects the chain: the initial
// position is immutable, versions advance by exactly one per accepted move,
// same-value moves are refused without writing, invalid tokens never move
// state, and replaying the same commands lands on the same tip.
func FuzzDebatePositionTransitions(f *testing.F) {
	f.Add(0, "")
	f.Add(0, "agree")
	f.Add(1, "agree,disagree")
	f.Add(2, "agree,agree")
	f.Add(0, "agree,disagree,undecided,agree")
	f.Add(1, "agree,,disagree")
	f.Add(0, "agree, \u202e,disagree")
	f.Add(2, "AGREE,DISAGREE")
	f.Add(0, strings.Repeat("agree,", 200)+"disagree")

	f.Fuzz(func(t *testing.T, initialSeed int, spec string) {
		arenaID, err := domain.ParseArenaID("arena-fuzz")
		if err != nil {
			t.Fatalf("ParseArenaID() failed: %v", err)
		}
		accountID, err := domain.ParseAccountID("account-fuzz")
		if err != nil {
			t.Fatalf("ParseAccountID() failed: %v", err)
		}
		initial := fuzzPositionAt(initialSeed)
		base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
		projection, err := domain.ConfirmInitialPosition(arenaID, accountID, initial, base)
		if err != nil {
			t.Fatalf("ConfirmInitialPosition() failed: %v", err)
		}

		tokens := strings.Split(spec, ",")
		if len(tokens) > 128 {
			tokens = tokens[:128]
		}
		wantVersion := int32(1)
		for index, token := range tokens {
			before := projection.CurrentPosition()
			beforeVersion := projection.Version()
			at := base.Add(time.Duration(index+1) * time.Second)
			next, err := domain.ParsePosition(token)
			if err != nil {
				// Unparseable tokens must never move state: the invalid
				// path is proved on every run.
				if _, changeErr := projection.ChangeTo(domain.Position{}, at); changeErr == nil {
					t.Fatalf("zero position accepted for token %q", token)
				}
				if !projection.CurrentPosition().Equals(before) || projection.Version() != beforeVersion {
					t.Fatalf("refused token %q moved the state", token)
				}
				continue
			}
			if next.Equals(before) {
				// The same-value transition is unreachable by design.
				if _, err := projection.ChangeTo(next, at); err == nil {
					t.Fatalf("same-value move to %q accepted", next.String())
				}
				if !projection.CurrentPosition().Equals(before) || projection.Version() != beforeVersion {
					t.Fatalf("refused same-value move to %q moved the state", next.String())
				}
				continue
			}
			change, err := projection.ChangeTo(next, at)
			if err != nil {
				t.Fatalf("ChangeTo(%q) failed: %v", next.String(), err)
			}
			wantVersion++
			if projection.Version() != wantVersion {
				t.Fatalf("version = %d, want %d", projection.Version(), wantVersion)
			}
			if !projection.CurrentPosition().Equals(next) {
				t.Fatal("current position did not advance to the accepted move")
			}
			if !projection.InitialPosition().Equals(initial) {
				t.Fatal("accepted move rewrote the immutable initial position")
			}
			if !change.From().Equals(before) || !change.To().Equals(next) || change.Version() != wantVersion {
				t.Fatal("history entry does not describe the applied move")
			}
		}

		// Replay determinism: the same commands on a fresh projection land
		// on the same tip. A nondeterministic machine persists corpus.
		tip, version := replayFuzzSequence(t, initial, tokens, base)
		if !tip.Equals(projection.CurrentPosition()) || version != projection.Version() {
			t.Fatal("replaying the same commands landed on a different tip")
		}
	})
}
