package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// RunStatus names the lifecycle of one weekly distribution run:
// open runs pay each planned account once, closed epochs never pay
// again and never reopen.
type RunStatus string

const (
	// RunOpen accepts idempotent applications in sealed plan order:
	// the first application of a planned account counts, retries of
	// the same account change nothing.
	RunOpen RunStatus = "open"
	// RunClosed is terminal: the epoch settled with exactly the paid
	// shares moved and the rest left over. Late eligibility changes,
	// appeals and corrections never reopen it.
	RunClosed RunStatus = "closed"
)

// ParseRunStatus validates a status against the closed machine.
func ParseRunStatus(raw string) (RunStatus, error) {
	status := RunStatus(raw)
	switch status {
	case RunOpen, RunClosed:
		return status, nil
	default:
		return "", ErrInvalidRun
	}
}

// String returns the stored status value.
func (s RunStatus) String() string { return string(s) }

// Run is one weekly distribution execution: a sealed copy of a plan
// plus the set of accounts already paid. The seal binds epoch,
// budget, cap, quota and the ordered accounts: any reclassification
// breaks the seal first, and status moves freely without breaking
// it. The run moves no value itself: later adapters move each share
// once, and this value only decides who is still owed.
type Run struct {
	EpochKey    string
	Budget      int64
	Cap         int64
	Quota       int64
	Order       []string
	Paid        map[string]bool
	Distributed int64
	Remainder   int64
	Status      RunStatus
	Hash        string
}

