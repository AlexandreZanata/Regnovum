package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// CreateArenaRequest is a validated draft to persist.
type CreateArenaRequest struct {
	CreatorID domain.CreatorID
	Statement domain.Statement
	Context   domain.Context
	Category  domain.Category
	Language  domain.Language
}

// DraftUpdate replaces the mutable fields of a draft under an optimistic
// version check.
type DraftUpdate struct {
	Statement       domain.Statement
	Context         domain.Context
	Category        domain.Category
	Language        domain.Language
	ExpectedVersion int32
}

// ArenaRepository persists Arenas and their private drafts. Every mutation
// is scoped by the creator; the repository never exposes a foreign draft.
type ArenaRepository interface {
	// CreateArena stores a new draft and returns the reconstituted entity.
	CreateArena(ctx context.Context, request CreateArenaRequest) (*domain.Arena, error)

	// GetArenaForCreator returns the Arena owned by the creator, or
	// ErrArenaNotFound.
	GetArenaForCreator(ctx context.Context, arenaID domain.ArenaID, creatorID domain.CreatorID) (*domain.Arena, error)

	// ListArenaDraftsForCreator returns the creator's drafts, newest first.
	ListArenaDraftsForCreator(ctx context.Context, creatorID domain.CreatorID) ([]domain.Arena, error)

	// UpdateArenaDraft replaces the draft fields under the optimistic
	// version check. It returns ErrVersionConflict when the stored version
	// moved, ErrArenaNotFound when the draft is missing or foreign and
	// domain.ErrArenaNotDraft when the Arena already left the draft state.
	UpdateArenaDraft(ctx context.Context, arenaID domain.ArenaID, creatorID domain.CreatorID, update DraftUpdate) (*domain.Arena, error)

	// DeleteArenaDraft removes a draft owned by the creator. It returns
	// ErrArenaNotFound when the draft is missing or foreign and
	// domain.ErrArenaNotDraft when the Arena already left the draft state.
	DeleteArenaDraft(ctx context.Context, arenaID domain.ArenaID, creatorID domain.CreatorID) error

	// PublishArenaDraft performs the draft→published transition under the
	// optimistic version check. It returns ErrSlugConflict when the slug is
	// already taken, ErrArenaNotFound when the draft is missing or foreign,
	// domain.ErrArenaNotDraft when the Arena left the draft state and
	// ErrVersionConflict when the version moved.
	PublishArenaDraft(ctx context.Context, arenaID domain.ArenaID, creatorID domain.CreatorID, slug domain.Slug, publishedAt time.Time, expectedVersion int32) (*domain.Arena, error)

	// GetArenaByID returns any Arena by identifier; moderation is not scoped
	// by the creator. It returns ErrArenaNotFound when the id is unknown.
	GetArenaByID(ctx context.Context, arenaID domain.ArenaID) (*domain.Arena, error)

	// CloseArena performs the published→closed transition requested by the
	// creator under the optimistic version check.
	CloseArena(ctx context.Context, arenaID domain.ArenaID, creatorID domain.CreatorID, expectedVersion int32) (*domain.Arena, error)

	// RestrictArena applies the moderation restriction to a published or
	// closed Arena under the optimistic version check.
	RestrictArena(ctx context.Context, arenaID domain.ArenaID, expectedVersion int32) (*domain.Arena, error)

	// RemoveArena applies the moderation removal to a published, closed or
	// restricted Arena under the optimistic version check.
	RemoveArena(ctx context.Context, arenaID domain.ArenaID, expectedVersion int32) (*domain.Arena, error)
}
