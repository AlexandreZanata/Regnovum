package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

var _ application.AppealStore = (*Repository)(nil)

// ActionForAppeal resolves the sanction with its case target and owner, or
// nil when no action carries the identifier.
func (r *Repository) ActionForAppeal(ctx context.Context, actionID string) (*application.SanctionedAction, error) {
	id, err := pgUUIDFromString(actionID)
	if err != nil {
		return nil, nil
	}
	row, err := r.queries.GetModerationActionForAppeal(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load action for appeal: %w", err)
	}
	action, err := domain.ParseAction(row.ActionType)
	if err != nil {
		return nil, fmt.Errorf("stored action type is invalid: %w", err)
	}
	target, err := domain.ParseTargetType(row.TargetType)
	if err != nil {
		return nil, fmt.Errorf("stored case target is invalid: %w", err)
	}
	targetID := uuidToString(row.TargetArenaID)
	if target == domain.TargetArgument {
		targetID = uuidToString(row.TargetArgumentID)
	} else if target == domain.TargetProfile {
		targetID = uuidToString(row.TargetAccountID)
	}
	owner, err := domainAccountID(row.TargetOwnerID)
	if err != nil {
		owner = ""
	}
	actor, err := domainAccountID(row.ActorID)
	if err != nil {
		return nil, fmt.Errorf("stored action actor is invalid: %w", err)
	}
	return &application.SanctionedAction{
		ActionID:    uuidToString(row.ID),
		Action:      action,
		Actor:       actor,
		Rule:        row.RuleApplied,
		CreatedAt:   row.CreatedAt.Time.UTC(),
		CaseID:      uuidToString(row.CaseID),
		Target:      target,
		TargetID:    targetID,
		TargetOwner: owner,
	}, nil
}

// AppealByAction resolves the appeal contesting the action, or nil when the
// action carries none yet.
func (r *Repository) AppealByAction(ctx context.Context, actionID string) (*application.AppealRecord, error) {
	id, err := pgUUIDFromString(actionID)
	if err != nil {
		return nil, nil
	}
	row, err := r.queries.GetModerationAppealByAction(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load appeal by action: %w", err)
	}
	return mapAppealRow(row.ID, row.ActionID, row.AppellantID, row.Status, row.ReviewerID, row.DecidedAt)
}

// AppealByID resolves one appeal, or nil when unknown.
func (r *Repository) AppealByID(ctx context.Context, appealID string) (*application.AppealRecord, error) {
	id, err := pgUUIDFromString(appealID)
	if err != nil {
		return nil, nil
	}
	row, err := r.queries.GetModerationAppealByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load appeal: %w", err)
	}
	return mapAppealRow(row.ID, row.ActionID, row.AppellantID, row.Status, row.ReviewerID, row.DecidedAt)
}

// InsertAppeal files one fresh appeal in open state. A concurrent second
// contest of the same action collides on the UNIQUE constraint and denies
// distinctly: exactly one appeal contests one action.
func (r *Repository) InsertAppeal(ctx context.Context, request application.InsertAppealRequest) (*application.AppealRecord, error) {
	actionID, err := pgUUIDFromString(request.ActionID)
	if err != nil {
		return nil, fmt.Errorf("insert appeal: %w", err)
	}
	appellant, err := pgUUIDFromAccountID(request.Appellant)
	if err != nil {
		return nil, fmt.Errorf("insert appeal: %w", err)
	}
	row, err := r.queries.InsertModerationAppeal(ctx, platformpg.InsertModerationAppealParams{
		ActionID:    actionID,
		AppellantID: appellant,
		Context:     request.Context,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, application.ErrAppealDuplicate
		}
		return nil, fmt.Errorf("insert appeal: %w", err)
	}
	return mapAppealRow(row.ID, row.ActionID, row.AppellantID, row.Status, row.ReviewerID, row.DecidedAt)
}

// ClaimAppeal moves an open appeal to review. A concurrent claim finds no
// open row and refuses without touching the stored review.
func (r *Repository) ClaimAppeal(ctx context.Context, request application.ClaimAppealRequest) (*application.AppealRecord, error) {
	id, err := pgUUIDFromString(request.AppealID)
	if err != nil {
		return nil, application.ErrAppealNotFound
	}
	row, err := r.queries.ClaimModerationAppeal(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current, loadErr := r.AppealByID(ctx, request.AppealID)
			if loadErr != nil {
				return nil, loadErr
			}
			if current == nil {
				return nil, application.ErrAppealNotFound
			}
			return nil, application.ErrAppealAlreadyClaimed
		}
		return nil, fmt.Errorf("claim appeal: %w", err)
	}
	return mapAppealRow(row.ID, row.ActionID, row.AppellantID, row.Status, row.ReviewerID, row.DecidedAt)
}

