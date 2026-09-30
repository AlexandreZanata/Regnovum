package postgres_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/billingpass"
	arenaspg "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	billingpg "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// grantPass seeds one valid bought Arena Pass for the creator.
func grantPass(t *testing.T, ctx context.Context, repo *billingpg.Repository, accountID string, reference string) {
	t.Helper()
	quantity, err := billingdomain.NewQuantity(1)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	parsedReference, err := billingdomain.ParseReference(reference)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if _, err := repo.GrantPassLot(ctx, billingapp.GrantPassLotRequest{
		AccountID: billingdomain.AccountID(accountID),
		Origin:    billingdomain.OriginPurchase,
		Quantity:  quantity,
		Reference: parsedReference,
		GrantedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("GrantPassLot: %v", err)
	}
}

// TestPublishArenaEndToEndAtomic covers the P08-T04 acceptance path: the
// publication consumes exactly one pass in the same transaction and retries
// resolve the replay without consuming again.
func TestPublishArenaEndToEndAtomic(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	arenasRepo := arenaspg.NewRepository(pool)
	billingRepo := billingpg.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "publish-e2e@arena.example.com")
	grantPass(t, ctx, billingRepo, creator.String(), "stripe:evt_publish_e2e")

	clock := clockseed.NewClock()
	create := arenasapp.NewCreateArenaDraftUseCase(arenasRepo, domain.DefaultStatementPolicy())
	draft, err := create.Execute(ctx, arenasapp.CreateArenaDraftCommand{
		AccountID: creator.String(),
		Statement: "A AGI existirá até 2040",
		Category:  "technology",
		Language:  "pt-BR",
	})
	if err != nil {
		t.Fatalf("create Execute() error = %v", err)
	}

	bridge := billingpass.New(billingRepo, clock)
	publish := arenasapp.NewPublishArenaUseCase(arenasRepo, bridge, platformpg.NewTxManager(pool), clock)

	result, err := publish.Execute(ctx, arenasapp.PublishArenaCommand{
		AccountID: creator.String(),
		ArenaID:   draft.ID().String(),
	})
	if err != nil {
		t.Fatalf("publish Execute() error = %v", err)
	}
	if result.Replayed || result.Arena.Status() != "published" {
		t.Fatalf("publication = status %s, replayed %v", result.Arena.Status(), result.Replayed)
	}
	expectedSlug, err := arenasapp.DeriveSlug(*draft)
	if err != nil {
		t.Fatalf("DeriveSlug: %v", err)
	}
	if result.Arena.Slug().String() != expectedSlug.String() {
		t.Fatalf("slug = %q, want %q", result.Arena.Slug().String(), expectedSlug.String())
	}
	if result.Arena.PublishedAt() == nil {
		t.Fatal("published_at must be set")
	}

	if got := countArenaRows(t, ctx, pool, "SELECT count(*) FROM app.arena_pass_consumptions"); got != 1 {
		t.Fatalf("consumptions = %d, want 1", got)
	}
	var remaining int
	if err := pool.QueryRow(ctx, "SELECT COALESCE(sum(remaining_quantity), 0)::int FROM app.arena_pass_lots").Scan(&remaining); err != nil {
		t.Fatalf("sum lot remaining: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("lot remaining = %d, want 0", remaining)
	}

	// The retry resolves the replay without touching the ledger.
	retry, err := publish.Execute(ctx, arenasapp.PublishArenaCommand{
		AccountID: creator.String(),
		ArenaID:   draft.ID().String(),
	})
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if !retry.Replayed {
		t.Fatal("retry must resolve the published Arena as a replay")
	}
	if got := countArenaRows(t, ctx, pool, "SELECT count(*) FROM app.arena_pass_consumptions"); got != 1 {
		t.Fatalf("consumptions after retry = %d, want 1", got)
	}

	// No orphan consumption and no published Arena without consumption.
	assertPublicationInvariants(t, ctx, pool)
}

func TestPublishArenaWithoutPassKeepsDraft(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	arenasRepo := arenaspg.NewRepository(pool)
	billingRepo := billingpg.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "publish-no-pass@arena.example.com")
	clock := clockseed.NewClock()
	create := arenasapp.NewCreateArenaDraftUseCase(arenasRepo, domain.DefaultStatementPolicy())
	draft, err := create.Execute(ctx, arenasapp.CreateArenaDraftCommand{
		AccountID: creator.String(),
		Statement: "A AGI existirá até 2040",
		Category:  "technology",
		Language:  "pt-BR",
	})
	if err != nil {
		t.Fatalf("create Execute() error = %v", err)
	}

	bridge := billingpass.New(billingRepo, clock)
	publish := arenasapp.NewPublishArenaUseCase(arenasRepo, bridge, platformpg.NewTxManager(pool), clock)

	if _, err := publish.Execute(ctx, arenasapp.PublishArenaCommand{
		AccountID: creator.String(),
		ArenaID:   draft.ID().String(),
	}); !errors.Is(err, arenasapp.ErrNoPassAvailable) {
		t.Fatalf("publish without pass error = %v, want ErrNoPassAvailable", err)
	}

	stored, err := arenasRepo.GetArenaForCreator(ctx, draft.ID(), creator)
	if err != nil {
		t.Fatalf("reload draft: %v", err)
	}
	if stored.Status() != "draft" {
		t.Fatalf("status = %s, want draft", stored.Status())
	}
	if got := countArenaRows(t, ctx, pool, "SELECT count(*) FROM app.arena_pass_consumptions"); got != 0 {
		t.Fatalf("consumptions = %d, want 0", got)
	}
}

