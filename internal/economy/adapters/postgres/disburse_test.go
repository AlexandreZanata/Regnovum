package postgres_test

// P34-T05 — governed Treasury disbursement on real PostgreSQL.
//
// One act pays one beneficiary from one origin vault with an
// allowlisted purpose and two governors besides the paid account,
// keyed idempotently with the audit row beside the legs. The tests
// prove on a disposable database: exact settlement with its audit
// trail, replay without duplication, concurrent acts collapsing to
// one, empty and unknown origins refusing without mint, unknown
// beneficiaries resolving to absence, divergent terms conflicting,
// third parties untouched and the frozen book refusing.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func disburseCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// disburseParties seeds beneficiary and governor accounts and returns
// their ids: distinct holders so no act governs itself.
func disburseParties(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (beneficiary, approverOne, approverTwo string) {
	t.Helper()
	for _, slot := range []*string{&beneficiary, &approverOne, &approverTwo} {
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO app.accounts (email, status) VALUES ('disburse-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
			t.Fatalf("seed party: %v", err)
		}
		*slot = id
	}
	return beneficiary, approverOne, approverTwo
}

func disburseCmd(key, vault, beneficiary, purpose string, millis int64, approverOne, approverTwo string) application.DisburseCommand {
	return application.DisburseCommand{
		Key: key, Vault: vault, Beneficiary: beneficiary, Purpose: purpose,
		Millis: millis, ApproverOne: approverOne, ApproverTwo: approverTwo,
	}
}

func mustDisburse(t *testing.T, ctx context.Context, repo *postgres.Repository, cmd application.DisburseCommand) *application.DisburseResult {
	t.Helper()
	result, err := application.NewDisburseUseCase(repo).Execute(ctx, cmd)
	if err != nil {
		t.Fatalf("Disburse: %v", err)
	}
	return result
}

// TestDisburseSettlesGovernedAct proves the happy path: 1000 leaves
// operating cash for the beneficiary, the audit row names purpose and
// governors beside the transfer, and S is conserved.
func TestDisburseSettlesGovernedAct(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := disburseCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)

	result := mustDisburse(t, ctx, repo, disburseCmd("disburse-1", "operating_cash", beneficiary, "compensation", 1000, approverOne, approverTwo))
	if result.Paid.Millis() != 1000 || result.Replayed {
		t.Fatalf("result = %+v, want 1000 fresh", result)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "operating_cash"); got != 4000 {
		t.Fatalf("origin = %d, want 4000", got)
	}
	if got := custodyBalance(t, ctx, pool, "user", beneficiary); got != 1000 {
		t.Fatalf("beneficiary = %d, want 1000", got)
	}
	var purpose, recordedOne, recordedTwo, transfer string
	var millis int64
	if err := pool.QueryRow(ctx,
		`SELECT purpose, approver_one::text, approver_two::text, transfer_id::text, amount_milli
		 FROM app.treasury_disbursements WHERE disbursement_key = 'disburse-1'`).Scan(
		&purpose, &recordedOne, &recordedTwo, &transfer, &millis); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if purpose != "compensation" || recordedOne != approverOne || recordedTwo != approverTwo {
		t.Fatalf("audit = %s/%s/%s, want compensation with both governors", purpose, recordedOne, recordedTwo)
	}
	if transfer != result.TransferID || millis != 1000 {
		t.Fatalf("audit transfer = %s/%d, want the settling transfer with 1000", transfer, millis)
	}
	economySupply(t, ctx, pool)
}

// TestDisburseReplaysWithoutDuplicating proves the second approval of
// one act resolves the original transfer with zero new legs and zero
// new audit rows.
func TestDisburseReplaysWithoutDuplicating(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := disburseCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)
	cmd := disburseCmd("disburse-replay", "operating_cash", beneficiary, "due_payment", 2500, approverOne, approverTwo)

	first := mustDisburse(t, ctx, repo, cmd)
	second := mustDisburse(t, ctx, repo, cmd)
	if !second.Replayed || second.TransferID != first.TransferID {
		t.Fatalf("replay resolved another settlement: %+v vs %+v", second, first)
	}
	if got := custodyBalance(t, ctx, pool, "user", beneficiary); got != 2500 {
		t.Fatalf("beneficiary = %d, want 2500 once", got)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.treasury_disbursements WHERE disbursement_key = 'disburse-replay'`).Scan(&rows); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", rows)
	}
	economySupply(t, ctx, pool)
}

// TestDisburseRacesCollapseToOne proves eight simultaneous approvals
// of one act settle a single payment: one executes, seven replay.
func TestDisburseRacesCollapseToOne(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := disburseCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 9000)
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)

	const runners = 8
	var wg sync.WaitGroup
	results := make([]*application.DisburseResult, runners)
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			useCase := application.NewDisburseUseCase(repo)
			results[i], errs[i] = useCase.Execute(ctx, disburseCmd("disburse-race", "operating_cash", beneficiary, "sale_settlement", 4000, approverOne, approverTwo))
		}(i)
	}
	wg.Wait()
	founded, replayed := 0, 0
	var transferID string
	for i := range runners {
		if errs[i] != nil {
			t.Fatalf("runner %d: %v", i, errs[i])
		}
		if results[i].Replayed {
			replayed++
		} else {
			founded++
		}
		if transferID == "" {
			transferID = results[i].TransferID
		} else if results[i].TransferID != transferID {
			t.Fatalf("runner %d settled another transfer", i)
		}
	}
	if founded != 1 || replayed != runners-1 {
		t.Fatalf("founded = %d, replayed = %d; want 1 and %d", founded, replayed, runners-1)
	}
	if got := custodyBalance(t, ctx, pool, "user", beneficiary); got != 4000 {
		t.Fatalf("beneficiary = %d, want 4000 once", got)
	}
	economySupply(t, ctx, pool)
}

// TestDisburseRefusesEmptyOrigin proves short origins fail closed: a
// never-opened vault resolves to absence and an empty vault refuses
// for insufficiency, both writing nothing anywhere.
func TestDisburseRefusesEmptyOrigin(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := disburseCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)
	useCase := application.NewDisburseUseCase(repo)

	if _, err := useCase.Execute(ctx, disburseCmd("disburse-ghost", "operating_cash", beneficiary, "compensation", 100, approverOne, approverTwo)); !errors.Is(err, domain.ErrUnknownCustody) {
		t.Fatalf("unopened origin = %v, want ErrUnknownCustody", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', 'operating_cash')`); err != nil {
		t.Fatalf("open empty vault: %v", err)
	}
	if _, err := useCase.Execute(ctx, disburseCmd("disburse-broke", "operating_cash", beneficiary, "compensation", 100, approverOne, approverTwo)); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("empty origin = %v, want ErrInsufficientMilliInk", err)
	}
	var legs, audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries`).Scan(&legs); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	if legs != 1 {
		t.Fatalf("legs = %d, want only the Genesis leg", legs)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.treasury_disbursements`).Scan(&audits); err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if audits != 0 {
		t.Fatalf("refused origins recorded %d audits", audits)
	}
	economySupply(t, ctx, pool)
}

