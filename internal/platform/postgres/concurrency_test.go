package postgres_test

// P25-T05 — schedules concorrentes sobre PostgreSQL descartável real.
//
// Seis superfícies Q0, um banco dedicado por schedule, largada sincronizada
// por barreira e deadline explícito em cada contexto: double spend (chaves
// iguais e distintas), posição (mesma chave e chaves distintas), atribuição
// (limite acumulado e replay), webhook (corrida no claim idempotente), job
// lease (claim exclusivo e cancellation) e claim de moderação. Cada schedule
// trava o conjunto fechado de desfechos (quem pode vencer, quem recusa e
// com qual erro) e as invariantes pós-carga; tudo roda com -race.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	argumentspg "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	argumentswallet "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/walletdebit"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	billingpg "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	stripeadapter "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	jobspg "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	moderationpg "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	persuasionpg "github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/postgres"
	persuasionapp "github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	persuasiondomain "github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	positionspg "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/postgres"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// raceOutcome carries one worker result. Workers write disjoint indexes and
// the reader joins before reading, so no test-level race exists by
// construction (proven by -race on top).
type raceOutcome[T any] struct {
	value T
	err   error
}

// runRace starts workers behind a gate so they contend for real, with an
// explicit deadline on the context: a schedule that cannot finish is a
// livelock, and livelocks fail here instead of hanging the suite. A worker
// panic is captured as that worker's error (instead of killing the run and
// orphaning its disposable database), because a schedule must report,
// never strand state.
func runRace[T any](t *testing.T, ctx context.Context, count int, fn func(ctx context.Context, i int) (T, error)) []raceOutcome[T] {
	t.Helper()
	gate := make(chan struct{})
	outcomes := make([]raceOutcome[T], count)
	var wg sync.WaitGroup
	for i := range outcomes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					outcomes[i] = raceOutcome[T]{err: errors.New("t05 worker panic")}
				}
			}()
			<-gate
			value, err := fn(ctx, i)
			outcomes[i] = raceOutcome[T]{value: value, err: err}
		}(i)
	}
	close(gate)
	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()
	select {
	case <-done:
		return outcomes
	case <-ctx.Done():
		t.Fatalf("schedule did not finish before its deadline (livelock): %v", ctx.Err())
		return nil
	}
}

// concClock is one fixed instant for funding and operations.
type concClock struct{ now time.Time }

func (c concClock) Now() time.Time { return c.now }

// concAllowAllArguments is a permissive account gate for argument
// publication; contention, not eligibility, is under test.
type concAllowAllArguments struct{}

func (concAllowAllArguments) EnsureEligible(_ context.Context, _ argumentsdomain.AccountID) error {
	return nil
}

// concOpenArenasForArguments accepts the seeded published arena.
type concOpenArenasForArguments struct{}

func (concOpenArenasForArguments) EnsureAcceptsArguments(_ context.Context, _ argumentsdomain.ArenaID) error {
	return nil
}

// concAllowAllPositions is a permissive account gate for positions.
type concAllowAllPositions struct{}

func (concAllowAllPositions) EnsureEligible(_ context.Context, _ positionsdomain.AccountID) error {
	return nil
}

// concOpenArenasForPositions accepts the seeded published arena.
type concOpenArenasForPositions struct{}

func (concOpenArenasForPositions) EnsureAcceptsPositions(_ context.Context, _ positionsdomain.ArenaID) error {
	return nil
}

