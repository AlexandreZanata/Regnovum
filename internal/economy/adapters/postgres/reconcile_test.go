package postgres_test

// P32-T08 — reconciliation with audit-trailed freeze on real PostgreSQL.
//
// The reconciler recomputes supply, custody positions and leg pairing
// without writing on clean books, and freezes with an incident on any
// divergence. While frozen every mutation is refused with
// ErrEconomyFrozen and every read keeps serving; only a compensated
// resolution recorded against the freezing incident reopens the book.
// The tests prove on a disposable database: clean pass-through, orphan
// freeze with blocked mutations and live reads, negative-balance freeze,
// and compensated resolution with bogus refusals.

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

func reconcileCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func settleTreasuryGrant(t *testing.T, ctx context.Context, repo *postgres.Repository, pool *pgxpool.Pool, label string, millis int64) {
	t.Helper()
	if _, err := repo.RunGenesis(ctx, application.GenesisRequest{Key: mustTransferKey(t, "genesis-reconcile")}); err != nil {
		t.Fatalf("seed Genesis: %v", err)
	}
	makeCustody(t, ctx, pool, "user", label)
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		t.Fatalf("NewMilliInk(%d): %v", millis, err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromKind: domain.CustodyTreasury, FromLabel: "main",
		ToKind: domain.CustodyUser, ToLabel: label,
		Amount: amount,
	}); err != nil {
		t.Fatalf("fund %s: %v", label, err)
	}
}

func freezeWithOrphan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) string {
	t.Helper()
	orphan := "99999999-9999-7999-8999-999999999999"
	var custody string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'user' AND label = $1`, label).Scan(&custody); err != nil {
		t.Fatalf("resolve %s: %v", label, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', 500)`, orphan, custody); err != nil {
		t.Fatalf("inject orphan leg: %v", err)
	}
	return orphan
}

// TestReconcileCleanBookStaysOpen proves a conserved book reports no
// mismatch, names no incident and keeps accepting mutations.
func TestReconcileCleanBookStaysOpen(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reconcileCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	settleTreasuryGrant(t, ctx, repo, pool, "ana", 1000)

	report, err := repo.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.Frozen || len(report.Mismatch) != 0 || len(report.Unpaired) != 0 {
		t.Fatalf("clean book reported %+v", report)
	}
	if report.SupplyMillis != domain.GenesisSupplyMillis {
		t.Fatalf("supply = %d, want S", report.SupplyMillis)
	}
	if report.IncidentID != "" {
		t.Fatalf("clean book names incident %q", report.IncidentID)
	}
	var incidents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_incidents`).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if incidents != 0 {
		t.Fatalf("clean reconcile wrote %d incidents", incidents)
	}
}

// TestReconcileFreezesOnOrphanLeg proves an unpaired leg freezes the
// book with a named incident: the sealed pre-injection projection
// diverges, mutations stop, reads continue.
func TestReconcileFreezesOnOrphanLeg(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reconcileCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	settleTreasuryGrant(t, ctx, repo, pool, "ana", 1000)
	sealed, err := repo.RebuildAll(ctx)
	if err != nil {
		t.Fatalf("RebuildAll: %v", err)
	}
	var before int64
	for _, projection := range sealed {
		if string(projection.Kind) == "user" && projection.Label == "ana" {
			before = projection.Balance.Millis()
		}
	}

	orphan := freezeWithOrphan(t, ctx, pool, "ana")
	report, err := repo.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !report.Frozen || report.IncidentID == "" {
		t.Fatalf("orphan book stayed open: %+v", report)
	}
	found := false
	for _, transfer := range report.Unpaired {
		if transfer == orphan {
			found = true
		}
	}
	if !found {
		t.Fatalf("report does not name the orphan transfer: %+v", report.Unpaired)
	}
	rebuilt, err := repo.RebuildAll(ctx)
	if err != nil {
		t.Fatalf("RebuildAll: %v", err)
	}
	for _, projection := range rebuilt {
		if string(projection.Kind) == "user" && projection.Label == "ana" && projection.Balance.Millis() == before {
			t.Fatalf("ana projection did not diverge after the orphan injection")
		}
	}

	amount, _ := domain.NewMilliInk(10)
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromKind: domain.CustodyTreasury, FromLabel: "main",
		ToKind: domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	}); !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("transfer while frozen = %v, want ErrEconomyFrozen", err)
	}
	if _, err := repo.Reserve(ctx, domain.CustodyTreasury, "main", mustHoldPurpose(t, "frozen probe"), amount, time.Now().UTC().Add(time.Hour)); !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("reserve while frozen = %v, want ErrEconomyFrozen", err)
	}
	if _, err := repo.ReadStatement(ctx, application.StatementRequest{
		Kind: domain.CustodyUser, Label: "ana", CallerAccountID: "nobody", Limit: 10,
	}); err == nil {
		t.Fatalf("statement read failed while frozen: reads must continue serving")
	} else if errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("statement read refused as frozen: reads must continue serving")
	}
	if _, err := repo.RebuildAll(ctx); err != nil {
		t.Fatalf("rebuild while frozen: %v (reads must continue serving)", err)
	}
}

// TestReconcileFreezesOnNegativeBalance proves an adulterated custody
// position freezes the book even when every transfer is paired.
func TestReconcileFreezesOnNegativeBalance(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reconcileCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	settleTreasuryGrant(t, ctx, repo, pool, "ana", 1000)
	makeCustody(t, ctx, pool, "user", "mallory")
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 SELECT gen_random_uuid(), id, 'debit', 999999 FROM app.economy_custodies
		 WHERE kind = 'user' AND label = 'mallory'`); err != nil {
		t.Fatalf("inject oversized debit: %v", err)
	}
	report, err := repo.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !report.Frozen {
		t.Fatalf("negative custody stayed open: %+v", report)
	}
	named := false
	for _, mismatch := range report.Mismatch {
		if strings.HasPrefix(mismatch, "negative custody ") {
			named = true
		}
	}
	if !named {
		t.Fatalf("report does not name the negative custody: %+v", report.Mismatch)
	}
}

