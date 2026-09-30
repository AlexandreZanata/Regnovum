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

// reputationHarness wires the recording, moderation and reputation use cases
// over one isolated database.
type reputationHarness struct {
	pool       *pgxpool.Pool
	repo       *postgres.Repository
	record     *application.RecordAttributionsUseCase
	invalidate *application.InvalidateAttributionUseCase
	restore    *application.RestoreAttributionUseCase
	reputation *application.GetAuthorReputationUseCase
	author     pgtype.UUID
	moderator  pgtype.UUID
}

func newReputationHarness(t *testing.T) *reputationHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	author := mustReputationAccount(t, ctx, q, pool, "reputation-author@arena.example.com", "active", true)
	moderator := mustReputationAccount(t, ctx, q, pool, "reputation-moderator@arena.example.com", "active", true)

	repo := postgres.NewRepository(pool)
	uow := platformpg.NewTxManager(pool)
	audit := &captureAttributionAudit{}
	authorizer := &allowAttributionModerator{}
	clock := clockseed.NewClock()

	return &reputationHarness{
		pool:       pool,
		repo:       repo,
		record:     application.NewRecordAttributionsUseCase(repo, domain.DefaultEligibilityPolicy(), uow),
		invalidate: application.NewInvalidateAttributionUseCase(repo, authorizer, audit, clock, uow),
		restore:    application.NewRestoreAttributionUseCase(repo, authorizer, audit, clock, uow),
		reputation: application.NewGetAuthorReputationUseCase(repo, clock),
		author:     author,
		moderator:  moderator,
	}
}

// mustReputationAccount creates one account in the requested status, with the
// email verified or not: eligibility of an attributor is exactly "active and
// verified" (BR §7).
func mustReputationAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, pool *pgxpool.Pool, email, status string, verified bool) pgtype.UUID {
	t.Helper()
	account := mustPersuasionAccount(t, ctx, q, email)
	if _, err := pool.Exec(ctx, `UPDATE app.accounts SET status = $2 WHERE id = $1`, account, status); err != nil {
		t.Fatalf("set status of %s: %v", email, err)
	}
	if verified {
		if _, err := pool.Exec(ctx, `UPDATE app.accounts SET email_verified_at = now() WHERE id = $1`, account); err != nil {
			t.Fatalf("verify %s: %v", email, err)
		}
	}
	return account
}

// mustReputationArena publishes one Arena with the requested category and
// content language, the dimensions of the public distribution.
func mustReputationArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator pgtype.UUID, slug, category, language string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Afirmação para métricas de reputação', $2, $3, 'draft')
		RETURNING id`, creator, category, language).Scan(&id); err != nil {
		t.Fatalf("insert arena %s: %v", slug, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE app.arenas SET status = 'published', slug = $2, published_at = now(), version = version + 1
		WHERE id = $1`, id, slug); err != nil {
		t.Fatalf("publish arena %s: %v", slug, err)
	}
	return id
}

// attributeThroughChange records one attribution of the given arguments by
// creating a position change of the requested version for the attributor.
func (h *reputationHarness) attributeThroughChange(t *testing.T, ctx context.Context, arenaID, attributorID pgtype.UUID, version int32, changedAt time.Time, argumentIDs ...pgtype.UUID) pgtype.UUID {
	t.Helper()
	changeID := mustPersuasionChangeAt(t, ctx, h.pool, arenaID, attributorID, "agree", "disagree", version, changedAt)
	if _, err := h.record.Execute(ctx, recordAttributionsCommand(attributorID, changeID, argumentIDs...)); err != nil {
		t.Fatalf("record attribution: %v", err)
	}
	return changeID
}

