package domain

import (
	"errors"
	"testing"
)

func mustStartRun(t *testing.T, plan Plan) Run {
	t.Helper()
	run, err := StartRun(plan)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := run.VerifyHash(); err != nil {
		t.Fatalf("run VerifyHash: %v", err)
	}
	return run
}

func mustRunPlan(t *testing.T, epoch string, budget, cap int64, n int) (Plan, []Grant) {
	t.Helper()
	grants := admitDistributionSet(t, epoch, n)
	plan, err := PlanDistribution(budget, cap, epoch, grants)
	if err != nil {
		t.Fatalf("PlanDistribution: %v", err)
	}
	return plan, grants
}

func TestTwoWorkersShareOneRunWithoutDouble(t *testing.T) {
	plan, _ := mustRunPlan(t, eligibilityEpoch, 1000, 5000, 5)
	run := mustStartRun(t, plan)
	paidBy := map[string]string{}
	for i := 0; i < len(plan.Shares); i++ {
		worker := "worker-a"
		if i%2 == 1 {
			worker = "worker-b"
		}
		next, ok := run.NextUnpaid()
		if !ok {
			t.Fatalf("NextUnpaid drained at %d/5", i)
		}
		var counted bool
		var err error
		run, counted, err = run.Apply(next)
		if err != nil || !counted {
			t.Fatalf("Apply(%q): %+v/%v, want first payment", next, run, err)
		}
		paidBy[next] = worker
	}
	if run.PaidCount() != 5 || run.PaidTotal() != plan.Distributed {
		t.Fatalf("paid=%d total=%d, want 5/%d", run.PaidCount(), run.PaidTotal(), plan.Distributed)
	}
	if _, ok := run.NextUnpaid(); ok {
		t.Fatal("NextUnpaid after all paid: want drained")
	}
	for account := range paidBy {
		unchanged, counted, err := run.Apply(account)
		if err != nil || counted {
			t.Fatalf("replay Apply(%q): counted=%v/%v, want idempotent duplicate", account, counted, err)
		}
		if unchanged.PaidTotal() != run.PaidTotal() {
			t.Fatal("retry moved value: want no double payment")
		}
	}
	seen := map[string]bool{}
	for account := range paidBy {
		if seen[account] {
			t.Fatalf("account %q paid twice", account)
		}
		seen[account] = true
	}
}

func TestCrashAfterEachBeneficiaryResumes(t *testing.T) {
	plan, _ := mustRunPlan(t, eligibilityEpoch, 1000, 5000, 4)
	var order []string
	for _, share := range plan.Shares {
		order = append(order, share.Account)
	}
	for crashAt := 0; crashAt <= len(order); crashAt++ {
		fresh := mustStartRun(t, plan)
		for _, account := range order[:crashAt] {
			var counted bool
			var err error
			fresh, counted, err = fresh.Apply(account)
			if err != nil || !counted {
				t.Fatalf("crash %d: Apply(%q) = %v/%v", crashAt, account, counted, err)
			}
		}
		resumed, err := ResumeFromPaid(plan, order[:crashAt])
		if err != nil {
			t.Fatalf("crash %d: ResumeFromPaid: %v", crashAt, err)
		}
		if resumed.PaidTotal() != fresh.PaidTotal() {
			t.Fatalf("crash %d: resumed total %d, want %d", crashAt, resumed.PaidTotal(), fresh.PaidTotal())
		}
		for _, account := range order[crashAt:] {
			var err error
			resumed, _, err = resumed.Apply(account)
			if err != nil {
				t.Fatalf("crash %d: resume Apply(%q): %v", crashAt, account, err)
			}
		}
		if resumed.PaidTotal() != plan.Distributed {
			t.Fatalf("crash %d: total %d, want %d", crashAt, resumed.PaidTotal(), plan.Distributed)
		}
	}
}

