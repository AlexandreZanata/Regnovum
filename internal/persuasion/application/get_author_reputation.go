package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// GetAuthorReputationQuery addresses the reputation of one author.
type GetAuthorReputationQuery struct {
	AuthorID string
}

// GetAuthorReputationUseCase derives the factual reputation of one author
// from valid attributions (P11-T05; BR §6, REQ-PERS-06/07). It is a pure
// projection: nothing is cached, stored or scored, so any projection can be
// recomputed from the source facts (REQ-INV-10). An author without valid
// attributions derives zeroed facts instead of an error — existence belongs
// to the profile layer, not to the metric.
type GetAuthorReputationUseCase struct {
	reputations AuthorReputationRepository
	clock       Clock
}

// NewGetAuthorReputationUseCase creates an instance of
// GetAuthorReputationUseCase.
func NewGetAuthorReputationUseCase(reputations AuthorReputationRepository, clock Clock) *GetAuthorReputationUseCase {
	return &GetAuthorReputationUseCase{reputations: reputations, clock: clock}
}

// Execute derives the reputation projection of the author.
func (uc *GetAuthorReputationUseCase) Execute(ctx context.Context, query GetAuthorReputationQuery) (*AuthorReputation, error) {
	authorID, err := domain.ParseAuthorID(query.AuthorID)
	if err != nil {
		return nil, err
	}

	return deriveAuthorReputation(ctx, uc.reputations, authorID, uc.clock.Now().UTC())
}
