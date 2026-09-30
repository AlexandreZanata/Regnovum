package postgres_test

// P32-T06 — derived balances and private statements on real PostgreSQL.
//
// Balances are never stored, only summed from the journal; statements are
// private keyset pages authorized against the live holder and its account
// status; rebuild re-derives every projection without editing the
// journal, sealing each with a verifiable digest. The tests prove on a
// disposable database: projection equals an independent sum after seeded
// random sequences, cursor walks neither duplicate nor skip, other
// holders and suspended owners read nothing, tampered checkpoints fail,
// and conservation holds throughout.

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
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

func statementCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func makeAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email, status string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ($1, $2) RETURNING id::text`,
		email, status).Scan(&id); err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return id
}

func makeOwnedCustody(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, kind, label string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, owner_account_id) VALUES ($1, $2, $3::uuid)`,
		kind, label, owner); err != nil {
		t.Fatalf("create owned custody %s/%s: %v", kind, label, err)
	}
}

func settleForStatement(t *testing.T, ctx context.Context, repo *postgres.Repository, fromLabel, toLabel string, millis int64) {
	t.Helper()
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		t.Fatalf("NewMilliInk(%d): %v", millis, err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(domain.CompatSeasonKey),
		FromKind:   domain.CustodyTreasury, FromLabel: fromLabel,
		ToSeason: domain.SeasonKey(domain.CompatSeasonKey),
		ToKind:   domain.CustodyUser, ToLabel: toLabel,
		Amount: amount,
	}); err != nil {
		t.Fatalf("transfer %s -> %s %d: %v", fromLabel, toLabel, millis, err)
	}
}

func readPage(t *testing.T, ctx context.Context, repo *postgres.Repository, caller, label, cursor string, limit int) *application.StatementPage {
	t.Helper()
	page, err := repo.ReadStatement(ctx, application.StatementRequest{
		Season: domain.SeasonKey(domain.CompatSeasonKey),
		Kind:   domain.CustodyUser, Label: label, CallerAccountID: caller, Limit: limit,
		Cursor: mustParseCursor(t, cursor),
	})
	if err != nil {
		t.Fatalf("ReadStatement(%s): %v", label, err)
	}
	return page
}

func mustParseCursor(t *testing.T, raw string) application.StatementCursor {
	t.Helper()
	cursor, err := application.ParseStatementCursor(raw)
	if err != nil {
		t.Fatalf("ParseStatementCursor(%q): %v", raw, err)
	}
	return cursor
}

// TestProjectionEqualsIndependentSum runs seeded random transfers and
// proves the rebuild equals per-custody sums aggregated independently:
// one journal, two computations, zero divergence.
func TestProjectionEqualsIndependentSum(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := statementCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasuryForStatement(t, ctx, repo)
	holders := []string{"ana", "bia", "carol", "dave", "eva"}
	for _, holder := range holders {
		owner := makeAccount(t, ctx, pool, holder+"@arena.example.com", "active")
		makeOwnedCustody(t, ctx, pool, owner, "user", holder)
	}

	rng := testsource.NewRandom(testsource.SeedFor(t))
	for range 200 {
		to := holders[rng.Int64n(int64(len(holders)))]
		amount := rng.Int64n(500) + 1
		settleForStatement(t, ctx, repo, "main", to, amount)
	}

	projections, err := repo.RebuildAll(ctx, domain.SeasonKey(domain.CompatSeasonKey))
	if err != nil {
		t.Fatalf("RebuildAll: %v", err)
	}
	byLabel := map[string]application.CustodyProjection{}
	for _, projection := range projections {
		if err := projection.Verify(); err != nil {
			t.Fatalf("projection %s/%s digest: %v", projection.Kind, projection.Label, err)
		}
		byLabel[string(projection.Kind)+"/"+projection.Label] = projection
	}
	for _, holder := range holders {
		projection, ok := byLabel["user/"+holder]
		if !ok {
			t.Fatalf("no projection for user/%s", holder)
		}
		if got := custodyBalance(t, ctx, pool, "user", holder); got != projection.Balance.Millis() {
			t.Fatalf("user/%s projection %d != independent sum %d", holder, projection.Balance.Millis(), got)
		}
	}
	var credits, debits int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'credit'`).Scan(&credits); err != nil {
		t.Fatalf("sum credits: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'debit'`).Scan(&debits); err != nil {
		t.Fatalf("sum debits: %v", err)
	}
	if credits-debits != domain.GenesisSupplyMillis {
		t.Fatalf("credits %d - debits %d != S after random sequences", credits, debits)
	}
}