// DecideAppeal records the outcome with reason and, on reversal, restores
// the sanctioned projection from the original action record, atomically.
// The original action row is never edited or deleted.
func (r *Repository) DecideAppeal(ctx context.Context, request application.DecideAppealRequest) (*application.AppealRecord, error) {
	id, err := pgUUIDFromString(request.AppealID)
	if err != nil {
		return nil, application.ErrAppealNotFound
	}
	reviewer, err := pgUUIDFromAccountID(request.Reviewer)
	if err != nil {
		return nil, fmt.Errorf("decide appeal: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin decide appeal transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := r.queries.WithTx(tx)

	appeal, err := qtx.GetModerationAppealByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrAppealNotFound
		}
		return nil, fmt.Errorf("load appeal for decision: %w", err)
	}
	if appeal.Status != "under_review" {
		return nil, application.ErrInvalidAppealTransition
	}

	action, err := qtx.GetModerationActionForAppeal(ctx, appeal.ActionID)
	if err != nil {
		return nil, fmt.Errorf("load action for reversal: %w", err)
	}
	// The deciding moderator never reviews, whoever claimed: handoff
	// between distinct reviewers stays allowed.
	if uuidEquals(action.ActorID, reviewer) {
		return nil, application.ErrSameReviewer
	}

	decided, err := qtx.DecideModerationAppeal(ctx, platformpg.DecideModerationAppealParams{
		ID:             id,
		ReviewerID:     reviewer,
		Status:         request.Outcome.String(),
		DecisionReason: pgtype.Text{String: request.Reason, Valid: true},
		DecidedAt:      pgtype.Timestamptz{Time: request.DecidedAt.UTC(), Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrInvalidAppealTransition
		}
		return nil, fmt.Errorf("decide appeal: %w", err)
	}

	if request.Outcome == domain.OutcomeReversed {
		if err := applyAppealReversal(ctx, qtx, action, request, reviewer); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit decide appeal transaction: %w", err)
	}
	return mapAppealRow(decided.ID, decided.ActionID, decided.AppellantID, decided.Status, decided.ReviewerID, decided.DecidedAt)
}

// applyAppealReversal restores the sanctioned projection from the original
// action record inside the decision transaction. Each statement is
// conditional: a target that left the sanctioned state aborts the whole
// outcome instead of recording a phantom restore.
func applyAppealReversal(ctx context.Context, qtx *platformpg.Queries, action platformpg.GetModerationActionForAppealRow, request application.DecideAppealRequest, reviewer pgtype.UUID) error {
	switch action.ActionType {
	case "arena_close":
		if _, err := qtx.ReopenArenaForAppeal(ctx, action.TargetArenaID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("reverse arena close: %w", application.ErrInvalidAppealTransition)
			}
			return fmt.Errorf("reverse arena close: %w", err)
		}
		return nil
	case "argument_remove":
		if _, err := qtx.RestoreArgumentForAppeal(ctx, action.TargetArgumentID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("reverse argument remove: %w", application.ErrInvalidAppealTransition)
			}
			return fmt.Errorf("reverse argument remove: %w", err)
		}
		return nil
	case "attribution_invalidate":
		if _, err := qtx.RestoreArgumentAttributions(ctx, platformpg.RestoreArgumentAttributionsParams{
			ArgumentID:       action.TargetArgumentID,
			ModerationReason: pgtype.Text{String: action.RuleApplied, Valid: true},
			ModeratedBy:      reviewer,
		}); err != nil {
			return fmt.Errorf("reverse attribution invalidate: %w", err)
		}
		return nil
	case "suspension", "ban":
		if _, err := qtx.ReactivateAccountForAppeal(ctx, action.TargetAccountID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("reverse account suspension: %w", application.ErrInvalidAppealTransition)
			}
			return fmt.Errorf("reverse account suspension: %w", err)
		}
		return nil
	default:
		return nil
	}
}

func mapAppealRow(id, actionID, appellantID pgtype.UUID, status string, reviewerID pgtype.UUID, decidedAt pgtype.Timestamptz) (*application.AppealRecord, error) {
	var reviewer domain.AccountID
	if reviewerID.Valid {
		reviewer = domain.AccountID(uuidToString(reviewerID))
	}
	var decided *time.Time
	if decidedAt.Valid {
		instant := decidedAt.Time.UTC()
		decided = &instant
	}
	appellant, err := domainAccountID(appellantID)
	if err != nil {
		return nil, fmt.Errorf("stored appeal appellant is invalid: %w", err)
	}
	return &application.AppealRecord{
		ID:        uuidToString(id),
		ActionID:  uuidToString(actionID),
		Appellant: appellant,
		Status:    application.AppealStatus(status),
		Reviewer:  reviewer,
		DecidedAt: decided,
	}, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
