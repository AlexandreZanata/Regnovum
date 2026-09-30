package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

type stubEligibility struct {
	eligible map[domain.AccountID]bool
	err      error
	calls    int
}

func (s *stubEligibility) EnsureEligible(_ context.Context, accountID domain.AccountID) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	if !s.eligible[accountID] {
		return application.ErrAccountNotEligible
	}
	return nil
}

type inMemoryProfileRepo struct {
	mu              sync.Mutex
	profiles        map[domain.AccountID]*domain.Profile
	history         map[domain.AccountID][]domain.UsernameChange
	localeUpdates   int
	timezoneUpdates int
	createCalls     int
}

func newInMemoryProfileRepo() *inMemoryProfileRepo {
	return &inMemoryProfileRepo{
		profiles: make(map[domain.AccountID]*domain.Profile),
		history:  make(map[domain.AccountID][]domain.UsernameChange),
	}
}

func (r *inMemoryProfileRepo) CreateProfileWithUsernameHistory(
	_ context.Context,
	accountID domain.AccountID,
	username domain.Username,
	locale domain.Locale,
	changedAt time.Time,
) (*domain.Profile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.createCalls++
	if _, exists := r.profiles[accountID]; exists {
		return nil, application.ErrProfileAlreadyExists
	}
	for _, profile := range r.profiles {
		if profile.Username().Equals(username) {
			return nil, application.ErrUsernameTaken
		}
	}

	profile, err := domain.NewProfile(accountID, username, locale, changedAt)
	if err != nil {
		return nil, err
	}
	r.profiles[accountID] = profile
	r.history[accountID] = append(r.history[accountID], domain.UsernameChange{
		Current:   username,
		ChangedAt: changedAt,
	})
	return profile, nil
}

func (r *inMemoryProfileRepo) GetProfileByAccountID(_ context.Context, accountID domain.AccountID) (*domain.Profile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	profile, exists := r.profiles[accountID]
	if !exists {
		return nil, application.ErrProfileNotFound
	}
	return profile, nil
}

func (r *inMemoryProfileRepo) LastUsernameChangeAt(_ context.Context, accountID domain.AccountID) (time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entries := r.history[accountID]
	if len(entries) == 0 {
		return time.Time{}, nil
	}
	latest := entries[0].ChangedAt
	for _, entry := range entries[1:] {
		if entry.ChangedAt.After(latest) {
			latest = entry.ChangedAt
		}
	}
	return latest, nil
}

func (r *inMemoryProfileRepo) ApplyUsernameChange(_ context.Context, accountID domain.AccountID, change domain.UsernameChange) (*domain.Profile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	profile, exists := r.profiles[accountID]
	if !exists {
		return nil, application.ErrProfileNotFound
	}
	for otherID, other := range r.profiles {
		if otherID != accountID && other.Username().Equals(change.Current) {
			return nil, application.ErrUsernameTaken
		}
	}
	if err := profile.ChangeUsername(change); err != nil {
		return nil, err
	}
	r.history[accountID] = append(r.history[accountID], change)
	return profile, nil
}

func (r *inMemoryProfileRepo) UpdateProfileLocale(_ context.Context, accountID domain.AccountID, locale domain.Locale, updatedAt time.Time) (*domain.Profile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	profile, exists := r.profiles[accountID]
	if !exists {
		return nil, application.ErrProfileNotFound
	}
	r.localeUpdates++
	if err := profile.ChangeLocale(locale, updatedAt); err != nil {
		return nil, err
	}
	return profile, nil
}

func (r *inMemoryProfileRepo) UpdateProfileTimezone(_ context.Context, accountID domain.AccountID, timezone domain.Timezone, updatedAt time.Time) (*domain.Profile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	profile, exists := r.profiles[accountID]
	if !exists {
		return nil, application.ErrProfileNotFound
	}
	r.timezoneUpdates++
	profile.ChangeTimezone(timezone, updatedAt)
	return profile, nil
}

