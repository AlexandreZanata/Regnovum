package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

func mustStatement(t *testing.T, raw string) domain.Statement {
	t.Helper()
	statement, err := domain.ParseStatement(raw, domain.DefaultStatementPolicy())
	if err != nil {
		t.Fatalf("ParseStatement(%q): %v", raw, err)
	}
	return statement
}

func mustContext(t *testing.T, raw string) domain.Context {
	t.Helper()
	context, err := domain.ParseContext(raw, domain.DefaultStatementPolicy())
	if err != nil {
		t.Fatalf("ParseContext(%q): %v", raw, err)
	}
	return context
}

func mustCategory(t *testing.T, raw string) domain.Category {
	t.Helper()
	category, err := domain.ParseCategory(raw)
	if err != nil {
		t.Fatalf("ParseCategory(%q): %v", raw, err)
	}
	return category
}

func mustLanguage(t *testing.T, raw string) domain.Language {
	t.Helper()
	language, err := domain.ParseLanguage(raw)
	if err != nil {
		t.Fatalf("ParseLanguage(%q): %v", raw, err)
	}
	return language
}

func mustSlug(t *testing.T, raw string) domain.Slug {
	t.Helper()
	slug, err := domain.ParseSlug(raw)
	if err != nil {
		t.Fatalf("ParseSlug(%q): %v", raw, err)
	}
	return slug
}

var (
	testCreatedAt   = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	testPublishedAt = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
)

func mustDraftArena(t *testing.T, version int32) *domain.Arena {
	t.Helper()
	arena, err := domain.ReconstituteArena(
		domain.ArenaID("018f6b2a-0000-7000-8000-000000000001"),
		domain.CreatorID("018f6b2a-0000-7000-8000-000000000002"),
		mustStatement(t, "A AGI existirá até 2040"),
		domain.Context{},
		mustCategory(t, "technology"),
		mustLanguage(t, "pt-BR"),
		domain.ArenaStatusDraft,
		domain.Slug{},
		version,
		testCreatedAt,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ReconstituteArena(draft): %v", err)
	}
	return arena
}

func mustPublishedArena(t *testing.T) *domain.Arena {
	t.Helper()
	arena := mustDraftArena(t, 1)
	publishedAt := testPublishedAt
	arena, err := domain.ReconstituteArena(
		arena.ID(),
		arena.CreatorID(),
		arena.Statement(),
		arena.Context(),
		arena.Category(),
		arena.Language(),
		domain.ArenaStatusPublished,
		mustSlug(t, "a-agi-existira-ate-2040"),
		3,
		testCreatedAt,
		&publishedAt,
		nil,
	)
	if err != nil {
		t.Fatalf("ReconstituteArena(published): %v", err)
	}
	return arena
}

