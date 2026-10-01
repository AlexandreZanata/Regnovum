package application

import (
	"context"
	"errors"
	"testing"
	"time"

	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

func closeCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// fakeRuns is an in-memory CloseStore: one run, scripted holds and
// escrows, fenced writes and a blocked seal switch.
type fakeRuns struct {
	run        CloseRunView
	hasRun     bool
	holds      []string
	escrows    []string
	released   map[string]int
	failSeal   bool
	sealed     bool
	parkedCode string
}

func newFakeRuns() *fakeRuns {
	return &fakeRuns{released: map[string]int{}}
}

func (f *fakeRuns) fence(season string, generation int64, owner string) error {
	if !f.hasRun {
		return seasondomain.ErrInvalidSeason
	}
	if season != f.run.Season {
		return seasondomain.ErrInvalidSeason
	}
	if generation != f.run.Generation {
		return seasondomain.ErrStaleGeneration
	}
	if owner != f.run.LeaseOwner {
		return seasondomain.ErrLeaseHeld
	}
	return nil
}

func (f *fakeRuns) BeginClose(_ context.Context, season, owner string, _ time.Duration) (CloseRunView, error) {
	if f.hasRun {
		return f.run, nil
	}
	f.run = CloseRunView{Season: season, Generation: 1, LeaseOwner: owner, State: "closing"}
	f.hasRun = true
	return f.run, nil
}

func (f *fakeRuns) DrainBatch(_ context.Context, req DrainRequest, releaseHold ReleaseHoldFunc) (BatchView, error) {
	if err := f.fence(req.Season, req.Generation, req.Owner); err != nil {
		return BatchView{}, err
	}
	var batch BatchView
	for _, hold := range f.holds {
		if hold <= f.run.LastHoldKey {
			continue
		}
		if batch.Processed >= int64(req.Limit) {
			break
		}
		if releaseHold == nil {
			return BatchView{}, errors.New("seasons: closing worker needs a hold port")
		}
		drain := seasondomain.CloseDrain{Season: req.Season, Generation: req.Generation, Owner: req.Owner}
		if _, err := releaseHold(context.Background(), hold, drain); err != nil {
			return BatchView{}, err
		}
		f.run.LastHoldKey = hold
		f.run.Drained++
		batch.Processed++
		batch.Drained++
	}
	batch.HoldsDone = len(f.pendingHolds()) == 0
	for _, escrow := range f.escrows {
		if escrow <= f.run.LastEscrowKey {
			continue
		}
		if batch.Processed >= int64(req.Limit) {
			break
		}
		f.run.LastEscrowKey = escrow
		f.run.Blocked++
		batch.Processed++
		batch.Blocked++
	}
	batch.EscrowsDone = len(f.pendingEscrows()) == 0
	return batch, nil
}

func (f *fakeRuns) pendingHolds() []string {
	var pending []string
	for _, hold := range f.holds {
		if hold > f.run.LastHoldKey {
			pending = append(pending, hold)
		}
	}
	return pending
}

func (f *fakeRuns) pendingEscrows() []string {
	var pending []string
	for _, escrow := range f.escrows {
		if escrow > f.run.LastEscrowKey {
			pending = append(pending, escrow)
		}
	}
	return pending
}

func (f *fakeRuns) Seal(_ context.Context, season string, generation int64, owner string) (SealView, error) {
	if err := f.fence(season, generation, owner); err != nil {
		return SealView{}, err
	}
	if f.failSeal || f.run.Blocked > 0 || len(f.pendingHolds()) > 0 || len(f.pendingEscrows()) > 0 {
		return SealView{}, seasondomain.ErrCloseBlocked
	}
	f.sealed = true
	return SealView{Season: season, Generation: generation}, nil
}

func (f *fakeRuns) RecordFailure(_ context.Context, season string, generation int64, owner, code, _ string) (CloseRunView, error) {
	if err := f.fence(season, generation, owner); err != nil {
		return CloseRunView{}, err
	}
	f.parkedCode = code
	return f.run, nil
}

func (f *fakeRuns) Load(_ context.Context, season string) (CloseRunView, error) {
	if !f.hasRun || season != f.run.Season {
		return CloseRunView{}, seasondomain.ErrInvalidSeason
	}
	return f.run, nil
}

func (f *fakeRuns) Takeover(_ context.Context, season, owner string, _ time.Duration) (CloseRunView, error) {
	if !f.hasRun || season != f.run.Season {
		return CloseRunView{}, seasondomain.ErrInvalidSeason
	}
	f.run.Generation++
	f.run.LeaseOwner = owner
	return f.run, nil
}

func nextGeneration(run CloseRunView) CloseRunView {
	run.Generation++
	return run
}

func foreignOwner(run CloseRunView) CloseRunView {
	run.LeaseOwner = "fechador-2"
	return run
}

func releaseFake(released map[string]int, failOnce map[string]bool) ReleaseHoldFunc {
	return func(_ context.Context, holdID string, _ seasondomain.CloseDrain) (bool, error) {
		if failOnce[holdID] {
			failOnce[holdID] = false
			return false, errors.New("hold repo unavailable")
		}
		released[holdID]++
		return true, nil
	}
}

func TestWorkerSealsEmptyBook(t *testing.T) {
	ctx, cancel := closeCtx()
	defer cancel()
	store := newFakeRuns()
	worker := &Worker{Store: store, BatchLimit: 10}
	sealed, err := worker.Run(ctx, "temporada-fecho-1", "fechador-1", time.Minute)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sealed.Season != "temporada-fecho-1" || sealed.Generation != 1 || !store.sealed {
		t.Fatalf("seal = %+v, want generation 1 sealed", sealed)
	}
}

func TestWorkerReleasesOnceAcrossFailureResume(t *testing.T) {
	ctx, cancel := closeCtx()
	defer cancel()
	store := newFakeRuns()
	store.holds = []string{"hold-aaa", "hold-bbb"}
	failOnce := map[string]bool{"hold-aaa": true}
	worker := &Worker{Store: store, ReleaseHold: releaseFake(store.released, failOnce), BatchLimit: 10}
	opened, err := worker.Begin(ctx, "temporada-fecho-1", "fechador-1", time.Minute)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := worker.DrainOnce(ctx, opened); err == nil {
		t.Fatal("failing batch passed: worker errors park the run, never the batch")
	}
	if _, err := worker.Park(ctx, opened, "drain-port", "hold repo unavailable"); err != nil {
		t.Fatalf("Park: %v", err)
	}
	if store.parkedCode != "drain-port" {
		t.Fatalf("parked = %q, want drain-port with cursor kept", store.parkedCode)
	}
	for i := 0; i < 3; i++ {
		batch, err := worker.DrainOnce(ctx, opened)
		if err != nil {
			t.Fatalf("resume DrainOnce: %v", err)
		}
		if batch.HoldsDone {
			break
		}
	}
	for hold, count := range store.released {
		if count != 1 {
			t.Fatalf("hold %s released %d times, want exactly once", hold, count)
		}
	}
	if len(store.released) != 2 {
		t.Fatalf("released = %v, want both holds once", store.released)
	}
}

func TestWorkerSealRefusesBlocked(t *testing.T) {
	ctx, cancel := closeCtx()
	defer cancel()
	store := newFakeRuns()
	store.escrows = []string{"escrow-aaa"}
	worker := &Worker{Store: store, BatchLimit: 10}
	if _, err := worker.Run(ctx, "temporada-fecho-1", "fechador-1", time.Minute); !errors.Is(err, seasondomain.ErrCloseBlocked) {
		t.Fatalf("blocked run = %v, want ErrCloseBlocked: custody preserved, no successor", err)
	}
	if store.sealed {
		t.Fatal("blocked book sealed: open obligations never seal")
	}
}

func TestWorkerRefusesStaleAndUnwired(t *testing.T) {
	ctx, cancel := closeCtx()
	defer cancel()
	store := newFakeRuns()
	store.holds = []string{"hold-aaa"}
	unwired := &Worker{Store: store, BatchLimit: 10}
	unused, err := unwired.Begin(ctx, "temporada-fecho-1", "fechador-1", time.Minute)
	opened := unused
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := unwired.DrainOnce(ctx, opened); err == nil {
		t.Fatal("unwired drain passed: pending holds without a port are an explicit error")
	}
	wired := &Worker{Store: store, ReleaseHold: releaseFake(store.released, map[string]bool{}), BatchLimit: 10}
	if _, err := wired.DrainOnce(ctx, nextGeneration(opened)); !errors.Is(err, seasondomain.ErrStaleGeneration) {
		t.Fatalf("stale generation = %v, want ErrStaleGeneration", err)
	}
	if _, err := wired.DrainOnce(ctx, foreignOwner(opened)); !errors.Is(err, seasondomain.ErrLeaseHeld) {
		t.Fatalf("foreign owner = %v, want ErrLeaseHeld", err)
	}
}