// sealRun binds epoch, budget, cap, quota and the ordered accounts.
func sealRun(epochKey string, budget, cap, quota int64, order []string) string {
	canonical := fmt.Sprintf("%s\x00%d\x00%d\x00%d\x00%s",
		epochKey, budget, cap, quota, strings.Join(order, "\x00"))
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// copyRunOrder validates one plan snapshot into run order: every
// share carries the plan quota, accounts are opaque, sorted ascending
// and unique. The copy never aliases the plan.
func copyRunOrder(plan Plan) ([]string, error) {
	order := make([]string, 0, len(plan.Shares))
	seen := make(map[string]bool, len(plan.Shares))
	for _, share := range plan.Shares {
		if share.Amount != plan.Quota {
			return nil, ErrInvalidRun
		}
		if _, err := parseNewcomerToken(share.Account); err != nil {
			return nil, ErrInvalidRun
		}
		if seen[share.Account] {
			return nil, ErrInvalidRun
		}
		seen[share.Account] = true
		order = append(order, share.Account)
	}
	if !sort.StringsAreSorted(order) {
		return nil, ErrInvalidRun
	}
	return order, nil
}

// checkPlanAccounting proves one plan closes before it runs:
// distributed and remainder split the budget, the quota never
// exceeds the floored split, and empty plans move zero.
func checkPlanAccounting(plan Plan) error {
	if plan.Budget < 0 || plan.Cap <= 0 || plan.Quota < 0 {
		return ErrInvalidRun
	}
	if plan.Distributed < 0 || plan.Remainder < 0 {
		return ErrInvalidRun
	}
	if plan.Distributed+plan.Remainder != plan.Budget {
		return ErrInvalidRun
	}
	headcount := int64(len(plan.Shares))
	if headcount == 0 {
		if plan.Quota != 0 || plan.Distributed != 0 {
			return ErrInvalidRun
		}
		return nil
	}
	if plan.Quota > plan.Budget/headcount {
		return ErrInvalidRun
	}
	if plan.Distributed != plan.Quota*headcount {
		return ErrInvalidRun
	}
	return nil
}

// StartRun seals one open execution over a plan snapshot. Blocked,
// appealed, cancelled and erased grants never reach it: the plan
// already captured only admitted accounts. The order is copied, so
// later changes to the input plan never move the run.
func StartRun(plan Plan) (Run, error) {
	if _, err := ParseEpochKey(plan.EpochKey); err != nil {
		return Run{}, ErrInvalidRun
	}
	if err := checkPlanAccounting(plan); err != nil {
		return Run{}, err
	}
	order, err := copyRunOrder(plan)
	if err != nil {
		return Run{}, err
	}
	return Run{
		EpochKey: plan.EpochKey, Budget: plan.Budget, Cap: plan.Cap,
		Quota: plan.Quota, Order: order, Paid: map[string]bool{},
		Distributed: plan.Distributed, Remainder: plan.Remainder,
		Status: RunOpen, Hash: sealRun(plan.EpochKey, plan.Budget, plan.Cap, plan.Quota, order),
	}, nil
}

// VerifyHash recomputes the seal and refuses a run whose planned
// terms no longer agree.
func (r Run) VerifyHash() error {
	if r.Hash == "" || sealRun(r.EpochKey, r.Budget, r.Cap, r.Quota, r.Order) != r.Hash {
		return ErrInvalidRun
	}
	return nil
}

// member reports whether account belongs to the sealed order.
func (r Run) member(account string) bool {
	for _, ordered := range r.Order {
		if ordered == account {
			return true
		}
	}
	return false
}

// copyPaid clones the paid set: applications never mutate the run
// they were called on.
func copyPaid(paid map[string]bool) map[string]bool {
	next := make(map[string]bool, len(paid))
	for account, done := range paid {
		next[account] = done
	}
	return next
}

// Apply records one share payment idempotently: the first
// application of a planned account counts, a retry of the same
// account returns the run unchanged with paid=false, and two workers
// racing the same account converge on one payment. Closed runs never
// pay, and accounts outside the sealed order — including later
// eligibility changes and appeals — refuse with ErrRunState.
func (r Run) Apply(account string) (Run, bool, error) {
	if err := r.VerifyHash(); err != nil {
		return Run{}, false, err
	}
	if r.Status != RunOpen {
		return Run{}, false, ErrRunState
	}
	if _, err := parseNewcomerToken(account); err != nil {
		return Run{}, false, ErrInvalidRun
	}
	if !r.member(account) {
		return Run{}, false, ErrRunState
	}
	if r.Paid[account] {
		return r, false, nil
	}
	next := r
	next.Paid = copyPaid(r.Paid)
	next.Paid[account] = true
	return next, true, nil
}

// NextUnpaid peeks the next owed account in sealed order: the slot a
// worker claims next. Empty while closed or when nothing is owed.
func (r Run) NextUnpaid() (string, bool) {
	if r.Status != RunOpen {
		return "", false
	}
	for _, account := range r.Order {
		if !r.Paid[account] {
			return account, true
		}
	}
	return "", false
}

// PaidCount counts paid accounts: each counted once by construction.
func (r Run) PaidCount() int {
	count := 0
	for _, done := range r.Paid {
		if done {
			count++
		}
	}
	return count
}

// PaidTotal sums paid shares: quota times the paid headcount, always
// within the planned distributed total.
func (r Run) PaidTotal() int64 {
	return r.Quota * int64(r.PaidCount())
}

// Close settles the epoch: terminal, with exactly the paid shares
// moved and the rest left over. Closed runs never reopen, never pay
// again, and never absorb later eligibility changes. A correction is
// a new plan from an explicit current budget with a new run: this
// run stays untouched.
func (r Run) Close() (Run, error) {
	if err := r.VerifyHash(); err != nil {
		return Run{}, err
	}
	if r.Status != RunOpen {
		return Run{}, ErrRunState
	}
	next := r
	next.Status = RunClosed
	return next, nil
}

// ResumeFromPaid rebuilds an open run after a crash: the sealed plan
// plus the persisted paid accounts, replayed idempotently. Unknown
// accounts refuse instead of paying.
func ResumeFromPaid(plan Plan, paidAccounts []string) (Run, error) {
	run, err := StartRun(plan)
	if err != nil {
		return Run{}, err
	}
	for _, account := range paidAccounts {
		next, _, err := run.Apply(account)
		if err != nil {
			return Run{}, err
		}
		run = next
	}
	return run, nil
}
