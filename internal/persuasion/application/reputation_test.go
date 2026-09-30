package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

const reputationAuthorRaw = "018f6b2a-0000-7000-8000-0000000000a1"

func mustArenaReputation(t *testing.T, arenaRaw, category, language string, people, attributions int64) application.ArenaReputation {
	t.Helper()
	arenaID, err := domain.ParseArenaID(arenaRaw)
	if err != nil {
		t.Fatalf("ParseArenaID(%q): %v", arenaRaw, err)
	}
	return application.ArenaReputation{
		ArenaID:           arenaID,
		Category:          category,
		Language:          language,
		DistinctPeople:    people,
		ValidAttributions: attributions,
	}
}

func mustAuthorReputation(t *testing.T, arenas ...application.ArenaReputation) application.AuthorReputation {
	t.Helper()
	authorID, err := domain.ParseAuthorID(reputationAuthorRaw)
	if err != nil {
		t.Fatalf("ParseAuthorID: %v", err)
	}
	return application.AuthorReputation{AuthorID: authorID, Arenas: arenas, CheckedAt: testInstant}
}

// TestAuthorReputationHeadlineCountsPeopleOncePerArena proves the BR §6
// headline rule: a participant counts at most once per author in each Arena,
// so repeated events never inflate it — while the same person influencing two
// Arenas counts in both.
func TestAuthorReputationHeadlineCountsPeopleOncePerArena(t *testing.T) {
	t.Parallel()

	reputation := mustAuthorReputation(t,
		// The same person credited two arguments here: one person, two events.
		mustArenaReputation(t, "018f6b2a-0000-7000-8000-0000000000b1", "technology", "pt-BR", 3, 4),
		// The same person (plus another) influenced a second Arena: it counts
		// again there, because the rule is per author and Arena.
		mustArenaReputation(t, "018f6b2a-0000-7000-8000-0000000000b2", "technology", "en-US", 2, 2),
	)
	if got := reputation.InfluencedPeople(); got != 5 {
		t.Fatalf("InfluencedPeople() = %d, want 5 (sum of per-arena distinct people)", got)
	}
	if got := reputation.TotalValidAttributions(); got != 6 {
		t.Fatalf("TotalValidAttributions() = %d, want the detailed event count 6", got)
	}

	// No Arena means no facts: zeros, never an error.
	empty := mustAuthorReputation(t)
	if empty.InfluencedPeople() != 0 || empty.TotalValidAttributions() != 0 {
		t.Fatalf("empty reputation = %d/%d, want zeros", empty.InfluencedPeople(), empty.TotalValidAttributions())
	}
	if len(empty.ByCategory()) != 0 || len(empty.ByLanguage()) != 0 {
		t.Fatal("an author without facts must have empty distributions")
	}
}

func TestAuthorReputationDistributions(t *testing.T) {
	t.Parallel()

	reputation := mustAuthorReputation(t,
		mustArenaReputation(t, "018f6b2a-0000-7000-8000-0000000000b2", "science", "en-US", 2, 3),
		mustArenaReputation(t, "018f6b2a-0000-7000-8000-0000000000b1", "philosophy", "pt-BR", 3, 4),
		mustArenaReputation(t, "018f6b2a-0000-7000-8000-0000000000b3", "philosophy", "pt-BR", 1, 1),
	)

	byCategory := reputation.ByCategory()
	if len(byCategory) != 2 {
		t.Fatalf("ByCategory() = %+v, want two categories", byCategory)
	}
	// Deterministic order by label, and the buckets fold the per-Arena facts.
	if byCategory[0].Label != "philosophy" || byCategory[1].Label != "science" {
		t.Fatalf("ByCategory() order = %+v, want philosophical first", byCategory)
	}
	if byCategory[0].DistinctPeople != 4 || byCategory[0].ValidAttributions != 5 {
		t.Fatalf("philosophy bucket = %+v, want 4 people / 5 events", byCategory[0])
	}
	if byCategory[1].DistinctPeople != 2 || byCategory[1].ValidAttributions != 3 {
		t.Fatalf("science bucket = %+v, want 2 people / 3 events", byCategory[1])
	}

	byLanguage := reputation.ByLanguage()
	if len(byLanguage) != 2 {
		t.Fatalf("ByLanguage() = %+v, want two languages", byLanguage)
	}
	if byLanguage[0].Label != "en-US" || byLanguage[1].Label != "pt-BR" {
		t.Fatalf("ByLanguage() order = %+v, want en-US first", byLanguage)
	}
	if byLanguage[1].DistinctPeople != 4 || byLanguage[1].ValidAttributions != 5 {
		t.Fatalf("pt-BR bucket = %+v, want 4 people / 5 events", byLanguage[1])
	}

	// The distributions are derived from the same facts as the headline.
	var categoryPeople, categoryEvents int64
	for _, bucket := range byCategory {
		categoryPeople += bucket.DistinctPeople
		categoryEvents += bucket.ValidAttributions
	}
	if categoryPeople != reputation.InfluencedPeople() || categoryEvents != reputation.TotalValidAttributions() {
		t.Fatalf("distributions = %d/%d, want the headline facts %d/%d",
			categoryPeople, categoryEvents, reputation.InfluencedPeople(), reputation.TotalValidAttributions())
	}
}

