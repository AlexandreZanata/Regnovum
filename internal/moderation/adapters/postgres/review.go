package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

var _ application.CaseRepository = (*Repository)(nil)

// GetCase resolves the case with its target owner for conflict checks.
func (r *Repository) GetCase(ctx context.Context, caseID string) (*application.CaseRecord, error) {
	id, err := pgUUIDFromString(caseID)
	if err != nil {
		return nil, nil
	}
	row, err := r.queries.GetModerationCaseByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load moderation case: %w", err)
	}
	return mapCaseRow(row.ID, row.TargetType, row.TargetArenaID, row.TargetArgumentID, row.TargetAccountID, row.Status, row.ClaimedBy, row.LeaseExpiresAt, row.TargetOwnerID)
}

// ClaimCase moves an open case (or an expired lease) under the actor with a
// fresh lease. A live lease held by anyone else refuses without touching
// the row; a re-claim by the lease holder resolves the current claim.
func (r *Repository) ClaimCase(ctx context.Context, request application.ClaimCaseRequest) (*application.CaseRecord, error) {
	id, err := pgUUIDFromString(request.CaseID)
	if err != nil {
		return nil, application.ErrCaseNotFound
	}
	actor, err := pgUUIDFromAccountID(request.Actor)
	if err != nil {
		return nil, fmt.Errorf("claim case: %w", err)
	}
	lease := request.ClaimedAt.Add(request.LeaseDays)
	if request.LeaseDays <= 0 {
		lease = request.ClaimedAt.Add(domain.ClaimLease)
	}

	row, err := r.queries.ClaimModerationCase(ctx, platformpg.ClaimModerationCaseParams{
		ID:               id,
		ClaimedBy:        actor,
		ClaimedAt:        pgtype.Timestamptz{Time: request.ClaimedAt.UTC(), Valid: true},
		LeaseExpiresAt:   pgtype.Timestamptz{Time: lease.UTC(), Valid: true},
		LeaseExpiresAt_2: pgtype.Timestamptz{Time: request.ClaimedAt.UTC(), Valid: true},
	})
	if err == nil {
		return mapClaimRow(row.ID, row.TargetType, row.TargetArenaID, row.TargetArgumentID, row.TargetAccountID, row.Status, row.ClaimedBy, row.LeaseExpiresAt)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("claim case: %w", err)
	}

	current, err := r.GetCase(ctx, request.CaseID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, application.ErrCaseNotFound
	}
	if current.Status != application.CaseUnderReview && current.Status != application.CaseOpen {
		return nil, application.ErrInvalidCaseTransition
	}
	if current.Status == application.CaseUnderReview && current.ClaimedBy == request.Actor && current.LeaseExpiresAt != nil && current.LeaseExpiresAt.After(request.ClaimedAt) {
		return current, nil
	}
	return nil, application.ErrCaseAlreadyClaimed
}

// DecideCase records one action and moves the case to decided while
// clearing the claim, atomically. The action row and the case transition
// commit together or not at all.
func (r *Repository) DecideCase(ctx context.Context, request application.DecideCaseRequest) (*application.DecisionRecord, error) {
	id, err := pgUUIDFromString(request.CaseID)
	if err != nil {
		return nil, application.ErrCaseNotFound
	}
	actor, err := pgUUIDFromAccountID(request.Actor)
	if err != nil {
		return nil, fmt.Errorf("decide case: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin decide transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := r.queries.WithTx(tx)

	current, err := qtx.GetModerationCaseByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrCaseNotFound
		}
		return nil, fmt.Errorf("load case for decision: %w", err)
	}
	if current.Status != "under_review" {
		return nil, application.ErrInvalidCaseTransition
	}
	if !uuidEquals(current.ClaimedBy, actor) {
		return nil, application.ErrCaseAlreadyClaimed
	}
	if !current.LeaseExpiresAt.Valid || !current.LeaseExpiresAt.Time.After(request.DecidedAt) {
		return nil, application.ErrLeaseExpired
	}

	// Defense in depth: the use case already checked the sanction matrix,
	// but a direct caller must face the same rule.
	caseTarget, err := domain.ParseTargetType(current.TargetType)
	if err != nil {
		return nil, fmt.Errorf("stored case target is invalid: %w", err)
	}
	if !domain.SanctionAllowed(caseTarget, request.Action) {
		return nil, fmt.Errorf("%w: %s cannot sanction %s", domain.ErrTargetActionMismatch, request.Action, caseTarget)
	}

	action, err := qtx.CreateModerationAction(ctx, platformpg.CreateModerationActionParams{
		CaseID:        id,
		ActionType:    request.Action.String(),
		ActorID:       actor,
		RuleApplied:   request.Rule,
		Justification: request.Justification,
		ExpiresAt:     timestamptz(request.ExpiresAt),
	})
	if err != nil {
		return nil, fmt.Errorf("record moderation action: %w", err)
	}

	// The sanction effect joins the same transaction as the audit event:
	// either the projection moves and the action is recorded, or nothing
	// persists. Record-only actions skip this step explicitly.
	if err := applySanctionEffect(ctx, qtx, current, request, actor); err != nil {
		return nil, err
	}

	if _, err := qtx.DecideModerationCase(ctx, platformpg.DecideModerationCaseParams{
		ID:             id,
		ClaimedBy:      actor,
		LeaseExpiresAt: pgtype.Timestamptz{Time: request.DecidedAt.UTC(), Valid: true},
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrLeaseExpired
		}
		return nil, fmt.Errorf("decide case: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit decide transaction: %w", err)
	}

	return &application.DecisionRecord{
		ActionID: uuidToString(action.ID),
		CaseID:   request.CaseID,
		Actor:    request.Actor,
		Action:   request.Action,
	}, nil
}

