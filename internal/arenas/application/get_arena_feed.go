package application

import (
	"context"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// GetArenaFeedCommand holds the optional feed filters and pagination
// parameters. Empty filter strings mean "no filter".
type GetArenaFeedCommand struct {
	Language string
	Category string
	Status   string
	Cursor   string
	Limit    int
}

// GetArenaFeedUseCase answers the public feed with keyset pagination. The
// cursor only moves backward from the last delivered Arena, so entries can
// never be duplicated or skipped, and drafts never appear.
type GetArenaFeedUseCase struct {
	arenas  ArenaFeedRepository
	cursors *FeedCursorCodec
}

// NewGetArenaFeedUseCase creates an instance of GetArenaFeedUseCase with
// signed cursors.
func NewGetArenaFeedUseCase(arenas ArenaFeedRepository, cursors *FeedCursorCodec) *GetArenaFeedUseCase {
	return &GetArenaFeedUseCase{arenas: arenas, cursors: cursors}
}

// Execute returns one page of the public feed. Limits are clamped to the
// contract bounds: default 20, maximum 100.
func (uc *GetArenaFeedUseCase) Execute(ctx context.Context, cmd GetArenaFeedCommand) (*ArenaFeedPage, error) {
	filter, err := buildFeedFilter(cmd)
	if err != nil {
		return nil, err
	}

	after, err := uc.cursors.Decode(cmd.Cursor)
	if err != nil {
		return nil, err
	}

	limit := cmd.Limit
	switch {
	case limit <= 0:
		limit = DefaultFeedLimit
	case limit > MaxFeedLimit:
		limit = MaxFeedLimit
	}

	arenas, err := uc.arenas.ListPublicArenas(ctx, filter, after, limit+1)
	if err != nil {
		return nil, err
	}

	page := &ArenaFeedPage{Arenas: arenas}
	if len(arenas) > limit {
		page.Arenas = arenas[:limit]
		page.NextCursor = uc.cursors.Encode(page.Arenas[limit-1])
	}
	return page, nil
}

// buildFeedFilter validates the optional filters. Unknown values are
// rejected instead of being reflected; the status filter only accepts
// publicly visible statuses.
func buildFeedFilter(cmd GetArenaFeedCommand) (ArenaFeedFilter, error) {
	var filter ArenaFeedFilter

	if strings.TrimSpace(cmd.Language) != "" {
		language, err := domain.ParseLanguage(cmd.Language)
		if err != nil {
			return ArenaFeedFilter{}, err
		}
		filter.Language = &language
	}

	if strings.TrimSpace(cmd.Category) != "" {
		category, err := domain.ParseCategory(cmd.Category)
		if err != nil {
			return ArenaFeedFilter{}, err
		}
		filter.Category = &category
	}

	if strings.TrimSpace(cmd.Status) != "" {
		status := domain.ArenaStatus(strings.TrimSpace(cmd.Status))
		switch status {
		case domain.ArenaStatusPublished, domain.ArenaStatusClosed, domain.ArenaStatusRestricted:
			filter.Status = &status
		default:
			return ArenaFeedFilter{}, ErrInvalidFeedFilter
		}
	}

	return filter, nil
}