func TestReconstituteArenaInvariants(t *testing.T) {
	statement := mustStatement(t, "A AGI existirá até 2040")
	category := mustCategory(t, "technology")
	language := mustLanguage(t, "pt-BR")
	slug := mustSlug(t, "a-agi-existira-ate-2040")
	publishedAt := testPublishedAt
	closeBefore := testPublishedAt.Add(-time.Hour)

	tests := []struct {
		name        string
		id          domain.ArenaID
		creator     domain.CreatorID
		statement   domain.Statement
		category    domain.Category
		language    domain.Language
		status      domain.ArenaStatus
		slug        domain.Slug
		version     int32
		publishedAt *time.Time
		closesAt    *time.Time
		want        error
	}{
		{name: "empty id", id: "", creator: "c", statement: statement, category: category, language: language, status: domain.ArenaStatusDraft, version: 1, want: domain.ErrEmptyArenaID},
		{name: "empty creator", id: "a", creator: "", statement: statement, category: category, language: language, status: domain.ArenaStatusDraft, version: 1, want: domain.ErrEmptyCreatorID},
		{name: "empty statement", id: "a", creator: "c", statement: domain.Statement{}, category: category, language: language, status: domain.ArenaStatusDraft, version: 1, want: domain.ErrEmptyStatement},
		{name: "empty category", id: "a", creator: "c", statement: statement, category: domain.Category{}, language: language, status: domain.ArenaStatusDraft, version: 1, want: domain.ErrEmptyCategory},
		{name: "empty language", id: "a", creator: "c", statement: statement, category: category, language: domain.Language{}, status: domain.ArenaStatusDraft, version: 1, want: domain.ErrEmptyLanguage},
		{name: "unknown status", id: "a", creator: "c", statement: statement, category: category, language: language, status: domain.ArenaStatus("archived"), version: 1, want: domain.ErrInvalidStatus},
		{name: "zero version", id: "a", creator: "c", statement: statement, category: category, language: language, status: domain.ArenaStatusDraft, version: 0, want: domain.ErrInvalidVersion},
		{name: "draft with slug", id: "a", creator: "c", statement: statement, category: category, language: language, status: domain.ArenaStatusDraft, slug: slug, version: 1, want: domain.ErrInvalidStatusChange},
		{name: "draft with publication date", id: "a", creator: "c", statement: statement, category: category, language: language, status: domain.ArenaStatusDraft, version: 1, publishedAt: &publishedAt, want: domain.ErrInvalidStatusChange},
		{name: "published without slug", id: "a", creator: "c", statement: statement, category: category, language: language, status: domain.ArenaStatusPublished, version: 1, publishedAt: &publishedAt, want: domain.ErrInvalidStatusChange},
		{name: "published without date", id: "a", creator: "c", statement: statement, category: category, language: language, status: domain.ArenaStatusPublished, slug: slug, version: 1, want: domain.ErrInvalidStatusChange},
		{name: "close before publication", id: "a", creator: "c", statement: statement, category: category, language: language, status: domain.ArenaStatusPublished, slug: slug, version: 1, publishedAt: &publishedAt, closesAt: &closeBefore, want: domain.ErrInvalidCloseDate},
		{name: "close without publication", id: "a", creator: "c", statement: statement, category: category, language: language, status: domain.ArenaStatusDraft, version: 1, closesAt: &publishedAt, want: domain.ErrInvalidCloseDate},
		{name: "valid draft", id: "a", creator: "c", statement: statement, category: category, language: language, status: domain.ArenaStatusDraft, version: 1},
		{name: "valid published", id: "a", creator: "c", statement: statement, category: category, language: language, status: domain.ArenaStatusPublished, slug: slug, version: 2, publishedAt: &publishedAt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			arena, err := domain.ReconstituteArena(
				tc.id, tc.creator, tc.statement, domain.Context{}, tc.category, tc.language,
				tc.status, tc.slug, tc.version, testCreatedAt, tc.publishedAt, tc.closesAt,
			)
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("error = %v, want %v", err, tc.want)
				}
				if arena != nil {
					t.Fatal("expected nil arena on error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if arena.Version() != tc.version || arena.CreatedAt().Location() != time.UTC {
				t.Errorf("arena = version %d, createdAt %v", arena.Version(), arena.CreatedAt())
			}
		})
	}
}

func TestArenaDraftEditing(t *testing.T) {
	arena := mustDraftArena(t, 4)

	newStatement := mustStatement(t, "A afirmação revisada do rascunho")
	newContext := mustContext(t, "Contexto revisado")
	newCategory := mustCategory(t, "science")
	newLanguage := mustLanguage(t, "en-US")

	if err := arena.UpdateDraft(&newStatement, &newContext, &newCategory, &newLanguage); err != nil {
		t.Fatalf("UpdateDraft() error = %v", err)
	}
	if !arena.Statement().Equals(newStatement) || !arena.Context().Equals(newContext) {
		t.Error("statement/context were not updated")
	}
	if !arena.Category().Equals(newCategory) || !arena.Language().Equals(newLanguage) {
		t.Error("category/language were not updated")
	}
	if arena.Version() != 5 {
		t.Fatalf("Version() = %d, want 5", arena.Version())
	}

	published := mustPublishedArena(t)
	if err := published.UpdateDraft(&newStatement, nil, nil, nil); !errors.Is(err, domain.ErrArenaNotDraft) {
		t.Fatalf("published update error = %v, want ErrArenaNotDraft", err)
	}
	if published.Version() != 3 {
		t.Errorf("rejected update changed the version to %d", published.Version())
	}
}

func TestArenaPublishTransition(t *testing.T) {
	arena := mustDraftArena(t, 1)
	slug := mustSlug(t, "a-agi-existira-ate-2040")

	if err := arena.Publish(domain.Slug{}, testPublishedAt); !errors.Is(err, domain.ErrMissingSlug) {
		t.Fatalf("publish without slug error = %v, want ErrMissingSlug", err)
	}
	if err := arena.Close(); !errors.Is(err, domain.ErrInvalidStatusChange) {
		t.Fatalf("draft close error = %v, want ErrInvalidStatusChange", err)
	}

	if err := arena.Publish(slug, testPublishedAt); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if arena.Status() != domain.ArenaStatusPublished || !arena.Slug().Equals(slug) {
		t.Fatalf("published arena = %s/%q", arena.Status(), arena.Slug())
	}
	if arena.PublishedAt() == nil || !arena.PublishedAt().Equal(testPublishedAt) {
		t.Fatalf("PublishedAt() = %v, want %v", arena.PublishedAt(), testPublishedAt)
	}
	if arena.Version() != 2 || !arena.AcceptsParticipation() {
		t.Fatalf("published arena = version %d, accepts %v", arena.Version(), arena.AcceptsParticipation())
	}

	if err := arena.Publish(slug, testPublishedAt.Add(time.Hour)); !errors.Is(err, domain.ErrInvalidStatusChange) {
		t.Fatalf("second publish error = %v, want ErrInvalidStatusChange", err)
	}
}

