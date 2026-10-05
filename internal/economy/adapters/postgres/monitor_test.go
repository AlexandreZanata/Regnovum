package postgres_test

// P44-T06 — continuous monetary monitoring on real PostgreSQL.
//
// One health pass judges supply, vaults, obligations, projections,
// stock, the webhook backlog, succession lag and the archive receipt
// of one season book: a one-milliINK drift freezes before the next
// mutation, a diverged archive blocks the successor, and lag or stale
// authority blocks real acts with a persisted alert while the book
// keeps moving. The suite runs on disposable databases and activates
// nothing.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

const monitorSeason = "S-2077-MON"

func monitorCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func monitorCommand() application.MonitorCommand {
	return application.MonitorCommand{
		Season:             monitorSeason,
		LagBudgetSeconds:   60,
		MaxPendingWebhooks: 0,
	}
}

// seedMonitorBook opens one season book with S in the Treasury and a
// funded user custody, using only the tested adapter.
func seedMonitorBook(t *testing.T, ctx context.Context, testDB *dbtest.TestDB, repo *postgres.Repository, pool *pgxpool.Pool) {
	t.Helper()
	seedSeasonBook(t, ctx, testDB, monitorSeason, 1, "2027-01-01T00:00:00Z")
	if _, err := repo.RunGenesis(ctx, application.GenesisRequest{
		Key:    mustTransferKey(t, "genesis-monitor"),
		Season: domain.SeasonKey(monitorSeason),
	}); err != nil {
		t.Fatalf("seed Genesis: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', 'ana', $1)`, monitorSeason); err != nil {
		t.Fatalf("create user custody: %v", err)
	}
	amount, err := domain.NewMilliInk(1000)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(monitorSeason),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(monitorSeason),
		ToKind:   domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	}); err != nil {
		t.Fatalf("fund ana: %v", err)
	}
}

// TestMonitorCleanBookIsGreen proves a conserved book passes green
// with no freeze and no incident rows: the pass reads, judges and
// writes nothing.
func TestMonitorCleanBookIsGreen(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := monitorCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedMonitorBook(t, ctx, testDB, repo, pool)

	useCase := application.NewMonitorUseCase(repo, repo)
	outcome, err := useCase.Execute(ctx, monitorCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !outcome.Report.Green() {
		t.Fatalf("clean book: %+v", outcome.Report)
	}
	if outcome.IncidentID != "" || len(outcome.AlertIDs) != 0 {
		t.Fatalf("green outcome names action: %+v", outcome)
	}
	var incidents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_incidents`).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if incidents != 0 {
		t.Fatalf("green pass wrote %d incidents", incidents)
	}
}

// TestMonitorOneMilliDeviationFreezesBeforeMutation injects a single
// milliINK orphan leg: the pass freezes with a named incident, and the
// very next transfer is refused with ErrEconomyFrozen.
func TestMonitorOneMilliDeviationFreezesBeforeMutation(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := monitorCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedMonitorBook(t, ctx, testDB, repo, pool)
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		 SELECT '99999999-9999-7999-8999-999999999999'::uuid, c.id, 'debit', 1, $1
		 FROM app.economy_custodies c WHERE c.kind = 'user' AND c.label = 'ana' AND c.season_key = $1`,
		monitorSeason); err != nil {
		t.Fatalf("inject one-milli orphan: %v", err)
	}

	useCase := application.NewMonitorUseCase(repo, repo)
	outcome, err := useCase.Execute(ctx, monitorCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Report.Green() || !outcome.Report.FreezeRequired {
		t.Fatalf("one-milli drift: %+v", outcome.Report)
	}
	if outcome.IncidentID == "" {
		t.Fatal("freeze names no incident")
	}
	found := false
	for _, finding := range outcome.Report.Findings {
		if finding.Severity == "critical" && finding.Owner == "tesouro" && finding.Runbook == "R3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings carry no owned critical: %+v", outcome.Report.Findings)
	}

	amount, err := domain.NewMilliInk(1)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	_, err = repo.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(monitorSeason),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(monitorSeason),
		ToKind:   domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	})
	if !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("post-freeze transfer = %v, want ErrEconomyFrozen", err)
	}
}

