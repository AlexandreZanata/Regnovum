package application_test

// Guard of the historical aggregate against formal rites (P39-T03):
// an Arena is one debate instance with no winner and no official
// truth, so recomputation over the same reading always yields the
// same aggregate, and the published shape cannot carry a verdict at
// all. Whatever future formal rites record elsewhere, this
// historical aggregate never moves except through new eligible
// positions.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/positions/application"
	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

func TestHistoricalAggregateIgnoresFormalRites(t *testing.T) {
	repo := &fakeAggregateRepo{
		initial: application.PositionDistribution{Agree: 7, Disagree: 3, Undecided: 2},
		current: application.PositionDistribution{Agree: 5, Disagree: 4, Undecided: 3},
	}
	useCase := application.NewGetPositionAggregateUseCase(repo, domain.DefaultAggregatePolicy(), fixedClock{now: testInstant})
	query := application.GetPositionAggregateQuery{ArenaID: testArenaRaw}

	first, err := useCase.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	second, err := useCase.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if *first != *second {
		t.Fatalf("recomputed aggregate moved: %+v vs %+v; history is append-only", first, second)
	}
	if first.Initial.Agree != 7 || first.Current.Undecided != 3 || first.Total != 12 {
		t.Fatalf("aggregate = %+v, want the repository reading untouched by rites", first)
	}
}

func TestHistoricalAggregateCarriesNoVerdict(t *testing.T) {
	banned := []string{"winner", "verdict", "truth", "vencedor", "verdade"}
	for _, shape := range []reflect.Type{
		reflect.TypeOf(application.PositionAggregate{}),
		reflect.TypeOf(application.PositionDistribution{}),
	} {
		for i := 0; i < shape.NumField(); i++ {
			lower := strings.ToLower(shape.Field(i).Name)
			for _, bad := range banned {
				if strings.Contains(lower, bad) {
					t.Fatalf("field %s.%q carries %q: debates keep no official outcome",
						shape.Name(), shape.Field(i).Name, bad)
				}
			}
		}
	}
}
