package domain

import (
	"math"
	"strings"
	"time"
)

// Refusal refund vocabulary (P33-T06).
//
// A refused charter never converts: the holder leaves with a contractual
// fiat reconciliation and a legacy-only revocation of the still-unused
// purchased benefit. Consumed benefit never becomes an implicit debt, a
// contested charge always waits for a human, and an innocent third party
// is never debited. Only PURCHASED_INK revokes automatically; FREE
// franchise, passes and Member benefits keep their contracted behavior
// and surface as review when nothing reversible remains.

// RefusalResponseDays bounds the human answer for a refusal exit: export,
// recourse and settlement stay alive until then and beyond by contract.
const RefusalResponseDays = 15

// maxRefundProviderBody mirrors the provider identifier shape the fiat
// reconciliation correlates: one of the two prefixes followed by one to
// 194 alphanumerics. The check is ASCII-only so pt and en holders mean
// the same object.
const maxRefundProviderBody = 194

// RefundSource names which fiat fact triggers the legacy compensation: a
// voluntary money-back or a contested chargeback.
type RefundSource string

const (
	// RefundSourceRefund is provider money returned to the holder.
	RefundSourceRefund RefundSource = "refund"
	// RefundSourceDispute is a contested charge that always waits for a human.
	RefundSourceDispute RefundSource = "dispute"
)

// ParseRefundSource validates the refund source vocabulary.
func ParseRefundSource(raw string) (RefundSource, error) {
	source := RefundSource(raw)
	if !source.IsValid() {
		return "", ErrInvalidRefund
	}
	return source, nil
}

// IsValid reports whether the source belongs to the closed vocabulary.
func (s RefundSource) IsValid() bool {
	switch s {
	case RefundSourceRefund, RefundSourceDispute:
		return true
	default:
		return false
	}
}

// String returns the stored source value.
func (s RefundSource) String() string { return string(s) }

// IsDispute reports whether the source is a contested chargeback.
func (s RefundSource) IsDispute() bool { return s == RefundSourceDispute }

// ParseRefundProviderID validates the fiat correlation identifier: re_ for
// refunds, dp_ for disputes, followed by one to 194 alphanumerics. The
// identifier is opaque here: it anchors idempotency and never carries PII.
func ParseRefundProviderID(raw string) (string, error) {
	rest, ok := strings.CutPrefix(raw, "re_")
	if !ok {
		rest, ok = strings.CutPrefix(raw, "dp_")
		if !ok {
			return "", ErrInvalidRefund
		}
	}
	if rest == "" || len(rest) > maxRefundProviderBody {
		return "", ErrInvalidRefund
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		default:
			return "", ErrInvalidRefund
		}
	}
	return raw, nil
}

// RefusalResponseDue answers when the human response for a refusal exit is
// due: fifteen days after the recorded verdict, in UTC.
func RefusalResponseDue(decidedAt time.Time) time.Time {
	return decidedAt.UTC().AddDate(0, 0, RefusalResponseDays)
}

// AssessRefusalRefund decides the legacy revocation for a refused holder.
//
//   - grantedUnits is the purchased legacy INK ever credited (whole units).
//   - remainingUnits is the purchased balance still held.
//   - paidMinor is the fiat price charged (minor units, >0).
//   - refundedMinor is the fiat money returned (minor units, 1..paid).
//   - source distinguishes a refund from a chargeback.
//
// The reversible quantity is the granted benefit prorated by the refunded
// share of the price with integer arithmetic only (never float): a full
// refund reverses the whole grant, a partial refund reverses the floored
// share (at least one unit when anything granted was refunded). The debit
// is capped at the remaining balance so good-faith consumption never
// produces a negative balance or an implicit debt: a shortfall, a dispute
// source or an empty debit becomes needsReview instead of an overdraft.
func AssessRefusalRefund(grantedUnits, remainingUnits, paidMinor, refundedMinor int64, source RefundSource) (revoke int64, needsReview bool, err error) {
	if grantedUnits < 0 || remainingUnits < 0 || remainingUnits > grantedUnits {
		return 0, false, ErrInvalidRefund
	}
	if grantedUnits == 0 {
		if remainingUnits != 0 {
			return 0, false, ErrInvalidRefund
		}
		if paidMinor < 1 || refundedMinor < 1 || refundedMinor > paidMinor {
			return 0, false, ErrInvalidRefund
		}
		if !source.IsValid() {
			return 0, false, ErrInvalidRefund
		}
		return 0, true, nil
	}
	if paidMinor < 1 || refundedMinor < 1 || refundedMinor > paidMinor {
		return 0, false, ErrInvalidRefund
	}
	if !source.IsValid() {
		return 0, false, ErrInvalidRefund
	}
	reversible, err := proratedUnits(grantedUnits, paidMinor, refundedMinor)
	if err != nil {
		return 0, false, err
	}
	revoke = reversible
	if revoke > remainingUnits {
		revoke = remainingUnits
	}
	if source.IsDispute() || revoke < reversible || revoke == 0 {
		return revoke, true, nil
	}
	return revoke, false, nil
}

// proratedUnits prices the granted benefit at the refunded fiat share with
// floor. Overflow fails instead of wrapping, and a dust-only share is
// refused upstream as nothing reversible rather than silently rounded.
func proratedUnits(granted, paid, refunded int64) (int64, error) {
	if refunded >= paid {
		return granted, nil
	}
	if granted > math.MaxInt64/refunded {
		return 0, ErrMilliInkOverflow
	}
	reversible := granted * refunded / paid
	if reversible < 1 {
		reversible = 1
	}
	return reversible, nil
}
