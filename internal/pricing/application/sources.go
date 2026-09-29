package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// Source is one approved rate source: its identity and one fetch of
// its current sighting. Implementations live in adapters and never
// leak provider types past this port.
type Source interface {
	// ID names the approved source.
	ID() domain.SourceID
	// Fetch returns the source's current sighting with its provenance,
	// or reports the source unavailable. Malformed answers refuse
	// instead of guessing.
	Fetch(ctx context.Context) (domain.Observation, error)
}

// SourceFailure records one source that sat a round out: its identity
// and why, so callers can tell a quiet source from a healthy one.
type SourceFailure struct {
	Source domain.SourceID
	Err    error
}

// Registry holds the approved sources of one collection round:
// independent fetchers keyed once by identity. Approval is explicit
// registration; anything unregistered is never consulted.
type Registry struct {
	sources map[domain.SourceID]Source
	order   []domain.SourceID
}

// NewRegistry creates an empty source registry.
func NewRegistry() *Registry {
	return &Registry{sources: map[domain.SourceID]Source{}}
}

// Register approves one source for the round. Nil fetchers and second
// registrations of one identity refuse: a duplicated source would
// weigh twice in every later aggregation.
func (r *Registry) Register(source Source) error {
	if source == nil {
		return domain.ErrInvalidSource
	}
	id := source.ID()
	if id.String() == "" {
		return domain.ErrInvalidSource
	}
	if _, taken := r.sources[id]; taken {
		return domain.ErrDuplicateSource
	}
	r.sources[id] = source
	r.order = append(r.order, id)
	return nil
}

// Len reports how many sources the round consults.
func (r *Registry) Len() int { return len(r.order) }

// Collect fetches one sighting per approved source in registration
// order, judging freshness with the injected instant and windows. One
// unreachable or stale source never aborts the round: its failure is
// recorded beside the healthy sightings, and an empty round reports
// every failure instead of an invented price.
func (r *Registry) Collect(ctx context.Context, now time.Time, limits domain.ObservationLimits) ([]domain.Observation, []SourceFailure) {
	sightings := []domain.Observation{}
	failures := []SourceFailure{}
	for _, id := range r.order {
		seen, err := r.sources[id].Fetch(ctx)
		if err != nil {
			failures = append(failures, SourceFailure{Source: id, Err: err})
			continue
		}
		if err := seen.Fresh(now, limits); err != nil {
			failures = append(failures, SourceFailure{Source: id, Err: err})
			continue
		}
		sightings = append(sightings, seen)
	}
	return sightings, failures
}