// concAccount inserts a minimal active account and returns its identifier.
func concAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ($1, 'active') RETURNING id::text`, email).Scan(&id); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

// concArena inserts a draft arena and publishes it through raw SQL and
// returns its identifier. Publication rules belong to the arenas module;
// here the arena is setup, so direct SQL keeps the focus on contention.
func concArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator, statement, slug string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1::uuid, $2, 'technology', 'pt-BR', 'draft')
		RETURNING id::text`, creator, statement).Scan(&id); err != nil {
		t.Fatalf("seed arena draft: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE app.arenas
		SET status = 'published', slug = $2, published_at = now(), version = version + 1
		WHERE id = $1::uuid`, id, slug); err != nil {
		t.Fatalf("seed arena publication: %v", err)
	}
	return id
}

// concFund credits purchased INK through the real ledger.
func concFund(t *testing.T, ctx context.Context, wallet *walletpg.Repository, clock concClock, account, keySuffix string, amount int64) {
	t.Helper()
	key, err := walletdomain.ParseIdempotencyKey("t05-fund-" + keySuffix)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey: %v", err)
	}
	reference, err := walletdomain.ParseReference("model:fund:t05")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if _, err := wallet.ApplyCredit(ctx, walletapp.CreditRequest{
		AccountID: walletdomain.AccountID(account), Bucket: walletdomain.BucketPurchased,
		OperationType: walletdomain.OperationCreditPurchase, IdempotencyKey: key,
		Reference: reference, Delta: amount, ChangedAt: clock.Now(),
	}); err != nil {
		t.Fatalf("fund wallet: %v", err)
	}
}

func concSpent(t *testing.T, ctx context.Context, wallet *walletpg.Repository, account string, funded int64) int64 {
	t.Helper()
	balance, err := wallet.DerivedBalance(ctx, walletdomain.AccountID(account))
	if err != nil {
		t.Fatalf("derived balance: %v", err)
	}
	return funded - balance.Purchased.Int64()
}

func concCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count state: %v", err)
	}
	return count
}

func TestConcurrentDoubleSpendDistinctKeys(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := concClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}

	walletRepo := walletpg.NewRepository(pool)
	debits := walletapp.NewDebitInkUseCase(walletRepo, clock)
	account := concAccount(t, ctx, pool, "t05-spend@arena.example.com")
	concFund(t, ctx, walletRepo, clock, account, "distinct", 1000)
	reference, err := walletdomain.ParseReference("model:spend:t05")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}

	type debitOutcome struct {
		applied bool
	}
	outcomes := runRace(t, ctx, 8, func(ctx context.Context, i int) (debitOutcome, error) {
		key, err := walletdomain.ParseIdempotencyKey("t05-spend-" + strconv.Itoa(i))
		if err != nil {
			return debitOutcome{}, err
		}
		result, err := debits.Execute(ctx, walletapp.DebitInkCommand{
			AccountID:      account,
			OperationType:  walletdomain.OperationDebitArgument.String(),
			Amount:         200,
			Reference:      reference.String(),
			IdempotencyKey: key.String(),
		})
		if err != nil {
			return debitOutcome{}, err
		}
		return debitOutcome{applied: !result.Replayed}, nil
	})
	applied := 0
	for i, outcome := range outcomes {
		if outcome.err != nil {
			if !errors.Is(outcome.err, walletdomain.ErrInsufficientInk) {
				t.Fatalf("worker %d refused with %v, want insufficient ink", i, outcome.err)
			}
			continue
		}
		if outcome.value.applied {
			applied++
		}
	}
	if applied != 5 {
		t.Fatalf("applied debits = %d, want exactly 5 (1000 of 200)", applied)
	}
	if refused := 8 - applied; refused != 3 {
		t.Fatalf("refused debits = %d, want 3", refused)
	}
	if got := concSpent(t, ctx, walletRepo, account, 1000); got != 1000 {
		t.Fatalf("wallet spent = %d, want 1000 (no double spend, no negative balance)", got)
	}
}

func TestConcurrentDoubleSpendSameKey(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := concClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}

	walletRepo := walletpg.NewRepository(pool)
	debits := walletapp.NewDebitInkUseCase(walletRepo, clock)
	account := concAccount(t, ctx, pool, "t05-replay@arena.example.com")
	concFund(t, ctx, walletRepo, clock, account, "replay", 1000)
	reference, err := walletdomain.ParseReference("model:spend:t05")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}

	type debitOutcome struct {
		replayed bool
	}
	outcomes := runRace(t, ctx, 8, func(ctx context.Context, _ int) (debitOutcome, error) {
		result, err := debits.Execute(ctx, walletapp.DebitInkCommand{
			AccountID:      account,
			OperationType:  walletdomain.OperationDebitArgument.String(),
			Amount:         100,
			Reference:      reference.String(),
			IdempotencyKey: "t05-replay-key",
		})
		if err != nil {
			return debitOutcome{}, err
		}
		return debitOutcome{replayed: result.Replayed}, nil
	})
	fresh := 0
	for i, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("worker %d: replay must succeed, got %v", i, outcome.err)
		}
		if !outcome.value.replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh debits = %d, want exactly 1 (seven replays)", fresh)
	}
	if got := concSpent(t, ctx, walletRepo, account, 1000); got != 100 {
		t.Fatalf("wallet spent = %d, want exactly 100", got)
	}
}

func TestConcurrentPositionSameKey(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := concClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}

	positionsRepo := positionspg.NewRepository(pool)
	uow := platformpg.NewTxManager(pool)
	confirm := positionsapp.NewConfirmInitialPositionUseCase(positionsRepo, concAllowAllPositions{}, concOpenArenasForPositions{}, clock)
	change := positionsapp.NewChangePositionUseCase(positionsRepo, concOpenArenasForPositions{}, uow, clock)

	account := concAccount(t, ctx, pool, "t05-pos@arena.example.com")
	arena := concArena(t, ctx, pool, account, "Afirmação t05 para posições", "t05-pos-arena")
	if _, err := confirm.Execute(ctx, positionsapp.ConfirmInitialPositionCommand{
		AccountID: account, ArenaID: arena, Position: "agree",
	}); err != nil {
		t.Fatalf("seed confirmation: %v", err)
	}
	command := positionsapp.ChangePositionCommand{AccountID: account, ArenaID: arena, Position: "disagree"}

	type changeOutcome struct{}
	outcomes := runRace(t, ctx, 8, func(ctx context.Context, _ int) (changeOutcome, error) {
		_, err := change.Execute(ctx, command)
		return changeOutcome{}, err
	})
	wins := 0
	for i, outcome := range outcomes {
		if outcome.err == nil {
			wins++
			continue
		}
		if !errors.Is(outcome.err, positionsapp.ErrVersionConflict) && !errors.Is(outcome.err, positionsdomain.ErrSamePosition) {
			t.Fatalf("worker %d refused with %v, want version conflict or same-position refusal", i, outcome.err)
		}
	}
	if wins != 1 {
		t.Fatalf("winning changes = %d, want exactly 1", wins)
	}
	if got := concCount(t, ctx, pool, `SELECT count(*) FROM app.position_changes WHERE arena_id = $1::uuid AND account_id = $2::uuid`, arena, account); got != 1 {
		t.Fatalf("stored changes = %d, want exactly 1", got)
	}
}

func TestConcurrentPositionDistinctKeys(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := concClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}

	positionsRepo := positionspg.NewRepository(pool)
	uow := platformpg.NewTxManager(pool)
	confirm := positionsapp.NewConfirmInitialPositionUseCase(positionsRepo, concAllowAllPositions{}, concOpenArenasForPositions{}, clock)
	change := positionsapp.NewChangePositionUseCase(positionsRepo, concOpenArenasForPositions{}, uow, clock)

	type key struct{ account, arena string }
	keys := make([]key, 8)
	for i := range keys {
		account := concAccount(t, ctx, pool, "t05-pos-distinct-"+strconv.Itoa(i)+"@arena.example.com")
		arena := concArena(t, ctx, pool, account, "Afirmação t05 distinta para posições", "t05-pos-distinct-"+strconv.Itoa(i))
		if _, err := confirm.Execute(ctx, positionsapp.ConfirmInitialPositionCommand{
			AccountID: account, ArenaID: arena, Position: "agree",
		}); err != nil {
			t.Fatalf("seed confirmation %d: %v", i, err)
		}
		keys[i] = key{account: account, arena: arena}
	}
	type changeOutcome struct{}
	outcomes := runRace(t, ctx, 8, func(ctx context.Context, i int) (changeOutcome, error) {
		_, err := change.Execute(ctx, positionsapp.ChangePositionCommand{
			AccountID: keys[i].account, ArenaID: keys[i].arena, Position: "disagree",
		})
		return changeOutcome{}, err
	})
	for i, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("worker %d on a distinct key: %v (disjoint keys must not interfere)", i, outcome.err)
		}
	}
	if got := concCount(t, ctx, pool, `SELECT count(*) FROM app.position_changes`); got != 8 {
		t.Fatalf("stored changes = %d, want 8 (one per key)", got)
	}
}

// concPublishWorld wires the real argument-publication stack: repositories,
// funded wallet, published arena, permissive gates and the shared unit of
// work. Eligibility is stubbed (P24 proved the gates); the writes under
// contention are all real.
type concPublishWorld struct {
	publish *argumentsapp.PublishArgumentUseCase
	arenaID string
}

func concPublishWorldFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, clock concClock, author, arenaStatement, slug string) *concPublishWorld {
	t.Helper()
	argumentsRepo := argumentspg.NewRepository(pool)
	walletRepo := walletpg.NewRepository(pool)
	accounts := &concAllowAllArguments{}
	uow := platformpg.NewTxManager(pool)
	publish := argumentsapp.NewPublishArgumentUseCase(
		argumentsRepo, accounts, concOpenArenasForArguments{},
		argumentswallet.New(walletapp.NewDebitInkUseCase(walletRepo, clock)),
		uow, text.GraphemeCount, argumentsdomain.DefaultReplyPolicy(), clock)
	arena := concArena(t, ctx, pool, author, arenaStatement, slug)
	concFund(t, ctx, walletRepo, clock, author, "attrs", 100000)
	return &concPublishWorld{publish: publish, arenaID: arena}
}

func (w *concPublishWorld) publishArgument(t *testing.T, ctx context.Context, author, keySuffix, content string) string {
	t.Helper()
	result, err := w.publish.Execute(ctx, argumentsapp.PublishArgumentCommand{
		AccountID: author, ArenaID: w.arenaID, Relation: "support",
		Content: content, IdempotencyKey: "t05-attr-" + keySuffix,
	})
	if err != nil {
		t.Fatalf("seed argument %s: %v", keySuffix, err)
	}
	return result.Argument.ID.String()
}

func TestConcurrentAttributionLimit(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := concClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	now := clock.Now()

	holder := concAccount(t, ctx, pool, "t05-attr@arena.example.com")
	author := concAccount(t, ctx, pool, "t05-attr-author@arena.example.com")
	world := concPublishWorldFor(t, ctx, pool, clock, author, "Afirmação t05 para atribuições", "t05-attr-arena")
	arguments := make([]string, 5)
	for i := range arguments {
		arguments[i] = world.publishArgument(t, ctx, author, "seed-"+strconv.Itoa(i), "Atribuição t05 número "+strconv.Itoa(i))
	}
	positionsRepo := positionspg.NewRepository(pool)
	holderArena, err := positionsdomain.ParseArenaID(world.arenaID)
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	holderAccount, err := positionsdomain.ParseAccountID(holder)
	if err != nil {
		t.Fatalf("ParseAccountID: %v", err)
	}
	agree, err := positionsdomain.ParsePosition("agree")
	if err != nil {
		t.Fatalf("ParsePosition: %v", err)
	}
	disagree, err := positionsdomain.ParsePosition("disagree")
	if err != nil {
		t.Fatalf("ParsePosition: %v", err)
	}
	if _, _, err := positionsRepo.ConfirmInitialPosition(ctx, holderArena, holderAccount, agree, now); err != nil {
		t.Fatalf("seed confirmation: %v", err)
	}
	// Attributions only credit arguments published before the change, so
	// the change carries a later instant than the seeded arguments.
	firstChange, err := positionsdomain.NewPositionChange(holderArena, holderAccount, agree, disagree, 2, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("NewPositionChange: %v", err)
	}
	firstChangeID, err := positionsRepo.CreatePositionChange(ctx, firstChange)
	if err != nil {
		t.Fatalf("seed change: %v", err)
	}

	persuasionRepo := persuasionpg.NewRepository(pool)
	uow := platformpg.NewTxManager(pool)
	record := persuasionapp.NewRecordAttributionsUseCase(persuasionRepo, persuasiondomain.DefaultEligibilityPolicy(), uow)
	type attributionOutcome struct {
		replayed bool
	}
	outcomes := runRace(t, ctx, 5, func(ctx context.Context, i int) (attributionOutcome, error) {
		result, err := record.Execute(ctx, persuasionapp.RecordAttributionsCommand{
			AccountID: holder, ChangeID: firstChangeID, ArgumentIDs: []string{arguments[i]},
		})
		if err != nil {
			return attributionOutcome{}, err
		}
		return attributionOutcome{replayed: result.Replayed}, nil
	})
	accepted := 0
	for i, outcome := range outcomes {
		if outcome.err != nil {
			if !errors.Is(outcome.err, persuasiondomain.ErrTooManyAttributions) {
				t.Fatalf("worker %d refused with %v, want the three-attribution limit", i, outcome.err)
			}
			continue
		}
		if outcome.value.replayed {
			t.Fatalf("worker %d resolved a distinct argument as replay", i)
		}
		accepted++
	}
	if accepted != 3 {
		t.Fatalf("accepted attributions = %d, want exactly 3 (policy limit)", accepted)
	}
	if got := concCount(t, ctx, pool, `SELECT count(*) FROM app.persuasion_attributions WHERE position_change_id = $1::uuid`, firstChangeID); got != 3 {
		t.Fatalf("stored attributions = %d, want exactly 3", got)
	}
}

func TestConcurrentAttributionSameArgument(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := concClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	now := clock.Now()

	holder := concAccount(t, ctx, pool, "t05-attr-replay@arena.example.com")
	author := concAccount(t, ctx, pool, "t05-attr-replay-author@arena.example.com")
	world := concPublishWorldFor(t, ctx, pool, clock, author, "Afirmação t05 para replay de atribuição", "t05-attr-replay-arena")
	argument := world.publishArgument(t, ctx, author, "replay-seed", "Argumento t05 para replay")
	positionsRepo := positionspg.NewRepository(pool)
	holderArena, err := positionsdomain.ParseArenaID(world.arenaID)
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	holderAccount, err := positionsdomain.ParseAccountID(holder)
	if err != nil {
		t.Fatalf("ParseAccountID: %v", err)
	}
	agree, err := positionsdomain.ParsePosition("agree")
	if err != nil {
		t.Fatalf("ParsePosition: %v", err)
	}
	disagree, err := positionsdomain.ParsePosition("disagree")
	if err != nil {
		t.Fatalf("ParsePosition: %v", err)
	}
	if _, _, err := positionsRepo.ConfirmInitialPosition(ctx, holderArena, holderAccount, agree, now); err != nil {
		t.Fatalf("seed confirmation: %v", err)
	}
	change, err := positionsdomain.NewPositionChange(holderArena, holderAccount, agree, disagree, 2, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("NewPositionChange: %v", err)
	}
	changeID, err := positionsRepo.CreatePositionChange(ctx, change)
	if err != nil {
		t.Fatalf("seed change: %v", err)
	}
	persuasionRepo := persuasionpg.NewRepository(pool)
	uow := platformpg.NewTxManager(pool)
	record := persuasionapp.NewRecordAttributionsUseCase(persuasionRepo, persuasiondomain.DefaultEligibilityPolicy(), uow)
	type attributionOutcome struct {
		replayed bool
	}
	outcomes := runRace(t, ctx, 5, func(ctx context.Context, _ int) (attributionOutcome, error) {
		result, err := record.Execute(ctx, persuasionapp.RecordAttributionsCommand{
			AccountID: holder, ChangeID: changeID, ArgumentIDs: []string{argument},
		})
		if err != nil {
			return attributionOutcome{}, err
		}
		return attributionOutcome{replayed: result.Replayed}, nil
	})
	fresh := 0
	for i, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("worker %d: same-argument replay must succeed, got %v", i, outcome.err)
		}
		if !outcome.value.replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh attributions = %d, want exactly 1 (four replays)", fresh)
	}
	if got := concCount(t, ctx, pool, `SELECT count(*) FROM app.persuasion_attributions WHERE position_change_id = $1::uuid`, changeID); got != 1 {
		t.Fatalf("stored attributions = %d, want exactly 1", got)
	}
}

// concSignWebhook signs one delivery exactly the way the provider does:
// HMAC-SHA256(secret, "timestamp.body") rendered as t=,v1=.
func concSignWebhook(secret string, at time.Time, body []byte) (sigHeader, tsHeader string) {
	tsHeader = strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsHeader + "." + string(body)))
	return "t=" + tsHeader + ",v1=" + hex.EncodeToString(mac.Sum(nil)), tsHeader
}

func TestConcurrentWebhookClaim(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := concClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	now := clock.Now()

	const webhookSecret = "whsec_t05schedules"
	verifier, err := stripeadapter.NewWebhookVerifier(webhookSecret, time.Hour, clock)
	if err != nil {
		t.Fatalf("NewWebhookVerifier: %v", err)
	}
	events := billingpg.NewRepositoryWithClock(pool, clock)
	webhook, err := billingapp.NewProcessWebhookUseCase(billingapp.ProcessWebhookDependencies{
		Verifier: verifier, Events: events, Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase: %v", err)
	}
	body := []byte(`{"id":"evt_t05race1","type":"payment_intent.succeeded","livemode":false,"created":1758792000}`)
	sigHeader, tsHeader := concSignWebhook(webhookSecret, now, body)
	command := billingapp.ProcessWebhookCommand{RawBody: body, SignatureHeader: sigHeader, TimestampHeader: tsHeader}

	type webhookOutcome struct{}
	outcomes := runRace(t, ctx, 8, func(ctx context.Context, _ int) (webhookOutcome, error) {
		return webhookOutcome{}, webhook.Execute(ctx, command)
	})
	for i, outcome := range outcomes {
		if outcome.err != nil && !errors.Is(outcome.err, billingapp.ErrWebhookEventInProcessing) {
			t.Fatalf("worker %d: %v, want nil or in-processing replay", i, outcome.err)
		}
	}
	if got := concCount(t, ctx, pool, `SELECT count(*) FROM app.stripe_events WHERE stripe_event_id = 'evt_t05race1'`); got != 1 {
		t.Fatalf("stored webhook events = %d, want exactly 1", got)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.stripe_events WHERE stripe_event_id = 'evt_t05race1'`).Scan(&status); err != nil {
		t.Fatalf("read event status: %v", err)
	}
	if status != "processed" {
		t.Fatalf("event status = %q, want processed", status)
	}
	if got := concCount(t, ctx, pool, `SELECT count(*) FROM app.checkout_intents`); got != 0 {
		t.Fatalf("checkout intents = %d, want 0 (unknown type settles nothing)", got)
	}
	if err := webhook.Execute(ctx, command); err != nil {
		t.Fatalf("sequential replay after terminal event: %v", err)
	}
}

