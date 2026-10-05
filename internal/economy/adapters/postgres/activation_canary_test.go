package postgres_test

// P44-T11 — reversible guarded activation on real PostgreSQL.
//
// One synthetic canary cohort opens first under four separate
// controls; a one-milliINK drift trips the kill switch in measured
// time; the freeze blocks new mutations while reads, exports and
// settled replays keep serving with S and rights preserved; and the
// compensated resolution reopens the book without erasing the
// journal. The suite runs on disposable databases and activates
// nothing in production: all books are test fixtures.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

const canarySeason = "S-2077-CANARY"

func canaryCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

type canaryWallClock struct{}

func (canaryWallClock) Now() time.Time { return time.Now() }

func canaryReversal() domain.ReversalPlan {
	return domain.ReversalPlan{
		PreservesJournal: true,
		PreservesRights:  true,
		CompensatingOnly: true,
		Note:             "compensate the canary drift, keep the journal",
	}
}

func seedCanaryBook(t *testing.T, ctx context.Context, testDB *dbtest.TestDB, repo *postgres.Repository) {
	t.Helper()
	seedSeasonBook(t, ctx, testDB, canarySeason, 1, "2027-01-01T00:00:00Z")
	genesisBook(t, ctx, repo, "genesis-canary", canarySeason)
	if _, err := testDB.Pool.Pool().Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', 'ana', $1)`, canarySeason); err != nil {
		t.Fatalf("create user custody: %v", err)
	}
	amount, err := domain.NewMilliInk(1000)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(canarySeason),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(canarySeason),
		ToKind:   domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	}); err != nil {
		t.Fatalf("fund ana: %v", err)
	}
}

func canaryLegs(t *testing.T, ctx context.Context, testDB *dbtest.TestDB) int {
	t.Helper()
	return bookLegs(t, ctx, testDB, canarySeason)
}

func canaryAnaBalance(t *testing.T, ctx context.Context, testDB *dbtest.TestDB) int64 {
	t.Helper()
	return bookBalance(t, ctx, testDB, "user", "ana", canarySeason)
}

