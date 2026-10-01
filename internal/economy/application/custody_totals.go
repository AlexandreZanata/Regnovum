package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// VaultTotal is one Treasury vault position in raw millis. Vaults name
// the Treasury itself through the closed vault vocabulary: no holder
// label ever enters this shape.
type VaultTotal struct {
	Vault  domain.TreasuryVault
	Millis int64
}

// CustodyTotalsReading is one exact re-derivation of the custody
// aggregates from the journal: supply, Treasury total and vaults,
// sovereign reserve, third-party circulation with its custody count,
// active holds with their count and the frozen flag. Source counts
// stay inside this reading: the public snapshot carries amounts only.
type CustodyTotalsReading struct {
	SupplyMillis         int64
	TreasuryMillis       int64
	Vaults               []VaultTotal
	ReserveMillis        int64
	CirculationMillis    int64
	CirculationCustodies int64
	LockedMillis         int64
	LockedHolds          int64
	Frozen               bool
}

// CustodyTotalsSnapshot is the published custody totals document: the
// locale and its titles, the derivation instant with the cache window,
// the sovereign aggregates in full and the third-party aggregates
// suppressed below the low-count threshold. Custody identifiers,
// transfer identifiers, governors and holder labels never enter this
// shape: reflection must find instants, integers, flags, the closed
// vault vocabulary and dictionary titles only.
type CustodyTotalsSnapshot struct {
	Locale                string
	Labels                domain.TotalsLabels
	GeneratedAt           time.Time
	CacheSeconds          int64
	SupplyMillis          int64
	TreasuryMillis        int64
	Vaults                []VaultTotal
	ReserveMillis         int64
	CirculationMillis     int64
	CirculationSuppressed bool
	LockedMillis          int64
	LockedHolds           int64
	LockedSuppressed      bool
	Frozen                bool
}

// CustodyTotalsRepository re-derives the custody aggregates from the
// journal without writing anything. Derivation rebuilds from source
// rows on every call: no derived table is ever read.
type CustodyTotalsRepository interface {
	// ReadCustodyTotals resolves one exact reading of every custody
	// aggregate of one season book. Reads never mint, freeze or
	// move anything.
	ReadCustodyTotals(ctx context.Context, season domain.SeasonKey) (*CustodyTotalsReading, error)
}

// CustodyTotalsUseCase publishes one privacy-safe custody totals
// document per locale: exact sovereign aggregates with suppressed
// third-party amounts, served from a bounded cache so readers share
// one derivation per window. It is an internal operation: no public
// surface calls it.
type CustodyTotalsUseCase struct {
	totals   CustodyTotalsRepository
	clock    Clock
	cached   *CustodyTotalsSnapshot
	cachedAt time.Time
	cachedOf string
}

// NewCustodyTotalsUseCase creates an instance of CustodyTotalsUseCase,
// refusing incomplete composition.
func NewCustodyTotalsUseCase(totals CustodyTotalsRepository, clock Clock) (*CustodyTotalsUseCase, error) {
	if totals == nil || clock == nil {
		return nil, domain.ErrInvalidTotals
	}
	return &CustodyTotalsUseCase{totals: totals, clock: clock}, nil
}

// Totals serves the document in one locale for one season book: the
// cached derivation while the window holds, a fresh re-derivation
// afterwards. Unknown locales and missing books refuse before any
// reading happens; the frozen flag travels as information, never as
// a refusal to render.
func (uc *CustodyTotalsUseCase) Totals(ctx context.Context, locale, season string) (CustodyTotalsSnapshot, error) {
	parsed, err := domain.ParseTotalsLocale(locale)
	if err != nil {
		return CustodyTotalsSnapshot{}, err
	}
	book, err := domain.ParseSeasonKey(season)
	if err != nil {
		return CustodyTotalsSnapshot{}, err
	}
	now := uc.clock.Now().UTC()
	if uc.cached != nil && uc.cachedOf == parsed.String()+"\x00"+book.String() &&
		now.Before(uc.cachedAt.Add(domain.TotalsCacheSeconds*time.Second)) {
		return *uc.cached, nil
	}
	reading, err := uc.totals.ReadCustodyTotals(ctx, book)
	if err != nil {
		return CustodyTotalsSnapshot{}, err
	}
	if reading == nil {
		return CustodyTotalsSnapshot{}, domain.ErrInvalidTotals
	}
	snapshot := assembleTotals(parsed, reading, now)
	uc.cached = &snapshot
	uc.cachedAt = now
	uc.cachedOf = parsed.String() + "\x00" + book.String()
	return snapshot, nil
}

// assembleTotals renders one document from one exact reading: the
// sovereign classes travel whole, the third-party classes pass the
// low-count rule, and every title comes from the closed dictionary so
// no source string can reach the reader.
func assembleTotals(locale domain.TotalsLocale, reading *CustodyTotalsReading, now time.Time) CustodyTotalsSnapshot {
	vaults := make([]VaultTotal, 0, len(reading.Vaults))
	for _, position := range reading.Vaults {
		if !position.Vault.IsValid() {
			continue
		}
		vaults = append(vaults, position)
	}
	circulation, circulationSuppressed := domain.SuppressTotal(reading.CirculationMillis, reading.CirculationCustodies)
	locked, lockedSuppressed := domain.SuppressTotal(reading.LockedMillis, reading.LockedHolds)
	return CustodyTotalsSnapshot{
		Locale:                locale.String(),
		Labels:                domain.TotalsLabelsFor(locale),
		GeneratedAt:           now,
		CacheSeconds:          domain.TotalsCacheSeconds,
		SupplyMillis:          reading.SupplyMillis,
		TreasuryMillis:        reading.TreasuryMillis,
		Vaults:                vaults,
		ReserveMillis:         reading.ReserveMillis,
		CirculationMillis:     circulation,
		CirculationSuppressed: circulationSuppressed,
		LockedMillis:          locked,
		LockedHolds:           reading.LockedHolds,
		LockedSuppressed:      lockedSuppressed,
		Frozen:                reading.Frozen,
	}
}
