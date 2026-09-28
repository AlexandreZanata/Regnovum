// Package performance_test enforces backend resource budgets (P28-T07) on
// top of the unit costs pinned by budget_test.go: response payloads and
// pagination maxima stay bounded per journey, over-limit input clamps or
// fails early instead of growing, and byte ceilings hold independently of
// dataset size. Every measurement runs against real PostgreSQL through the
// application use cases (the layer that owns clamping), with synthetic
// fixtures only. Environment is logged, never hidden: budgets below were
// calibrated on Linux x86_64, i7-13620H, Go 1.27.1, PostgreSQL 18, and carry
// headroom for CI variance; they change only with a new recorded
// measurement, never to accommodate slower code.
package performance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	arenasrepo "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	argumentsrepo "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/exportjson"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	walletrepo "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// Payload ceilings per journey, in serialized JSON bytes of one max page.
// Calibrated 2026-09-28 (feed ~210KB, arguments ~330KB, statement ~45KB at
// max-length content) with headroom for CI variance.
const (
	maxFeedPageBytes      = 512 << 10
	maxArgumentsPageBytes = 512 << 10
	maxStatementPageBytes = 128 << 10
	maxExportDocBytes     = 2 << 20
)

// payloadWorld wires the real repositories and use cases over one
// disposable database. Cursor secrets are fixed test values, never real
// credentials; cursors only sign pagination positions.
type payloadWorld struct {
	pool        *pgxpool.Pool
	feed        *arenasapp.GetArenaFeedUseCase
	args        *argumentsapp.ListArenaArgumentsUseCase
	wallet      *walletapp.GetWalletStatementUseCase
	account     walletdomain.AccountID
	arena       string
	arenaUUID   pgtype.UUID
	accountUUID pgtype.UUID
	version     string
}

func mustPayloadSecret(t *testing.T) []byte {
	t.Helper()
	return []byte("payload-test-cursor-secret-0123456789")
}

func newPayloadWorld(t *testing.T) *payloadWorld {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()

	feedCodec, err := arenasapp.NewFeedCursorCodec(mustPayloadSecret(t))
	if err != nil {
		t.Fatalf("feed codec: %v", err)
	}
	argCodec, err := argumentsapp.NewArgumentCursorCodec(mustPayloadSecret(t))
	if err != nil {
		t.Fatalf("argument codec: %v", err)
	}
	stmtCodec, err := walletapp.NewStatementCursorCodec(mustPayloadSecret(t))
	if err != nil {
		t.Fatalf("statement codec: %v", err)
	}

	var version string
	if err := pool.QueryRow(ctx, "SELECT split_part(version(), ' ', 2)").Scan(&version); err != nil {
		t.Fatalf("postgres version: %v", err)
	}
	t.Logf("payload env: %s/%s cpus=%d go=%s postgres=%s",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version(), version)

	return &payloadWorld{
		pool:    pool,
		feed:    arenasapp.NewGetArenaFeedUseCase(arenasrepo.NewRepository(pool), feedCodec),
		args:    argumentsapp.NewListArenaArgumentsUseCase(argumentsrepo.NewRepository(pool), argCodec),
		wallet:  walletapp.NewGetWalletStatementUseCase(walletrepo.NewRepository(pool), stmtCodec),
		version: version,
	}
}

