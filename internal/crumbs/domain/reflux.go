package domain

import "math"

// RefluxOrigin names the closed vocabulary of journal sources: the
// table whose transfer moved value. Anything outside this vocabulary
// is unknown, and unknown never counts as regular reflux.
type RefluxOrigin string

const (
	// RefluxPublication is a settled service charge: the platform's
	// own service revenue.
	RefluxPublication RefluxOrigin = "publication"
	// RefluxTitheSettlement is a liquidated formal payment: only its
	// Treasury tithe counts.
	RefluxTitheSettlement RefluxOrigin = "tithe_settlement"
	// RefluxMeteringRefund is a publication compensation: the whole
	// reversed charge leaves the Treasury.
	RefluxMeteringRefund RefluxOrigin = "metering_refund"
	// RefluxServiceRefund is a service compensation: only its tithe
	// reversal leaves the Treasury.
	RefluxServiceRefund RefluxOrigin = "service_refund"
	// RefluxSale is a fiat-funded purchase: new INK from stock, never
	// regular revenue.
	RefluxSale RefluxOrigin = "sale"
	// RefluxConfiscation is a compelled seizure: extraordinary, never
	// regular.
	RefluxConfiscation RefluxOrigin = "confiscation"
	// RefluxDeathSettlement is a death liquidation: extraordinary,
	// never regular.
	RefluxDeathSettlement RefluxOrigin = "death_settlement"
	// RefluxCorrection is a book correction: it repairs, never earns.
	RefluxCorrection RefluxOrigin = "correction"
	// RefluxTreasuryMove reallocates sovereign stock between vaults:
	// no revenue crosses a vault boundary.
	RefluxTreasuryMove RefluxOrigin = "treasury_move"
	// RefluxGift is a personal present: no consideration, no revenue.
	RefluxGift RefluxOrigin = "gift"
	// RefluxEscrow is a locked or returned escrow hold: still the
	// buyer's funds, never Treasury revenue.
	RefluxEscrow RefluxOrigin = "escrow"
	// RefluxUnknown is anything the classifier cannot name: excluded
	// by construction, never inferred into revenue.
	RefluxUnknown RefluxOrigin = "unknown"
)

// AllRefluxOrigins returns the closed vocabulary in canonical order.
func AllRefluxOrigins() []RefluxOrigin {
	return []RefluxOrigin{
		RefluxPublication, RefluxTitheSettlement, RefluxMeteringRefund, RefluxServiceRefund,
		RefluxSale, RefluxConfiscation, RefluxDeathSettlement, RefluxCorrection,
		RefluxTreasuryMove, RefluxGift, RefluxEscrow, RefluxUnknown,
	}
}

// String returns the stored origin value.
func (o RefluxOrigin) String() string { return string(o) }

// RefluxClass is one classified Treasury leg: regular revenue, a
// regular reversal, or excluded. Only the four regular classes enter
// the net; every other kind — sales, seizures, death, corrections,
// vault moves, gifts, holds and the unknown — sums to nothing.
type RefluxClass string

const (
	// RefluxServiceCharge is a settled service charge entering the
	// Treasury: +amount.
	RefluxServiceCharge RefluxClass = "service_charge"
	// RefluxTithe is a liquidated tithe entering the Treasury:
	// +amount.
	RefluxTithe RefluxClass = "tithe"
	// RefluxMeteringReversal is a publication compensation leaving
	// the Treasury: -amount.
	RefluxMeteringReversal RefluxClass = "metering_reversal"
	// RefluxTitheReversal is a service tithe reversal leaving the
	// Treasury: -amount.
	RefluxTitheReversal RefluxClass = "tithe_reversal"
	// RefluxExcluded never enters the net, whatever its amount.
	RefluxExcluded RefluxClass = "excluded"
)

// RefluxLeg is one Treasury leg ready to classify: its source, its
// direction and its exact amount in milliINK.
type RefluxLeg struct {
	Origin    RefluxOrigin
	Direction string
	Amount    int64
}

// Classify names the class of one Treasury leg. Regular revenue
// needs the exact source, the Treasury custody and the matching
// direction: a service or tithe credit enters, a refund debit
// leaves. Anything else — wrong source, wrong direction or unknown
// origin — is excluded, fail-closed.
func Classify(leg RefluxLeg) RefluxClass {
	switch leg.Origin {
	case RefluxPublication:
		if leg.Direction == "credit" {
			return RefluxServiceCharge
		}
	case RefluxTitheSettlement:
		if leg.Direction == "credit" {
			return RefluxTithe
		}
	case RefluxMeteringRefund:
		if leg.Direction == "debit" {
			return RefluxMeteringReversal
		}
	case RefluxServiceRefund:
		if leg.Direction == "debit" {
			return RefluxTitheReversal
		}
	}
	return RefluxExcluded
}

// Sign renders the class contribution: revenue enters, reversals
// leave, excluded kinds weigh nothing.
func (c RefluxClass) Sign() int {
	switch c {
	case RefluxServiceCharge, RefluxTithe:
		return 1
	case RefluxMeteringReversal, RefluxTitheReversal:
		return -1
	default:
		return 0
	}
}

// Regular reports whether the class enters the net.
func (c RefluxClass) Regular() bool {
	return c.Sign() != 0
}

// SumRegular nets classified legs with overflow checked. The net may
// be negative — a week of refunds outrunning revenue owes nothing
// and hides nothing: later tasks decide what a negative R4 input
// means, never this sum. Overflows refuse instead of wrapping.
func SumRegular(legs []RefluxLeg) (int64, error) {
	var total int64
	for _, leg := range legs {
		class := Classify(leg)
		if !class.Regular() {
			continue
		}
		if leg.Amount <= 0 {
			return 0, ErrInvalidReflux
		}
		signed := int64(class.Sign()) * leg.Amount
		if class.Sign() > 0 && total > math.MaxInt64-leg.Amount {
			return 0, ErrInvalidReflux
		}
		if class.Sign() < 0 && total < math.MinInt64+leg.Amount {
			return 0, ErrInvalidReflux
		}
		total += signed
	}
	return total, nil
}
