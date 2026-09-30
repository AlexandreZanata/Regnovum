// FileReport implements the structured report use case (P13-T03):
// an authenticated account contests an Arena, an argument or a profile with
// a closed reason vocabulary and an optional bounded context. Reports are
// restricted evidence: they never remove content by themselves, no matter
// the volume, and reporter context never enters errors or logs.
package application

import (
	"context"
	"fmt"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// TargetInfo describes what the contested identifier resolved to. Owner is
// empty when the target is not account-owned in a way the caller needs.
type TargetInfo struct {
	Exists  bool
	Removed bool
	Owner   domain.AccountID
}

// TargetDirectory resolves contested targets by identifier. It reads only:
// filing a report never mutates the target, whatever the volume.
type TargetDirectory interface {
	// DescribeArena resolves an Arena target.
	DescribeArena(ctx context.Context, targetID string) (*TargetInfo, error)
	// DescribeArgument resolves an argument or reply target.
	DescribeArgument(ctx context.Context, targetID string) (*TargetInfo, error)
	// DescribeProfile resolves a profile (account) target.
	DescribeProfile(ctx context.Context, targetID string) (*TargetInfo, error)
}

// ReportRecord is one stored report as read back from persistence.
type ReportRecord struct {
	ID       string
	Reporter domain.AccountID
}

// ReportRepository persists reports with deduplication anchored on
// reporter, target and reason inside the duplicate window.
type ReportRepository interface {
	// FindDuplicate resolves the original report when the same reporter
	// already contested the same target for the same reason since the
	// instant, or nil when this is a fresh contest.
	FindDuplicate(ctx context.Context, reporter domain.AccountID, target domain.TargetType, targetID string, reason domain.Reason, since time.Time) (*ReportRecord, error)
	// CountRecentByReporter counts reports filed by the reporter since the
	// instant. The count feeds the rate signal only.
	CountRecentByReporter(ctx context.Context, reporter domain.AccountID, since time.Time) (int, error)
	// Insert stores one fresh report.
	Insert(ctx context.Context, request InsertReportRequest) (*ReportRecord, error)
}

// InsertReportRequest is one fresh report to persist.
type InsertReportRequest struct {
	Reporter domain.AccountID
	Target   domain.TargetType
	TargetID string
	Reason   domain.Reason
	Context  string
}

// FileReportCommand is everything a caller may name: who reports, what is
// contested, why, and optional context. Amounts, priorities and outcomes
// are deliberately absent: volume never removes content automatically.
type FileReportCommand struct {
	Reporter domain.AccountID
	Target   string
	TargetID string
	Reason   string
	Context  string
}

// FileReportResult is the explicit outcome.
type FileReportResult struct {
	ReportID        string
	Replayed        bool
	RateLimited     bool
	ReportsInWindow int
}

// FileReportDependencies groups everything the use case needs.
type FileReportDependencies struct {
	Targets TargetDirectory
	Reports ReportRepository
	Clock   Clock
}

// FileReportUseCase files one structured report.
type FileReportUseCase struct {
	targets TargetDirectory
	reports ReportRepository
	clock   Clock
}

// NewFileReportUseCase builds the use case, refusing incomplete composition.
func NewFileReportUseCase(deps FileReportDependencies) (*FileReportUseCase, error) {
	if deps.Targets == nil || deps.Reports == nil || deps.Clock == nil {
		return nil, ErrInvalidReportConfig
	}
	return &FileReportUseCase{targets: deps.Targets, reports: deps.Reports, clock: deps.Clock}, nil
}

// Execute validates the report, resolves the target, deduplicates and
// persists. Self-reports are allowed: contesting owned content is a valid
// way to ask for review. Removed targets deny; unknown targets deny
// distinctly. Reporter context never enters an error.
func (uc *FileReportUseCase) Execute(ctx context.Context, cmd FileReportCommand) (*FileReportResult, error) {
	if cmd.Reporter.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	target, err := domain.ParseTargetType(cmd.Target)
	if err != nil {
		return nil, err
	}
	if cmd.TargetID == "" {
		return nil, domain.ErrEmptyTargetID
	}
	reason, err := domain.ParseReason(cmd.Reason)
	if err != nil {
		return nil, err
	}
	reportContext, err := domain.ParseReportContext(cmd.Context)
	if err != nil {
		return nil, err
	}

	info, err := uc.describe(ctx, target, cmd.TargetID)
	if err != nil {
		return nil, err
	}
	if !info.Exists {
		return nil, ErrTargetNotFound
	}
	if info.Removed {
		return nil, ErrTargetRemoved
	}

	now := uc.clock.Now()
	duplicate, err := uc.reports.FindDuplicate(ctx, cmd.Reporter, target, cmd.TargetID, reason, now.Add(-domain.DuplicateWindow))
	if err != nil {
		return nil, fmt.Errorf("file report: %w", err)
	}
	recent, err := uc.reports.CountRecentByReporter(ctx, cmd.Reporter, now.Add(-domain.RateWindow))
	if err != nil {
		return nil, fmt.Errorf("file report: %w", err)
	}
	rateLimited := recent >= domain.RateThreshold

	if duplicate != nil {
		return &FileReportResult{
			ReportID:        duplicate.ID,
			Replayed:        true,
			RateLimited:     rateLimited,
			ReportsInWindow: recent,
		}, nil
	}

	stored, err := uc.reports.Insert(ctx, InsertReportRequest{
		Reporter: cmd.Reporter,
		Target:   target,
		TargetID: cmd.TargetID,
		Reason:   reason,
		Context:  reportContext,
	})
	if err != nil {
		return nil, fmt.Errorf("file report: %w", err)
	}

	return &FileReportResult{
		ReportID:        stored.ID,
		RateLimited:     rateLimited,
		ReportsInWindow: recent + 1,
	}, nil
}

func (uc *FileReportUseCase) describe(ctx context.Context, target domain.TargetType, targetID string) (*TargetInfo, error) {
	switch target {
	case domain.TargetArena:
		return uc.targets.DescribeArena(ctx, targetID)
	case domain.TargetArgument:
		return uc.targets.DescribeArgument(ctx, targetID)
	case domain.TargetProfile:
		return uc.targets.DescribeProfile(ctx, targetID)
	default:
		return nil, domain.ErrInvalidTargetType
	}
}
