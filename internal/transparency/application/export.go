package application

import (
	"context"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/transparency/domain"
)

// Public export pagination bounds fixed by the API conventions: the page
// defaults to 20 arguments and never exceeds 100, so one request can never
// materialize an arbitrarily large Arena (P14-T04).
const (
	DefaultExportPageLimit = 20
	MaxExportPageLimit     = 100
)

// ExportArena is the public identity and state of the exported Arena. It
// mirrors the public Arena document: no creator, no internal version.
type ExportArena struct {
	ID          string
	Slug        string
	Statement   string
	Context     string
	Category    string
	Language    string
	Status      string
	PublishedAt time.Time
	ClosesAt    *time.Time
}

// ExportDistribution is one aggregate distribution over eligible
// participants. It carries counts only: no account identifier exists here.
type ExportDistribution struct {
	Agree     int64
	Disagree  int64
	Undecided int64
}

// ExportPositions is the aggregated position picture of one Arena: the
// initial and current distributions, the eligible total, the total number
// of accepted changes and whether the sample was too small to publish.
// Individual positions and individual change histories never enter this
// projection.
type ExportPositions struct {
	Initial         ExportDistribution
	Current         ExportDistribution
	Participants    int64
	PositionChanges int64
	Suppressed      bool
}

// ExportInfluence is the Arena-level valid influence count: valid
// attribution events and the distinct authors credited in the Arena. It
// carries counts only, never an attributor identity.
type ExportInfluence struct {
	ValidAttributions int64
	InfluencedAuthors int64
}

// ExportArgumentInfluence is the public count of one argument: the valid
// attribution events it received and the eligible people who credited it,
// each person counted once. The attributor identity is private by default.
type ExportArgumentInfluence struct {
	ValidAttributions int64
	DistinctPeople    int64
}

// ExportSource is one structured source supporting an argument.
type ExportSource struct {
	URL         string
	Description *string
}

// ExportArgument is one public argument in the export: published content
// or the retracted placeholder (content and sources withheld while the
// status stays visible) plus its public counts. Removed arguments never
// appear.
type ExportArgument struct {
	ID          string
	ParentID    string
	Relation    string
	Content     *string
	Status      string
	CreatedAt   time.Time
	WithdrawnAt *time.Time
	Sources     []ExportSource
	Influence   ExportArgumentInfluence
}

// ExportHeader is the Arena-level part of the export: identity, state and
// aggregates, identical for every page of one document.
type ExportHeader struct {
	Arena     ExportArena
	Positions ExportPositions
	Influence ExportInfluence
}

// ArenaExportRepository is the consumer-oriented port of the public Arena
// export. It is an approved read-only projection over the arenas,
// positions, arguments, sources and attribution schemas: it never mutates
// and it only ever returns public columns (no creator, author, attributor,
// email or provider data).
type ArenaExportRepository interface {
	// GetArenaExportHeader resolves the publicly readable Arena (published,
	// closed or restricted) plus its derived aggregates. Unknown, draft and
	// removed Arenas are ErrArenaNotFound.
	GetArenaExportHeader(ctx context.Context, arenaID string) (*ExportHeader, error)

	// ListArenaExportArguments returns one bounded page of published and
	// withdrawn arguments of one Arena, oldest first, strictly after the
	// position. The caller asks for limit+1 rows and trims the lookahead.
	ListArenaExportArguments(ctx context.Context, arenaID string, after *ExportPosition, limit int) ([]ExportArgument, error)
}

// GetArenaExportQuery addresses one page of the versioned public export.
type GetArenaExportQuery struct {
	ArenaID string
	Cursor  string
	Limit   int
}

// ExportPage is one page of the versioned document: schema version, Arena
// identity and state, aggregates, a bounded argument page and the cursor of
// the next page (empty on the last one).
type ExportPage struct {
	SchemaVersion int
	Arena         ExportArena
	Positions     ExportPositions
	Influence     ExportInfluence
	Arguments     []ExportArgument
	NextCursor    string
}

// GetArenaExportUseCase assembles one export page. Aggregates are derived
// per request from source rows and the argument list is keyset-paginated,
// so a large Arena is exported through bounded pages instead of a single
// in-memory document.
type GetArenaExportUseCase struct {
	exports ArenaExportRepository
	cursors *ExportCursorCodec
}

// NewGetArenaExportUseCase builds the use case, refusing incomplete
// composition.
func NewGetArenaExportUseCase(exports ArenaExportRepository, cursors *ExportCursorCodec) (*GetArenaExportUseCase, error) {
	if exports == nil || cursors == nil {
		return nil, ErrInvalidExportConfig
	}
	return &GetArenaExportUseCase{exports: exports, cursors: cursors}, nil
}

// Execute resolves the requested export page. Unknown Arenas answer
// ErrArenaNotFound, forged cursors answer ErrInvalidCursor and the page
// size is always clamped to the contract bounds.
func (uc *GetArenaExportUseCase) Execute(ctx context.Context, query GetArenaExportQuery) (*ExportPage, error) {
	arenaID := strings.TrimSpace(query.ArenaID)
	if arenaID == "" {
		return nil, ErrArenaNotFound
	}

	after, err := uc.cursors.Decode(query.Cursor)
	if err != nil {
		return nil, err
	}

	limit := clampExportPageLimit(query.Limit)
	header, err := uc.exports.GetArenaExportHeader(ctx, arenaID)
	if err != nil {
		return nil, err
	}

	arguments, err := uc.exports.ListArenaExportArguments(ctx, arenaID, after, limit+1)
	if err != nil {
		return nil, err
	}

	page := &ExportPage{
		SchemaVersion: domain.ExportSchemaVersion,
		Arena:         header.Arena,
		Positions:     suppressExportPositions(header.Positions),
		Influence:     header.Influence,
		Arguments:     arguments,
	}
	if len(arguments) > limit {
		page.Arguments = arguments[:limit]
		page.NextCursor = uc.cursors.Encode(page.Arguments[limit-1])
	}
	return page, nil
}

// suppressExportPositions applies the uniform low-count rule: a population
// below the threshold publishes no total, no distribution and no change
// count at all, so a small sample never reveals an individual position.
func suppressExportPositions(positions ExportPositions) ExportPositions {
	if positions.Participants < domain.LowCountThreshold {
		return ExportPositions{Suppressed: true}
	}
	return positions
}

// clampExportPageLimit applies the contract bounds: default 20, maximum
// 100.
func clampExportPageLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultExportPageLimit
	case limit > MaxExportPageLimit:
		return MaxExportPageLimit
	default:
		return limit
	}
}
