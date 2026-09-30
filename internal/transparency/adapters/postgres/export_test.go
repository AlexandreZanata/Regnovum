package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	transparencypg "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/application"
)

const (
	exportTestCursorSecret = "0123456789abcdef0123456789abcdef"
	exportWithdrawnSecret  = "withdrawn content that must never serialize"
)

var exportBaseInstant = time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)

// uuidText renders a pgtype.UUID in canonical form for assertions.
func uuidText(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func seedExportAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email, status string, verified bool) platformpg.AppAccount {
	t.Helper()
	account, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: status})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	if verified {
		if _, err := q.SetEmailVerified(ctx, account.ID); err != nil {
			t.Fatalf("verify account %s: %v", email, err)
		}
	}
	return account
}

func seedExportArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator pgtype.UUID, slug, status string, publishedAt time.Time) pgtype.UUID {
	t.Helper()
	var published any
	if !publishedAt.IsZero() {
		published = publishedAt
	}
	var address any
	if slug != "" {
		address = slug
	}
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, context, category, language, status, slug, published_at)
		VALUES ($1, 'Export probe statement', 'Export probe context', 'technology', 'pt-BR', $2, $3, $4)
		RETURNING id`, creator, status, address, published).Scan(&id); err != nil {
		t.Fatalf("seed arena %s: %v", slug, err)
	}
	return id
}

func seedExportArgument(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID, authorID pgtype.UUID, relation, content, status string, createdAt time.Time) pgtype.UUID {
	t.Helper()
	var withdrawnAt any
	if status == "withdrawn" {
		withdrawnAt = createdAt.Add(time.Hour)
	}
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments
			(arena_id, author_id, relation, content, content_hash, grapheme_cost, status, created_at, updated_at, withdrawn_at)
		VALUES ($1, $2, $3, $4, 'export-probe-hash', 10, $5, $6, $6, $7)
		RETURNING id`, arenaID, authorID, relation, content, status, createdAt, withdrawnAt).Scan(&id); err != nil {
		t.Fatalf("seed argument %s: %v", content, err)
	}
	return id
}

func seedExportSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, argumentID pgtype.UUID, url string, description any, createdAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.argument_sources (argument_id, url, description, created_at)
		VALUES ($1, $2, $3, $4)`, argumentID, url, description, createdAt); err != nil {
		t.Fatalf("seed source %s: %v", url, err)
	}
}

func seedExportPosition(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID, accountID pgtype.UUID, initial, current string, version int) pgtype.UUID {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position, version)
		VALUES ($1, $2, $3, $4, $5)`, arenaID, accountID, initial, current, version); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	if version < 2 {
		return pgtype.UUID{}
	}
	var changeID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.position_changes (arena_id, account_id, from_position, to_position, version, changed_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`, arenaID, accountID, initial, current, version, exportBaseInstant.Add(time.Duration(version)*time.Hour)).Scan(&changeID); err != nil {
		t.Fatalf("seed position change: %v", err)
	}
	return changeID
}

func seedExportAttribution(t *testing.T, ctx context.Context, pool *pgxpool.Pool, changeID, attributorID, argumentID pgtype.UUID, status string, createdAt time.Time) {
	t.Helper()
	if status == "invalid" {
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.persuasion_attributions
				(position_change_id, attributor_id, argument_id, status, created_at, invalidated_at, moderation_reason, moderated_by, moderated_at)
			VALUES ($1, $2, $3, 'invalid', $4, $4, 'export probe', $2, $4)`,
			changeID, attributorID, argumentID, createdAt); err != nil {
			t.Fatalf("seed invalid attribution: %v", err)
		}
		return
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.persuasion_attributions (position_change_id, attributor_id, argument_id, status, created_at)
		VALUES ($1, $2, $3, 'valid', $4)`,
		changeID, attributorID, argumentID, createdAt); err != nil {
		t.Fatalf("seed attribution: %v", err)
	}
}

// exportHarness seeds one Arena with eligible and ineligible participants,
// published, withdrawn and removed arguments, sources and valid, invalid
// and ineligible attributions, plus a second Arena and lifecycle states.
type exportHarness struct {
	repo     *transparencypg.Repository
	uc       *application.GetArenaExportUseCase
	pool     *pgxpool.Pool
	arenaID  pgtype.UUID
	authorID []pgtype.UUID
	arg1     pgtype.UUID
	arg2     pgtype.UUID
	arg3     pgtype.UUID
	arg4     pgtype.UUID
	foreign  pgtype.UUID
	draft    pgtype.UUID
	removed  pgtype.UUID
}

func setupExportHarness(t *testing.T) *exportHarness {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	q := platformpg.New(pool)
	repo := transparencypg.NewRepository(pool)

	codec, err := application.NewExportCursorCodec([]byte(exportTestCursorSecret))
	if err != nil {
		t.Fatalf("NewExportCursorCodec: %v", err)
	}
	uc, err := application.NewGetArenaExportUseCase(repo, codec)
	if err != nil {
		t.Fatalf("NewGetArenaExportUseCase: %v", err)
	}

	// Six active verified participants, one suspended account and one
	// active unverified account (eligible for positions, never for
	// influence).
	var authors []pgtype.UUID
	for i := 0; i < 6; i++ {
		account := seedExportAccount(t, ctx, q, fmt.Sprintf("export-author-%d@arena.example.com", i), "active", true)
		authors = append(authors, account.ID)
	}
	suspended := seedExportAccount(t, ctx, q, "export-suspended@arena.example.com", "suspended", false)
	unverified := seedExportAccount(t, ctx, q, "export-unverified@arena.example.com", "active", false)

	arenaID := seedExportArena(t, ctx, pool, authors[0], "export-probe-arena", "published", exportBaseInstant)

	// Positions: five changes among the participants plus one from the
	// active unverified account; suspended positions never count.
	changes := make([]pgtype.UUID, 0, 6)
	changes = append(changes, seedExportPosition(t, ctx, pool, arenaID, authors[0], "agree", "undecided", 2))
	changes = append(changes, seedExportPosition(t, ctx, pool, arenaID, authors[1], "agree", "disagree", 2))
	changes = append(changes, seedExportPosition(t, ctx, pool, arenaID, authors[2], "agree", "disagree", 2))
	changes = append(changes, seedExportPosition(t, ctx, pool, arenaID, authors[3], "disagree", "agree", 2))
	changes = append(changes, seedExportPosition(t, ctx, pool, arenaID, authors[4], "disagree", "undecided", 2))
	seedExportPosition(t, ctx, pool, arenaID, authors[5], "undecided", "undecided", 1)
	seedExportPosition(t, ctx, pool, arenaID, suspended.ID, "agree", "agree", 1)
	changes = append(changes, seedExportPosition(t, ctx, pool, arenaID, unverified.ID, "disagree", "undecided", 2))

	// Arguments: oldest is removed (never listed), then published with two
	// sources, published without sources and withdrawn with a source whose
	// content and sources must stay out of the document.
	arg4 := seedExportArgument(t, ctx, pool, arenaID, authors[3], "support", "removed probe argument", "removed", exportBaseInstant.Add(-time.Hour))
	arg1 := seedExportArgument(t, ctx, pool, arenaID, authors[0], "support", "First public argument", "published", exportBaseInstant.Add(time.Hour))
	arg2 := seedExportArgument(t, ctx, pool, arenaID, authors[1], "oppose", "Second public argument", "published", exportBaseInstant.Add(2*time.Hour))
	arg3 := seedExportArgument(t, ctx, pool, arenaID, authors[2], "context", exportWithdrawnSecret, "withdrawn", exportBaseInstant.Add(3*time.Hour))
	seedExportSource(t, ctx, pool, arg1, "https://example.com/first", "First source description", exportBaseInstant.Add(time.Hour))
	seedExportSource(t, ctx, pool, arg1, "https://example.com/second", nil, exportBaseInstant.Add(time.Hour+time.Minute))
	seedExportSource(t, ctx, pool, arg3, "https://example.com/withdrawn", "Withdrawn source", exportBaseInstant.Add(3*time.Hour))

	// Attributions: arg1 receives three valid eligible events, arg2 one
	// valid eligible plus one unverified, arg3 one valid while withdrawn
	// (historical fact), and arg1 one invalid.
	seedExportAttribution(t, ctx, pool, changes[0], authors[0], arg1, "valid", exportBaseInstant.Add(4*time.Hour))
	seedExportAttribution(t, ctx, pool, changes[1], authors[1], arg1, "valid", exportBaseInstant.Add(4*time.Hour))
	seedExportAttribution(t, ctx, pool, changes[3], authors[3], arg1, "valid", exportBaseInstant.Add(4*time.Hour))
	seedExportAttribution(t, ctx, pool, changes[2], authors[2], arg1, "invalid", exportBaseInstant.Add(4*time.Hour))
	seedExportAttribution(t, ctx, pool, changes[0], authors[0], arg2, "valid", exportBaseInstant.Add(4*time.Hour))
	seedExportAttribution(t, ctx, pool, changes[5], unverified.ID, arg2, "valid", exportBaseInstant.Add(4*time.Hour))
	seedExportAttribution(t, ctx, pool, changes[4], authors[4], arg3, "valid", exportBaseInstant.Add(4*time.Hour))

	// A second Arena must never feed the first one's export.
	foreignArena := seedExportArena(t, ctx, pool, authors[4], "export-foreign-arena", "published", exportBaseInstant)
	foreignArgument := seedExportArgument(t, ctx, pool, foreignArena, authors[4], "support", "Foreign argument", "published", exportBaseInstant.Add(time.Hour))
	seedExportAttribution(t, ctx, pool, changes[3], authors[3], foreignArgument, "valid", exportBaseInstant.Add(4*time.Hour))

	draftArena := seedExportArena(t, ctx, pool, authors[0], "", "draft", time.Time{})
	removedArena := seedExportArena(t, ctx, pool, authors[0], "export-removed-arena", "removed", exportBaseInstant)

	return &exportHarness{
		repo: repo, uc: uc, pool: pool, arenaID: arenaID,
		authorID: authors, arg1: arg1, arg2: arg2, arg3: arg3, arg4: arg4,
		foreign: foreignArena, draft: draftArena, removed: removedArena,
	}
}

func TestExportReconstructsPublicArena(t *testing.T) {
	harness := setupExportHarness(t)
	ctx := context.Background()

	page, err := harness.uc.Execute(ctx, application.GetArenaExportQuery{ArenaID: uuidText(harness.arenaID), Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if page.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", page.SchemaVersion)
	}
	arena := page.Arena
	if arena.ID != uuidText(harness.arenaID) || arena.Slug != "export-probe-arena" || arena.Status != "published" {
		t.Fatalf("arena identity = %+v", arena)
	}
	if arena.Statement != "Export probe statement" || arena.Context != "Export probe context" || arena.Category != "technology" || arena.Language != "pt-BR" {
		t.Fatalf("arena content = %+v", arena)
	}
	if !arena.PublishedAt.Equal(exportBaseInstant) || arena.ClosesAt != nil {
		t.Fatalf("arena dates = %+v", arena)
	}

	// Eligible participants only: seven active accounts (the suspended one
	// never counts), five initial changes plus the unverified account's.
	positions := page.Positions
	if positions.Suppressed {
		t.Fatalf("seven eligible participants must not suppress: %+v", positions)
	}
	if positions.Participants != 7 || positions.PositionChanges != 6 {
		t.Fatalf("participant aggregates = %+v, want 7 eligible and 6 changes", positions)
	}
	if positions.Initial != (application.ExportDistribution{Agree: 3, Disagree: 3, Undecided: 1}) {
		t.Fatalf("initial distribution = %+v", positions.Initial)
	}
	if positions.Current != (application.ExportDistribution{Agree: 1, Disagree: 2, Undecided: 4}) {
		t.Fatalf("current distribution = %+v", positions.Current)
	}

	// Valid influence only, eligible attributors only, first Arena only:
	// three eligible events on arg1, one on arg2 and one on the withdrawn
	// arg3; the invalid and unverified events never count.
	if page.Influence.ValidAttributions != 5 || page.Influence.InfluencedAuthors != 3 {
		t.Fatalf("arena influence = %+v, want 5 valid events and 3 authors", page.Influence)
	}

	// Arguments: oldest first, removed excluded, withdrawn as a placeholder.
	if len(page.Arguments) != 3 {
		t.Fatalf("arguments = %d, want 3", len(page.Arguments))
	}
	if page.Arguments[0].ID != uuidText(harness.arg1) || page.Arguments[1].ID != uuidText(harness.arg2) || page.Arguments[2].ID != uuidText(harness.arg3) {
		t.Fatalf("argument order = %s, %s, %s", page.Arguments[0].ID, page.Arguments[1].ID, page.Arguments[2].ID)
	}
	if page.NextCursor != "" {
		t.Fatalf("next_cursor = %q, want empty on the last page", page.NextCursor)
	}

	first := page.Arguments[0]
	if first.Content == nil || *first.Content != "First public argument" || first.Status != "published" {
		t.Fatalf("first argument = %+v", first)
	}
	if len(first.Sources) != 2 || first.Sources[0].URL != "https://example.com/first" || first.Sources[0].Description == nil || first.Sources[1].Description != nil {
		t.Fatalf("first argument sources = %+v", first.Sources)
	}
	if first.Influence.ValidAttributions != 3 || first.Influence.DistinctPeople != 3 {
		t.Fatalf("first argument influence = %+v", first.Influence)
	}

	second := page.Arguments[1]
	if second.Influence.ValidAttributions != 1 || second.Influence.DistinctPeople != 1 {
		t.Fatalf("second argument influence = %+v, ineligible attributor must not count", second.Influence)
	}
	if len(second.Sources) != 0 {
		t.Fatalf("second argument sources = %+v, want none", second.Sources)
	}

	withdrawn := page.Arguments[2]
	if withdrawn.Status != "withdrawn" || withdrawn.Content != nil || len(withdrawn.Sources) != 0 {
		t.Fatalf("withdrawn argument must withhold content and sources: %+v", withdrawn)
	}
	if withdrawn.WithdrawnAt == nil {
		t.Fatalf("withdrawn argument must keep its withdrawal instant: %+v", withdrawn)
	}
	if withdrawn.Influence.ValidAttributions != 1 {
		t.Fatalf("withdrawn argument influence = %+v, attribution facts survive withdrawal", withdrawn.Influence)
	}

	// No account identifier ever crosses the projection, not even through a
	// debug rendering of the page.
	rendered := fmt.Sprintf("%+v", page)
	for _, account := range append(append([]pgtype.UUID{}, harness.authorID...), harness.foreign) {
		if identifier := uuidText(account); identifier != "" && strings.Contains(rendered, identifier) {
			t.Fatalf("export leaks account identifier %s", identifier)
		}
	}
	if strings.Contains(rendered, exportWithdrawnSecret) {
		t.Fatal("export leaks withdrawn content")
	}
	if strings.Contains(rendered, uuidText(harness.arg4)) {
		t.Fatal("export leaks the removed argument")
	}
}

func TestExportKeepsUnboundedArenaPageBounded(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	q := platformpg.New(pool)
	repo := transparencypg.NewRepository(pool)

	codec, err := application.NewExportCursorCodec([]byte(exportTestCursorSecret))
	if err != nil {
		t.Fatalf("NewExportCursorCodec: %v", err)
	}
	uc, err := application.NewGetArenaExportUseCase(repo, codec)
	if err != nil {
		t.Fatalf("NewGetArenaExportUseCase: %v", err)
	}

	author := seedExportAccount(t, ctx, q, "export-large@arena.example.com", "active", true)
	arenaID := seedExportArena(t, ctx, pool, author.ID, "export-large-arena", "published", exportBaseInstant)

	const total = 150
	for i := 0; i < total; i++ {
		seedExportArgument(t, ctx, pool, arenaID, author.ID, "support",
			fmt.Sprintf("Large arena argument %03d", i), "published",
			exportBaseInstant.Add(time.Duration(i)*time.Minute))
	}

	limit := application.DefaultExportPageLimit
	cursor := ""
	collected := 0
	pages := 0
	for {
		page, err := uc.Execute(ctx, application.GetArenaExportQuery{
			ArenaID: uuidText(arenaID), Cursor: cursor, Limit: limit,
		})
		if err != nil {
			t.Fatalf("Execute page %d: %v", pages, err)
		}
		if len(page.Arguments) > limit {
			t.Fatalf("page %d delivered %d arguments, want at most %d", pages, len(page.Arguments), limit)
		}
		collected += len(page.Arguments)
		pages++
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > total {
			t.Fatal("pagination did not terminate")
		}
	}
	if collected != total || pages != 8 {
		t.Fatalf("traversal delivered %d arguments in %d pages, want %d in 8", collected, pages, total)
	}
}

func TestExportUnknownDraftAndRemovedArenasAreNotFound(t *testing.T) {
	harness := setupExportHarness(t)
	ctx := context.Background()

	for name, arenaID := range map[string]pgtype.UUID{
		"unknown": {Bytes: [16]byte{1, 2, 3}, Valid: true},
		"draft":   harness.draft,
		"removed": harness.removed,
	} {
		name, arenaID := name, arenaID
		t.Run(name, func(t *testing.T) {
			_, err := harness.uc.Execute(ctx, application.GetArenaExportQuery{ArenaID: uuidText(arenaID)})
			if !errors.Is(err, application.ErrArenaNotFound) {
				t.Fatalf("error = %v, want ErrArenaNotFound", err)
			}
		})
	}
}
