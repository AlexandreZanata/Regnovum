package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// AbuseSignalFactRepository loads the observations of one assessment: the
// reciprocity pairs, the per-account concentration and the per-account
// alternation inside the window. It is a read-only internal surface: the
// facts are restricted prevention data and never leave the module through a
// public projection (CONSTITUTION §Dados pessoais, THR-MOD-03).
type AbuseSignalFactRepository interface {
	// LoadAbuseSignalFacts returns the aggregated facts of one subject in the
	// half-open window [windowStart, windowEnd). An identifier that cannot
	// address an account at all reports ErrInvalidAuthorID; a subject without
	// recent facts returns empty slices, never an error — existence belongs to
	// the profile layer, not to the assessment.
	LoadAbuseSignalFacts(ctx context.Context, subject domain.AuthorID, windowStart, windowEnd time.Time) (*domain.SignalFacts, error)
}

// AssessAttributionSignalsQuery addresses one assessment: the acting
// moderator and the subject whose attribution pattern is read.
type AssessAttributionSignalsQuery struct {
	ActorAccountID string
	SubjectID      string
}

// GetAttributionSignalsUseCase derives the advisory abuse signals of one
// subject (P11-T07; THR-PERS-01, METRICS §4). It is moderator-only: the actor
// is authorized before any fact is read, so an unauthorized caller never
// observes even the existence of a signal. The assessment is pure and
// read-only — nothing is written, no weight is reweighted, no content is
// removed and no account is blocked (MODERATION §5, §10): a signal is a
// reason to look, and a human decides.
type GetAttributionSignalsUseCase struct {
	facts      AbuseSignalFactRepository
	authorizer ModerationAuthorizer
	policy     domain.SignalPolicy
	clock      Clock
}

// NewGetAttributionSignalsUseCase creates an instance of
// GetAttributionSignalsUseCase.
func NewGetAttributionSignalsUseCase(facts AbuseSignalFactRepository, authorizer ModerationAuthorizer, policy domain.SignalPolicy, clock Clock) *GetAttributionSignalsUseCase {
	return &GetAttributionSignalsUseCase{facts: facts, authorizer: authorizer, policy: policy, clock: clock}
}

// Execute authorizes the actor, loads the observations of the window ending
// now and assesses them under the injected policy.
func (uc *GetAttributionSignalsUseCase) Execute(ctx context.Context, query AssessAttributionSignalsQuery) (*domain.SignalAssessment, error) {
	if !uc.policy.IsValid() {
		return nil, domain.ErrInvalidSignalPolicy
	}

	actorID, err := domain.ParseModeratorID(query.ActorAccountID)
	if err != nil {
		return nil, err
	}
	subjectID, err := domain.ParseAuthorID(query.SubjectID)
	if err != nil {
		return nil, err
	}

	// Authorization precedes every read: an unauthorized caller must not even
	// learn whether a subject holds facts.
	if err := uc.authorizer.EnsureModerator(ctx, actorID); err != nil {
		return nil, err
	}

	assessedAt := uc.clock.Now().UTC()
	facts, err := uc.facts.LoadAbuseSignalFacts(ctx, subjectID, assessedAt.Add(-uc.policy.Window), assessedAt)
	if err != nil {
		return nil, err
	}
	if facts == nil {
		facts = &domain.SignalFacts{}
	}

	signals, err := uc.policy.Assess(subjectID, *facts)
	if err != nil {
		return nil, err
	}

	assessment := domain.SignalAssessment{
		Subject:       subjectID,
		Signals:       signals,
		PolicyVersion: uc.policy.Version,
		Window:        uc.policy.Window,
		AssessedAt:    assessedAt,
	}
	if err := assessment.Validate(); err != nil {
		return nil, err
	}
	return &assessment, nil
}
