package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// LoadAbuseSignalFacts aggregates, inside the half-open window
// [windowStart, windowEnd), the recipients credited by the subject, the
// subject's own attributions to other authors, and the position changes of
// every account that credited the subject. Only valid attributions count: an
// invalidated attribution is a closed case and never feeds a signal.
//
// No eligibility filter is applied on purpose: coordinated manipulation uses
// suspended and unverified accounts, and abuse review must not be blind to
// them. Those facts never reach a public projection (CONSTITUTION §Dados
// pessoais).
func (r *Repository) LoadAbuseSignalFacts(ctx context.Context, subject domain.AuthorID, windowStart, windowEnd time.Time) (*domain.SignalFacts, error) {
	param, ok := uuidParam(subject.String())
	if !ok {
		return nil, application.ErrInvalidAuthorID
	}
	if !windowEnd.After(windowStart) {
		// A non-positive window would silently aggregate nothing and look
		// like a clean subject.
		return nil, domain.ErrInvalidSignalFacts
	}

	reciprocityRows, err := r.queriesFor(ctx).ListAttributionReciprocity(ctx, platformpg.ListAttributionReciprocityParams{
		SubjectID:   param,
		WindowStart: timestamptzParam(windowStart),
		WindowEnd:   timestamptzParam(windowEnd),
	})
	if err != nil {
		return nil, fmt.Errorf("list attribution reciprocity: %w", err)
	}

	concentrationRows, err := r.queriesFor(ctx).ListAttributionConcentration(ctx, platformpg.ListAttributionConcentrationParams{
		SubjectID:   param,
		WindowStart: timestamptzParam(windowStart),
		WindowEnd:   timestamptzParam(windowEnd),
	})
	if err != nil {
		return nil, fmt.Errorf("list attribution concentration: %w", err)
	}

	alternationRows, err := r.queriesFor(ctx).ListAttributionAlternation(ctx, platformpg.ListAttributionAlternationParams{
		SubjectID:   param,
		WindowStart: timestamptzParam(windowStart),
		WindowEnd:   timestamptzParam(windowEnd),
	})
	if err != nil {
		return nil, fmt.Errorf("list attribution alternation: %w", err)
	}

	facts := &domain.SignalFacts{
		Reciprocity:   make([]domain.ReciprocityFact, 0, len(reciprocityRows)),
		Concentration: make([]domain.ConcentrationFact, 0, len(concentrationRows)),
		Alternation:   make([]domain.AlternationFact, 0, len(alternationRows)),
	}

	for _, row := range reciprocityRows {
		counterpart, err := domain.ParseAttributorID(uuidToString(row.AccountID))
		if err != nil {
			return nil, fmt.Errorf("stored counterpart id is invalid: %w", err)
		}
		facts.Reciprocity = append(facts.Reciprocity, domain.ReciprocityFact{
			Counterpart: counterpart,
			Inbound:     row.Inbound,
			Outbound:    row.Outbound,
		})
	}

	for _, row := range concentrationRows {
		attributor, err := domain.ParseAttributorID(uuidToString(row.AccountID))
		if err != nil {
			return nil, fmt.Errorf("stored attributor id is invalid: %w", err)
		}
		facts.Concentration = append(facts.Concentration, domain.ConcentrationFact{
			Attributor: attributor,
			Events:     row.Events,
		})
	}

	for _, row := range alternationRows {
		account, err := domain.ParseAttributorID(uuidToString(row.AccountID))
		if err != nil {
			return nil, fmt.Errorf("stored alternation account id is invalid: %w", err)
		}
		facts.Alternation = append(facts.Alternation, domain.AlternationFact{
			Account:   account,
			Changes:   row.Changes,
			Reversals: row.Reversals,
		})
	}

	return facts, nil
}

// timestamptzParam renders an instant as its database parameter. Instants are
// stored in UTC; the window bounds compare against the entries' own
// timestamptz values.
func timestamptzParam(instant time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: instant.UTC(), Valid: true}
}
