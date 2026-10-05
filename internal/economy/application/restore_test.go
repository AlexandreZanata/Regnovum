package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubRestoreSnapshotter stands in for the PostgreSQL adapter where no
// database behavior is under test: the use case judges whatever the
// snapshotter returns, and failures close the pass.
type stubRestoreSnapshotter struct {
	snapshot domain.BookFingerprint
	err      error
}

func (s *stubRestoreSnapshotter) SnapshotBook(_ context.Context, _ domain.SeasonKey) (domain.BookFingerprint, error) {
	return s.snapshot, s.err
}

// stubRestoreGuard stands in for the restored-cluster freeze: it
// records the block lines, so the tests prove no divergence travels
// without its persisted block.
type stubRestoreGuard struct {
	lines   []string
	blockID string
	err     error
}

func (s *stubRestoreGuard) BlockRestore(_ context.Context, _ domain.SeasonKey, lines []string) (string, error) {
	s.lines = append(s.lines, lines...)
	return s.blockID, s.err
}

func restoreExpected() domain.BookFingerprint {
	return domain.BookFingerprint{
		Season:             domain.SeasonKey("S-2077-PITR"),
		SupplyMillis:       domain.GenesisSupplyMillis,
		Custodies:          map[string]int64{"treasury/main": domain.GenesisSupplyMillis},
		Legs:               1,
		Intentions:         0,
		ArchiveSealed:      false,
		ActiveReignHolder:  "bob",
		ActiveReignVersion: 2,
		HasActiveReign:     true,
		FormerHolders:      []string{"alice"},
	}
}

// TestVerifyRestoreResumeAllowed proves identical snapshots resume
// without touching the guard.
func TestVerifyRestoreResumeAllowed(t *testing.T) {
	t.Parallel()
	snapshotter := &stubRestoreSnapshotter{snapshot: restoreExpected()}
	guard := &stubRestoreGuard{blockID: "block-1"}
	useCase := application.NewVerifyRestoreUseCase(snapshotter, guard)
	outcome, err := useCase.Execute(context.Background(), application.VerifyRestoreCommand{
		Season:   "S-2077-PITR",
		Expected: restoreExpected(),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !outcome.Verdict.ResumeAllowed() || outcome.BlockID != "" {
		t.Fatalf("identical snapshots: %+v", outcome)
	}
	if len(guard.lines) != 0 {
		t.Fatal("resumed cluster was blocked")
	}
}

// TestVerifyRestoreDivergenceBlocks proves a drifted snapshot blocks
// resumption with a named guard marker before the use case returns.
func TestVerifyRestoreDivergenceBlocks(t *testing.T) {
	t.Parallel()
	drifted := restoreExpected()
	drifted.SupplyMillis++
	snapshotter := &stubRestoreSnapshotter{snapshot: drifted}
	guard := &stubRestoreGuard{blockID: "block-9"}
	useCase := application.NewVerifyRestoreUseCase(snapshotter, guard)
	outcome, err := useCase.Execute(context.Background(), application.VerifyRestoreCommand{
		Season:   "S-2077-PITR",
		Expected: restoreExpected(),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Verdict.ResumeAllowed() {
		t.Fatalf("drifted snapshot resumes: %+v", outcome.Verdict)
	}
	if outcome.BlockID != "block-9" || len(guard.lines) != 1 {
		t.Fatalf("block = %q lines = %d, want one named block", outcome.BlockID, len(guard.lines))
	}
}

// TestVerifyRestoreFailsClosed proves every silent path is an error:
// unreadable snapshots, unpersisted blocks and season mismatches never
// return a verdict, allowed or otherwise.
func TestVerifyRestoreFailsClosed(t *testing.T) {
	t.Parallel()
	useCase := application.NewVerifyRestoreUseCase(
		&stubRestoreSnapshotter{err: errors.New("cluster down")},
		&stubRestoreGuard{blockID: "block-1"},
	)
	if _, err := useCase.Execute(context.Background(), application.VerifyRestoreCommand{Season: "S-2077-PITR", Expected: restoreExpected()}); err == nil {
		t.Fatal("unreadable snapshot accepted")
	}

	drifted := restoreExpected()
	drifted.Legs++
	useCase = application.NewVerifyRestoreUseCase(
		&stubRestoreSnapshotter{snapshot: drifted},
		&stubRestoreGuard{blockID: ""},
	)
	if _, err := useCase.Execute(context.Background(), application.VerifyRestoreCommand{Season: "S-2077-PITR", Expected: restoreExpected()}); !errors.Is(err, domain.ErrIncidentNotFound) {
		t.Fatalf("unpersisted block = %v, want ErrIncidentNotFound", err)
	}

	useCase = application.NewVerifyRestoreUseCase(
		&stubRestoreSnapshotter{snapshot: drifted},
		&stubRestoreGuard{err: errors.New("disk full")},
	)
	if _, err := useCase.Execute(context.Background(), application.VerifyRestoreCommand{Season: "S-2077-PITR", Expected: restoreExpected()}); err == nil {
		t.Fatal("guard failure accepted")
	}

	useCase = application.NewVerifyRestoreUseCase(
		&stubRestoreSnapshotter{snapshot: restoreExpected()},
		&stubRestoreGuard{blockID: "block-1"},
	)
	if _, err := useCase.Execute(context.Background(), application.VerifyRestoreCommand{Season: "", Expected: restoreExpected()}); err == nil {
		t.Fatal("empty season accepted")
	}
	mismatched := restoreExpected()
	mismatched.Season = "S-2077-OTHER"
	if _, err := useCase.Execute(context.Background(), application.VerifyRestoreCommand{Season: "S-2077-PITR", Expected: mismatched}); !errors.Is(err, domain.ErrInvalidRestoreSnapshot) {
		t.Fatalf("season mismatch = %v, want ErrInvalidRestoreSnapshot", err)
	}
}
