package postgres_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// Independent accounting model of the wallet ledger (P24-T04).
//
// The system under test is the real stack: the credit/debit application use
// cases over the real PostgreSQL repository in a disposable database. The
// oracle below is a separate, deliberately small set of integer rules
// written from the documented contract: conservation per bucket and
// account, free-first debit priority, single-use idempotency keys, no
// partial writes and no negative balances. It never calls domain
// constructors, domain transitions or application use cases; operation
// VALUES are learned from observed system outputs, while the TRANSITION
// RULES are the oracle's own.

// modelClock is one fixed instant for the use cases. Wallet credits and
// debits carry no time rules, so a frozen clock keeps every sequence
// replayable bit-for-bit.
type modelClock struct{}

func (modelClock) Now() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }

// ledgerOutcome is what the oracle recorded for one idempotency key: the
// first outcome sticks, and every replay must repeat it without moving.
type ledgerOutcome struct {
	accepted       bool
	opID           string
	freeDelta      int64
	purchasedDelta int64
	allocFree      int64
	allocPurchased int64
	reference      string
	isDebit        bool
}

// ledgerAccount is everything the oracle believes about one wallet.
type ledgerAccount struct {
	pgRow     platformpg.AppAccount
	id        domain.AccountID
	free      int64
	purchased int64
	keys      map[string]*ledgerOutcome
	ops       int
}

// ledgerDivergence is one place where the ledger and the oracle disagreed.
type ledgerDivergence struct {
	step int
	op   string
	want bool
	got  bool
}

// ledgerWorld wires the real use cases over one disposable database.
type ledgerWorld struct {
	ctx      context.Context
	t        *testing.T
	pool     *pgxpool.Pool
	repo     *walletpg.Repository
	queries  *platformpg.Queries
	credit   *application.CreditInkUseCase
	debit    *application.DebitInkUseCase
	balances *application.GetWalletBalanceUseCase
}

func newLedgerWorld(t *testing.T) *ledgerWorld {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	queries := platformpg.New(pool)
	return &ledgerWorld{
		ctx:      ctx,
		t:        t,
		pool:     pool,
		repo:     repo,
		queries:  queries,
		credit:   application.NewCreditInkUseCase(repo, modelClock{}),
		debit:    application.NewDebitInkUseCase(repo, modelClock{}),
		balances: application.NewGetWalletBalanceUseCase(repo),
	}
}

// ledgerRefValid mirrors the persisted reference rule (docs: printable
// ASCII 0x21-0x7e, 1..200 chars) without calling the constructor.
func ledgerRefValid(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 200 {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] < 0x21 || trimmed[i] > 0x7e {
			return false
		}
	}
	return true
}

var ledgerCreditTypes = map[string]bool{
	"credit_free": true, "credit_member": true, "credit_purchase": true, "credit_refund": true,
}

var ledgerDebitTypes = map[string]bool{
	"debit_argument": true, "debit_refund": true,
}

var ledgerBuckets = map[string]bool{"FREE_INK": true, "PURCHASED_INK": true}

// ledgerRunner carries one deterministic command sequence over two
// wallets: the world, the per-account oracles and every divergence found.
type ledgerRunner struct {
	t         *testing.T
	world     *ledgerWorld
	accounts  []*ledgerAccount
	originals map[string]recordedCommand
	step      int
	diverged  []ledgerDivergence
	trace     []string
	accepts   int
	refusals  int
	replays   int
}

// recordedCommand is the first attempt under one key, kept for replays.
type recordedCommand struct {
	isDebit   bool
	bucket    string
	opType    string
	amount    int64
	reference string
	key       string
}

// ledgerCheck compares one outcome with the oracle prediction and records
// the divergence instead of stopping, so one red sequence shows every
// place it disagrees.
func (r *ledgerRunner) check(op string, wantAccept bool, err error) bool {
	gotAccept := err == nil
	r.trace = append(r.trace, op)
	if gotAccept != wantAccept {
		r.diverged = append(r.diverged, ledgerDivergence{step: r.step, op: op, want: wantAccept, got: gotAccept})
		return false
	}
	return true
}

