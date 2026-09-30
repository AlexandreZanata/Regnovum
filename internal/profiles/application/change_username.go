package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// ChangeUsernameCommand holds the parameters for claiming a new username.
type ChangeUsernameCommand struct {
	AccountID string
	Username  string
}

// ChangeUsernameUseCase applies the username policy (reserved list,
// immutability of the handle and configured cooldown) to a profile change
// and persists the profile update together with its audit entry.
type ChangeUsernameUseCase struct {
	profiles    ProfileRepository
	eligibility AccountEligibility
	policy      domain.UsernamePolicy
	clock       Clock
}

// NewChangeUsernameUseCase creates an instance of ChangeUsernameUseCase.
func NewChangeUsernameUseCase(
	profiles ProfileRepository,
	eligibility AccountEligibility,
	policy domain.UsernamePolicy,
	clock Clock,
) *ChangeUsernameUseCase {
	return &ChangeUsernameUseCase{
		profiles:    profiles,
		eligibility: eligibility,
		policy:      policy,
		clock:       clock,
	}
}

// Execute loads the current profile, evaluates the requested username
// against the policy and applies the change atomically.
func (uc *ChangeUsernameUseCase) Execute(ctx context.Context, cmd ChangeUsernameCommand) (*domain.Profile, error) {
	accountID := domain.AccountID(cmd.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	username, err := domain.ParseUsername(cmd.Username)
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

	lastChangedAt, err := uc.profiles.LastUsernameChangeAt(ctx, accountID)
	if err != nil {
		return nil, err
	}

	change, err := uc.policy.PlanChange(profile.Username(), username, lastChangedAt, uc.clock.Now())
	if err != nil {
		return nil, err
	}

	return uc.profiles.ApplyUsernameChange(ctx, accountID, change)
}