// TestResolveRequiresAuditedCompensation proves only the freezing
// incident with a recorded compensation reopens the book: bogus ids
// and already-resolved breaks are refused, and transfers land again
// after the compensated resolution.
func TestResolveRequiresAuditedCompensation(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reconcileCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	settleTreasuryGrant(t, ctx, repo, pool, "ana", 1000)
	freezeWithOrphan(t, ctx, pool, "ana")
	report, err := repo.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !report.Frozen {
		t.Fatalf("book stayed open without a break to resolve")
	}

	if err := repo.Resolve(ctx, application.ResolveCommand{IncidentID: "00000000-0000-7000-8000-000000000000", Note: "bogus"}); !errors.Is(err, domain.ErrIncidentNotFound) {
		t.Fatalf("bogus resolve = %v, want ErrIncidentNotFound", err)
	}
	if frozen, _ := frozenNow(t, ctx, pool); !frozen {
		t.Fatalf("book reopened on a bogus resolution")
	}
	if err := repo.Resolve(ctx, application.ResolveCommand{IncidentID: report.IncidentID, Note: "orphan leg compensated by linked reversal"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if frozen, _ := frozenNow(t, ctx, pool); frozen {
		t.Fatalf("book stayed frozen after compensated resolution")
	}
	if err := repo.Resolve(ctx, application.ResolveCommand{IncidentID: report.IncidentID, Note: "again"}); !errors.Is(err, domain.ErrIncidentNotFound) {
		t.Fatalf("second resolve = %v, want ErrIncidentNotFound", err)
	}
	amount, _ := domain.NewMilliInk(10)
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromKind: domain.CustodyTreasury, FromLabel: "main",
		ToKind: domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	}); err != nil {
		t.Fatalf("transfer after resolution: %v", err)
	}
}

func frozenNow(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	t.Helper()
	var frozen bool
	err := pool.QueryRow(ctx, `SELECT frozen FROM app.economy_mode`).Scan(&frozen)
	return frozen, err
}

func mustHoldPurpose(t *testing.T, purpose string) domain.HoldPurpose {
	t.Helper()
	parsed, err := domain.ParseHoldPurpose(purpose)
	if err != nil {
		t.Fatalf("ParseHoldPurpose(%q): %v", purpose, err)
	}
	return parsed
}