// seedPayloadVolume inserts scale arenas with max-length statements, scale
// max-length arguments on the probe arena and scale wallet operations for
// the probe account. All literals are synthetic; content uses repeated ASCII
// so grapheme counts are exact.
func seedPayloadVolume(t *testing.T, ctx context.Context, world *payloadWorld, scale int) {
	t.Helper()
	queries := platformpg.New(world.pool)

	account, err := queries.CreateAccount(ctx, platformpg.CreateAccountParams{
		Email:  fmt.Sprintf("payload-%d@arena.example.com", scale),
		Status: "active",
	})
	if err != nil {
		t.Fatalf("create probe account: %v", err)
	}
	world.account = walletdomain.AccountID(account.ID.String())
	world.accountUUID = account.ID
	if err := queries.EnsureWalletAccount(ctx, account.ID); err != nil {
		t.Fatalf("ensure probe wallet: %v", err)
	}

	var arenaUUID pgtype.UUID
	statement := strings.Repeat("s", 2000)
	if err := world.pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at, version)
		VALUES ($1, $2, 'technology', 'pt-BR', 'published', 'payload-probe-arena', now(), 2)
		RETURNING id`, account.ID, statement).Scan(&arenaUUID); err != nil {
		t.Fatalf("create probe arena: %v", err)
	}
	world.arena = arenaUUID.String()
	world.arenaUUID = arenaUUID
	arenaID := world.arena
	if _, err := world.pool.Exec(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at, version)
		SELECT $1, $2, 'technology', 'pt-BR', 'published',
		    'payload-volume-' || g, now() - (g || ' minutes')::interval, 2
		FROM generate_series(1, $3) AS g`, account.ID, statement, scale); err != nil {
		t.Fatalf("seed volume arenas: %v", err)
	}

	content := strings.Repeat("x", 3000)
	contentHash := argumentsdomain.HashContent(content).String()
	for _, relation := range []string{"support", "oppose", "context"} {
		if _, err := world.pool.Exec(ctx, `
			INSERT INTO app.arguments (arena_id, author_id, parent_id, relation, content, content_hash, grapheme_cost, status, created_at, updated_at)
			SELECT $1, $2, NULL, $3, $4, $5, 3000, 'published',
			    now() - (g || ' seconds')::interval, now() - (g || ' seconds')::interval
			FROM generate_series(1, $6) AS g`, arenaID, account.ID, relation, content, contentHash, scale); err != nil {
			t.Fatalf("seed volume arguments: %v", err)
		}
	}

	if _, err := world.pool.Exec(ctx, `
		INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
		SELECT $1, 'credit_free', 'payload-op-' || g, 'payload-volume'
		FROM generate_series(1, $2) AS g`, account.ID, scale); err != nil {
		t.Fatalf("seed volume operations: %v", err)
	}
	if _, err := world.pool.Exec(ctx, `
		INSERT INTO app.wallet_transactions (operation_id, bucket, amount)
		SELECT o.id, 'FREE_INK', 10
		FROM app.wallet_operations o
		WHERE o.account_id = $1`, account.ID); err != nil {
		t.Fatalf("seed volume transactions: %v", err)
	}
	for _, table := range []string{"app.arenas", "app.arguments", "app.wallet_operations", "app.wallet_transactions"} {
		if _, err := world.pool.Exec(ctx, "ANALYZE "+table); err != nil {
			t.Fatalf("analyze %s: %v", table, err)
		}
	}
}

func marshalPayload(t *testing.T, value any) int {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return len(encoded)
}

// feedPayloadRow pins the wire shape of one public feed row for byte
// measurement: the same columns the feed statement returns, with the JSON
// names the transport renders. Domain objects marshal to {} (unexported
// fields), so measuring them would prove nothing about payload size.
type feedPayloadRow struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Statement   string    `json:"statement"`
	Category    string    `json:"category"`
	Language    string    `json:"language"`
	Status      string    `json:"status"`
	PublishedAt time.Time `json:"published_at"`
}

