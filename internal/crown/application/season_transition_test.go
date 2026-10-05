package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

type fakeSeasonAuthority struct {
	views map[domain.SeasonID]SeasonAuthorityView
	err   error
}

func (f *fakeSeasonAuthority) LoadSeasonAuthority(_ context.Context, season domain.SeasonID) (SeasonAuthorityView, error) {
	if f.err != nil {
		return SeasonAuthorityView{}, f.err
	}
	v, ok := f.views[season]
	if !ok {
		return SeasonAuthorityView{}, domain.ErrSeasonMismatch
	}
	return v, nil
}

type fakeInitialStore struct {
	saved      map[domain.SeasonID]domain.InitialReignOutcome
	saveCalls  int
	failFirst  error
	crashArmed bool
}

func newFakeInitialStore() *fakeInitialStore {
	return &fakeInitialStore{saved: map[domain.SeasonID]domain.InitialReignOutcome{}}
}

func (f *fakeInitialStore) FindInitial(_ context.Context, season domain.SeasonID) (domain.InitialReignOutcome, bool, error) {
	out, ok := f.saved[season]
	return out, ok, nil
}

func (f *fakeInitialStore) SaveInitial(_ context.Context, outcome domain.InitialReignOutcome) (bool, error) {
	if f.crashArmed && f.failFirst != nil {
		err := f.failFirst
		f.crashArmed = false
		return false, err
	}
	if stored, ok := f.saved[outcome.Season]; ok {
		if stored.Holder != outcome.Holder || stored.Policy != outcome.Policy || stored.IsRegent != outcome.IsRegent {
			return false, domain.ErrTamperedAct
		}
		return true, nil
	}
	f.saveCalls++
	f.saved[outcome.Season] = outcome
	return false, nil
}

func transitionView() SeasonAuthorityView {
	start := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	end := start.Add(domain.SeasonalWindowSeconds * time.Second)
	return SeasonAuthorityView{
		Season: "temporada-2", StartsAt: start, EndsAt: end,
		Open: true, PredecessorSealed: true,
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
		Policy: domain.WealthPolicyV1,
	}
}

// Repeated opening and crash resume record exactly one reign v1.
func TestSeasonTransition_RepeatedOpeningAndCrashReplay(t *testing.T) {
	ctx := context.Background()
	seasons := &fakeSeasonAuthority{views: map[domain.SeasonID]SeasonAuthorityView{"temporada-2": transitionView()}}
	store := newFakeInitialStore()
	uc := NewSeasonTransitionUseCase(seasons, store)

	first, err := uc.OpenInitial(ctx, OpenSeasonCommand{Season: "temporada-2", MonarchPresent: true, MonarchEligible: true})
	if err != nil {
		t.Fatalf("OpenInitial: %v", err)
	}
	second, err := uc.OpenInitial(ctx, OpenSeasonCommand{Season: "temporada-2", MonarchPresent: true, MonarchEligible: true})
	if err != nil {
		t.Fatalf("repeated OpenInitial: %v", err)
	}
	if first != second || store.saveCalls != 1 {
		t.Fatalf("repeated opening duplicated reign: first=%+v second=%+v saves=%d", first, second, store.saveCalls)
	}

	// Crash between decision and save resumes without a second reign:
	seasons2 := &fakeSeasonAuthority{views: map[domain.SeasonID]SeasonAuthorityView{"temporada-3": {
		Season: "temporada-3", StartsAt: transitionView().StartsAt, EndsAt: transitionView().EndsAt,
		Open: true, PredecessorSealed: true,
		InitialMonarch: "fundadora", Regent: "regente-tecnica", Policy: domain.WealthPolicyV1,
	}}}
	store2 := newFakeInitialStore()
	store2.crashArmed = true
	store2.failFirst = errors.New("crash antes do commit")
	uc2 := NewSeasonTransitionUseCase(seasons2, store2)
	if _, err := uc2.OpenInitial(ctx, OpenSeasonCommand{Season: "temporada-3", MonarchPresent: true, MonarchEligible: true}); err == nil {
		t.Fatal("crashed open passed: crash recupera sem duplicar")
	}
	resumed, err := uc2.OpenInitial(ctx, OpenSeasonCommand{Season: "temporada-3", MonarchPresent: true, MonarchEligible: true})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Reign != 1 || resumed.Holder != "fundadora" {
		t.Fatalf("resumed = %+v, want single v1 founder", resumed)
	}
}

