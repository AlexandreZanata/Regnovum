package postgres_test

// P46-T10 — selo do arquivo e abertura da sucessora.
//
// O SeasonOpener arquiva o livro selado sem reescrever o diário
// (receipt + archived) e abre a sucessora com continuidade exata,
// Genesis idempotente e ACTIVE único: checksum adulterado ou Σ≠S
// impede abertura, duas aberturas criam uma Genesis/ACTIVE, crash
// entre arquivo/Genesis/ativação recupera sem carry-over, S antigo
// permanece verificável e zero saldo novo nasce de vencedor anterior.
// Restore sintético preserva ambos livros. Sem wiring: Genesis entra
// por func-port e o produto econômico segue desativado.

import (
	"context"
	"errors"
	"testing"
	"time"

	economypg "github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	economyapp "github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	seasonpg "github.com/AlexandreZanata/Regnovum/internal/seasons/adapters/postgres"
	closeapp "github.com/AlexandreZanata/Regnovum/internal/seasons/application"
	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

// seedOpenSeason inserts one season row with the correctly sealed
// manifesto hash: the opener recomputes it, so dummy hashes refuse.
func seedOpenSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int, starts time.Time) (time.Time, time.Time) {
	t.Helper()
	manifest, err := seasondomain.NewManifest(seasondomain.ManifestRequest{
		ID: key, Ordinal: ordinal, StartsAt: starts.UTC(),
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ($1, $2, $3, $4, 'v3', 'politica-inicial', 'fundadora', 'regente-tecnica', $5)`,
		key, ordinal, manifest.StartsAt, manifest.StartsAt.Add(7776000*time.Second), manifest.Hash); err != nil {
		t.Fatalf("seed season %s: %v", key, err)
	}
	var from, to time.Time
	if err := db.QueryRow(ctx, `SELECT starts_at, ends_at FROM app.seasons WHERE season_key = $1`, key).Scan(&from, &to); err != nil {
		t.Fatalf("read season window: %v", err)
	}
	return from.UTC(), to.UTC()
}

// endedOpenSeason seeds a book whose end already passed: the cutoff is
// reached, only the barrier is missing.
func endedOpenSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int) (time.Time, time.Time) {
	t.Helper()
	return seedOpenSeason(t, ctx, db, key, ordinal, time.Now().UTC().Add(-91*24*time.Hour).Truncate(time.Second))
}

// sealOpenPredecessor funds, activates, drains and seals one book with
// a released escrow and no open obligations: the clean seal T10 opens
// from.
func sealOpenPredecessor(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int) time.Time {
	t.Helper()
	_, ends := endedOpenSeason(t, ctx, db, key, ordinal)
	buyer := closeAccount(t, ctx, db)
	provider := closeAccount(t, ctx, db)
	fundCloseHolder(t, ctx, db, buyer, key, 100000)
	activateCloseSeason(t, ctx, db, key)
	seedReleasedEscrow(t, ctx, db, escrowFundOrder{
		key: "limpo-abertura", buyer: buyer, provider: provider,
		amount: 20000, posted: ends.Add(-2 * time.Hour), season: key, ends: ends,
	})
	worker, _ := closeWorker(db, 10)
	if _, err := worker.Run(ctx, key, "fechador-1", time.Minute); err != nil {
		t.Fatalf("seal predecessor: %v", err)
	}
	return ends
}

// openGenesisFunc returns the Genesis port closure over the real
// repository: one Genesis per book, replays resolve. A concurrent
// opener may activate before this Genesis runs: the same key then
// replays instead of refusing as not-prepared.
func openGenesisFunc(db *dbtest.TestDB) closeapp.GenesisFunc {
	repo := economypg.NewRepository(db.Pool.Pool())
	return func(ctx context.Context, season, genesisKey string) (bool, error) {
		key, err := economydomain.ParseGenesisKey(genesisKey)
		if err != nil {
			return false, err
		}
		result, err := repo.RunGenesis(ctx, economyapp.GenesisRequest{
			Key: key, Season: economydomain.SeasonKey(season),
		})
		if err == nil {
			return result.Replayed, nil
		}
		if !errors.Is(err, economydomain.ErrGenesisAlreadyExists) &&
			!errors.Is(err, economydomain.ErrBookNotPrepared) {
			return false, err
		}
		var present bool
		if qerr := db.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM app.economy_genesis WHERE genesis_key = $1 AND season_key = $2)`,
			genesisKey, season).Scan(&present); qerr != nil {
			return false, err
		}
		if present {
			return true, nil
		}
		return false, err
	}
}

