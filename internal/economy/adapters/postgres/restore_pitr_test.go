package postgres_test

// P44-T07 — financial restore and replay safety on real PostgreSQL.
//
// The harness takes a physical base backup (pg_dump, custom format)
// of a disposable cluster through the same container the tests
// already require, restores it into an empty database, and judges the
// result: per-season and per-reign snapshots before and after,
// declared RPO/RTO, no Genesis/cutoff/succession/transfer reapplied in
// duplicate, simulated corruption blocking resumption, and a restore
// that neither spends the archived book nor crowns the ex-King. The
// WAL-based PITR drill stays with backup-verify and P45; this sample
// proves the snapshot point returns byte-equal and replays are safe.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	crowndomain "github.com/AlexandreZanata/Regnovum/internal/crown/domain"
	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

const (
	pitrActiveBook = "S-2077-PITR-A"
	pitrSealedBook = "S-2077-PITR-B"
	// pitrRTOBudget is the declared recovery budget of the directed
	// sample: dump plus restore of two small books must land inside
	// two minutes on the disposable cluster. The integral drill
	// measures production RTO in P45; this budget only gates the
	// harness itself against hanging.
	pitrRTOBudget = 120 * time.Second
	pitrDumpHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func pitrCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Minute)
}

// pitrContainer resolves the disposable PostgreSQL container through
// the port the test database DSN publishes. The harness needs the
// container because pg_dump and pg_restore run where the server
// lives; without it the test refuses instead of pretending.
func pitrContainer(t *testing.T, dsn string) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("pitr harness requires the docker CLI: the disposable cluster is a container")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
	}
	port := parsed.Port()
	if port == "" {
		t.Fatal("test DSN names no port: the container cannot be resolved")
	}
	out, err := exec.Command("docker", "ps", "--filter", "publish="+port, "--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	name := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	if name == "" {
		t.Fatalf("no container publishes %s: start the disposable cluster first", port)
	}
	return name
}

func pitrExec(t *testing.T, container string, args ...string) {
	t.Helper()
	full := append([]string{"exec", container}, args...)
	if out, err := exec.Command("docker", full...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

// pitrSeed builds the pre-disaster world: an active book with an
// idempotent settlement, and a sealed book with an archive receipt
// and a finished succession from Alice to Bob.
func pitrSeed(t *testing.T, ctx context.Context, testDB *dbtest.TestDB, repo *postgres.Repository) {
	t.Helper()
	pool := testDB.Pool.Pool()
	seedSeasonBook(t, ctx, testDB, pitrActiveBook, 1, "2027-01-01T00:00:00Z")
	seedSeasonBook(t, ctx, testDB, pitrSealedBook, 2, "2027-04-01T00:00:00Z")
	genesisBook(t, ctx, repo, "genesis-pitr-a", pitrActiveBook)
	genesisBook(t, ctx, repo, "genesis-pitr-b", pitrSealedBook)
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', 'ana', $1)`, pitrActiveBook); err != nil {
		t.Fatalf("create user custody: %v", err)
	}
	if _, err := repo.TransferIdempotent(ctx, pitrIntention("pitr-intent-1", 1000)); err != nil {
		t.Fatalf("settle intention: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', 'ana', $1)`, pitrSealedBook); err != nil {
		t.Fatalf("create sealed custody: %v", err)
	}
	amount, err := domain.NewMilliInk(500)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(pitrSealedBook),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(pitrSealedBook),
		ToKind:   domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	}); err != nil {
		t.Fatalf("fund sealed book: %v", err)
	}
	sealSeasonBook(t, ctx, testDB, pitrSealedBook)
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.season_archives (season_key, cutoff_at, sealed_at, snapshot_milli, snapshot_legs, snapshot_intentions, manifest_hash)
		 VALUES ($1, now(), now(), $2, 3, 0, $3)`,
		pitrSealedBook, domain.GenesisSupplyMillis, pitrDumpHash); err != nil {
		t.Fatalf("file archive receipt: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.seasonal_reigns (season_id, reign_version, holder_subject, is_regent, reason, is_active, ended_at)
		 VALUES ($1, 1, 'alice', false, 'conquest', false, now()),
		        ($1, 2, 'bob', false, 'conquest', true, NULL)`, pitrSealedBook); err != nil {
		t.Fatalf("seed succession: %v", err)
	}
}

func pitrIntention(key string, millis int64) application.IdempotentTransferRequest {
	intentionKey, err := domain.ParseIntentionKey(key)
	if err != nil {
		panic(err)
	}
	actor, err := domain.ParseIntentionActor("ana")
	if err != nil {
		panic(err)
	}
	operation, err := domain.ParseIntentionOperation("pitr-settlement")
	if err != nil {
		panic(err)
	}
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		panic(err)
	}
	return application.IdempotentTransferRequest{
		FromSeason:  domain.SeasonKey(pitrActiveBook),
		Key:         intentionKey,
		Actor:       actor,
		Operation:   operation,
		PayloadHash: pitrDumpHash,
		FromKind:    domain.CustodyTreasury,
		FromLabel:   "main",
		ToSeason:    domain.SeasonKey(pitrActiveBook),
		ToKind:      domain.CustodyUser,
		ToLabel:     "ana",
		Amount:      amount,
	}
}

