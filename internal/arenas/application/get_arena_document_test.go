package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

type fakeDocumentRepo struct {
	arena       *domain.Arena
	publicErr   error
	status      domain.ArenaStatus
	statusErr   error
	statusCalls int
}

func (r *fakeDocumentRepo) GetPublicArenaBySlug(_ context.Context, _ domain.Slug) (*domain.Arena, error) {
	if r.publicErr != nil {
		return nil, r.publicErr
	}
	return r.arena, nil
}

func (r *fakeDocumentRepo) GetArenaStatusBySlug(_ context.Context, _ domain.Slug) (domain.ArenaStatus, error) {
	r.statusCalls++
	if r.statusErr != nil {
		return "", r.statusErr
	}
	return r.status, nil
}

func publishedDocumentArena(t *testing.T) *domain.Arena {
	t.Helper()
	statement, err := domain.ParseStatement("A AGI existirá até 2040", domain.DefaultStatementPolicy())
	if err != nil {
		t.Fatalf("ParseStatement: %v", err)
	}
	category, err := domain.ParseCategory("technology")
	if err != nil {
		t.Fatalf("ParseCategory: %v", err)
	}
	language, err := domain.ParseLanguage("pt-BR")
	if err != nil {
		t.Fatalf("ParseLanguage: %v", err)
	}
	slug, err := domain.ParseSlug("a-agi-existira-ate-2040")
	if err != nil {
		t.Fatalf("ParseSlug: %v", err)
	}
	arena, err := domain.ReconstituteArena(
		domain.ArenaID("00000000-0000-0000-0000-000000000001"),
		domain.CreatorID("00000000-0000-0000-0000-000000000002"),
		statement,
		domain.Context{},
		category,
		language,
		domain.ArenaStatusPublished,
		slug,
		2,
		testInstant,
		&testInstant,
		nil,
	)
	if err != nil {
		t.Fatalf("ReconstituteArena: %v", err)
	}
	return arena
}

func TestGetArenaDocumentResolvesPublicArena(t *testing.T) {
	arena := publishedDocumentArena(t)
	repo := &fakeDocumentRepo{arena: arena}
	useCase := application.NewGetArenaDocumentUseCase(repo)

	resolved, err := useCase.Execute(context.Background(), "a-agi-existira-ate-2040")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if resolved.ID() != arena.ID() {
		t.Fatalf("resolved arena = %s, want %s", resolved.ID(), arena.ID())
	}
	if repo.statusCalls != 0 {
		t.Fatalf("status lookups = %d, want 0 when the public read succeeds", repo.statusCalls)
	}
}

func TestGetArenaDocumentDistinguishesRemovedFromMissing(t *testing.T) {
	tests := []struct {
		name      string
		repo      *fakeDocumentRepo
		wantErr   error
		wantCalls int
	}{
		{
			name:      "removed answers gone",
			repo:      &fakeDocumentRepo{publicErr: application.ErrArenaNotFound, status: domain.ArenaStatusRemoved},
			wantErr:   application.ErrArenaGone,
			wantCalls: 1,
		},
		{
			name:      "unknown slug is not found",
			repo:      &fakeDocumentRepo{publicErr: application.ErrArenaNotFound, statusErr: application.ErrArenaNotFound},
			wantErr:   application.ErrArenaNotFound,
			wantCalls: 1,
		},
		{
			name:      "non-removed stored status is not found",
			repo:      &fakeDocumentRepo{publicErr: application.ErrArenaNotFound, status: domain.ArenaStatusDraft},
			wantErr:   application.ErrArenaNotFound,
			wantCalls: 1,
		},
		{
			name:      "status lookup failure propagates",
			repo:      &fakeDocumentRepo{publicErr: application.ErrArenaNotFound, statusErr: errors.New("storage down")},
			wantErr:   nil,
			wantCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			useCase := application.NewGetArenaDocumentUseCase(test.repo)
			_, err := useCase.Execute(context.Background(), "a-agi-existira-ate-2040")
			if test.wantErr == nil {
				if err == nil || errors.Is(err, application.ErrArenaNotFound) || errors.Is(err, application.ErrArenaGone) {
					t.Fatalf("Execute() error = %v, want a propagated storage failure", err)
				}
			} else if !errors.Is(err, test.wantErr) {
				t.Fatalf("Execute() error = %v, want %v", err, test.wantErr)
			}
			if test.repo.statusCalls != test.wantCalls {
				t.Fatalf("status lookups = %d, want %d", test.repo.statusCalls, test.wantCalls)
			}
		})
	}
}

func TestGetArenaDocumentRejectsInvalidSlugWithoutLookup(t *testing.T) {
	repo := &fakeDocumentRepo{arena: publishedDocumentArena(t)}
	useCase := application.NewGetArenaDocumentUseCase(repo)

	for _, raw := range []string{"", "AB", "UPPERCASE", "espaco invalido", "trailing-"} {
		if _, err := useCase.Execute(context.Background(), raw); !errors.Is(err, application.ErrArenaNotFound) {
			t.Fatalf("Execute(%q) error = %v, want ErrArenaNotFound", raw, err)
		}
	}
	if repo.statusCalls != 0 {
		t.Fatalf("status lookups = %d, want 0 for invalid slugs", repo.statusCalls)
	}
}

func TestGetArenaDocumentPropagatesPublicReadFailure(t *testing.T) {
	storageErr := errors.New("storage down")
	repo := &fakeDocumentRepo{publicErr: storageErr}
	useCase := application.NewGetArenaDocumentUseCase(repo)

	if _, err := useCase.Execute(context.Background(), "a-agi-existira-ate-2040"); !errors.Is(err, storageErr) {
		t.Fatalf("Execute() error = %v, want the storage failure", err)
	}
	if repo.statusCalls != 0 {
		t.Fatalf("status lookups = %d, want 0 after a storage failure", repo.statusCalls)
	}
}