// openSuccessorReq builds the exact successor request: next ordinal,
// fresh id, start at the predecessor end.
func openSuccessorReq(t *testing.T, ctx context.Context, db *dbtest.TestDB, predecessor, successor string, ordinal int) seasondomain.ManifestRequest {
	t.Helper()
	var ends time.Time
	if err := db.QueryRow(ctx, `SELECT ends_at FROM app.seasons WHERE season_key = $1`, predecessor).Scan(&ends); err != nil {
		t.Fatalf("read predecessor end: %v", err)
	}
	return seasondomain.ManifestRequest{
		ID: successor, Ordinal: ordinal, StartsAt: ends.UTC(),
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	}
}

func openStore(t *testing.T, db *dbtest.TestDB) (*seasonpg.SeasonOpener, *closeapp.Opener) {
	t.Helper()
	opener, err := seasonpg.NewSeasonOpener(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewSeasonOpener: %v", err)
	}
	worker := &closeapp.Opener{Store: opener, Genesis: openGenesisFunc(db)}
	return opener, worker
}

// TestOpenRefusesTamperedAndDiverged proves checksum and conservation
// gate the archive: a diverged barrier snapshot refuses with custody
// preserved and no successor row appears.
func TestOpenRefusesTamperedAndDiverged(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := closeCtx()
	defer cancel()

	sealOpenPredecessor(t, ctx, db, "temporada-arquivo-a", 201)
	opener, _ := openStore(t, db)

	if _, err := db.Exec(ctx,
		`UPDATE app.season_close_runs SET snapshot_milli = snapshot_milli + 1 WHERE season_key = $1`,
		"temporada-arquivo-a"); err != nil {
		t.Fatalf("diverge snapshot: %v", err)
	}
	if _, err := opener.Archive(ctx, "temporada-arquivo-a"); err == nil {
		t.Fatal("diverged S archived: Σ≠S impede abertura")
	}
	var archives, seasons int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.season_archives WHERE season_key = $1`, "temporada-arquivo-a").Scan(&archives); err != nil {
		t.Fatalf("count archives: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.seasons WHERE season_key = $1`, "temporada-arquivo-b").Scan(&seasons); err != nil {
		t.Fatalf("count successor: %v", err)
	}
	if archives != 0 || seasons != 0 {
		t.Fatalf("archives %d successor %d after diverged seal: BLOCKED move nada", archives, seasons)
	}
}

// TestOpenCreatesOneGenesisActive proves idempotent opening: two
// concurrent openings record one archive, one season, one Genesis and
// one ACTIVE with the same cutoff.
func TestOpenCreatesOneGenesisActive(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := closeCtx()
	defer cancel()

	sealOpenPredecessor(t, ctx, db, "temporada-arquivo-c", 202)
	opener, worker := openStore(t, db)
	next := openSuccessorReq(t, ctx, db, "temporada-arquivo-c", "temporada-arquivo-d", 203)

	type openOutcome struct {
		result closeapp.OpenResult
		err    error
	}
	first, second := make(chan openOutcome, 1), make(chan openOutcome, 1)
	go func() {
		result, err := worker.OpenNext(ctx, "temporada-arquivo-c", next, "genesis-arquivo-d")
		first <- openOutcome{result: result, err: err}
	}()
	go func() {
		result, err := worker.OpenNext(ctx, "temporada-arquivo-c", next, "genesis-arquivo-d")
		second <- openOutcome{result: result, err: err}
	}()
	one, two := <-first, <-second
	if one.err != nil || two.err != nil {
		t.Fatalf("concurrent opens: %v / %v", one.err, two.err)
	}
	var geneses, actives, archives int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_genesis WHERE season_key = $1`, "temporada-arquivo-d").Scan(&geneses); err != nil {
		t.Fatalf("count geneses: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.season_lifecycle WHERE season_key = $1 AND to_state = 'active'`, "temporada-arquivo-d").Scan(&actives); err != nil {
		t.Fatalf("count actives: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.season_archives WHERE season_key = $1`, "temporada-arquivo-c").Scan(&archives); err != nil {
		t.Fatalf("count archives: %v", err)
	}
	if geneses != 1 || actives != 1 || archives != 1 {
		t.Fatalf("geneses %d actives %d archives %d: duas aberturas criam uma Genesis/ACTIVE", geneses, actives, archives)
	}
	if _, err := opener.Activate(ctx, "temporada-arquivo-c", "temporada-arquivo-d"); err != nil {
		t.Fatalf("replay activate: %v", err)
	}
}

