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

const metricsArgumentRaw = "018f6b2a-0000-7000-8000-0000000000c1"

// fakeMetricsRepo is a deterministic ArgumentMetricsRepository double.
type fakeMetricsRepo struct {
	metrics *application.ArgumentMetrics
	err     error
	calls   int
	got     string
}

func (r *fakeMetricsRepo) GetArgumentMetrics(_ context.Context, argumentID domain.ArgumentID) (*application.ArgumentMetrics, error) {
	r.calls++
	r.got = argumentID.String()
	if r.err != nil {
		return nil, r.err
	}
	if r.metrics == nil {
		return nil, nil
	}
	copied := *r.metrics
	return &copied, nil
}

// fakeDirectory is a deterministic AuthorDirectory double.
type fakeDirectory struct {
	handle application.AuthorHandle
	err    error
	calls  int
	got    string
}

func (d *fakeDirectory) ResolveAuthor(_ context.Context, username string) (application.AuthorHandle, error) {
	d.calls++
	d.got = username
	if d.err != nil {
		return application.AuthorHandle{}, d.err
	}
	return d.handle, nil
}

func mustAuthorHandle(t *testing.T, username string) application.AuthorHandle {
	t.Helper()
	authorID, err := domain.ParseAuthorID(reputationAuthorRaw)
	if err != nil {
		t.Fatalf("ParseAuthorID: %v", err)
	}
	return application.AuthorHandle{AuthorID: authorID, Username: username}
}