func (r *inMemoryProfileRepo) historyLength(accountID domain.AccountID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.history[accountID])
}

func newUseCaseFixture(t *testing.T, accountID domain.AccountID) (*inMemoryProfileRepo, *stubEligibility, *fakeClock) {
	t.Helper()
	repo := newInMemoryProfileRepo()
	eligibility := &stubEligibility{eligible: map[domain.AccountID]bool{accountID: true}}
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	return repo, eligibility, clock
}

func TestCreateProfileUseCase(t *testing.T) {
	const accountID domain.AccountID = "018f6b2a-0000-7000-8000-000000000001"
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	t.Run("creates profile and first audit entry", func(t *testing.T) {
		repo, eligibility, clock := newUseCaseFixture(t, accountID)
		clock.Set(now)
		useCase := application.NewCreateProfileUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)

		profile, err := useCase.Execute(context.Background(), application.CreateProfileCommand{
			AccountID: accountID.String(),
			Username:  "ArenaUser",
			Locale:    "en-us",
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if profile.Username().String() != "ArenaUser" || profile.Username().Normalized() != "arenauser" {
			t.Errorf("username = %q/%q, want ArenaUser/arenauser", profile.Username().String(), profile.Username().Normalized())
		}
		if profile.Locale().String() != domain.LocaleAmericanEnglish {
			t.Errorf("locale = %q, want %q", profile.Locale().String(), domain.LocaleAmericanEnglish)
		}
		if repo.historyLength(accountID) != 1 {
			t.Errorf("history entries = %d, want 1", repo.historyLength(accountID))
		}
		if !profile.CreatedAt().Equal(now) || !profile.UpdatedAt().Equal(now) {
			t.Errorf("timestamps = %v/%v, want %v", profile.CreatedAt(), profile.UpdatedAt(), now)
		}
		lastChange, err := repo.LastUsernameChangeAt(context.Background(), accountID)
		if err != nil {
			t.Fatalf("LastUsernameChangeAt() error = %v", err)
		}
		if !lastChange.Equal(now) {
			t.Errorf("last change = %v, want %v", lastChange, now)
		}
	})

	t.Run("empty locale falls back to product default", func(t *testing.T) {
		repo, eligibility, clock := newUseCaseFixture(t, accountID)
		useCase := application.NewCreateProfileUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)

		profile, err := useCase.Execute(context.Background(), application.CreateProfileCommand{
			AccountID: accountID.String(),
			Username:  "DefaultLocale",
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if profile.Locale().String() != domain.LocaleBrazilianPortuguese {
			t.Errorf("locale = %q, want %q", profile.Locale().String(), domain.LocaleBrazilianPortuguese)
		}
	})

	t.Run("rejects ineligible account without touching storage", func(t *testing.T) {
		repo, eligibility, clock := newUseCaseFixture(t, accountID)
		eligibility.eligible[accountID] = false
		useCase := application.NewCreateProfileUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)

		_, err := useCase.Execute(context.Background(), application.CreateProfileCommand{
			AccountID: accountID.String(),
			Username:  "PendingUser",
		})
		if !errors.Is(err, application.ErrAccountNotEligible) {
			t.Fatalf("Execute() error = %v, want ErrAccountNotEligible", err)
		}
		if repo.createCalls != 0 {
			t.Errorf("storage was called %d times for an ineligible account", repo.createCalls)
		}
	})

	t.Run("validation errors", func(t *testing.T) {
		tests := []struct {
			name string
			cmd  application.CreateProfileCommand
			want error
		}{
			{name: "empty account id", cmd: application.CreateProfileCommand{Username: "ArenaUser"}, want: domain.ErrEmptyAccountID},
			{name: "too short", cmd: application.CreateProfileCommand{AccountID: accountID.String(), Username: "ab"}, want: domain.ErrUsernameTooShort},
			{name: "non-ascii", cmd: application.CreateProfileCommand{AccountID: accountID.String(), Username: "árvore"}, want: domain.ErrUsernameNonASCII},
			{name: "reserved", cmd: application.CreateProfileCommand{AccountID: accountID.String(), Username: "Admin"}, want: domain.ErrUsernameReserved},
			{name: "unsupported locale", cmd: application.CreateProfileCommand{AccountID: accountID.String(), Username: "ArenaUser", Locale: "fr-FR"}, want: domain.ErrUnsupportedLocale},
			{name: "malformed locale", cmd: application.CreateProfileCommand{AccountID: accountID.String(), Username: "ArenaUser", Locale: "pt_BR"}, want: domain.ErrInvalidLocale},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				repo, eligibility, clock := newUseCaseFixture(t, accountID)
				useCase := application.NewCreateProfileUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)

				_, err := useCase.Execute(context.Background(), tc.cmd)
				if !errors.Is(err, tc.want) {
					t.Fatalf("Execute() error = %v, want %v", err, tc.want)
				}
				if repo.createCalls != 0 {
					t.Errorf("storage was called %d times on validation error", repo.createCalls)
				}
			})
		}
	})

	t.Run("propagates duplicate and taken errors", func(t *testing.T) {
		repo, eligibility, clock := newUseCaseFixture(t, accountID)
		useCase := application.NewCreateProfileUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)

		cmd := application.CreateProfileCommand{AccountID: accountID.String(), Username: "ArenaUser"}
		if _, err := useCase.Execute(context.Background(), cmd); err != nil {
			t.Fatalf("first Execute() error = %v", err)
		}
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, application.ErrProfileAlreadyExists) {
			t.Fatalf("duplicate Execute() error = %v, want ErrProfileAlreadyExists", err)
		}

		otherID := domain.AccountID("018f6b2a-0000-7000-8000-000000000002")
		eligibility.eligible[otherID] = true
		_, err := useCase.Execute(context.Background(), application.CreateProfileCommand{
			AccountID: otherID.String(),
			Username:  "ARENAUSER",
		})
		if !errors.Is(err, application.ErrUsernameTaken) {
			t.Fatalf("taken Execute() error = %v, want ErrUsernameTaken", err)
		}
	})
}

