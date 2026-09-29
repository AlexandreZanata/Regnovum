package domain

// RestitutionCause is the closed vocabulary of why the Treasury pays a
// third-party restitution for a platform error: the three scenarios the
// phase file names — a broken contract, a paying suspension and a
// chargeback. The names are structural mechanics only: amounts, shares
// and budgets stay explicit per act, and fiat settlements never reach
// the INK journal (they reconcile in the separate fiat books per P33).
// Q10/Q11/Q12 stay PENDENTE, so no approval, price or escrow effect is
// vigente here — later phases fund restitutions from available Treasury
// stock once the titular ratifies them, and never by inference from
// this vocabulary.
type RestitutionCause string

const (
	// RestitutionBrokenContract repairs a broken platform contract.
	RestitutionBrokenContract RestitutionCause = "broken_contract"
	// RestitutionPayingSuspension repairs a suspension that kept charging.
	RestitutionPayingSuspension RestitutionCause = "paying_suspension"
	// RestitutionChargeback repairs a contested charge passed through.
	RestitutionChargeback RestitutionCause = "chargeback"
)

// AllRestitutionCauses returns the closed vocabulary in canonical order.
func AllRestitutionCauses() []RestitutionCause {
	return []RestitutionCause{
		RestitutionBrokenContract,
		RestitutionPayingSuspension,
		RestitutionChargeback,
	}
}

// ParseRestitutionCause validates a cause against the closed vocabulary.
// Matching is exact: surrounding whitespace is not trimmed, so two
// spellings can never name one cause. Unknown causes are refused.
func ParseRestitutionCause(raw string) (RestitutionCause, error) {
	cause := RestitutionCause(raw)
	if !cause.IsValid() {
		return "", ErrInvalidDisbursement
	}
	return cause, nil
}

// IsValid reports whether the cause belongs to the closed vocabulary.
func (c RestitutionCause) IsValid() bool {
	switch c {
	case RestitutionBrokenContract,
		RestitutionPayingSuspension,
		RestitutionChargeback:
		return true
	default:
		return false
	}
}

// String returns the stored cause value.
func (c RestitutionCause) String() string { return string(c) }
