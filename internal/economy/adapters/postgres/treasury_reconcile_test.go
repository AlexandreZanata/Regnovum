package postgres_test

// P34-T07 — vault-scoped treasury reconciliation on real PostgreSQL.
//
// The Treasury holds every unit exactly once across vaults, holds and
// disbursements: the journal, the vault partitions, the active holds,
// the disbursement audits and the sellable projection must agree for
// the period, and any divergence freezes disbursement and sale with a
// named incident while reads stay live. The tests prove on a
// disposable database: a clean vault binds all five readings with S
// conserved and the book open, and each of the three deviations — a
// missing obligation leg, a doubled reserve under one transfer and an
// overpromised vault — is detected, frozen and recorded, with vault
// mutations refused and reads still serving.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// treconIncident proves the freeze recorded exactly one
// conservation-break incident: the audit the resolution addresses.
func treconIncident(t *testing.T, ctx context.Context, pool *pgxpool.Pool, incidentID string) {
	t.Helper()
	var reason string
	if err := pool.QueryRow(ctx, `SELECT reason FROM app.economy_incidents WHERE id = $1::uuid`, incidentID).Scan(&reason); err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if reason != "conservation-break" {
		t.Fatalf("incident reason = %q, want conservation-break", reason)
	}
	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_incidents`).Scan(&total); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if total != 1 {
		t.Fatalf("incidents = %d, want exactly 1", total)
	}
}

// treconReconcileFreezes runs one pass and proves it froze with a
// named incident instead of failing silent.
func treconReconcileFreezes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repo *postgres.Repository) *application.ReconciliationReport {
	t.Helper()
	report, err := repo.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !report.Frozen || report.IncidentID == "" || len(report.Mismatch) == 0 {
		t.Fatalf("deviated book stayed open: %+v", report)
	}
	treconIncident(t, ctx, pool, report.IncidentID)
	return report
}

// treconMutationsRefused proves the divergence froze the vault: a
// covered disbursement and a covered commitment both refuse with the
// freeze instead of moving value.
func treconMutationsRefused(t *testing.T, ctx context.Context, repo *postgres.Repository, vault, beneficiary, approverOne, approverTwo string) {
	t.Helper()
	if _, err := application.NewDisburseUseCase(repo).Execute(ctx,
		disburseCmd("trecon-frozen-disburse", vault, beneficiary, "due_payment", 100, approverOne, approverTwo)); !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("disbursement while frozen = %v, want ErrEconomyFrozen", err)
	}
	commitUseCase := application.NewCommitFundsUseCase(repo, fixedHoldClock{now: commitNow()})
	if _, err := commitUseCase.Execute(ctx, application.CommitFundsCommand{
		Vault: vault, Purpose: "frozen probe", Millis: 100, ExpiresAt: commitNow().Add(time.Hour),
	}); !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("commitment while frozen = %v, want ErrEconomyFrozen", err)
	}
}

// treconReadsStayLive proves the freeze stops mutations without
// killing the vault readings the audit needs.
func treconReadsStayLive(t *testing.T, ctx context.Context, repo *postgres.Repository, vault string) {
	t.Helper()
	if got := stockReport(t, ctx, repo, vault); got.Available != got.Balance {
		t.Fatalf("stock projection broke while frozen: %+v", got)
	}
	if _, err := application.NewTreasuryVaultsUseCase(repo).Execute(ctx); err != nil {
		t.Fatalf("vault reading while frozen: %v (reads must continue serving)", err)
	}
}

// TestTreasuryReconciliationBindsEveryVault proves the clean path: one
// funded vault with a period hold and one disbursement binds the five
// readings — journal supply, vault partitions, active holds,
// disbursement audit and sellable projection — with S conserved and
// the book open.
func TestTreasuryReconciliationBindsEveryVault(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reconcileCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	moveToVault(t, ctx, pool, "commercial_stock", 2000)
	hold := commitFunds(t, ctx, repo, "operating_cash", "accepted sale #1", 1500, commitNow().Add(time.Hour))
	if !hold.ExpiresAt.Equal(commitNow().Add(time.Hour)) {
		t.Fatalf("hold period = %v, want the committed hour", hold.ExpiresAt)
	}
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)
	if _, err := application.NewDisburseUseCase(repo).Execute(ctx,
		disburseCmd("trecon-clean-sale", "operating_cash", beneficiary, "due_payment", 1000, approverOne, approverTwo)); err != nil {
		t.Fatalf("settle disbursement: %v", err)
	}

	report, err := repo.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.Frozen || len(report.Mismatch) != 0 || report.IncidentID != "" {
		t.Fatalf("clean vault reported %+v", report)
	}
	if report.SupplyMillis != domain.GenesisSupplyMillis {
		t.Fatalf("supply = %d, want S", report.SupplyMillis)
	}

	vaults, err := application.NewTreasuryVaultsUseCase(repo).Execute(ctx)
	if err != nil {
		t.Fatalf("vault partitions: %v", err)
	}
	if len(vaults.Mismatch) != 0 {
		t.Fatalf("partitions mismatch: %v", vaults.Mismatch)
	}
	balances := map[domain.TreasuryVault]int64{}
	for _, position := range vaults.Vaults {
		balances[position.Vault] = position.Millis
	}
	want := map[domain.TreasuryVault]int64{
		domain.TreasuryVaultGenesisHome:      domain.GenesisSupplyMillis - 7000,
		domain.TreasuryVaultSovereignReserve: 0,
		domain.TreasuryVaultCommercialStock:  2000,
		domain.TreasuryVaultOperatingCash:    2500,
		domain.TreasuryVaultFree:             0,
	}
	for vault, millis := range want {
		if balances[vault] != millis {
			t.Fatalf("vault %q = %d, want %d", vault, balances[vault], millis)
		}
	}
	if vaults.TotalMillis != domain.GenesisSupplyMillis-2500 {
		t.Fatalf("treasury total = %d, want S-2500: 1500 sits in the hold escrow and 1000 left with the beneficiary, both outside the Treasury", vaults.TotalMillis)
	}

	if got := committedTotal(t, ctx, pool); got != 1500 {
		t.Fatalf("active holds = %d, want 1500 for the period", got)
	}
	var origin, purpose string
	var millis int64
	if err := pool.QueryRow(ctx,
		`SELECT origin_vault, purpose, amount_milli FROM app.treasury_disbursements WHERE disbursement_key = 'trecon-clean-sale'`).Scan(
		&origin, &purpose, &millis); err != nil {
		t.Fatalf("disbursement audit: %v", err)
	}
	if origin != "operating_cash" || purpose != "due_payment" || millis != 1000 {
		t.Fatalf("audit = %s/%s/%d, want the settled obligation", origin, purpose, millis)
	}

	projection := stockReport(t, ctx, repo, "operating_cash")
	if projection.Balance != 2500 || projection.Committed != 1500 || projection.Available != 2500 {
		t.Fatalf("projection = %+v, want 2500/1500/2500", projection)
	}
	if _, err := checkSale(t, ctx, repo, "operating_cash", 2500); err != nil {
		t.Fatalf("exact sale against projection: %v", err)
	}
	if _, err := checkSale(t, ctx, repo, "operating_cash", 2501); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("oversale = %v, want ErrInsufficientMilliInk", err)
	}
	var incidents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_incidents`).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if incidents != 0 {
		t.Fatalf("clean reconcile wrote %d incidents", incidents)
	}
	economySupply(t, ctx, pool)
}