// TestPublishArenaSlugConflictRollsBackConsumption proves the atomicity in
// the failure direction: the pass is consumed first and the slug conflict
// afterwards rolls the consumption back.
func TestPublishArenaSlugConflictRollsBackConsumption(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	arenasRepo := arenaspg.NewRepository(pool)
	billingRepo := billingpg.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "publish-conflict@arena.example.com")
	grantPass(t, ctx, billingRepo, creator.String(), "stripe:evt_publish_conflict")

	clock := clockseed.NewClock()
	create := arenasapp.NewCreateArenaDraftUseCase(arenasRepo, domain.DefaultStatementPolicy())
	draft, err := create.Execute(ctx, arenasapp.CreateArenaDraftCommand{
		AccountID: creator.String(),
		Statement: "A AGI existirá até 2040",
		Category:  "technology",
		Language:  "pt-BR",
	})
	if err != nil {
		t.Fatalf("create Execute() error = %v", err)
	}

	// Another Arena already owns the exact derived slug.
	derived, err := arenasapp.DeriveSlug(*draft)
	if err != nil {
		t.Fatalf("DeriveSlug: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
		VALUES ($1, 'Uma arena concorrente com o mesmo slug', 'technology', 'pt-BR', 'published', $2, now())`,
		mustUUID(t, creator.String()), derived.String()); err != nil {
		t.Fatalf("seed slug conflict: %v", err)
	}

	bridge := billingpass.New(billingRepo, clock)
	publish := arenasapp.NewPublishArenaUseCase(arenasRepo, bridge, platformpg.NewTxManager(pool), clock)

	if _, err := publish.Execute(ctx, arenasapp.PublishArenaCommand{
		AccountID: creator.String(),
		ArenaID:   draft.ID().String(),
	}); !errors.Is(err, arenasapp.ErrSlugConflict) {
		t.Fatalf("slug conflict error = %v, want ErrSlugConflict", err)
	}

	if got := countArenaRows(t, ctx, pool, "SELECT count(*) FROM app.arena_pass_consumptions"); got != 0 {
		t.Fatalf("consumptions = %d, want 0 (the consumed pass rolled back)", got)
	}
	var remaining int
	if err := pool.QueryRow(ctx, "SELECT COALESCE(sum(remaining_quantity), 0)::int FROM app.arena_pass_lots").Scan(&remaining); err != nil {
		t.Fatalf("sum lot remaining: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("lot remaining = %d, want 1 untouched", remaining)
	}
	stored, err := arenasRepo.GetArenaForCreator(ctx, draft.ID(), creator)
	if err != nil {
		t.Fatalf("reload draft: %v", err)
	}
	if stored.Status() != "draft" {
		t.Fatalf("status = %s, want draft", stored.Status())
	}
}

// TestPublishArenaConcurrent is the P08-T04 stress probe: twenty concurrent
// publications compete for one pass and the invariants hold afterwards.
func TestPublishArenaConcurrent(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	arenasRepo := arenaspg.NewRepository(pool)
	billingRepo := billingpg.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "publish-race@arena.example.com")
	grantPass(t, ctx, billingRepo, creator.String(), "stripe:evt_publish_race")

	clock := clockseed.NewClock()
	create := arenasapp.NewCreateArenaDraftUseCase(arenasRepo, domain.DefaultStatementPolicy())
	draft, err := create.Execute(ctx, arenasapp.CreateArenaDraftCommand{
		AccountID: creator.String(),
		Statement: "A AGI existirá até 2040",
		Category:  "technology",
		Language:  "pt-BR",
	})
	if err != nil {
		t.Fatalf("create Execute() error = %v", err)
	}

	bridge := billingpass.New(billingRepo, clock)
	publish := arenasapp.NewPublishArenaUseCase(arenasRepo, bridge, platformpg.NewTxManager(pool), clock)

	const workers = 20
	var (
		successes atomic.Int32
		replays   atomic.Int32
		wg        sync.WaitGroup
	)
	unexpected := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := publish.Execute(ctx, arenasapp.PublishArenaCommand{
				AccountID: creator.String(),
				ArenaID:   draft.ID().String(),
			})
			if err != nil {
				switch {
				case errors.Is(err, arenasapp.ErrNoPassAvailable),
					errors.Is(err, arenasapp.ErrVersionConflict),
					errors.Is(err, arenasapp.ErrArenaAlreadyConsumed):
				default:
					unexpected[index] = err
				}
				return
			}
			if result.Replayed {
				replays.Add(1)
			} else {
				successes.Add(1)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range unexpected {
		if err != nil {
			t.Fatalf("worker %d unexpected error = %v", i, err)
		}
	}
	if successes.Load()+replays.Load() < 1 {
		t.Fatal("no publication succeeded")
	}
	if replays.Load() > 0 && successes.Load() != 1 {
		t.Fatalf("successes = %d with replays = %d, want exactly one publication", successes.Load(), replays.Load())
	}

	if got := countArenaRows(t, ctx, pool, "SELECT count(*) FROM app.arena_pass_consumptions"); got != 1 {
		t.Fatalf("consumptions = %d, want exactly 1", got)
	}
	if got := countArenaRows(t, ctx, pool, "SELECT count(*) FROM app.arenas WHERE status = 'published'"); got != 1 {
		t.Fatalf("published arenas = %d, want exactly 1", got)
	}
	assertPublicationInvariants(t, ctx, pool)
}

// assertPublicationInvariants proves there is never a published Arena
// without a consumption nor a consumption whose Arena is not published.
func assertPublicationInvariants(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	orphanConsumptions := countArenaRows(t, ctx, pool, `
		SELECT count(*)
		FROM app.arena_pass_consumptions c
		JOIN app.arenas a ON a.id = c.arena_id
		WHERE a.status <> 'published'`)
	if orphanConsumptions != 0 {
		t.Fatalf("orphan consumptions = %d, want 0", orphanConsumptions)
	}

	publishedWithoutConsumption := countArenaRows(t, ctx, pool, `
		SELECT count(*)
		FROM app.arenas a
		LEFT JOIN app.arena_pass_consumptions c ON c.arena_id = a.id
		WHERE a.status = 'published' AND c.id IS NULL`)
	if publishedWithoutConsumption != 0 {
		t.Fatalf("published arenas without consumption = %d, want 0", publishedWithoutConsumption)
	}
}
