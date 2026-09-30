package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

const persuasionHash = "v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// uuidText renders a database UUID in canonical form.
func uuidText(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func mustPersuasionAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) pgtype.UUID {
	t.Helper()
	account, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return account.ID
}

func mustPersuasionArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator pgtype.UUID, slug string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Afirmação para atribuições de persuasão', 'technology', 'pt-BR', 'draft')
		RETURNING id`, creator).Scan(&id); err != nil {
		t.Fatalf("insert arena: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE app.arenas SET status = 'published', slug = $2, published_at = now(), version = version + 1
		WHERE id = $1`, id, slug); err != nil {
		t.Fatalf("publish arena: %v", err)
	}
	return id
}

func mustPersuasionArgument(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID, authorID pgtype.UUID, statement, status string, createdAt time.Time) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost, status, created_at, updated_at)
		VALUES ($1, $2, 'support', $3, $4, 30, $5, $6, $6)
		RETURNING id`, arenaID, authorID, statement, persuasionHash, status, createdAt).Scan(&id); err != nil {
		t.Fatalf("insert argument %q: %v", statement, err)
	}
	return id
}

func mustPersuasionChange(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID, accountID pgtype.UUID, changedAt time.Time) pgtype.UUID {
	t.Helper()
	return mustPersuasionChangeAt(t, ctx, pool, arenaID, accountID, "agree", "disagree", 2, changedAt)
}

// mustPersuasionChangeAt seeds one more change of the same account in the
// same Arena: the projection upsert moves current_position/version without
// touching the immutable initial choice, and the chain advances by version.
func mustPersuasionChangeAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID, accountID pgtype.UUID, from, to string, version int32, changedAt time.Time) pgtype.UUID {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position, version)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (arena_id, account_id) DO UPDATE
		SET current_position = EXCLUDED.current_position,
		    version = EXCLUDED.version,
		    updated_at = now()`, arenaID, accountID, from, to, version); err != nil {
		t.Fatalf("upsert position: %v", err)
	}
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.position_changes (arena_id, account_id, from_position, to_position, version, changed_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`, arenaID, accountID, from, to, version, changedAt).Scan(&id); err != nil {
		t.Fatalf("insert position change: %v", err)
	}
	return id
}

func newPersuasionHarness(t *testing.T) (*pgxpool.Pool, *application.RecordAttributionsUseCase, pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	attributor := mustPersuasionAccount(t, ctx, q, "persuasion-owner@arena.example.com")
	author := mustPersuasionAccount(t, ctx, q, "persuasion-author@arena.example.com")
	arena := mustPersuasionArena(t, ctx, pool, attributor, "persuasion-arena")

	repo := postgres.NewRepository(pool)
	useCase := application.NewRecordAttributionsUseCase(repo, domain.DefaultEligibilityPolicy(), platformpg.NewTxManager(pool))
	return pool, useCase, attributor, author, arena
}

func recordAttributionsCommand(attributor, changeID pgtype.UUID, argumentIDs ...pgtype.UUID) application.RecordAttributionsCommand {
	command := application.RecordAttributionsCommand{
		AccountID: uuidText(attributor),
		ChangeID:  uuidText(changeID),
	}
	for _, argumentID := range argumentIDs {
		command.ArgumentIDs = append(command.ArgumentIDs, uuidText(argumentID))
	}
	return command
}

func attributionRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, changeID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM app.persuasion_attributions WHERE position_change_id = $1`, changeID).Scan(&count); err != nil {
		t.Fatalf("count attributions: %v", err)
	}
	return count
}

func TestRecordAttributionsEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool, useCase, attributor, author, arena := newPersuasionHarness(t)

	changeAt := time.Now().UTC()
	change := mustPersuasionChange(t, ctx, pool, arena, attributor, changeAt)
	first := mustPersuasionArgument(t, ctx, pool, arena, author, "Primeiro argumento elegível", "published", changeAt.Add(-time.Hour))
	second := mustPersuasionArgument(t, ctx, pool, arena, author, "Segundo argumento elegível", "published", changeAt.Add(-time.Hour))
	third := mustPersuasionArgument(t, ctx, pool, arena, author, "Terceiro argumento elegível", "published", changeAt.Add(-time.Hour))
	fourth := mustPersuasionArgument(t, ctx, pool, arena, author, "Quarto argumento elegível", "published", changeAt.Add(-time.Hour))

	// Two arguments are recorded and the retry resolves the same set.
	recorded, err := useCase.Execute(ctx, recordAttributionsCommand(attributor, change, first, second))
	if err != nil {
		t.Fatalf("record Execute() error = %v", err)
	}
	if recorded.Replayed || len(recorded.ArgumentIDs) != 2 {
		t.Fatalf("recorded = %+v, want two fresh attributions", recorded)
	}
	retry, err := useCase.Execute(ctx, recordAttributionsCommand(attributor, change, second, first))
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if !retry.Replayed || len(retry.ArgumentIDs) != 2 {
		t.Fatalf("retry = %+v, want the recorded set replayed", retry)
	}
	if count := attributionRowCount(t, ctx, pool, change); count != 2 {
		t.Fatalf("attributions = %d, want the two recorded", count)
	}

	// The third argument completes the limit; the fourth is refused.
	if _, err := useCase.Execute(ctx, recordAttributionsCommand(attributor, change, third)); err != nil {
		t.Fatalf("third Execute() error = %v", err)
	}
	if _, err := useCase.Execute(ctx, recordAttributionsCommand(attributor, change, fourth)); !errors.Is(err, domain.ErrTooManyAttributions) {
		t.Fatalf("fourth error = %v, want ErrTooManyAttributions", err)
	}
	if count := attributionRowCount(t, ctx, pool, change); count != 3 {
		t.Fatalf("attributions = %d, want exactly three", count)
	}

	// Skipping on a fresh change creates no fake rows.
	otherChange := mustPersuasionChangeAt(t, ctx, pool, arena, attributor, "disagree", "undecided", 3, changeAt.Add(time.Minute))
	skipped, err := useCase.Execute(ctx, recordAttributionsCommand(attributor, otherChange))
	if err != nil {
		t.Fatalf("skip Execute() error = %v", err)
	}
	if len(skipped.ArgumentIDs) != 0 || skipped.Replayed {
		t.Fatalf("skip = %+v, want an empty fresh selection", skipped)
	}
	if count := attributionRowCount(t, ctx, pool, otherChange); count != 0 {
		t.Fatalf("skip created %d rows, want none", count)
	}
}

func TestRecordAttributionsRejectsInvalidSelectionsEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool, useCase, attributor, author, arena := newPersuasionHarness(t)

	changeAt := time.Now().UTC()
	change := mustPersuasionChange(t, ctx, pool, arena, attributor, changeAt)
	eligible := mustPersuasionArgument(t, ctx, pool, arena, author, "Argumento elegível", "published", changeAt.Add(-time.Hour))
	withdrawn := mustPersuasionArgument(t, ctx, pool, arena, author, "Argumento retirado", "withdrawn", changeAt.Add(-time.Hour))
	removed := mustPersuasionArgument(t, ctx, pool, arena, author, "Argumento removido", "removed", changeAt.Add(-time.Hour))
	late := mustPersuasionArgument(t, ctx, pool, arena, author, "Argumento posterior à mudança", "published", changeAt.Add(time.Hour))
	selfAuthored := mustPersuasionArgument(t, ctx, pool, arena, attributor, "Argumento do próprio attributor", "published", changeAt.Add(-time.Hour))
	otherArena := mustPersuasionArena(t, ctx, pool, author, "persuasion-other-arena")
	crossArena := mustPersuasionArgument(t, ctx, pool, otherArena, author, "Argumento de outra Arena", "published", changeAt.Add(-time.Hour))

	tests := []struct {
		name      string
		arguments []pgtype.UUID
		want      error
	}{
		{name: "withdrawn", arguments: []pgtype.UUID{withdrawn}, want: domain.ErrArgumentNotEligible},
		{name: "removed", arguments: []pgtype.UUID{removed}, want: domain.ErrArgumentNotEligible},
		{name: "after the change", arguments: []pgtype.UUID{late}, want: domain.ErrArgumentNotBeforeChange},
		{name: "self attribution", arguments: []pgtype.UUID{selfAuthored}, want: domain.ErrSelfAttribution},
		{name: "cross arena", arguments: []pgtype.UUID{crossArena}, want: domain.ErrCrossArenaArgument},
		{name: "duplicate", arguments: []pgtype.UUID{eligible, eligible}, want: domain.ErrDuplicateAttribution},
		{name: "four at once", arguments: []pgtype.UUID{eligible, eligible, eligible, eligible}, want: domain.ErrTooManyAttributions},
		{name: "unknown argument", arguments: []pgtype.UUID{{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef}, Valid: true}}, want: application.ErrArgumentNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := useCase.Execute(ctx, recordAttributionsCommand(attributor, change, test.arguments...)); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if count := attributionRowCount(t, ctx, pool, change); count != 0 {
		t.Fatalf("attributions = %d, want no rows after refusals", count)
	}

	// A foreign account never reaches the change.
	foreign, err := useCase.Execute(ctx, recordAttributionsCommand(author, change, eligible))
	if !errors.Is(err, application.ErrChangeNotFound) {
		t.Fatalf("foreign change error = %v, want ErrChangeNotFound", err)
	}
	if foreign != nil {
		t.Fatal("a foreign change must not return a result")
	}
	if count := attributionRowCount(t, ctx, pool, change); count != 0 {
		t.Fatalf("attributions = %d, want no rows after a foreign attempt", count)
	}
}

