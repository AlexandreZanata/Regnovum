package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

func TestChangeTimezoneUseCase(t *testing.T) {
	const accountID domain.AccountID = "018f6b2a-0000-7000-8000-000000000060"
	createdAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	newFixture := func(t *testing.T) (*inMemoryProfileRepo, *stubEligibility, *fakeClock) {
		t.Helper()
		repo, eligibility, clock := newUseCaseFixture(t, accountID)
		clock.Set(createdAt)
		create := application.NewCreateProfileUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)
		if _, err := create.Execute(context.Background(), application.CreateProfileCommand{
			AccountID: accountID.String(),
			Username:  "TimezoneUser",
		}); err != nil {
			t.Fatalf("seed profile: %v", err)
		}
		return repo, eligibility, clock
	}

	t.Run("persists an informed IANA timezone", func(t *testing.T) {
		repo, eligibility, clock := newFixture(t)
		useCase := application.NewChangeTimezoneUseCase(repo, eligibility, clock)

		changedAt := createdAt.Add(2 * time.Hour)
		clock.Set(changedAt)
		profile, err := useCase.Execute(context.Background(), application.ChangeTimezoneCommand{
			AccountID: accountID.String(),
			Timezone:  "America/Sao_Paulo",
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if profile.Timezone().String() != "America/Sao_Paulo" {
			t.Errorf("Timezone = %q, want America/Sao_Paulo", profile.Timezone())
		}
		if repo.timezoneUpdates != 1 {
			t.Errorf("storage updates = %d, want 1", repo.timezoneUpdates)
		}
		if !profile.UpdatedAt().Equal(changedAt) {
			t.Errorf("UpdatedAt = %v, want %v", profile.UpdatedAt(), changedAt)
		}
		if profile.Locale().String() != domain.LocaleBrazilianPortuguese {
			t.Errorf("locale changed with timezone: %q", profile.Locale())
		}
	})

	t.Run("empty input clears the optional timezone", func(t *testing.T) {
		repo, eligibility, clock := newFixture(t)
		useCase := application.NewChangeTimezoneUseCase(repo, eligibility, clock)

		if _, err := useCase.Execute(context.Background(), application.ChangeTimezoneCommand{
			AccountID: accountID.String(),
			Timezone:  "Europe/Lisbon",
		}); err != nil {
			t.Fatalf("set timezone: %v", err)
		}
		profile, err := useCase.Execute(context.Background(), application.ChangeTimezoneCommand{
			AccountID: accountID.String(),
			Timezone:  "",
		})
		if err != nil {
			t.Fatalf("clear timezone: %v", err)
		}
		if !profile.Timezone().IsZero() {
			t.Errorf("Timezone = %q, want unset", profile.Timezone())
		}
	})

	t.Run("repeating the current value is a no-op", func(t *testing.T) {
		repo, eligibility, clock := newFixture(t)
		useCase := application.NewChangeTimezoneUseCase(repo, eligibility, clock)

		if _, err := useCase.Execute(context.Background(), application.ChangeTimezoneCommand{
			AccountID: accountID.String(),
			Timezone:  "Asia/Tokyo",
		}); err != nil {
			t.Fatalf("first Execute() error = %v", err)
		}
		if _, err := useCase.Execute(context.Background(), application.ChangeTimezoneCommand{
			AccountID: accountID.String(),
			Timezone:  "Asia/Tokyo",
		}); err != nil {
			t.Fatalf("second Execute() error = %v", err)
		}
		if repo.timezoneUpdates != 1 {
			t.Errorf("storage updates = %d, want 1 for an unchanged value", repo.timezoneUpdates)
		}
	})

	t.Run("invalid timezone and authorization failures never write", func(t *testing.T) {
		repo, eligibility, clock := newFixture(t)
		useCase := application.NewChangeTimezoneUseCase(repo, eligibility, clock)

		_, err := useCase.Execute(context.Background(), application.ChangeTimezoneCommand{
			AccountID: accountID.String(),
			Timezone:  "Mars/Phobos",
		})
		if !errors.Is(err, domain.ErrInvalidTimezone) {
			t.Fatalf("invalid timezone error = %v, want ErrInvalidTimezone", err)
		}

		eligibility.eligible[accountID] = false
		_, err = useCase.Execute(context.Background(), application.ChangeTimezoneCommand{
			AccountID: accountID.String(),
			Timezone:  "UTC",
		})
		if !errors.Is(err, application.ErrAccountNotEligible) {
			t.Fatalf("ineligible account error = %v, want ErrAccountNotEligible", err)
		}

		otherID := domain.AccountID("018f6b2a-0000-7000-8000-000000000061")
		eligibility.eligible[otherID] = true
		_, err = useCase.Execute(context.Background(), application.ChangeTimezoneCommand{
			AccountID: otherID.String(),
			Timezone:  "UTC",
		})
		if !errors.Is(err, application.ErrProfileNotFound) {
			t.Fatalf("missing profile error = %v, want ErrProfileNotFound", err)
		}

		_, err = useCase.Execute(context.Background(), application.ChangeTimezoneCommand{
			AccountID: "",
			Timezone:  "UTC",
		})
		if !errors.Is(err, domain.ErrEmptyAccountID) {
			t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
		}

		if repo.timezoneUpdates != 0 {
			t.Fatalf("failed updates wrote state: updates=%d, want 0", repo.timezoneUpdates)
		}
	})
}