// ledgerBalances reads the derived and cached balances of one account. A
// wallet that never received a write has no row yet: both reads must then
// agree with the empty oracle, and any nonzero reading is a divergence the
// caller records.
func (r *ledgerRunner) ledgerBalances(acct *ledgerAccount) (derivedFree, derivedPurchased, cachedFree, cachedPurchased int64) {
	r.t.Helper()
	oracleEmpty := acct.free == 0 && acct.purchased == 0 && acct.ops == 0
	derived, derr := r.world.balances.Execute(r.world.ctx, acct.id)
	if derr != nil {
		if !oracleEmpty {
			r.t.Fatalf("derived balance of a written wallet: %v", derr)
		}
	} else {
		derivedFree, derivedPurchased = derived.Free.Int64(), derived.Purchased.Int64()
	}
	cached, cerr := r.world.queries.GetWalletAccount(r.world.ctx, acct.pgRow.ID)
	if cerr != nil {
		if !oracleEmpty {
			r.t.Fatalf("cached wallet of a written wallet: %v", cerr)
		}
	} else {
		cachedFree, cachedPurchased = cached.BalanceFree, cached.BalancePurchased
	}
	return derivedFree, derivedPurchased, cachedFree, cachedPurchased
}

// reconcile proves conservation after one command: derived, cached and
// oracle agree per bucket, balances never go negative, and the operation
// count matches the accepted outcomes.
func (r *ledgerRunner) reconcile(op string) {
	r.t.Helper()
	for _, acct := range r.accounts {
		derivedFree, derivedPurchased, cachedFree, cachedPurchased := r.ledgerBalances(acct)
		if derivedFree != acct.free || derivedPurchased != acct.purchased {
			r.diverged = append(r.diverged, ledgerDivergence{step: r.step, op: op + ":derived", want: true, got: false})
		}
		if cachedFree != acct.free || cachedPurchased != acct.purchased {
			r.diverged = append(r.diverged, ledgerDivergence{step: r.step, op: op + ":cached", want: true, got: false})
		}
		if derivedFree < 0 || derivedPurchased < 0 || cachedFree < 0 || cachedPurchased < 0 {
			r.diverged = append(r.diverged, ledgerDivergence{step: r.step, op: op + ":negative", want: true, got: false})
		}
		if got := countRows(r.t, r.world.ctx, r.world.pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acct.pgRow.ID); got != acct.ops {
			r.diverged = append(r.diverged, ledgerDivergence{step: r.step, op: op + ":opcount", want: true, got: false})
		}
	}
}

