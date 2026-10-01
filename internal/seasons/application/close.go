package application

import (
	"context"
	"fmt"
	"time"

	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

// ReleaseHoldFunc releases one economy hold back to its owner under
// a fenced drain token. It is a function port so the seasons module
// never wires another module's adapter: tests inject a closure over
// the real repository, and production wiring stays out until the
// release gate. It reports whether the call released now: an already
// settled hold replays as settled without failing, so crash resume
// never double-pays.
type ReleaseHoldFunc func(ctx context.Context, holdID string, drain seasondomain.CloseDrain) (released bool, err error)

// DrainRequest carries one idempotent drain batch: the fenced run,
// the cursor position and the batch size. Cursors order batches for
// determinism only; commit order is never inferred from them.
type DrainRequest struct {
	Season     string
	Generation int64
	Owner      string
	Limit      int
}

// BatchView reports one drained batch: processed items, holds
// released, escrows blocked, and whether each kind is exhausted.
type BatchView struct {
	Processed   int64
	Drained     int64
	Blocked     int64
	HoldsDone   bool
	EscrowsDone bool
}

// CloseRunView is the stored barrier run without driver types: the
// fence, the lease, the cutoff, the cursors, the progress hints and
// the conservation snapshot.
type CloseRunView struct {
	Season             string
	Generation         int64
	LeaseOwner         string
	LeasedUntil        time.Time
	State              string
	CutoffAt           time.Time
	LastHoldKey        string
	LastEscrowKey      string
	Drained            int64
	Blocked            int64
	SnapshotMilli      int64
	SnapshotLegs       int64
	SnapshotIntentions int64
}

// SealView proves one sealed book: the fence that sealed it, the
// cutoff it observed and the seal instant.
type SealView struct {
	Season     string
	Generation int64
	CutoffAt   time.Time
	SealedAt   time.Time
}

// CloseStore persists fenced closing barriers: one run per book
// with generation, lease, checkpoint cursors and the cutoff
// snapshot. Every mutation carries the fence and refuses stale or
// foreign writes in-transaction.
type CloseStore interface {
	// BeginClose records the barrier once the book end is reached on
	// the database clock. Two closers open exactly one run: the loser
	// replays the stored run in the same generation.
	BeginClose(ctx context.Context, season, owner string, ttl time.Duration) (CloseRunView, error)
	// DrainBatch releases one batch of active holds through the port
	// and classifies one batch of escrows, advancing the cursors in
	// the same transaction. Stale generations and foreign owners
	// refuse before any row is read.
	DrainBatch(ctx context.Context, req DrainRequest, releaseHold ReleaseHoldFunc) (BatchView, error)
	// Seal refuses while obligations stay open and records the sealed
	// lifecycle row once conserved. Blocked seals move nothing.
	Seal(ctx context.Context, season string, generation int64, owner string) (SealView, error)
	// RecordFailure parks a run that hit a worker error keeping the
	// cursor for resume.
	RecordFailure(ctx context.Context, season string, generation int64, owner, code, detail string) (CloseRunView, error)
	// Takeover moves a lapsed run to the next generation under a new
	// owner. A held lease refuses; sealed runs never reopen.
	Takeover(ctx context.Context, season, owner string, ttl time.Duration) (CloseRunView, error)
	// Load reads one stored run for fencing and resume.
	Load(ctx context.Context, season string) (CloseRunView, error)
}

// Worker drives one fenced closing run: barrier, idempotent drain
// batches, then seal. It moves no money itself: holds leave through
// the injected port, escrows never settle without wired evidence,
// and liquidation never enters any competitive ranking.
type Worker struct {
	Store       CloseStore
	ReleaseHold ReleaseHoldFunc
	BatchLimit  int
}

// validate checks the worker composition and batch size before the
// run starts: an unwired hold port with a positive limit is an
// explicit error, never a silent skip.
func (w *Worker) validate() error {
	if w.Store == nil {
		return fmt.Errorf("seasons: closing worker needs a store: %w", seasondomain.ErrInvalidSeason)
	}
	if w.BatchLimit < 1 {
		return fmt.Errorf("seasons: closing batch needs a positive limit: %w", seasondomain.ErrInvalidSeason)
	}
	return nil
}

// Begin records the barrier for one book.
func (w *Worker) Begin(ctx context.Context, season, owner string, ttl time.Duration) (CloseRunView, error) {
	if err := w.validate(); err != nil {
		return CloseRunView{}, err
	}
	return w.Store.BeginClose(ctx, season, owner, ttl)
}

// DrainOnce drains one batch of holds and escrows for a fenced run.
// Holds need the wired port: without it a pending hold batch is an
// explicit error and the run parks instead of pretending.
func (w *Worker) DrainOnce(ctx context.Context, run CloseRunView) (BatchView, error) {
	if err := w.validate(); err != nil {
		return BatchView{}, err
	}
	return w.Store.DrainBatch(ctx, DrainRequest{
		Season: run.Season, Generation: run.Generation, Owner: run.LeaseOwner, Limit: w.BatchLimit,
	}, w.ReleaseHold)
}

// Seal closes one drained book: refused with custody preserved
// while any escrow or hold stays open.
func (w *Worker) Seal(ctx context.Context, run CloseRunView) (SealView, error) {
	if err := w.validate(); err != nil {
		return SealView{}, err
	}
	return w.Store.Seal(ctx, run.Season, run.Generation, run.LeaseOwner)
}

// Park records one worker error keeping the cursor for resume.
func (w *Worker) Park(ctx context.Context, run CloseRunView, code, detail string) (CloseRunView, error) {
	if err := w.validate(); err != nil {
		return CloseRunView{}, err
	}
	return w.Store.RecordFailure(ctx, run.Season, run.Generation, run.LeaseOwner, code, detail)
}

// Takeover moves a lapsed run to the next generation under a new
// owner after the lease lapses on the database clock.
func (w *Worker) Takeover(ctx context.Context, season, owner string, ttl time.Duration) (CloseRunView, error) {
	if err := w.validate(); err != nil {
		return CloseRunView{}, err
	}
	return w.Store.Takeover(ctx, season, owner, ttl)
}

// Run records the barrier, drains until both kinds are exhausted,
// then seals. A nil hold port parks the run at the first pending
// hold batch instead of sealing over open obligations.
func (w *Worker) Run(ctx context.Context, season, owner string, ttl time.Duration) (SealView, error) {
	barrier, err := w.Begin(ctx, season, owner, ttl)
	if err != nil {
		return SealView{}, err
	}
	for {
		batch, err := w.DrainOnce(ctx, barrier)
		if err != nil {
			return SealView{}, err
		}
		if batch.HoldsDone && batch.EscrowsDone {
			return w.Seal(ctx, barrier)
		}
	}
}
