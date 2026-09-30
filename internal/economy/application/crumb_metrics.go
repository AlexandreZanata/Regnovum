package application

import (
	"context"
	"fmt"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// CrumbMetricsReading is one exact derivation of the public
// crumb-metrics aggregates: supply, Treasury and circulation with
// the R4 reference, the crumbs granted with their headcount and the
// real reflux index ratio, all in integer millis over an explicit
// sealed UTC window. The caller re-derives every number: this shape
// carries values only, never queries. Q25 and Q31 stay PENDENTE
// (docs/reino/DECISOES_VIGENTES.md): the window, budget and ratio
// arrive per call, never as ratified values here.
type CrumbMetricsReading struct {
	SupplyMillis            int64
	TreasuryMillis          int64
	CirculationMillis       int64
	R4Millis                int64
	CrumbsDistributedMillis int64
	CrumbsHeadcount         int64
	IRRPerMille             int64
	IRRHasRatio             bool
	WindowStart             time.Time
	WindowEnd               time.Time
	MethodologyVersion      int
}

// CrumbMetricsSnapshot is the published crumb-metrics document: the
// locale and its titles, the derivation instant with the cache
// window, the sealed UTC window with its definitions, the sovereign
// and metric aggregates in full and the crumbs amount suppressed
// below the low-count threshold. Account labels, transfer
// identifiers, governors and holder labels never enter this shape:
// reflection must find instants, integers, flags and dictionary
// titles only.
type CrumbMetricsSnapshot struct {
	Locale                  string
	Labels                  domain.CrumbMetricsLabels
	GeneratedAt             time.Time
	CacheSeconds            int64
	WindowStart             time.Time
	WindowEnd               time.Time
	SupplyMillis            int64
	TreasuryMillis          int64
	CirculationMillis       int64
	R4Millis                int64
	CrumbsDistributedMillis int64
	CrumbsHeadcount         int64
	CrumbsSuppressed        bool
	IRRLabel                string
	MethodologyVersion      int
}

// CrumbMetricsProvider resolves one exact reading of every
// crumb-metrics aggregate. Reads never mint, freeze or move anything.
type CrumbMetricsProvider interface {
	// ReadCrumbMetrics resolves one exact reading of the public
	// aggregates over an explicit sealed window.
	ReadCrumbMetrics(ctx context.Context) (*CrumbMetricsReading, error)
}

// CrumbMetricsUseCase publishes one privacy-safe crumb-metrics
// document per locale: exact sovereign and metric aggregates with
// the crumbs amount suppressed below the low-count threshold,
// served from a bounded cache so readers share one derivation per
// window. It is an internal operation: no public surface calls it.
type CrumbMetricsUseCase struct {
	metrics  CrumbMetricsProvider
	clock    Clock
	cached   *CrumbMetricsSnapshot
	cachedAt time.Time
	cachedOf string
}

// NewCrumbMetricsUseCase creates an instance of CrumbMetricsUseCase,
// refusing incomplete composition.
func NewCrumbMetricsUseCase(metrics CrumbMetricsProvider, clock Clock) (*CrumbMetricsUseCase, error) {
	if metrics == nil || clock == nil {
		return nil, domain.ErrInvalidTotals
	}
	return &CrumbMetricsUseCase{metrics: metrics, clock: clock}, nil
}

// Metrics serves the document in one locale: the cached derivation
// while the window holds, a fresh re-derivation afterwards. Unknown
// locales refuse before any reading happens.
func (uc *CrumbMetricsUseCase) Metrics(ctx context.Context, locale string) (CrumbMetricsSnapshot, error) {
	parsed, err := domain.ParseTotalsLocale(locale)
	if err != nil {
		return CrumbMetricsSnapshot{}, err
	}
	now := uc.clock.Now().UTC()
	if uc.cached != nil && uc.cachedOf == parsed.String() &&
		now.Before(uc.cachedAt.Add(domain.TotalsCacheSeconds*time.Second)) {
		return *uc.cached, nil
	}
	reading, err := uc.metrics.ReadCrumbMetrics(ctx)
	if err != nil {
		return CrumbMetricsSnapshot{}, err
	}
	if reading == nil {
		return CrumbMetricsSnapshot{}, domain.ErrInvalidTotals
	}
	snapshot, err := assembleCrumbMetrics(parsed, reading, now)
	if err != nil {
		return CrumbMetricsSnapshot{}, err
	}
	uc.cached = &snapshot
	uc.cachedAt = now
	uc.cachedOf = parsed.String()
	return snapshot, nil
}

// irrLabel renders the index for disclosure without float: "N/A"
// with no outflows, otherwise one decimal percent kept in integers.
func irrLabel(permille int64, hasRatio bool) (string, error) {
	if !hasRatio {
		return "N/A", nil
	}
	if permille < 0 {
		return "", domain.ErrInvalidTotals
	}
	return fmt.Sprintf("%d.%d%%", permille/10, permille%10), nil
}

// assembleCrumbMetrics renders one document from one exact reading:
// the window travels in UTC so display timezones never move it, the
// sovereign and metric classes travel whole, and the crumbs amount
// passes the low-count rule. Negative ledger amounts refuse: the
// journal never holds them. A negative R4 travels whole: the deficit
// stays visible for the distribution block, never hidden here.
func assembleCrumbMetrics(locale domain.TotalsLocale, reading *CrumbMetricsReading, now time.Time) (CrumbMetricsSnapshot, error) {
	if reading.WindowStart.IsZero() || reading.WindowEnd.IsZero() ||
		!reading.WindowStart.Before(reading.WindowEnd) {
		return CrumbMetricsSnapshot{}, domain.ErrInvalidTotals
	}
	if reading.SupplyMillis < 0 || reading.TreasuryMillis < 0 ||
		reading.CirculationMillis < 0 || reading.CrumbsDistributedMillis < 0 ||
		reading.CrumbsHeadcount < 0 {
		return CrumbMetricsSnapshot{}, domain.ErrInvalidTotals
	}
	label, err := irrLabel(reading.IRRPerMille, reading.IRRHasRatio)
	if err != nil {
		return CrumbMetricsSnapshot{}, err
	}
	crumbs, suppressed := domain.SuppressTotal(reading.CrumbsDistributedMillis, reading.CrumbsHeadcount)
	return CrumbMetricsSnapshot{
		Locale:                  locale.String(),
		Labels:                  domain.CrumbMetricsLabelsFor(locale),
		GeneratedAt:             now,
		CacheSeconds:            domain.TotalsCacheSeconds,
		WindowStart:             reading.WindowStart.UTC(),
		WindowEnd:               reading.WindowEnd.UTC(),
		SupplyMillis:            reading.SupplyMillis,
		TreasuryMillis:          reading.TreasuryMillis,
		CirculationMillis:       reading.CirculationMillis,
		R4Millis:                reading.R4Millis,
		CrumbsDistributedMillis: crumbs,
		CrumbsHeadcount:         reading.CrumbsHeadcount,
		CrumbsSuppressed:        suppressed,
		IRRLabel:                label,
		MethodologyVersion:      reading.MethodologyVersion,
	}, nil
}
