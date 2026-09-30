package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// GetPublicArenaUseCase resolves a public Arena by its slug. Drafts are never
// addressable and removed Arenas are not found: every unresolvable value is
// simply ErrArenaNotFound, so unknown inputs are never reflected back.
type GetPublicArenaUseCase struct {
	arenas ArenaFeedRepository
}

// NewGetPublicArenaUseCase creates an instance of GetPublicArenaUseCase.
func NewGetPublicArenaUseCase(arenas ArenaFeedRepository) *GetPublicArenaUseCase {
	return &GetPublicArenaUseCase{arenas: arenas}
}

// Execute resolves the public Arena.
func (uc *GetPublicArenaUseCase) Execute(ctx context.Context, rawSlug string) (*domain.Arena, error) {
	slug, err := domain.ParseSlug(rawSlug)
	if err != nil {
		return nil, ErrArenaNotFound
	}
	return uc.arenas.GetPublicArenaBySlug(ctx, slug)
}
