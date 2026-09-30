package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/application"
	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// stubSource stands in for the HTTP adapters where no network behavior
// is under test: canned sightings and failures prove the registry and
// the round tolerance.
type stubSource struct {
	id      domain.SourceID
	ssten   domain.Observation
	err     error
	fetches int
}

func (s *stubSource) ID() domain.SourceID { return s.id }

func (s *stubSource) Fetch(context.Context) (domain.Observation, error) {
	s.fetches++
	if s.err != nil {
		return domain.Observation{}, s.err
	}
	return s.ssten, nil
}

func stubSighting(t *testing.T, id string, priceMinor int64, at time.Time) *stubSource {
	t.Helper()
	source, err := domain.ParseSourceID(id)
	if err != nil {
		t.Fatalf("ParseSourceID: %v", err)
	}
	seen, err := domain.NewObservation(source, priceMinor, at, []byte(`{}`))
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	return &stubSource{id: source, ssten: seen}
}

func collectInstant() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

func collectLimits() domain.ObservationLimits {
	return domain.ObservationLimits{MaxFutureSkew: time.Minute, MaxAge: 5 * time.Minute}
}

func TestRegistryRefusesDuplicates(t *testing.T) {
	t.Parallel()

	registry := application.NewRegistry()
	first := stubSighting(t, "fonte-1", 35000000, collectInstant())
	if err := registry.Register(first); err != nil {
		t.Fatalf("Register: %v", err)
	}
	second := stubSighting(t, "fonte-1", 36000000, collectInstant())
	if err := registry.Register(second); !errors.Is(err, domain.ErrDuplicateSource) {
		t.Fatalf("duplicate Register = %v, want ErrDuplicateSource", err)
	}
	if err := registry.Register(nil); !errors.Is(err, domain.ErrInvalidSource) {
		t.Fatalf("nil Register = %v, want ErrInvalidSource", err)
	}
	if got := registry.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1 approved source", got)
	}
}

func TestCollectToleratesOneDarkSource(t *testing.T) {
	t.Parallel()

	registry := application.NewRegistry()
	healthy := stubSighting(t, "fonte-1", 35000000, collectInstant())
	dark := &stubSource{id: mustSourceID(t, "fonte-2"), err: domain.ErrSourceUnavailable}
	other := stubSighting(t, "fonte-3", 36000000, collectInstant())
	for _, source := range []*stubSource{healthy, dark, other} {
		if err := registry.Register(source); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	sightings, failures := registry.Collect(context.Background(), collectInstant(), collectLimits())
	if len(sightings) != 2 || len(failures) != 1 {
		t.Fatalf("sightings = %d, failures = %d; want 2 and 1", len(sightings), len(failures))
	}
	if failures[0].Source.String() != "fonte-2" || !errors.Is(failures[0].Err, domain.ErrSourceUnavailable) {
		t.Fatalf("failure not attributed: %+v", failures[0])
	}
	if healthy.fetches != 1 || dark.fetches != 1 || other.fetches != 1 {
		t.Fatalf("a source was skipped or repeated")
	}
}

func TestCollectRefusesStaleSightings(t *testing.T) {
	t.Parallel()

	registry := application.NewRegistry()
	aged := stubSighting(t, "fonte-1", 35000000, collectInstant().Add(-time.Hour))
	future := stubSighting(t, "fonte-2", 35000000, collectInstant().Add(time.Hour))
	for _, source := range []*stubSource{aged, future} {
		if err := registry.Register(source); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	sightings, failures := registry.Collect(context.Background(), collectInstant(), collectLimits())
	if len(sightings) != 0 || len(failures) != 2 {
		t.Fatalf("sightings = %d, failures = %d; want 0 and 2", len(sightings), len(failures))
	}
	for _, failure := range failures {
		if !errors.Is(failure.Err, domain.ErrStaleObservation) {
			t.Fatalf("failure = %v, want ErrStaleObservation", failure.Err)
		}
	}
}

func mustSourceID(t *testing.T, raw string) domain.SourceID {
	t.Helper()
	source, err := domain.ParseSourceID(raw)
	if err != nil {
		t.Fatalf("ParseSourceID: %v", err)
	}
	return source
}
