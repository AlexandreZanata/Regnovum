package domain

// Direction is the signed effect of an INK operation on a bucket.
type Direction uint8

const (
	// DirectionCredit adds INK to a bucket.
	DirectionCredit Direction = iota + 1

	// DirectionDebit removes INK from a bucket (publication cost, admin
	// adjustment or expiration of the franchise).
	DirectionDebit
)

// IsValid reports whether the direction is an authorized enum value.
func (d Direction) IsValid() bool {
	return d == DirectionCredit || d == DirectionDebit
}

// String renders the direction name.
func (d Direction) String() string {
	switch d {
	case DirectionCredit:
		return "credit"
	case DirectionDebit:
		return "debit"
	default:
		return "invalid"
	}
}

// Apply returns the signed bigint ledger delta for a transaction amount:
// positive for credits, negative for debits. Zero amounts are not valid
// ledger entries, and invalid directions never produce a delta.
func (d Direction) Apply(amount Ink) (int64, error) {
	if !d.IsValid() {
		return 0, ErrInvalidDirection
	}
	if amount.IsZero() {
		return 0, ErrZeroAmount
	}
	if d == DirectionDebit {
		return -amount.Int64(), nil
	}
	return amount.Int64(), nil
}

// OperationType is the stable vocabulary of INK operations. The values are
// mirrored exactly by the CHECK constraint of app.wallet_operations
// (migrations 00008 and 00021), and each type fixes the direction of its
// transactions.
type OperationType string

const (
	OperationCreditFree     OperationType = "credit_free"
	OperationCreditMember   OperationType = "credit_member"
	OperationCreditPurchase OperationType = "credit_purchase"
	OperationCreditRefund   OperationType = "credit_refund"
	OperationCreditAdmin    OperationType = "credit_admin"
	OperationDebitArgument  OperationType = "debit_argument"
	OperationDebitAdmin     OperationType = "debit_admin"
	// OperationDebitRefund removes purchased INK after a verified refund or
	// chargeback (P12-T09). It is a non-administrative debit: the webhook is
	// the authority, the ledger entry is the compensating operation and a
	// shortfall never becomes a negative balance — it becomes a review flag.
	OperationDebitRefund OperationType = "debit_refund"
	// OperationDebitConversion extinguishes opted-in legacy credit inside a
	// Genesis conversion (P33-T04): the legacy debit and the Treasury credit
	// commit in one transaction, so the right converts exactly once and the
	// Genesis supply never grows.
	OperationDebitConversion OperationType = "debit_conversion"
	OperationExpireFree      OperationType = "expire_free"
)

// operationTypeDirection is the single source of truth for the vocabulary
// and the sign policy of every operation type.
var operationTypeDirection = map[OperationType]Direction{
	OperationCreditFree:      DirectionCredit,
	OperationCreditMember:    DirectionCredit,
	OperationCreditPurchase:  DirectionCredit,
	OperationCreditRefund:    DirectionCredit,
	OperationCreditAdmin:     DirectionCredit,
	OperationDebitArgument:   DirectionDebit,
	OperationDebitAdmin:      DirectionDebit,
	OperationDebitRefund:     DirectionDebit,
	OperationDebitConversion: DirectionDebit,
	// Expiring the unused franchise removes FREE_INK: a debit.
	OperationExpireFree: DirectionDebit,
}

// AllOperationTypes returns the vocabulary in its canonical order.
func AllOperationTypes() []OperationType {
	return []OperationType{
		OperationCreditFree,
		OperationCreditMember,
		OperationCreditPurchase,
		OperationCreditRefund,
		OperationCreditAdmin,
		OperationDebitArgument,
		OperationDebitAdmin,
		OperationDebitRefund,
		OperationDebitConversion,
		OperationExpireFree,
	}
}

// ParseOperationType validates a persisted or transport operation type
// against the exact ledger vocabulary.
func ParseOperationType(raw string) (OperationType, error) {
	operationType := OperationType(raw)
	if !operationType.IsValid() {
		return "", ErrInvalidOperationType
	}
	return operationType, nil
}

// IsValid reports whether the operation type is an authorized enum value.
func (t OperationType) IsValid() bool {
	_, ok := operationTypeDirection[t]
	return ok
}

// Direction returns the signed effect of the operation type.
func (t OperationType) Direction() (Direction, error) {
	direction, ok := operationTypeDirection[t]
	if !ok {
		return 0, ErrInvalidOperationType
	}
	return direction, nil
}

// IsCredit reports whether the operation type credits a bucket.
func (t OperationType) IsCredit() bool {
	direction, ok := operationTypeDirection[t]
	return ok && direction == DirectionCredit
}

// IsDebit reports whether the operation type debits a bucket.
func (t OperationType) IsDebit() bool {
	direction, ok := operationTypeDirection[t]
	return ok && direction == DirectionDebit
}

// IsAdmin reports whether the operation type is an administrative
// adjustment: those only enter the ledger through the restricted, audited
// adjustment path (P06-T07).
func (t OperationType) IsAdmin() bool {
	return t == OperationCreditAdmin || t == OperationDebitAdmin
}

// String returns the stored operation type value.
func (t OperationType) String() string {
	return string(t)
}
