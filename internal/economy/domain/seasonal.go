package domain

// SeasonalResetSeconds pins the ninety-day book for the economy
// family: 90 × 24 × 60 × 60 in UTC, semi-open [starts_at, ends_at).
// It mirrors the charter disclosure without importing it: domains
// stay disjoint by architecture.
const SeasonalResetSeconds = 7776000

// RequireSeasonalReset refuses a seasonal conversion without an
// explicit reset acknowledgement: an omitted reset or a legacy
// acceptance alone never authorizes it. The compat-legacy book is
// the explicitly inactive pre-season namespace and keeps its own
// legacy path without a reset acknowledgement; every other book
// needs it. Paid credit without a new acceptance stays valid by
// preservation, never by conversion here.
func RequireSeasonalReset(season SeasonKey, resetAcknowledged bool) error {
	if season == SeasonKey(CompatSeasonKey) {
		return nil
	}
	if !resetAcknowledged {
		return ErrConsentRequired
	}
	return nil
}