// opCredit predicts and runs one credit command expressed in transport
// strings, exactly as the API would carry it.
func (r *ledgerRunner) opCredit(acct *ledgerAccount, bucket, opType string, amount int64, reference, key string) {
	r.step++
	if prev, ok := acct.keys[key]; ok && prev.accepted {
		// The key resolved to an accepted operation: the system must
		// repeat it without moving anything.
		res, err := r.world.credit.Execute(r.world.ctx, application.CreditInkCommand{AccountID: string(acct.id), Bucket: bucket, OperationType: opType, Amount: amount, Reference: reference, IdempotencyKey: key})
		if !r.check("credit-replay", true, err) {
			return
		}
		if res == nil || !res.Replayed || res.Operation.ID().String() != prev.opID || res.Operation.Reference().String() != prev.reference {
			r.diverged = append(r.diverged, ledgerDivergence{step: r.step, op: "credit-replay-shape", want: true, got: false})
		}
		r.replays++
		r.reconcile("credit-replay")
		return
	}
	wantAccept := ledgerBuckets[bucket] && ledgerCreditTypes[opType] && amount >= 1 && ledgerRefValid(reference) && ledgerRefValid(key)
	r.originals[string(acct.id)+":"+key] = recordedCommand{bucket: bucket, opType: opType, amount: amount, reference: reference, key: key}
	res, err := r.world.credit.Execute(r.world.ctx, application.CreditInkCommand{AccountID: string(acct.id), Bucket: bucket, OperationType: opType, Amount: amount, Reference: reference, IdempotencyKey: key})
	if !r.check("credit", wantAccept, err) {
		acct.keys[key] = &ledgerOutcome{accepted: false}
		return
	}
	if !wantAccept {
		acct.keys[key] = &ledgerOutcome{accepted: false}
		r.refusals++
		r.reconcile("credit-refused")
		return
	}
	if res == nil || res.Replayed || res.Operation.ID().IsZero() || res.Operation.Reference().String() != reference {
		r.diverged = append(r.diverged, ledgerDivergence{step: r.step, op: "credit-shape", want: true, got: false})
		return
	}
	outcome := &ledgerOutcome{accepted: true, opID: res.Operation.ID().String(), reference: reference}
	if bucket == "FREE_INK" {
		outcome.freeDelta = amount
		acct.free += amount
	} else {
		outcome.purchasedDelta = amount
		acct.purchased += amount
	}
	acct.keys[key] = outcome
	acct.ops++
	r.accepts++
	r.reconcile("credit")
}

// planDebit is the oracle's own priority rule: free first, never partial,
// overflow-safe without ever adding two large balances.
func planDebit(amount, free, purchased int64) (takeFree, takePurchased int64, ok bool) {
	if amount <= 0 {
		return 0, 0, false
	}
	if amount <= free {
		return amount, 0, true
	}
	if free > math.MaxInt64-purchased {
		return 0, 0, false
	}
	if amount > free+purchased {
		return 0, 0, false
	}
	return free, amount - free, true
}

// opDebit predicts and runs one debit, comparing the applied split with
// the oracle plan on every acceptance.
func (r *ledgerRunner) opDebit(acct *ledgerAccount, opType string, amount int64, reference, key string, mutantPriority bool) {
	r.step++
	if prev, ok := acct.keys[key]; ok && prev.accepted {
		res, err := r.world.debit.Execute(r.world.ctx, application.DebitInkCommand{AccountID: string(acct.id), OperationType: opType, Amount: amount, Reference: reference, IdempotencyKey: key})
		if !r.check("debit-replay", true, err) {
			return
		}
		if res == nil || !res.Replayed || res.Operation.ID().String() != prev.opID {
			r.diverged = append(r.diverged, ledgerDivergence{step: r.step, op: "debit-replay-shape", want: true, got: false})
		}
		if res != nil && (res.Allocation.FromFree().Int64() != prev.allocFree || res.Allocation.FromPurchased().Int64() != prev.allocPurchased) {
			r.diverged = append(r.diverged, ledgerDivergence{step: r.step, op: "debit-replay-alloc", want: true, got: false})
		}
		r.replays++
		r.reconcile("debit-replay")
		return
	}
	takeFree, takePurchased, plannable := planDebit(amount, acct.free, acct.purchased)
	wantAccept := ledgerDebitTypes[opType] && plannable && ledgerRefValid(reference) && ledgerRefValid(key)
	if mutantPriority {
		takeFree, takePurchased = takePurchased, takeFree
	}
	r.originals[string(acct.id)+":"+key] = recordedCommand{isDebit: true, opType: opType, amount: amount, reference: reference, key: key}
	res, err := r.world.debit.Execute(r.world.ctx, application.DebitInkCommand{AccountID: string(acct.id), OperationType: opType, Amount: amount, Reference: reference, IdempotencyKey: key})
	if !r.check("debit", wantAccept, err) {
		acct.keys[key] = &ledgerOutcome{accepted: false}
		return
	}
	if !wantAccept {
		acct.keys[key] = &ledgerOutcome{accepted: false}
		r.refusals++
		r.reconcile("debit-refused")
		return
	}
	if res == nil || res.Replayed || res.Operation.ID().IsZero() || res.Operation.Reference().String() != reference {
		r.diverged = append(r.diverged, ledgerDivergence{step: r.step, op: "debit-shape", want: true, got: false})
		return
	}
	if res.Allocation.FromFree().Int64() != takeFree || res.Allocation.FromPurchased().Int64() != takePurchased {
		r.diverged = append(r.diverged, ledgerDivergence{step: r.step, op: "debit-alloc", want: true, got: false})
		return
	}
	acct.keys[key] = &ledgerOutcome{accepted: true, opID: res.Operation.ID().String(), freeDelta: -takeFree, purchasedDelta: -takePurchased, allocFree: takeFree, allocPurchased: takePurchased, reference: reference, isDebit: true}
	acct.free -= takeFree
	acct.purchased -= takePurchased
	acct.ops++
	r.accepts++
	r.reconcile("debit")
}

