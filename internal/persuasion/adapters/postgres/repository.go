// Package postgres is the outbound PostgreSQL adapter of the persuasion
// module (P11-T03). It joins the caller transaction when the context
// carries one, so the change lock, the set-level checks and the attribution
// inserts commit or roll back together.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Repository implements the persuasion application ports using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *platformpg.Queries
}

var (
	_ application.AttributionRepository           = (*Repository)(nil)
	_ application.AttributionModerationRepository = (*Repository)(nil)
	_ application.AuthorReputationRepository      = (*Repository)(nil)
	_ application.ArgumentMetricsRepository       = (*Repository)(nil)
	_ application.AuthorDirectory                 = (*Repository)(nil)
	_ application.AbuseSignalFactRepository       = (*Repository)(nil)
)

// NewRepository creates a PostgreSQL repository adapter for persuasion.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
	}
}

// queriesFor binds the queries to the caller transaction when one is
// carried by the context; the FOR UPDATE lock only means anything inside
// the shared transaction.
func (r *Repository) queriesFor(ctx context.Context) *platformpg.Queries {
	if tx, ok := platformpg.TxFromContext(ctx); ok {
		return r.queries.WithTx(tx)
	}
	return r.queries
}

// LockChangeForAttributor loads the position change scoped to its account
// and locks it FOR UPDATE.
func (r *Repository) LockChangeForAttributor(ctx context.Context, changeID domain.ChangeID, attributorID domain.AttributorID) (*domain.Change, error) {
	changeParam, ok := uuidParam(changeID.String())
	if !ok {
		return nil, application.ErrChangeNotFound
	}
	attributorParam, ok := uuidParam(attributorID.String())
	if !ok {
		return nil, application.ErrChangeNotFound
	}

	row, err := r.queriesFor(ctx).GetPositionChangeForAttributor(ctx, platformpg.GetPositionChangeForAttributorParams{
		ChangeID:     changeParam,
		AttributorID: attributorParam,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrChangeNotFound
		}
		return nil, fmt.Errorf("lock position change: %w", err)
	}

	arenaID, err := domain.ParseArenaID(uuidToString(row.ArenaID))
	if err != nil {
		return nil, fmt.Errorf("stored arena id is invalid: %w", err)
	}
	accountID, err := domain.ParseAttributorID(uuidToString(row.AccountID))
	if err != nil {
		return nil, fmt.Errorf("stored attributor id is invalid: %w", err)
	}
	return &domain.Change{
		ID:           changeID,
		ArenaID:      arenaID,
		AttributorID: accountID,
		ChangedAt:    row.ChangedAt.Time,
	}, nil
}

// ListAttributedArgumentIDs returns the arguments already credited by the
// change.
func (r *Repository) ListAttributedArgumentIDs(ctx context.Context, changeID domain.ChangeID) ([]domain.ArgumentID, error) {
	changeParam, ok := uuidParam(changeID.String())
	if !ok {
		return nil, application.ErrChangeNotFound
	}

	rows, err := r.queriesFor(ctx).ListAttributionArgumentIDs(ctx, changeParam)
	if err != nil {
		return nil, fmt.Errorf("list attributed arguments: %w", err)
	}

	ids := make([]domain.ArgumentID, 0, len(rows))
	for _, row := range rows {
		argumentID, err := domain.ParseArgumentID(uuidToString(row))
		if err != nil {
			return nil, fmt.Errorf("stored argument id is invalid: %w", err)
		}
		ids = append(ids, argumentID)
	}
	return ids, nil
}

// ListCandidates loads the eligibility inputs of the proposed arguments.
func (r *Repository) ListCandidates(ctx context.Context, argumentIDs []domain.ArgumentID) ([]domain.Candidate, error) {
	if len(argumentIDs) == 0 {
		return nil, nil
	}
	params := make([]pgtype.UUID, 0, len(argumentIDs))
	for _, argumentID := range argumentIDs {
		param, ok := uuidParam(argumentID.String())
		if !ok {
			return nil, application.ErrArgumentNotFound
		}
		params = append(params, param)
	}

	rows, err := r.queriesFor(ctx).ListAttributionCandidates(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list attribution candidates: %w", err)
	}
	if len(rows) != len(argumentIDs) {
		// At least one proposed identifier does not exist.
		return nil, application.ErrArgumentNotFound
	}

	candidates := make([]domain.Candidate, 0, len(rows))
	for _, row := range rows {
		argumentID, err := domain.ParseArgumentID(uuidToString(row.ID))
		if err != nil {
			return nil, fmt.Errorf("stored argument id is invalid: %w", err)
		}
		arenaID, err := domain.ParseArenaID(uuidToString(row.ArenaID))
		if err != nil {
			return nil, fmt.Errorf("stored arena id is invalid: %w", err)
		}
		authorID, err := domain.ParseAuthorID(uuidToString(row.AuthorID))
		if err != nil {
			return nil, fmt.Errorf("stored author id is invalid: %w", err)
		}
		status := domain.ArgumentStatus(row.Status)
		if !status.IsValid() {
			return nil, fmt.Errorf("stored argument status is invalid: %q", row.Status)
		}
		candidates = append(candidates, domain.Candidate{
			ID:        argumentID,
			ArenaID:   arenaID,
			AuthorID:  authorID,
			CreatedAt: row.CreatedAt.Time,
			Status:    status,
		})
	}
	return candidates, nil
}

