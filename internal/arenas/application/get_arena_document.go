package application

import (
	"context"
	"errors"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// ArenaDocumentRepository is the consumer-oriented port of the public Arena
// document (P08-T08). Besides resolving public addresses, it answers the
// status of a slug that is no longer publicly readable, so removed Arenas
// can be distinguished from values that never existed.
type ArenaDocumentRepository interface {
	// GetPublicArenaBySlug resolves a public Arena address; drafts and
	// removed Arenas are not found.
	GetPublicArenaBySlug(ctx context.Context, slug domain.Slug) (*domain.Arena, error)

	// GetArenaStatusBySlug returns the stored status of the Arena holding
	// the slug. Draft arenas never hold a slug, so observable statuses are
	// the published ones plus removed.
	GetArenaStatusBySlug(ctx context.Context, slug domain.Slug) (domain.ArenaStatus, error)
}

// GetArenaDocumentUseCase resolves the public document addressed by slug for
// the SEO surface: drafts are never addressable, unknown values are not
// found and removed Arenas answer ErrArenaGone (410) instead of 404.
type GetArenaDocumentUseCase struct {
	arenas ArenaDocumentRepository
}

// NewGetArenaDocumentUseCase creates an instance of GetArenaDocumentUseCase.
func NewGetArenaDocumentUseCase(arenas ArenaDocumentRepository) *GetArenaDocumentUseCase {
	return &GetArenaDocumentUseCase{arenas: arenas}
}

// Execute resolves the public Arena or explains why no document exists.
func (uc *GetArenaDocumentUseCase) Execute(ctx context.Context, rawSlug string) (*domain.Arena, error) {
	slug, err := domain.ParseSlug(rawSlug)
	if err != nil {
		return nil, ErrArenaNotFound
	}

	arena, err := uc.arenas.GetPublicArenaBySlug(ctx, slug)
	if err == nil {
		return arena, nil
	}
	if !errors.Is(err, ErrArenaNotFound) {
		return nil, err
	}

	// Not publicly readable: distinguish removed (gone) from never existed.
	status, statusErr := uc.arenas.GetArenaStatusBySlug(ctx, slug)
	if statusErr != nil {
		if errors.Is(statusErr, ErrArenaNotFound) {
			return nil, ErrArenaNotFound
		}
		return nil, statusErr
	}
	if status == domain.ArenaStatusRemoved {
		return nil, ErrArenaGone
	}
	return nil, ErrArenaNotFound
}
