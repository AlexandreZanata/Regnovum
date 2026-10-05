package domain

import "strings"

// BettingMarketStatus marks the lifecycle state of prediction/betting markets.
// Under docs/reino/RESPOSTAS.md §29 and docs/reino/DECISOES_VIGENTES.md Q29,
// betting and prediction markets remain a disabled constitutional concept
// ("possibilidade constitucional desativada") pending country-specific legal review,
// licensing, AML, objective verification, and conflict safeguards.
type BettingMarketStatus string

const (
	// BettingMarketStatusDisabledByDefault is the permanent default state in this release.
	BettingMarketStatusDisabledByDefault BettingMarketStatus = "desativado-por-padrao"
	// BettingMarketStatusPendingCountryReview marks evaluation under country preconditions.
	BettingMarketStatusPendingCountryReview BettingMarketStatus = "pendente-precondicoes-pais"
)

// ParseBettingMarketStatus validates a status string against the closed vocabulary.
func ParseBettingMarketStatus(raw string) (BettingMarketStatus, error) {
	switch BettingMarketStatus(raw) {
	case BettingMarketStatusDisabledByDefault, BettingMarketStatusPendingCountryReview:
		return BettingMarketStatus(raw), nil
	default:
		return "", ErrBettingPreconditionMissing
	}
}

// String returns the stored status string.
func (s BettingMarketStatus) String() string {
	return string(s)
}

// BettingPreconditions records the mandatory country-by-country safeguards
// required before any market or prediction product could ever be evaluated.
//
// Every item is mandatory (docs/reino/RESPOSTAS.md §29):
// 1. Legal review per country/jurisdiction.
// 2. Regulatory authorization/license where required.
// 3. Age and geographic location verification.
// 4. Anti-Money Laundering (AML) compliance.
// 5. Objective, verifiable event definition (no subjective or arbitrary resolution).
// 6. Independent, tamper-resistant oracle resolution source.
// 7. Restriction and isolation of parties with conflicts of interest.
// 8. Absolute prohibition of real-world harm, injury, violence, or harassment.
//
// In this release, real betting and INK-based wagering remain completely disabled.
type BettingPreconditions struct {
	CountryCode               string
	LegalReviewPassed         bool
	RegulatoryLicenseGranted  bool
	AgeAndLocationVerified    bool
	AntiMoneyLaunderingPassed bool
	ObjectiveEvent            bool
	IndependentOracle         bool
	ConflictsRestricted       bool
	InvolvesRealHarm          bool
	Status                    BettingMarketStatus
}

// ValidateBettingPreconditions inspects a market proposal against the mandatory
// country-by-country safeguards.
//
// Invariants enforced:
//   - Any proposal linked to real-world harm fails immediately with ErrRealHarmProhibited.
//   - Any omitted safeguard fails with ErrBettingPreconditionMissing.
//   - Even if all safeguards are met, markets cannot be offered: the function returns
//     ErrBettingMarketsUnavailable to guarantee fail-closed evaluation.
func ValidateBettingPreconditions(p BettingPreconditions) error {
	if p.InvolvesRealHarm {
		return ErrRealHarmProhibited
	}
	if strings.TrimSpace(p.CountryCode) == "" || len(p.CountryCode) < 2 {
		return ErrBettingPreconditionMissing
	}
	if !p.LegalReviewPassed || !p.RegulatoryLicenseGranted {
		return ErrBettingPreconditionMissing
	}
	if !p.AgeAndLocationVerified || !p.AntiMoneyLaunderingPassed {
		return ErrBettingPreconditionMissing
	}
	if !p.ObjectiveEvent || !p.IndependentOracle || !p.ConflictsRestricted {
		return ErrBettingPreconditionMissing
	}
	if p.Status != BettingMarketStatusDisabledByDefault && p.Status != BettingMarketStatusPendingCountryReview {
		return ErrBettingPreconditionMissing
	}
	return ErrBettingMarketsUnavailable
}

// AreBettingMarketsAvailable reports whether betting or prediction markets are enabled.
// It unconditionally returns false in production.
func AreBettingMarketsAvailable() bool {
	return false
}

// AttemptPlaceBet rejects any attempt to place a bet or wager using real funds or INK.
func AttemptPlaceBet() error {
	return ErrBettingMarketsUnavailable
}

// AttemptCreateMarket rejects any attempt to initialize or open a betting market.
func AttemptCreateMarket() error {
	return ErrBettingMarketsUnavailable
}

// AttemptSettleBet rejects any attempt to settle or disburse betting proceeds.
func AttemptSettleBet() error {
	return ErrBettingMarketsUnavailable
}