// drawCommand generates one command from the registered stream. Amounts
// span small change, bucket-exact boundaries, uint-scale funding and the
// invalid edges; keys are namespaced per account because the ledger key is
// global and cross-account reuse is a conflict, not a replay.
func drawCommand(rnd *testsource.Random, slot, seq, step int, free, purchased int64, seen []string) (ledgerCommand, bool) {
	total := free + purchased
	if total < 0 {
		total = 0
	}
	key := func(kind string) string { return fmt.Sprintf("a%d:%s:%d:%d", slot, kind, seq, step) }
	ref := func(kind string) string { return fmt.Sprintf("%s:%d:%d", kind, seq, step) }
	roll := rnd.Int64n(100)
	switch {
	case roll < 6 && len(seen) > 0:
		return ledgerCommand{key: seen[rnd.Int64n(int64(len(seen)))]}, true
	case roll < 48:
		bucket := "FREE_INK"
		if rnd.Int64n(2) == 0 {
			bucket = "PURCHASED_INK"
		}
		types := []string{"credit_free", "credit_member", "credit_purchase", "credit_refund"}
		// Room keeps the oracle inside int64: the store is bigint and
		// would hold more, but neither the cached projection nor the
		// oracle can represent it, so saturation draws a refused edge
		// instead of a false divergence. Near-max funding lives in the
		// dedicated boundary test.
		cur := free
		if bucket == "PURCHASED_INK" {
			cur = purchased
		}
		room := math.MaxInt64 - cur
		if room <= 0 {
			return ledgerCommand{bucket: bucket, opType: types[0], amount: 0, reference: ref("bad"), key: key("bad")}, false
		}
		capSmall := room - 1
		if capSmall > 5000 {
			capSmall = 5000
		}
		amount := 1 + rnd.Int64n(capSmall)
		if room > 200000 {
			switch rnd.Int64n(20) {
			case 0:
				amount = 1 + rnd.Int64n(200000)
			case 1:
				amount = math.MaxInt64 - rnd.Int64n(100000)
				if amount > room {
					amount = 0
				}
			}
		}
		if amount < 1 {
			return ledgerCommand{bucket: bucket, opType: types[0], amount: 0, reference: ref("bad"), key: key("bad")}, false
		}
		return ledgerCommand{bucket: bucket, opType: types[rnd.Int64n(int64(len(types)))], amount: amount, reference: ref("credit"), key: key("credit")}, false
	case roll < 86:
		types := []string{"debit_argument", "debit_refund"}
		var amount int64
		switch rnd.Int64n(20) {
		case 0, 1, 2, 3:
			amount = total + 1 + rnd.Int64n(5000)
			if total == math.MaxInt64 {
				amount = math.MaxInt64
			}
		case 4:
			amount = 0
		case 5:
			amount = math.MaxInt64
		case 6:
			amount = free
			if amount == 0 {
				amount = total
			}
		case 7:
			amount = total
		default:
			if total == 0 {
				amount = 1 + rnd.Int64n(100)
			} else {
				amount = 1 + rnd.Int64n(total)
			}
		}
		if amount < 0 {
			amount = 0
		}
		return ledgerCommand{isDebit: true, opType: types[rnd.Int64n(int64(len(types)))], amount: amount, reference: ref("debit"), key: key("debit")}, false
	default:
		// Invalid edges: unknown bucket/type, admin through the open path,
		// crossed direction, zero and negative amounts, malformed
		// reference and key. Every one must be refused writing nothing.
		switch rnd.Int64n(9) {
		case 0:
			return ledgerCommand{bucket: "NOPE", opType: "credit_free", amount: 10, reference: ref("bad"), key: key("bad")}, false
		case 1:
			return ledgerCommand{bucket: "FREE_INK", opType: "credit_nope", amount: 10, reference: ref("bad"), key: key("bad")}, false
		case 2:
			return ledgerCommand{bucket: "FREE_INK", opType: "credit_admin", amount: 10, reference: ref("bad"), key: key("bad")}, false
		case 3:
			return ledgerCommand{isDebit: true, opType: "credit_free", amount: 10, reference: ref("bad"), key: key("bad")}, false
		case 4:
			return ledgerCommand{bucket: "FREE_INK", opType: "debit_argument", amount: 10, reference: ref("bad"), key: key("bad")}, false
		case 5:
			return ledgerCommand{isDebit: true, opType: "debit_argument", amount: 0, reference: ref("bad"), key: key("bad")}, false
		case 6:
			return ledgerCommand{bucket: "FREE_INK", opType: "credit_free", amount: -1 - rnd.Int64n(1000), reference: ref("bad"), key: key("bad")}, false
		case 7:
			return ledgerCommand{bucket: "FREE_INK", opType: "credit_free", amount: 10, reference: "has space", key: key("bad")}, false
		default:
			return ledgerCommand{bucket: "FREE_INK", opType: "credit_free", amount: 10, reference: ref("bad"), key: ""}, false
		}
	}
}

