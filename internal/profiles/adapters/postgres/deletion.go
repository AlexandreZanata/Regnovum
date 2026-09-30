package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

var (
	_ application.DeletionRepository = (*Repository)(nil)
)

// CreateDeletionRequest creates a new active request or replays the
// existing one. The partial unique index resolves concurrent requests: the
// loser re-reads the winner, so the cooldown never restarts.
func (r *Repository) CreateDeletionRequest(ctx context.Context, accountID domain.AccountID, requestedAt time.Time) (*application.DeletionRequest, bool, error) {
	accountUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, false, application.ErrDeletionRequestNotFound
	}

	row, err := r.queriesFor(ctx).CreateDeletionRequest(ctx, platformpg.CreateDeletionRequestParams{
		AccountID:   accountUUID,
		RequestedAt: timestamptz(requestedAt.UTC()),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			existing, readErr := r.GetDeletionRequest(ctx, accountID)
			if readErr != nil {
				return nil, false, readErr
			}
			return existing, true, nil
		}
		if isForeignKeyViolation(err) {
			return nil, false, application.ErrDeletionRequestNotFound
		}
		return nil, false, fmt.Errorf("create deletion request: %w", err)
	}
	return &application.DeletionRequest{
		ID:          uuidToString(row.ID),
		AccountID:   domain.AccountID(uuidToString(row.AccountID)),
		Status:      domain.DeletionRequestStatus(row.Status),
		RequestedAt: exportTime(row.RequestedAt),
		ExecutedAt:  exportTimePtr(row.ExecutedAt),
		CanceledAt:  exportTimePtr(row.CanceledAt),
	}, false, nil
}

// GetDeletionRequest returns the active request when one exists, otherwise
// the most recent terminal record.
func (r *Repository) GetDeletionRequest(ctx context.Context, accountID domain.AccountID) (*application.DeletionRequest, error) {
	accountUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, application.ErrDeletionRequestNotFound
	}

	row, err := r.queriesFor(ctx).GetDeletionRequestForAccount(ctx, accountUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrDeletionRequestNotFound
		}
		return nil, fmt.Errorf("get deletion request: %w", err)
	}
	return &application.DeletionRequest{
		ID:          uuidToString(row.ID),
		AccountID:   domain.AccountID(uuidToString(row.AccountID)),
		Status:      domain.DeletionRequestStatus(row.Status),
		RequestedAt: exportTime(row.RequestedAt),
		ExecutedAt:  exportTimePtr(row.ExecutedAt),
		CanceledAt:  exportTimePtr(row.CanceledAt),
	}, nil
}

// CancelDeletionRequest cancels the active record; missing rows and
// terminal records answer the same not-cancellable error.
func (r *Repository) CancelDeletionRequest(ctx context.Context, accountID domain.AccountID, reason string, canceledAt time.Time) (*application.DeletionRequest, error) {
	accountUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, application.ErrDeletionNotCancellable
	}

	row, err := r.queriesFor(ctx).CancelDeletionRequest(ctx, platformpg.CancelDeletionRequestParams{
		AccountID:  accountUUID,
		CanceledAt: timestamptz(canceledAt.UTC()),
		Reason:     reason,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrDeletionNotCancellable
		}
		return nil, fmt.Errorf("cancel deletion request: %w", err)
	}
	return &application.DeletionRequest{
		ID:          uuidToString(row.ID),
		AccountID:   domain.AccountID(uuidToString(row.AccountID)),
		Status:      domain.DeletionRequestStatus(row.Status),
		RequestedAt: exportTime(row.RequestedAt),
		ExecutedAt:  exportTimePtr(row.ExecutedAt),
		CanceledAt:  exportTimePtr(row.CanceledAt),
	}, nil
}

// ListDueDeletionRequests returns active requests whose cooldown elapsed.
func (r *Repository) ListDueDeletionRequests(ctx context.Context, now time.Time) ([]application.DeletionRequest, error) {
	rows, err := r.queriesFor(ctx).ListDueDeletionRequests(ctx, timestamptz(now.UTC().Add(-domain.DeletionCooldown)))
	if err != nil {
		return nil, fmt.Errorf("list due deletion requests: %w", err)
	}

	requests := make([]application.DeletionRequest, 0, len(rows))
	for _, row := range rows {
		requests = append(requests, application.DeletionRequest{
			ID:          uuidToString(row.ID),
			AccountID:   domain.AccountID(uuidToString(row.AccountID)),
			Status:      domain.DeletionStatusRequested,
			RequestedAt: exportTime(row.RequestedAt),
		})
	}
	return requests, nil
}

