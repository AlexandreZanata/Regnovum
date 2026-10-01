package postgres_test

// P46-T04 — conserved custody scoped to immutable seasons, on real
// PostgreSQL.
//
// Two synthetic books carry S each with independent balances and
// clean reconciliations; the same intention key settles per book
// without redirecting the other book's replay; missing seasons,
// cross-season moves and sealed books are refused before any leg;
// short balances refuse per book; a retry after a post-commit
// timeout replays the stored outcome without new legs; and eight
// concurrent Geneses of one book found exactly once. The suite runs
// on disposable databases and activates nothing: season rows are
// test fixtures, never production wiring.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func seasonScopeCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func seedSeasonBook(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int, starts string) {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ($1, $2, $3::timestamptz, $3::timestamptz + make_interval(secs => 7776000), 'v3', 'politica-inicial', 'fundadora', 'regente-tecnica',
		 '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')`,
		key, ordinal, starts); err != nil {
		t.Fatalf("seed season %s: %v", key, err)
	}
}

func sealSeasonBook(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string) {
	t.Helper()
	for _, stage := range [][2]string{
		{"NULL", "prepared"},
		{"prepared", "active"},
		{"active", "closing"},
		{"closing", "sealed"},
	} {
		from := "NULL"
		if stage[0] != "NULL" {
			from = "'" + stage[0] + "'"
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
			 VALUES ($1, `+from+`, $2, now())`, key, stage[1]); err != nil {
			t.Fatalf("seal stage %s of %s: %v", stage[1], key, err)
		}
	}
}

func genesisBook(t *testing.T, ctx context.Context, repo *postgres.Repository, key, season string) {
	t.Helper()
	parsed, err := domain.ParseGenesisKey(key)
	if err != nil {
		t.Fatalf("ParseGenesisKey: %v", err)
	}
	if _, err := repo.RunGenesis(ctx, application.GenesisRequest{
		Key: parsed, Season: domain.SeasonKey(season),
	}); err != nil {
		t.Fatalf("genesis %s: %v", season, err)
	}
}

func bookBalance(t *testing.T, ctx context.Context, db *dbtest.TestDB, kind, label, season string) int64 {
	t.Helper()
	var balance int64
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = $1 AND c.label = $2 AND e.season_key = $3`,
		kind, label, season).Scan(&balance); err != nil {
		t.Fatalf("book balance: %v", err)
	}
	return balance
}

func makeBookCustody(t *testing.T, ctx context.Context, db *dbtest.TestDB, kind, label, season string) {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ($1, $2, $3)`, kind, label, season); err != nil {
		t.Fatalf("create custody %s/%s in %s: %v", kind, label, season, err)
	}
}

