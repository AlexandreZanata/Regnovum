package domain

import "sort"

// Share is one fixed-stock crumb quota: the opaque account and its
// exact milliINK amount. Every share of one plan carries the same
// quota: `min(cap, floor(M/N))`, never rounded up.
type Share struct {
	Account string
	Amount  int64
}

// Plan is one weekly distribution snapshot: the budget M it splits,
// the per-person cap it honours, the epoch it pays, the single quota
// every beneficiary receives, the ordered shares and the exact
// accounting `distributed + remainder == M`. Ordering is by account
// ascending, so a retry over the same snapshot replays the same
// sequence instead of paying twice.
type Plan struct {
	Budget      int64
	Cap         int64
	EpochKey    string
	Quota       int64
	Shares      []Share
	Distributed int64
	Remainder   int64
}

// collectEligible captures theSnapshot of one epoch: admitted grants
// of that epoch only. Blocked, appealed, cancelled and erased grants
// never count, other epochs never count, and a corrupt seal or a
// doubled account refuses the whole snapshot fail-closed.
func collectEligible(epochKey string, grants []Grant) ([]string, error) {
	accounts := make([]string, 0, len(grants))
	seen := make(map[string]bool, len(grants))
	for _, g := range grants {
		if g.Status != NewcomerAdmitted || g.EpochKey != epochKey {
			continue
		}
		if err := g.VerifyHash(); err != nil {
			return nil, ErrInvalidDistribution
		}
		if _, err := parseNewcomerToken(g.Account); err != nil {
			return nil, ErrInvalidDistribution
		}
		if seen[g.Account] {
			return nil, ErrInvalidDistribution
		}
		seen[g.Account] = true
		accounts = append(accounts, g.Account)
	}
	sort.Strings(accounts)
	return accounts, nil
}

// quotaOf floors the equal split and honours the per-person cap:
// `min(cap, floor(M/N))` in milliINK. Sub-milliINK floors settle to
// zero: dust is never paid, it stays in the remainder.
func quotaOf(budget, cap, n int64) int64 {
	floor := budget / n
	if floor < cap {
		return floor
	}
	return cap
}

// PlanDistribution splits one weekly budget M over the eligible set
// of one epoch in fixed stock: every beneficiary receives
// `min(cap, floor(M/N))` milliINK from the commitment, the remainder
// `M - quota*N` stays with the Treasury, and `distributed +
// remainder == M` always holds.
//
// The budget M and the per-person cap arrive per call: Q25 stays
// PENDENTE (docs/reino/DECISOES_VIGENTES.md), so no ratified cap,
// ratio or percentage lives here. `N=0` is defined as an empty plan
// with the whole M left over: zero beneficiaries move zero. A zero
// floor (budget smaller than the headcount) pays zero to everyone
// with the whole M left over: fractions below one milliINK are never
// paid and never rounded up. The plan moves no value itself: later
// tasks execute it exactly once, and this function only names the
// shares in deterministic account order so a crash after k
// beneficiaries resumes at k+1 with the same quota.
func PlanDistribution(budgetMilli, capMilli int64, epochKey string, grants []Grant) (Plan, error) {
	if budgetMilli < 0 || capMilli <= 0 {
		return Plan{}, ErrInvalidDistribution
	}
	if _, err := ParseEpochKey(epochKey); err != nil {
		return Plan{}, ErrInvalidDistribution
	}
	accounts, err := collectEligible(epochKey, grants)
	if err != nil {
		return Plan{}, err
	}
	if len(accounts) == 0 {
		return Plan{Budget: budgetMilli, Cap: capMilli, EpochKey: epochKey, Remainder: budgetMilli}, nil
	}
	headcount := int64(len(accounts))
	quota := quotaOf(budgetMilli, capMilli, headcount)
	if quota < 0 || (headcount != 0 && quota > budgetMilli/headcount) {
		return Plan{}, ErrInvalidDistribution
	}
	distributed := quota * headcount
	if distributed < 0 || distributed > budgetMilli {
		return Plan{}, ErrInvalidDistribution
	}
	shares := make([]Share, 0, len(accounts))
	for _, account := range accounts {
		shares = append(shares, Share{Account: account, Amount: quota})
	}
	return Plan{
		Budget: budgetMilli, Cap: capMilli, EpochKey: epochKey,
		Quota: quota, Shares: shares,
		Distributed: distributed, Remainder: budgetMilli - distributed,
	}, nil
}