func TestArgumentMetricsValidateRejectsIncoherentProjections(t *testing.T) {
	t.Parallel()

	argumentID, err := domain.ParseArgumentID(metricsArgumentRaw)
	if err != nil {
		t.Fatalf("ParseArgumentID: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*application.ArgumentMetrics)
	}{
		{name: "missing argument", mutate: func(m *application.ArgumentMetrics) { m.ArgumentID = domain.ArgumentID{} }},
		{name: "missing instant", mutate: func(m *application.ArgumentMetrics) { m.CheckedAt = time.Time{} }},
		{name: "negative people", mutate: func(m *application.ArgumentMetrics) { m.DistinctPeople = -1 }},
		{name: "negative events", mutate: func(m *application.ArgumentMetrics) { m.ValidAttributions = -1 }},
		{name: "more people than events", mutate: func(m *application.ArgumentMetrics) {
			m.DistinctPeople = 2
			m.ValidAttributions = 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := application.ArgumentMetrics{
				ArgumentID:        argumentID,
				DistinctPeople:    1,
				ValidAttributions: 2,
				CheckedAt:         testInstant,
			}
			test.mutate(&metrics)
			if err := metrics.Validate(); !errors.Is(err, application.ErrInvalidReputationProjection) {
				t.Fatalf("Validate() error = %v, want ErrInvalidReputationProjection", err)
			}
		})
	}

	// The zeroed projection of an argument nobody credited is well-formed: a
	// count of zero is a fact, not a defect.
	if err := (application.ArgumentMetrics{
		ArgumentID:        argumentID,
		DistinctPeople:    0,
		ValidAttributions: 0,
		CheckedAt:         testInstant,
	}).Validate(); err != nil {
		t.Fatalf("zeroed projection rejected: %v", err)
	}
}

func TestGetArgumentMetricsDerivesCounts(t *testing.T) {
	t.Parallel()

	repo := &fakeMetricsRepo{metrics: &application.ArgumentMetrics{DistinctPeople: 3, ValidAttributions: 4}}
	useCase := application.NewGetArgumentMetricsUseCase(repo, fixedClock{instant: testInstant})

	metrics, err := useCase.Execute(context.Background(), application.GetArgumentMetricsQuery{ArgumentID: metricsArgumentRaw})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if repo.calls != 1 || repo.got != metricsArgumentRaw {
		t.Fatalf("port calls = %d for %q, want one call with the parsed argument", repo.calls, repo.got)
	}
	if metrics.DistinctPeople != 3 || metrics.ValidAttributions != 4 {
		t.Fatalf("counts = %d/%d, want 3/4", metrics.DistinctPeople, metrics.ValidAttributions)
	}
	if !metrics.CheckedAt.Equal(testInstant) {
		t.Fatalf("CheckedAt = %s, want the injected clock instant", metrics.CheckedAt)
	}
	if metrics.ArgumentID.String() != metricsArgumentRaw {
		t.Fatalf("ArgumentID = %q, want the addressed argument", metrics.ArgumentID.String())
	}
}

func TestGetArgumentMetricsValidatesInputAndPropagatesFailures(t *testing.T) {
	t.Parallel()

	t.Run("unknown argument", func(t *testing.T) {
		repo := &fakeMetricsRepo{}
		useCase := application.NewGetArgumentMetricsUseCase(repo, fixedClock{instant: testInstant})
		if _, err := useCase.Execute(context.Background(), application.GetArgumentMetricsQuery{ArgumentID: metricsArgumentRaw}); !errors.Is(err, application.ErrArgumentNotFound) {
			t.Fatalf("Execute() error = %v, want ErrArgumentNotFound", err)
		}
	})

	t.Run("invalid identifier never reaches the port", func(t *testing.T) {
		tests := []struct {
			name string
			raw  string
			want error
		}{
			{name: "empty", raw: "", want: domain.ErrEmptyArgumentID},
			{name: "blank", raw: "   ", want: domain.ErrEmptyArgumentID},
			{name: "too long", raw: strings.Repeat("a", 65), want: domain.ErrInvalidIdentifier},
			{name: "control characters", raw: "argument\nid", want: domain.ErrInvalidIdentifier},
		}
		for _, test := range tests {
			repo := &fakeMetricsRepo{}
			useCase := application.NewGetArgumentMetricsUseCase(repo, fixedClock{instant: testInstant})
			if _, err := useCase.Execute(context.Background(), application.GetArgumentMetricsQuery{ArgumentID: test.raw}); !errors.Is(err, test.want) {
				t.Fatalf("%s: Execute() error = %v, want %v", test.name, err, test.want)
			}
			if repo.calls != 0 {
				t.Fatalf("%s: the port was called for an invalid identifier", test.name)
			}
		}
	})

	t.Run("storage failure", func(t *testing.T) {
		repo := &fakeMetricsRepo{err: errors.New("storage offline")}
		useCase := application.NewGetArgumentMetricsUseCase(repo, fixedClock{instant: testInstant})
		if _, err := useCase.Execute(context.Background(), application.GetArgumentMetricsQuery{ArgumentID: metricsArgumentRaw}); err == nil {
			t.Fatal("Execute() error = nil, want the storage failure")
		}
	})

	t.Run("incoherent projection is refused", func(t *testing.T) {
		repo := &fakeMetricsRepo{metrics: &application.ArgumentMetrics{DistinctPeople: 3, ValidAttributions: 1}}
		useCase := application.NewGetArgumentMetricsUseCase(repo, fixedClock{instant: testInstant})
		if _, err := useCase.Execute(context.Background(), application.GetArgumentMetricsQuery{ArgumentID: metricsArgumentRaw}); !errors.Is(err, application.ErrInvalidReputationProjection) {
			t.Fatalf("Execute() error = %v, want ErrInvalidReputationProjection", err)
		}
	})
}

func TestGetProfileReputationResolvesTheUsername(t *testing.T) {
	t.Parallel()

	directory := &fakeDirectory{handle: mustAuthorHandle(t, "ana_zanata")}
	repo := &fakeReputationRepo{arenas: []application.ArenaReputation{
		mustArenaReputation(t, "018f6b2a-0000-7000-8000-0000000000b1", "technology", "pt-BR", 3, 4),
	}}
	useCase := application.NewGetProfileReputationUseCase(directory, repo, fixedClock{instant: testInstant})

	reputation, err := useCase.Execute(context.Background(), application.GetProfileReputationQuery{Username: "ANA_ZANATA"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if directory.calls != 1 || directory.got != "ANA_ZANATA" {
		t.Fatalf("directory calls = %d for %q, want one call with the requested handle", directory.calls, directory.got)
	}
	if repo.author != reputationAuthorRaw {
		t.Fatalf("reputation port addressed %q, want the resolved author %q", repo.author, reputationAuthorRaw)
	}
	if reputation.Username != "ana_zanata" {
		t.Fatalf("Username = %q, want the canonical handle", reputation.Username)
	}
	if reputation.InfluencedPeople() != 3 || reputation.TotalValidAttributions() != 4 {
		t.Fatalf("derived facts = %d/%d, want 3/4", reputation.InfluencedPeople(), reputation.TotalValidAttributions())
	}
	if !reputation.CheckedAt.Equal(testInstant) {
		t.Fatalf("CheckedAt = %s, want the injected clock instant", reputation.CheckedAt)
	}
}

func TestGetProfileReputationIsAbsentWithoutAProfile(t *testing.T) {
	t.Parallel()

	t.Run("unknown username", func(t *testing.T) {
		directory := &fakeDirectory{err: application.ErrProfileNotFound}
		repo := &fakeReputationRepo{}
		useCase := application.NewGetProfileReputationUseCase(directory, repo, fixedClock{instant: testInstant})
		if _, err := useCase.Execute(context.Background(), application.GetProfileReputationQuery{Username: "ninguem"}); !errors.Is(err, application.ErrProfileNotFound) {
			t.Fatalf("Execute() error = %v, want ErrProfileNotFound", err)
		}
		if repo.calls != 0 {
			t.Fatal("the reputation port must not be read for a handle that owns no profile")
		}
	})

	t.Run("directory failure is not masked as absence", func(t *testing.T) {
		directory := &fakeDirectory{err: errors.New("storage offline")}
		useCase := application.NewGetProfileReputationUseCase(directory, &fakeReputationRepo{}, fixedClock{instant: testInstant})
		_, err := useCase.Execute(context.Background(), application.GetProfileReputationQuery{Username: "ana_zanata"})
		if err == nil || errors.Is(err, application.ErrProfileNotFound) {
			t.Fatalf("Execute() error = %v, want the storage failure, not an absence", err)
		}
	})

	t.Run("resolved handle without canonical username is a defect", func(t *testing.T) {
		directory := &fakeDirectory{handle: mustAuthorHandle(t, "  ")}
		useCase := application.NewGetProfileReputationUseCase(directory, &fakeReputationRepo{}, fixedClock{instant: testInstant})
		_, err := useCase.Execute(context.Background(), application.GetProfileReputationQuery{Username: "ana_zanata"})
		if err == nil || errors.Is(err, application.ErrProfileNotFound) {
			t.Fatalf("Execute() error = %v, want a port-contract defect", err)
		}
	})

	t.Run("reputation failure propagates", func(t *testing.T) {
		directory := &fakeDirectory{handle: mustAuthorHandle(t, "ana_zanata")}
		repo := &fakeReputationRepo{err: errors.New("storage offline")}
		useCase := application.NewGetProfileReputationUseCase(directory, repo, fixedClock{instant: testInstant})
		if _, err := useCase.Execute(context.Background(), application.GetProfileReputationQuery{Username: "ana_zanata"}); err == nil {
			t.Fatal("Execute() error = nil, want the storage failure")
		}
	})
}
