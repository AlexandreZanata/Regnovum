package domain

// CountryCode is the attested country of one peer payment in ISO
// 3166-1 alpha-2 shape: exactly two ASCII uppercase letters. The
// shape is syntax only: no code is approved by parsing, and approval
// itself is a juridical act outside this module, so the approved set
// stays empty until that review.
type CountryCode string

// ParseCountryCode validates a country against the ISO shape.
// Matching is exact: no trimming, no case folding, no combined
// values. A malformed country refuses before any gate is read.
func ParseCountryCode(raw string) (CountryCode, error) {
	if len(raw) != 2 {
		return "", ErrInvalidJurisdiction
	}
	for _, c := range raw {
		if c < 'A' || c > 'Z' {
			return "", ErrInvalidJurisdiction
		}
	}
	return CountryCode(raw), nil
}

// String returns the stored country value.
func (c CountryCode) String() string { return string(c) }

// JurisdictionEvidence is the minimal attestation one peer payment
// carries into the activation gate: the attested country and three
// verified booleans. Collection is proportional by construction:
// there is no name, no document, no address, no account and no
// network signal here — only what a verifier attested. Headers, IPs
// and VPN hints never enter this shape, so they can never decide.
type JurisdictionEvidence struct {
	Country           CountryCode
	AgeVerified       bool
	ResidenceVerified bool
	IDVerified        bool
}

// JurisdictionReason names the closed outcome vocabulary of the
// activation gate.
type JurisdictionReason string

const (
	// JurisdictionApproved is the open gate: unreachable while no
	// juridical review approved a country.
	JurisdictionApproved JurisdictionReason = "approved"
	// JurisdictionUnknownCountry refuses an unattested country:
	// the empty shape decides nothing.
	JurisdictionUnknownCountry JurisdictionReason = "unknown_country"
	// JurisdictionAgeUnverified refuses unverified age: minority
	// never trades, even where a country is approved.
	JurisdictionAgeUnverified JurisdictionReason = "age_unverified"
	// JurisdictionResidenceUnverified refuses unverified residence:
	// presence never trades on claim alone.
	JurisdictionResidenceUnverified JurisdictionReason = "residence_unverified"
	// JurisdictionIdentificationUnverified refuses unverified
	// identity: anonymity never trades.
	JurisdictionIdentificationUnverified JurisdictionReason = "identification_unverified"
	// JurisdictionCountryUnapproved refuses a well-formed country
	// with no juridical approval: without that review the gate
	// stays shut for every code, including well-formed ones.
	JurisdictionCountryUnapproved JurisdictionReason = "country_unapproved"
)

// AllJurisdictionReasons returns the closed vocabulary in canonical
// order.
func AllJurisdictionReasons() []JurisdictionReason {
	return []JurisdictionReason{
		JurisdictionApproved, JurisdictionUnknownCountry, JurisdictionAgeUnverified,
		JurisdictionResidenceUnverified, JurisdictionIdentificationUnverified,
		JurisdictionCountryUnapproved,
	}
}

// String returns the stored reason value.
func (r JurisdictionReason) String() string { return string(r) }

// JurisdictionDecision is one gate outcome: whether the payment may
// proceed and why. Denial is a decision, not an error: callers count
// it, they never retry it into approval.
type JurisdictionDecision struct {
	Allowed bool
	Reason  JurisdictionReason
}

// ApprovedCountries returns the juridically approved country set:
// empty. Approval is an act of legal review outside this module, so
// no code path approves a country by inference, by volume, by header
// or by internal call. The gate below denies every country against
// this set until that review exists.
func ApprovedCountries() []CountryCode {
	return nil
}