// ExecuteDeletionRequest anonymizes one account atomically: private rows are
// purged, the account becomes an opaque placeholder and the request is
// marked executed in one transaction. Billing, ledger, pass, moderation and
// audit rows are never touched: retention obligations survive the deletion.
func (r *Repository) ExecuteDeletionRequest(ctx context.Context, accountID domain.AccountID, executedAt time.Time) error {
	accountUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return application.ErrDeletionNotExecutable
	}

	if _, shared := platformpg.TxFromContext(ctx); shared {
		return r.executeDeletion(ctx, accountUUID, executedAt)
	}
	txManager := platformpg.NewTxManager(r.pool)
	return txManager.WithinTransaction(ctx, func(txCtx context.Context) error {
		return r.executeDeletion(txCtx, accountUUID, executedAt)
	})
}

// executeDeletion performs the anonymization statements inside the caller
// transaction.
func (r *Repository) executeDeletion(ctx context.Context, accountUUID pgtype.UUID, executedAt time.Time) error {
	queries := r.queriesFor(ctx)

	// The active request must still be requested and due: a concurrent
	// cancellation, an earlier execution or a not-yet-elapsed cooldown wins
	// and this run reports not executable.
	rows, err := queries.MarkDeletionExecuted(ctx, platformpg.MarkDeletionExecutedParams{
		AccountID:  accountUUID,
		ExecutedAt: timestamptz(executedAt.UTC()),
		DueBefore:  timestamptz(executedAt.UTC().Add(-domain.DeletionCooldown)),
	})
	if err != nil {
		return fmt.Errorf("mark deletion executed: %w", err)
	}
	if rows != 1 {
		return application.ErrDeletionNotExecutable
	}

	if _, err := queries.AnonymizeDeletedAccount(ctx, platformpg.AnonymizeDeletedAccountParams{
		AccountID:        accountUUID,
		PlaceholderEmail: deletedAccountEmail(accountUUID),
		ExecutedAt:       timestamptz(executedAt.UTC()),
	}); err != nil {
		return fmt.Errorf("anonymize deleted account: %w", err)
	}
	if _, err := queries.DeleteDeletedAccountProfile(ctx, accountUUID); err != nil {
		return fmt.Errorf("delete deleted account profile: %w", err)
	}
	if _, err := queries.DeleteDeletedAccountUsernameHistory(ctx, accountUUID); err != nil {
		return fmt.Errorf("delete deleted account username history: %w", err)
	}
	if _, err := queries.DeleteDeletedAccountCredentials(ctx, accountUUID); err != nil {
		return fmt.Errorf("delete deleted account credentials: %w", err)
	}
	if _, err := queries.DeleteDeletedAccountCommunicationPreferences(ctx, accountUUID); err != nil {
		return fmt.Errorf("delete deleted account communication preferences: %w", err)
	}
	if _, err := queries.DeleteDeletedAccountCommunicationPreferenceHistory(ctx, accountUUID); err != nil {
		return fmt.Errorf("delete deleted account communication preference history: %w", err)
	}
	if _, err := queries.DeleteDeletedAccountDraftRelations(ctx, accountUUID); err != nil {
		return fmt.Errorf("delete deleted account draft relations: %w", err)
	}
	if _, err := queries.DeleteDeletedAccountDrafts(ctx, accountUUID); err != nil {
		return fmt.Errorf("delete deleted account drafts: %w", err)
	}
	if _, err := queries.DeleteDeletedAccountVerificationTokens(ctx, accountUUID); err != nil {
		return fmt.Errorf("delete deleted account verification tokens: %w", err)
	}
	if _, err := queries.DeleteDeletedAccountResetTokens(ctx, accountUUID); err != nil {
		return fmt.Errorf("delete deleted account reset tokens: %w", err)
	}
	if _, err := queries.DeleteDeletedAccountSessions(ctx, accountUUID); err != nil {
		return fmt.Errorf("delete deleted account sessions: %w", err)
	}
	if _, err := queries.RevokeDeletedAccountAdminRoles(ctx, platformpg.RevokeDeletedAccountAdminRolesParams{
		AccountID: accountUUID,
		RevokedAt: timestamptz(executedAt.UTC()),
	}); err != nil {
		return fmt.Errorf("revoke deleted account admin roles: %w", err)
	}
	if _, err := queries.PurgeDeletedAccountExports(ctx, platformpg.PurgeDeletedAccountExportsParams{
		AccountID: accountUUID,
		PurgedAt:  timestamptz(executedAt.UTC()),
	}); err != nil {
		return fmt.Errorf("purge deleted account exports: %w", err)
	}
	return nil
}

// deletedAccountEmail renders the opaque placeholder that replaces the
// personal address; it embeds only the stable account identifier, so the
// value is deterministic, unique and never personal data.
func deletedAccountEmail(accountUUID pgtype.UUID) string {
	return "deleted+" + uuidToString(accountUUID) + "@deleted.invalid"
}