// fetchFeedRows mirrors the public feed statement (same columns, order and
// lookahead cap) for byte measurement; row counts always come from the use
// case, which owns clamping.
func fetchFeedRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, limit int) []feedPayloadRow {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT id::text, slug, statement, category, language, status, published_at
		FROM app.arenas
		WHERE status IN ('published', 'closed', 'restricted')
		ORDER BY published_at DESC, id DESC
		LIMIT $1`, limit)
	if err != nil {
		t.Fatalf("fetch feed rows: %v", err)
	}
	defer rows.Close()
	var out []feedPayloadRow
	for rows.Next() {
		var row feedPayloadRow
		if err := rows.Scan(&row.ID, &row.Slug, &row.Statement, &row.Category, &row.Language, &row.Status, &row.PublishedAt); err != nil {
			t.Fatalf("scan feed row: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate feed rows: %v", err)
	}
	return out
}

// fetchArgumentRows reads one keyset page through the same statement the
// use case runs, returning the DB rows (same columns the wire DTOs carry)
// for byte measurement.
func fetchArgumentRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arena pgtype.UUID, limit int32) []platformpg.ListArenaArgumentsPageRow {
	t.Helper()
	rows, err := platformpg.New(pool).ListArenaArgumentsPage(ctx, platformpg.ListArenaArgumentsPageParams{
		ArenaID: arena, Relation: "support", PageLimit: limit,
	})
	if err != nil {
		t.Fatalf("fetch argument rows: %v", err)
	}
	return rows
}

// fetchStatementRows reads one keyset page through the same statement the
// use case runs, for byte measurement.
func fetchStatementRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account pgtype.UUID, limit int32) []platformpg.ListWalletStatementPageRow {
	t.Helper()
	rows, err := platformpg.New(pool).ListWalletStatementPage(ctx, platformpg.ListWalletStatementPageParams{
		AccountID: account, PageLimit: limit,
	})
	if err != nil {
		t.Fatalf("fetch statement rows: %v", err)
	}
	return rows
}

// TestMaxPagesStayBounded fetches every journey at its contract maximum and
// proves row counts and serialized bytes stay within budget.
func TestMaxPagesStayBounded(t *testing.T) {
	ctx := context.Background()
	world := newPayloadWorld(t)
	seedPayloadVolume(t, ctx, world, 150)

	feed, err := world.feed.Execute(ctx, arenasapp.GetArenaFeedCommand{Limit: 100})
	if err != nil {
		t.Fatalf("feed max page: %v", err)
	}
	if len(feed.Arenas) > 100 {
		t.Fatalf("feed rows = %d, want at most 100", len(feed.Arenas))
	}
	feedRows := fetchFeedRows(t, ctx, world.pool, 101)
	if bytes := marshalPayload(t, feedRows); bytes > maxFeedPageBytes {
		t.Fatalf("feed page = %d bytes, want at most %d", bytes, maxFeedPageBytes)
	} else {
		t.Logf("feed max page: %d rows, %d bytes", len(feedRows), bytes)
	}

	args, err := world.args.Execute(ctx, argumentsapp.ListArenaArgumentsQuery{
		ArenaID: world.arena, Relation: "support", Limit: 100,
	})
	if err != nil {
		t.Fatalf("arguments max page: %v", err)
	}
	if len(args.Arguments) > 100 {
		t.Fatalf("arguments rows = %d, want at most 100", len(args.Arguments))
	}
	argRows := fetchArgumentRows(t, ctx, world.pool, world.arenaUUID, 100)
	if bytes := marshalPayload(t, argRows); bytes > maxArgumentsPageBytes {
		t.Fatalf("arguments page = %d bytes, want at most %d", bytes, maxArgumentsPageBytes)
	} else {
		t.Logf("arguments max page: %d rows, %d bytes", len(argRows), bytes)
	}

	statement, err := world.wallet.Execute(ctx, world.account, "", 100)
	if err != nil {
		t.Fatalf("statement max page: %v", err)
	}
	if len(statement.Entries) > 100 {
		t.Fatalf("statement rows = %d, want at most 100", len(statement.Entries))
	}
	stmtRows := fetchStatementRows(t, ctx, world.pool, world.accountUUID, 100)
	if bytes := marshalPayload(t, stmtRows); bytes > maxStatementPageBytes {
		t.Fatalf("statement page = %d bytes, want at most %d", bytes, maxStatementPageBytes)
	} else {
		t.Logf("statement max page: %d rows, %d bytes", len(stmtRows), bytes)
	}
}

// TestOverLimitClampsEarly proves absurd limits resolve to the same bounded
// pages instead of growing, zero/negative limits fall back to defaults, and
// malformed cursors fail fast instead of scanning.
func TestOverLimitClampsEarly(t *testing.T) {
	ctx := context.Background()
	world := newPayloadWorld(t)
	seedPayloadVolume(t, ctx, world, 150)

	maxFeed, err := world.feed.Execute(ctx, arenasapp.GetArenaFeedCommand{Limit: 100})
	if err != nil {
		t.Fatalf("feed max page: %v", err)
	}
	hugeFeed, err := world.feed.Execute(ctx, arenasapp.GetArenaFeedCommand{Limit: 100000})
	if err != nil {
		t.Fatalf("feed huge limit: %v", err)
	}
	if len(hugeFeed.Arenas) != len(maxFeed.Arenas) {
		t.Fatalf("feed huge limit rows = %d, want clamped %d", len(hugeFeed.Arenas), len(maxFeed.Arenas))
	}
	defaultFeed, err := world.feed.Execute(ctx, arenasapp.GetArenaFeedCommand{Limit: 0})
	if err != nil {
		t.Fatalf("feed zero limit: %v", err)
	}
	if len(defaultFeed.Arenas) == 0 || len(defaultFeed.Arenas) > 20 {
		t.Fatalf("feed zero limit rows = %d, want default page 1..20", len(defaultFeed.Arenas))
	}

	maxArgs, err := world.args.Execute(ctx, argumentsapp.ListArenaArgumentsQuery{
		ArenaID: world.arena, Relation: "support", Limit: 100,
	})
	if err != nil {
		t.Fatalf("arguments max page: %v", err)
	}
	hugeArgs, err := world.args.Execute(ctx, argumentsapp.ListArenaArgumentsQuery{
		ArenaID: world.arena, Relation: "support", Limit: 100000,
	})
	if err != nil {
		t.Fatalf("arguments huge limit: %v", err)
	}
	if len(hugeArgs.Arguments) != len(maxArgs.Arguments) {
		t.Fatalf("arguments huge limit rows = %d, want clamped %d", len(hugeArgs.Arguments), len(maxArgs.Arguments))
	}

	maxStatement, err := world.wallet.Execute(ctx, world.account, "", 100)
	if err != nil {
		t.Fatalf("statement max page: %v", err)
	}
	hugeStatement, err := world.wallet.Execute(ctx, world.account, "", 100000)
	if err != nil {
		t.Fatalf("statement huge limit: %v", err)
	}
	if len(hugeStatement.Entries) != len(maxStatement.Entries) {
		t.Fatalf("statement huge limit rows = %d, want clamped %d", len(hugeStatement.Entries), len(maxStatement.Entries))
	}
	if _, err := world.wallet.Execute(ctx, world.account, "not-a-cursor", 10); err == nil {
		t.Fatal("statement malformed cursor accepted, want fail-fast error")
	}
}

// TestResponseBytesDoNotGrowWithDataset proves the ceilings above are
// properties of the page contract, not of one dataset size: quadrupling the
// volume returns identical row counts within the same byte ceilings.
func TestResponseBytesDoNotGrowWithDataset(t *testing.T) {
	ctx := context.Background()

	small := newPayloadWorld(t)
	seedPayloadVolume(t, ctx, small, 120)
	smallFeed, err := small.feed.Execute(ctx, arenasapp.GetArenaFeedCommand{Limit: 100})
	if err != nil {
		t.Fatalf("small feed: %v", err)
	}
	smallArgs, err := small.args.Execute(ctx, argumentsapp.ListArenaArgumentsQuery{
		ArenaID: small.arena, Relation: "support", Limit: 100,
	})
	if err != nil {
		t.Fatalf("small arguments: %v", err)
	}

	large := newPayloadWorld(t)
	seedPayloadVolume(t, ctx, large, 480)
	largeFeed, err := large.feed.Execute(ctx, arenasapp.GetArenaFeedCommand{Limit: 100})
	if err != nil {
		t.Fatalf("large feed: %v", err)
	}
	largeArgs, err := large.args.Execute(ctx, argumentsapp.ListArenaArgumentsQuery{
		ArenaID: large.arena, Relation: "support", Limit: 100,
	})
	if err != nil {
		t.Fatalf("large arguments: %v", err)
	}

	if len(largeFeed.Arenas) != len(smallFeed.Arenas) {
		t.Fatalf("feed rows changed with dataset: %d vs %d", len(largeFeed.Arenas), len(smallFeed.Arenas))
	}
	if len(largeArgs.Arguments) != len(smallArgs.Arguments) {
		t.Fatalf("arguments rows changed with dataset: %d vs %d", len(largeArgs.Arguments), len(smallArgs.Arguments))
	}
	pages := []struct {
		name    string
		rows    any
		ceiling int
	}{
		{"small feed", fetchFeedRows(t, ctx, small.pool, 101), maxFeedPageBytes},
		{"large feed", fetchFeedRows(t, ctx, large.pool, 101), maxFeedPageBytes},
		{"small arguments", fetchArgumentRows(t, ctx, small.pool, small.arenaUUID, 100), maxArgumentsPageBytes},
		{"large arguments", fetchArgumentRows(t, ctx, large.pool, large.arenaUUID, 100), maxArgumentsPageBytes},
	}
	for _, page := range pages {
		if bytes := marshalPayload(t, page.rows); bytes > page.ceiling {
			t.Fatalf("%s = %d bytes, want at most %d", page.name, bytes, page.ceiling)
		} else {
			t.Logf("%s: %d bytes within %d", page.name, bytes, page.ceiling)
		}
	}
}

// TestExportDocumentStaysBounded proves a reference-scale export document
// serializes within budget and oversized content is refused at the domain
// boundary instead of growing the document.
func TestExportDocumentStaysBounded(t *testing.T) {
	at := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	arguments := make([]profilesapp.PersonalExportArgument, 0, 200)
	for i := 0; i < 200; i++ {
		arguments = append(arguments, profilesapp.PersonalExportArgument{
			ID:        "018f6b2a-0000-7000-8000-000000000001",
			ArenaID:   "018f6b2a-0000-7000-8000-000000000002",
			Relation:  "support",
			Content:   strings.Repeat("x", 3000),
			Status:    "published",
			CreatedAt: at,
		})
	}
	document := profilesapp.PersonalExportDocument{
		SchemaVersion:      1,
		GeneratedAt:        at,
		ExcludedCategories: []string{"moderation"},
		PersonalExportSections: profilesapp.PersonalExportSections{
			Account: profilesapp.PersonalExportAccount{
				ID:            "018f6b2a-0000-7000-8000-000000000003",
				Email:         "export-budget@arena.example.com",
				Status:        "active",
				EmailVerified: true,
				CreatedAt:     at,
			},
			Arguments: arguments,
		},
	}
	encoded, err := exportjson.NewEncoder().EncodePersonalExport(document)
	if err != nil {
		t.Fatalf("encode reference export: %v", err)
	}
	if len(encoded) > maxExportDocBytes {
		t.Fatalf("reference export = %d bytes, want at most %d", len(encoded), maxExportDocBytes)
	}
	t.Logf("reference export (200 max-length arguments): %d bytes within %d", len(encoded), maxExportDocBytes)

	if _, err := argumentsdomain.ParseContent(strings.Repeat("x", 3001), text.GraphemeCount); err == nil {
		t.Fatal("3001-grapheme content accepted, want fail-fast domain refusal")
	}
}