func mustAttributionID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, changeID, argumentID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM app.persuasion_attributions
		WHERE position_change_id = $1 AND argument_id = $2`, changeID, argumentID).Scan(&id); err != nil {
		t.Fatalf("read attribution: %v", err)
	}
	return id
}

func (h *reputationHarness) reputationCommand() application.GetAuthorReputationQuery {
	return application.GetAuthorReputationQuery{AuthorID: uuidText(h.author)}
}

func (h *reputationHarness) moderateCommand(attributionID pgtype.UUID, reason string) application.ModerateAttributionCommand {
	return application.ModerateAttributionCommand{
		ActorAccountID: uuidText(h.moderator),
		AttributionID:  uuidText(attributionID),
		Reason:         reason,
	}
}

// reputationDataset is the fixture of the T05 validation: multiple changes,
// multiple arguments, multiple Arenas, invalidations and ineligible
// attributors.
type reputationDataset struct {
	harness *reputationHarness

	technologyPT pgtype.UUID
	technologyEN pgtype.UUID
	philosophyPT pgtype.UUID

	firstArgument   pgtype.UUID // technology pt-BR
	secondArgument  pgtype.UUID // technology pt-BR
	thirdArgument   pgtype.UUID // technology en-US
	fourthArgument  pgtype.UUID // philosophy pt-BR
	foreignArgument pgtype.UUID // authored by another account

	firstOfFirstArena  pgtype.UUID // P1 credits firstArgument in technology pt-BR
	secondOfFirstArena pgtype.UUID // P1 credits secondArgument in technology pt-BR (same person, same Arena)
}

func newReputationDataset(t *testing.T) *reputationDataset {
	t.Helper()
	ctx := context.Background()
	harness := newReputationHarness(t)
	pool := harness.pool
	q := platformpg.New(pool)

	dataset := &reputationDataset{harness: harness}
	dataset.technologyPT = mustReputationArena(t, ctx, pool, harness.author, "reputation-technology-pt", "technology", "pt-BR")
	dataset.technologyEN = mustReputationArena(t, ctx, pool, harness.author, "reputation-technology-en", "technology", "en-US")
	dataset.philosophyPT = mustReputationArena(t, ctx, pool, harness.author, "reputation-philosophy-pt", "philosophy", "pt-BR")

	changeAt := time.Now().UTC().Add(-time.Hour)
	dataset.firstArgument = mustPersuasionArgument(t, ctx, pool, dataset.technologyPT, harness.author, "Argumento tecnológico um", "published", changeAt.Add(-time.Hour))
	dataset.secondArgument = mustPersuasionArgument(t, ctx, pool, dataset.technologyPT, harness.author, "Argumento tecnológico dois", "published", changeAt.Add(-time.Hour))
	dataset.thirdArgument = mustPersuasionArgument(t, ctx, pool, dataset.technologyEN, harness.author, "Argumento tecnológico em inglês", "published", changeAt.Add(-time.Hour))
	dataset.fourthArgument = mustPersuasionArgument(t, ctx, pool, dataset.philosophyPT, harness.author, "Argumento filosófico", "published", changeAt.Add(-time.Hour))

	// An argument of another author must never leak into this author's facts.
	otherAuthor := mustReputationAccount(t, ctx, q, pool, "reputation-other-author@arena.example.com", "active", true)
	dataset.foreignArgument = mustPersuasionArgument(t, ctx, pool, dataset.technologyPT, otherAuthor, "Argumento de outra autoria", "published", changeAt.Add(-time.Hour))

	// P1 influences the same author twice in the same Arena: one person, two
	// events.
	first := mustReputationAccount(t, ctx, q, pool, "reputation-person-one@arena.example.com", "active", true)
	dataset.firstOfFirstArena = harness.attributeThroughChange(t, ctx, dataset.technologyPT, first, 2, changeAt, dataset.firstArgument)
	dataset.secondOfFirstArena = harness.attributeThroughChange(t, ctx, dataset.technologyPT, first, 3, changeAt.Add(time.Minute), dataset.secondArgument)

	// P2 influences the author in two Arenas of the same category and once in
	// another author's argument.
	second := mustReputationAccount(t, ctx, q, pool, "reputation-person-two@arena.example.com", "active", true)
	harness.attributeThroughChange(t, ctx, dataset.technologyPT, second, 2, changeAt, dataset.firstArgument)
	harness.attributeThroughChange(t, ctx, dataset.philosophyPT, second, 2, changeAt.Add(time.Minute), dataset.fourthArgument)
	otherChange := mustPersuasionChangeAt(t, ctx, pool, dataset.technologyPT, second, "agree", "disagree", 3, changeAt.Add(2*time.Minute))
	if _, err := harness.record.Execute(ctx, recordAttributionsCommand(second, otherChange, dataset.foreignArgument)); err != nil {
		t.Fatalf("record foreign attribution: %v", err)
	}

	// P3 influences the author in two Arenas with different languages.
	third := mustReputationAccount(t, ctx, q, pool, "reputation-person-three@arena.example.com", "active", true)
	harness.attributeThroughChange(t, ctx, dataset.technologyPT, third, 2, changeAt, dataset.secondArgument)
	harness.attributeThroughChange(t, ctx, dataset.technologyEN, third, 2, changeAt.Add(time.Minute), dataset.thirdArgument)

	// P4 completes the second Arena.
	fourth := mustReputationAccount(t, ctx, q, pool, "reputation-person-four@arena.example.com", "active", true)
	harness.attributeThroughChange(t, ctx, dataset.technologyEN, fourth, 2, changeAt, dataset.thirdArgument)

	// P5 is invalidated, so its facts leave the valid totals.
	fifth := mustReputationAccount(t, ctx, q, pool, "reputation-person-five@arena.example.com", "active", true)
	invalidatedChange := harness.attributeThroughChange(t, ctx, dataset.technologyPT, fifth, 2, changeAt, dataset.firstArgument)
	invalidated := mustAttributionID(t, ctx, pool, invalidatedChange, dataset.firstArgument)
	if _, err := harness.invalidate.Execute(ctx, harness.moderateCommand(invalidated, "atribuição fraudulenta no dataset de reputação")); err != nil {
		t.Fatalf("invalidate attribution: %v", err)
	}

	// P6 is suspended and P7 has no verified email: neither counts.
	suspended := mustReputationAccount(t, ctx, q, pool, "reputation-person-six@arena.example.com", "suspended", true)
	harness.attributeThroughChange(t, ctx, dataset.technologyPT, suspended, 2, changeAt, dataset.firstArgument)
	unverified := mustReputationAccount(t, ctx, q, pool, "reputation-person-seven@arena.example.com", "active", false)
	harness.attributeThroughChange(t, ctx, dataset.technologyPT, unverified, 2, changeAt, dataset.secondArgument)

	return dataset
}

// sourceFact is one valid attribution fact read straight from the database
// without any aggregation: the source of the reconstruction.
type sourceFact struct {
	arenaID    string
	category   string
	language   string
	attributor string
}

func readSourceFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, authorID pgtype.UUID) []sourceFact {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT ar.id::text, ar.category, ar.language, pa.attributor_id::text
		FROM app.persuasion_attributions pa
		JOIN app.arguments a ON a.id = pa.argument_id
		JOIN app.arenas ar ON ar.id = a.arena_id
		JOIN app.accounts acc ON acc.id = pa.attributor_id
		    AND acc.status = 'active'
		    AND acc.email_verified_at IS NOT NULL
		WHERE a.author_id = $1
		  AND pa.status = 'valid'`, authorID)
	if err != nil {
		t.Fatalf("read source facts: %v", err)
	}
	defer rows.Close()

	facts := []sourceFact{}
	for rows.Next() {
		var fact sourceFact
		if err := rows.Scan(&fact.arenaID, &fact.category, &fact.language, &fact.attributor); err != nil {
			t.Fatalf("scan source fact: %v", err)
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate source facts: %v", err)
	}
	return facts
}

