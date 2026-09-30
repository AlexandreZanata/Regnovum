package application_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

type preferenceChange struct {
	optIn     bool
	changedAt time.Time
}

type inMemoryPreferencesRepo struct {
	mu        sync.Mutex
	profiles  map[domain.AccountID]domain.Locale
	optIn     map[domain.AccountID]bool
	history   map[domain.AccountID][]preferenceChange
	setCalls  int
	readCalls int
}

func newInMemoryPreferencesRepo() *inMemoryPreferencesRepo {
	return &inMemoryPreferencesRepo{
		profiles: make(map[domain.AccountID]domain.Locale),
		optIn:    make(map[domain.AccountID]bool),
		history:  make(map[domain.AccountID][]preferenceChange),
	}
}

func (r *inMemoryPreferencesRepo) PreferencesFor(_ context.Context, accountID domain.AccountID) (*application.CommunicationPreferences, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.readCalls++
	locale, ok := r.profiles[accountID]
	if !ok {
		return nil, application.ErrProfileNotFound
	}
	return &application.CommunicationPreferences{
		AccountID:       accountID,
		InterfaceLocale: locale,
		MarketingOptIn:  r.optIn[accountID],
	}, nil
}

func (r *inMemoryPreferencesRepo) SetMarketingOptIn(_ context.Context, accountID domain.AccountID, optIn bool, changedAt time.Time) (*application.CommunicationPreferences, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.setCalls++
	locale, ok := r.profiles[accountID]
	if !ok {
		return nil, application.ErrProfileNotFound
	}
	r.optIn[accountID] = optIn
	r.history[accountID] = append(r.history[accountID], preferenceChange{optIn: optIn, changedAt: changedAt})
	return &application.CommunicationPreferences{
		AccountID:       accountID,
		InterfaceLocale: locale,
		MarketingOptIn:  optIn,
	}, nil
}

func (r *inMemoryPreferencesRepo) historyLength(accountID domain.AccountID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.history[accountID])
}

func newPreferencesFixture(t *testing.T, accountID domain.AccountID) (*inMemoryPreferencesRepo, *stubEligibility, *fakeClock) {
	t.Helper()
	repo := newInMemoryPreferencesRepo()
	repo.profiles[accountID] = domain.DefaultLocale()
	eligibility := &stubEligibility{eligible: map[domain.AccountID]bool{accountID: true}}
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	return repo, eligibility, clock
}

// TestCommunicationPreferencesDTOExposesOnlyAllowedFields snapshots the
// projection consumed by notifications: exactly the account, the interface
// locale and the explicit opt-in. There is no content_language field: the
// Arena content language is independent from interface preferences.
func TestCommunicationPreferencesDTOExposesOnlyAllowedFields(t *testing.T) {
	typ := reflect.TypeOf(application.CommunicationPreferences{})
	want := []string{"AccountID", "InterfaceLocale", "MarketingOptIn"}
	if typ.NumField() != len(want) {
		t.Fatalf("CommunicationPreferences has %d fields, want exactly %d", typ.NumField(), len(want))
	}
	for i, expected := range want {
		if got := typ.Field(i).Name; got != expected {
			t.Errorf("field %d = %q, want %q", i, got, expected)
		}
		if lowered := strings.ToLower(typ.Field(i).Name); strings.Contains(lowered, "contentlanguage") || strings.Contains(lowered, "content") {
			t.Fatalf("preferences must not carry content language: field %q", typ.Field(i).Name)
		}
	}
}

func TestGetCommunicationPreferencesUseCase(t *testing.T) {
	const accountID domain.AccountID = "018f6b2a-0000-7000-8000-000000000040"
	repo, _, _ := newPreferencesFixture(t, accountID)
	useCase := application.NewGetCommunicationPreferencesUseCase(repo)

	preferences, err := useCase.Execute(context.Background(), accountID)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if preferences.MarketingOptIn {
		t.Fatal("marketing opt-in must default to false before any explicit choice")
	}
	if preferences.InterfaceLocale.String() != domain.LocaleBrazilianPortuguese {
		t.Errorf("InterfaceLocale = %q, want pt-BR", preferences.InterfaceLocale)
	}
	if preferences.AccountID != accountID {
		t.Errorf("AccountID = %q, want %q", preferences.AccountID, accountID)
	}

	if _, err := useCase.Execute(context.Background(), domain.AccountID("")); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
	}

	unknown := domain.AccountID("018f6b2a-0000-7000-8000-000000000041")
	if _, err := useCase.Execute(context.Background(), unknown); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("unknown account error = %v, want ErrProfileNotFound", err)
	}
}

