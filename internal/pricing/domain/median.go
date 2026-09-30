package domain

import (
	"math"
	"sort"
)

// MedianPolicy bounds one reference computation: how many independent
// sightings a round needs at minimum and how far apart the furthest
// two may stand in minor units. Windows arrive per call: no ratified
// spread lives here, and later phases set the vigente values where
// quotes are accepted.
type MedianPolicy struct {
	MinSources int
	MaxSpread  int64
}

// Valid reports whether the policy bounds make sense: at least one
// source and a non-negative spread.
func (p MedianPolicy) Valid() bool {
	return p.MinSources >= 1 && p.MaxSpread >= 0
}

// MedianPrice computes the guarded BTC/BRL reference price of one
// round: the median of the sighting prices in integer minor units,
// floored when an even sample straddles two millis. Every guard fails
// closed and suspends the quotation instead of pricing through doubt:
// too few sightings, a repeated source, a spread beyond the window
// and an overflowing central sum all refuse. Permuting the sightings
// never moves the result: the sample is copied and ordered before any
// arithmetic, and float never appears on this path.
func MedianPrice(sightings []Observation, policy MedianPolicy) (PriceMinor, error) {
	if !policy.Valid() {
		return 0, ErrInvalidObservation
	}
	if len(sightings) < policy.MinSources {
		return 0, ErrInsufficientSources
	}
	prices := make([]int64, 0, len(sightings))
	seen := map[SourceID]bool{}
	var floor, ceiling int64
	for i, sighting := range sightings {
		if sighting.Source.String() == "" || sighting.Price.Int64() <= 0 {
			return 0, ErrInvalidObservation
		}
		if seen[sighting.Source] {
			return 0, ErrDuplicateSource
		}
		seen[sighting.Source] = true
		price := sighting.Price.Int64()
		if i == 0 || price < floor {
			floor = price
		}
		if i == 0 || price > ceiling {
			ceiling = price
		}
		prices = append(prices, price)
	}
	if ceiling-floor > policy.MaxSpread {
		return 0, ErrDivergentSources
	}
	sort.Slice(prices, func(i, j int) bool { return prices[i] < prices[j] })
	middle := len(prices) / 2
	if len(prices)%2 == 1 {
		return PriceMinor(prices[middle]), nil
	}
	low, high := prices[middle-1], prices[middle]
	if high > math.MaxInt64-low {
		return 0, ErrInvalidPrice
	}
	return PriceMinor((low + high) / 2), nil
}