func TestArenaStatusTransitionsMatrix(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T) *domain.Arena
		transition func(*domain.Arena) error
		wantStatus domain.ArenaStatus
		wantErr    error
	}{
		{
			name:       "published closes",
			setup:      mustPublishedArena,
			transition: func(a *domain.Arena) error { return a.Close() },
			wantStatus: domain.ArenaStatusClosed,
		},
		{
			name: "closed closes again",
			setup: func(t *testing.T) *domain.Arena {
				arena := mustPublishedArena(t)
				if err := arena.Close(); err != nil {
					t.Fatalf("Close(): %v", err)
				}
				return arena
			},
			transition: func(a *domain.Arena) error { return a.Close() },
			wantErr:    domain.ErrInvalidStatusChange,
		},
		{
			name:       "published restricts",
			setup:      mustPublishedArena,
			transition: func(a *domain.Arena) error { return a.Restrict() },
			wantStatus: domain.ArenaStatusRestricted,
		},
		{
			name: "closed restricts",
			setup: func(t *testing.T) *domain.Arena {
				arena := mustPublishedArena(t)
				if err := arena.Close(); err != nil {
					t.Fatalf("Close(): %v", err)
				}
				return arena
			},
			transition: func(a *domain.Arena) error { return a.Restrict() },
			wantStatus: domain.ArenaStatusRestricted,
		},
		{
			name: "draft restricts",
			setup: func(t *testing.T) *domain.Arena {
				return mustDraftArena(t, 1)
			},
			transition: func(a *domain.Arena) error { return a.Restrict() },
			wantErr:    domain.ErrInvalidStatusChange,
		},
		{
			name:       "published removes",
			setup:      mustPublishedArena,
			transition: func(a *domain.Arena) error { return a.Remove() },
			wantStatus: domain.ArenaStatusRemoved,
		},
		{
			name: "restricted removes",
			setup: func(t *testing.T) *domain.Arena {
				arena := mustPublishedArena(t)
				if err := arena.Restrict(); err != nil {
					t.Fatalf("Restrict(): %v", err)
				}
				return arena
			},
			transition: func(a *domain.Arena) error { return a.Remove() },
			wantStatus: domain.ArenaStatusRemoved,
		},
		{
			name: "removed is terminal for close",
			setup: func(t *testing.T) *domain.Arena {
				arena := mustPublishedArena(t)
				if err := arena.Remove(); err != nil {
					t.Fatalf("Remove(): %v", err)
				}
				return arena
			},
			transition: func(a *domain.Arena) error { return a.Close() },
			wantErr:    domain.ErrInvalidStatusChange,
		},
		{
			name: "removed is terminal for restrict",
			setup: func(t *testing.T) *domain.Arena {
				arena := mustPublishedArena(t)
				if err := arena.Remove(); err != nil {
					t.Fatalf("Remove(): %v", err)
				}
				return arena
			},
			transition: func(a *domain.Arena) error { return a.Restrict() },
			wantErr:    domain.ErrInvalidStatusChange,
		},
		{
			name: "removed is terminal for remove",
			setup: func(t *testing.T) *domain.Arena {
				arena := mustPublishedArena(t)
				if err := arena.Remove(); err != nil {
					t.Fatalf("Remove(): %v", err)
				}
				return arena
			},
			transition: func(a *domain.Arena) error { return a.Remove() },
			wantErr:    domain.ErrInvalidStatusChange,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			arena := tc.setup(t)
			versionBefore := arena.Version()
			err := tc.transition(arena)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				if arena.Version() != versionBefore {
					t.Error("rejected transition changed the version")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if arena.Status() != tc.wantStatus {
				t.Fatalf("status = %s, want %s", arena.Status(), tc.wantStatus)
			}
			if arena.Version() != versionBefore+1 {
				t.Errorf("version = %d, want %d", arena.Version(), versionBefore+1)
			}
			if arena.AcceptsParticipation() {
				t.Error("a non-published arena must not accept participation")
			}
		})
	}
}

