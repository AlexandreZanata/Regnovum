package application

import (
	"context"
	"fmt"
	"time"

	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

// GenesisFunc records the creation event of one successor book. It is
// a function port so the seasons module never wires another module's
// adapter: tests inject a closure over the real repository, and
// production wiring stays out until the release gate. It reports
// whether the call replayed an existing Genesis: a second opener
// replays instead of minting twice.
type GenesisFunc func(ctx context.Context, season, genesisKey string) (replayed bool, err error)

// ArchiveProof is the stored seal one predecessor carries into its
// successor opening: the fence that sealed it, the cutoff it observed
// and the conservation snapshot.
type ArchiveProof struct {
	Season     string
	CutoffAt   time.Time
	SealedAt   time.Time
	Milli      int64
	Legs       int64
	Intentions int64
	Replayed   bool
}

// OpenResult proves one idempotent opening: the archived predecessor,
// the opened successor and which steps replayed. Personal accounts
// start at zero by construction: the opener never copies balances.
type OpenResult struct {
	Predecessor      string
	Successor        string
	ArchiveReplayed  bool
	PreparedReplayed bool
	GenesisReplayed  bool
	ActiveReplayed   bool
}

// OpenStore persists season archives and successor books: one archive
// row per sealed book, one season row per successor and append-only
// lifecycle stages. Every mutation is idempotent: a second opener
// replays the stored outcome instead of duplicating Genesis or ACTIVE.
type OpenStore interface {
	// Archive records the predecessor seal and its archived stage.
	// A predecessor without a seal refuses with ErrCloseBlocked;
	// a diverged snapshot refuses with ErrInvalidSeason.
	Archive(ctx context.Context, predecessor string) (ArchiveProof, error)
	// EnsurePrepared records the successor season and its prepared
	// stage. Continuity is exact: the successor starts at the
	// predecessor end with the next ordinal.
	EnsurePrepared(ctx context.Context, predecessor string, next seasondomain.ManifestRequest) (replayed bool, err error)
	// Activate records the successor active stage once its Genesis
	// is conserved and fresh. A missing Genesis or a carried balance
	// refuses; two activators record exactly one ACTIVE.
	Activate(ctx context.Context, predecessor, successor string) (replayed bool, err error)
}

// Opener drives one idempotent archive-and-open: seal proof, prepared
// successor, Genesis, then active. It moves no money itself: Genesis
// runs through the injected port, user balances are never copied, and
// old holder offices end by event, never by deleting accounts.
type Opener struct {
	Store   OpenStore
	Genesis GenesisFunc
	Now     func() time.Time
}

// validate checks the opener composition before any row is read.
func (o *Opener) validate() error {
	if o.Store == nil {
		return fmt.Errorf("seasons: opener needs a store: %w", seasondomain.ErrInvalidSeason)
	}
	if o.Genesis == nil {
		return fmt.Errorf("seasons: opener needs a genesis port: %w", seasondomain.ErrInvalidSeason)
	}
	return nil
}

// OpenNext archives one sealed predecessor and opens its exact
// successor. Crash between steps resumes by replay: every stage is
// recorded once, Genesis runs once per book and ACTIVE appears once.
func (o *Opener) OpenNext(ctx context.Context, predecessor string, next seasondomain.ManifestRequest, genesisKey string) (OpenResult, error) {
	if err := o.validate(); err != nil {
		return OpenResult{}, err
	}
	if _, err := seasondomain.ParseCloseOwner(predecessor); err != nil {
		return OpenResult{}, err
	}
	if _, err := seasondomain.ParseCloseOwner(genesisKey); err != nil {
		return OpenResult{}, err
	}
	proof, err := o.Store.Archive(ctx, predecessor)
	if err != nil {
		return OpenResult{}, err
	}
	preparedReplayed, err := o.Store.EnsurePrepared(ctx, predecessor, next)
	if err != nil {
		return OpenResult{}, err
	}
	genesisReplayed, err := o.Genesis(ctx, next.ID, genesisKey)
	if err != nil {
		return OpenResult{}, err
	}
	activeReplayed, err := o.Store.Activate(ctx, predecessor, next.ID)
	if err != nil {
		return OpenResult{}, err
	}
	return OpenResult{
		Predecessor: predecessor, Successor: next.ID,
		ArchiveReplayed: proof.Replayed, PreparedReplayed: preparedReplayed,
		GenesisReplayed: genesisReplayed, ActiveReplayed: activeReplayed,
	}, nil
}