// TestDisburseRefusesUnknownBeneficiary proves unknown accounts
// resolve to absence: the whole transaction rolls back and the
// journal keeps every unit.
func TestDisburseRefusesUnknownBeneficiary(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := disburseCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	_, approverOne, approverTwo := disburseParties(t, ctx, pool)
	useCase := application.NewDisburseUseCase(repo)

	if _, err := useCase.Execute(ctx, disburseCmd("disburse-ghost-payee",
		"operating_cash", "00000000-0000-0000-0000-000000000000", "compensation", 100, approverOne, approverTwo)); !errors.Is(err, domain.ErrUnknownCustody) {
		t.Fatalf("unknown beneficiary = %v, want ErrUnknownCustody", err)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "operating_cash"); got != 5000 {
		t.Fatalf("origin moved on refused payee: %d", got)
	}
	economySupply(t, ctx, pool)
}

// TestDisburseRefusesDivergentTerms proves a duplicated approval with
// different terms conflicts instead of paying twice.
func TestDisburseRefusesDivergentTerms(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := disburseCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)
	useCase := application.NewDisburseUseCase(repo)

	first, err := useCase.Execute(ctx, disburseCmd("disburse-terms", "operating_cash", beneficiary, "compensation", 1000, approverOne, approverTwo))
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	divergent := disburseCmd("disburse-terms", "operating_cash", beneficiary, "compensation", 2000, approverOne, approverTwo)
	if _, err := useCase.Execute(ctx, divergent); !errors.Is(err, domain.ErrIntentionConflict) {
		t.Fatalf("divergent terms = %v, want ErrIntentionConflict", err)
	}
	replayed, err := useCase.Execute(ctx, disburseCmd("disburse-terms", "operating_cash", beneficiary, "compensation", 1000, approverOne, approverTwo))
	if err != nil || !replayed.Replayed || replayed.TransferID != first.TransferID {
		t.Fatalf("original terms did not replay untouched: %+v, %v", replayed, err)
	}
	if got := custodyBalance(t, ctx, pool, "user", beneficiary); got != 1000 {
		t.Fatalf("beneficiary = %d, want 1000 once", got)
	}
	economySupply(t, ctx, pool)
}

// TestDisburseLeavesThirdPartiesUntouched proves the move is scoped:
// escrows and other vaults keep every unit while one vault pays out.
func TestDisburseLeavesThirdPartiesUntouched(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := disburseCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	moveToVault(t, ctx, pool, "commercial_stock", 700)
	seedCutEscrow(t, ctx, pool, 300)
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)

	mustDisburse(t, ctx, repo, disburseCmd("disburse-scoped", "operating_cash", beneficiary, "due_payment", 1000, approverOne, approverTwo))
	if got := custodyBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 700 {
		t.Fatalf("innocent vault = %d, want 700 untouched", got)
	}
	assertCutEscrow(t, ctx, pool, 300)
	economySupply(t, ctx, pool)
}

// TestDisburseFrozenRefuses proves the frozen book refuses the payment
// while reads continue.
func TestDisburseFrozenRefuses(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := disburseCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)
	makeCustody(t, ctx, pool, "user", beneficiary)
	freezeWithOrphan(t, ctx, pool, beneficiary)
	if _, err := repo.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	useCase := application.NewDisburseUseCase(repo)
	if _, err := useCase.Execute(ctx, disburseCmd("disburse-frozen", "operating_cash", beneficiary, "compensation", 100, approverOne, approverTwo)); !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("frozen disbursement = %v, want ErrEconomyFrozen", err)
	}
}
