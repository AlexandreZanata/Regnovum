package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// ChangeLocaleCommand holds the parameters for updating the interface
// locale preference of a profile.
type ChangeLocaleCommand struct {
	AccountID string
	Locale    string
}

// ChangeLocaleUseCase canonicalizes and persists the interface locale
// preference. It never touches content_language or any Arena content: the
// interface locale is an independent preference (I18N_STANDARD.md §1).
type ChangeLocaleUseCase struct {
	profiles    ProfileRepository
	eligibility AccountEligibility
	clock       Clock
}

// NewChangeLocaleUseCase creates an instance of ChangeLocaleUseCase.
func NewChangeLocaleUseCase(
	profiles ProfileRepository,
	eligibility AccountEligibility,
	clock Clock,
) *ChangeLocaleUseCase {
	return &ChangeLocaleUseCase{
		profiles:    profiles,
		eligibility: eligibility,
		clock:       clock,
	}
}

// Execute parses the requested locale against the allowlist and updates the
// profile. Re-selecting the current locale is an idempotent no-op.
func (uc *ChangeLocaleUseCase) Execute(ctx context.Context, cmd ChangeLocaleCommand) (*domain.Profile, error) {
	accountID := domain.AccountID(cmd.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	locale, err := domain.ParseLocale(cmd.Locale)
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
	if profile.Locale().Equals(locale) {
		return profile, nil
	}

	return uc.profiles.UpdateProfileLocale(ctx, accountID, locale, uc.clock.Now())
}