// ledgerCommand is one drawn operation in transport strings.
type ledgerCommand struct {
	isDebit   bool
	bucket    string
	opType    string
	amount    int64
	reference string
	key       string
}

// runOneSequence executes one deterministic command list over the runner
// wallets, comparing every outcome, split and balance with the oracle.
func (r *ledgerRunner) runOneSequence(rnd *testsource.Random, seq int, mutantPriority bool) {
	steps := 6 + rnd.Int64n(9)
	for i := 0; i < int(steps); i++ {
		slot := int(rnd.Int64n(2))
		acct := r.accounts[slot]
		seen := make([]string, 0, len(acct.keys))
		for k := range acct.keys {
			seen = append(seen, k)
		}
		cmd, replay := drawCommand(rnd, slot, seq, i, acct.free, acct.purchased, seen)
		if replay {
			r.replayOriginal(acct, cmd.key)
			continue
		}
		if cmd.isDebit {
			r.opDebit(acct, cmd.opType, cmd.amount, cmd.reference, cmd.key, mutantPriority)
		} else {
			r.opCredit(acct, cmd.bucket, cmd.opType, cmd.amount, cmd.reference, cmd.key)
		}
	}
}

// replayOriginal resends a recorded first attempt identically: the replay
// path only exists for byte-identical retries.
func (r *ledgerRunner) replayOriginal(acct *ledgerAccount, key string) {
	r.t.Helper()
	rec, ok := r.originals[acct.id.String()+":"+key]
	if !ok {
		r.t.Fatalf("model lost the original command of key %q", key)
	}
	if rec.isDebit {
		r.opDebit(acct, rec.opType, rec.amount, rec.reference, rec.key, false)
	} else {
		r.opCredit(acct, rec.bucket, rec.opType, rec.amount, rec.reference, rec.key)
	}
}

