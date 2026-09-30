package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// GetPrivateProfileUseCase answers the owner's profile query. Authentication
// happens at the transport boundary; this use case only validates that an
// account identifier was supplied.
type GetPrivateProfileUseCase struct {
	profiles ProfileQueryRepository
}

// NewGetPrivateProfileUseCase creates an instance of GetPrivateProfileUseCase.
func NewGetPrivateProfileUseCase(profiles ProfileQueryRepository) *GetPrivateProfileUseCase {
	return &GetPrivateProfileUseCase{profiles: profiles}
}

// Execute resolves the private projection owned by the account.
func (uc *GetPrivateProfileUseCase) Execute(ctx context.Context, accountID domain.AccountID) (*PrivateProfile, error) {
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	return uc.profiles.GetPrivateProfileByAccountID(ctx, accountID)
}
