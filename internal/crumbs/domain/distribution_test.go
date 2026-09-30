package domain

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

func distributionPerson(i int) string {
	return fmt.Sprintf("%064x", 1000+i)
}

func mustDistributionAdmit(t *testing.T, account string, i int, epoch string, admitted []Grant) ([]Grant, Grant) {
	t.Helper()
	req := AdmitRequest{
		Account: account, PersonProofHash: distributionPerson(i), EpochKey: epoch,
		AccountActive: true, AntifraudClear: true,
	}
	grant, err := AdmitNewcomer(req, admitted)
	if err != nil {
		t.Fatalf("AdmitNewcomer(%s): %v", account, err)
	}
	return append(admitted, grant), grant
}

func admitDistributionSet(t *testing.T, epoch string, n int) []Grant {
	t.Helper()
	var admitted []Grant
	for i := 0; i < n; i++ {
		var grant Grant
		admitted, grant = mustDistributionAdmit(t, fmt.Sprintf("campones-%02d", i), i, epoch, admitted)
		_ = grant
	}
	return admitted
}

func TestPlanDistributionEmptySetMovesZero(t *testing.T) {
	plan, err := PlanDistribution(1000, 5000, eligibilityEpoch, nil)
	if err != nil {
		t.Fatalf("PlanDistribution: %v", err)
	}
	if plan.Quota != 0 || plan.Distributed != 0 || len(plan.Shares) != 0 || plan.Remainder != 1000 {
		t.Fatalf("empty plan = %+v, want zero shares with whole M left over", plan)
	}
	zero, err := PlanDistribution(0, 5000, eligibilityEpoch, nil)
	if err != nil || zero.Distributed != 0 || zero.Remainder != 0 {
		t.Fatalf("zero budget plan = %+v/%v, want defined zeros", zero, err)
	}
}

func TestPlanDistributionSingleAndMany(t *testing.T) {
	single, err := PlanDistribution(1000, 5000, eligibilityEpoch, admitDistributionSet(t, eligibilityEpoch, 1))
	if err != nil || single.Quota != 1000 || single.Distributed != 1000 || single.Remainder != 0 {
		t.Fatalf("single = %+v/%v, want quota 1000", single, err)
	}
	many, err := PlanDistribution(1000, 5000, eligibilityEpoch, admitDistributionSet(t, eligibilityEpoch, 3))
	if err != nil {
		t.Fatalf("PlanDistribution: %v", err)
	}
	if many.Quota != 333 || many.Distributed != 999 || many.Remainder != 1 {
		t.Fatalf("many = %+v, want 333/999/1", many)
	}
	if many.Distributed+many.Remainder != 1000 || many.Distributed > 1000 {
		t.Fatalf("many = %+v, want distributed+remainder==M and distributed<=M", many)
	}
	capped, err := PlanDistribution(10000, 100, eligibilityEpoch, admitDistributionSet(t, eligibilityEpoch, 2))
	if err != nil || capped.Quota != 100 || capped.Distributed != 200 || capped.Remainder != 9800 {
		t.Fatalf("capped = %+v/%v, want min(cap,floor)", capped, err)
	}
	edge, err := PlanDistribution(math.MaxInt64, math.MaxInt64, eligibilityEpoch, admitDistributionSet(t, eligibilityEpoch, 2))
	if err != nil || edge.Quota != math.MaxInt64/2 || edge.Distributed+edge.Remainder != math.MaxInt64 || edge.Remainder != 1 {
		t.Fatalf("edge = %+v/%v, want floored half with remainder 1", edge, err)
	}
}

func TestPlanDistributionNeverRoundsUpDust(t *testing.T) {
	dust, err := PlanDistribution(2, 5000, eligibilityEpoch, admitDistributionSet(t, eligibilityEpoch, 3))
	if err != nil {
		t.Fatalf("PlanDistribution: %v", err)
	}
	if dust.Quota != 0 || dust.Distributed != 0 || dust.Remainder != 2 {
		t.Fatalf("dust = %+v, want zero quotas with whole M left over", dust)
	}
	for _, share := range dust.Shares {
		if share.Amount != 0 {
			t.Fatalf("dust share = %+v, want exactly zero: fractions below one milliINK are never paid", share)
		}
	}
	empty, err := PlanDistribution(0, 5000, eligibilityEpoch, admitDistributionSet(t, eligibilityEpoch, 2))
	if err != nil || empty.Quota != 0 || empty.Distributed != 0 || empty.Remainder != 0 {
		t.Fatalf("zero budget with heads = %+v/%v, want defined zeros", empty, err)
	}
}

