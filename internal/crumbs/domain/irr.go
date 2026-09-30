package domain

import (
	"fmt"
	"math"
	"math/bits"
)

// MetricName identifies the real reflux index in logs, disclosures
// and audit events: a circulation ratio, never a yield.
const MetricName = "real-reflux-index"

// OutflowKind names the closed vocabulary of gross Treasury outflows
// to citizens: the destination a Treasury leg paid. The four mirror
// the disbursement purposes of internal/economy/domain without
// importing them — domains stay disjoint by architecture — and the
// caller passes only citizen-bound legs: vault moves, holds and
// escrows have no kind here and never enter. Unknown kinds refuse
// fail-closed.
type OutflowKind string

const (
	// OutflowCrumbDistribution is one crumb grant paid to a newcomer.
	OutflowCrumbDistribution OutflowKind = "crumb_distribution"
	// OutflowSaleSettlement is one stock sale delivered to a buyer.
	OutflowSaleSettlement OutflowKind = "sale_settlement"
	// OutflowCompensation is one third-party restitution paid out.
	OutflowCompensation OutflowKind = "compensation"
	// OutflowDuePayment is one owed service payment released.
	OutflowDuePayment OutflowKind = "due_payment"
)

// AllOutflowKinds returns the closed vocabulary in canonical order.
func AllOutflowKinds() []OutflowKind {
	return []OutflowKind{
		OutflowCrumbDistribution, OutflowSaleSettlement,
		OutflowCompensation, OutflowDuePayment,
	}
}

// ParseOutflowKind validates a kind against the closed vocabulary.
// Matching is exact: no trimming, no case folding, no combined
// values.
func ParseOutflowKind(raw string) (OutflowKind, error) {
	kind := OutflowKind(raw)
	switch kind {
	case OutflowCrumbDistribution, OutflowSaleSettlement, OutflowCompensation, OutflowDuePayment:
		return kind, nil
	default:
		return "", ErrInvalidMetric
	}
}

// String returns the stored kind value.
func (k OutflowKind) String() string { return string(k) }

// OutflowLeg is one gross Treasury payment to a citizen: its closed
// kind and its exact amount in milliINK.
type OutflowLeg struct {
	Kind   OutflowKind
	Amount int64
}

// addChecked sums one positive amount without wrapping.
func addChecked(total, amount int64) (int64, error) {
	if amount <= 0 || total > math.MaxInt64-amount {
		return 0, ErrInvalidMetric
	}
	return total + amount, nil
}

// GrossReturns sums gross regular Treasury returns in milliINK: own
// service charges plus tithe credits, grouped by class. Reversals
// never net the numerator — the index counts gross returns — and
// excluded kinds never enter. Every leg needs a positive amount;
// overflows refuse instead of wrapping.
func GrossReturns(legs []RefluxLeg) (int64, map[RefluxClass]int64, error) {
	byClass := map[RefluxClass]int64{}
	var total int64
	for _, leg := range legs {
		class := Classify(leg)
		if class.Sign() <= 0 {
			continue
		}
		var err error
		total, err = addChecked(total, leg.Amount)
		if err != nil {
			return 0, nil, err
		}
		byClass[class], err = addChecked(byClass[class], leg.Amount)
		if err != nil {
			return 0, nil, err
		}
	}
	return total, byClass, nil
}

// SumOutflows sums gross Treasury payments to citizens in milliINK,
// grouped by kind. Every leg needs a known kind and a positive
// amount; overflows refuse instead of wrapping.
func SumOutflows(flows []OutflowLeg) (int64, map[OutflowKind]int64, error) {
	byKind := map[OutflowKind]int64{}
	var total int64
	for _, flow := range flows {
		if _, err := ParseOutflowKind(string(flow.Kind)); err != nil {
			return 0, nil, err
		}
		var err error
		total, err = addChecked(total, flow.Amount)
		if err != nil {
			return 0, nil, err
		}
		byKind[flow.Kind], err = addChecked(byKind[flow.Kind], flow.Amount)
		if err != nil {
			return 0, nil, err
		}
	}
	return total, byKind, nil
}

// permilleOf floors the index ratio to parts per thousand:
// `floor(returns*1000/outflows)` with the product checked. The same
// INK recirculating twice reads 2000 permille (200.0%): above 100%
// is honest circulation, never an error.
func permilleOf(returns, outflows int64) (int64, error) {
	if outflows <= 0 {
		return 0, ErrInvalidMetric
	}
	hi, lo := bits.Mul64(uint64(returns), 1000)
	if hi != 0 {
		return 0, ErrInvalidMetric
	}
	return int64(lo / uint64(outflows)), nil
}

// RefluxReport is one real reflux index: gross regular returns over
// gross Treasury outflows to citizens, both in milliINK with their
// per-category breakdowns. HasRatio is false exactly when outflows
// are zero — an empty period reports N/A instead of dividing. Q31
// stays PENDENTE (docs/reino/DECISOES_VIGENTES.md): this report is
// decision-free mechanics, not an official metric, and no ratified
// window, threshold or benchmark lives here. Float never appears.
type RefluxReport struct {
	Returns        int64
	ReturnsByKind  map[RefluxClass]int64
	Outflows       int64
	OutflowsByKind map[OutflowKind]int64
	HasRatio       bool
	PerMille       int64
}

// RealRefluxIndex computes one real reflux index from explicit legs:
// gross regular returns over gross citizen-bound outflows. Empty
// periods are defined: zero returns over zero outflows reports N/A.
func RealRefluxIndex(returns []RefluxLeg, outflows []OutflowLeg) (RefluxReport, error) {
	gross, byClass, err := GrossReturns(returns)
	if err != nil {
		return RefluxReport{}, err
	}
	paid, byKind, err := SumOutflows(outflows)
	if err != nil {
		return RefluxReport{}, err
	}
	if paid == 0 {
		return RefluxReport{
			Returns: gross, ReturnsByKind: byClass,
			OutflowsByKind: byKind, HasRatio: false,
		}, nil
	}
	permille, err := permilleOf(gross, paid)
	if err != nil {
		return RefluxReport{}, err
	}
	return RefluxReport{
		Returns: gross, ReturnsByKind: byClass,
		Outflows: paid, OutflowsByKind: byKind,
		HasRatio: true, PerMille: permille,
	}, nil
}

// RatioLabel renders the index for disclosure: "N/A" with no
// outflows, otherwise one decimal percent kept in integers, above
// 100% when circulation repeats.
func (r RefluxReport) RatioLabel() string {
	if !r.HasRatio {
		return "N/A"
	}
	return fmt.Sprintf("%d.%d%%", r.PerMille/10, r.PerMille%10)
}

// Disclaimer names what the number is and refuses what it is not: a
// gross circulation ratio over disclosed categories, not a yield,
// interest or promise of return.
func Disclaimer() string {
	return MetricName + ": gross regular Treasury returns over gross Treasury payments to citizens in disclosed categories; a circulation ratio, not a yield, interest or promise of return"
}
