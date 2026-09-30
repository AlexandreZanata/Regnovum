package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// CreateProfileCommand holds the parameters for creating the profile of an
// authenticated account.
type CreateProfileCommand struct {
	AccountID string
	Username  string
	Locale    string
}

// CreateProfileUseCase creates the public profile of an eligible (email
// verified) account. The profile and its first username history entry are
// persisted atomically, so a rejected username never leaves partial state.
type CreateProfileUseCase struct {
	profiles    ProfileRepository
	eligibility AccountEligibility
	policy      domain.UsernamePolicy
	clock       Clock
}

// NewCreateProfileUseCase creates an instance of CreateProfileUseCase.
func NewCreateProfileUseCase(
	profiles ProfileRepository,
	eligibility AccountEligibility,
	policy domain.UsernamePolicy,
	clock Clock,
) *CreateProfileUseCase {
	return &CreateProfileUseCase{
		profiles:    profiles,
		eligibility: eligibility,
		policy:      policy,
		clock:       clock,
	}
}

// Execute validates the requested handle and locale, asserts that the
// account is eligible to own a profile, applies the username policy and
// persists the profile with its audit trail. An empty locale falls back to
// the product default (pt-BR); unknown locales are rejected, never
// reflected.
func (uc *CreateProfileUseCase) Execute(ctx context.Context, cmd CreateProfileCommand) (*domain.Profile, error) {
	accountID := domain.AccountID(cmd.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	username, err := domain.ParseUsername(cmd.Username)
	if err != nil {
		return nil, err
	}

	locale := domain.DefaultLocale()
	if cmd.Locale != "" {
		locale, err = domain.ParseLocale(cmd.Locale)
		if err != nil {
			return nil, err
		}
	}

	if err := uc.eligibility.EnsureEligible(ctx, accountID); err != nil {
		return nil, err
	}

	now := uc.clock.Now()
	change, err := uc.policy.PlanChange(domain.Username{}, username, time.Time{}, now)
	if err != nil {
		return nil, err
	}

	return uc.profiles.CreateProfileWithUsernameHistory(ctx, accountID, change.Current, locale, change.ChangedAt)
}
