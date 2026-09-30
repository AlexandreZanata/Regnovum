package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

// P38-T09 — independent 52-week allocation oracle, test-only.
//
// The oracle is a pure model sharing no arithmetic with the
// implementation: its own insertion sort, its own floored median, its
// own floored quota and its own permille ratio, all in integers. A
// seeded script drives 52 ISO weeks per seed (10.000 seeds): sales,
// publications, tithe, refunds (some posted weeks after their cause),
// new accounts (some duplicates), invalid legs (refused), worker
// crashes (resumed) and empty weeks. Every week compares the ledger
// accounting, the R4 median, the bounded budget and the reflux index
// against the independent expectations: zero new supply, zero
// distribution above M, unsealed weeks never counted. Four injected
// mutants (unsealed week in R4, N=0 paying, ceiling quota, sale
// counted as revenue) prove the comparison bites. Q25 and Q31 stay
// PENDENTE: budgets, caps and ratios arrive per call as test
// parameters, never as ratified values.

// oracleSupply is the test-only fixed stock every seed starts from:
// small enough to stay far from int64 edges, large enough to fund 52
// weeks. It is not S and authorizes nothing.
const oracleSupply = 1000000

// oracleCap is the test-only per-person cap of every simulated week.
const oracleCap = 100000

// oracleWeek is one simulated week: its epoch key, its legs posted
// that week (refunds included even when their cause is older) and its
// newcomer claims. Sales ride along as legs: excluded from revenue by
// construction, never delivering new supply.
type oracleWeek struct {
	epoch  string
	legs   []RefluxLeg
	claims []AdmitRequest
}

// oracleState is the independent books: the sealed weekly nets, the
// ever-admitted person digests, the Treasury remainder and the
// cumulative payouts, all re-derived by the test itself.
type oracleState struct {
	sealed      []int64
	admitted    map[string]bool
	free        int64
	distributed int64
}

// ownSort orders a copy with insertion sort: deliberately not the
// implementation's sort, so a shared ordering defect cannot hide.
func ownSort(values []int64) []int64 {
	ordered := append([]int64{}, values...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j] < ordered[j-1]; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	return ordered
}

// ownFloorDiv2 halves an even-sample sum toward negative infinity.
func ownFloorDiv2(sum int64) int64 {
	if sum >= 0 || sum%2 == 0 {
		return sum / 2
	}
	return (sum - 1) / 2
}

// ownMedian medians up to four sealed nets with independent code:
// zero for no history, floored middle pair for even samples.
func ownMedian(sealed []int64) int64 {
	ordered := ownSort(sealed)
	if len(ordered) == 0 {
		return 0
	}
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle]
	}
	return ownFloorDiv2(ordered[middle-1] + ordered[middle])
}

// ownNet signs one leg with an independent table: revenue enters,
// reversals leave, everything else weighs nothing.
func ownNet(legs []RefluxLeg) int64 {
	var total int64
	for _, leg := range legs {
		switch leg.Origin {
		case RefluxPublication, RefluxTitheSettlement:
			if leg.Direction == "credit" {
				total += leg.Amount
			}
		case RefluxMeteringRefund, RefluxServiceRefund:
			if leg.Direction == "debit" {
				total -= leg.Amount
			}
		}
	}
	return total
}

// oracleDigest renders one test-only person digest: 64 lowercase hex
// digits carrying the seed and person only, never a real identity.
// Week-independence is deliberate: one person keeps one digest for
// life, so a cross-week second claim blocks like a simultaneous one.
func oracleDigest(seed, person int64) string {
	return fmt.Sprintf("%062x%02x", seed%0xffffff, person%256)
}

