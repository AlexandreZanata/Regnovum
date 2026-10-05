package domain

// HypotheticalBondStatus identifies the unratified evaluation stage of Crown Bonds.
// Under docs/reino/RESPOSTAS.md §26 and docs/reino/DECISOES_VIGENTES.md Q26, Crown Bonds
// are concept only ("somente conceito") pending jurisdiction-specific legal review and
// formal contract design. No bond is offered or available in production.
type HypotheticalBondStatus string

const (
	// BondStatusConceptOnly marks the unratified proposal stage.
	BondStatusConceptOnly HypotheticalBondStatus = "conceito"
	// BondStatusPendingLegalReview marks the required legal review gate.
	BondStatusPendingLegalReview HypotheticalBondStatus = "pendente-analise-juridica"
)

// ParseHypotheticalBondStatus validates a status token against the closed vocabulary.
func ParseHypotheticalBondStatus(raw string) (HypotheticalBondStatus, error) {
	switch HypotheticalBondStatus(raw) {
	case BondStatusConceptOnly, BondStatusPendingLegalReview:
		return HypotheticalBondStatus(raw), nil
	default:
		return "", ErrUnknownCustody
	}
}

// String returns the stored status value.
func (s HypotheticalBondStatus) String() string {
	return string(s)
}

// HypotheticalBondTerms documents the proposed parameters of a Crown Bond
// (docs/reino/CARTA_ECONOMICA.md §7). It exists purely for evaluation and threat modeling.
//
// Invariants of the hypothetical contract:
//  1. Principal must be segregated in CustodyTitle ("title") and returned at maturity.
//     Principal hold can never be spent for ordinary Crown expenses (CustodyTitle.CanSpend() == false).
//  2. Yield is variable, never guaranteed, can be zero, and must be funded solely from Free Treasury
//     (Tesouro Livre), never from currency emission or minting.
//  3. No yield advertising, promises, or public marketing.
//  4. Proposed formulas (Y = min(5% × R4, limit), 20% cap) are unratified suggestions,
//     never homologated rules.
//  5. Crown Bonds remain strictly unavailable in production pending formal legal review.
type HypotheticalBondTerms struct {
	// Custody must strictly be CustodyTitle ("title").
	Custody CustodyKind
	// DurationDays is the proposed maturity lock in days (e.g. 90, 180).
	DurationDays int
	// WeightFactor is the proposed term weighting factor.
	WeightFactor int
	// YieldCapped confirms yield is bounded by Free Treasury, never unbounded.
	YieldCapped bool
	// YieldGuaranteed must be false; guaranteed returns are strictly prohibited.
	YieldGuaranteed bool
	// FundedByEmission must be false; minting or inflation backing is forbidden.
	FundedByEmission bool
	// Status records the evaluation stage.
	Status HypotheticalBondStatus
}

// ValidateHypotheticalContract inspects the hypothetical terms against the strict
// non-negotiable invariants (segregated principal in CustodyTitle, zero guaranteed yield,
// no emission funding, no ordinary spend).
//
// Because Crown Bonds remain unratified and pending legal review, even valid hypothetical
// terms cannot be activated: this function confirms invariant safety and then returns
// ErrBondsUnavailable to guarantee fail-closed evaluation.
func ValidateHypotheticalContract(terms HypotheticalBondTerms) error {
	if terms.Custody != CustodyTitle {
		return ErrUnauthorizedCustody
	}
	if terms.YieldGuaranteed {
		return ErrBondYieldForbidden
	}
	if terms.FundedByEmission {
		return ErrInvalidGenesisKey
	}
	if !terms.YieldCapped {
		return ErrBondYieldForbidden
	}
	if terms.DurationDays <= 0 || terms.WeightFactor <= 0 {
		return ErrInvalidMilliInk
	}
	if terms.Status != BondStatusConceptOnly && terms.Status != BondStatusPendingLegalReview {
		return ErrUnknownCustody
	}
	return ErrBondsUnavailable
}

// AreBondsAvailable reports whether Crown Bonds can be offered, purchased, or checked out.
// It returns false unconditionally pending legal review and formal ratification.
func AreBondsAvailable() bool {
	return false
}

// EvaluateBondPurchase rejects any attempt to purchase or issue a Crown Bond.
func EvaluateBondPurchase() error {
	return ErrBondsUnavailable
}

// EvaluateBondYieldPublication rejects any attempt to publish, promise, or advertise bond yield.
func EvaluateBondYieldPublication(ratePercent float64) error {
	return ErrBondYieldForbidden
}
