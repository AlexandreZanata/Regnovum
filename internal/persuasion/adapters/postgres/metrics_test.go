package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// metricsHarness wires the public reads over one isolated database.
type metricsHarness struct {
	pool    *pgxpool.Pool
	repo    *postgres.Repository
	record  *application.RecordAttributionsUseCase
	metrics *application.GetArgumentMetricsUseCase
	profile *application.GetProfileReputationUseCase
	author  pgtype.UUID
	arena   pgtype.UUID
}

func newMetricsHarness(t *testing.T) *metricsHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	author := mustReputationAccount(t, ctx, q, pool, "metrics-author@arena.example.com", "active", true)
	arena := mustReputationArena(t, ctx, pool, author, "metrics-public-arena", "technology", "pt-BR")

	repo := postgres.NewRepository(pool)
	clock := clockseed.NewClock()

	return &metricsHarness{
		pool:    pool,
		repo:    repo,
		record:  application.NewRecordAttributionsUseCase(repo, domain.DefaultEligibilityPolicy(), platformpg.NewTxManager(pool)),
		metrics: application.NewGetArgumentMetricsUseCase(repo, clock),
		profile: application.NewGetProfileReputationUseCase(repo, repo, clock),
		author:  author,
		arena:   arena,
	}
}

// mustProfile creates the public profile of one account: the username is the
// only handle the public reputation route accepts.
func mustProfile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account pgtype.UUID, username string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.profiles (account_id, username, username_normalized)
		VALUES ($1, $2, lower($2))`, account, username); err != nil {
		t.Fatalf("insert profile %q: %v", username, err)
	}
}

// mustAttributedArguments records one attribution of the given arguments from
// one change of the attributor, through the real recording use case.
func (h *metricsHarness) mustAttribute(t *testing.T, ctx context.Context, attributor pgtype.UUID, version int32, argumentIDs ...pgtype.UUID) {
	t.Helper()
	changeID := mustPersuasionChangeAt(t, ctx, h.pool, h.arena, attributor, "agree", "disagree", version, time.Now().UTC())
	if _, err := h.record.Execute(ctx, recordAttributionsCommand(attributor, changeID, argumentIDs...)); err != nil {
		t.Fatalf("record attribution: %v", err)
	}
}

// mustInvalidateAttribution drives the guarded invalidation exactly as
// moderation does: the retained row moves to invalid with its decision
// record, and the counts must follow.
func mustInvalidateAttribution(t *testing.T, ctx context.Context, pool *pgxpool.Pool, changeID, argumentID, moderator pgtype.UUID) {
	t.Helper()
	decidedAt := time.Now().UTC()
	tag, err := pool.Exec(ctx, `
		UPDATE app.persuasion_attributions
		SET status = 'invalid',
		    invalidated_at = $3,
		    moderation_reason = 'atribuição fraudulenta',
		    moderated_by = $4,
		    moderated_at = $3
		WHERE position_change_id = $1 AND argument_id = $2`, changeID, argumentID, decidedAt, moderator)
	if err != nil {
		t.Fatalf("invalidate attribution: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("invalidation touched %d rows, want exactly 1", tag.RowsAffected())
	}
}

func (h *metricsHarness) metricsFor(t *testing.T, argumentID pgtype.UUID) *application.ArgumentMetrics {
	t.Helper()
	metrics, err := h.metrics.Execute(context.Background(), application.GetArgumentMetricsQuery{ArgumentID: uuidText(argumentID)})
	if err != nil {
		t.Fatalf("get argument metrics: %v", err)
	}
	return metrics
}

// TestArgumentMetricsCountPeopleOnceAndFollowValidity proves the public count
// of one argument against the real database: eligible people are counted once
// even when they credited the same argument from two changes, ineligible
// accounts (suspended or without a verified email) never integrate the count,
// an invalidated attribution leaves the valid total and restoring it brings
// the fact back (REQ-PERS-05/06/08).
func TestArgumentMetricsCountPeopleOnceAndFollowValidity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newMetricsHarness(t)
	q := platformpg.New(h.pool)

	first := mustReputationAccount(t, ctx, q, h.pool, "metrics-first@arena.example.com", "active", true)
	second := mustReputationAccount(t, ctx, q, h.pool, "metrics-second@arena.example.com", "active", true)
	suspended := mustReputationAccount(t, ctx, q, h.pool, "metrics-suspended@arena.example.com", "suspended", true)
	unverified := mustReputationAccount(t, ctx, q, h.pool, "metrics-unverified@arena.example.com", "active", false)

	argument := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Argumento com atribuições públicas", "published", time.Now().UTC().Add(-time.Hour))
	empty := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Argumento sem atribuições", "published", time.Now().UTC().Add(-time.Hour))

	// The same person credits the same argument twice: one person, two events.
	h.mustAttribute(t, ctx, first, 2, argument)
	h.mustAttribute(t, ctx, first, 3, argument)
	// A second eligible person credits it once.
	h.mustAttribute(t, ctx, second, 2, argument)
	// Ineligible accounts credit it too: their events never reach the count.
	h.mustAttribute(t, ctx, suspended, 2, argument)
	h.mustAttribute(t, ctx, unverified, 2, argument)

	metrics := h.metricsFor(t, argument)
	if metrics.DistinctPeople != 2 || metrics.ValidAttributions != 3 {
		t.Fatalf("metrics = %d people / %d events, want 2/3 (eligible only, one person once)", metrics.DistinctPeople, metrics.ValidAttributions)
	}

	// An argument nobody credited is a zeroed fact, not a missing one.
	emptyMetrics := h.metricsFor(t, empty)
	if emptyMetrics.DistinctPeople != 0 || emptyMetrics.ValidAttributions != 0 {
		t.Fatalf("untouched argument = %d/%d, want zeros", emptyMetrics.DistinctPeople, emptyMetrics.ValidAttributions)
	}

	// Invalidate the eligible person's first event: the valid totals drop
	// while the retained row stays in the database.
	changeID := mustPersuasionChangeAt(t, ctx, h.pool, h.arena, first, "agree", "disagree", 4, time.Now().UTC())
	if _, err := h.record.Execute(ctx, recordAttributionsCommand(first, changeID)); err != nil {
		t.Fatalf("record empty selection: %v", err)
	}
	moderator := mustReputationAccount(t, ctx, q, h.pool, "metrics-moderator@arena.example.com", "active", true)
	mustInvalidateAttribution(t, ctx, h.pool, mustChangeOf(t, ctx, h.pool, h.arena, first, 2), argument, moderator)

	invalidated := h.metricsFor(t, argument)
	if invalidated.ValidAttributions != 2 || invalidated.DistinctPeople != 2 {
		t.Fatalf("after invalidation = %d/%d, want 2 events / 2 people", invalidated.ValidAttributions, invalidated.DistinctPeople)
	}

	// Restoring the attribution brings the fact back: counts follow validity.
	if _, err := h.pool.Exec(ctx, `
		UPDATE app.persuasion_attributions
		SET status = 'valid', invalidated_at = NULL,
		    moderation_reason = 'restaurada', moderated_by = $3, moderated_at = now()
		WHERE position_change_id = $1 AND argument_id = $2`,
		mustChangeOf(t, ctx, h.pool, h.arena, first, 2), argument, moderator); err != nil {
		t.Fatalf("restore attribution: %v", err)
	}
	restored := h.metricsFor(t, argument)
	if restored.ValidAttributions != 3 || restored.DistinctPeople != 2 {
		t.Fatalf("after restore = %d/%d, want 3 events / 2 people", restored.ValidAttributions, restored.DistinctPeople)
	}

	// The malformed and unknown identifiers are absences, never zeroes.
	if _, err := h.metrics.Execute(ctx, application.GetArgumentMetricsQuery{ArgumentID: "018f6b2a-0000-7000-8000-0000000000ff"}); !errors.Is(err, application.ErrArgumentNotFound) {
		t.Fatalf("unknown argument error = %v, want ErrArgumentNotFound", err)
	}
	if _, err := h.metrics.Execute(ctx, application.GetArgumentMetricsQuery{ArgumentID: "not-a-uuid"}); !errors.Is(err, application.ErrArgumentNotFound) {
		t.Fatalf("malformed argument error = %v, want ErrArgumentNotFound", err)
	}
}

// mustChangeOf returns the change identifier of one version of the
// attributor's chain in the harness Arena.
func mustChangeOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID, accountID pgtype.UUID, version int32) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM app.position_changes
		WHERE arena_id = $1 AND account_id = $2 AND version = $3`, arenaID, accountID, version).Scan(&id); err != nil {
		t.Fatalf("read change version %d: %v", version, err)
	}
	return id
}

