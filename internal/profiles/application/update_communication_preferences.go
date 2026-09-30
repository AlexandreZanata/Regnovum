package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// SetMarketingOptInCommand holds the parameters for changing the explicit
// marketing consent of the authenticated owner.
type SetMarketingOptInCommand struct {
	AccountID string
	OptIn     bool
}

// UpdateCommunicationPreferencesUseCase applies an explicit marketing
// opt-in/opt-out. The account must be eligible (active with verified email),
// the change is appended to the audit trail, and setting the current value
// again is an idempotent no-op that adds no audit noise.
type UpdateCommunicationPreferencesUseCase struct {
	preferences CommunicationPreferencesRepository
	eligibility AccountEligibility
	clock       Clock
}

// NewUpdateCommunicationPreferencesUseCase creates an instance of UpdateCommunicationPreferencesUseCase.
func NewUpdateCommunicationPreferencesUseCase(
	preferences CommunicationPreferencesRepository,
	eligibility AccountEligibility,
	clock Clock,
) *UpdateCommunicationPreferencesUseCase {
	return &UpdateCommunicationPreferencesUseCase{
		preferences: preferences,
		eligibility: eligibility,
		clock:       clock,
	}
}

// Execute sets the explicit marketing consent of the account.
func (uc *UpdateCommunicationPreferencesUseCase) Execute(ctx context.Context, cmd SetMarketingOptInCommand) (*CommunicationPreferences, error) {
	accountID := domain.AccountID(cmd.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	if err := uc.eligibility.EnsureEligible(ctx, accountID); err != nil {
		return nil, err
	}

	current, err := uc.preferences.PreferencesFor(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if current.MarketingOptIn == cmd.OptIn {
		return current, nil
	}

	return uc.preferences.SetMarketingOptIn(ctx, accountID, cmd.OptIn, uc.clock.Now())
}
