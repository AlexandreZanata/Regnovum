package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// errMissingCanonicalUsername reports an adapter that resolved a username
// without returning the canonical handle. The port contract requires it, so
// the failure surfaces as an internal defect instead of a public document
// with an empty identity.
var errMissingCanonicalUsername = errors.New("persuasion: resolved author has no canonical username")

// GetProfileReputationQuery addresses the public reputation of one profile.
type GetProfileReputationQuery struct {
	Username string
}

// GetProfileReputationUseCase answers the public reputation of one author
// addressed by the profile username (P11-T06; BR §6, REQ-PERS-06/07). The
// username is resolved to the author before any metric is read, so a handle
// that owns no profile is simply absent instead of deriving empty numbers for
// an arbitrary identity. The derived facts are the same projection the
// author-keyed use case returns, plus the canonical username the document is
// addressed by.
type GetProfileReputationUseCase struct {
	directory   AuthorDirectory
	reputations AuthorReputationRepository
	clock       Clock
}

// NewGetProfileReputationUseCase creates an instance of
// GetProfileReputationUseCase.
func NewGetProfileReputationUseCase(directory AuthorDirectory, reputations AuthorReputationRepository, clock Clock) *GetProfileReputationUseCase {
	return &GetProfileReputationUseCase{directory: directory, reputations: reputations, clock: clock}
}

// Execute resolves the username and derives the reputation projection.
func (uc *GetProfileReputationUseCase) Execute(ctx context.Context, query GetProfileReputationQuery) (*AuthorReputation, error) {
	handle, err := uc.directory.ResolveAuthor(ctx, query.Username)
	if err != nil {
		return nil, err
	}

	if handle.AuthorID.IsZero() || strings.TrimSpace(handle.Username) == "" {
		return nil, errMissingCanonicalUsername
	}

	reputation, err := deriveAuthorReputation(ctx, uc.reputations, handle.AuthorID, uc.clock.Now().UTC())
	if err != nil {
		return nil, err
	}

	// The canonical username is the identity the public document is
	// addressed by; it never replaces the internal author identifier used to
	// derive the facts.
	reputation.Username = handle.Username
	return reputation, nil
}

// deriveAuthorReputation loads and validates the per-Arena facts of one
// author. It is shared by the author-keyed and the username-keyed queries so
// both answer with exactly the same projection rules.
func deriveAuthorReputation(ctx context.Context, reputations AuthorReputationRepository, authorID domain.AuthorID, checkedAt time.Time) (*AuthorReputation, error) {
	arenas, err := reputations.ListAuthorArenaReputation(ctx, authorID)
	if err != nil {
		return nil, err
	}

	reputation := AuthorReputation{
		AuthorID:  authorID,
		Arenas:    arenas,
		CheckedAt: checkedAt,
	}
	if err := reputation.Validate(); err != nil {
		return nil, err
	}
	return &reputation, nil
}
