package application_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

var testFeedSecret = []byte("0123456789abcdef0123456789abcdef")

type fakeFeedRepo struct {
	arenas []domain.Arena
	err    error

	filter    application.ArenaFeedFilter
	after     *application.FeedPosition
	lastLimit int
	calls     int
}

func (r *fakeFeedRepo) GetPublicArenaBySlug(_ context.Context, slug domain.Slug) (*domain.Arena, error) {
	if r.err != nil {
		return nil, r.err
	}
	for _, arena := range r.arenas {
		if arena.Slug().Equals(slug) {
			copied := arena
			return &copied, nil
		}
	}
	return nil, application.ErrArenaNotFound
}

func (r *fakeFeedRepo) ListPublicArenas(_ context.Context, filter application.ArenaFeedFilter, after *application.FeedPosition, limit int) ([]domain.Arena, error) {
	r.calls++
	r.filter = filter
	r.after = after
	r.lastLimit = limit
	if r.err != nil {
		return nil, r.err
	}

	start := 0
	if after != nil {
		for i, arena := range r.arenas {
			if arena.ID().String() == after.ArenaID {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	if end > len(r.arenas) {
		end = len(r.arenas)
	}
	page := append([]domain.Arena(nil), r.arenas[start:end]...)
	return page, nil
}

func newTestFeedUseCase(t *testing.T, repo application.ArenaFeedRepository) *application.GetArenaFeedUseCase {
	t.Helper()
	codec, err := application.NewFeedCursorCodec(testFeedSecret)
	if err != nil {
		t.Fatalf("build feed cursor codec: %v", err)
	}
	return application.NewGetArenaFeedUseCase(repo, codec)
}

func mustFeedArenas(t *testing.T, count int) []domain.Arena {
	t.Helper()
	policy := domain.DefaultStatementPolicy()
	arenas := make([]domain.Arena, count)
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for i := range arenas {
		statement, err := domain.ParseStatement(strings.Repeat("a", policy.MinLength+i%5), policy)
		if err != nil {
			t.Fatalf("ParseStatement: %v", err)
		}
		category, _ := domain.ParseCategory("technology")
		language, _ := domain.ParseLanguage("pt-BR")
		slug, err := domain.ParseSlug("feed-arena-" + string(rune('a'+i)))
		if err != nil {
			t.Fatalf("ParseSlug: %v", err)
		}
		publishedAt := base.Add(-time.Duration(i) * time.Minute)
		arena, err := domain.ReconstituteArena(
			domain.ArenaID("018f6b2a-0000-7000-8000-0000000000"+string(rune('a'+i))),
			domain.CreatorID("018f6b2a-0000-7000-8000-000000000001"),
			statement, domain.Context{}, category, language,
			domain.ArenaStatusPublished, slug, 2, base.Add(-24*time.Hour), &publishedAt, nil,
		)
		if err != nil {
			t.Fatalf("ReconstituteArena: %v", err)
		}
		arenas[i] = *arena
	}
	return arenas
}

func TestFeedCursorCodec(t *testing.T) {
	if _, err := application.NewFeedCursorCodec([]byte(strings.Repeat("s", 31))); !errors.Is(err, application.ErrWeakFeedCursorSecret) {
		t.Fatalf("weak secret error = %v, want ErrWeakFeedCursorSecret", err)
	}
	codec, err := application.NewFeedCursorCodec(testFeedSecret)
	if err != nil {
		t.Fatalf("NewFeedCursorCodec: %v", err)
	}

	arena := mustFeedArenas(t, 1)[0]
	decoded, err := codec.Decode(codec.Encode(arena))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded == nil || decoded.ArenaID != arena.ID().String() ||
		!decoded.PublishedAt.Equal(*arena.PublishedAt()) {
		t.Fatalf("decoded = %+v, want %+v", decoded, arena.ID())
	}
	if position, err := codec.Decode("   "); err != nil || position != nil {
		t.Fatalf("empty cursor = %+v/%v, want nil without error", position, err)
	}

	unsigned := base64.RawURLEncoding.EncodeToString([]byte("v1|2026-09-17T12:00:00Z|arena-a"))
	forgedMac := hmac.New(sha256.New, []byte("a-different-secret-key-32-bytes-long"))
	forgedMac.Write([]byte("v1|2026-09-17T12:00:00Z|arena-a"))
	forged := base64.RawURLEncoding.EncodeToString([]byte("v1|2026-09-17T12:00:00Z|arena-a")) +
		"." + base64.RawURLEncoding.EncodeToString(forgedMac.Sum(nil))

	signed := func(payload string) string {
		mac := hmac.New(sha256.New, testFeedSecret)
		mac.Write([]byte(payload))
		return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	invalid := []string{
		"%%%",
		unsigned,
		forged,
		signed("v9|2026-09-17T12:00:00Z|arena-a"),
		signed("v1|not-a-time|arena-a"),
		signed("v1|2026-09-17T12:00:00Z|"),
		signed("v1|2026-09-17T12:00:00Z|arena\nbroken"),
		signed("v1|2026-09-17T12:00:00Z|arena-a|extra"),
		signed("v1|2026-09-17T12:00:00Z|arena broken"),
		signed("v1|2026-09-17T12:00:00Z|arena\x7fbroken"),
	}
	for _, raw := range invalid {
		if _, err := codec.Decode(raw); !errors.Is(err, application.ErrInvalidCursor) {
			t.Errorf("Decode(%q) error = %v, want ErrInvalidCursor", raw, err)
		}
	}

	// The printable range edges are valid identifier bytes (mutation
	// gate: feed_cursor.go:103).
	edged, err := codec.Decode(signed("v1|2026-09-17T12:00:00Z|edge!~id"))
	if err != nil || edged.ArenaID != "edge!~id" {
		t.Fatalf("edge cursor = (%+v, %v), want edge!~id decoded", edged, err)
	}
}

func TestGetArenaFeedUseCasePaginatesWithoutDuplicatesOrGaps(t *testing.T) {
	repo := &fakeFeedRepo{arenas: mustFeedArenas(t, 5)}
	useCase := newTestFeedUseCase(t, repo)

	seen := make([]string, 0, 5)
	cursor := ""
	pages := 0
	for page := 0; page < 10; page++ {
		feed, err := useCase.Execute(context.Background(), application.GetArenaFeedCommand{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatalf("page %d Execute() error = %v", page, err)
		}
		pages++
		for _, arena := range feed.Arenas {
			seen = append(seen, arena.ID().String())
		}
		if feed.NextCursor == "" {
			break
		}
		cursor = feed.NextCursor
	}

	if len(seen) != 5 {
		t.Fatalf("entries delivered = %d, want 5 (no duplicates, no gaps)", len(seen))
	}
	unique := map[string]bool{}
	for _, id := range seen {
		if unique[id] {
			t.Fatalf("duplicate entry %q", id)
		}
		unique[id] = true
	}
	for _, arena := range repo.arenas {
		if !unique[arena.ID().String()] {
			t.Fatalf("entry %q was skipped", arena.ID())
		}
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3 for limit 2 and 5 entries", pages)
	}
	if repo.lastLimit != 3 {
		t.Errorf("lookahead limit = %d, want 3", repo.lastLimit)
	}
	if repo.after == nil || repo.after.ArenaID != repo.arenas[3].ID().String() {
		t.Errorf("last cursor = %+v, want the last delivered entry", repo.after)
	}
}

func TestGetArenaFeedUseCaseClampsLimitsAndValidatesFilters(t *testing.T) {
	repo := &fakeFeedRepo{arenas: mustFeedArenas(t, 2)}
	useCase := newTestFeedUseCase(t, repo)

	if _, err := useCase.Execute(context.Background(), application.GetArenaFeedCommand{}); err != nil {
		t.Fatalf("default Execute() error = %v", err)
	}
	if repo.lastLimit != application.DefaultFeedLimit+1 {
		t.Errorf("default lookahead = %d, want %d", repo.lastLimit, application.DefaultFeedLimit+1)
	}

	if _, err := useCase.Execute(context.Background(), application.GetArenaFeedCommand{Limit: 5000}); err != nil {
		t.Fatalf("maximum Execute() error = %v", err)
	}
	if repo.lastLimit != application.MaxFeedLimit+1 {
		t.Errorf("clamped lookahead = %d, want %d", repo.lastLimit, application.MaxFeedLimit+1)
	}

	// Valid filters are parsed and forwarded.
	feed, err := useCase.Execute(context.Background(), application.GetArenaFeedCommand{
		Language: "pt-br",
		Category: "technology",
		Status:   "published",
	})
	if err != nil {
		t.Fatalf("filtered Execute() error = %v", err)
	}
	if len(feed.Arenas) != 2 {
		t.Fatalf("filtered entries = %d", len(feed.Arenas))
	}
	if repo.filter.Language == nil || repo.filter.Language.String() != "pt-BR" {
		t.Errorf("language filter = %+v, want canonical pt-BR", repo.filter.Language)
	}
	if repo.filter.Category == nil || repo.filter.Category.String() != "technology" {
		t.Errorf("category filter = %+v", repo.filter.Category)
	}
	if repo.filter.Status == nil || *repo.filter.Status != domain.ArenaStatusPublished {
		t.Errorf("status filter = %+v", repo.filter.Status)
	}

	// Invalid filters never reach the repository.
	callsBefore := repo.calls
	for _, cmd := range []application.GetArenaFeedCommand{
		{Language: "es-ES"},
		{Language: "pt_BR"},
		{Category: "Technology"},
		{Status: "draft"},
		{Status: "removed"},
		{Status: "unknown"},
	} {
		if _, err := useCase.Execute(context.Background(), cmd); err == nil {
			t.Fatalf("filter %+v must be rejected", cmd)
		}
	}
	if repo.calls != callsBefore {
		t.Fatal("invalid filters must not reach the repository")
	}

	repo.err = errors.New("storage unavailable")
	if _, err := useCase.Execute(context.Background(), application.GetArenaFeedCommand{}); !errors.Is(err, repo.err) {
		t.Fatalf("repository error not propagated: %v", err)
	}
}

func TestGetArenaFeedExactPageCarriesNoCursor(t *testing.T) {
	repo := &fakeFeedRepo{arenas: mustFeedArenas(t, 2)}
	useCase := newTestFeedUseCase(t, repo)

	// Exactly the requested rows complete the page: no cursor may point
	// past it (mutation gate: get_arena_feed.go:61).
	feed, err := useCase.Execute(context.Background(), application.GetArenaFeedCommand{Limit: 2})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(feed.Arenas) != 2 || feed.NextCursor != "" {
		t.Fatalf("exact page = %d arenas/cursor %q, want two with no cursor", len(feed.Arenas), feed.NextCursor)
	}
}