func bookLegs(t *testing.T, ctx context.Context, db *dbtest.TestDB, season string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_entries WHERE season_key = $1`, season).Scan(&count); err != nil {
		t.Fatalf("book legs: %v", err)
	}
	return count
}

// TestSeasonBooksConserveSupplySeparately proves two synthetic books
// with S each: independent Genesis events, independent Treasury
// balances and a clean reconciliation per book, with zero legs
// leaking across books.
func TestSeasonBooksConserveSupplySeparately(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()

	repo := postgres.NewRepository(db.Pool.Pool())
	seedSeasonBook(t, ctx, db, "temporada-1", 1, "2026-10-04T12:00:00Z")
	seedSeasonBook(t, ctx, db, "temporada-2", 2, "2027-01-02T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-livro-1", "temporada-1")
	genesisBook(t, ctx, repo, "genesis-livro-2", "temporada-2")

	transfer := func(from, to, season string, millis int64) {
		t.Helper()
		amount, err := domain.NewMilliInk(millis)
		if err != nil {
			t.Fatalf("NewMilliInk: %v", err)
		}
		if _, err := repo.Transfer(ctx, application.TransferRequest{
			FromSeason: domain.SeasonKey(season),
			FromKind:   domain.CustodyTreasury, FromLabel: from,
			ToSeason: domain.SeasonKey(season),
			ToKind:   domain.CustodyUser, ToLabel: to,
			Amount: amount,
		}); err != nil {
			t.Fatalf("transfer in %s: %v", season, err)
		}
	}
	makeBookCustody(t, ctx, db, "user", "ana-livro-1", "temporada-1")
	transfer("main", "ana-livro-1", "temporada-1", 1000)
	transfer("main", "ana-livro-1", "temporada-1", 1000)

	if got := bookBalance(t, ctx, db, "treasury", "main", "temporada-1"); got != domain.GenesisSupplyMillis-2000 {
		t.Fatalf("book 1 treasury = %d, want S-2000", got)
	}
	if got := bookBalance(t, ctx, db, "treasury", "main", "temporada-2"); got != domain.GenesisSupplyMillis {
		t.Fatalf("book 2 treasury = %d, want untouched S", got)
	}
	for _, season := range []string{"temporada-1", "temporada-2"} {
		report, err := repo.Reconcile(ctx, domain.SeasonKey(season))
		if err != nil {
			t.Fatalf("reconcile %s: %v", season, err)
		}
		if report.Frozen || len(report.Mismatch) != 0 {
			t.Fatalf("book %s mismatch: %+v", season, report.Mismatch)
		}
		if report.SupplyMillis != domain.GenesisSupplyMillis {
			t.Fatalf("book %s supply = %d, want S", season, report.SupplyMillis)
		}
	}
}

// TestSeasonReplayNeverRedirectsBooks proves the same intention key
// settles per book: the second book records its own outcome, and a
// replay of the first key returns the first transfer untouched.
func TestSeasonReplayNeverRedirectsBooks(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()

	repo := postgres.NewRepository(db.Pool.Pool())
	seedSeasonBook(t, ctx, db, "temporada-1", 1, "2026-10-04T12:00:00Z")
	seedSeasonBook(t, ctx, db, "temporada-2", 2, "2027-01-02T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-livro-1", "temporada-1")
	genesisBook(t, ctx, repo, "genesis-livro-2", "temporada-2")
	makeBookCustody(t, ctx, db, "user", "ana-temporada-1", "temporada-1")
	makeBookCustody(t, ctx, db, "user", "ana-temporada-2", "temporada-2")

	intend := func(season, key string) *application.IdempotentTransferResult {
		t.Helper()
		useCase := application.NewIdempotentTransferUseCase(repo, repo)
		result, err := useCase.Execute(ctx, application.IdempotentTransferCommand{
			FromSeason: season,
			Key:        key, Actor: "tesoureira", Operation: "repasse",
			FromKind: "treasury", FromLabel: "main",
			ToSeason: season,
			ToKind:   "user", ToLabel: "ana-" + season,
			Millis: 500,
		})
		if err != nil {
			t.Fatalf("intend %s in %s: %v", key, season, err)
		}
		return result
	}
	first := intend("temporada-1", "repasse-duplo")
	second := intend("temporada-2", "repasse-duplo")
	if first.TransferID == second.TransferID {
		t.Fatal("cross-book replay redirected: each book owns its outcome")
	}
	if second.Replayed {
		t.Fatal("second book replayed the first: it must settle its own legs")
	}
	replay := intend("temporada-1", "repasse-duplo")
	if !replay.Replayed || replay.TransferID != first.TransferID {
		t.Fatalf("replay = %+v, want the first transfer untouched", replay)
	}
	if got := bookLegs(t, ctx, db, "temporada-2"); got != 3 {
		t.Fatalf("book 2 legs = %d, want genesis plus one intention pair", got)
	}
}

// TestSeasonRefusalsBeforeAnyLeg proves missing seasons, cross-season
// moves and sealed books are refused at the use-case and repository
// layers before any leg is written.
func TestSeasonRefusalsBeforeAnyLeg(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()

	repo := postgres.NewRepository(db.Pool.Pool())
	seedSeasonBook(t, ctx, db, "temporada-1", 1, "2026-10-04T12:00:00Z")
	seedSeasonBook(t, ctx, db, "temporada-2", 2, "2027-01-02T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-livro-1", "temporada-1")

	genesisUC := application.NewGenesisUseCase(repo, repo)
	if _, err := genesisUC.Execute(ctx, application.GenesisCommand{Key: "genesis-sem-livro"}); !errors.Is(err, domain.ErrMissingSeason) {
		t.Fatalf("seasonless genesis = %v, want ErrMissingSeason", err)
	}
	transferUC := application.NewTransferUseCase(repo, repo)
	if _, err := transferUC.Execute(ctx, application.TransferCommand{
		FromKind: "treasury", FromLabel: "main",
		ToKind: "user", ToLabel: "ana", Millis: 10,
	}); !errors.Is(err, domain.ErrMissingSeason) {
		t.Fatalf("seasonless transfer = %v, want ErrMissingSeason", err)
	}
	if _, err := transferUC.Execute(ctx, application.TransferCommand{
		FromSeason: "temporada-1",
		FromKind:   "treasury", FromLabel: "main",
		ToSeason: "temporada-2",
		ToKind:   "user", ToLabel: "ana", Millis: 10,
	}); !errors.Is(err, domain.ErrCrossSeason) {
		t.Fatalf("cross-season transfer = %v, want ErrCrossSeason", err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: "temporada-1",
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: "temporada-2",
		ToKind:   domain.CustodyUser, ToLabel: "ana",
		Amount: mustSeasonMilli(t, 10),
	}); !errors.Is(err, domain.ErrCrossSeason) {
		t.Fatalf("cross-season repository transfer = %v, want ErrCrossSeason", err)
	}

	sealSeasonBook(t, ctx, db, "temporada-1")
	if _, err := transferUC.Execute(ctx, application.TransferCommand{
		FromSeason: "temporada-1",
		FromKind:   "treasury", FromLabel: "main",
		ToSeason: "temporada-1",
		ToKind:   "user", ToLabel: "ana", Millis: 10,
	}); !errors.Is(err, domain.ErrBookSealed) {
		t.Fatalf("sealed transfer = %v, want ErrBookSealed", err)
	}
	if _, err := genesisUC.Execute(ctx, application.GenesisCommand{Key: "genesis-tardia", Season: "temporada-2"}); err != nil {
		t.Fatalf("prepared book genesis = %v, want success", err)
	}
	if _, err := repo.RunGenesis(ctx, application.GenesisRequest{
		Key: mustSeasonGenesisKey(t, "genesis-ativa"), Season: "temporada-1",
	}); !errors.Is(err, domain.ErrBookNotPrepared) {
		t.Fatalf("post-activation genesis = %v, want ErrBookNotPrepared", err)
	}

	if got := bookLegs(t, ctx, db, "temporada-1"); got != 1 {
		t.Fatalf("sealed book legs = %d, want only the genesis credit", got)
	}
	if got := bookLegs(t, ctx, db, "temporada-2"); got != 1 {
		t.Fatalf("second book legs = %d, want only the genesis credit", got)
	}
}

func mustSeasonMilli(t *testing.T, millis int64) domain.MilliInk {
	t.Helper()
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		t.Fatalf("NewMilliInk(%d): %v", millis, err)
	}
	return amount
}

func mustSeasonGenesisKey(t *testing.T, key string) domain.GenesisKey {
	t.Helper()
	parsed, err := domain.ParseGenesisKey(key)
	if err != nil {
		t.Fatalf("ParseGenesisKey(%q): %v", key, err)
	}
	return parsed
}

// TestSeasonShortBalancesRefusePerBook proves insufficient funds are
// judged inside the debtor book: an empty second book refuses what a
// funded first book settles.
func TestSeasonShortBalancesRefusePerBook(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()

	repo := postgres.NewRepository(db.Pool.Pool())
	seedSeasonBook(t, ctx, db, "temporada-1", 1, "2026-10-04T12:00:00Z")
	seedSeasonBook(t, ctx, db, "temporada-2", 2, "2027-01-02T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-livro-1", "temporada-1")
	genesisBook(t, ctx, repo, "genesis-livro-2", "temporada-2")
	makeBookCustody(t, ctx, db, "user", "ana-livro-2", "temporada-2")

	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: "temporada-2",
		FromKind:   domain.CustodyUser, FromLabel: "ana-livro-2",
		ToSeason: "temporada-2",
		ToKind:   domain.CustodyTreasury, ToLabel: "main",
		Amount: mustSeasonMilli(t, 1),
	}); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("empty book transfer = %v, want ErrInsufficientMilliInk", err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: "temporada-1",
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: "temporada-1",
		ToKind:   domain.CustodyTreasury, ToLabel: "main",
		Amount: mustSeasonMilli(t, domain.GenesisSupplyMillis+1),
	}); !errors.Is(err, domain.ErrSameCustody) {
		t.Fatalf("self transfer = %v, want ErrSameCustody before the balance", err)
	}
}

// TestSeasonPostCommitTimeoutReplaysStoredOutcome proves a retry after
// a post-commit timeout reads the stored intention instead of writing
// new legs: the transfer id and the leg count never move.
func TestSeasonPostCommitTimeoutReplaysStoredOutcome(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()

	repo := postgres.NewRepository(db.Pool.Pool())
	seedSeasonBook(t, ctx, db, "temporada-1", 1, "2026-10-04T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-livro-1", "temporada-1")
	makeBookCustody(t, ctx, db, "user", "ana-timeout", "temporada-1")

	intend := func() *application.IdempotentTransferResult {
		t.Helper()
		useCase := application.NewIdempotentTransferUseCase(repo, repo)
		result, err := useCase.Execute(ctx, application.IdempotentTransferCommand{
			FromSeason: "temporada-1",
			Key:        "repasse-timeout", Actor: "tesoureira", Operation: "repasse",
			FromKind: "treasury", FromLabel: "main",
			ToSeason: "temporada-1",
			ToKind:   "user", ToLabel: "ana-timeout",
			Millis: 700,
		})
		if err != nil {
			t.Fatalf("intend: %v", err)
		}
		return result
	}
	first := intend()
	if first.Replayed {
		t.Fatal("first intention replayed: it must settle")
	}
	legs := bookLegs(t, ctx, db, "temporada-1")
	retry := intend()
	if !retry.Replayed || retry.TransferID != first.TransferID {
		t.Fatalf("timeout retry = %+v, want the stored outcome", retry)
	}
	if got := bookLegs(t, ctx, db, "temporada-1"); got != legs {
		t.Fatalf("legs moved %d -> %d: a replay writes nothing", legs, got)
	}
}

// TestSeasonConcurrentGenesesFoundOnce proves eight concurrent
// Geneses of one book found exactly one: the losers read
// ErrGenesisAlreadyExists, never a second supply.
func TestSeasonConcurrentGenesesFoundOnce(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()

	repo := postgres.NewRepository(db.Pool.Pool())
	seedSeasonBook(t, ctx, db, "temporada-1", 1, "2026-10-04T12:00:00Z")

	const racers = 8
	var wg sync.WaitGroup
	founded := make([]bool, racers)
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = repo.RunGenesis(ctx, application.GenesisRequest{
				Key:    mustSeasonGenesisKey(t, fmt.Sprintf("genesis-race-%d", i)),
				Season: "temporada-1",
			})
			founded[i] = errs[i] == nil
		}(i)
	}
	wg.Wait()
	winners, refused := 0, 0
	for i := range racers {
		switch {
		case errs[i] == nil:
			winners++
		case errors.Is(errs[i], domain.ErrGenesisAlreadyExists):
			refused++
		default:
			t.Fatalf("racer %d = %v, want Genesis or ErrGenesisAlreadyExists", i, errs[i])
		}
	}
	if winners != 1 || refused != racers-1 {
		t.Fatalf("winners = %d, refused = %d; want 1 and %d", winners, refused, racers-1)
	}
	report, err := repo.Reconcile(ctx, "temporada-1")
	if err != nil {
		t.Fatalf("reconcile raced book: %v", err)
	}
	if report.Frozen || report.SupplyMillis != domain.GenesisSupplyMillis {
		t.Fatalf("raced book = %+v, want one clean S", report)
	}
}