func mapCaseRow(id pgtype.UUID, targetType string, arena, argument, account pgtype.UUID, status string, claimedBy pgtype.UUID, lease pgtype.Timestamptz, owner pgtype.UUID) (*application.CaseRecord, error) {
	target, err := domain.ParseTargetType(targetType)
	if err != nil {
		return nil, fmt.Errorf("stored case target is invalid: %w", err)
	}
	targetID := uuidToString(arena)
	if target == domain.TargetArgument {
		targetID = uuidToString(argument)
	} else if target == domain.TargetProfile {
		targetID = uuidToString(account)
	}
	ownerID, err := domainAccountID(owner)
	if err != nil {
		ownerID = ""
	}
	var leaseAt *time.Time
	if lease.Valid {
		instant := lease.Time.UTC()
		leaseAt = &instant
	}
	var claimant domain.AccountID
	if claimedBy.Valid {
		claimant = domain.AccountID(uuidToString(claimedBy))
	}
	return &application.CaseRecord{
		ID:             uuidToString(id),
		Target:         target,
		TargetID:       targetID,
		TargetOwner:    ownerID,
		Status:         application.CaseStatus(status),
		ClaimedBy:      claimant,
		LeaseExpiresAt: leaseAt,
	}, nil
}

func mapClaimRow(id pgtype.UUID, targetType string, arena, argument, account pgtype.UUID, status string, claimedBy pgtype.UUID, lease pgtype.Timestamptz) (*application.CaseRecord, error) {
	target, err := domain.ParseTargetType(targetType)
	if err != nil {
		return nil, fmt.Errorf("stored case target is invalid: %w", err)
	}
	targetID := uuidToString(arena)
	if target == domain.TargetArgument {
		targetID = uuidToString(argument)
	} else if target == domain.TargetProfile {
		targetID = uuidToString(account)
	}
	var leaseAt *time.Time
	if lease.Valid {
		instant := lease.Time.UTC()
		leaseAt = &instant
	}
	return &application.CaseRecord{
		ID:             uuidToString(id),
		Target:         target,
		TargetID:       targetID,
		Status:         application.CaseStatus(status),
		ClaimedBy:      domain.AccountID(uuidToString(claimedBy)),
		LeaseExpiresAt: leaseAt,
	}, nil
}

func uuidEquals(a, b pgtype.UUID) bool {
	if !a.Valid || !b.Valid {
		return false
	}
	return a.Bytes == b.Bytes
}

// applySanctionEffect moves the sanctioned projection inside the decision
// transaction. It covers exactly the mutating actions of
// domain.Action.MutatesProjection; every other action is recorded only, as
// documented on the sanction matrix. A target that left the sanctionable
// state concurrently fails the conditional statement, which aborts the
// whole decision instead of recording a phantom sanction.
func applySanctionEffect(ctx context.Context, qtx *platformpg.Queries, current platformpg.GetModerationCaseByIDRow, request application.DecideCaseRequest, actor pgtype.UUID) error {
	switch request.Action {
	case domain.ActionArenaClose:
		if _, err := qtx.CloseArenaForModeration(ctx, current.TargetArenaID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("apply arena close: %w", application.ErrInvalidCaseTransition)
			}
			return fmt.Errorf("apply arena close: %w", err)
		}
		return nil
	case domain.ActionArgumentRemove:
		if _, err := qtx.RemoveArgumentForModeration(ctx, current.TargetArgumentID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("apply argument remove: %w", application.ErrInvalidCaseTransition)
			}
			return fmt.Errorf("apply argument remove: %w", err)
		}
		return nil
	case domain.ActionAttributionInvalidate:
		if _, err := qtx.InvalidateArgumentAttributions(ctx, platformpg.InvalidateArgumentAttributionsParams{
			ArgumentID:       current.TargetArgumentID,
			ModerationReason: pgtype.Text{String: request.Rule, Valid: true},
			ModeratedBy:      actor,
		}); err != nil {
			return fmt.Errorf("apply attribution invalidate: %w", err)
		}
		return nil
	case domain.ActionSuspension, domain.ActionBan:
		if _, err := qtx.SuspendAccountForModeration(ctx, current.TargetAccountID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("apply account suspension: %w", application.ErrInvalidCaseTransition)
			}
			return fmt.Errorf("apply account suspension: %w", err)
		}
		return nil
	default:
		return nil
	}
}

func timestamptz(instant *time.Time) pgtype.Timestamptz {
	if instant == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: instant.UTC(), Valid: true}
}