func TestChangeUsernameUseCase(t *testing.T) {
	const accountID domain.AccountID = "018f6b2a-0000-7000-8000-000000000010"
	createdAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	newFixture := func(t *testing.T) (*inMemoryProfileRepo, *stubEligibility, *fakeClock) {
		t.Helper()
		repo, eligibility, clock := newUseCaseFixture(t, accountID)
		clock.Set(createdAt)
		create := application.NewCreateProfileUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)
		if _, err := create.Execute(context.Background(), application.CreateProfileCommand{
			AccountID: accountID.String(),
			Username:  "FirstChoice",
		}); err != nil {
			t.Fatalf("seed profile: %v", err)
		}
		return repo, eligibility, clock
	}

	t.Run("applies change after cooldown and appends audit", func(t *testing.T) {
		repo, eligibility, clock := newFixture(t)
		useCase := application.NewChangeUsernameUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)

		changedAt := createdAt.Add(31 * 24 * time.Hour)
		clock.Set(changedAt)

		profile, err := useCase.Execute(context.Background(), application.ChangeUsernameCommand{
			AccountID: accountID.String(),
			Username:  "SecondChoice",
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if profile.Username().Normalized() != "secondchoice" {
			t.Errorf("username = %q, want secondchoice", profile.Username().Normalized())
		}
		if !profile.UpdatedAt().Equal(changedAt) {
			t.Errorf("UpdatedAt = %v, want %v", profile.UpdatedAt(), changedAt)
		}
		if repo.historyLength(accountID) != 2 {
			t.Errorf("history entries = %d, want 2", repo.historyLength(accountID))
		}
		lastChange, err := repo.LastUsernameChangeAt(context.Background(), accountID)
		if err != nil {
			t.Fatalf("LastUsernameChangeAt() error = %v", err)
		}
		if !lastChange.Equal(changedAt) {
			t.Errorf("last change = %v, want %v", lastChange, changedAt)
		}
	})

	t.Run("rejects change inside cooldown without partial state", func(t *testing.T) {
		repo, eligibility, clock := newFixture(t)
		useCase := application.NewChangeUsernameUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)

		clock.Set(createdAt.Add(10 * 24 * time.Hour))
		_, err := useCase.Execute(context.Background(), application.ChangeUsernameCommand{
			AccountID: accountID.String(),
			Username:  "SecondChoice",
		})
		if !errors.Is(err, domain.ErrUsernameCooldown) {
			t.Fatalf("Execute() error = %v, want ErrUsernameCooldown", err)
		}
		profile, err := repo.GetProfileByAccountID(context.Background(), accountID)
		if err != nil {
			t.Fatalf("GetProfileByAccountID() error = %v", err)
		}
		if profile.Username().Normalized() != "firstchoice" {
			t.Errorf("username changed during cooldown: %q", profile.Username().Normalized())
		}
		if repo.historyLength(accountID) != 1 {
			t.Errorf("history entries = %d, want 1", repo.historyLength(accountID))
		}
	})

	t.Run("rejects unchanged and reserved handles", func(t *testing.T) {
		repo, eligibility, clock := newFixture(t)
		useCase := application.NewChangeUsernameUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)
		clock.Set(createdAt.Add(31 * 24 * time.Hour))

		_, err := useCase.Execute(context.Background(), application.ChangeUsernameCommand{
			AccountID: accountID.String(),
			Username:  "FIRSTCHOICE",
		})
		if !errors.Is(err, domain.ErrUsernameUnchanged) {
			t.Fatalf("case-only change error = %v, want ErrUsernameUnchanged", err)
		}

		_, err = useCase.Execute(context.Background(), application.ChangeUsernameCommand{
			AccountID: accountID.String(),
			Username:  "Admin",
		})
		if !errors.Is(err, domain.ErrUsernameReserved) {
			t.Fatalf("reserved change error = %v, want ErrUsernameReserved", err)
		}
		if repo.historyLength(accountID) != 1 {
			t.Errorf("history entries = %d, want 1", repo.historyLength(accountID))
		}
	})

	t.Run("rejects ineligible account and missing profile", func(t *testing.T) {
		repo, eligibility, clock := newFixture(t)
		useCase := application.NewChangeUsernameUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)
		clock.Set(createdAt.Add(31 * 24 * time.Hour))

		eligibility.eligible[accountID] = false
		_, err := useCase.Execute(context.Background(), application.ChangeUsernameCommand{
			AccountID: accountID.String(),
			Username:  "SecondChoice",
		})
		if !errors.Is(err, application.ErrAccountNotEligible) {
			t.Fatalf("suspended account error = %v, want ErrAccountNotEligible", err)
		}

		eligibility.eligible[accountID] = true
		otherID := domain.AccountID("018f6b2a-0000-7000-8000-000000000011")
		eligibility.eligible[otherID] = true
		_, err = useCase.Execute(context.Background(), application.ChangeUsernameCommand{
			AccountID: otherID.String(),
			Username:  "SecondChoice",
		})
		if !errors.Is(err, application.ErrProfileNotFound) {
			t.Fatalf("missing profile error = %v, want ErrProfileNotFound", err)
		}
	})
}