// CreateAttributions records the accepted candidates under the unique
// (change, argument) pair.
func (r *Repository) CreateAttributions(ctx context.Context, changeID domain.ChangeID, attributorID domain.AttributorID, candidates []domain.Candidate) error {
	changeParam, ok := uuidParam(changeID.String())
	if !ok {
		return application.ErrChangeNotFound
	}
	attributorParam, ok := uuidParam(attributorID.String())
	if !ok {
		return application.ErrChangeNotFound
	}

	for _, candidate := range candidates {
		argumentParam, ok := uuidParam(candidate.ID.String())
		if !ok {
			return application.ErrArgumentNotFound
		}
		_, err := r.queriesFor(ctx).CreateAttribution(ctx, platformpg.CreateAttributionParams{
			ChangeID:     changeParam,
			AttributorID: attributorParam,
			ArgumentID:   argumentParam,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Already attributed by a concurrent replay: nothing to do.
				continue
			}
			return fmt.Errorf("create attribution: %w", err)
		}
	}
	return nil
}

// LockAttributionForModeration loads the attribution with its current
// validity and latest decision and locks it FOR UPDATE, so concurrent
// decisions on the same row serialize.
func (r *Repository) LockAttributionForModeration(ctx context.Context, attributionID domain.AttributionID) (*domain.Attribution, error) {
	param, ok := uuidParam(attributionID.String())
	if !ok {
		return nil, application.ErrAttributionNotFound
	}

	row, err := r.queriesFor(ctx).GetAttributionForModeration(ctx, param)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrAttributionNotFound
		}
		return nil, fmt.Errorf("lock attribution: %w", err)
	}
	return moderationAttribution(row.ID, row.PositionChangeID, row.AttributorID, row.ArgumentID, row.Status, row.CreatedAt, row.ModerationReason, row.ModeratedBy, row.ModeratedAt)
}

// ApplyModerationDecision writes the decision and moves the validity to the
// action target under the guard of the validity that action requires.
func (r *Repository) ApplyModerationDecision(ctx context.Context, attributionID domain.AttributionID, decision domain.ModerationDecision) (*domain.Attribution, error) {
	attributionParam, ok := uuidParam(attributionID.String())
	if !ok {
		return nil, application.ErrAttributionNotFound
	}
	moderatorParam, ok := uuidParam(decision.Actor.String())
	if !ok {
		return nil, fmt.Errorf("moderator id is not a database identifier")
	}
	decidedAt := pgtype.Timestamptz{Time: decision.DecidedAt, Valid: true}

	switch decision.Action {
	case domain.ModerationActionInvalidate:
		row, err := r.queriesFor(ctx).InvalidateAttribution(ctx, platformpg.InvalidateAttributionParams{
			DecidedAt:     decidedAt,
			Reason:        decision.Reason.String(),
			ModeratorID:   moderatorParam,
			AttributionID: attributionParam,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, application.ErrModerationConflict
			}
			return nil, fmt.Errorf("invalidate attribution: %w", err)
		}
		return moderationAttribution(row.ID, row.PositionChangeID, row.AttributorID, row.ArgumentID, row.Status, row.CreatedAt, row.ModerationReason, row.ModeratedBy, row.ModeratedAt)
	case domain.ModerationActionRestore:
		row, err := r.queriesFor(ctx).RestoreAttribution(ctx, platformpg.RestoreAttributionParams{
			Reason:        decision.Reason.String(),
			ModeratorID:   moderatorParam,
			DecidedAt:     decidedAt,
			AttributionID: attributionParam,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, application.ErrModerationConflict
			}
			return nil, fmt.Errorf("restore attribution: %w", err)
		}
		return moderationAttribution(row.ID, row.PositionChangeID, row.AttributorID, row.ArgumentID, row.Status, row.CreatedAt, row.ModerationReason, row.ModeratedBy, row.ModeratedAt)
	default:
		return nil, domain.ErrInvalidModerationAction
	}
}

