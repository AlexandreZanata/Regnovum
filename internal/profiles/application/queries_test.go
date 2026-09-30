package application_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

type inMemoryProfileQueryRepo struct {
	publicByNormalized map[string]*application.PublicProfile
	privateByAccount   map[domain.AccountID]*application.PrivateProfile
	publicCalls        int
	privateCalls       int
}

func newInMemoryProfileQueryRepo() *inMemoryProfileQueryRepo {
	return &inMemoryProfileQueryRepo{
		publicByNormalized: make(map[string]*application.PublicProfile),
		privateByAccount:   make(map[domain.AccountID]*application.PrivateProfile),
	}
}

func (r *inMemoryProfileQueryRepo) GetPublicProfileByUsername(_ context.Context, normalizedUsername string) (*application.PublicProfile, error) {
	r.publicCalls++
	profile, ok := r.publicByNormalized[normalizedUsername]
	if !ok {
		return nil, application.ErrProfileNotFound
	}
	return profile, nil
}

func (r *inMemoryProfileQueryRepo) GetPrivateProfileByAccountID(_ context.Context, accountID domain.AccountID) (*application.PrivateProfile, error) {
	r.privateCalls++
	profile, ok := r.privateByAccount[accountID]
	if !ok {
		return nil, application.ErrProfileNotFound
	}
	return profile, nil
}

func TestGetPublicProfileUseCase(t *testing.T) {
	const accountID domain.AccountID = "018f6b2a-0000-7000-8000-000000000030"
	createdAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	repo := newInMemoryProfileQueryRepo()
	repo.publicByNormalized["arenauser"] = &application.PublicProfile{
		Username:        "ArenaUser",
		InterfaceLocale: domain.LocaleAmericanEnglish,
		CreatedAt:       createdAt,
	}
	repo.publicByNormalized["shared"] = &application.PublicProfile{
		Username:        "shared",
		InterfaceLocale: domain.LocaleBrazilianPortuguese,
		CreatedAt:       createdAt,
	}
	useCase := application.NewGetPublicProfileUseCase(repo)

	t.Run("normalizes the username before lookup", func(t *testing.T) {
		profile, err := useCase.Execute(context.Background(), "ARENAUSER")
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if profile.Username != "ArenaUser" || profile.InterfaceLocale != domain.LocaleAmericanEnglish {
			t.Errorf("profile = %+v, want ArenaUser/en-US", profile)
		}
		if !profile.CreatedAt.Equal(createdAt) {
			t.Errorf("CreatedAt = %v, want %v", profile.CreatedAt, createdAt)
		}
		if repo.publicCalls != 1 {
			t.Errorf("repository calls = %d, want 1", repo.publicCalls)
		}
	})

	t.Run("malformed usernames resolve to not found without querying", func(t *testing.T) {
		repo.publicCalls = 0
		for _, input := range []string{"", "   ", "ab", "a b", "árvore", "-leading", strings.Repeat("a", 31)} {
			profile, err := useCase.Execute(context.Background(), input)
			if !errors.Is(err, application.ErrProfileNotFound) {
				t.Fatalf("Execute(%q) error = %v, want ErrProfileNotFound", input, err)
			}
			if profile != nil {
				t.Fatalf("Execute(%q) returned a profile on error", input)
			}
		}
		if repo.publicCalls != 0 {
			t.Errorf("repository was queried %d times for malformed usernames", repo.publicCalls)
		}
	})

	t.Run("unknown username is not found", func(t *testing.T) {
		if _, err := useCase.Execute(context.Background(), "ghosthandle"); !errors.Is(err, application.ErrProfileNotFound) {
			t.Fatalf("Execute() error = %v, want ErrProfileNotFound", err)
		}
	})

	t.Run("shared account id is ignored by the interface", func(t *testing.T) {
		// A username may never be resolved through any other key: the query
		// port accepts exactly one canonical username.
		if _, err := useCase.Execute(context.Background(), string(accountID)); !errors.Is(err, application.ErrProfileNotFound) {
			t.Fatalf("Execute(accountID) error = %v, want ErrProfileNotFound", err)
		}
	})
}

func TestGetPrivateProfileUseCase(t *testing.T) {
	const accountID domain.AccountID = "018f6b2a-0000-7000-8000-000000000031"
	updatedAt := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	repo := newInMemoryProfileQueryRepo()
	repo.privateByAccount[accountID] = &application.PrivateProfile{
		Username:        "PrivateUser",
		InterfaceLocale: domain.LocaleBrazilianPortuguese,
		CreatedAt:       updatedAt.Add(-24 * time.Hour),
		UpdatedAt:       updatedAt,
	}
	useCase := application.NewGetPrivateProfileUseCase(repo)

	profile, err := useCase.Execute(context.Background(), accountID)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if profile.Username != "PrivateUser" || profile.InterfaceLocale != domain.LocaleBrazilianPortuguese {
		t.Errorf("profile = %+v, want PrivateUser/pt-BR", profile)
	}
	if !profile.UpdatedAt.Equal(updatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", profile.UpdatedAt, updatedAt)
	}

	if _, err := useCase.Execute(context.Background(), domain.AccountID("")); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
	}

	unknown := domain.AccountID("018f6b2a-0000-7000-8000-000000000032")
	if _, err := useCase.Execute(context.Background(), unknown); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("unknown account error = %v, want ErrProfileNotFound", err)
	}
}

// TestProfileDTOsExposeOnlyAllowedFields snapshots the DTO shapes: the
// public and private projections carry exactly the allowed fields, and no
// field name hints at email, credentials, payment identifiers, antifraud
// flags or administrative notes.
func TestProfileDTOsExposeOnlyAllowedFields(t *testing.T) {
	forbiddenMarkers := []string{
		"email", "accountid", "password", "credential", "hash",
		"stripe", "customer", "billing", "payment",
		"fraud", "admin", "notes", "ip", "useragent", "session",
	}

	tests := []struct {
		name   string
		typ    reflect.Type
		fields []string
	}{
		{name: "PublicProfile", typ: reflect.TypeOf(application.PublicProfile{}), fields: []string{"Username", "InterfaceLocale", "CreatedAt"}},
		{name: "PrivateProfile", typ: reflect.TypeOf(application.PrivateProfile{}), fields: []string{"Username", "InterfaceLocale", "CreatedAt", "UpdatedAt"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.typ.NumField() != len(tc.fields) {
				t.Fatalf("%s has %d fields, want exactly %d", tc.name, tc.typ.NumField(), len(tc.fields))
			}
			for i, want := range tc.fields {
				if got := tc.typ.Field(i).Name; got != want {
					t.Errorf("field %d = %q, want %q", i, got, want)
				}
			}
			for i := 0; i < tc.typ.NumField(); i++ {
				lowered := strings.ToLower(tc.typ.Field(i).Name)
				for _, marker := range forbiddenMarkers {
					if strings.Contains(lowered, marker) {
						t.Fatalf("SECURITY VIOLATION: %s exposes forbidden field %q", tc.name, tc.typ.Field(i).Name)
					}
				}
			}
		})
	}
}
