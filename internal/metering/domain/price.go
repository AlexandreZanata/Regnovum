package domain

import (
	"sort"
	"time"
)

// PriceEntry is one versioned price of a service: the service it
// charges, the table version, the half-open validity window
// [ValidFrom, ValidUntil) in UTC, the integer milliINK amount per
// canonical unit, the canonical unit itself and the recorded
// approving authority.
//
// Prices travel as integers only (never float). The entry carries
// no ratified value: every field arrives from the caller, and a
// missing entry never defaults to another service, version or
// window.
type PriceEntry struct {
	Service    ServiceID
	Version    int
	ValidFrom  time.Time
	ValidUntil time.Time
	PriceMilli int64
	Unit       BillingUnit
	Authority  string
}

// PriceRequest carries the fields of one price entry. Callers build
// the table version by version; no ratified value lives here.
type PriceRequest struct {
	Service    ServiceID
	Version    int
	ValidFrom  time.Time
	ValidUntil time.Time
	PriceMilli int64
	Unit       BillingUnit
	Authority  string
}

// NewPriceEntry validates one price entry. Times are normalized to
// UTC; the window is half-open, so ValidFrom must be strictly before
// ValidUntil. Zero instants, empty windows, non-positive versions or
// prices, unknown units and empty authorities all refuse.
func NewPriceEntry(req PriceRequest) (PriceEntry, error) {
	if req.Service.String() == "" {
		return PriceEntry{}, ErrInvalidService
	}
	if _, err := ParseServiceID(req.Service.String()); err != nil {
		return PriceEntry{}, err
	}
	if req.Version <= 0 {
		return PriceEntry{}, ErrInvalidVersion
	}
	if req.ValidFrom.IsZero() || req.ValidUntil.IsZero() {
		return PriceEntry{}, ErrInvalidPriceWindow
	}
	start := req.ValidFrom.UTC()
	end := req.ValidUntil.UTC()
	if !start.Before(end) {
		return PriceEntry{}, ErrInvalidPriceWindow
	}
	if req.PriceMilli <= 0 {
		return PriceEntry{}, ErrInvalidPrice
	}
	if !req.Unit.IsValid() {
		return PriceEntry{}, ErrUnknownUnit
	}
	approved, err := ParseAuthority(req.Authority)
	if err != nil {
		return PriceEntry{}, err
	}
	return PriceEntry{
		Service:    req.Service,
		Version:    req.Version,
		ValidFrom:  start,
		ValidUntil: end,
		PriceMilli: req.PriceMilli,
		Unit:       req.Unit,
		Authority:  approved,
	}, nil
}

// Covers reports whether the entry prices the given instant: the
// half-open window [ValidFrom, ValidUntil). The boundary instant
// itself belongs to the next entry, never to this one.
func (e PriceEntry) Covers(at time.Time) bool {
	instant := at.UTC()
	return !instant.Before(e.ValidFrom) && instant.Before(e.ValidUntil)
}

// overlaps reports whether two entries of the same service share any
// instant. Abutting windows ([a,b) + [b,c)) do not overlap.
func (e PriceEntry) overlaps(other PriceEntry) bool {
	if e.Service != other.Service {
		return false
	}
	return e.ValidFrom.Before(other.ValidUntil) && other.ValidFrom.Before(e.ValidUntil)
}

// Catalog is the versioned price table by service and time. It starts
// empty: handlers read prices from here instead of hardcoding any
// value. Entries are kept in insertion order; lookups never mutate
// the catalog.
type Catalog struct {
	entries []PriceEntry
}

// Add records one validated entry. Windows of the same service must
// not overlap, and versions of the same service are never reused, so
// every previous version stays retrievable by ByVersion. Abutting
// windows are allowed: the change takes effect exactly at the
// boundary instant.
func (c *Catalog) Add(entry PriceEntry) error {
	if entry.Service.String() == "" || entry.Version <= 0 || entry.PriceMilli <= 0 {
		return ErrInvalidPrice
	}
	if entry.ValidFrom.IsZero() || entry.ValidUntil.IsZero() || !entry.ValidFrom.Before(entry.ValidUntil) {
		return ErrInvalidPriceWindow
	}
	if !entry.Unit.IsValid() {
		return ErrUnknownUnit
	}
	if _, err := ParseAuthority(entry.Authority); err != nil {
		return err
	}
	for _, existing := range c.entries {
		if existing.Service == entry.Service && existing.Version == entry.Version {
			return ErrDuplicatePriceVersion
		}
		if existing.overlaps(entry) {
			return ErrOverlappingPrice
		}
	}
	c.entries = append(c.entries, entry)
	return nil
}

// PriceAt returns the entry covering the service at the given
// instant. Unknown services, instants before the first window, gaps
// and instants at or after the last expiry all refuse with
// ErrPriceNotFound instead of defaulting. A zero instant is an
// invalid query, not a missing price.
func (c Catalog) PriceAt(service ServiceID, at time.Time) (PriceEntry, error) {
	if service.String() == "" {
		return PriceEntry{}, ErrInvalidService
	}
	if at.IsZero() {
		return PriceEntry{}, ErrInvalidPriceWindow
	}
	for _, entry := range c.entries {
		if entry.Service == service && entry.Covers(at) {
			return entry, nil
		}
	}
	return PriceEntry{}, ErrPriceNotFound
}

// ByVersion returns the entry of one service and version, even after
// newer versions took effect. Previous versions stay auditável: a
// price change never rewrites history.
func (c Catalog) ByVersion(service ServiceID, version int) (PriceEntry, error) {
	if version <= 0 {
		return PriceEntry{}, ErrInvalidVersion
	}
	for _, entry := range c.entries {
		if entry.Service == service && entry.Version == version {
			return entry, nil
		}
	}
	return PriceEntry{}, ErrPriceNotFound
}

// History returns every entry of one service ordered by ValidFrom,
// then by version. The returned slice is a copy.
func (c Catalog) History(service ServiceID) []PriceEntry {
	out := make([]PriceEntry, 0)
	for _, entry := range c.entries {
		if entry.Service == service {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ValidFrom.Equal(out[j].ValidFrom) {
			return out[i].Version < out[j].Version
		}
		return out[i].ValidFrom.Before(out[j].ValidFrom)
	})
	return out
}

// Len returns the number of entries in the catalog.
func (c Catalog) Len() int { return len(c.entries) }
