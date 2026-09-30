package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// GetArgumentMetricsQuery addresses the counts of one argument.
type GetArgumentMetricsQuery struct {
	ArgumentID string
}

// GetArgumentMetricsUseCase answers the public count of one argument
// (P11-T06; REQ-PERS-05, REQ-PERS-06). It is a pure projection: nothing is
// cached, stored or scored, so any count can be recomputed from the source
// facts (REQ-INV-10), and attribution validity is always applied (REQ-PERS-08)
// because validity lives in the query, never in the projection.
type GetArgumentMetricsUseCase struct {
	metrics ArgumentMetricsRepository
	clock   Clock
}

// NewGetArgumentMetricsUseCase creates an instance of
// GetArgumentMetricsUseCase.
func NewGetArgumentMetricsUseCase(metrics ArgumentMetricsRepository, clock Clock) *GetArgumentMetricsUseCase {
	return &GetArgumentMetricsUseCase{metrics: metrics, clock: clock}
}

// Execute derives the public counts of the argument. The identifier is
// validated before the port is touched, so an unusable identifier never
// reaches the database.
func (uc *GetArgumentMetricsUseCase) Execute(ctx context.Context, query GetArgumentMetricsQuery) (*ArgumentMetrics, error) {
	argumentID, err := domain.ParseArgumentID(query.ArgumentID)
	if err != nil {
		return nil, err
	}

	metrics, err := uc.metrics.GetArgumentMetrics(ctx, argumentID)
	if err != nil {
		return nil, err
	}
	if metrics == nil {
		return nil, ErrArgumentNotFound
	}

	projection := *metrics
	projection.ArgumentID = argumentID
	projection.CheckedAt = uc.clock.Now().UTC()
	if err := projection.Validate(); err != nil {
		return nil, err
	}
	return &projection, nil
}