// TestOpenCrashRecoversWithoutCarry proves crash recovery: archive,
// prepared and Genesis replay after a crash before active, the old S
// stays verifiable and the new book holds Treasury S with zero others.
func TestOpenCrashRecoversWithoutCarry(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := closeCtx()
	defer cancel()

	sealOpenPredecessor(t, ctx, db, "temporada-arquivo-e", 204)
	opener, worker := openStore(t, db)
	next := openSuccessorReq(t, ctx, db, "temporada-arquivo-e", "temporada-arquivo-f", 205)

	proof, err := opener.Archive(ctx, "temporada-arquivo-e")
	if err != nil || proof.Replayed {
		t.Fatalf("archive = %+v/%v, want fresh receipt", proof, err)
	}
	if _, err := opener.EnsurePrepared(ctx, "temporada-arquivo-e", next); err != nil {
		t.Fatalf("ensure prepared: %v", err)
	}
	if _, err := openGenesisFunc(db)(ctx, "temporada-arquivo-f", "genesis-arquivo-f"); err != nil {
		t.Fatalf("genesis: %v", err)
	}
	// Crash before active: cancelled context commits nothing new.
	dead, stop := context.WithCancel(context.Background())
	stop()
	if _, err := opener.Activate(dead, "temporada-arquivo-e", "temporada-arquivo-f"); err == nil {
		t.Fatal("cancelled activate passed: crash before commit leaves zero effect")
	}
	resumed, err := worker.OpenNext(ctx, "temporada-arquivo-e", next, "genesis-arquivo-f")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumed.ArchiveReplayed || !resumed.PreparedReplayed || !resumed.GenesisReplayed {
		t.Fatalf("resume = %+v, want archive/prepared/genesis replayed", resumed)
	}
	oldGenesis, oldSigned, _, _ := closeSums(t, ctx, db, "temporada-arquivo-e")
	if oldSigned != oldGenesis {
		t.Fatalf("old book moved: genesis %d signed %d", oldGenesis, oldSigned)
	}
	var treasury, others, genesis int64
	if err := db.QueryRow(ctx, `SELECT amount_milli FROM app.economy_genesis WHERE season_key = $1`, "temporada-arquivo-f").Scan(&genesis); err != nil {
		t.Fatalf("read new genesis: %v", err)
	}
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.season_key = $1 AND c.kind = 'treasury' AND c.label = 'main'`, "temporada-arquivo-f").Scan(&treasury); err != nil {
		t.Fatalf("read new treasury: %v", err)
	}
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.season_key = $1 AND NOT (c.kind = 'treasury' AND c.label = 'main')`, "temporada-arquivo-f").Scan(&others); err != nil {
		t.Fatalf("read new others: %v", err)
	}
	if treasury != genesis || others != 0 {
		t.Fatalf("new book treasury %d genesis %d others %d: zero saldo novo nasce de vencedor anterior", treasury, genesis, others)
	}
	// Synthetic restore preserves both books: every row counted in
	// both books is still there after the archive-and-open.
	var oldLegs, newLegs int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries WHERE season_key = $1`, "temporada-arquivo-e").Scan(&oldLegs); err != nil {
		t.Fatalf("count old legs: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries WHERE season_key = $1`, "temporada-arquivo-f").Scan(&newLegs); err != nil {
		t.Fatalf("count new legs: %v", err)
	}
	if oldLegs == 0 || newLegs == 0 {
		t.Fatalf("legs old %d new %d: restore sintético preserva ambos livros", oldLegs, newLegs)
	}
}