func pitrActiveHolder(t *testing.T, ctx context.Context, testDB *dbtest.TestDB, season string) string {
	t.Helper()
	var holder string
	if err := testDB.Pool.Pool().QueryRow(ctx,
		`SELECT holder_subject FROM app.seasonal_reigns WHERE season_id = $1 AND is_active`, season).Scan(&holder); err != nil {
		t.Fatalf("read active reign: %v", err)
	}
	return holder
}

// TestFinancialRestoreAndReplaySafety is the directed restore sample:
// snapshot, physical backup, restore into an empty cluster, and the
// full judgment — integrity, replay safety, sealed books, ex-King,
// corruption and the declared RPO/RTO.
func TestFinancialRestoreAndReplaySafety(t *testing.T) {
	sourceDB := dbtest.New(t)
	ctx, cancel := pitrCtx()
	defer cancel()
	container := pitrContainer(t, sourceDB.DSN)
	source := postgres.NewRepository(sourceDB.Pool.Pool())
	pitrSeed(t, ctx, sourceDB, source)

	expectedActive, err := source.SnapshotBook(ctx, domain.SeasonKey(pitrActiveBook))
	if err != nil {
		t.Fatalf("snapshot active: %v", err)
	}
	expectedSealed, err := source.SnapshotBook(ctx, domain.SeasonKey(pitrSealedBook))
	if err != nil {
		t.Fatalf("snapshot sealed: %v", err)
	}
	if expectedActive.ActiveReignHolder != "" || expectedSealed.ActiveReignHolder != "bob" {
		t.Fatalf("pre-disaster reigns: active=%q sealed=%q", expectedActive.ActiveReignHolder, expectedSealed.ActiveReignHolder)
	}

	// RPO marker: committed before the backup starts, so the restore
	// must return it. Anything committed after the backup completes
	// is outside the snapshot point by design.
	if _, err := source.TransferIdempotent(ctx, pitrIntention("pitr-rpo-marker", 7)); err != nil {
		t.Fatalf("settle RPO marker: %v", err)
	}
	expectedActive, err = source.SnapshotBook(ctx, domain.SeasonKey(pitrActiveBook))
	if err != nil {
		t.Fatalf("snapshot active: %v", err)
	}

	dump := fmt.Sprintf("/tmp/pitr-%s.dump", strings.ReplaceAll(t.Name(), "/", "_"))
	start := time.Now()
	pitrExec(t, container, "pg_dump", "-U", "arena", "-d", sourceDB.DBName, "-Fc", "-f", dump)
	targetDB := dbtest.New(t, dbtest.WithoutMigrations())
	pitrExec(t, container, "pg_restore", "-U", "arena", "-d", targetDB.DBName, dump)
	pitrExec(t, container, "rm", "-f", dump)
	if elapsed := time.Since(start); elapsed > pitrRTOBudget {
		t.Fatalf("restore took %s, beyond the declared RTO %s", elapsed, pitrRTOBudget)
	}
	t.Logf("restore RTO: %s inside %s", time.Since(start).Round(time.Millisecond), pitrRTOBudget)

	// Outside the snapshot point: written after the backup, so the
	// restore must not return it. This is the honest RPO boundary.
	if _, err := source.TransferIdempotent(ctx, pitrIntention("pitr-lost-marker", 9)); err != nil {
		t.Fatalf("settle post-backup marker: %v", err)
	}

	target := postgres.NewRepository(targetDB.Pool.Pool())
	verify := application.NewVerifyRestoreUseCase(target, target)
	for _, expected := range []domain.BookFingerprint{expectedActive, expectedSealed} {
		outcome, err := verify.Execute(ctx, application.VerifyRestoreCommand{
			Season:   expected.Season.String(),
			Expected: expected,
		})
		if err != nil {
			t.Fatalf("verify %s: %v", expected.Season, err)
		}
		if !outcome.Verdict.ResumeAllowed() {
			t.Fatalf("restored %s blocked: %+v", expected.Season, outcome.Verdict.Divergences)
		}
	}
	var rpo, lost int64
	targetPool := targetDB.Pool.Pool()
	if err := targetPool.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_intentions WHERE intention_key = 'pitr-rpo-marker'`).Scan(&rpo); err != nil {
		t.Fatalf("read RPO marker: %v", err)
	}
	if rpo != 1 {
		t.Fatal("pre-backup marker did not return: RPO violated")
	}
	if err := targetPool.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_intentions WHERE intention_key = 'pitr-lost-marker'`).Scan(&lost); err != nil {
		t.Fatalf("read lost marker: %v", err)
	}
	if lost != 0 {
		t.Fatal("post-backup marker returned: snapshot point violated")
	}

	// Nothing is reapplied in duplicate: Genesis replays the
	// attestation, the intention replays its outcome, the cutoff and
	// the succession die on their uniqueness guards.
	legsBefore := bookLegs(t, ctx, targetDB, pitrActiveBook)
	if result, err := target.RunGenesis(ctx, application.GenesisRequest{
		Key:    mustTransferKey(t, "genesis-pitr-a"),
		Season: domain.SeasonKey(pitrActiveBook),
	}); err != nil || result == nil || !result.Replayed {
		t.Fatalf("restored Genesis replay = %+v, %v: want the original attestation", result, err)
	}
	if result, err := target.TransferIdempotent(ctx, pitrIntention("pitr-intent-1", 1000)); err != nil || !result.Replayed {
		t.Fatalf("restored intention replay = %+v, %v: want the stored outcome", result, err)
	}
	if _, err := targetPool.Exec(ctx,
		`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ($1, 'closing', 'sealed', now())`, pitrSealedBook); err == nil {
		t.Fatal("restored cutoff reapplied: the sealed stage duplicated")
	}
	if _, err := targetPool.Exec(ctx,
		`INSERT INTO app.seasonal_reigns (season_id, reign_version, holder_subject, is_regent, reason, is_active)
		 VALUES ($1, 2, 'bob', false, 'conquest', true)`, pitrSealedBook); err == nil {
		t.Fatal("restored succession reapplied: the reign duplicated")
	}
	if got := bookLegs(t, ctx, targetDB, pitrActiveBook); got != legsBefore {
		t.Fatalf("replays moved legs %d -> %d", legsBefore, got)
	}

	// The archived book is history, not money: it moves nothing on
	// the restored cluster.
	sealedLegs := bookLegs(t, ctx, targetDB, pitrSealedBook)
	sealedAmount, err := domain.NewMilliInk(1)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	_, err = target.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(pitrSealedBook),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(pitrSealedBook),
		ToKind:   domain.CustodyUser, ToLabel: "ana",
		Amount: sealedAmount,
	})
	if !errors.Is(err, domain.ErrBookSealed) {
		t.Fatalf("restored sealed transfer = %v, want ErrBookSealed", err)
	}
	if got := bookLegs(t, ctx, targetDB, pitrSealedBook); got != sealedLegs {
		t.Fatalf("sealed book moved legs %d -> %d on restore", sealedLegs, got)
	}

	// The ex-King stays without power: Bob still reigns on the
	// restored cluster, and Alice's stale fence is refused.
	if holder := pitrActiveHolder(t, ctx, targetDB, pitrSealedBook); holder != "bob" {
		t.Fatalf("restored reign holder = %q, want bob", holder)
	}
	anchor := time.Now().UTC()
	stale := crowndomain.EffectFence{
		Season: crowndomain.SeasonID(pitrSealedBook), Reign: crowndomain.ReignVersion(1),
		Competence: crowndomain.Competence("patrimonial"), Author: crowndomain.HolderSubject("alice"),
		CurrentReign: crowndomain.CurrentReign{
			Season: crowndomain.SeasonID(pitrSealedBook), Holder: crowndomain.HolderSubject("bob"),
			Reign: crowndomain.ReignVersion(2), AuthorityVersion: crowndomain.AuthorityVersion(3),
			StartsAt: anchor.Add(-time.Hour), EndsAt: anchor.Add(6 * time.Hour), Open: true,
		},
		EconomicBacklogClean: true, Now: anchor,
	}
	if err := crowndomain.ValidateEffectFence(stale); !errors.Is(err, crowndomain.ErrStaleReign) {
		t.Fatalf("restored ex-King fence = %v, want ErrStaleReign", err)
	}

	// Simulated corruption on the restored cluster blocks resumption:
	// one forged leg, and the verification freezes the cluster with a
	// named block instead of letting it resume.
	if _, err := targetPool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		 SELECT '88888888-8888-4888-8888-888888888888'::uuid, c.id, 'credit', 1, $1
		 FROM app.economy_custodies c WHERE c.kind = 'user' AND c.label = 'ana' AND c.season_key = $1`,
		pitrActiveBook); err != nil {
		t.Fatalf("inject corruption: %v", err)
	}
	outcome, err := verify.Execute(ctx, application.VerifyRestoreCommand{
		Season:   pitrActiveBook,
		Expected: expectedActive,
	})
	if err != nil {
		t.Fatalf("verify corruption: %v", err)
	}
	if outcome.Verdict.ResumeAllowed() || outcome.BlockID == "" {
		t.Fatalf("corrupted cluster resumes: %+v", outcome.Verdict)
	}
	moveAmount, err := domain.NewMilliInk(1)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	_, err = target.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(pitrActiveBook),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(pitrActiveBook),
		ToKind:   domain.CustodyUser, ToLabel: "ana",
		Amount: moveAmount,
	})
	if !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("post-block transfer = %v, want ErrEconomyFrozen", err)
	}
}
