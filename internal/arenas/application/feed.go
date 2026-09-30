package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// Feed pagination bounds fixed by the API conventions (limit defaults to
// 20, maximum 100).
const (
	DefaultFeedLimit = 20
	MaxFeedLimit     = 100
)

// ArenaFeedFilter carries the optional public feed filters. A nil filter is
// absent; the status filter only accepts publicly visible statuses.
type ArenaFeedFilter struct {
	Language *domain.Language
	Category *domain.Category
	Status   *domain.ArenaStatus
}

// FeedPosition is the decoded keyset cursor position: the last Arena already
// delivered to the caller.
type FeedPosition struct {
	PublishedAt time.Time
	ArenaID     string
}

// ArenaFeedPage is one page of the public feed plus the cursor of the next
// page (empty when the page is the last one).
type ArenaFeedPage struct {
	Arenas     []domain.Arena
	NextCursor string
}

// ArenaFeedRepository exposes the public reads. Drafts and removed Arenas
// are never candidates, independently of the filters.
type ArenaFeedRepository interface {
	// ListPublicArenas returns up to limit Arenas strictly older than the
	// cursor position, newest first.
	ListPublicArenas(ctx context.Context, filter ArenaFeedFilter, after *FeedPosition, limit int) ([]domain.Arena, error)

	// GetPublicArenaBySlug resolves a public Arena address; drafts and
	// removed Arenas are not found.
	GetPublicArenaBySlug(ctx context.Context, slug domain.Slug) (*domain.Arena, error)
}