// TestWalletLedgerModel runs hundreds of deterministic credit/debit
// sequences over two real wallets and compares every outcome, every bucket
// split and every balance with the independent oracle: final balances equal
// credits minus debits per bucket and account, replays repeat without
// moving, refusals write nothing, and the two accounts never cross.
func TestWalletLedgerModel(t *testing.T) {
	seed := testsource.SeedFor(t)
	rnd := testsource.NewRandom(seed)
	world := newLedgerWorld(t)
	const sequences = 200
	divergentRuns := 0
	var first []ledgerDivergence
	var firstTrace []string
	accepts, refusals, replays := 0, 0, 0
	for seq := 0; seq < sequences; seq++ {
		accounts := make([]*ledgerAccount, 0, 2)
		for slot := 0; slot < 2; slot++ {
			email := fmt.Sprintf("ledger-model-%d-%d@arena.example.com", seq, slot)
			pgAcc := mustWalletAccount(t, world.ctx, world.queries, email)
			accounts = append(accounts, &ledgerAccount{pgRow: pgAcc, id: domain.AccountID(uuidString(pgAcc.ID)), keys: make(map[string]*ledgerOutcome)})
		}
		runner := &ledgerRunner{t: t, world: world, accounts: accounts, originals: make(map[string]recordedCommand)}
		runner.runOneSequence(rnd, seq, false)
		accepts += runner.accepts
		refusals += runner.refusals
		replays += runner.replays
		if len(runner.diverged) > 0 {
			divergentRuns++
			if first == nil {
				first = append([]ledgerDivergence{}, runner.diverged...)
				if len(first) > 5 {
					first = first[:5]
				}
				firstTrace = append([]string{}, runner.trace...)
				if len(firstTrace) > 10 {
					firstTrace = firstTrace[len(firstTrace)-10:]
				}
			}
		}
	}
	if accepts == 0 || refusals == 0 || replays == 0 {
		t.Fatalf("model never exercised a full path: accepts=%d refusals=%d replays=%d", accepts, refusals, replays)
	}
	if divergentRuns > 0 {
		t.Fatalf("%d of %d sequences diverged, first: %+v trace: %v", divergentRuns, sequences, first, firstTrace)
	}
	t.Logf("wallet ledger model: %d sequences, accepts=%d refusals=%d replays=%d, seed %d", sequences, accepts, refusals, replays, seed)
}

