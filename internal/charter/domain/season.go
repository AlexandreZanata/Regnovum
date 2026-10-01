package domain

import (
	"strings"
	"unicode"
)

// SeasonalResetSeconds pins the ninety-day book: 90 × 24 × 60 × 60 in
// UTC, semi-open [starts_at, ends_at). Civil months, browser clocks
// and DST never enter the calendar.
const SeasonalResetSeconds = 7776000

// SeasonalTerms is the opt-in disclosure a new Charter acceptance
// must show before adhesion: the ninety-day reset, the absence of
// carry-over, the preserved history and the real function. Old
// acceptances keep their verdict without these fields: they remain
// valid for paid rights already acquired, but they never authorize a
// seasonal conversion on their own.
type SeasonalTerms struct {
	ResetSeconds      int
	NoCarryOver       bool
	HistoryKept       bool
	RealFunction      string
	ResetAcknowledged bool
}

// ParseSeasonalTerms validates one seasonal disclosure. The reset is
// exactly 7776000 seconds, carry-over is never promised, history is
// always kept and the real function is an explicit non-blank token
// without control characters. Anything else is refused before any
// verdict is recorded.
func ParseSeasonalTerms(resetSeconds int, noCarryOver, historyKept bool, realFunction string, acknowledged bool) (SeasonalTerms, error) {
	if resetSeconds != SeasonalResetSeconds {
		return SeasonalTerms{}, ErrInvalidCharter
	}
	if !noCarryOver {
		return SeasonalTerms{}, ErrInvalidCharter
	}
	if !historyKept {
		return SeasonalTerms{}, ErrInvalidCharter
	}
	trimmed := strings.TrimSpace(realFunction)
	if trimmed == "" || trimmed != realFunction {
		return SeasonalTerms{}, ErrInvalidCharter
	}
	for _, r := range realFunction {
		if unicode.IsControl(r) {
			return SeasonalTerms{}, ErrInvalidCharter
		}
	}
	if !acknowledged {
		return SeasonalTerms{}, ErrInvalidCharter
	}
	return SeasonalTerms{
		ResetSeconds:      resetSeconds,
		NoCarryOver:       noCarryOver,
		HistoryKept:       historyKept,
		RealFunction:      realFunction,
		ResetAcknowledged: true,
	}, nil
}

// AuthorizesSeasonalConversion reports whether one disclosure unlocks
// a seasonal conversion: every term shown and explicitly
// acknowledged. An omitted reset or a legacy acceptance without
// these terms never authorizes it; paid credit without a new
// acceptance stays valid by preservation, not by conversion.
func (t SeasonalTerms) AuthorizesSeasonalConversion() bool {
	return t.ResetSeconds == SeasonalResetSeconds && t.NoCarryOver && t.HistoryKept && t.RealFunction != "" && t.ResetAcknowledged
}
