package domain_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

func medianPolicy() domain.MedianPolicy {
	return domain.MedianPolicy{MinSources: 3, MaxSpread: 100000}
}

func medianSighting(t *testing.T, id string, priceMinor int64) domain.Observation {
	t.Helper()
	source, err := domain.ParseSourceID(id)
	if err != nil {
		t.Fatalf("ParseSourceID: %v", err)
	}
	seen, err := domain.NewObservation(source, priceMinor, time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), []byte(`{}`))
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	return seen
}

// TestMedianPriceOrdersBeforeArithmetic proves the reference: the odd
// sample settles on its centre whatever the arrival order.
func TestMedianPriceOrdersBeforeArithmetic(t *testing.T) {
	t.Parallel()

	permutations := [][]int64{
		{35000000, 35050000, 34950000},
		{35000000, 34950000, 35050000},
		{35050000, 35000000, 34950000},
		{35050000, 34950000, 35000000},
		{34950000, 35000000, 35050000},
		{34950000, 35050000, 35000000},
	}
	for _, order := range permutations {
		sightings := []domain.Observation{
			medianSighting(t, "fonte-1", order[0]),
			medianSighting(t, "fonte-2", order[1]),
			medianSighting(t, "fonte-3", order[2]),
		}
		median, err := domain.MedianPrice(sightings, medianPolicy())
		if err != nil {
			t.Fatalf("MedianPrice(%v): %v", order, err)
		}
		if median.Int64() != 35000000 {
			t.Fatalf("MedianPrice(%v) = %d, want 35000000", order, median.Int64())
		}
	}
}

// TestMedianPriceFloorsEvenSamples proves the even sample settles
// down: the straddling milli never rounds up into a price nobody saw.
func TestMedianPriceFloorsEvenSamples(t *testing.T) {
	t.Parallel()

	sightings := []domain.Observation{
		medianSighting(t, "fonte-1", 35000000),
		medianSighting(t, "fonte-2", 35000001),
	}
	median, err := domain.MedianPrice(sightings, domain.MedianPolicy{MinSources: 2, MaxSpread: 100000})
	if err != nil {
		t.Fatalf("MedianPrice: %v", err)
	}
	if median.Int64() != 35000000 {
		t.Fatalf("median = %d, want floored 35000000", median.Int64())
	}
}

// TestMedianPriceSuspendsAnomalies proves every guard fails closed: an
// outlier, a thin round, an overflowing centre and a spread beyond the
// window suspend the quotation instead of pricing through doubt.
func TestMedianPriceSuspendsAnomalies(t *testing.T) {
	t.Parallel()

	t.Run("outlier", func(t *testing.T) {
		t.Parallel()
		sightings := []domain.Observation{
			medianSighting(t, "fonte-1", 35000000),
			medianSighting(t, "fonte-2", 35010000),
			medianSighting(t, "fonte-3", 99900000),
		}
		if _, err := domain.MedianPrice(sightings, medianPolicy()); !errors.Is(err, domain.ErrDivergentSources) {
			t.Fatalf("MedianPrice = %v, want ErrDivergentSources", err)
		}
	})
	t.Run("thin round", func(t *testing.T) {
		t.Parallel()
		sightings := []domain.Observation{
			medianSighting(t, "fonte-1", 35000000),
			medianSighting(t, "fonte-2", 35010000),
		}
		if _, err := domain.MedianPrice(sightings, medianPolicy()); !errors.Is(err, domain.ErrInsufficientSources) {
			t.Fatalf("MedianPrice = %v, want ErrInsufficientSources", err)
		}
	})
	t.Run("overflowing centre", func(t *testing.T) {
		t.Parallel()
		sightings := []domain.Observation{
			medianSighting(t, "fonte-1", math.MaxInt64-1),
			medianSighting(t, "fonte-2", math.MaxInt64),
		}
		policy := domain.MedianPolicy{MinSources: 2, MaxSpread: math.MaxInt64}
		if _, err := domain.MedianPrice(sightings, policy); !errors.Is(err, domain.ErrInvalidPrice) {
			t.Fatalf("MedianPrice = %v, want ErrInvalidPrice", err)
		}
	})
	t.Run("wide divergence", func(t *testing.T) {
		t.Parallel()
		sightings := []domain.Observation{
			medianSighting(t, "fonte-1", 34000000),
			medianSighting(t, "fonte-2", 35000000),
			medianSighting(t, "fonte-3", 36000000),
		}
		if _, err := domain.MedianPrice(sightings, medianPolicy()); !errors.Is(err, domain.ErrDivergentSources) {
			t.Fatalf("MedianPrice = %v, want ErrDivergentSources", err)
		}
	})
	t.Run("repeated source", func(t *testing.T) {
		t.Parallel()
		sightings := []domain.Observation{
			medianSighting(t, "fonte-1", 35000000),
			medianSighting(t, "fonte-1", 35000000),
			medianSighting(t, "fonte-2", 35000000),
		}
		if _, err := domain.MedianPrice(sightings, medianPolicy()); !errors.Is(err, domain.ErrDuplicateSource) {
			t.Fatalf("MedianPrice = %v, want ErrDuplicateSource", err)
		}
	})
	t.Run("empty round", func(t *testing.T) {
		t.Parallel()
		if _, err := domain.MedianPrice(nil, medianPolicy()); !errors.Is(err, domain.ErrInsufficientSources) {
			t.Fatalf("MedianPrice = %v, want ErrInsufficientSources", err)
		}
	})
	t.Run("void policy", func(t *testing.T) {
		t.Parallel()
		sightings := []domain.Observation{medianSighting(t, "fonte-1", 35000000)}
		if _, err := domain.MedianPrice(sightings, domain.MedianPolicy{}); !errors.Is(err, domain.ErrInvalidObservation) {
			t.Fatalf("MedianPrice = %v, want ErrInvalidObservation", err)
		}
	})
}
