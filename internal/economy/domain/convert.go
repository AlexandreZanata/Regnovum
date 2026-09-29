package domain

import "math"

// MillisPerLegacyInk fixes the legacy unit on the milli scale: legacy INK
// has no subunit, so one legacy unit is exactly 1000 milliINK and an
// opt-in quantity converts only in whole units.
const MillisPerLegacyInk int64 = 1000

// LegacyUnitsOf reads whole legacy INK units out of an opt-in quantity.
// Quantities that are not whole units cannot convert: dust has no ledger
// expression on the legacy side.
func LegacyUnitsOf(quantity MilliInk) (int64, error) {
	if quantity.Millis()%MillisPerLegacyInk != 0 {
		return 0, ErrInvalidCharter
	}
	return quantity.Millis() / MillisPerLegacyInk, nil
}

// ConvertedMillis prices legacy units at an exact rational rate with
// floor: the holder receives whole subunits only, and dust stays
// unconverted. Overflow fails instead of wrapping, and a dust-only
// conversion is refused instead of extinguishing value for nothing.
func ConvertedMillis(legacyUnits int64, rate ConversionRate) (MilliInk, error) {
	if legacyUnits <= 0 || rate.Num <= 0 || rate.Den <= 0 {
		return MilliInk{}, ErrInvalidCharter
	}
	if legacyUnits > math.MaxInt64/rate.Num {
		return MilliInk{}, ErrMilliInkOverflow
	}
	converted, err := NewMilliInk(legacyUnits * rate.Num / rate.Den)
	if err != nil {
		return MilliInk{}, err
	}
	if converted.IsZero() {
		return MilliInk{}, ErrInvalidCharter
	}
	return converted, nil
}