func TestAuthorReputationValidateRejectsIncoherentProjections(t *testing.T) {
	t.Parallel()

	zeroArena, err := domain.ParseArenaID("018f6b2a-0000-7000-8000-0000000000b1")
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	otherArena, err := domain.ParseArenaID("018f6b2a-0000-7000-8000-0000000000b2")
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*application.AuthorReputation)
	}{
		{name: "missing author", mutate: func(r *application.AuthorReputation) { r.AuthorID = domain.AuthorID{} }},
		{name: "missing instant", mutate: func(r *application.AuthorReputation) { r.CheckedAt = time.Time{} }},
		{name: "missing arena", mutate: func(r *application.AuthorReputation) { r.Arenas[0].ArenaID = domain.ArenaID{} }},
		{name: "repeated arena", mutate: func(r *application.AuthorReputation) {
			r.Arenas = append(r.Arenas, application.ArenaReputation{
				ArenaID: otherArena, Category: "technology", Language: "pt-BR", DistinctPeople: 1, ValidAttributions: 1,
			})
			r.Arenas[0].ArenaID = otherArena
		}},
		{name: "missing category", mutate: func(r *application.AuthorReputation) { r.Arenas[0].Category = "" }},
		{name: "blank category", mutate: func(r *application.AuthorReputation) { r.Arenas[0].Category = "   " }},
		{name: "missing language", mutate: func(r *application.AuthorReputation) { r.Arenas[0].Language = "" }},
		{name: "negative people", mutate: func(r *application.AuthorReputation) { r.Arenas[0].DistinctPeople = -1 }},
		{name: "negative events", mutate: func(r *application.AuthorReputation) { r.Arenas[0].ValidAttributions = -1 }},
		{name: "more people than events", mutate: func(r *application.AuthorReputation) {
			r.Arenas[0].DistinctPeople = 2
			r.Arenas[0].ValidAttributions = 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reputation := mustAuthorReputation(t, application.ArenaReputation{
				ArenaID: zeroArena, Category: "technology", Language: "pt-BR", DistinctPeople: 1, ValidAttributions: 1,
			})
			test.mutate(&reputation)
			if err := reputation.Validate(); !errors.Is(err, application.ErrInvalidReputationProjection) {
				t.Fatalf("Validate() error = %v, want ErrInvalidReputationProjection", err)
			}
		})
	}

	// The well-formed projection passes.
	if err := mustAuthorReputation(t, application.ArenaReputation{
		ArenaID: zeroArena, Category: "technology", Language: "pt-BR", DistinctPeople: 2, ValidAttributions: 3,
	}).Validate(); err != nil {
		t.Fatalf("valid projection rejected: %v", err)
	}

	// Zero is a fact and equality is coherent: the fences refuse only
	// below zero and strictly above the events (mutation gate:
	// reputation.go:145,148).
	if err := mustAuthorReputation(t, application.ArenaReputation{
		ArenaID: zeroArena, Category: "technology", Language: "pt-BR", DistinctPeople: 0, ValidAttributions: 0,
	}).Validate(); err != nil {
		t.Fatalf("zeroed arena rejected: %v", err)
	}
	if err := mustAuthorReputation(t, application.ArenaReputation{
		ArenaID: zeroArena, Category: "technology", Language: "pt-BR", DistinctPeople: 2, ValidAttributions: 2,
	}).Validate(); err != nil {
		t.Fatalf("equal counts rejected: %v", err)
	}
}

type fakeReputationRepo struct {
	arenas []application.ArenaReputation
	err    error
	calls  int
	author string
}