// TestRecordAttributionsConcurrentLimit proves the FOR UPDATE lock on the
// change row: two simultaneous disjoint selections cannot push the total
// above three.
func TestRecordAttributionsConcurrentLimit(t *testing.T) {
	ctx := context.Background()
	pool, useCase, attributor, author, arena := newPersuasionHarness(t)

	changeAt := time.Now().UTC()
	change := mustPersuasionChange(t, ctx, pool, arena, attributor, changeAt)
	first := mustPersuasionArgument(t, ctx, pool, arena, author, "Concorrente um", "published", changeAt.Add(-time.Hour))
	second := mustPersuasionArgument(t, ctx, pool, arena, author, "Concorrente dois", "published", changeAt.Add(-time.Hour))
	third := mustPersuasionArgument(t, ctx, pool, arena, author, "Concorrente três", "published", changeAt.Add(-time.Hour))
	fourth := mustPersuasionArgument(t, ctx, pool, arena, author, "Concorrente quatro", "published", changeAt.Add(-time.Hour))

	start := make(chan struct{})
	errs := make(chan error, 2)
	var waitGroup sync.WaitGroup
	selections := [][]pgtype.UUID{{first, second}, {third, fourth}}
	for _, selection := range selections {
		waitGroup.Add(1)
		go func(selection []pgtype.UUID) {
			defer waitGroup.Done()
			<-start
			_, err := useCase.Execute(ctx, recordAttributionsCommand(attributor, change, selection...))
			errs <- err
		}(selection)
	}
	close(start)
	waitGroup.Wait()
	close(errs)

	successes := 0
	refusals := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrTooManyAttributions):
			refusals++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || refusals != 1 {
		t.Fatalf("successes = %d, refusals = %d, want one of each", successes, refusals)
	}
	if count := attributionRowCount(t, ctx, pool, change); count != 2 {
		t.Fatalf("attributions = %d, want the winner's two", count)
	}

	// A smaller second selection fits under the limit: 2 + 1 = 3.
	otherChange := mustPersuasionChangeAt(t, ctx, pool, arena, attributor, "disagree", "undecided", 3, changeAt.Add(time.Minute))
	if _, err := useCase.Execute(ctx, recordAttributionsCommand(attributor, otherChange, first, second)); err != nil {
		t.Fatalf("seed other change: %v", err)
	}
	if _, err := useCase.Execute(ctx, recordAttributionsCommand(attributor, otherChange, third)); err != nil {
		t.Fatalf("growing selection error = %v", err)
	}
	if count := attributionRowCount(t, ctx, pool, otherChange); count != 3 {
		t.Fatalf("attributions = %d, want three", count)
	}
}