func TestArenaCloseSchedule(t *testing.T) {
	arena := mustPublishedArena(t)
	due := testPublishedAt.Add(48 * time.Hour)

	if err := arena.ScheduleClose(testPublishedAt.Add(-time.Hour)); !errors.Is(err, domain.ErrInvalidCloseDate) {
		t.Fatalf("schedule before publication error = %v, want ErrInvalidCloseDate", err)
	}
	if err := arena.ScheduleClose(testPublishedAt); !errors.Is(err, domain.ErrInvalidCloseDate) {
		t.Fatalf("schedule at publication error = %v, want ErrInvalidCloseDate", err)
	}
	if err := arena.ScheduleClose(due); err != nil {
		t.Fatalf("ScheduleClose() error = %v", err)
	}
	if arena.ClosesAt() == nil || !arena.ClosesAt().Equal(due) {
		t.Fatalf("ClosesAt() = %v, want %v", arena.ClosesAt(), due)
	}

	// Not due yet.
	if closed, err := arena.CloseIfDue(due.Add(-time.Nanosecond)); err != nil || closed {
		t.Fatalf("CloseIfDue(before due) = %v/%v, want false/nil", closed, err)
	}
	// At the instant, the Arena closes exactly once.
	if closed, err := arena.CloseIfDue(due); err != nil || !closed {
		t.Fatalf("CloseIfDue(at due) = %v/%v, want true/nil", closed, err)
	}
	if arena.Status() != domain.ArenaStatusClosed {
		t.Fatalf("status = %s, want closed", arena.Status())
	}
	if closed, err := arena.CloseIfDue(due.Add(time.Hour)); err != nil || closed {
		t.Fatalf("second CloseIfDue() = %v/%v, want false/nil", closed, err)
	}

	// Clearing the schedule keeps the Arena published.
	open := mustPublishedArena(t)
	if err := open.ClearCloseSchedule(); err != nil {
		t.Fatalf("ClearCloseSchedule() error = %v", err)
	}
	if open.ClosesAt() != nil {
		t.Fatal("ClosesAt() must be cleared")
	}
	if closed, err := open.CloseIfDue(testPublishedAt.Add(1000 * time.Hour)); err != nil || closed {
		t.Fatalf("CloseIfDue without schedule = %v/%v, want false/nil", closed, err)
	}
	if err := mustDraftArena(t, 1).ClearCloseSchedule(); !errors.Is(err, domain.ErrInvalidStatusChange) {
		t.Fatalf("draft clear schedule error = %v, want ErrInvalidStatusChange", err)
	}
}

func TestArenaStatusPredicates(t *testing.T) {
	draft := mustDraftArena(t, 1)
	if !draft.IsDraft() || draft.AcceptsParticipation() {
		t.Error("draft predicates are inconsistent")
	}

	published := mustPublishedArena(t)
	if published.IsDraft() || !published.AcceptsParticipation() {
		t.Error("published predicates are inconsistent")
	}

	closed := mustPublishedArena(t)
	if err := closed.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if closed.AcceptsParticipation() || closed.IsDraft() {
		t.Error("closed predicates are inconsistent")
	}

	restricted := mustPublishedArena(t)
	if err := restricted.Restrict(); err != nil {
		t.Fatalf("Restrict(): %v", err)
	}
	if err := restricted.EnsureAcceptsParticipation(); !errors.Is(err, domain.ErrArenaNotOpen) {
		t.Fatalf("restricted participation error = %v, want ErrArenaNotOpen", err)
	}

	removed := mustPublishedArena(t)
	if err := removed.Remove(); err != nil {
		t.Fatalf("Remove(): %v", err)
	}
	if err := removed.EnsureAcceptsParticipation(); !errors.Is(err, domain.ErrArenaNotOpen) {
		t.Fatalf("removed participation error = %v, want ErrArenaNotOpen", err)
	}

	if err := published.EnsureAcceptsParticipation(); err != nil {
		t.Fatalf("published participation error = %v, want nil", err)
	}
	if err := closed.EnsureAcceptsParticipation(); !errors.Is(err, domain.ErrArenaNotOpen) {
		t.Fatalf("closed participation error = %v, want ErrArenaNotOpen", err)
	}
	if err := draft.EnsureAcceptsParticipation(); !errors.Is(err, domain.ErrArenaNotOpen) {
		t.Fatalf("draft participation error = %v, want ErrArenaNotOpen", err)
	}

	if domain.ArenaStatus("unknown").IsValid() {
		t.Error("unknown status must be invalid")
	}
	for _, status := range []domain.ArenaStatus{
		domain.ArenaStatusDraft, domain.ArenaStatusPublished, domain.ArenaStatusClosed,
		domain.ArenaStatusRestricted, domain.ArenaStatusRemoved,
	} {
		if !status.IsValid() {
			t.Errorf("status %q must be valid", status)
		}
	}
}