func TestConcurrentJobLease(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	repository := jobspg.NewRepository(pool)
	now := time.Now().UTC()

	job, _, err := repository.Enqueue(ctx, jobsapp.EnqueueRecord{
		Type: jobsdomain.TypeEmailDelivery, Version: 1, Payload: []byte(`{"account_id":"t05"}`),
		AvailableAt: now, MaxAttempts: 3, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	type leaseOutcome struct {
		leased bool
	}
	outcomes := runRace(t, ctx, 6, func(ctx context.Context, i int) (leaseOutcome, error) {
		claimed, err := repository.Lease(ctx, "t05-worker-"+strconv.Itoa(i), now, now.Add(5*time.Minute))
		if err != nil {
			return leaseOutcome{}, err
		}
		return leaseOutcome{leased: claimed != nil}, nil
	})
	winners := 0
	for i, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("worker %d: lease must succeed or pass cleanly, got %v", i, outcome.err)
		}
		if outcome.value.leased {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("lease winners = %d, want exactly 1 (job %s)", winners, job.ID)
	}
	if got := concCount(t, ctx, pool, `SELECT count(*) FROM app.jobs WHERE lease_owner IS NOT NULL`); got != 1 {
		t.Fatalf("leased jobs = %d, want exactly 1", got)
	}
}

func TestConcurrentJobLeaseCancellation(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	repository := jobspg.NewRepository(pool)
	now := time.Now().UTC()

	if _, _, err := repository.Enqueue(ctx, jobsapp.EnqueueRecord{
		Type: jobsdomain.TypeEmailDelivery, Version: 1, Payload: []byte(`{"account_id":"t05"}`),
		AvailableAt: now, MaxAttempts: 3, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	_, err := repository.Lease(cancelled, "t05-cancelled-worker", now, now.Add(5*time.Minute))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lease = %v, want context.Canceled without a claim", err)
	}
	if got := concCount(t, ctx, pool, `SELECT count(*) FROM app.jobs WHERE lease_owner IS NOT NULL`); got != 0 {
		t.Fatalf("leased jobs after cancellation = %d, want 0", got)
	}
}

func TestConcurrentModerationClaim(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()

	repository := moderationpg.NewRepository(pool)
	reporter := concAccount(t, ctx, pool, "t05-reporter@arena.example.com")
	targetArena := concArena(t, ctx, pool, reporter, "Arena t05 para denúncia concorrente", "t05-report-arena")
	var caseID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.moderation_cases (target_type, target_arena_id)
		VALUES ('arena', $1::uuid)
		RETURNING id::text`, targetArena).Scan(&caseID); err != nil {
		t.Fatalf("seed review case: %v", err)
	}
	reviewerOne := concAccount(t, ctx, pool, "t05-reviewer-1@arena.example.com")
	reviewerTwo := concAccount(t, ctx, pool, "t05-reviewer-2@arena.example.com")
	now := time.Now().UTC()

	type claimOutcome struct{}
	actors := []string{reviewerOne, reviewerTwo}
	outcomes := runRace(t, ctx, 2, func(ctx context.Context, i int) (claimOutcome, error) {
		_, err := repository.ClaimCase(ctx, moderationapp.ClaimCaseRequest{
			CaseID: caseID, Actor: moderationdomain.AccountID(actors[i]), ClaimedAt: now,
		})
		return claimOutcome{}, err
	})
	wins := 0
	for i, outcome := range outcomes {
		if outcome.err == nil {
			wins++
			continue
		}
		if !errors.Is(outcome.err, moderationapp.ErrCaseAlreadyClaimed) {
			t.Fatalf("reviewer %d refused with %v, want ErrCaseAlreadyClaimed", i, outcome.err)
		}
	}
	if wins != 1 {
		t.Fatalf("claim winners = %d, want exactly 1", wins)
	}
	var holder string
	if err := pool.QueryRow(ctx, `SELECT claimed_by::text FROM app.moderation_cases WHERE id = $1::uuid`, caseID).Scan(&holder); err != nil {
		t.Fatalf("read claim holder: %v", err)
	}
	if holder != reviewerOne && holder != reviewerTwo {
		t.Fatalf("claim holder = %q, want one of the two reviewers", holder)
	}
}
