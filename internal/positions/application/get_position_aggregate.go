package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// GetPositionAggregateQuery addresses the public aggregate of one Arena.
type GetPositionAggregateQuery struct {
	ArenaID string
}

// PositionAggregate is the privacy-safe public result: the initial and
// current distributions over eligible participants, the eligible total, the
// derivation instant and whether the sample was too small to publish. It
// carries no account identifier by construction.
type PositionAggregate struct {
	Initial    PositionDistribution
	Current    PositionDistribution
	Total      int64
	Suppressed bool
	CheckedAt  time.Time
}

// GetPositionAggregateUseCase derives the public aggregate of one Arena.
// Only eligible accounts count (active accounts, per
// docs/BUSINESS_RULES.md §7); below the configured threshold every count is
// withheld, so a small sample never reveals an individual position.
type GetPositionAggregateUseCase struct {
	aggregates PositionAggregateRepository
	policy     domain.AggregatePolicy
	clock      Clock
}

// NewGetPositionAggregateUseCase creates an instance of
// GetPositionAggregateUseCase.
func NewGetPositionAggregateUseCase(aggregates PositionAggregateRepository, policy domain.AggregatePolicy, clock Clock) *GetPositionAggregateUseCase {
	return &GetPositionAggregateUseCase{
		aggregates: aggregates,
		policy:     policy,
		clock:      clock,
	}
}

// Execute derives the aggregate.
func (uc *GetPositionAggregateUseCase) Execute(ctx context.Context, query GetPositionAggregateQuery) (*PositionAggregate, error) {
	if !uc.policy.IsValid() {
		return nil, domain.ErrInvalidPolicy
	}

	arenaID, err := domain.ParseArenaID(query.ArenaID)
	if err != nil {
		return nil, err
	}

	initial, current, err := uc.aggregates.CountEligiblePositions(ctx, arenaID)
	if err != nil {
		return nil, err
	}

	checkedAt := uc.clock.Now()
	total := initial.Total()
	if uc.policy.Suppresses(total) {
		// Insufficient sample: no count at all is published.
		return &PositionAggregate{
			Total:      0,
			Suppressed: true,
			CheckedAt:  checkedAt,
		}, nil
	}

	return &PositionAggregate{
		Initial:   initial,
		Current:   current,
		Total:     total,
		CheckedAt: checkedAt,
	}, nil
}