func TestPlanDistributionRetryResumesWithoutDouble(t *testing.T) {
	grants := admitDistributionSet(t, eligibilityEpoch, 5)
	plan, err := PlanDistribution(1000, 5000, eligibilityEpoch, grants)
	if err != nil {
		t.Fatalf("PlanDistribution: %v", err)
	}
	if plan.Quota != 200 || plan.Distributed != 1000 || plan.Remainder != 0 {
		t.Fatalf("plan = %+v, want 200/1000/0", plan)
	}
	var prefix, suffix int64
	for i, share := range plan.Shares {
		if i < 2 {
			prefix += share.Amount
		} else {
			suffix += share.Amount
		}
	}
	if prefix != 400 || suffix != 600 || prefix+suffix != plan.Distributed {
		t.Fatalf("prefix=%d suffix=%d, want 400+600==1000: crash after 2 resumes at 3", prefix, suffix)
	}
	reversed := make([]Grant, len(grants))
	for i, g := range grants {
		reversed[len(grants)-1-i] = g
	}
	replay, err := PlanDistribution(1000, 5000, eligibilityEpoch, reversed)
	if err != nil {
		t.Fatalf("replay PlanDistribution: %v", err)
	}
	if len(replay.Shares) != len(plan.Shares) {
		t.Fatalf("replay shares = %d, want %d", len(replay.Shares), len(plan.Shares))
	}
	for i := range plan.Shares {
		if replay.Shares[i] != plan.Shares[i] {
			t.Fatalf("share %d = %+v, want %+v: same snapshot replays the same order", i, replay.Shares[i], plan.Shares[i])
		}
	}
}

func TestPlanDistributionCountsOnlyAdmittedOfEpoch(t *testing.T) {
	var admitted []Grant
	admitted, first := mustDistributionAdmit(t, "viva-1", 1, eligibilityEpoch, admitted)
	admitted, _ = mustDistributionAdmit(t, "viva-2", 2, eligibilityEpoch, admitted)
	admitted, _ = mustDistributionAdmit(t, "outra-epoca", 3, "2027-W01", admitted)
	blocked, _ := AdmitNewcomer(admitReq("duplicada", distributionPerson(1)), admitted)
	appealed, _ := blocked.Appeal("duplicada")
	cancelled, _ := first.Cancel()
	erased, _ := first.Erase()
	_ = appealed
	_ = cancelled
	_ = erased
	snapshot := append(append([]Grant{}, admitted...), blocked)
	plan, err := PlanDistribution(1000, 5000, eligibilityEpoch, snapshot)
	if err != nil {
		t.Fatalf("PlanDistribution: %v", err)
	}
	if len(plan.Shares) != 2 || plan.Quota != 500 || plan.Distributed != 1000 {
		t.Fatalf("plan = %+v, want only the 2 admitted of the epoch", plan)
	}
	if plan.Shares[0].Account != "viva-1" || plan.Shares[1].Account != "viva-2" {
		t.Fatalf("shares = %+v, want deterministic account order", plan.Shares)
	}
}

func TestPlanDistributionRefusesMalformedPlans(t *testing.T) {
	grants := admitDistributionSet(t, eligibilityEpoch, 1)
	for _, tc := range []struct {
		name   string
		budget int64
		cap    int64
		epoch  string
	}{
		{"negative budget", -1, 100, eligibilityEpoch},
		{"zero cap", 1000, 0, eligibilityEpoch},
		{"negative cap", 1000, -5, eligibilityEpoch},
		{"bad epoch", 1000, 100, "2021-W53"},
	} {
		if _, err := PlanDistribution(tc.budget, tc.cap, tc.epoch, grants); !errors.Is(err, ErrInvalidDistribution) {
			t.Fatalf("%s = nil, want ErrInvalidDistribution", tc.name)
		}
	}
	tampered := grants[0]
	tampered.Account = "forjado"
	if _, err := PlanDistribution(1000, 5000, eligibilityEpoch, []Grant{tampered}); !errors.Is(err, ErrInvalidDistribution) {
		t.Fatalf("corrupt seal = nil, want ErrInvalidDistribution")
	}
	dupA := buildGrant("duplicada", distributionPerson(11), eligibilityEpoch, NewcomerAdmitted)
	dupB := buildGrant("duplicada", distributionPerson(12), eligibilityEpoch, NewcomerAdmitted)
	if _, err := PlanDistribution(1000, 5000, eligibilityEpoch, []Grant{dupA, dupB}); !errors.Is(err, ErrInvalidDistribution) {
		t.Fatalf("doubled account = %v, want ErrInvalidDistribution: one snapshot never pays one account twice", err)
	}
}