// TestAuthorDirectoryResolvesPublicUsernames proves the username resolution
// the public reputation route depends on: the canonical normalized form is
// the authority key, the stored spelling comes back, and a handle that owns
// no profile is an absence.
func TestAuthorDirectoryResolvesPublicUsernames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newMetricsHarness(t)

	first := mustReputationAccount(t, ctx, platformpg.New(h.pool), h.pool, "metrics-handle@arena.example.com", "active", true)
	mustProfile(t, ctx, h.pool, first, "Ana_Zanata")

	handle, err := h.repo.ResolveAuthor(ctx, "ana_zanata")
	if err != nil {
		t.Fatalf("ResolveAuthor() error = %v", err)
	}
	if handle.AuthorID.String() != uuidText(first) {
		t.Fatalf("resolved %q, want %q", handle.AuthorID.String(), uuidText(first))
	}
	if handle.Username != "Ana_Zanata" {
		t.Fatalf("Username = %q, want the stored spelling", handle.Username)
	}

	if _, err := h.repo.ResolveAuthor(ctx, "nao_existe"); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("unknown username error = %v, want ErrProfileNotFound", err)
	}
}

// TestProfileReputationFollowsTheResolvedAuthor proves the username-addressed
// projection end to end: the canonical handle comes back with the facts
// derived from the author's valid attributions.
func TestProfileReputationFollowsTheResolvedAuthor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newMetricsHarness(t)
	q := platformpg.New(h.pool)

	handle := mustReputationAccount(t, ctx, q, h.pool, "metrics-reputation@arena.example.com", "active", true)
	argument := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Argumento do autor com perfil", "published", time.Now().UTC().Add(-time.Hour))
	mustProfile(t, ctx, h.pool, h.author, "autor_publico")

	h.mustAttribute(t, ctx, handle, 2, argument)
	h.mustAttribute(t, ctx, handle, 3, argument)

	reputation, err := h.profile.Execute(ctx, application.GetProfileReputationQuery{Username: "AUTOR_PUBLICO"})
	if err != nil {
		t.Fatalf("get profile reputation: %v", err)
	}
	if reputation.Username != "autor_publico" {
		t.Fatalf("Username = %q, want the canonical handle", reputation.Username)
	}
	if reputation.AuthorID.String() != uuidText(h.author) {
		t.Fatalf("AuthorID = %q, want the resolved author", reputation.AuthorID.String())
	}
	if reputation.InfluencedPeople() != 1 || reputation.TotalValidAttributions() != 2 {
		t.Fatalf("facts = %d/%d, want one person with two events", reputation.InfluencedPeople(), reputation.TotalValidAttributions())
	}
	for _, arena := range reputation.Arenas {
		if arena.ArenaID.String() == uuidText(h.arena) {
			if arena.Category != "technology" || arena.Language != "pt-BR" {
				t.Fatalf("arena slice = %+v, want the arena dimensions", arena)
			}
		}
	}

	if _, err := h.profile.Execute(ctx, application.GetProfileReputationQuery{Username: "sem_perfil"}); !errors.Is(err, application.ErrProfileNotFound) {
		t.Fatalf("unknown username error = %v, want ErrProfileNotFound", err)
	}
}