// Ex-King in the new season has no authority and old wealth never triggers the algorithm.
func TestSeasonTransition_ExKingHasNoAuthorityInSuccessor(t *testing.T) {
	ctx := context.Background()
	seasons := &fakeSeasonAuthority{views: map[domain.SeasonID]SeasonAuthorityView{"temporada-2": transitionView()}}
	store := newFakeInitialStore()
	uc := NewSeasonTransitionUseCase(seasons, store)

	outcome, err := uc.OpenInitial(ctx, OpenSeasonCommand{Season: "temporada-2", MonarchPresent: true, MonarchEligible: true})
	if err != nil {
		t.Fatalf("OpenInitial: %v", err)
	}
	if outcome.Holder == "alice" {
		t.Fatal("ex-King alice inherited the successor: nova season não herda titular")
	}
	if outcome.WinningWealth != 0 {
		t.Fatalf("successor wealth = %d, want 0 (riqueza antiga nunca aciona o algoritmo)", outcome.WinningWealth)
	}

	// Alice (ex-Queen of temporada-1) claiming temporada-2 refuses:
	anchor := transitionView().StartsAt.Add(2 * time.Hour)
	current := domain.CurrentReign{
		Season: "temporada-2", Holder: outcome.Holder, Reign: 1,
		StartsAt: transitionView().StartsAt, EndsAt: transitionView().EndsAt, Open: true,
	}
	claim := domain.Claim{Act: "ato-1", Season: "temporada-2", Holder: "alice", Competence: "cerimonial", Reign: 1}
	session := domain.SovereignSession{
		Season: "temporada-2", Subject: "alice", Reign: 1, Competence: "cerimonial",
		AuthenticatedAt: anchor.Add(-10 * time.Minute), MFAAt: anchor.Add(-9 * time.Minute), ExpiresAt: anchor.Add(20 * time.Minute),
	}
	if _, err := domain.AuthorizeAct(claim, session, nil, current, domain.AuthContext{Now: anchor, MaxSessionAge: 30 * time.Minute}); !errors.Is(err, domain.ErrNotHolder) {
		t.Fatalf("ex-King claim err = %v, want ErrNotHolder", err)
	}
}

// Ineligible founder falls back to limited regency; without regent there are zero kings.
func TestSeasonTransition_IneligibleFounder(t *testing.T) {
	ctx := context.Background()
	seasons := &fakeSeasonAuthority{views: map[domain.SeasonID]SeasonAuthorityView{"temporada-2": transitionView()}}
	store := newFakeInitialStore()
	uc := NewSeasonTransitionUseCase(seasons, store)

	outcome, err := uc.OpenInitial(ctx, OpenSeasonCommand{Season: "temporada-2", MonarchPresent: true, MonarchEligible: false})
	if err != nil {
		t.Fatalf("OpenInitial ineligible: %v", err)
	}
	if outcome.Holder != "regente-tecnica" || !outcome.IsRegent {
		t.Fatalf("ineligible founder outcome = %+v, want limited regent", outcome)
	}

	view := transitionView()
	view.InitialMonarch = "fundadora"
	view.Regent = ""
	seasonsNoRegent := &fakeSeasonAuthority{views: map[domain.SeasonID]SeasonAuthorityView{"temporada-2": view}}
	ucNoRegent := NewSeasonTransitionUseCase(seasonsNoRegent, newFakeInitialStore())
	if _, err := ucNoRegent.OpenInitial(ctx, OpenSeasonCommand{Season: "temporada-2", MonarchPresent: true, MonarchEligible: false}); !errors.Is(err, domain.ErrNoQualifiedSuccessor) {
		t.Fatalf("ineligible without regent err = %v, want ErrNoQualifiedSuccessor (zero reis)", err)
	}
}

// Cutoff ends offices: past the end only technical liquidation runs, never a second throne.
func TestSeasonTransition_CloseEndsOffices(t *testing.T) {
	ctx := context.Background()
	uc := NewSeasonTransitionUseCase(&fakeSeasonAuthority{}, newFakeInitialStore())
	view := transitionView()
	current := domain.CurrentReign{
		Season: "temporada-1", Holder: "alice", Reign: 3,
		StartsAt: view.StartsAt.Add(-time.Duration(domain.SeasonalWindowSeconds) * time.Second), EndsAt: view.StartsAt, Open: true,
	}
	live, technical, err := uc.CloseSeason(ctx, "temporada-1", current, view.StartsAt.Add(-time.Hour))
	if err != nil || technical || !live.Open {
		t.Fatalf("before cutoff = %+v technical=%v err=%v, want live reign", live, technical, err)
	}
	closed, technical, err := uc.CloseSeason(ctx, "temporada-1", current, view.StartsAt)
	if err != nil || !technical || closed.Open {
		t.Fatalf("at cutoff = %+v technical=%v err=%v, want closed technical-only", closed, technical, err)
	}
	if err := domain.AssertSingleAuthority([]domain.HolderSubject{closed.Holder}); err != nil {
		t.Fatalf("single holder err = %v", err)
	}
	if err := domain.AssertSingleAuthority([]domain.HolderSubject{closed.Holder, "bob"}); !errors.Is(err, domain.ErrActiveReignConflict) {
		t.Fatalf("two live holders must conflict")
	}
}