// AuthorizeJurisdiction judges one attested payment against the
// approved set, fail-closed in a fixed order: unattested country,
// unverified age, unverified residence, unverified identity, then
// the juridical approval. The order is stable so denials count the
// first missing attestation; no later check can reopen an earlier
// denial. Network signals cannot bypass it: they are not parameters
// here. Internal calls cannot bypass it either: the approved set is
// empty, so even the best-formed evidence denies today.
func AuthorizeJurisdiction(evidence JurisdictionEvidence) JurisdictionDecision {
	if evidence.Country == "" {
		return JurisdictionDecision{Reason: JurisdictionUnknownCountry}
	}
	if !evidence.AgeVerified {
		return JurisdictionDecision{Reason: JurisdictionAgeUnverified}
	}
	if !evidence.ResidenceVerified {
		return JurisdictionDecision{Reason: JurisdictionResidenceUnverified}
	}
	if !evidence.IDVerified {
		return JurisdictionDecision{Reason: JurisdictionIdentificationUnverified}
	}
	for _, approved := range ApprovedCountries() {
		if approved == evidence.Country {
			return JurisdictionDecision{Allowed: true, Reason: JurisdictionApproved}
		}
	}
	return JurisdictionDecision{Reason: JurisdictionCountryUnapproved}
}

// JurisdictionDimension names one axis of the per-country legal
// matrix: age, residence, volume, fraud and identification
// requirements. The matrix is declared, not calibrated: no threshold
// lives here, because unratified proposals never become rules by
// inference.
type JurisdictionDimension string

const (
	// JurisdictionAge gates verified majority: enforced.
	JurisdictionAge JurisdictionDimension = "age"
	// JurisdictionResidence gates verified residence: enforced.
	JurisdictionResidence JurisdictionDimension = "residence"
	// JurisdictionVolume tracks declared volume: monitored without
	// a ratified ceiling, so the gate records the axis and enforces
	// nothing.
	JurisdictionVolume JurisdictionDimension = "volume"
	// JurisdictionFraud tracks fraud signals: monitored without a
	// ratified score, so the gate records the axis and enforces
	// nothing.
	JurisdictionFraud JurisdictionDimension = "fraud"
	// JurisdictionIdentification gates verified identity: enforced.
	JurisdictionIdentification JurisdictionDimension = "identification"
)

// AllJurisdictionDimensions returns the closed matrix in canonical
// order.
func AllJurisdictionDimensions() []JurisdictionDimension {
	return []JurisdictionDimension{
		JurisdictionAge, JurisdictionResidence, JurisdictionVolume,
		JurisdictionFraud, JurisdictionIdentification,
	}
}

// String returns the stored dimension value.
func (d JurisdictionDimension) String() string { return string(d) }

// DimensionEnforced reports whether the gate enforces one matrix
// axis today. Country approval, age, residence and identification
// enforce; volume and fraud stay monitored until a juridical review
// ratifies their thresholds, and unratified numbers never enter the
// gate by inference.
func DimensionEnforced(dimension JurisdictionDimension) bool {
	switch dimension {
	case JurisdictionAge, JurisdictionResidence, JurisdictionIdentification:
		return true
	default:
		return false
	}
}

// JurisdictionCounters are the public gate metrics: one counter per
// denial reason plus approvals. Every field is an integer: no
// account, no country, no header and no PII ever reaches a public
// metric.
type JurisdictionCounters struct {
	Approved                 uint64
	UnknownCountry           uint64
	AgeUnverified            uint64
	ResidenceUnverified      uint64
	IdentificationUnverified uint64
	CountryUnapproved        uint64
}

// Record counts one gate outcome.
func (c *JurisdictionCounters) Record(decision JurisdictionDecision) {
	switch decision.Reason {
	case JurisdictionApproved:
		c.Approved++
	case JurisdictionUnknownCountry:
		c.UnknownCountry++
	case JurisdictionAgeUnverified:
		c.AgeUnverified++
	case JurisdictionResidenceUnverified:
		c.ResidenceUnverified++
	case JurisdictionIdentificationUnverified:
		c.IdentificationUnverified++
	default:
		c.CountryUnapproved++
	}
}

// Snapshot copies the public gate metrics.
func (c JurisdictionCounters) Snapshot() JurisdictionCounters {
	return c
}