func TestUpdateCommunicationPreferencesUseCase(t *testing.T) {
	const accountID domain.AccountID = "018f6b2a-0000-7000-8000-000000000042"
	changedAt := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	t.Run("explicit opt-in is audited", func(t *testing.T) {
		repo, eligibility, clock := newPreferencesFixture(t, accountID)
		clock.Set(changedAt)
		useCase := application.NewUpdateCommunicationPreferencesUseCase(repo, eligibility, clock)

		preferences, err := useCase.Execute(context.Background(), application.SetMarketingOptInCommand{
			AccountID: accountID.String(),
			OptIn:     true,
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if !preferences.MarketingOptIn {
			t.Fatal("marketing opt-in should be true after the explicit command")
		}
		if repo.setCalls != 1 || repo.historyLength(accountID) != 1 {
			t.Fatalf("setCalls=%d history=%d, want 1/1", repo.setCalls, repo.historyLength(accountID))
		}
	})

	t.Run("repeating the current value is a no-op without audit noise", func(t *testing.T) {
		repo, eligibility, clock := newPreferencesFixture(t, accountID)
		useCase := application.NewUpdateCommunicationPreferencesUseCase(repo, eligibility, clock)

		if _, err := useCase.Execute(context.Background(), application.SetMarketingOptInCommand{
			AccountID: accountID.String(),
			OptIn:     false,
		}); err != nil {
			t.Fatalf("no-op Execute() error = %v", err)
		}
		if repo.setCalls != 0 {
			t.Errorf("setCalls = %d, want 0 for an unchanged value", repo.setCalls)
		}
		if repo.historyLength(accountID) != 0 {
			t.Errorf("history = %d, want 0 for an unchanged value", repo.historyLength(accountID))
		}
	})

	t.Run("opt-out after opt-in appends a second audit entry", func(t *testing.T) {
		repo, eligibility, clock := newPreferencesFixture(t, accountID)
		useCase := application.NewUpdateCommunicationPreferencesUseCase(repo, eligibility, clock)

		if _, err := useCase.Execute(context.Background(), application.SetMarketingOptInCommand{
			AccountID: accountID.String(),
			OptIn:     true,
		}); err != nil {
			t.Fatalf("opt-in error = %v", err)
		}
		preferences, err := useCase.Execute(context.Background(), application.SetMarketingOptInCommand{
			AccountID: accountID.String(),
			OptIn:     false,
		})
		if err != nil {
			t.Fatalf("opt-out error = %v", err)
		}
		if preferences.MarketingOptIn {
			t.Fatal("marketing opt-in should be false after opt-out")
		}
		if repo.historyLength(accountID) != 2 {
			t.Fatalf("history = %d, want 2", repo.historyLength(accountID))
		}
	})

	t.Run("authorization failures never write", func(t *testing.T) {
		repo, eligibility, clock := newPreferencesFixture(t, accountID)
		useCase := application.NewUpdateCommunicationPreferencesUseCase(repo, eligibility, clock)

		_, err := useCase.Execute(context.Background(), application.SetMarketingOptInCommand{
			AccountID: accountID.String(),
			OptIn:     true,
		})
		if err != nil {
			t.Fatalf("seed opt-in: %v", err)
		}
		baselineHistory := repo.historyLength(accountID)
		baselineSets := repo.setCalls

		eligibility.eligible[accountID] = false
		_, err = useCase.Execute(context.Background(), application.SetMarketingOptInCommand{
			AccountID: accountID.String(),
			OptIn:     false,
		})
		if !errors.Is(err, application.ErrAccountNotEligible) {
			t.Fatalf("ineligible account error = %v, want ErrAccountNotEligible", err)
		}

		unknown := domain.AccountID("018f6b2a-0000-7000-8000-000000000043")
		eligibility.eligible[unknown] = true
		_, err = useCase.Execute(context.Background(), application.SetMarketingOptInCommand{
			AccountID: unknown.String(),
			OptIn:     true,
		})
		if !errors.Is(err, application.ErrProfileNotFound) {
			t.Fatalf("missing profile error = %v, want ErrProfileNotFound", err)
		}

		_, err = useCase.Execute(context.Background(), application.SetMarketingOptInCommand{
			AccountID: "",
			OptIn:     true,
		})
		if !errors.Is(err, domain.ErrEmptyAccountID) {
			t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
		}

		if repo.setCalls != baselineSets || repo.historyLength(accountID) != baselineHistory {
			t.Fatalf("failed updates wrote state: setCalls=%d history=%d, want %d/%d",
				repo.setCalls, repo.historyLength(accountID), baselineSets, baselineHistory)
		}
	})
}