// TestWalletLedgerOverflowUnderflow proves the boundary refusals write
// nothing: a spanning debit over near-max balances (which would overflow
// the guarded sum), a debit on an empty wallet, and zero and negative
// amounts all leave balances and operation counts untouched.
func TestWalletLedgerOverflowUnderflow(t *testing.T) {
	world := newLedgerWorld(t)
	pgAcc := mustWalletAccount(t, world.ctx, world.queries, "ledger-boundary@arena.example.com")
	accountID := domain.AccountID(uuidString(pgAcc.ID))
	credit := func(bucket, opType string, amount int64, ref, key string) error {
		_, err := world.credit.Execute(world.ctx, application.CreditInkCommand{AccountID: string(accountID), Bucket: bucket, OperationType: opType, Amount: amount, Reference: ref, IdempotencyKey: key})
		return err
	}
	debit := func(opType string, amount int64, ref, key string) error {
		_, err := world.debit.Execute(world.ctx, application.DebitInkCommand{AccountID: string(accountID), OperationType: opType, Amount: amount, Reference: ref, IdempotencyKey: key})
		return err
	}
	balances := func() (int64, int64, int) {
		t.Helper()
		derived, err := world.balances.Execute(world.ctx, accountID)
		if err != nil {
			t.Fatalf("derived balance: %v", err)
		}
		ops := countRows(t, world.ctx, world.pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", pgAcc.ID)
		return derived.Free.Int64(), derived.Purchased.Int64(), ops
	}
	huge := int64(math.MaxInt64 - 500)
	if err := credit("FREE_INK", "credit_free", huge, "boundary:free", "boundary:free:1"); err != nil {
		t.Fatalf("fund free: %v", err)
	}
	if err := credit("PURCHASED_INK", "credit_purchase", huge, "boundary:pur", "boundary:pur:1"); err != nil {
		t.Fatalf("fund purchased: %v", err)
	}
	freeBefore, purchasedBefore, opsBefore := balances()
	// A debit spanning both near-max buckets would overflow the guarded
	// sum: it must be refused atomically.
	if err := debit("debit_argument", math.MaxInt64, "boundary:span", "boundary:span:1"); err == nil {
		t.Fatal("spanning debit over near-max balances was accepted")
	}
	// Empty-amount and negative-amount edges, both directions.
	for _, attempt := range []struct {
		name string
		call func() error
	}{
		{"credit-zero", func() error { return credit("FREE_INK", "credit_free", 0, "boundary:z", "boundary:z:1") }},
		{"credit-negative", func() error { return credit("FREE_INK", "credit_free", -5, "boundary:n", "boundary:n:1") }},
		{"debit-zero", func() error { return debit("debit_argument", 0, "boundary:z", "boundary:z:2") }},
		{"debit-negative", func() error { return debit("debit_argument", -5, "boundary:n", "boundary:n:2") }},
	} {
		if err := attempt.call(); err == nil {
			t.Fatalf("%s was accepted", attempt.name)
		}
	}
	freeAfter, purchasedAfter, opsAfter := balances()
	if freeAfter != freeBefore || purchasedAfter != purchasedBefore || opsAfter != opsBefore {
		t.Fatalf("refused boundary operations moved the ledger: balances %d/%d ops %d, want %d/%d ops %d",
			freeAfter, purchasedAfter, opsAfter, freeBefore, purchasedBefore, opsBefore)
	}
	// An empty wallet refuses any debit without writing.
	emptyAcc := mustWalletAccount(t, world.ctx, world.queries, "ledger-empty@arena.example.com")
	emptyID := domain.AccountID(uuidString(emptyAcc.ID))
	if _, err := world.debit.Execute(world.ctx, application.DebitInkCommand{AccountID: string(emptyID), OperationType: "debit_argument", Amount: 1, Reference: "boundary:empty", IdempotencyKey: "boundary:empty:1"}); err == nil {
		t.Fatal("debit on an empty wallet was accepted")
	}
	if got := countRows(t, world.ctx, world.pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", emptyAcc.ID); got != 0 {
		t.Fatalf("refused empty-wallet debit wrote %d operations", got)
	}
}

// TestWalletLedgerConcurrent races one idempotency key across goroutines:
// the locked repository admits exactly one winner, so single-use survives
// concurrency, and a scarce balance stays consistent under distinct races.
func TestWalletLedgerConcurrent(t *testing.T) {
	world := newLedgerWorld(t)
	pgAcc := mustWalletAccount(t, world.ctx, world.queries, "ledger-race@arena.example.com")
	accountID := domain.AccountID(uuidString(pgAcc.ID))
	credit := func(key string, amount int64) (bool, error) {
		res, err := world.credit.Execute(world.ctx, application.CreditInkCommand{AccountID: string(accountID), Bucket: "PURCHASED_INK", OperationType: "credit_purchase", Amount: amount, Reference: "race:credit", IdempotencyKey: key})
		if err != nil {
			return false, err
		}
		return !res.Replayed, nil
	}
	if _, err := credit("race:fund", 100000); err != nil {
		t.Fatalf("fund race wallet: %v", err)
	}
	const racers = 16
	raceKey := func(t *testing.T, run func(int) (bool, error)) int {
		t.Helper()
		var wg sync.WaitGroup
		wins := make(chan bool, racers)
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				fresh, err := run(i)
				wins <- err == nil && fresh
			}(i)
		}
		wg.Wait()
		close(wins)
		winners := 0
		for win := range wins {
			if win {
				winners++
			}
		}
		return winners
	}
	if winners := raceKey(t, func(i int) (bool, error) { return credit("race:same-credit", 100) }); winners != 1 {
		t.Fatalf("racing one credit key admitted %d winners, want exactly 1", winners)
	}
	debit := func(key string, amount int64) (bool, int64, int64, error) {
		res, err := world.debit.Execute(world.ctx, application.DebitInkCommand{AccountID: string(accountID), OperationType: "debit_argument", Amount: amount, Reference: "race:debit", IdempotencyKey: key})
		if err != nil {
			return false, 0, 0, err
		}
		return !res.Replayed, res.Allocation.FromFree().Int64(), res.Allocation.FromPurchased().Int64(), nil
	}
	if winners := raceKey(t, func(i int) (bool, error) {
		fresh, _, _, err := debit("race:same-debit", 50)
		return fresh, err
	}); winners != 1 {
		t.Fatalf("racing one debit key admitted %d winners, want exactly 1", winners)
	}
	// Distinct keys against a scarce balance: whatever the interleaving,
	// the final balances equal the funding plus the observed accepted
	// deltas, and never go negative.
	var mu sync.Mutex
	var acceptedPurchased int64
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fresh, takeFree, takePurchased, err := debit(fmt.Sprintf("race:scarce:%d", i), 20000)
			if err != nil || !fresh {
				return
			}
			if takeFree != 0 {
				t.Errorf("scarce race took %d from the empty free bucket", takeFree)
				return
			}
			mu.Lock()
			acceptedPurchased += takePurchased
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	derived, err := world.balances.Execute(world.ctx, accountID)
	if err != nil {
		t.Fatalf("derived balance after races: %v", err)
	}
	// Funding was 100000 purchased plus the 100 raced credit, minus the 50
	// raced debit (all purchased: the free bucket stayed empty).
	wantPurchased := int64(100000+100-50) - acceptedPurchased
	if derived.Free.Int64() != 0 || derived.Purchased.Int64() != wantPurchased {
		t.Fatalf("balances after races = %d/%d, want 0/%d", derived.Free.Int64(), derived.Purchased.Int64(), wantPurchased)
	}
	if derived.Purchased.Int64() < 0 {
		t.Fatal("scarce-balance race left a negative balance")
	}
}