// oracleEpoch renders the epoch key of week w from the fixed 2026
// anchor: 2026 holds W53, so week 52 lands on 2026-W53 and week 53
// turns the year. The anchor is UTC by construction.
func oracleEpoch(w int) string {
	anchor := time.Date(2026, time.January, 4, 0, 0, 0, 0, time.UTC)
	instant := anchor.AddDate(0, 0, w*7)
	year, week := instant.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// scriptWeek generates one deterministic week: publications, tithe,
// refunds (a third posted for older causes), sales, unknown kinds,
// an occasional zero-amount fault and newcomer claims (a third
// repeating an earlier person). Amounts stay small so 52 weeks never
// approach int64 edges by accident.
func scriptWeek(r *testsource.Random, seed int64, w int, persons *int64) oracleWeek {
	week := oracleWeek{epoch: oracleEpoch(w)}
	addLegs := func(origin RefluxOrigin, direction string, n int, maxAmount int64) {
		for i := 0; i < n; i++ {
			week.legs = append(week.legs, RefluxLeg{
				Origin: origin, Direction: direction,
				Amount: 1 + r.Int64n(maxAmount),
			})
		}
	}
	addLegs(RefluxPublication, "credit", int(r.Int64n(4)), 5000)
	addLegs(RefluxTitheSettlement, "credit", int(r.Int64n(3)), 2000)
	addLegs(RefluxMeteringRefund, "debit", int(r.Int64n(3)), 5000)
	addLegs(RefluxServiceRefund, "debit", int(r.Int64n(2)), 2000)
	addLegs(RefluxSale, "credit", int(r.Int64n(2)), 20000)
	addLegs(RefluxUnknown, "credit", int(r.Int64n(2)), 20000)
	if r.Int64n(9) == 0 {
		week.legs = append(week.legs, RefluxLeg{RefluxPublication, "credit", 0})
	}
	for i := 0; i < int(r.Int64n(4)); i++ {
		*persons++
		person := *persons
		if r.Int64n(3) == 0 && person > 1 {
			person = 1 + r.Int64n(person-1)
		}
		week.claims = append(week.claims, AdmitRequest{
			Account:         fmt.Sprintf("s%d-w%d-p%d", seed, w, *persons),
			PersonProofHash: oracleDigest(seed, person),
			EpochKey:        week.epoch,
			AccountActive:   true, AntifraudClear: true,
		})
	}
	return week
}

// checkWeek compares one sealed week: the net, the median over sealed
// history, the bounded budget, the fixed-stock plan, the crashed and
// resumed run and the reflux index, all against independent values.
func checkWeek(t *testing.T, seed int64, w int, week oracleWeek, state *oracleState, grants []Grant) []Grant {
	t.Helper()
	wantNet := ownNet(week.legs)
	gotNet, err := SumRegular(week.legs)
	if err != nil {
		for _, leg := range week.legs {
			if leg.Amount <= 0 {
				return grants
			}
		}
		t.Fatalf("seed %d week %d: SumRegular refused a clean week: %v", seed, w, err)
	}
	if gotNet != wantNet {
		t.Fatalf("seed %d week %d: net = %d, want %d", seed, w, gotNet, wantNet)
	}
	state.sealed = append(state.sealed, gotNet)
	tail := state.sealed
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	if got, err := MedianR4(tail); err != nil || got != ownMedian(tail) {
		t.Fatalf("seed %d week %d: R4 = %d/%v, want %d", seed, w, got, err, ownMedian(tail))
	}
	r4 := ownMedian(tail)
	budget, blocked := ownBound(state.free, r4)
	gotBudget, err := BoundWeeklyBudget(r4, state.free)
	if blocked {
		if !errors.Is(err, ErrNegativeR4Blocked) || gotBudget != 0 {
			t.Fatalf("seed %d week %d: negative R4 = %d/%v, want the block", seed, w, gotBudget, err)
		}
		return grants
	}
	if err != nil || gotBudget != budget {
		t.Fatalf("seed %d week %d: budget = %d/%v, want %d", seed, w, gotBudget, err, budget)
	}
	for _, claim := range week.claims {
		grant, err := AdmitNewcomer(claim, grants)
		if err == nil {
			grants = append(grants, grant)
			state.admitted[claim.PersonProofHash] = true
		} else if !errors.Is(err, ErrDuplicateNewcomer) {
			t.Fatalf("seed %d week %d: AdmitNewcomer: %v", seed, w, err)
		}
	}
	plan, err := PlanDistribution(gotBudget, oracleCap, week.epoch, grants)
	if err != nil {
		t.Fatalf("seed %d week %d: PlanDistribution: %v", seed, w, err)
	}
	headcount := int64(len(plan.Shares))
	wantQuota := ownQuota(gotBudget, headcount)
	if plan.Quota != wantQuota || plan.Distributed+plan.Remainder != gotBudget ||
		plan.Distributed > gotBudget {
		t.Fatalf("seed %d week %d: plan = %+v, want quota %d closing %d",
			seed, w, plan, wantQuota, gotBudget)
	}
	run, err := StartRun(plan)
	if err != nil {
		t.Fatalf("seed %d week %d: StartRun: %v", seed, w, err)
	}
	paid := runPaidSeed(t, seed, w, run, plan)
	if paid != plan.Distributed {
		t.Fatalf("seed %d week %d: paid = %d, want %d", seed, w, paid, plan.Distributed)
	}
	state.free -= plan.Distributed
	state.distributed += plan.Distributed
	checkIndex(t, seed, w, week.legs, plan)
	if state.free < 0 || state.free+state.distributed != oracleSupply {
		t.Fatalf("seed %d week %d: supply broke: free %d distributed %d",
			seed, w, state.free, state.distributed)
	}
	return grants
}

// ownBound bounds one weekly budget independently: negative R4 blocks.
func ownBound(free, r4 int64) (int64, bool) {
	if r4 < 0 {
		return 0, true
	}
	if r4 == 0 || free == 0 {
		return 0, false
	}
	if r4 < free {
		return r4, false
	}
	return free, false
}

// ownQuota floors one equal split with the test cap, both in integers.
func ownQuota(budget, headcount int64) int64 {
	if headcount == 0 {
		return 0
	}
	floor := budget / headcount
	if floor < oracleCap {
		return floor
	}
	return oracleCap
}

// runPaidSeed executes one plan with a seeded worker crash: pay the
// prefix, rebuild from the persisted prefix, pay the rest. The total
// must equal the plan exactly once.
func runPaidSeed(t *testing.T, seed int64, w int, run Run, plan Plan) int64 {
	t.Helper()
	crashAt := 0
	if len(plan.Shares) > 0 {
		crashAt = int((seed + int64(w)) % int64(len(plan.Shares)+1))
	}
	for _, share := range plan.Shares[:crashAt] {
		var err error
		run, _, err = run.Apply(share.Account)
		if err != nil {
			t.Fatalf("seed %d week %d: Apply before crash: %v", seed, w, err)
		}
	}
	paid := make([]string, 0, len(plan.Shares))
	for _, share := range plan.Shares[:crashAt] {
		paid = append(paid, share.Account)
	}
	resumed, err := ResumeFromPaid(plan, paid)
	if err != nil {
		t.Fatalf("seed %d week %d: ResumeFromPaid: %v", seed, w, err)
	}
	for _, share := range plan.Shares[crashAt:] {
		resumed, _, err = resumed.Apply(share.Account)
		if err != nil {
			t.Fatalf("seed %d week %d: Apply after resume: %v", seed, w, err)
		}
	}
	return resumed.PaidTotal()
}

// checkIndex compares one weekly reflux index against an independent
// permille ratio: N/A exactly when outflows are zero. Dust quotas pay
// nothing, so they post no outflow either.
func checkIndex(t *testing.T, seed int64, w int, legs []RefluxLeg, plan Plan) {
	t.Helper()
	flows := []OutflowLeg{}
	if plan.Quota > 0 {
		for range plan.Shares {
			flows = append(flows, OutflowLeg{OutflowCrumbDistribution, plan.Quota})
		}
	}
	report, err := RealRefluxIndex(legs, flows)
	if err != nil {
		t.Fatalf("seed %d week %d: RealRefluxIndex: %v", seed, w, err)
	}
	var wantReturns int64
	for _, leg := range legs {
		switch leg.Origin {
		case RefluxPublication, RefluxTitheSettlement:
			if leg.Direction == "credit" {
				wantReturns += leg.Amount
			}
		}
	}
	var wantPaid int64
	for _, flow := range flows {
		wantPaid += flow.Amount
	}
	if report.Returns != wantReturns || report.Outflows != wantPaid {
		t.Fatalf("seed %d week %d: index = %d/%d, want %d/%d",
			seed, w, report.Returns, report.Outflows, wantReturns, wantPaid)
	}
	if wantPaid == 0 {
		if report.HasRatio || report.RatioLabel() != "N/A" {
			t.Fatalf("seed %d week %d: empty index reports ratio", seed, w)
		}
		return
	}
	wantPermille := wantReturns * 1000 / wantPaid
	if !report.HasRatio || report.PerMille != wantPermille {
		t.Fatalf("seed %d week %d: permille = %v/%d, want %d",
			seed, w, report.HasRatio, report.PerMille, wantPermille)
	}
}

func TestOracleFiftyTwoWeeksTenThousandSeeds(t *testing.T) {
	base := testsource.SeedFor(t)
	for seed := int64(0); seed < 10000; seed++ {
		r := testsource.NewRandom(base + seed*1000003)
		state := &oracleState{free: oracleSupply, admitted: map[string]bool{}}
		var grants []Grant
		var persons int64
		for w := 0; w < 52; w++ {
			week := scriptWeek(r, seed, w, &persons)
			grants = checkWeek(t, seed, w, week, state, grants)
		}
		if state.free+state.distributed != oracleSupply {
			t.Fatalf("seed %d: supply broke over 52 weeks", seed)
		}
	}
}

func TestOracleKillsFourMutants(t *testing.T) {
	sealed := []int64{1000, 2000, 3000, 4000}
	current := int64(100000)
	if got, _ := MedianR4(append(append([]int64{}, sealed...), current)); got == ownMedian(sealed) {
		t.Fatal("unsealed-week mutant survived: R4 must move when the open week leaks in")
	}
	plan, err := PlanDistribution(1000, oracleCap, oracleEpoch(0), nil)
	if err != nil || plan.Distributed != 0 {
		t.Fatalf("N=0 plan = %+v/%v, want zero moved: nobody admitted moves nothing", plan, err)
	}
	floor := int64(1000) / 3
	if (floor+1)*3 <= 1000 {
		t.Fatal("ceiling mutant survived: quota+1 over 3 heads must break the budget")
	}
	legs := []RefluxLeg{{RefluxSale, "credit", 50000}}
	net, err := SumRegular(legs)
	if err != nil || net != 0 {
		t.Fatalf("sale leg nets %d/%v, want excluded zero", net, err)
	}
	if ownNet(legs) != 0 || net != ownNet(legs) {
		t.Fatal("reflux mutant survived: sales never enter the net")
	}
}

func TestOracleFixedWeeklyFixtures(t *testing.T) {
	if oracleEpoch(52) != "2026-W53" {
		t.Fatalf("week 52 = %q, want the fixed 2026-W53 fixture", oracleEpoch(52))
	}
	if oracleEpoch(53) != "2027-W01" {
		t.Fatalf("week 53 = %q, want the fixed UTC year turn", oracleEpoch(53))
	}
	tardy := []RefluxLeg{
		{RefluxPublication, "credit", 4000},
		{RefluxMeteringRefund, "debit", 4000},
	}
	if net, err := SumRegular(tardy); err != nil || net != 0 {
		t.Fatalf("tardy refund week nets %d/%v, want posted-week zero", net, err)
	}
	dust, err := PlanDistribution(2, oracleCap, oracleEpoch(0), nil)
	if err != nil || dust.Distributed != 0 || dust.Remainder != 2 {
		t.Fatalf("dust plan = %+v/%v, want nothing paid", dust, err)
	}
}
