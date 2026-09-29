package postgres_test

// P34-T06 — third-party restitution through the single disbursement
// port, on real PostgreSQL.
//
// A platform error pays its reparation from available Treasury stock
// or not at all: every restitution settles as a compensation through
// the governed disbursement port with its origin vault, beneficiary
// and unique reference, and fiat settlements never reach the INK
// journal. The tests prove on a disposable database: the three
// platform-error scenarios (broken contract, paying suspension,
// chargeback) each settle with origin/beneficiary/reference, a replay
// resolves without duplicating, escrows and innocent vaults keep every
// unit and the frozen book refuses.

import (
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func restituteCmd(key, vault, beneficiary, cause string, millis int64, approverOne, approverTwo string) application.RestituteCommand {
	return application.RestituteCommand{
		Key: key, Vault: vault, Beneficiary: beneficiary, Cause: cause,
		Millis: millis, ApproverOne: approverOne, ApproverTwo: approverTwo,
	}
}

func mustRestitute(t *testing.T, repo *postgres.Repository, cmd application.RestituteCommand) *application.RestituteResult {
	t.Helper()
	ctx, cancel := disburseCtx()
	defer cancel()
	result, err := application.NewRestituteUseCase(repo).Execute(ctx, cmd)
	if err != nil {
		t.Fatalf("Restitute(%s): %v", cmd.Cause, err)
	}
	return result
}

// TestRestitutionSettlesBrokenContract proves the first scenario: 1000
// leaves operating cash for the wronged holder, the audit row carries
// origin, beneficiary and the unique reference beside the transfer,
// and S is conserved.
func TestRestitutionSettlesBrokenContract(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := disburseCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)

	result := mustRestitute(t, repo, restituteCmd("restitution-broken", "operating_cash", beneficiary, "broken_contract", 1000, approverOne, approverTwo))
	if result.Paid.Millis() != 1000 || result.Replayed || result.Cause.String() != "broken_contract" {
		t.Fatalf("result = %+v, want 1000 fresh broken_contract", result)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "operating_cash"); got != 4000 {
		t.Fatalf("origin = %d, want 4000", got)
	}
	if got := custodyBalance(t, ctx, pool, "user", beneficiary); got != 1000 {
		t.Fatalf("beneficiary = %d, want 1000", got)
	}
	var vault, purpose, payee, key, transfer string
	var millis int64
	if err := pool.QueryRow(ctx,
		`SELECT origin_vault, purpose, beneficiary_account_id::text, disbursement_key, transfer_id::text, amount_milli
		 FROM app.treasury_disbursements WHERE disbursement_key = 'restitution-broken'`).Scan(
		&vault, &purpose, &payee, &key, &transfer, &millis); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if vault != "operating_cash" || purpose != "compensation" || payee != beneficiary || key != "restitution-broken" {
		t.Fatalf("audit = %s/%s/%s/%s, want origin+beneficiary+reference as compensation", vault, purpose, payee, key)
	}
	if transfer != result.TransferID || millis != 1000 {
		t.Fatalf("audit transfer = %s/%d, want the settling transfer with 1000", transfer, millis)
	}
	economySupply(t, ctx, pool)
}

// TestRestitutionSettlesPayingSuspension proves the second scenario: a
// suspension that kept charging is repaired from available stock with
// its own unique reference.
func TestRestitutionSettlesPayingSuspension(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := disburseCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)

	result := mustRestitute(t, repo, restituteCmd("restitution-suspension", "operating_cash", beneficiary, "paying_suspension", 1500, approverOne, approverTwo))
	if result.Paid.Millis() != 1500 || result.Cause.String() != "paying_suspension" {
		t.Fatalf("result = %+v, want 1500 paying_suspension", result)
	}
	if got := custodyBalance(t, ctx, pool, "user", beneficiary); got != 1500 {
		t.Fatalf("beneficiary = %d, want 1500", got)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.treasury_disbursements WHERE disbursement_key = 'restitution-suspension'`).Scan(&count); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("audit rows = %d, want exactly 1 reference", count)
	}
	economySupply(t, ctx, pool)
}

// TestRestitutionSettlesChargeback proves the third scenario: a
// contested charge is repaired without debiting any third party.
func TestRestitutionSettlesChargeback(t *testing.T) {
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

	result := mustRestitute(t, repo, restituteCmd("restitution-chargeback", "operating_cash", beneficiary, "chargeback", 800, approverOne, approverTwo))
	if result.Paid.Millis() != 800 || result.Cause.String() != "chargeback" {
		t.Fatalf("result = %+v, want 800 chargeback", result)
	}
	if got := custodyBalance(t, ctx, pool, "user", beneficiary); got != 800 {
		t.Fatalf("beneficiary = %d, want 800", got)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 700 {
		t.Fatalf("innocent vault = %d, want 700 untouched", got)
	}
	assertCutEscrow(t, ctx, pool, 300)
	economySupply(t, ctx, pool)
}

// TestRestitutionReplaysWithoutDuplicating proves the reference is
// unique: the second approval of one act resolves the original
// transfer with zero new legs and zero new audit rows.
func TestRestitutionReplaysWithoutDuplicating(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := disburseCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)
	useCase := application.NewRestituteUseCase(repo)
	cmd := restituteCmd("restitution-replay", "operating_cash", beneficiary, "broken_contract", 1200, approverOne, approverTwo)

	first, err := useCase.Execute(ctx, cmd)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	second, err := useCase.Execute(ctx, cmd)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !second.Replayed || second.TransferID != first.TransferID {
		t.Fatalf("replay settled another payment: %+v vs %+v", second, first)
	}
	if got := custodyBalance(t, ctx, pool, "user", beneficiary); got != 1200 {
		t.Fatalf("beneficiary = %d, want 1200 once", got)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.treasury_disbursements WHERE disbursement_key = 'restitution-replay'`).Scan(&rows); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", rows)
	}
	economySupply(t, ctx, pool)
}

// TestRestitutionLeavesEscrowUntouched proves a platform decision never
// covers itself with third-party money: escrows and other vaults keep
// every unit while one vault pays the reparation.
func TestRestitutionLeavesEscrowUntouched(t *testing.T) {
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

	mustRestitute(t, repo, restituteCmd("restitution-scoped", "operating_cash", beneficiary, "paying_suspension", 1000, approverOne, approverTwo))
	if got := custodyBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 700 {
		t.Fatalf("innocent vault = %d, want 700 untouched", got)
	}
	assertCutEscrow(t, ctx, pool, 300)
	economySupply(t, ctx, pool)
}

// TestRestitutionFrozenRefuses proves the frozen book refuses the
// reparation while reads continue.
func TestRestitutionFrozenRefuses(t *testing.T) {
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
	useCase := application.NewRestituteUseCase(repo)
	if _, err := useCase.Execute(ctx, restituteCmd("restitution-frozen", "operating_cash", beneficiary, "chargeback", 100, approverOne, approverTwo)); !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("frozen restitution = %v, want ErrEconomyFrozen", err)
	}
}
