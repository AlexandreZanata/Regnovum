package domain

// GrantSource answers where a new monetary grant may come from. Before
// Genesis the legacy books grant by their own contracts, untouched by
// this rule. After Genesis every new monetary unit leaves Treasury
// stock, or the grant does not happen: stockout is unavailability,
// never mint.
type GrantSource string

const (
	// GrantSourceLegacy serves grants from the legacy books, which keep
	// their contracted behavior while Genesis never happened.
	GrantSourceLegacy GrantSource = "legacy"
	// GrantSourceTreasury serves grants from Treasury stock, debited in
	// the same transaction as the grant.
	GrantSourceTreasury GrantSource = "treasury"
	// GrantSourceUnavailable refuses the grant: no stock, no grant.
	GrantSourceUnavailable GrantSource = "unavailable"
)

// MonetaryGrantSource decides one grant funding. A non-positive request
// is refused instead of answered, so zero-amount grants can never
// probe the rule.
func MonetaryGrantSource(genesisHappened bool, stock, requested MilliInk) (GrantSource, error) {
	if requested.IsZero() {
		return "", ErrInvalidGrant
	}
	if !genesisHappened {
		return GrantSourceLegacy, nil
	}
	if _, err := stock.Sub(requested); err != nil {
		return GrantSourceUnavailable, nil
	}
	return GrantSourceTreasury, nil
}