func TestChangeLocaleUseCase(t *testing.T) {
	const accountID domain.AccountID = "018f6b2a-0000-7000-8000-000000000020"
	createdAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	newFixture := func(t *testing.T) (*inMemoryProfileRepo, *stubEligibility, *fakeClock) {
		t.Helper()
		repo, eligibility, clock := newUseCaseFixture(t, accountID)
		clock.Set(createdAt)
		create := application.NewCreateProfileUseCase(repo, eligibility, domain.DefaultUsernamePolicy(), clock)
		if _, err := create.Execute(context.Background(), application.CreateProfileCommand{
			AccountID: accountID.String(),
			Username:  "LocaleUser",
		}); err != nil {
			t.Fatalf("seed profile: %v", err)
		}
		return repo, eligibility, clock
	}

	t.Run("canonicalizes and persists the locale without touching username", func(t *testing.T) {
		repo, eligibility, clock := newFixture(t)
		useCase := application.NewChangeLocaleUseCase(repo, eligibility, clock)

		changedAt := createdAt.Add(2 * time.Hour)
		clock.Set(changedAt)
		profile, err := useCase.Execute(context.Background(), application.ChangeLocaleCommand{
			AccountID: accountID.String(),
			Locale:    "EN-us",
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if profile.Locale().String() != domain.LocaleAmericanEnglish {
			t.Errorf("locale = %q, want %q", profile.Locale().String(), domain.LocaleAmericanEnglish)
		}
		if profile.Username().Normalized() != "localeuser" {
			t.Errorf("username = %q, want localeuser", profile.Username().Normalized())
		}
		if !profile.UpdatedAt().Equal(changedAt) {
			t.Errorf("UpdatedAt = %v, want %v", profile.UpdatedAt(), changedAt)
		}
		if repo.localeUpdates != 1 {
			t.Errorf("storage updates = %d, want 1", repo.localeUpdates)
		}
		if repo.historyLength(accountID) != 1 {
			t.Errorf("history entries = %d, want 1 (locale change is not a username change)", repo.historyLength(accountID))
		}
	})

	t.Run("re-selecting the current locale is a no-op", func(t *testing.T) {
		repo, eligibility, clock := newFixture(t)
		useCase := application.NewChangeLocaleUseCase(repo, eligibility, clock)

		profile, err := useCase.Execute(context.Background(), application.ChangeLocaleCommand{
			AccountID: accountID.String(),
			Locale:    "pt-br",
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if profile.Locale().String() != domain.LocaleBrazilianPortuguese {
			t.Errorf("locale = %q, want %q", profile.Locale().String(), domain.LocaleBrazilianPortuguese)
		}
		if repo.localeUpdates != 0 {
			t.Errorf("storage updates = %d, want 0 for idempotent change", repo.localeUpdates)
		}
	})

	t.Run("rejects invalid locales and ineligible accounts", func(t *testing.T) {
		repo, eligibility, clock := newFixture(t)
		useCase := application.NewChangeLocaleUseCase(repo, eligibility, clock)

		for _, tc := range []struct {
			locale string
			want   error
		}{
			{locale: "", want: domain.ErrEmptyLocale},
			{locale: "fr-FR", want: domain.ErrUnsupportedLocale},
			{locale: "pt_BR", want: domain.ErrInvalidLocale},
		} {
			_, err := useCase.Execute(context.Background(), application.ChangeLocaleCommand{
				AccountID: accountID.String(),
				Locale:    tc.locale,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("locale %q error = %v, want %v", tc.locale, err, tc.want)
			}
		}
		if repo.localeUpdates != 0 {
			t.Errorf("storage updates = %d, want 0 on validation errors", repo.localeUpdates)
		}

		eligibility.eligible[accountID] = false
		_, err := useCase.Execute(context.Background(), application.ChangeLocaleCommand{
			AccountID: accountID.String(),
			Locale:    "en-US",
		})
		if !errors.Is(err, application.ErrAccountNotEligible) {
			t.Fatalf("ineligible account error = %v, want ErrAccountNotEligible", err)
		}

		otherID := domain.AccountID("018f6b2a-0000-7000-8000-000000000021")
		eligibility.eligible[otherID] = true
		_, err = useCase.Execute(context.Background(), application.ChangeLocaleCommand{
			AccountID: otherID.String(),
			Locale:    "en-US",
		})
		if !errors.Is(err, application.ErrProfileNotFound) {
			t.Fatalf("missing profile error = %v, want ErrProfileNotFound", err)
		}
	})
}