func (r *fakeReputationRepo) ListAuthorArenaReputation(_ context.Context, authorID domain.AuthorID) ([]application.ArenaReputation, error) {
	r.calls++
	r.author = authorID.String()
	if r.err != nil {
		return nil, r.err
	}
	return append([]application.ArenaReputation{}, r.arenas...), nil
}

func TestGetAuthorReputationDerivesProjection(t *testing.T) {
	t.Parallel()

	repo := &fakeReputationRepo{arenas: []application.ArenaReputation{
		mustArenaReputation(t, "018f6b2a-0000-7000-8000-0000000000b1", "technology", "pt-BR", 3, 4),
	}}
	useCase := application.NewGetAuthorReputationUseCase(repo, fixedClock{instant: testInstant})

	reputation, err := useCase.Execute(context.Background(), application.GetAuthorReputationQuery{AuthorID: reputationAuthorRaw})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if repo.calls != 1 || repo.author != reputationAuthorRaw {
		t.Fatalf("port calls = %d for %q, want one call with the parsed author", repo.calls, repo.author)
	}
	if !reputation.CheckedAt.Equal(testInstant) {
		t.Fatalf("CheckedAt = %s, want the injected clock instant", reputation.CheckedAt)
	}
	if reputation.InfluencedPeople() != 3 || reputation.TotalValidAttributions() != 4 {
		t.Fatalf("derived facts = %d/%d, want 3/4", reputation.InfluencedPeople(), reputation.TotalValidAttributions())
	}
}

func TestGetAuthorReputationValidatesInputsAndPropagatesFailures(t *testing.T) {
	t.Parallel()

	t.Run("unknown author derives zeros", func(t *testing.T) {
		repo := &fakeReputationRepo{}
		useCase := application.NewGetAuthorReputationUseCase(repo, fixedClock{instant: testInstant})
		reputation, err := useCase.Execute(context.Background(), application.GetAuthorReputationQuery{AuthorID: reputationAuthorRaw})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if reputation.InfluencedPeople() != 0 || len(reputation.Arenas) != 0 {
			t.Fatalf("reputation = %+v, want zeroed facts", reputation)
		}
	})

	t.Run("invalid author identifier", func(t *testing.T) {
		tests := []struct {
			name string
			raw  string
			want error
		}{
			{name: "empty", raw: "", want: domain.ErrEmptyAuthorID},
			{name: "blank", raw: "   ", want: domain.ErrEmptyAuthorID},
			{name: "too long", raw: strings.Repeat("a", 65), want: domain.ErrInvalidIdentifier},
			{name: "control characters", raw: "author\nid", want: domain.ErrInvalidIdentifier},
		}
		for _, test := range tests {
			repo := &fakeReputationRepo{}
			useCase := application.NewGetAuthorReputationUseCase(repo, fixedClock{instant: testInstant})
			if _, err := useCase.Execute(context.Background(), application.GetAuthorReputationQuery{AuthorID: test.raw}); !errors.Is(err, test.want) {
				t.Fatalf("%s: Execute() error = %v, want %v", test.name, err, test.want)
			}
			if repo.calls != 0 {
				t.Fatalf("%s: the port was called for an invalid identifier", test.name)
			}
		}
	})

	t.Run("storage failure", func(t *testing.T) {
		repo := &fakeReputationRepo{err: errors.New("storage offline")}
		useCase := application.NewGetAuthorReputationUseCase(repo, fixedClock{instant: testInstant})
		if _, err := useCase.Execute(context.Background(), application.GetAuthorReputationQuery{AuthorID: reputationAuthorRaw}); err == nil {
			t.Fatal("Execute() error = nil, want the storage failure")
		}
	})

	t.Run("incoherent projection is refused", func(t *testing.T) {
		arena := mustArenaReputation(t, "018f6b2a-0000-7000-8000-0000000000b1", "technology", "pt-BR", 3, 4)
		repeated := arena
		repo := &fakeReputationRepo{arenas: []application.ArenaReputation{arena, repeated}}
		useCase := application.NewGetAuthorReputationUseCase(repo, fixedClock{instant: testInstant})
		if _, err := useCase.Execute(context.Background(), application.GetAuthorReputationQuery{AuthorID: reputationAuthorRaw}); !errors.Is(err, application.ErrInvalidReputationProjection) {
			t.Fatalf("Execute() error = %v, want ErrInvalidReputationProjection", err)
		}
	})
}
