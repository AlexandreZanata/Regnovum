package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// VerifyRestoreCommand names the book under judgment and carries the
// pre-disaster fingerprint: the snapshot taken before the loss, which
// the restored cluster must reproduce exactly.
type VerifyRestoreCommand struct {
	Season   string
	Expected domain.BookFingerprint
}

// RestoreSnapshotter reads one book fingerprint from a cluster. The
// same port reads the pre-disaster source and the restored target, so
// both sides answer the same questions.
type RestoreSnapshotter interface {
	SnapshotBook(ctx context.Context, season domain.SeasonKey) (domain.BookFingerprint, error)
}

// RestoreResumeGuard blocks resumption on the restored cluster. A
// divergence without a persisted block is a failure, never a warning:
// the guard returns the marker that names the block.
type RestoreResumeGuard interface {
	BlockRestore(ctx context.Context, season domain.SeasonKey, lines []string) (string, error)
}

// RestoreVerificationOutcome is one judgment: the domain verdict
// plus the guard marker when resumption was blocked.
type RestoreVerificationOutcome struct {
	Verdict domain.RestoreVerdict
	BlockID string
}

// VerifyRestoreUseCase judges whether a restored cluster may resume:
// snapshot the target, compare against the pre-disaster fingerprint,
// and persist the block before returning when anything diverged. It
// is an internal operation: no public surface calls it, and the
// economy stays disabled while it watches test books.
type VerifyRestoreUseCase struct {
	snapshotter RestoreSnapshotter
	guard       RestoreResumeGuard
}

// NewVerifyRestoreUseCase creates an instance of VerifyRestoreUseCase.
func NewVerifyRestoreUseCase(snapshotter RestoreSnapshotter, guard RestoreResumeGuard) *VerifyRestoreUseCase {
	return &VerifyRestoreUseCase{snapshotter: snapshotter, guard: guard}
}

// Execute judges one restored book. Detection and its persisted block
// share the pass: divergences without a guard marker are refused
// instead of returned as a silent report.
func (uc *VerifyRestoreUseCase) Execute(ctx context.Context, cmd VerifyRestoreCommand) (*RestoreVerificationOutcome, error) {
	season, err := domain.ParseSeasonKey(cmd.Season)
	if err != nil {
		return nil, err
	}
	if cmd.Expected.Season.String() != "" && cmd.Expected.Season != season {
		return nil, domain.ErrInvalidRestoreSnapshot
	}
	observed, err := uc.snapshotter.SnapshotBook(ctx, season)
	if err != nil {
		return nil, err
	}
	verdict, err := domain.CompareBookRestore(cmd.Expected, observed)
	if err != nil {
		return nil, err
	}
	outcome := &RestoreVerificationOutcome{Verdict: verdict}
	if verdict.ResumeAllowed() {
		return outcome, nil
	}
	lines := make([]string, 0, len(verdict.Divergences))
	for _, divergence := range verdict.Divergences {
		lines = append(lines, divergence.Redacted(season))
	}
	block, err := uc.guard.BlockRestore(ctx, season, lines)
	if err != nil {
		return nil, err
	}
	if block == "" {
		return nil, domain.ErrIncidentNotFound
	}
	outcome.BlockID = block
	return outcome, nil
}
