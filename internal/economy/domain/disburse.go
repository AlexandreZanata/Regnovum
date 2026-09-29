package domain

// DisbursementPurpose is the closed vocabulary of what one Treasury
// disbursement may pay for: the obligation classes the phase file
// names — accepted sales, approved Crumbs, compensations and due
// payments. The names are structural mechanics only: amounts, shares
// and budgets stay explicit per act, and later phases match these
// tokens instead of inventing parallel ones.
type DisbursementPurpose string

const (
	// DisbursementSaleSettlement pays an accepted sale.
	DisbursementSaleSettlement DisbursementPurpose = "sale_settlement"
	// DisbursementCrumbDistribution pays an approved Crumb.
	DisbursementCrumbDistribution DisbursementPurpose = "crumb_distribution"
	// DisbursementCompensation pays a compensation.
	DisbursementCompensation DisbursementPurpose = "compensation"
	// DisbursementDuePayment pays a due payment.
	DisbursementDuePayment DisbursementPurpose = "due_payment"
)

// AllDisbursementPurposes returns the closed vocabulary in canonical
// order.
func AllDisbursementPurposes() []DisbursementPurpose {
	return []DisbursementPurpose{
		DisbursementSaleSettlement,
		DisbursementCrumbDistribution,
		DisbursementCompensation,
		DisbursementDuePayment,
	}
}

// ParseDisbursementPurpose validates a purpose against the schema CHECK
// vocabulary. Matching is exact: surrounding whitespace is not
// trimmed, so two spellings can never name one purpose. Unknown
// purposes are refused.
func ParseDisbursementPurpose(raw string) (DisbursementPurpose, error) {
	purpose := DisbursementPurpose(raw)
	if !purpose.IsValid() {
		return "", ErrInvalidDisbursement
	}
	return purpose, nil
}

// IsValid reports whether the purpose belongs to the closed
// vocabulary.
func (p DisbursementPurpose) IsValid() bool {
	switch p {
	case DisbursementSaleSettlement,
		DisbursementCrumbDistribution,
		DisbursementCompensation,
		DisbursementDuePayment:
		return true
	default:
		return false
	}
}

// String returns the stored purpose value.
func (p DisbursementPurpose) String() string { return string(p) }
