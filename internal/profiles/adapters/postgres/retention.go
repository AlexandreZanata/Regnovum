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
	_ application.RetentionRepository = (*Repository)(nil)
)

// ActiveRetentionHolds returns the holds in force. A stored class outside the
// executable vocabulary is refused: enforcing a policy that does not know a
// hold would purge data the platform promised to keep.
func (r *Repository) ActiveRetentionHolds(ctx context.Context) ([]domain.RetentionHold, error) {
	rows, err := r.queriesFor(ctx).ListActiveRetentionHolds(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active retention holds: %w", err)
	}

	holds := make([]domain.RetentionHold, 0, len(rows))
	for _, row := range rows {
		class, known := domain.ParseRetentionClass(row.DataClass)
		if !known {
			return nil, fmt.Errorf("list active retention holds: %w", domain.ErrUnknownRetentionClass)
		}
		hold := domain.RetentionHold{
			ID:         uuidToString(row.ID),
			Class:      class,
			ReasonCode: row.ReasonCode,
			PlacedAt:   exportTime(row.PlacedAt),
		}
		if row.AccountID.Valid {
			hold.AccountID = uuidToString(row.AccountID)
		}
		holds = append(holds, hold)
	}
	return holds, nil
}

// PurgeTerminalTokens removes the single-use token hashes of both token
// tables, so the tokens class behaves as one class even though the storage
// is two tables.
func (r *Repository) PurgeTerminalTokens(ctx context.Context, cutoff time.Time, held application.RetentionHoldSelection) (application.RetentionCounts, error) {
	counts := application.RetentionCounts{}
	err := r.withinRetentionTransaction(ctx, func(txCtx context.Context) error {
		accounts, err := pgUUIDsFromStrings(held.Accounts)
		if err != nil {
			return err
		}
		queries := r.queriesFor(txCtx)

		verification, err := queries.PurgeTerminalVerificationTokens(txCtx, platformpg.PurgeTerminalVerificationTokensParams{
			Cutoff:       timestamptz(cutoff.UTC()),
			ClassHeld:    held.ClassHeld,
			HeldAccounts: accounts,
		})
		if err != nil {
			return fmt.Errorf("purge terminal verification tokens: %w", err)
		}
		recovery, err := queries.PurgeTerminalRecoveryTokens(txCtx, platformpg.PurgeTerminalRecoveryTokensParams{
			Cutoff:       timestamptz(cutoff.UTC()),
			ClassHeld:    held.ClassHeld,
			HeldAccounts: accounts,
		})
		if err != nil {
			return fmt.Errorf("purge terminal recovery tokens: %w", err)
		}

		counts.Purged = verification.Purged + recovery.Purged
		counts.Held = verification.Held + recovery.Held
		return nil
	})
	if err != nil {
		return application.RetentionCounts{}, err
	}
	return counts, nil
}

// PurgeTerminalSessions removes sessions revoked or expired at or before the
// cutoff, except the ones a hold preserves.
func (r *Repository) PurgeTerminalSessions(ctx context.Context, cutoff time.Time, held application.RetentionHoldSelection) (application.RetentionCounts, error) {
	counts := application.RetentionCounts{}
	err := r.withinRetentionTransaction(ctx, func(txCtx context.Context) error {
		accounts, err := pgUUIDsFromStrings(held.Accounts)
		if err != nil {
			return err
		}
		row, err := r.queriesFor(txCtx).PurgeTerminalSessions(txCtx, platformpg.PurgeTerminalSessionsParams{
			Cutoff:       timestamptz(cutoff.UTC()),
			ClassHeld:    held.ClassHeld,
			HeldAccounts: accounts,
		})
		if err != nil {
			return fmt.Errorf("purge terminal sessions: %w", err)
		}
		counts.Purged = row.Purged
		counts.Held = row.Held
		return nil
	})
	if err != nil {
		return application.RetentionCounts{}, err
	}
	return counts, nil
}

// AnonymizeTerminalSessionReferentials strips the client IP and user agent
// of terminal sessions past the prevention window. The session row itself is
// untouched: its own class decides when it is purged.
func (r *Repository) AnonymizeTerminalSessionReferentials(ctx context.Context, cutoff time.Time, held application.RetentionHoldSelection) (application.RetentionCounts, error) {
	counts := application.RetentionCounts{}
	err := r.withinRetentionTransaction(ctx, func(txCtx context.Context) error {
		accounts, err := pgUUIDsFromStrings(held.Accounts)
		if err != nil {
			return err
		}
		row, err := r.queriesFor(txCtx).AnonymizeTerminalSessionReferentials(txCtx, platformpg.AnonymizeTerminalSessionReferentialsParams{
			Cutoff:       timestamptz(cutoff.UTC()),
			ClassHeld:    held.ClassHeld,
			HeldAccounts: accounts,
		})
		if err != nil {
			return fmt.Errorf("anonymize terminal session referentials: %w", err)
		}
		counts.Anonymized = row.Anonymized
		counts.Held = row.Held
		return nil
	})
	if err != nil {
		return application.RetentionCounts{}, err
	}
	return counts, nil
}

