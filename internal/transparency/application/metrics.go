// Package application derives privacy-safe platform metrics (P14-T02):
// one snapshot per period, rebuilt from source rows on every call, with
// low counts suppressed. Snapshots carry instants and integer counts
// only: no email, no Stripe identifier, no IP and no account-level
// position ever enters this projection.
package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/transparency/domain"
)

// RawCounts is one atomic read of every source family inside the period.
// The adapter fills it from ledger, content, entitlement and moderation
// tables; the use case suppresses it into a Snapshot.
type RawCounts struct {
	EligibleAccounts        int64
	ArenasPublished         int64
	ArenasClosed            int64
	ArenasRestricted        int64
	ArenasRemoved           int64
	ArgumentsPublished      int64
	ArgumentsWithdrawn      int64
	PositionChanges         int64
	AttributionsValid       int64
	AttributionsInvalidated int64
	InfluencedAuthors       int64
	InkFreeGranted          int64
	InkFreeExpired          int64
	InkFreeConsumed         int64
	InkPurchasedGranted     int64
	InkPurchasedConsumed    int64
	InkRefunded             int64
	InkAdminAdjusted        int64
	PassesPurchaseGranted   int64
	PassesMemberGranted     int64
	PassesConsumed          int64
	ReportsFiled            int64
	ActionsRecorded         int64
	AppealsFiled            int64
	AppealsReversed         int64
}

// Snapshot is the published metrics document of one period: methodology
// version, UTC bounds and suppressed integer counts. Reflection over this
// struct must find instants and integers only.
type Snapshot struct {
	MethodologyVersion      int
	PeriodStart             time.Time
	PeriodEnd               time.Time
	EligibleAccounts        int64
	ArenasPublished         int64
	ArenasClosed            int64
	ArenasRestricted        int64
	ArenasRemoved           int64
	ArgumentsPublished      int64
	ArgumentsWithdrawn      int64
	PositionChanges         int64
	AttributionsValid       int64
	AttributionsInvalidated int64
	InfluencedAuthors       int64
	InkFreeGranted          int64
	InkFreeExpired          int64
	InkFreeConsumed         int64
	InkPurchasedGranted     int64
	InkPurchasedConsumed    int64
	InkRefunded             int64
	InkAdminAdjusted        int64
	PassesPurchaseGranted   int64
	PassesMemberGranted     int64
	PassesConsumed          int64
	ReportsFiled            int64
	ActionsRecorded         int64
	AppealsFiled            int64
	AppealsReversed         int64
}

// MetricsSource reads one atomic snapshot of raw source counts. The read
// must not mutate anything: derivation rebuilds from sources every time.
type MetricsSource interface {
	// RawCounts returns every source count for the half-open window.
	RawCounts(ctx context.Context, start, end time.Time) (*RawCounts, error)
}

// DeriveMetricsUseCase rebuilds one privacy-safe snapshot per period.
type DeriveMetricsUseCase struct {
	source MetricsSource
}

// NewDeriveMetricsUseCase builds the use case, refusing incomplete
// composition.
func NewDeriveMetricsUseCase(source MetricsSource) (*DeriveMetricsUseCase, error) {
	if source == nil {
		return nil, ErrInvalidMetricsConfig
	}
	return &DeriveMetricsUseCase{source: source}, nil
}

// Execute derives the snapshot: raw reconstruction from sources, then the
// uniform low-count suppression. Lifecycle snapshots (current statuses)
// travel like every other count.
func (uc *DeriveMetricsUseCase) Execute(ctx context.Context, period domain.Period) (*Snapshot, error) {
	if period.IsZero() {
		return nil, domain.ErrInvalidPeriod
	}

	raw, err := uc.source.RawCounts(ctx, period.Start(), period.End())
	if err != nil {
		return nil, err
	}
	if raw == nil {
		raw = &RawCounts{}
	}

	suppress := domain.Suppress
	return &Snapshot{
		MethodologyVersion:      domain.MethodologyVersion,
		PeriodStart:             period.Start(),
		PeriodEnd:               period.End(),
		EligibleAccounts:        suppress(raw.EligibleAccounts),
		ArenasPublished:         suppress(raw.ArenasPublished),
		ArenasClosed:            suppress(raw.ArenasClosed),
		ArenasRestricted:        suppress(raw.ArenasRestricted),
		ArenasRemoved:           suppress(raw.ArenasRemoved),
		ArgumentsPublished:      suppress(raw.ArgumentsPublished),
		ArgumentsWithdrawn:      suppress(raw.ArgumentsWithdrawn),
		PositionChanges:         suppress(raw.PositionChanges),
		AttributionsValid:       suppress(raw.AttributionsValid),
		AttributionsInvalidated: suppress(raw.AttributionsInvalidated),
		InfluencedAuthors:       suppress(raw.InfluencedAuthors),
		InkFreeGranted:          suppress(raw.InkFreeGranted),
		InkFreeExpired:          suppress(raw.InkFreeExpired),
		InkFreeConsumed:         suppress(raw.InkFreeConsumed),
		InkPurchasedGranted:     suppress(raw.InkPurchasedGranted),
		InkPurchasedConsumed:    suppress(raw.InkPurchasedConsumed),
		InkRefunded:             suppress(raw.InkRefunded),
		InkAdminAdjusted:        suppress(raw.InkAdminAdjusted),
		PassesPurchaseGranted:   suppress(raw.PassesPurchaseGranted),
		PassesMemberGranted:     suppress(raw.PassesMemberGranted),
		PassesConsumed:          suppress(raw.PassesConsumed),
		ReportsFiled:            suppress(raw.ReportsFiled),
		ActionsRecorded:         suppress(raw.ActionsRecorded),
		AppealsFiled:            suppress(raw.AppealsFiled),
		AppealsReversed:         suppress(raw.AppealsReversed),
	}, nil
}