// moderationAttribution maps one moderation row into the domain entity. The
// three moderation queries project the same columns, so a single mapping
// keeps validity reading identical everywhere.
func moderationAttribution(
	id, positionChangeID, attributorID, argumentID pgtype.UUID,
	status string,
	createdAt pgtype.Timestamptz,
	reason pgtype.Text,
	moderatedBy pgtype.UUID,
	moderatedAt pgtype.Timestamptz,
) (*domain.Attribution, error) {
	attributionID, err := domain.ParseAttributionID(uuidToString(id))
	if err != nil {
		return nil, fmt.Errorf("stored attribution id is invalid: %w", err)
	}
	changeID, err := domain.ParseChangeID(uuidToString(positionChangeID))
	if err != nil {
		return nil, fmt.Errorf("stored change id is invalid: %w", err)
	}
	attributor, err := domain.ParseAttributorID(uuidToString(attributorID))
	if err != nil {
		return nil, fmt.Errorf("stored attributor id is invalid: %w", err)
	}
	argument, err := domain.ParseArgumentID(uuidToString(argumentID))
	if err != nil {
		return nil, fmt.Errorf("stored argument id is invalid: %w", err)
	}
	validity, err := domain.ParseAttributionStatus(status)
	if err != nil {
		return nil, fmt.Errorf("stored attribution status is invalid: %w", err)
	}
	if reason.Valid != moderatedBy.Valid {
		return nil, fmt.Errorf("stored moderation decision is incoherent")
	}

	attribution := &domain.Attribution{
		ID:           attributionID,
		ChangeID:     changeID,
		ArgumentID:   argument,
		AttributorID: attributor,
		Status:       validity,
		CreatedAt:    createdAt.Time,
	}
	if reason.Valid {
		decisionReason, err := domain.ParseReason(reason.String)
		if err != nil {
			return nil, fmt.Errorf("stored moderation reason is invalid: %w", err)
		}
		actor, err := domain.ParseModeratorID(uuidToString(moderatedBy))
		if err != nil {
			return nil, fmt.Errorf("stored moderator id is invalid: %w", err)
		}
		// The recorded action is the one that produced the current validity:
		// an invalid attribution was invalidated, a valid one with a decision
		// was restored.
		action := domain.ModerationActionInvalidate
		if validity == domain.AttributionStatusValid {
			action = domain.ModerationActionRestore
		}
		attribution.Decision = domain.ModerationDecision{
			Action:    action,
			Actor:     actor,
			Reason:    decisionReason,
			DecidedAt: moderatedAt.Time,
		}
	}
	return attribution, nil
}

// ListAuthorArenaReputation derives the per-Arena reputation projection of
// one author: the eligible people influenced there and the valid attribution
// events received there. Counts only leave the database.
func (r *Repository) ListAuthorArenaReputation(ctx context.Context, authorID domain.AuthorID) ([]application.ArenaReputation, error) {
	param, ok := uuidParam(authorID.String())
	if !ok {
		return nil, application.ErrInvalidAuthorID
	}

	rows, err := r.queriesFor(ctx).ListAuthorArenaReputation(ctx, param)
	if err != nil {
		return nil, fmt.Errorf("list author arena reputation: %w", err)
	}

	arenas := make([]application.ArenaReputation, 0, len(rows))
	for _, row := range rows {
		arenaID, err := domain.ParseArenaID(uuidToString(row.ArenaID))
		if err != nil {
			return nil, fmt.Errorf("stored arena id is invalid: %w", err)
		}
		arenas = append(arenas, application.ArenaReputation{
			ArenaID:           arenaID,
			Category:          row.Category,
			Language:          row.Language,
			DistinctPeople:    row.DistinctPeople,
			ValidAttributions: row.ValidAttributions,
		})
	}
	return arenas, nil
}

// GetArgumentMetrics derives the public count facts of one argument: the
// eligible people who credited it and the valid attribution events it
// received. An argument without eligible attributions yields zeroed counts;
// an identifier that addresses no argument reports ErrArgumentNotFound.
func (r *Repository) GetArgumentMetrics(ctx context.Context, argumentID domain.ArgumentID) (*application.ArgumentMetrics, error) {
	param, ok := uuidParam(argumentID.String())
	if !ok {
		return nil, application.ErrArgumentNotFound
	}

	row, err := r.queriesFor(ctx).GetArgumentAttributionMetrics(ctx, param)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrArgumentNotFound
		}
		return nil, fmt.Errorf("get argument attribution metrics: %w", err)
	}

	return &application.ArgumentMetrics{
		ArgumentID:        argumentID,
		DistinctPeople:    row.DistinctPeople,
		ValidAttributions: row.ValidAttributions,
	}, nil
}

// ResolveAuthor resolves a public username to the author who owns it,
// matching the canonical normalized username of the profiles projection. Only
// the resolved identity leaves the port: the profile fields stay in their
// owning module.
func (r *Repository) ResolveAuthor(ctx context.Context, username string) (application.AuthorHandle, error) {
	row, err := r.queriesFor(ctx).ResolveAuthorByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.AuthorHandle{}, application.ErrProfileNotFound
		}
		return application.AuthorHandle{}, fmt.Errorf("resolve author by username: %w", err)
	}

	authorID, err := domain.ParseAuthorID(uuidToString(row.AccountID))
	if err != nil {
		return application.AuthorHandle{}, fmt.Errorf("stored author id is invalid: %w", err)
	}
	return application.AuthorHandle{AuthorID: authorID, Username: row.Username}, nil
}

// uuidParam parses a canonical UUID string into its database parameter.
func uuidParam(raw string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		return pgtype.UUID{}, false
	}
	return id, id.Valid
}

// uuidToString renders a database UUID in canonical form.
func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