// PurgeExpiredExports purges the document bytes of exports whose link
// expired at or before the cutoff and expires requests that were never
// generated by then. The record survives: it is evidence that the holder
// asked for an export and that the platform served it with expiry.
func (r *Repository) PurgeExpiredExports(ctx context.Context, cutoff, executedAt time.Time, held application.RetentionHoldSelection) (application.RetentionCounts, error) {
	counts := application.RetentionCounts{}
	err := r.withinRetentionTransaction(ctx, func(txCtx context.Context) error {
		accounts, err := pgUUIDsFromStrings(held.Accounts)
		if err != nil {
			return err
		}
		row, err := r.queriesFor(txCtx).PurgeExpiredExportDocuments(txCtx, platformpg.PurgeExpiredExportDocumentsParams{
			Cutoff:       timestamptz(cutoff.UTC()),
			ExecutedAt:   timestamptz(executedAt.UTC()),
			ClassHeld:    held.ClassHeld,
			HeldAccounts: accounts,
		})
		if err != nil {
			return fmt.Errorf("purge expired export documents: %w", err)
		}
		counts.Purged = row.Purged
		counts.Held = row.Held
		return nil
	})
	if err != nil {
		return application.RetentionCounts{}, err
	}
	return counts, nil
}

// CountRetainedAuditEvents counts the administrative trail kept as evidence.
func (r *Repository) CountRetainedAuditEvents(ctx context.Context) (int32, error) {
	retained, err := r.queriesFor(ctx).CountRetainedAuditEvents(ctx)
	if err != nil {
		return 0, fmt.Errorf("count retained audit events: %w", err)
	}
	return retained, nil
}

// CountRetainedBillingRows counts the billing records kept as evidence of
// money.
func (r *Repository) CountRetainedBillingRows(ctx context.Context) (int32, error) {
	retained, err := r.queriesFor(ctx).CountRetainedBillingRows(ctx)
	if err != nil {
		return 0, fmt.Errorf("count retained billing rows: %w", err)
	}
	return retained, nil
}

// RecordRetentionRun appends one ledger entry. When the class already
// recorded that instant the entry is replayed: the outcome the ledger holds
// is returned instead of a second, invented one.
func (r *Repository) RecordRetentionRun(ctx context.Context, record application.RetentionRunRecord) (*application.RetentionRun, error) {
	queries := r.queriesFor(ctx)

	row, err := queries.CreateRetentionRun(ctx, platformpg.CreateRetentionRunParams{
		DataClass:       string(record.Class),
		ExecutedAt:      timestamptz(record.ExecutedAt.UTC()),
		CutoffAt:        retentionCutoff(record.CutoffAt),
		PurgedCount:     record.Counts.Purged,
		AnonymizedCount: record.Counts.Anonymized,
		RetainedCount:   record.Counts.Retained,
		HeldCount:       record.Counts.Held,
	})
	if err == nil {
		return &application.RetentionRun{
			ID:         uuidToString(row.ID),
			Class:      record.Class,
			ExecutedAt: exportTime(row.ExecutedAt),
			CutoffAt:   exportTimePtr(row.CutoffAt),
			Counts: application.RetentionCounts{
				Purged:     row.PurgedCount,
				Anonymized: row.AnonymizedCount,
				Retained:   row.RetainedCount,
				Held:       row.HeldCount,
			},
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("record retention run: %w", err)
	}

	existing, readErr := queries.GetRetentionRunForClassAt(ctx, platformpg.GetRetentionRunForClassAtParams{
		DataClass:  string(record.Class),
		ExecutedAt: timestamptz(record.ExecutedAt.UTC()),
	})
	if readErr != nil {
		return nil, fmt.Errorf("resolve replayed retention run: %w", readErr)
	}
	return &application.RetentionRun{
		ID:         uuidToString(existing.ID),
		Class:      record.Class,
		ExecutedAt: exportTime(existing.ExecutedAt),
		CutoffAt:   exportTimePtr(existing.CutoffAt),
		Counts: application.RetentionCounts{
			Purged:     existing.PurgedCount,
			Anonymized: existing.AnonymizedCount,
			Retained:   existing.RetainedCount,
			Held:       existing.HeldCount,
		},
		Replayed: true,
	}, nil
}

// withinRetentionTransaction joins the caller transaction when there is one
// and opens a private one otherwise, so the data change and its ledger entry
// always commit or roll back together.
func (r *Repository) withinRetentionTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, shared := platformpg.TxFromContext(ctx); shared {
		return fn(ctx)
	}
	return platformpg.NewTxManager(r.pool).WithinTransaction(ctx, fn)
}

// retentionCutoff renders the optional boundary of a run.
func retentionCutoff(cutoff *time.Time) pgtype.Timestamptz {
	if cutoff == nil {
		return pgtype.Timestamptz{}
	}
	return timestamptz(cutoff.UTC())
}

// pgUUIDsFromStrings converts held account identifiers, refusing a value the
// database cannot address instead of dropping the hold silently.
func pgUUIDsFromStrings(values []string) ([]pgtype.UUID, error) {
	if len(values) == 0 {
		return nil, nil
	}
	identifiers := make([]pgtype.UUID, 0, len(values))
	for _, value := range values {
		var identifier pgtype.UUID
		if err := identifier.Scan(value); err != nil {
			return nil, fmt.Errorf("invalid retention hold account %q: %w", value, err)
		}
		identifiers = append(identifiers, identifier)
	}
	return identifiers, nil
}
