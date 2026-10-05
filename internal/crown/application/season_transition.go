package application

import (
	"context"
	"fmt"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

// SeasonAuthorityView is the consumer view of one season book read
// through the seasons module: window, openness, predecessor seal,
// published initial holder and policy. It travels as scalars so the
// crown never imports the seasons module: domains stay disjoint.
type SeasonAuthorityView struct {
	Season            domain.SeasonID
	StartsAt          time.Time
	EndsAt            time.Time
	Open              bool
	PredecessorSealed bool
	InitialMonarch    domain.HolderSubject
	Regent            domain.HolderSubject
	Policy            domain.WealthPolicyVersion
}

// SeasonAuthoritySource is the consumer port for seasonal authority:
// it answers the current book window and manifesto terms. Unknown
// books refuse with domain.ErrSeasonMismatch; diverged snapshots
// refuse with domain.ErrInvalidAuthority. Implementations stay out
// of production wiring until the release gate.
type SeasonAuthoritySource interface {
	LoadSeasonAuthority(ctx context.Context, season domain.SeasonID) (SeasonAuthorityView, error)
}

// InitialReignStore persists the first investiture of one book
// exactly once: idempotent replay returns the stored outcome,
// divergent rewrites refuse. It never copies wealth: outcomes
// always carry zero measured wealth.
type InitialReignStore interface {
	FindInitial(ctx context.Context, season domain.SeasonID) (domain.InitialReignOutcome, bool, error)
	SaveInitial(ctx context.Context, outcome domain.InitialReignOutcome) (replayed bool, err error)
}

// OpenSeasonCommand opens the first reign of one successor book:
// the season key plus founder presence/eligibility resolved by the
// caller through verifiable consent (P47-T05). Old-season wealth
// never enters the command: new books start at zero by
// construction.
type OpenSeasonCommand struct {
	Season          string
	MonarchPresent  bool
	MonarchEligible bool
}

// SeasonTransitionUseCase bounds reigns to seasonal authority: it
// closes the old book at cutoff and opens the successor from its
// manifesto, with limited regency and fail-closed errors. Any error
// keeps the economy blocked until a coherent state is stored.
type SeasonTransitionUseCase struct {
	seasons SeasonAuthoritySource
	reigns  InitialReignStore
}

// NewSeasonTransitionUseCase creates an instance of SeasonTransitionUseCase.
func NewSeasonTransitionUseCase(seasons SeasonAuthoritySource, reigns InitialReignStore) *SeasonTransitionUseCase {
	return &SeasonTransitionUseCase{seasons: seasons, reigns: reigns}
}

// OpenInitial decides and records the first reign of one book once.
// A recorded investiture replays identically: repeated openings and
// crash resumes return the stored outcome without a second reign
// version. A divergent manifesto or holder refuses instead of
// forking the throne.
func (uc *SeasonTransitionUseCase) OpenInitial(ctx context.Context, cmd OpenSeasonCommand) (domain.InitialReignOutcome, error) {
	if uc.seasons == nil || uc.reigns == nil {
		return domain.InitialReignOutcome{}, domain.ErrInvalidAuthority
	}
	season, err := domain.ParseSeasonID(cmd.Season)
	if err != nil {
		return domain.InitialReignOutcome{}, err
	}
	if stored, found, err := uc.reigns.FindInitial(ctx, season); err != nil {
		return domain.InitialReignOutcome{}, err
	} else if found {
		return stored, nil
	}
	view, err := uc.seasons.LoadSeasonAuthority(ctx, season)
	if err != nil {
		return domain.InitialReignOutcome{}, err
	}
	if view.Season != season {
		return domain.InitialReignOutcome{}, fmt.Errorf("season authority for another book: %w", domain.ErrSeasonMismatch)
	}
	outcome, err := domain.DecideInitialReign(domain.InitialInvestitureInput{
		Policy:            view.Policy,
		Season:            season,
		StartsAt:          view.StartsAt,
		EndsAt:            view.EndsAt,
		PredecessorSealed: view.PredecessorSealed,
		InitialMonarch:    view.InitialMonarch,
		Regent:            view.Regent,
		MonarchPresent:    cmd.MonarchPresent,
		MonarchEligible:   cmd.MonarchEligible,
	})
	if err != nil {
		return domain.InitialReignOutcome{}, err
	}
	replayed, err := uc.reigns.SaveInitial(ctx, outcome)
	if err != nil {
		return domain.InitialReignOutcome{}, err
	}
	if replayed {
		stored, found, err := uc.reigns.FindInitial(ctx, season)
		if err != nil {
			return domain.InitialReignOutcome{}, err
		}
		if !found {
			return domain.InitialReignOutcome{}, domain.ErrSeasonMismatch
		}
		if stored.Holder != outcome.Holder || stored.Policy != outcome.Policy || stored.IsRegent != outcome.IsRegent {
			return domain.InitialReignOutcome{}, domain.ErrTamperedAct
		}
		return stored, nil
	}
	return outcome, nil
}

// CloseSeason terminates reign authority at cutoff for one book: it
// loads the invested snapshot through the ReignResolver-compatible
// loader, ends offices at the exclusive end and reports whether
// only permitted technical liquidation may run. Inside the window
// authority stays live; any error keeps the economy blocked.
func (uc *SeasonTransitionUseCase) CloseSeason(ctx context.Context, season domain.SeasonID, current domain.CurrentReign, now time.Time) (domain.CurrentReign, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.CurrentReign{}, true, err
	}
	if now.IsZero() {
		return domain.CurrentReign{}, true, domain.ErrInvalidAuthority
	}
	if current.Season != season {
		return domain.CurrentReign{}, true, domain.ErrSeasonMismatch
	}
	terminated, technicalOnly := domain.TerminateReignAtCutoff(current, now)
	return terminated, technicalOnly, nil
}
