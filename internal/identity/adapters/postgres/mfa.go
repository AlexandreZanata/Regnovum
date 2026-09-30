package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

var _ application.MFARepository = (*Repository)(nil)

// pgUUIDForString converts a domain identifier to the driver's UUID. A
// malformed identifier is reported as "not found" rather than as an internal
// failure: an identifier that cannot be a row identifies no row.
func pgUUIDForString(value string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		return pgtype.UUID{}, false
	}
	return id, true
}

// GetMFAEnrollment returns the stored second factor of an account.
//
// An account without a row has no enrollment, which is not an error of the
// storage: it is the state the use case names.
func (r *Repository) GetMFAEnrollment(ctx context.Context, accountID domain.AccountID) (*application.MFAEnrollmentRecord, error) {
	id, ok := pgUUIDForString(accountID.String())
	if !ok {
		return nil, application.ErrMFAEnrollmentMissing
	}

	row, err := r.queries.GetMFAEnrollment(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrMFAEnrollmentMissing
		}
		return nil, fmt.Errorf("get mfa enrollment: %w", err)
	}

	record := &application.MFAEnrollmentRecord{
		SecretSealed:     row.SecretSealed,
		LastAcceptedStep: row.LastAcceptedStep,
	}
	if row.ConfirmedAt.Valid {
		confirmedAt := row.ConfirmedAt.Time.UTC()
		record.ConfirmedAt = &confirmedAt
	}
	return record, nil
}

// UpsertPendingMFAEnrollment stores a new pending secret.
//
// The statement refuses to touch a confirmed enrollment, and it does so inside
// the database: the row is the invariant, so a concurrent confirmation cannot
// be overwritten by a request that read the old state.
func (r *Repository) UpsertPendingMFAEnrollment(ctx context.Context, accountID domain.AccountID, secretSealed []byte) (*application.MFAEnrollmentRecord, error) {
	id, ok := pgUUIDForString(accountID.String())
	if !ok {
		return nil, application.ErrMFAEnrollmentMissing
	}

	row, err := r.queries.UpsertPendingMFAEnrollment(ctx, platformpg.UpsertPendingMFAEnrollmentParams{
		AccountID:    id,
		SecretSealed: secretSealed,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The conflict clause did not fire because the existing row is
			// confirmed, which is the refusal this method exists for.
			return nil, application.ErrMFAAlreadyEnrolled
		}
		return nil, fmt.Errorf("upsert pending mfa enrollment: %w", err)
	}

	record := &application.MFAEnrollmentRecord{
		SecretSealed:     row.SecretSealed,
		LastAcceptedStep: row.LastAcceptedStep,
	}
	if row.ConfirmedAt.Valid {
		confirmedAt := row.ConfirmedAt.Time.UTC()
		record.ConfirmedAt = &confirmedAt
	}
	return record, nil
}

// ConfirmMFAEnrollment records the confirmation and the step it spent.
func (r *Repository) ConfirmMFAEnrollment(ctx context.Context, accountID domain.AccountID, confirmedAt time.Time, acceptedStep int64) (bool, error) {
	id, ok := pgUUIDForString(accountID.String())
	if !ok {
		return false, application.ErrMFAEnrollmentMissing
	}

	if _, err := r.queries.ConfirmMFAEnrollment(ctx, platformpg.ConfirmMFAEnrollmentParams{
		AccountID:        id,
		ConfirmedAt:      pgtype.Timestamptz{Time: confirmedAt.UTC(), Valid: true},
		LastAcceptedStep: acceptedStep,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Somebody else confirmed (or there is nothing pending): the
			// confirmation did not happen here.
			return false, nil
		}
		return false, fmt.Errorf("confirm mfa enrollment: %w", err)
	}
	return true, nil
}

// AdvanceMFAVerifiedStep moves the spent step forward.
func (r *Repository) AdvanceMFAVerifiedStep(ctx context.Context, accountID domain.AccountID, step int64) (bool, error) {
	id, ok := pgUUIDForString(accountID.String())
	if !ok {
		return false, application.ErrMFAEnrollmentMissing
	}

	affected, err := r.queries.AdvanceMFAVerifiedStep(ctx, platformpg.AdvanceMFAVerifiedStepParams{
		AccountID:        id,
		LastAcceptedStep: step,
	})
	if err != nil {
		return false, fmt.Errorf("advance mfa step: %w", err)
	}
	return affected > 0, nil
}

// ReplaceMFABackupCodes replaces the whole set, in one transaction: a partial
// set would be an account with fewer recovery codes than it was told it has.
func (r *Repository) ReplaceMFABackupCodes(ctx context.Context, accountID domain.AccountID, codeHashes []string) error {
	id, ok := pgUUIDForString(accountID.String())
	if !ok {
		return application.ErrMFAEnrollmentMissing
	}

	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin backup codes transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	queries := platformpg.New(transaction)
	if _, err := queries.DeleteMFABackupCodes(ctx, id); err != nil {
		return fmt.Errorf("clear backup codes: %w", err)
	}
	for _, hash := range codeHashes {
		if err := queries.InsertMFABackupCode(ctx, platformpg.InsertMFABackupCodeParams{
			AccountID: id,
			CodeHash:  hash,
		}); err != nil {
			return fmt.Errorf("insert backup code: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit backup codes: %w", err)
	}
	return nil
}

// ListUnusedMFABackupCodes returns the codes that can still be spent.
func (r *Repository) ListUnusedMFABackupCodes(ctx context.Context, accountID domain.AccountID) ([]application.MFABackupCodeRecord, error) {
	id, ok := pgUUIDForString(accountID.String())
	if !ok {
		return nil, application.ErrMFAEnrollmentMissing
	}

	rows, err := r.queries.ListUnusedMFABackupCodes(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list backup codes: %w", err)
	}

	records := make([]application.MFABackupCodeRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, application.MFABackupCodeRecord{
			ID:       uuidToString(row.ID),
			CodeHash: row.CodeHash,
		})
	}
	return records, nil
}

// ConsumeMFABackupCode spends one code exactly once.
func (r *Repository) ConsumeMFABackupCode(ctx context.Context, codeID string, usedAt time.Time) (bool, error) {
	id, ok := pgUUIDForString(codeID)
	if !ok {
		return false, nil
	}

	affected, err := r.queries.ConsumeMFABackupCode(ctx, platformpg.ConsumeMFABackupCodeParams{
		ID:     id,
		UsedAt: pgtype.Timestamptz{Time: usedAt.UTC(), Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("consume backup code: %w", err)
	}
	return affected > 0, nil
}

// MarkSessionMFAVerified elevates one session.
func (r *Repository) MarkSessionMFAVerified(ctx context.Context, sessionID domain.SessionID, verifiedAt time.Time) (bool, error) {
	id, ok := pgUUIDForString(sessionID.String())
	if !ok {
		return false, nil
	}

	affected, err := r.queries.MarkSessionMFAVerified(ctx, platformpg.MarkSessionMFAVerifiedParams{
		ID:            id,
		MfaVerifiedAt: pgtype.Timestamptz{Time: verifiedAt.UTC(), Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("mark session mfa verified: %w", err)
	}
	return affected > 0, nil
}
