package domain

import (
	"math"
	"sort"
)

// MedianR4 nets the R4 reference of one weekly epoch: the median of
// the net regular reflux of the up to four complete sealed weeks
// before it, floored when an even sample straddles two milliINK.
//
// The caller passes only sealed weekly nets in milliINK, oldest to
// newest or in any order: unsealed, current and future weeks never
// arrive here, and only sealed inputs decide. Zero sealed weeks is
// defined as zero — no history funds nothing, without failing.
// More than four nets refuse: R4 never looks past four weeks.
//
// Q31 stays PENDENTE (docs/reino/DECISOES_VIGENTES.md): this median
// is decision-free mechanics, not an official metric, and no
// ratified ratio, threshold or percentage lives here. The net may be
// negative — a run of refunds outrunning revenue stays visible for
// BoundWeeklyBudget to block, never clamped here. Central sums
// refuse overflow instead of wrapping, and float never appears.
func MedianR4(sealed []int64) (int64, error) {
	if len(sealed) > 4 {
		return 0, ErrInvalidBudget
	}
	if len(sealed) == 0 {
		return 0, nil
	}
	ordered := make([]int64, len(sealed))
	copy(ordered, sealed)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle], nil
	}
	low, high := ordered[middle-1], ordered[middle]
	sum, err := checkedSum2(low, high)
	if err != nil {
		return 0, err
	}
	return floorDiv2(sum), nil
}

// BoundWeeklyBudget caps one weekly crumb budget to what the free
// Treasury can actually cover: the minimum of a non-negative R4 and
// the explicit free Treasury in milliINK.
//
// Both inputs are exact milliINK. freeTreasury is the sovereign free
// stock already net of reserves, obligations and holds; it arrives
// per call and no partition or percentage is inferred here. A zero
// R4 or a zero Treasury is defined as a zero budget — nothing to
// distribute, without failing. A negative R4 blocks with
// ErrNegativeR4Blocked until a rule is ratified: the deficit stays
// visible and no negative amount ever leaves. A negative Treasury
// refuses with ErrInvalidBudget: the ledger never holds it.
func BoundWeeklyBudget(r4, freeTreasury int64) (int64, error) {
	if freeTreasury < 0 {
		return 0, ErrInvalidBudget
	}
	if r4 < 0 {
		return 0, ErrNegativeR4Blocked
	}
	if r4 == 0 || freeTreasury == 0 {
		return 0, nil
	}
	if r4 < freeTreasury {
		return r4, nil
	}
	return freeTreasury, nil
}

// checkedSum2 adds two ordered median neighbours without wrapping:
// mixed signs never overflow, like signs refuse past the int64 edge.
func checkedSum2(low, high int64) (int64, error) {
	if low >= 0 && high >= 0 && high > math.MaxInt64-low {
		return 0, ErrInvalidBudget
	}
	if low < 0 && high < 0 && low < math.MinInt64-high {
		return 0, ErrInvalidBudget
	}
	return low + high, nil
}

// floorDiv2 halves an even-sample sum rounding down: exact halves
// toward negative infinity, so a straddling refund week never rounds
// up at Treasury expense.
func floorDiv2(sum int64) int64 {
	if sum >= 0 || sum%2 == 0 {
		return sum / 2
	}
	return (sum - 1) / 2
}
