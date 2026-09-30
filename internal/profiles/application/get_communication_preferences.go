package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// GetCommunicationPreferencesUseCase answers the preferences query for the
// authenticated owner and implements the read path notifications use.
type GetCommunicationPreferencesUseCase struct {
	preferences CommunicationPreferencesReader
}

// NewGetCommunicationPreferencesUseCase creates an instance of GetCommunicationPreferencesUseCase.
func NewGetCommunicationPreferencesUseCase(preferences CommunicationPreferencesReader) *GetCommunicationPreferencesUseCase {
	return &GetCommunicationPreferencesUseCase{preferences: preferences}
}

// Execute returns the explicit preferences of the account. Accounts with no
// stored opt-in row read as marketing opt-in false.
func (uc *GetCommunicationPreferencesUseCase) Execute(ctx context.Context, accountID domain.AccountID) (*CommunicationPreferences, error) {
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	return uc.preferences.PreferencesFor(ctx, accountID)
}
