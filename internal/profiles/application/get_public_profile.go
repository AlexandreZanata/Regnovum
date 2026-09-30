package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// GetPublicProfileUseCase answers the public profile query. Any username that
// cannot name an existing profile — malformed, unknown or differently cased —
// resolves to ErrProfileNotFound: unknown values are never reflected back.
type GetPublicProfileUseCase struct {
	profiles ProfileQueryRepository
}

// NewGetPublicProfileUseCase creates an instance of GetPublicProfileUseCase.
func NewGetPublicProfileUseCase(profiles ProfileQueryRepository) *GetPublicProfileUseCase {
	return &GetPublicProfileUseCase{profiles: profiles}
}

// Execute resolves a public profile by its username.
func (uc *GetPublicProfileUseCase) Execute(ctx context.Context, username string) (*PublicProfile, error) {
	parsed, err := domain.ParseUsername(username)
	if err != nil {
		return nil, ErrProfileNotFound
	}
	return uc.profiles.GetPublicProfileByUsername(ctx, parsed.Normalized())
}