// TestMonitorDivergedArchiveBlocksSuccessor seals the book and files a
// receipt whose snapshot is one milli short of S: the pass freezes and
// blocks the successor, and the successor Genesis is refused.
func TestMonitorDivergedArchiveBlocksSuccessor(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := monitorCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedMonitorBook(t, ctx, testDB, repo, pool)
	sealSeasonBook(t, ctx, testDB, monitorSeason)
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.season_archives (season_key, cutoff_at, sealed_at, snapshot_milli, snapshot_legs, snapshot_intentions, manifest_hash)
		 VALUES ($1, now(), now(), $2, 3, 1, '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')`,
		monitorSeason, domain.GenesisSupplyMillis-1); err != nil {
		t.Fatalf("file diverged receipt: %v", err)
	}

	useCase := application.NewMonitorUseCase(repo, repo)
	outcome, err := useCase.Execute(ctx, monitorCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Report.Green() || !outcome.Report.FreezeRequired || !outcome.Report.BlocksSuccessor {
		t.Fatalf("diverged archive: %+v", outcome.Report)
	}

	_, err = repo.RunGenesis(ctx, application.GenesisRequest{
		Key:    mustTransferKey(t, "genesis-successor"),
		Season: domain.SeasonKey("S-2077-MON-NEXT"),
	})
	if !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("successor Genesis = %v, want ErrEconomyFrozen", err)
	}
}

// TestMonitorMissingArchiveReceiptFreezes proves a sealed book without
// any receipt freezes and blocks the successor: no receipt is not a
// clean archive.
func TestMonitorMissingArchiveReceiptFreezes(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := monitorCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedMonitorBook(t, ctx, testDB, repo, pool)
	sealSeasonBook(t, ctx, testDB, monitorSeason)

	useCase := application.NewMonitorUseCase(repo, repo)
	outcome, err := useCase.Execute(ctx, monitorCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Report.Green() || !outcome.Report.BlocksSuccessor {
		t.Fatalf("sealed book without receipt: %+v", outcome.Report)
	}
	if outcome.Report.Findings[0].Code != "archive-missing" {
		t.Fatalf("code = %q, want archive-missing", outcome.Report.Findings[0].Code)
	}
}

// TestMonitorLagBlocksRealActsWithoutFreezing leaves two unevaluated
// revisions behind a stale checkpoint: the pass blocks real acts and
// records its alert, while transfers keep moving — delay is judged,
// not frozen.
func TestMonitorLagBlocksRealActsWithoutFreezing(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := monitorCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedMonitorBook(t, ctx, testDB, repo, pool)
	for _, revision := range []int64{1, 2} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO app.seasonal_wealth_events (season_id, revision, subject_id, event_kind, delta_assets)
			 VALUES ($1, $2, 'ana', 'transfer', 1000)`, monitorSeason, revision); err != nil {
			t.Fatalf("seed outbox revision %d: %v", revision, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.seasonal_wealth_checkpoints (season_id, last_revision, checkpoint_hash, updated_at)
		 VALUES ($1, 0, 'lag', now() - make_interval(secs => 3600))`, monitorSeason); err != nil {
		t.Fatalf("seed stale checkpoint: %v", err)
	}

	useCase := application.NewMonitorUseCase(repo, repo)
	outcome, err := useCase.Execute(ctx, monitorCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Report.Green() || outcome.Report.FreezeRequired || !outcome.Report.BlocksRealActs {
		t.Fatalf("lagged succession: %+v", outcome.Report)
	}
	if len(outcome.AlertIDs) != 1 {
		t.Fatalf("alerts = %d, want exactly one persisted", len(outcome.AlertIDs))
	}

	amount, err := domain.NewMilliInk(1)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(monitorSeason),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(monitorSeason),
		ToKind:   domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	}); err != nil {
		t.Fatalf("transfer under lag alert = %v, want success (no over-freeze)", err)
	}
}

// TestMonitorPendingBacklogAlerts proves one unprocessed external
// event beyond a zero budget alerts with its owner and runbook while
// the book keeps moving.
func TestMonitorPendingBacklogAlerts(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := monitorCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedMonitorBook(t, ctx, testDB, repo, pool)
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.stripe_events (stripe_event_id, event_type, livemode, stripe_created_at, payload_sha256, payload_bytes, status)
		 VALUES ('evt_MON000000000000000001', 'checkout.session.completed', false, now(),
		         '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 128, 'received')`); err != nil {
		t.Fatalf("seed pending webhook: %v", err)
	}

	useCase := application.NewMonitorUseCase(repo, repo)
	outcome, err := useCase.Execute(ctx, monitorCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Report.Green() || outcome.Report.FreezeRequired {
		t.Fatalf("backlog: %+v", outcome.Report)
	}
	if len(outcome.Report.Findings) != 1 || outcome.Report.Findings[0].Code != "webhooks-backlog" {
		t.Fatalf("findings = %+v, want exactly webhooks-backlog", outcome.Report.Findings)
	}
	if len(outcome.AlertIDs) != 1 {
		t.Fatalf("alerts = %d, want exactly one persisted", len(outcome.AlertIDs))
	}
}
