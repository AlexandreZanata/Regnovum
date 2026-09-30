package application_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/positions/application"
	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

type fakeAggregateRepo struct {
	initial  application.PositionDistribution
	current  application.PositionDistribution
	err      error
	calls    int
	lastAren domain.ArenaID
}

func (r *fakeAggregateRepo) CountEligiblePositions(_ context.Context, arenaID domain.ArenaID) (application.PositionDistribution, application.PositionDistribution, error) {
	r.calls++
	r.lastAren = arenaID
	if r.err != nil {
		return application.PositionDistribution{}, application.PositionDistribution{}, r.err
	}
	return r.initial, r.current, nil
}

func TestGetPositionAggregatePublishesEligibleDistributions(t *testing.T) {
	repo := &fakeAggregateRepo{
		initial: application.PositionDistribution{Agree: 7, Disagree: 3, Undecided: 2},
		current: application.PositionDistribution{Agree: 5, Disagree: 4, Undecided: 3},
	}
	useCase := application.NewGetPositionAggregateUseCase(repo, domain.DefaultAggregatePolicy(), fixedClock{now: testInstant})

	aggregate, err := useCase.Execute(context.Background(), application.GetPositionAggregateQuery{ArenaID: testArenaRaw})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if aggregate.Suppressed {
		t.Fatal("a population above the threshold must not be suppressed")
	}
	if aggregate.Total != 12 || aggregate.Initial.Total() != 12 || aggregate.Current.Total() != 12 {
		t.Fatalf("total = %d, want 12 for both dimensions", aggregate.Total)
	}
	if aggregate.Initial.Agree != 7 || aggregate.Current.Undecided != 3 {
		t.Fatalf("distributions = %+v / %+v, want the repository counts", aggregate.Initial, aggregate.Current)
	}
	if !aggregate.CheckedAt.Equal(testInstant) {
		t.Fatal("aggregate must carry the derivation instant from the clock")
	}
	if repo.calls != 1 || !repo.lastAren.Equals(mustArenaID(t)) {
		t.Fatal("aggregate must query the parsed arena once")
	}
}

func TestGetPositionAggregateSuppressesSmallSamples(t *testing.T) {
	policy := domain.DefaultAggregatePolicy()

	tests := []struct {
		name       string
		population int64
		suppressed bool
	}{
		{name: "empty", population: 0, suppressed: true},
		{name: "one below the threshold", population: policy.MinParticipants - 1, suppressed: true},
		{name: "exactly the threshold", population: policy.MinParticipants, suppressed: false},
		{name: "above the threshold", population: policy.MinParticipants + 5, suppressed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeAggregateRepo{
				initial: application.PositionDistribution{Agree: test.population},
				current: application.PositionDistribution{Agree: test.population},
			}
			useCase := application.NewGetPositionAggregateUseCase(repo, policy, fixedClock{now: testInstant})

			aggregate, err := useCase.Execute(context.Background(), application.GetPositionAggregateQuery{ArenaID: testArenaRaw})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if aggregate.Suppressed != test.suppressed {
				t.Fatalf("suppressed = %v, want %v", aggregate.Suppressed, test.suppressed)
			}
			if !aggregate.CheckedAt.Equal(testInstant) {
				t.Fatal("suppressed aggregates still carry the derivation instant")
			}
			if test.suppressed {
				if aggregate.Total != 0 || !reflect.DeepEqual(aggregate.Initial, application.PositionDistribution{}) || !reflect.DeepEqual(aggregate.Current, application.PositionDistribution{}) {
					t.Fatalf("suppressed aggregate leaked counts: %+v", aggregate)
				}
			} else if aggregate.Total != test.population {
				t.Fatalf("total = %d, want %d", aggregate.Total, test.population)
			}
		})
	}
}

func TestGetPositionAggregateValidatesPolicyAndQuery(t *testing.T) {
	tests := []struct {
		name   string
		policy domain.AggregatePolicy
		query  application.GetPositionAggregateQuery
		want   error
	}{
		{
			name:   "invalid policy",
			policy: domain.AggregatePolicy{Version: "", MinParticipants: 10},
			query:  application.GetPositionAggregateQuery{ArenaID: testArenaRaw},
			want:   domain.ErrInvalidPolicy,
		},
		{
			name:   "zero threshold",
			policy: domain.AggregatePolicy{Version: "2026-09", MinParticipants: 0},
			query:  application.GetPositionAggregateQuery{ArenaID: testArenaRaw},
			want:   domain.ErrInvalidPolicy,
		},
		{
			name:   "empty arena",
			policy: domain.DefaultAggregatePolicy(),
			query:  application.GetPositionAggregateQuery{},
			want:   domain.ErrEmptyArenaID,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeAggregateRepo{}
			useCase := application.NewGetPositionAggregateUseCase(repo, test.policy, fixedClock{now: testInstant})

			if _, err := useCase.Execute(context.Background(), test.query); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if repo.calls != 0 {
				t.Fatal("invalid policy or query must not touch the repository")
			}
		})
	}
}

func TestGetPositionAggregatePropagatesRepositoryFailures(t *testing.T) {
	storageErr := errors.New("storage down")
	repo := &fakeAggregateRepo{err: storageErr}
	useCase := application.NewGetPositionAggregateUseCase(repo, domain.DefaultAggregatePolicy(), fixedClock{now: testInstant})

	if _, err := useCase.Execute(context.Background(), application.GetPositionAggregateQuery{ArenaID: testArenaRaw}); !errors.Is(err, storageErr) {
		t.Fatalf("error = %v, want the storage failure", err)
	}
}

// TestPositionAggregateCarriesNoAccountIdentifiers is the P09-T05 structural
// privacy proof: the public aggregate types expose counts only, never an
// account identifier or any other internal reference.
func TestPositionAggregateCarriesNoAccountIdentifiers(t *testing.T) {
	forbidden := []string{"account", "creator", "user", "email", "uuid"}

	types := []reflect.Type{
		reflect.TypeOf(application.PositionAggregate{}),
		reflect.TypeOf(application.PositionDistribution{}),
	}
	for _, aggregateType := range types {
		for index := 0; index < aggregateType.NumField(); index++ {
			field := strings.ToLower(aggregateType.Field(index).Name)
			for _, marker := range forbidden {
				if strings.Contains(field, marker) {
					t.Errorf("PRODUCT VIOLATION: %s exposes field %q containing %q", aggregateType.Name(), aggregateType.Field(index).Name, marker)
				}
			}
		}
	}
}