// TestWalletLedgerDetectsMutatedPriority proves the harness bites: the same
// fixed script run against a mutant oracle that drains purchased first
// must report the allocation divergence.
func TestWalletLedgerDetectsMutatedPriority(t *testing.T) {
	world := newLedgerWorld(t)
	run := func(mutantPriority bool) []ledgerDivergence {
		tag := "mutant:false"
		if mutantPriority {
			tag = "mutant:true"
		}
		pgAcc := mustWalletAccount(t, world.ctx, world.queries, fmt.Sprintf("ledger-%s@arena.example.com", tag))
		acct := &ledgerAccount{pgRow: pgAcc, id: domain.AccountID(uuidString(pgAcc.ID)), keys: make(map[string]*ledgerOutcome)}
		runner := &ledgerRunner{t: t, world: world, accounts: []*ledgerAccount{acct}, originals: make(map[string]recordedCommand)}
		runner.opCredit(acct, "FREE_INK", "credit_free", 1000, tag+":free", tag+":free:1")
		runner.opCredit(acct, "PURCHASED_INK", "credit_purchase", 1000, tag+":pur", tag+":pur:1")
		runner.opDebit(acct, "debit_argument", 1500, tag+":debit", tag+":debit:1", mutantPriority)
		return runner.diverged
	}
	if diverged := run(false); len(diverged) > 0 {
		t.Fatalf("true priority diverged on the fixed script: %+v", diverged)
	}
	mutated := run(true)
	if len(mutated) == 0 {
		t.Fatal("mutant priority (purchased-first) reported no divergence: the harness would not catch the flipped allocation")
	}
	found := false
	for _, d := range mutated {
		if d.op == "debit-alloc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mutant divergences %+v do not name the flipped allocation", mutated)
	}
}
