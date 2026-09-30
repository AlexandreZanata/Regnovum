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

var (
	_ application.CaseQueueRepository = (*Repository)(nil)
	_ application.SessionAgeDirectory = (*Repository)(nil)
)

// ListQueuePage returns one keyset page of case routing, newest first. It
// projects routing only: target, lifecycle, priority, claim holder and
// instants. Restricted evidence never leaves this query.
func (r *Repository) ListQueuePage(ctx context.Context, status string, after *application.QueuePosition, limit int) ([]application.QueueItem, error) {
	var afterCreatedAt pgtype.Timestamptz
	var afterID pgtype.UUID
	if after != nil {
		afterCreatedAt = pgtype.Timestamptz{Time: after.CreatedAt.UTC(), Valid: true}
		var err error
		afterID, err = pgUUIDFromString(after.CaseID)
		if err != nil {
			return nil, fmt.Errorf("list queue page: %w", err)
		}
	}

	rows, err := r.queries.ListModerationCasesPage(ctx, platformpg.ListModerationCasesPageParams{
		Status:         status,
		AfterCreatedAt: afterCreatedAt,
		AfterID:        afterID,
		PageLimit:      int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list queue page: %w", err)
	}

	items := make([]application.QueueItem, 0, len(rows))
	for _, row := range rows {
		target, err := domain.ParseTargetType(row.TargetType)
		if err != nil {
			return nil, fmt.Errorf("stored queue target is invalid: %w", err)
		}
		targetID := uuidToString(row.TargetArenaID)
		if target == domain.TargetArgument {
			targetID = uuidToString(row.TargetArgumentID)
		} else if target == domain.TargetProfile {
			targetID = uuidToString(row.TargetAccountID)
		}
		var claimedBy domain.AccountID
		if row.ClaimedBy.Valid {
			claimedBy = domain.AccountID(uuidToString(row.ClaimedBy))
		}
		items = append(items, application.QueueItem{
			CaseID:    uuidToString(row.ID),
			Target:    target,
			TargetID:  targetID,
			Status:    application.CaseStatus(row.Status),
			Priority:  row.Priority,
			CreatedAt: row.CreatedAt.Time.UTC(),
			ClaimedBy: claimedBy,
		})
	}
	return items, nil
}

// MFAVerifiedAt reports when the session last presented a second factor, and
// whether it ever did (P16-T05).
//
// A session that never presented one is not an error and not a denial by
// itself: it is a fact about the session, and the authorization rule that
// refuses it lives in the HTTP layer, next to the assignment check it
// complements. An unknown or malformed identifier denies distinctly, exactly
// as the age lookup does.
func (r *Repository) MFAVerifiedAt(ctx context.Context, sessionID string) (time.Time, bool, error) {
	var id pgtype.UUID
	if err := id.Scan(sessionID); err != nil {
		return time.Time{}, false, application.ErrUnknownSession
	}

	verifiedAt, err := r.queries.GetSessionMFAVerifiedAt(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, application.ErrUnknownSession
		}
		return time.Time{}, false, fmt.Errorf("load session mfa instant: %w", err)
	}
	if !verifiedAt.Valid {
		return time.Time{}, false, nil
	}
	return verifiedAt.Time.UTC(), true, nil
}

// SessionAgeAt returns how long ago the session authenticated, as of now.
// Unknown or malformed session identifiers deny distinctly instead of
// being treated as fresh.
func (r *Repository) SessionAgeAt(ctx context.Context, sessionID string, now time.Time) (time.Duration, error) {
	id, err := pgUUIDFromString(sessionID)
	if err != nil {
		return 0, application.ErrUnknownSession
	}
	createdAt, err := r.queries.GetSessionCreatedAt(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, application.ErrUnknownSession
		}
		return 0, fmt.Errorf("load session age: %w", err)
	}
	age := now.UTC().Sub(createdAt.Time.UTC())
	if age < 0 {
		age = 0
	}
	return age, nil
}