func TestLateAccountChangeNeverEntersOpenRun(t *testing.T) {
	plan, grants := mustRunPlan(t, eligibilityEpoch, 1000, 5000, 3)
	run := mustStartRun(t, plan)
	extended, newcomer := mustDistributionAdmit(t, "tardia", 90, eligibilityEpoch, grants)
	_ = newcomer
	if _, _, err := run.Apply("tardia"); !errors.Is(err, ErrRunState) {
		t.Fatalf("late account Apply = %v, want ErrRunState: sealed order never grows", err)
	}
	next, ok := run.NextUnpaid()
	if !ok || next == "tardia" {
		t.Fatalf("NextUnpaid = %q/%v, want a sealed account", next, ok)
	}
	replanned, err := PlanDistribution(1000, 5000, eligibilityEpoch, extended)
	if err != nil {
		t.Fatalf("replan: %v", err)
	}
	if len(replanned.Shares) != 4 {
		t.Fatalf("replan shares = %d, want 4 in a new plan, never inside the open run", len(replanned.Shares))
	}
	if len(run.Order) != 3 {
		t.Fatalf("open run order = %d, want still 3", len(run.Order))
	}
}

func TestAppealNeverDuplicatesPayment(t *testing.T) {
	plan, grants := mustRunPlan(t, eligibilityEpoch, 1000, 5000, 2)
	run := mustStartRun(t, plan)
	blocked, err := AdmitNewcomer(admitReq("contestante", distributionPerson(0)), grants)
	if !errors.Is(err, ErrDuplicateNewcomer) {
		t.Fatalf("duplicate = %v, want ErrDuplicateNewcomer", err)
	}
	appealed, err := blocked.Appeal("contestante")
	if err != nil {
		t.Fatalf("Appeal: %v", err)
	}
	if _, _, err := run.Apply(appealed.Account); err == nil {
		t.Fatal("appealed account Apply = nil: appeals never join the sealed order")
	}
	replanned, err := PlanDistribution(1000, 5000, eligibilityEpoch, grants)
	if err != nil || len(replanned.Shares) != 2 {
		t.Fatalf("replan = %+v/%v, want still 2 admitted", replanned, err)
	}
	first := plan.Shares[0].Account
	var counted bool
	run, counted, err = run.Apply(first)
	if err != nil || !counted {
		t.Fatalf("Apply(%q): %v/%v", first, counted, err)
	}
	retry, counted, err := run.Apply(first)
	if err != nil || counted || retry.PaidTotal() != run.PaidTotal() {
		t.Fatalf("replay = %v/%v, want no double payment", counted, err)
	}
}

func TestClosedEpochNeverReopensCorrectionNeedsCurrentBudget(t *testing.T) {
	plan, grants := mustRunPlan(t, eligibilityEpoch, 1000, 5000, 2)
	run := mustStartRun(t, plan)
	for _, share := range plan.Shares {
		var err error
		run, _, err = run.Apply(share.Account)
		if err != nil {
			t.Fatalf("Apply(%q): %v", share.Account, err)
		}
	}
	closed, err := run.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := closed.Apply(plan.Shares[0].Account); !errors.Is(err, ErrRunState) {
		t.Fatalf("pay after close = %v, want ErrRunState", err)
	}
	if _, err := closed.Close(); !errors.Is(err, ErrRunState) {
		t.Fatalf("second close = %v, want ErrRunState", err)
	}
	corrected, err := PlanDistribution(2000, 5000, eligibilityEpoch, grants)
	if err != nil {
		t.Fatalf("correction PlanDistribution: %v", err)
	}
	if corrected.Budget != 2000 || corrected.Distributed == closed.PaidTotal() && corrected.Budget == closed.Budget {
		t.Fatal("correction reused the sealed budget: want an explicit current budget")
	}
	fresh, err := StartRun(corrected)
	if err != nil {
		t.Fatalf("correction StartRun: %v", err)
	}
	if fresh.Status != RunOpen || closed.Status != RunClosed || closed.PaidTotal() != plan.Distributed {
		t.Fatalf("correction touched the closed run: want it untouched")
	}
}

func TestRunSealFreezesEpochTerms(t *testing.T) {
	plan, _ := mustRunPlan(t, eligibilityEpoch, 1000, 5000, 2)
	run := mustStartRun(t, plan)
	plan.Shares[0].Account = "mutante"
	if run.Order[0] == "mutante" {
		t.Fatal("run aliases the plan: want a sealed copy")
	}
	broken := run
	broken.Quota = 1
	if err := broken.VerifyHash(); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("tampered run Verify = %v, want ErrInvalidRun", err)
	}
	if _, _, err := broken.Apply(run.Order[0]); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("tampered Apply = %v, want ErrInvalidRun", err)
	}
	if _, _, err := run.Apply("conta-fantasma"); !errors.Is(err, ErrRunState) {
		t.Fatalf("unknown account Apply = %v, want ErrRunState", err)
	}
}
