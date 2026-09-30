package postgres_test

// P34-T01 — exclusive Treasury vaults on real PostgreSQL.
//
// The Genesis home plus the four vaults hold every Treasury unit
// exactly once: the use case reads each vault and the Treasury total
// through independent computations and reports any break instead of
// failing silent. The tests prove on a disposable database: funded
// vaults add up to the Treasury with S conserved, unknown vault labels
// die at the schema CHECK, and obligation escrows live in identified
// custodies outside the Treasury sum.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func treasuryCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func moveToVault(t *testing.T, ctx context.Context, pool *pgxpool.Pool, vault string, millis int64) {
	t.Helper()
	var treasury, target string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main'`).Scan(&treasury); err != nil {
		t.Fatalf("resolve treasury home: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', $1) ON CONFLICT DO NOTHING`, vault); err != nil {
		t.Fatalf("open vault %s: %v", vault, err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = $1`, vault).Scan(&target); err != nil {
		t.Fatalf("resolve vault %s: %v", vault, err)
	}
	var transfer string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $3), ($1::uuid, $4::uuid, 'credit', $3)`,
		transfer, treasury, millis, target); err != nil {
		t.Fatalf("fund vault %s: %v", vault, err)
	}
}

func vaultBalance(t *testing.T, report *application.TreasuryReport, vault domain.TreasuryVault) int64 {
	t.Helper()
	for _, position := range report.Vaults {
		if position.Vault == vault {
			return position.Millis
		}
	}
	t.Fatalf("vault %q missing from report", vault)
	return 0
}

// TestTreasuryVaultsSumToTreasury proves the happy path: funded vaults
// add up to the Treasury exactly, S is conserved and the report is
// clean.
func TestTreasuryVaultsSumToTreasury(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := treasuryCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "sovereign_reserve", 1000)
	moveToVault(t, ctx, pool, "commercial_stock", 2000)
	moveToVault(t, ctx, pool, "operating_cash", 500)

	report, err := application.NewTreasuryVaultsUseCase(repo).Execute(ctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(report.Mismatch) != 0 {
		t.Fatalf("Mismatch = %v, want clean", report.Mismatch)
	}
	if report.TotalMillis != domain.GenesisSupplyMillis {
		t.Fatalf("total = %d, want S", report.TotalMillis)
	}
	if got := vaultBalance(t, report, domain.TreasuryVaultGenesisHome); got != domain.GenesisSupplyMillis-3500 {
		t.Fatalf("genesis home = %d, want S-3500", got)
	}
	if got := vaultBalance(t, report, domain.TreasuryVaultSovereignReserve); got != 1000 {
		t.Fatalf("sovereign reserve = %d, want 1000", got)
	}
	if got := vaultBalance(t, report, domain.TreasuryVaultFree); got != 0 {
		t.Fatalf("free treasury = %d, want 0 (unfunded vaults read zero)", got)
	}
	economySupply(t, ctx, pool)
}

// TestTreasuryVaultLabelCheckRefusesUnknown proves the schema CHECK is
// the closed vocabulary: a sixth Treasury spelling dies at 23514, while
// the five vaults open.
func TestTreasuryVaultLabelCheckRefusesUnknown(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := treasuryCtx()
	defer cancel()

	_, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', 'slush')`)
	assertAccessCode(t, err, "23514")
	for _, vault := range []string{"main", "sovereign_reserve", "commercial_stock", "operating_cash", "free_treasury"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', $1) ON CONFLICT DO NOTHING`, vault); err != nil {
			t.Fatalf("open vault %s: %v", vault, err)
		}
	}
}

// TestObligationsStayInIdentifiedCustody proves obligations live outside
// the Treasury sum in their own named custody: carving an escrow out of
// the Treasury moves the total with it, the escrow stays addressable by
// kind and label, and the vault report stays clean.
func TestObligationsStayInIdentifiedCustody(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := treasuryCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	var treasury, escrow string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main'`).Scan(&treasury); err != nil {
		t.Fatalf("resolve treasury home: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('escrow', 'case-7')`); err != nil {
		t.Fatalf("open escrow: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'escrow' AND label = 'case-7'`).Scan(&escrow); err != nil {
		t.Fatalf("resolve escrow: %v", err)
	}
	var transfer string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', 300), ($1::uuid, $3::uuid, 'credit', 300)`,
		transfer, treasury, escrow); err != nil {
		t.Fatalf("carve escrow: %v", err)
	}

	report, err := application.NewTreasuryVaultsUseCase(repo).Execute(ctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(report.Mismatch) != 0 {
		t.Fatalf("Mismatch = %v, want clean", report.Mismatch)
	}
	if report.TotalMillis != domain.GenesisSupplyMillis-300 {
		t.Fatalf("total = %d, want S-300 (escrow carved out, never double-counted)", report.TotalMillis)
	}
	var escrowBalance int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries WHERE custody_id = $1::uuid`, escrow).Scan(&escrowBalance); err != nil {
		t.Fatalf("escrow balance: %v", err)
	}
	if escrowBalance != 300 {
		t.Fatalf("escrow = %d, want 300 in its identified custody", escrowBalance)
	}
	economySupply(t, ctx, pool)
}
