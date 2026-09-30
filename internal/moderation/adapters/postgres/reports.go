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
	_ application.TargetDirectory  = (*Repository)(nil)
	_ application.ReportRepository = (*Repository)(nil)
)

// DescribeArena resolves an Arena target: unknown identifiers deny
// distinctly from removed ones, and drafts stay reportable so creators can
// self-report.
func (r *Repository) DescribeArena(ctx context.Context, targetID string) (*application.TargetInfo, error) {
	id, err := pgUUIDFromString(targetID)
	if err != nil {
		return &application.TargetInfo{}, nil
	}
	row, err := r.queries.GetArenaModerationTarget(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &application.TargetInfo{}, nil
		}
		return nil, fmt.Errorf("describe arena target: %w", err)
	}
	owner, err := domainAccountID(row.CreatorID)
	if err != nil {
		return nil, fmt.Errorf("stored arena owner is invalid: %w", err)
	}
	return &application.TargetInfo{
		Exists:  true,
		Removed: row.Status == "removed",
		Owner:   owner,
	}, nil
}

// DescribeArgument resolves an argument or reply target. Withdrawn
// arguments deny like removed ones: retired content has nothing left to
// moderate.
func (r *Repository) DescribeArgument(ctx context.Context, targetID string) (*application.TargetInfo, error) {
	id, err := pgUUIDFromString(targetID)
	if err != nil {
		return &application.TargetInfo{}, nil
	}
	row, err := r.queries.GetArgumentModerationTarget(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &application.TargetInfo{}, nil
		}
		return nil, fmt.Errorf("describe argument target: %w", err)
	}
	owner, err := domainAccountID(row.AuthorID)
	if err != nil {
		return nil, fmt.Errorf("stored argument author is invalid: %w", err)
	}
	return &application.TargetInfo{
		Exists:  true,
		Removed: row.Status == "removed" || row.Status == "withdrawn",
		Owner:   owner,
	}, nil
}

// DescribeProfile resolves a profile (account) target. Deleted accounts
// deny; every other lifecycle stays reportable.
func (r *Repository) DescribeProfile(ctx context.Context, targetID string) (*application.TargetInfo, error) {
	id, err := pgUUIDFromString(targetID)
	if err != nil {
		return &application.TargetInfo{}, nil
	}
	row, err := r.queries.GetAccountModerationTarget(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &application.TargetInfo{}, nil
		}
		return nil, fmt.Errorf("describe profile target: %w", err)
	}
	owner, err := domainAccountID(row.ID)
	if err != nil {
		return nil, fmt.Errorf("stored account is invalid: %w", err)
	}
	return &application.TargetInfo{
		Exists:  true,
		Removed: row.Status == "deleted",
		Owner:   owner,
	}, nil
}

// FindDuplicate resolves the original report when the same reporter already
// contested the same target for the same reason since the instant.
func (r *Repository) FindDuplicate(ctx context.Context, reporter domain.AccountID, target domain.TargetType, targetID string, reason domain.Reason, since time.Time) (*application.ReportRecord, error) {
	reporterUUID, err := pgUUIDFromAccountID(reporter)
	if err != nil {
		return nil, fmt.Errorf("find duplicate report: %w", err)
	}
	arena, argument, account, err := targetColumns(target, targetID)
	if err != nil {
		return nil, fmt.Errorf("find duplicate report: %w", err)
	}
	row, err := r.queries.GetDuplicateModerationReport(ctx, platformpg.GetDuplicateModerationReportParams{
		ReporterID:       reporterUUID,
		TargetType:       target.String(),
		TargetArenaID:    arena,
		TargetArgumentID: argument,
		TargetAccountID:  account,
		Reason:           reason.String(),
		CreatedAt:        pgtype.Timestamptz{Time: since.UTC(), Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find duplicate report: %w", err)
	}
	return &application.ReportRecord{ID: uuidToString(row.ID), Reporter: reporter}, nil
}

// CountRecentByReporter counts reports filed by the reporter since the
// instant. The count feeds the rate signal only.
func (r *Repository) CountRecentByReporter(ctx context.Context, reporter domain.AccountID, since time.Time) (int, error) {
	reporterUUID, err := pgUUIDFromAccountID(reporter)
	if err != nil {
		return 0, fmt.Errorf("count recent reports: %w", err)
	}
	count, err := r.queries.CountRecentModerationReportsByReporter(ctx, platformpg.CountRecentModerationReportsByReporterParams{
		ReporterID: reporterUUID,
		CreatedAt:  pgtype.Timestamptz{Time: since.UTC(), Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("count recent reports: %w", err)
	}
	return int(count), nil
}

// Insert stores one fresh report.
func (r *Repository) Insert(ctx context.Context, request application.InsertReportRequest) (*application.ReportRecord, error) {
	reporterUUID, err := pgUUIDFromAccountID(request.Reporter)
	if err != nil {
		return nil, fmt.Errorf("insert report: %w", err)
	}
	arena, argument, account, err := targetColumns(request.Target, request.TargetID)
	if err != nil {
		return nil, fmt.Errorf("insert report: %w", err)
	}
	var reportContext pgtype.Text
	if request.Context != "" {
		reportContext = pgtype.Text{String: request.Context, Valid: true}
	}
	row, err := r.queries.CreateModerationReport(ctx, platformpg.CreateModerationReportParams{
		ReporterID:       reporterUUID,
		TargetType:       request.Target.String(),
		TargetArenaID:    arena,
		TargetArgumentID: argument,
		TargetAccountID:  account,
		Reason:           request.Reason.String(),
		Context:          reportContext,
	})
	if err != nil {
		return nil, fmt.Errorf("insert report: %w", err)
	}
	return &application.ReportRecord{ID: uuidToString(row.ID), Reporter: request.Reporter}, nil
}

func targetColumns(target domain.TargetType, targetID string) (pgtype.UUID, pgtype.UUID, pgtype.UUID, error) {
	id, err := pgUUIDFromString(targetID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, err
	}
	switch target {
	case domain.TargetArena:
		return id, pgtype.UUID{}, pgtype.UUID{}, nil
	case domain.TargetArgument:
		return pgtype.UUID{}, id, pgtype.UUID{}, nil
	case domain.TargetProfile:
		return pgtype.UUID{}, pgtype.UUID{}, id, nil
	default:
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, domain.ErrInvalidTargetType
	}
}

func pgUUIDFromString(raw string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid identifier format")
	}
	return id, nil
}

func domainAccountID(id pgtype.UUID) (domain.AccountID, error) {
	raw := uuidToString(id)
	account := domain.AccountID(raw)
	if account.IsZero() {
		return "", domain.ErrEmptyAccountID
	}
	return account, nil
}

func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
