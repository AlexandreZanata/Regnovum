package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// ChangeTimezoneCommand holds the parameters for updating the optional IANA
// timezone preference. An empty timezone clears the preference.
type ChangeTimezoneCommand struct {
	AccountID string
	Timezone  string
}

// ChangeTimezoneUseCase validates and persists the optional timezone
// preference. The value is validated only when informed; clearing it (empty
// input) is always allowed. Timezone never changes the interface locale or
// any Arena content language.
type ChangeTimezoneUseCase struct {
	profiles    ProfileRepository
	eligibility AccountEligibility
	clock       Clock
}

// NewChangeTimezoneUseCase creates an instance of ChangeTimezoneUseCase.
func NewChangeTimezoneUseCase(
	profiles ProfileRepository,
	eligibility AccountEligibility,
	clock Clock,
) *ChangeTimezoneUseCase {
	return &ChangeTimezoneUseCase{
		profiles:    profiles,
		eligibility: eligibility,
		clock:       clock,
	}
}

// Execute parses and applies the timezone preference. Re-selecting the
// current value is an idempotent no-op.
func (uc *ChangeTimezoneUseCase) Execute(ctx context.Context, cmd ChangeTimezoneCommand) (*domain.Profile, error) {
	accountID := domain.AccountID(cmd.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	timezone, err := domain.ParseTimezone(cmd.Timezone)
	if err != nil {
		return nil, err
	}

	if err := uc.eligibility.EnsureEligible(ctx, accountID); err != nil {
		return nil, err
	}

	profile, err := uc.profiles.GetProfileByAccountID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if profile.Timezone().Equals(timezone) {
		return profile, nil
	}

	return uc.profiles.UpdateProfileTimezone(ctx, accountID, timezone, uc.clock.Now())
}