// sourceArenaFacts recomputes the per-Arena facts in Go from the raw rows:
// distinct people per Arena (the headline rule) plus the event count.
type sourceArenaFacts struct {
	people map[string]bool
	events int64
}

func recomputeArenaFacts(facts []sourceFact) map[string]sourceArenaFacts {
	recomputed := map[string]sourceArenaFacts{}
	for _, fact := range facts {
		arena := recomputed[fact.arenaID]
		if arena.people == nil {
			arena.people = map[string]bool{}
		}
		arena.people[fact.attributor] = true
		arena.events++
		recomputed[fact.arenaID] = arena
	}
	return recomputed
}

// TestAuthorReputationProjectionMatchesSource proves the T05 validation: over
// a dataset with multiple changes, arguments, Arenas, invalidations and
// ineligible attributors, the projection equals the source reconstruction —
// and the headline counts each person once per author and Arena.
func TestAuthorReputationProjectionMatchesSource(t *testing.T) {
	ctx := context.Background()
	dataset := newReputationDataset(t)
	harness := dataset.harness

	reputation, err := harness.reputation.Execute(ctx, harness.reputationCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// The projection has one row per Arena with valid facts, ordered by arena.
	if len(reputation.Arenas) != 3 {
		t.Fatalf("arenas = %d, want three", len(reputation.Arenas))
	}
	expected := map[string]struct{ people, events int64 }{
		uuidText(dataset.technologyPT): {people: 3, events: 4},
		uuidText(dataset.technologyEN): {people: 2, events: 2},
		uuidText(dataset.philosophyPT): {people: 1, events: 1},
	}
	for _, arena := range reputation.Arenas {
		want, ok := expected[arena.ArenaID.String()]
		if !ok {
			t.Fatalf("unexpected arena in projection: %s", arena.ArenaID.String())
		}
		if arena.DistinctPeople != want.people || arena.ValidAttributions != want.events {
			t.Fatalf("arena %s = %d people/%d events, want %d/%d",
				arena.ArenaID.String(), arena.DistinctPeople, arena.ValidAttributions, want.people, want.events)
		}
	}

	// The headline is the per-Arena sum: P1 counts once in technology pt-BR
	// despite two events, and P2 counts again in philosophy (same category as
	// technology? no: different), while a global distinct count would be 4.
	if got := reputation.InfluencedPeople(); got != 6 {
		t.Fatalf("InfluencedPeople() = %d, want 6 (per-Arena headline)", got)
	}
	if got := reputation.TotalValidAttributions(); got != 7 {
		t.Fatalf("TotalValidAttributions() = %d, want the 7 valid events", got)
	}

	// Source reconstruction in Go, straight from the retained rows.
	facts := readSourceFacts(t, ctx, harness.pool, harness.author)
	recomputed := recomputeArenaFacts(facts)
	for arenaID, want := range recomputed {
		var found bool
		for _, arena := range reputation.Arenas {
			if arena.ArenaID.String() != arenaID {
				continue
			}
			found = true
			if arena.DistinctPeople != int64(len(want.people)) || arena.ValidAttributions != want.events {
				t.Fatalf("arena %s projection = %d/%d, source = %d/%d",
					arenaID, arena.DistinctPeople, arena.ValidAttributions, len(want.people), want.events)
			}
		}
		if !found {
			t.Fatalf("arena %s is missing from the projection", arenaID)
		}
	}
	var sourcePeople, sourceEvents int64
	for _, want := range recomputed {
		sourcePeople += int64(len(want.people))
		sourceEvents += want.events
	}
	if sourcePeople != reputation.InfluencedPeople() || sourceEvents != reputation.TotalValidAttributions() {
		t.Fatalf("headline = %d/%d, source = %d/%d",
			reputation.InfluencedPeople(), reputation.TotalValidAttributions(), sourcePeople, sourceEvents)
	}

	// The distributions use the same per-Arena facts: the category buckets sum
	// the Arena slices (a person credited in two Arenas of one category counts
	// in both, as the headline rule states), never a global distinct count.
	byCategory := reputation.ByCategory()
	if len(byCategory) != 2 {
		t.Fatalf("ByCategory() = %+v, want two buckets", byCategory)
	}
	if byCategory[0].Label != "philosophy" || byCategory[0].DistinctPeople != 1 || byCategory[0].ValidAttributions != 1 {
		t.Fatalf("philosophy bucket = %+v, want 1/1", byCategory[0])
	}
	if byCategory[1].Label != "technology" || byCategory[1].DistinctPeople != 5 || byCategory[1].ValidAttributions != 6 {
		t.Fatalf("technology bucket = %+v, want 5/6", byCategory[1])
	}
	byLanguage := reputation.ByLanguage()
	if len(byLanguage) != 2 {
		t.Fatalf("ByLanguage() = %+v, want two buckets", byLanguage)
	}
	if byLanguage[0].Label != "en-US" || byLanguage[0].DistinctPeople != 2 || byLanguage[0].ValidAttributions != 2 {
		t.Fatalf("en-US bucket = %+v, want 2/2", byLanguage[0])
	}
	if byLanguage[1].Label != "pt-BR" || byLanguage[1].DistinctPeople != 4 || byLanguage[1].ValidAttributions != 5 {
		t.Fatalf("pt-BR bucket = %+v, want 4/5", byLanguage[1])
	}

	// The independent source query, grouped directly by the dimensions, must
	// agree with the derived distribution.
	sourceByCategory, sourceByLanguage := readSourceDimensionCounts(t, ctx, harness.pool, harness.author)
	assertDimensionCounts(t, "category", byCategory, sourceByCategory)
	assertDimensionCounts(t, "language", byLanguage, sourceByLanguage)
}

// readSourceDimensionCounts recomputes the distributions directly in SQL,
// distinct per Arena first and summed per label: the same counting rule the
// projection derives in the domain.
func readSourceDimensionCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, authorID pgtype.UUID) (map[string]dimensionCount, map[string]dimensionCount) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		WITH per_arena AS (
		    SELECT ar.category,
		           ar.language,
		           ar.id AS arena_id,
		           count(DISTINCT pa.attributor_id)::bigint AS distinct_people,
		           count(*)::bigint AS valid_attributions
		    FROM app.persuasion_attributions pa
		    JOIN app.arguments a ON a.id = pa.argument_id
		    JOIN app.arenas ar ON ar.id = a.arena_id
		    JOIN app.accounts acc ON acc.id = pa.attributor_id
		        AND acc.status = 'active'
		        AND acc.email_verified_at IS NOT NULL
		    WHERE a.author_id = $1
		      AND pa.status = 'valid'
		    GROUP BY ar.category, ar.language, ar.id
		)
		SELECT category, language, sum(distinct_people)::bigint, sum(valid_attributions)::bigint
		FROM per_arena
		GROUP BY category, language`, authorID)
	if err != nil {
		t.Fatalf("read source dimensions: %v", err)
	}
	defer rows.Close()

	byCategory := map[string]dimensionCount{}
	byLanguage := map[string]dimensionCount{}
	for rows.Next() {
		var category, language string
		var people, events int64
		if err := rows.Scan(&category, &language, &people, &events); err != nil {
			t.Fatalf("scan source dimension: %v", err)
		}
		categoryBucket := byCategory[category]
		categoryBucket.people += people
		categoryBucket.events += events
		byCategory[category] = categoryBucket

		languageBucket := byLanguage[language]
		languageBucket.people += people
		languageBucket.events += events
		byLanguage[language] = languageBucket
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate source dimensions: %v", err)
	}
	return byCategory, byLanguage
}

type dimensionCount struct{ people, events int64 }

func assertDimensionCounts(t *testing.T, dimension string, derived []application.DimensionReputation, source map[string]dimensionCount) {
	t.Helper()
	if len(derived) != len(source) {
		t.Fatalf("%s buckets = %d, want %d", dimension, len(derived), len(source))
	}
	for _, bucket := range derived {
		want, ok := source[bucket.Label]
		if !ok {
			t.Fatalf("%s bucket %q is not in the source", dimension, bucket.Label)
		}
		if bucket.DistinctPeople != want.people || bucket.ValidAttributions != want.events {
			t.Fatalf("%s bucket %q = %d/%d, source = %d/%d",
				dimension, bucket.Label, bucket.DistinctPeople, bucket.ValidAttributions, want.people, want.events)
		}
	}
}

// TestAuthorReputationFollowsValidity proves the projection tracks
// invalidation and restoration: the facts of an invalidated attribution
// leave the valid totals and come back when it is restored.
func TestAuthorReputationFollowsValidity(t *testing.T) {
	ctx := context.Background()
	dataset := newReputationDataset(t)
	harness := dataset.harness

	// P1 holds two attributions in the same Arena: invalidating one keeps the
	// person counted but removes one event, invalidating both removes the
	// person from the Arena.
	firstAttribution := mustAttributionID(t, ctx, harness.pool, dataset.firstOfFirstArena, dataset.firstArgument)
	secondAttribution := mustAttributionID(t, ctx, harness.pool, dataset.secondOfFirstArena, dataset.secondArgument)

	arenaFacts := func() (int64, int64) {
		t.Helper()
		reputation, err := harness.reputation.Execute(ctx, harness.reputationCommand())
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		for _, arena := range reputation.Arenas {
			if arena.ArenaID.String() == uuidText(dataset.technologyPT) {
				return arena.DistinctPeople, arena.ValidAttributions
			}
		}
		return 0, 0
	}

	if people, events := arenaFacts(); people != 3 || events != 4 {
		t.Fatalf("initial arena facts = %d/%d, want 3/4", people, events)
	}

	if _, err := harness.invalidate.Execute(ctx, harness.moderateCommand(firstAttribution, "primeira invalidação do dataset")); err != nil {
		t.Fatalf("invalidate first: %v", err)
	}
	if people, events := arenaFacts(); people != 3 || events != 3 {
		t.Fatalf("after invalidation = %d/%d, want the person kept and one event removed (3/3)", people, events)
	}

	if _, err := harness.invalidate.Execute(ctx, harness.moderateCommand(secondAttribution, "segunda invalidação do dataset")); err != nil {
		t.Fatalf("invalidate second: %v", err)
	}
	if people, events := arenaFacts(); people != 2 || events != 2 {
		t.Fatalf("after both invalidations = %d/%d, want the person removed (2/2)", people, events)
	}

	if _, err := harness.restore.Execute(ctx, harness.moderateCommand(firstAttribution, "restauração do dataset")); err != nil {
		t.Fatalf("restore first: %v", err)
	}
	if people, events := arenaFacts(); people != 3 || events != 3 {
		t.Fatalf("after restoration = %d/%d, want the person back with one event (3/3)", people, events)
	}

	// The reconstruction still matches after the moderation cycle.
	reputation, err := harness.reputation.Execute(ctx, harness.reputationCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	recomputed := recomputeArenaFacts(readSourceFacts(t, ctx, harness.pool, harness.author))
	for _, arena := range reputation.Arenas {
		want, ok := recomputed[arena.ArenaID.String()]
		if !ok {
			t.Fatalf("arena %s missing from the source facts", arena.ArenaID.String())
		}
		if arena.DistinctPeople != int64(len(want.people)) || arena.ValidAttributions != want.events {
			t.Fatalf("arena %s projection = %d/%d, source = %d/%d",
				arena.ArenaID.String(), arena.DistinctPeople, arena.ValidAttributions, len(want.people), want.events)
		}
	}
}

// TestAuthorReputationScopeAndInvalidInputs proves the projection is scoped to
// one author, that another author's facts never leak in, and how the adapter
// answers an unknown author and a malformed identifier.
func TestAuthorReputationScopeAndInvalidInputs(t *testing.T) {
	ctx := context.Background()
	dataset := newReputationDataset(t)
	harness := dataset.harness

	// The other author owns the foreign argument: their own projection counts
	// exactly that one fact, isolated from this author's Arenas.
	var otherAuthorID pgtype.UUID
	if err := harness.pool.QueryRow(ctx, `
		SELECT author_id FROM app.arguments WHERE id = $1`, dataset.foreignArgument).Scan(&otherAuthorID); err != nil {
		t.Fatalf("read other author: %v", err)
	}
	otherReputation, err := harness.reputation.Execute(ctx, application.GetAuthorReputationQuery{AuthorID: uuidText(otherAuthorID)})
	if err != nil {
		t.Fatalf("Execute() for the other author error = %v", err)
	}
	if len(otherReputation.Arenas) != 1 || otherReputation.InfluencedPeople() != 1 || otherReputation.TotalValidAttributions() != 1 {
		t.Fatalf("other author reputation = %+v, want the single foreign fact", otherReputation)
	}
	if otherReputation.Arenas[0].ArenaID.String() != uuidText(dataset.technologyPT) {
		t.Fatalf("other author arena = %s, want the technology pt-BR Arena", otherReputation.Arenas[0].ArenaID.String())
	}

	// An author without valid attributions derives zeroed facts.
	empty := mustReputationAccount(t, ctx, platformpg.New(harness.pool), harness.pool, "reputation-empty@arena.example.com", "active", true)
	emptyReputation, err := harness.reputation.Execute(ctx, application.GetAuthorReputationQuery{AuthorID: uuidText(empty)})
	if err != nil {
		t.Fatalf("Execute() for an empty author error = %v", err)
	}
	if len(emptyReputation.Arenas) != 0 || emptyReputation.InfluencedPeople() != 0 {
		t.Fatalf("empty author reputation = %+v, want no facts", emptyReputation)
	}

	// A malformed identifier cannot address an account at all.
	_, err = harness.reputation.Execute(ctx, application.GetAuthorReputationQuery{AuthorID: "not-a-database-identifier"})
	if !errors.Is(err, application.ErrInvalidAuthorID) {
		t.Fatalf("malformed identifier error = %v, want ErrInvalidAuthorID", err)
	}
}