// TestTreasuryReconciliationFreezesOnMissingObligation proves the
// first deviation: an obligation leg written without its counterpart
// is named as unpaired, freezes the book with an incident, refuses
// vault mutations and keeps reads live.
func TestTreasuryReconciliationFreezesOnMissingObligation(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reconcileCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	commitFunds(t, ctx, repo, "operating_cash", "accepted sale #1", 1500, commitNow().Add(time.Hour))
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)

	missing := "77777777-7777-7999-8999-777777777777"
	var vaultID string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'operating_cash'`).Scan(&vaultID); err != nil {
		t.Fatalf("resolve vault: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', 500)`, missing, vaultID); err != nil {
		t.Fatalf("inject missing obligation leg: %v", err)
	}

	report := treconReconcileFreezes(t, ctx, pool, repo)
	found := false
	for _, transfer := range report.Unpaired {
		if transfer == missing {
			found = true
		}
	}
	if !found {
		t.Fatalf("report does not name the missing obligation: %+v", report.Unpaired)
	}
	treconMutationsRefused(t, ctx, repo, "operating_cash", beneficiary, approverOne, approverTwo)
	treconReadsStayLive(t, ctx, repo, "operating_cash")
}

// TestTreasuryReconciliationFreezesOnDoubleReserve proves the second
// deviation: one reserve act debited twice — its own vault and another
// vault under the same transfer. The schema already refuses the same
// custody twice per transfer, so the double lands on a second vault;
// the transfer tallies two debits against one credit, the duplicate is
// named, the book freezes with an incident and vault mutations stop.
func TestTreasuryReconciliationFreezesOnDoubleReserve(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reconcileCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "commercial_stock", 2000)
	moveToVault(t, ctx, pool, "operating_cash", 3000)
	hold := commitFunds(t, ctx, repo, "commercial_stock", "accepted sale #1", 600, commitNow().Add(time.Hour))
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)

	var transfer string
	if err := pool.QueryRow(ctx,
		`SELECT transfer_id::text FROM app.economy_entries WHERE custody_id = $1::uuid AND direction = 'credit'`,
		hold.HoldCustodyID).Scan(&transfer); err != nil {
		t.Fatalf("resolve reserve transfer: %v", err)
	}
	var foreignVault string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'operating_cash'`).Scan(&foreignVault); err != nil {
		t.Fatalf("resolve foreign vault: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', 600)`, transfer, foreignVault); err != nil {
		t.Fatalf("debit the reserve act twice: %v", err)
	}
	var debits int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_entries WHERE transfer_id = $1::uuid AND direction = 'debit'`, transfer).Scan(&debits); err != nil {
		t.Fatalf("count reserve debits: %v", err)
	}
	if debits != 2 {
		t.Fatalf("reserve debits = %d, want 2 proving the act paid twice", debits)
	}

	report := treconReconcileFreezes(t, ctx, pool, repo)
	found := false
	for _, unpaired := range report.Unpaired {
		if unpaired == transfer {
			found = true
		}
	}
	if !found {
		t.Fatalf("report does not name the doubled reserve: %+v", report.Unpaired)
	}
	treconMutationsRefused(t, ctx, repo, "commercial_stock", beneficiary, approverOne, approverTwo)
	treconReadsStayLive(t, ctx, repo, "commercial_stock")
}

// TestTreasuryReconciliationFreezesOnOverpromisedVault proves the
// third deviation: a promise settled beyond the vault balance keeps
// every transfer paired but drives the vault negative, so the vault
// is named, the book freezes with an incident and vault mutations
// stop.
func TestTreasuryReconciliationFreezesOnOverpromisedVault(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reconcileCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "operating_cash", 5000)
	moveToVault(t, ctx, pool, "commercial_stock", 2000)
	beneficiary, approverOne, approverTwo := disburseParties(t, ctx, pool)
	makeCustody(t, ctx, pool, "user", "overpromised-payee")

	var vaultID string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'operating_cash'`).Scan(&vaultID); err != nil {
		t.Fatalf("resolve vault: %v", err)
	}
	var payeeID string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'user' AND label = 'overpromised-payee'`).Scan(&payeeID); err != nil {
		t.Fatalf("resolve payee: %v", err)
	}
	var overpromise string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&overpromise); err != nil {
		t.Fatalf("overpromise id: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', 6000), ($1::uuid, $3::uuid, 'credit', 6000)`,
		overpromise, vaultID, payeeID); err != nil {
		t.Fatalf("settle overpromise: %v", err)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "operating_cash"); got != -1000 {
		t.Fatalf("vault = %d, want -1000 promised beyond 5000", got)
	}

	report := treconReconcileFreezes(t, ctx, pool, repo)
	named := false
	for _, mismatch := range report.Mismatch {
		if strings.HasPrefix(mismatch, "negative custody treasury/operating_cash") {
			named = true
		}
	}
	if !named {
		t.Fatalf("report does not name the overpromised vault: %+v", report.Mismatch)
	}
	if len(report.Unpaired) != 0 {
		t.Fatalf("overpromise paired every leg, want no unpaired: %+v", report.Unpaired)
	}
	treconMutationsRefused(t, ctx, repo, "commercial_stock", beneficiary, approverOne, approverTwo)
	treconReadsStayLive(t, ctx, repo, "commercial_stock")
}
