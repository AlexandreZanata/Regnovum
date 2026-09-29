package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

func observationInstant() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

func observationLimits() domain.ObservationLimits {
	return domain.ObservationLimits{MaxFutureSkew: time.Minute, MaxAge: 5 * time.Minute}
}

func mustObservation(t *testing.T) domain.Observation {
	t.Helper()
	seen, err := domain.NewObservation("fonte-1", 35000000, observationInstant(), []byte(`{"price_minor":35000000}`))
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	return seen
}

func TestNewObservationBindsPayload(t *testing.T) {
	t.Parallel()

	seen := mustObservation(t)
	if seen.Price.Int64() != 35000000 || seen.Source.String() != "fonte-1" {
		t.Fatalf("sighting changed: %+v", seen)
	}
	if !seen.ObservedAt.Equal(observationInstant()) {
		t.Fatalf("instant not carried: %+v", seen.ObservedAt)
	}
	if seen.PayloadHash == "" {
		t.Fatalf("payload hash missing")
	}
	if err := seen.VerifyProvenance([]byte(`{"price_minor":35000000}`)); err != nil {
		t.Fatalf("provenance of own bytes: %v", err)
	}
	if err := seen.VerifyProvenance([]byte(`{"price_minor":35000001}`)); !errors.Is(err, domain.ErrInvalidObservation) {
		t.Fatalf("adulterated payload = %v, want ErrInvalidObservation", err)
	}
}

func TestNewObservationRefusesMalformed(t *testing.T) {
	t.Parallel()

	if _, err := domain.NewObservation("", 35000000, observationInstant(), []byte(`{}`)); !errors.Is(err, domain.ErrInvalidObservation) {
		t.Errorf("sourceless = %v, want ErrInvalidObservation", err)
	}
	if _, err := domain.NewObservation("fonte-1", 0, observationInstant(), []byte(`{}`)); !errors.Is(err, domain.ErrInvalidPrice) {
		t.Errorf("zero price = %v, want ErrInvalidPrice", err)
	}
	if _, err := domain.NewObservation("fonte-1", -5, observationInstant(), []byte(`{}`)); !errors.Is(err, domain.ErrInvalidPrice) {
		t.Errorf("negative price = %v, want ErrInvalidPrice", err)
	}
	if _, err := domain.NewObservation("fonte-1", 35000000, time.Time{}, []byte(`{}`)); !errors.Is(err, domain.ErrInvalidObservation) {
		t.Errorf("zero instant = %v, want ErrInvalidObservation", err)
	}
	if _, err := domain.NewObservation("fonte-1", 35000000, observationInstant(), nil); !errors.Is(err, domain.ErrInvalidObservation) {
		t.Errorf("empty payload = %v, want ErrInvalidObservation", err)
	}
}

func TestObservationFreshnessWindow(t *testing.T) {
	t.Parallel()

	now := observationInstant()
	limits := observationLimits()
	fresh := func(at time.Time) error {
		seen, err := domain.NewObservation("fonte-1", 35000000, at, []byte(`{}`))
		if err != nil {
			t.Fatalf("NewObservation: %v", err)
		}
		return seen.Fresh(now, limits)
	}
	for _, at := range []time.Time{
		now.Add(-5 * time.Minute),
		now.Add(-5*time.Minute + time.Second),
		now,
		now.Add(time.Minute - time.Second),
	} {
		if err := fresh(at); err != nil {
			t.Errorf("Fresh(%v) = %v, want nil", at.Sub(now), err)
		}
	}
	for _, at := range []time.Time{
		now.Add(-5*time.Minute - time.Second),
		now.Add(time.Minute),
		now.Add(time.Hour),
	} {
		if err := fresh(at); !errors.Is(err, domain.ErrStaleObservation) {
			t.Errorf("Fresh(%v) = %v, want ErrStaleObservation", at.Sub(now), err)
		}
	}
	if err := mustObservation(t).Fresh(now, domain.ObservationLimits{}); !errors.Is(err, domain.ErrInvalidObservation) {
		t.Errorf("zero limits = %v, want ErrInvalidObservation", err)
	}
}