// TestStatementCursorWalksWithoutGap proves keyset pages partition the
// journal: walking with small pages reproduces the full ordered id list
// with no duplicate and no skip.
func TestStatementCursorWalksWithoutGap(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := statementCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasuryForStatement(t, ctx, repo)
	owner := makeAccount(t, ctx, pool, "ana@arena.example.com", "active")
	makeOwnedCustody(t, ctx, pool, owner, "user", "ana")
	for range 30 {
		settleForStatement(t, ctx, repo, "main", "ana", 10)
	}

	full := readPage(t, ctx, repo, owner, "ana", "", 100)
	if len(full.Entries) != 30 {
		t.Fatalf("full read = %d entries, want 30", len(full.Entries))
	}
	if full.NextCursor != "" {
		t.Fatalf("full read continues past the journal end")
	}

	var walked []string
	cursor := ""
	for {
		page := readPage(t, ctx, repo, owner, "ana", cursor, 7)
		for _, entry := range page.Entries {
			walked = append(walked, entry.EntryID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(walked) != len(full.Entries) {
		t.Fatalf("walked %d entries, journal holds %d", len(walked), len(full.Entries))
	}
	seen := map[string]int{}
	for index, id := range walked {
		seen[id]++
		if walked[index] != full.Entries[index].EntryID {
			t.Fatalf("walk order diverges at position %d", index)
		}
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("entry %s appears %d times", id, count)
		}
	}
	if full.Balance.Millis() != 300 {
		t.Fatalf("ana balance = %d, want 300", full.Balance.Millis())
	}
}

// TestStatementRefusesOtherAndSuspended proves private reads: another
// holder, the system Treasury and a suspended owner all read nothing.
func TestStatementRefusesOtherAndSuspended(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := statementCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasuryForStatement(t, ctx, repo)
	ana := makeAccount(t, ctx, pool, "ana@arena.example.com", "active")
	bia := makeAccount(t, ctx, pool, "bia@arena.example.com", "active")
	makeOwnedCustody(t, ctx, pool, ana, "user", "ana")
	makeOwnedCustody(t, ctx, pool, bia, "user", "bia")
	settleForStatement(t, ctx, repo, "main", "ana", 100)

	if _, err := repo.ReadStatement(ctx, application.StatementRequest{
		Season: domain.SeasonKey(domain.CompatSeasonKey),
		Kind:   domain.CustodyUser, Label: "ana", CallerAccountID: bia, Limit: 10,
	}); !errors.Is(err, domain.ErrStatementForbidden) {
		t.Fatalf("other holder read = %v, want ErrStatementForbidden", err)
	}
	if _, err := repo.ReadStatement(ctx, application.StatementRequest{
		Season: domain.SeasonKey(domain.CompatSeasonKey),
		Kind:   domain.CustodyTreasury, Label: "main", CallerAccountID: ana, Limit: 10,
	}); !errors.Is(err, domain.ErrStatementForbidden) {
		t.Fatalf("system treasury read = %v, want ErrStatementForbidden", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE app.accounts SET status = 'suspended' WHERE id = $1::uuid`, ana); err != nil {
		t.Fatalf("suspend ana: %v", err)
	}
	if _, err := repo.ReadStatement(ctx, application.StatementRequest{
		Season: domain.SeasonKey(domain.CompatSeasonKey),
		Kind:   domain.CustodyUser, Label: "ana", CallerAccountID: ana, Limit: 10,
	}); !errors.Is(err, domain.ErrStatementSuspended) {
		t.Fatalf("suspended owner read = %v, want ErrStatementSuspended", err)
	}
	if _, err := repo.ReadStatement(ctx, application.StatementRequest{
		Season: domain.SeasonKey(domain.CompatSeasonKey),
		Kind:   domain.CustodyUser, Label: "ghost", CallerAccountID: ana, Limit: 10,
	}); !errors.Is(err, domain.ErrUnknownCustody) {
		t.Fatalf("unknown custody read = %v, want ErrUnknownCustody", err)
	}
}

// TestCheckpointDetectsTampering proves sealed projections verify after
// rebuilds, stay stable without writes, and fail once edited.
func TestCheckpointDetectsTampering(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := statementCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasuryForStatement(t, ctx, repo)
	owner := makeAccount(t, ctx, pool, "ana@arena.example.com", "active")
	makeOwnedCustody(t, ctx, pool, owner, "user", "ana")
	settleForStatement(t, ctx, repo, "main", "ana", 250)

	first, err := repo.RebuildAll(ctx, domain.SeasonKey(domain.CompatSeasonKey))
	if err != nil {
		t.Fatalf("RebuildAll: %v", err)
	}
	second, err := repo.RebuildAll(ctx, domain.SeasonKey(domain.CompatSeasonKey))
	if err != nil {
		t.Fatalf("RebuildAll: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("rebuilds disagree in size: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("rebuild %d unstable without writes", i)
		}
		if err := first[i].Verify(); err != nil {
			t.Fatalf("projection %d: %v", i, err)
		}
	}
	tampered := first[0]
	tampered.Entries++
	if err := tampered.Verify(); err == nil {
		t.Fatalf("tampered checkpoint verified: the digest does not bind")
	}
}

func fundTreasuryForStatement(t *testing.T, ctx context.Context, repo *postgres.Repository) {
	t.Helper()
	if _, err := repo.RunGenesis(ctx, application.GenesisRequest{Key: mustTransferKey(t, "genesis-statements"), Season: domain.SeasonKey(domain.CompatSeasonKey)}); err != nil {
		t.Fatalf("seed Genesis: %v", err)
	}
}