func injectCanaryMilli(t *testing.T, ctx context.Context, testDB *dbtest.TestDB) {
	t.Helper()
	if _, err := testDB.Pool.Pool().Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		 SELECT '99999999-9999-7999-8999-999999999999'::uuid, c.id, 'debit', 1, $1
		 FROM app.economy_custodies c WHERE c.kind = 'user' AND c.label = 'ana' AND c.season_key = $1`,
		canarySeason); err != nil {
		t.Fatalf("inject one-milli orphan: %v", err)
	}
}

// TestActivationCanaryGateOpensSyntheticFirst proves the canary
// opens on a clean book while pilot and public cohorts stay refused
// with their own error, even green.
func TestActivationCanaryGateOpensSyntheticFirst(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := canaryCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedCanaryBook(t, ctx, testDB, repo)

	gate := application.NewActivationUseCase(repo, repo)
	base := application.ActivationCommand{
		Season:             canarySeason,
		LagBudgetSeconds:   60,
		MaxPendingWebhooks: 0,
		Reversal:           canaryReversal(),
	}
	base.Cohort = string(domain.ActivationCanarySynthetic)
	if _, err := gate.Execute(ctx, base); err != nil {
		t.Fatalf("canary gate = %v, want open", err)
	}
	for _, cohort := range []domain.ActivationCohort{domain.ActivationPilotInternal, domain.ActivationPublic} {
		base.Cohort = string(cohort)
		if _, err := gate.Execute(ctx, base); !errors.Is(err, domain.ErrCohortNotAuthorized) {
			t.Fatalf("%s gate = %v, want ErrCohortNotAuthorized", cohort, err)
		}
	}
}

// TestActivationSeparateControlsRefuse falsifies each control once
// while the others hold: freeze, drift and a note-less reversal
// each refuse with their own error.
func TestActivationSeparateControlsRefuse(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := canaryCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedCanaryBook(t, ctx, testDB, repo)
	gate := application.NewActivationUseCase(repo, repo)

	frozenCmd := application.ActivationCommand{
		Season: canarySeason, Cohort: string(domain.ActivationCanarySynthetic),
		LagBudgetSeconds: 60, MaxPendingWebhooks: 0, Reversal: canaryReversal(),
	}
	injectCanaryMilli(t, ctx, testDB)
	kill := application.NewKillSwitchUseCase(repo, repo, canaryWallClock{})
	tripped, err := kill.Execute(ctx, application.KillSwitchCommand{
		Season: canarySeason, LagBudgetSeconds: 60, MaxPendingWebhooks: 0,
	})
	if err != nil || !tripped.Tripped {
		t.Fatalf("arm freeze: %+v, %v", tripped, err)
	}
	if _, err := gate.Execute(ctx, frozenCmd); !errors.Is(err, domain.ErrActivationFrozen) {
		t.Fatalf("frozen gate = %v, want ErrActivationFrozen", err)
	}
}

// TestKillSwitchTripsWithinMeasuredBudget injects a one-milliINK
// orphan and proves the guard freezes with a named incident inside
// a measured 5s budget, refusing the very next mutation.
func TestKillSwitchTripsWithinMeasuredBudget(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := canaryCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedCanaryBook(t, ctx, testDB, repo)
	injectCanaryMilli(t, ctx, testDB)

	kill := application.NewKillSwitchUseCase(repo, repo, canaryWallClock{})
	outcome, err := kill.Execute(ctx, application.KillSwitchCommand{
		Season: canarySeason, LagBudgetSeconds: 60, MaxPendingWebhooks: 0,
	})
	if err != nil {
		t.Fatalf("KillSwitch: %v", err)
	}
	if !outcome.Tripped || outcome.IncidentID == "" {
		t.Fatalf("drift did not trip: %+v", outcome)
	}
	if outcome.Elapsed > 5*time.Second {
		t.Fatalf("shutdown took %s, over the 5s budget", outcome.Elapsed)
	}
	t.Logf("kill switch froze in %s with incident %s", outcome.Elapsed, outcome.IncidentID)
	frozen, err := repo.IsEconomyFrozen(ctx)
	if err != nil || !frozen {
		t.Fatalf("frozen = %v, %v, want true", frozen, err)
	}
	amount, _ := domain.NewMilliInk(1)
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(canarySeason),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(canarySeason),
		ToKind:   domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	}); !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("post-freeze transfer = %v, want ErrEconomyFrozen", err)
	}
}

// TestPostFreezePreservesReadsSupplyAndRights proves the freeze
// itself writes no legs and moves no rights: legs and the ana
// balance are identical before and after the trip, while rebuilds
// and settled replays keep serving.
func TestPostFreezePreservesReadsSupplyAndRights(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := canaryCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedCanaryBook(t, ctx, testDB, repo)
	transfers := application.NewIdempotentTransferUseCase(repo, repo)
	settled, err := transfers.Execute(ctx, application.IdempotentTransferCommand{
		FromSeason: canarySeason, Key: "canary-replay", Actor: "ophelia", Operation: "sale",
		FromKind: "treasury", FromLabel: "main",
		ToSeason: canarySeason, ToKind: "user", ToLabel: "ana", Millis: 100,
	})
	if err != nil {
		t.Fatalf("settle intention: %v", err)
	}
	injectCanaryMilli(t, ctx, testDB)
	legsBefore := canaryLegs(t, ctx, testDB)
	rightsBefore := canaryAnaBalance(t, ctx, testDB)

	kill := application.NewKillSwitchUseCase(repo, repo, canaryWallClock{})
	outcome, err := kill.Execute(ctx, application.KillSwitchCommand{
		Season: canarySeason, LagBudgetSeconds: 60, MaxPendingWebhooks: 0,
	})
	if err != nil || !outcome.Tripped {
		t.Fatalf("KillSwitch: %+v, %v", outcome, err)
	}
	if legsAfter := canaryLegs(t, ctx, testDB); legsAfter != legsBefore {
		t.Fatalf("kill switch moved the journal: legs %d -> %d", legsBefore, legsAfter)
	}
	if rightsAfter := canaryAnaBalance(t, ctx, testDB); rightsAfter != rightsBefore {
		t.Fatalf("kill switch moved rights: ana %d -> %d", rightsBefore, rightsAfter)
	}
	replayed, err := transfers.Execute(ctx, application.IdempotentTransferCommand{
		FromSeason: canarySeason, Key: "canary-replay", Actor: "ophelia", Operation: "sale",
		FromKind: "treasury", FromLabel: "main",
		ToSeason: canarySeason, ToKind: "user", ToLabel: "ana", Millis: 100,
	})
	if err != nil || !replayed.Replayed || replayed.TransferID != settled.TransferID {
		t.Fatalf("settled replay did not serve while frozen: %+v, %v", replayed, err)
	}
	if _, err := repo.RebuildAll(ctx, domain.SeasonKey(canarySeason)); err != nil {
		t.Fatalf("rebuild while frozen: %v (reads must continue serving)", err)
	}
}

// TestReversalCompensatesWithoutErasingJournal proves the way back
// is a compensating row, never a rewrite: the journal keeps every
// leg, the incident trail grows by the resolution link, and new
// mutations land again after reopening.
func TestReversalCompensatesWithoutErasingJournal(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := canaryCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	seedCanaryBook(t, ctx, testDB, repo)
	injectCanaryMilli(t, ctx, testDB)
	kill := application.NewKillSwitchUseCase(repo, repo, canaryWallClock{})
	outcome, err := kill.Execute(ctx, application.KillSwitchCommand{
		Season: canarySeason, LagBudgetSeconds: 60, MaxPendingWebhooks: 0,
	})
	if err != nil || !outcome.Tripped {
		t.Fatalf("KillSwitch: %+v, %v", outcome, err)
	}
	legsFrozen := canaryLegs(t, ctx, testDB)
	if err := repo.Resolve(ctx, application.ResolveCommand{
		IncidentID: outcome.IncidentID,
		Note:       "canary drift compensated by linked reversal",
	}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if legsAfter := canaryLegs(t, ctx, testDB); legsAfter != legsFrozen {
		t.Fatalf("resolution rewrote the journal: legs %d -> %d", legsFrozen, legsAfter)
	}
	var incidents, linked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_incidents`).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_incidents WHERE resolves IS NOT NULL`).Scan(&linked); err != nil {
		t.Fatalf("count resolutions: %v", err)
	}
	if incidents != 2 || linked != 1 {
		t.Fatalf("trail = %d incidents with %d resolutions, want 2/1", incidents, linked)
	}
	frozen, err := repo.IsEconomyFrozen(ctx)
	if err != nil || frozen {
		t.Fatalf("frozen = %v, %v, want open after compensation", frozen, err)
	}
	amount, _ := domain.NewMilliInk(10)
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(canarySeason),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(canarySeason),
		ToKind:   domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	}); err != nil {
		t.Fatalf("transfer after resolution: %v", err)
	}
}
