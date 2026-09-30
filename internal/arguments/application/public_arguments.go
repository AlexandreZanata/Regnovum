package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
)

// Public pagination bounds fixed by the API conventions (limit defaults to
// 20, maximum 100).
const (
	DefaultArgumentPageLimit = 20
	MaxArgumentPageLimit     = 100
)

// PublicArgument is the cache-safe public projection of one argument: it is
// identical for every visitor and carries no account identifier. The
// visibility policy is applied here (P10-T07): only published arguments
// appear in lists; a withdrawn argument resolves as a retracted placeholder
// on direct reads (content withheld, status visible); a moderation-removed
// argument is not found.
type PublicArgument struct {
	ID        domain.ArgumentID
	ArenaID   domain.ArenaID
	ParentID  domain.ArgumentID
	Relation  domain.Relation
	Content   *domain.Content
	Status    string
	CreatedAt time.Time
	// ReplyCount is the derived count of published replies, computed in the
	// same statement as the page (never one query per row).
	ReplyCount int64
}

// ArgumentPage is one keyset page plus the cursor of the next page (empty
// when the page is the last one).
type ArgumentPage struct {
	Arguments  []PublicArgument
	NextCursor string
}

// ArgumentQueryRepository is the consumer-oriented port of the public
// argument read model.
type ArgumentQueryRepository interface {
	// ListArenaArguments returns published top-level arguments of one
	// relation in one Arena, newest first, strictly older than the position.
	ListArenaArguments(ctx context.Context, arenaID domain.ArenaID, relation domain.Relation, after *ArgumentPosition, limit int) ([]PublicArgument, error)

	// ListReplies returns published replies of one parent, newest first,
	// strictly older than the position.
	ListReplies(ctx context.Context, parentID domain.ArgumentID, after *ArgumentPosition, limit int) ([]PublicArgument, error)

	// GetPublicArgument returns one argument for the public surface:
	// published and withdrawn (retracted) arguments resolve; removed ones
	// are not found.
	GetPublicArgument(ctx context.Context, argumentID domain.ArgumentID) (*PublicArgument, error)
}

// ListArenaArgumentsQuery addresses the public list of one relation in one
// Arena.
type ListArenaArgumentsQuery struct {
	ArenaID  string
	Relation string
	Cursor   string
	Limit    int
}

// ListArenaArgumentsUseCase answers the public per-relation list, newest
// first, as a keyset page.
type ListArenaArgumentsUseCase struct {
	arguments ArgumentQueryRepository
	cursors   *ArgumentCursorCodec
}

// NewListArenaArgumentsUseCase creates an instance of
// ListArenaArgumentsUseCase.
func NewListArenaArgumentsUseCase(arguments ArgumentQueryRepository, cursors *ArgumentCursorCodec) *ListArenaArgumentsUseCase {
	return &ListArenaArgumentsUseCase{arguments: arguments, cursors: cursors}
}

// Execute returns one page.
func (uc *ListArenaArgumentsUseCase) Execute(ctx context.Context, query ListArenaArgumentsQuery) (*ArgumentPage, error) {
	arenaID, err := domain.ParseArenaID(query.ArenaID)
	if err != nil {
		return nil, err
	}
	relation, err := domain.ParseRelation(query.Relation)
	if err != nil {
		return nil, err
	}
	after, err := uc.cursors.Decode(query.Cursor)
	if err != nil {
		return nil, err
	}

	limit := clampArgumentPageLimit(query.Limit)
	arguments, err := uc.arguments.ListArenaArguments(ctx, arenaID, relation, after, limit+1)
	if err != nil {
		return nil, err
	}
	return uc.page(arguments, limit), nil
}

// page trims the lookahead row and signs the next cursor.
func (uc *ListArenaArgumentsUseCase) page(arguments []PublicArgument, limit int) *ArgumentPage {
	page := &ArgumentPage{Arguments: arguments}
	if len(arguments) > limit {
		page.Arguments = arguments[:limit]
		page.NextCursor = uc.cursors.Encode(page.Arguments[limit-1])
	}
	return page
}

// ListRepliesQuery addresses the public replies of one argument.
type ListRepliesQuery struct {
	ParentID string
	Cursor   string
	Limit    int
}

// ListRepliesUseCase answers the public replies list of one parent, newest
// first, as a keyset page.
type ListRepliesUseCase struct {
	arguments ArgumentQueryRepository
	cursors   *ArgumentCursorCodec
}

// NewListRepliesUseCase creates an instance of ListRepliesUseCase.
func NewListRepliesUseCase(arguments ArgumentQueryRepository, cursors *ArgumentCursorCodec) *ListRepliesUseCase {
	return &ListRepliesUseCase{arguments: arguments, cursors: cursors}
}

// Execute returns one page.
func (uc *ListRepliesUseCase) Execute(ctx context.Context, query ListRepliesQuery) (*ArgumentPage, error) {
	parentID, err := domain.ParseArgumentID(query.ParentID)
	if err != nil {
		return nil, err
	}
	after, err := uc.cursors.Decode(query.Cursor)
	if err != nil {
		return nil, err
	}

	limit := clampArgumentPageLimit(query.Limit)
	arguments, err := uc.arguments.ListReplies(ctx, parentID, after, limit+1)
	if err != nil {
		return nil, err
	}
	return uc.page(arguments, limit), nil
}

// page trims the lookahead row and signs the next cursor.
func (uc *ListRepliesUseCase) page(arguments []PublicArgument, limit int) *ArgumentPage {
	page := &ArgumentPage{Arguments: arguments}
	if len(arguments) > limit {
		page.Arguments = arguments[:limit]
		page.NextCursor = uc.cursors.Encode(page.Arguments[limit-1])
	}
	return page
}

// GetPublicArgumentQuery addresses one public argument.
type GetPublicArgumentQuery struct {
	ArgumentID string
}

// GetPublicArgumentUseCase resolves one argument for the public surface,
// applying the visibility policy (retracted withdrawn arguments, missing
// removed ones).
type GetPublicArgumentUseCase struct {
	arguments ArgumentQueryRepository
}

// NewGetPublicArgumentUseCase creates an instance of
// GetPublicArgumentUseCase.
func NewGetPublicArgumentUseCase(arguments ArgumentQueryRepository) *GetPublicArgumentUseCase {
	return &GetPublicArgumentUseCase{arguments: arguments}
}

// Execute returns the public projection.
func (uc *GetPublicArgumentUseCase) Execute(ctx context.Context, query GetPublicArgumentQuery) (*PublicArgument, error) {
	argumentID, err := domain.ParseArgumentID(query.ArgumentID)
	if err != nil {
		return nil, err
	}
	return uc.arguments.GetPublicArgument(ctx, argumentID)
}

// clampArgumentPageLimit applies the contract bounds: default 20, maximum
// 100.
func clampArgumentPageLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultArgumentPageLimit
	case limit > MaxArgumentPageLimit:
		return MaxArgumentPageLimit
	default:
		return limit
	}
}
