package postgres_test

// P35-T05 — INK purchase intents with commercial-stock holds on real
// PostgreSQL.
//
// The buyer names key, account, quotation and fiat ticket: one
// transaction seals the intent with the hold that backs it, priced
// from the stored quotation with the server schedule. The tests prove
// on a disposable database: exact acceptance with its hold and legs,
// replay without duplication, divergent replays conflicting, expired
// and unknown quotes refusing, uncovered stock writing nothing, two
// racing sales never overselling, eight replays collapsing to one,
// third parties untouched and unknown accounts refused.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	pricepostgres "github.com/AlexandreZanata/Regnovum/internal/pricing/adapters/postgres"
	priceapp "github.com/AlexandreZanata/Regnovum/internal/pricing/application"
	pricedomain "github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// intentClock fixes acceptance and liveness instants.
type intentClock struct {
	now time.Time
}

func (c *intentClock) Now() time.Time { return c.now }

func intentCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func intentInstant() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

func intentSchedule() pricedomain.FeeSchedule {
	return pricedomain.FeeSchedule{
		Version:      1,
		SatsPerInk:   100,
		SpreadBps:    100,
		FeeMinor:     30,
		TaxBps:       1000,
		MinFiatMinor: 100,
		StepMilliInk: 1,
		Rounding:     pricedomain.RoundDown,
	}
}

func mustIntentRepo(t *testing.T, pool *pgxpool.Pool, clock *intentClock) *postgres.PurchaseIntentRepository {
	t.Helper()
	repo, err := postgres.NewPurchaseIntentRepository(pool, clock, intentSchedule())
	if err != nil {
		t.Fatalf("NewPurchaseIntentRepository: %v", err)
	}
	return repo
}

func intentAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('intent-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

// fundCommercialStock opens the Treasury home and the commercial vault
// with a balanced funding pair, so the vault holds exactly millis with
// zero net supply effect.
func fundCommercialStock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, millis int64) {
	t.Helper()
	var home, vault string
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', 'main'), ('treasury', 'commercial_stock') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("open vaults: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main'`).Scan(&home); err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'commercial_stock'`).Scan(&vault); err != nil {
		t.Fatalf("resolve vault: %v", err)
	}
	var transfer string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $3), ($1::uuid, $4::uuid, 'credit', $3)`,
		transfer, home, millis, vault); err != nil {
		t.Fatalf("fund vault: %v", err)
	}
}

func vaultBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind, label string) int64 {
	t.Helper()
	var balance int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = $1 AND c.label = $2`, kind, label).Scan(&balance); err != nil {
		t.Fatalf("balance %s/%s: %v", kind, label, err)
	}
	return balance
}

func journalLegs(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var legs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries`).Scan(&legs); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	return legs
}

func intentCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.billing_ink_intents`).Scan(&count); err != nil {
		t.Fatalf("count intents: %v", err)
	}
	return count
}

// sealQuote accepts one live quotation through the real pricing port:
// the intent tests price from stored snapshots, never from fixtures.
func sealQuote(t *testing.T, ctx context.Context, pool *pgxpool.Pool, clock *intentClock, ttl time.Duration) string {
	t.Helper()
	repo := pricepostgres.NewRepository(pool)
	uc, err := priceapp.NewAcceptQuoteUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewAcceptQuoteUseCase: %v", err)
	}
	sightings := []priceapp.SightingInput{
		{Source: "fonte-1", PriceMinor: 35000000, ObservedAt: intentInstant(), Payload: []byte(`{"a":1}`)},
		{Source: "fonte-2", PriceMinor: 35010000, ObservedAt: intentInstant().Add(time.Second), Payload: []byte(`{"a":2}`)},
		{Source: "fonte-3", PriceMinor: 34990000, ObservedAt: intentInstant().Add(2 * time.Second), Payload: []byte(`{"a":3}`)},
	}
	quote, err := uc.Execute(ctx, priceapp.AcceptQuoteCommand{
		PriceMinor: 35000000, Sightings: sightings,
		TTL: ttl, MaxSkew: time.Minute, MaxAge: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("AcceptQuote: %v", err)
	}
	return quote.ID
}

func acceptCmd(key, account, quote string, fiat int64) application.AcceptPurchaseCommand {
	return application.AcceptPurchaseCommand{IntentKey: key, AccountID: account, QuoteID: quote, FiatMinor: fiat}
}

func mustAccept(t *testing.T, ctx context.Context, repo *postgres.PurchaseIntentRepository, cmd application.AcceptPurchaseCommand) *application.PurchaseIntentResult {
	t.Helper()
	uc, err := application.NewAcceptPurchaseUseCase(repo)
	if err != nil {
		t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
	}
	result, err := uc.Execute(ctx, cmd)
	if err != nil {
		t.Fatalf("AcceptPurchase: %v", err)
	}
	return result
}

// TestPurchaseIntentSettlesWithHold proves the happy path: R$100,00
// against the live quote settles 253833 milliINK with the backing
// hold, the vault keeps the remainder and the journal stays balanced.
func TestPurchaseIntentSettlesWithHold(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	repo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 300000)
	quote := sealQuote(t, ctx, pool, clock, 5*time.Minute)
	account := intentAccount(t, ctx, pool)

	result := mustAccept(t, ctx, repo, acceptCmd("intent-1", account, quote, 10000))
	if result.Replayed || result.InkMilli != 253833 || result.HoldID == "" {
		t.Fatalf("result = %+v, want 253833 fresh with hold", result)
	}
	if got := vaultBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 300000-253833 {
		t.Fatalf("vault = %d, want %d", got, 300000-253833)
	}
	var hold int64
	if err := pool.QueryRow(ctx,
		`SELECT amount_milli FROM app.economy_holds WHERE id = $1::uuid`, result.HoldID).Scan(&hold); err != nil {
		t.Fatalf("read hold: %v", err)
	}
	if hold != 253833 {
		t.Fatalf("hold = %d, want 253833", hold)
	}
	var status, terms string
	var fiat, ink int64
	if err := pool.QueryRow(ctx,
		`SELECT status, terms_hash, fiat_minor, ink_milli FROM app.billing_ink_intents WHERE id::text = $1`,
		result.IntentID).Scan(&status, &terms, &fiat, &ink); err != nil {
		t.Fatalf("read intent: %v", err)
	}
	if status != "pending" || fiat != 10000 || ink != 253833 || terms == "" {
		t.Fatalf("intent row changed: %s/%d/%d", status, fiat, ink)
	}
}

// TestPurchaseIntentReplaysWithoutDuplicating proves the key settles
// once: the second acceptance resolves the original intent and hold
// with zero new legs and zero new rows.
func TestPurchaseIntentReplaysWithoutDuplicating(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	repo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 300000)
	quote := sealQuote(t, ctx, pool, clock, 5*time.Minute)
	account := intentAccount(t, ctx, pool)
	cmd := acceptCmd("intent-replay", account, quote, 10000)

	first := mustAccept(t, ctx, repo, cmd)
	legs := journalLegs(t, ctx, pool)
	second := mustAccept(t, ctx, repo, cmd)
	if !second.Replayed || second.IntentID != first.IntentID || second.HoldID != first.HoldID {
		t.Fatalf("replay settled another acceptance: %+v vs %+v", second, first)
	}
	if intentCount(t, ctx, pool) != 1 {
		t.Fatalf("intents duplicated on replay")
	}
	if got := journalLegs(t, ctx, pool); got != legs {
		t.Fatalf("legs = %d, want %d untouched", got, legs)
	}
}

// TestPurchaseIntentRefusesDivergentTerms proves a reused key with a
// different ticket conflicts instead of provisioning twice: the buyer
// never reprices through a replay.
func TestPurchaseIntentRefusesDivergentTerms(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	repo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 300000)
	quote := sealQuote(t, ctx, pool, clock, 5*time.Minute)
	account := intentAccount(t, ctx, pool)
	uc, err := application.NewAcceptPurchaseUseCase(repo)
	if err != nil {
		t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
	}

	if _, err := uc.Execute(ctx, acceptCmd("intent-terms", account, quote, 10000)); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if _, err := uc.Execute(ctx, acceptCmd("intent-terms", account, quote, 20000)); !errors.Is(err, application.ErrPurchaseIntentConflict) {
		t.Fatalf("divergent terms = %v, want ErrPurchaseIntentConflict", err)
	}
	if intentCount(t, ctx, pool) != 1 {
		t.Fatalf("conflict provisioned a second intent")
	}
}

// TestPurchaseIntentRefusesExpiredQuote proves the lifetime counts
// from acceptance: a lapsed quotation refuses with nothing written
// anywhere.
func TestPurchaseIntentRefusesExpiredQuote(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	repo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 300000)
	quote := sealQuote(t, ctx, pool, clock, time.Minute)
	account := intentAccount(t, ctx, pool)
	uc, err := application.NewAcceptPurchaseUseCase(repo)
	if err != nil {
		t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
	}

	clock.now = intentInstant().Add(10 * time.Minute)
	legs := journalLegs(t, ctx, pool)
	if _, err := uc.Execute(ctx, acceptCmd("intent-late", account, quote, 10000)); !errors.Is(err, application.ErrPurchaseQuoteExpired) {
		t.Fatalf("lapsed quote = %v, want ErrPurchaseQuoteExpired", err)
	}
	if intentCount(t, ctx, pool) != 0 {
		t.Fatalf("lapsed quote recorded an intent")
	}
	if got := journalLegs(t, ctx, pool); got != legs {
		t.Fatalf("legs moved on a refused quote")
	}
}

// TestPurchaseIntentRefusesUnknownQuote proves unknown quotations
// resolve to absence instead of a guess.
func TestPurchaseIntentRefusesUnknownQuote(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	repo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 300000)
	account := intentAccount(t, ctx, pool)
	uc, err := application.NewAcceptPurchaseUseCase(repo)
	if err != nil {
		t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
	}

	if _, err := uc.Execute(ctx, acceptCmd("intent-ghost",
		account, "00000000-0000-4000-8000-000000000000", 10000)); !errors.Is(err, application.ErrPurchaseQuoteNotFound) {
		t.Fatalf("unknown quote = %v, want ErrPurchaseQuoteNotFound", err)
	}
	if intentCount(t, ctx, pool) != 0 {
		t.Fatalf("unknown quote recorded an intent")
	}
}

// TestPurchaseIntentRefusesWithoutStock proves uncovered stock writes
// nothing: no intent, no hold and no legs exist without cover.
func TestPurchaseIntentRefusesWithoutStock(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	repo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 100)
	quote := sealQuote(t, ctx, pool, clock, 5*time.Minute)
	account := intentAccount(t, ctx, pool)
	uc, err := application.NewAcceptPurchaseUseCase(repo)
	if err != nil {
		t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
	}

	legs := journalLegs(t, ctx, pool)
	if _, err := uc.Execute(ctx, acceptCmd("intent-broke", account, quote, 10000)); !errors.Is(err, application.ErrInsufficientCommercialStock) {
		t.Fatalf("uncovered stock = %v, want ErrInsufficientCommercialStock", err)
	}
	if intentCount(t, ctx, pool) != 0 {
		t.Fatalf("uncovered stock recorded an intent")
	}
	if got := journalLegs(t, ctx, pool); got != legs {
		t.Fatalf("legs moved without cover")
	}
}

// TestPurchaseIntentRacesNeverOversell proves two simultaneous sales
// against one vault settle at most the cover: one provisions, the
// other refuses for insufficiency.
func TestPurchaseIntentRacesNeverOversell(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := intentCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	repo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 300000)
	quote := sealQuote(t, ctx, pool, clock, 5*time.Minute)
	first := intentAccount(t, ctx, pool)
	second := intentAccount(t, ctx, pool)

	var wg sync.WaitGroup
	type outcome struct {
		result *application.PurchaseIntentResult
		err    error
	}
	outcomes := make([]outcome, 2)
	for i, cmd := range []application.AcceptPurchaseCommand{
		acceptCmd("race-1", first, quote, 10000),
		acceptCmd("race-2", second, quote, 10000),
	} {
		wg.Add(1)
		go func(i int, cmd application.AcceptPurchaseCommand) {
			defer wg.Done()
			uc, err := application.NewAcceptPurchaseUseCase(repo)
			if err != nil {
				t.Errorf("NewAcceptPurchaseUseCase: %v", err)
				return
			}
			outcomes[i].result, outcomes[i].err = uc.Execute(ctx, cmd)
		}(i, cmd)
	}
	wg.Wait()
	won, short := 0, 0
	for _, outcome := range outcomes {
		if outcome.err == nil {
			won++
			continue
		}
		if errors.Is(outcome.err, application.ErrInsufficientCommercialStock) {
			short++
			continue
		}
		t.Fatalf("race outcome = %+v", outcome.err)
	}
	if won != 1 || short != 1 {
		t.Fatalf("won = %d, short = %d; want exactly 1 and 1", won, short)
	}
	if intentCount(t, ctx, pool) != 1 {
		t.Fatalf("race provisioned twice")
	}
}

// TestPurchaseIntentRacesCollapseToOne proves eight simultaneous
// retries of one key settle a single acceptance: one executes, seven
// replay.
func TestPurchaseIntentRacesCollapseToOne(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := intentCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	repo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 300000)
	quote := sealQuote(t, ctx, pool, clock, 5*time.Minute)
	account := intentAccount(t, ctx, pool)

	const runners = 8
	var wg sync.WaitGroup
	results := make([]*application.PurchaseIntentResult, runners)
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uc, err := application.NewAcceptPurchaseUseCase(repo)
			if err != nil {
				errs[i] = err
				return
			}
			results[i], errs[i] = uc.Execute(ctx, acceptCmd("intent-race", account, quote, 10000))
		}(i)
	}
	wg.Wait()
	founded, replayed := 0, 0
	var intentID string
	for i := range runners {
		if errs[i] != nil {
			t.Fatalf("runner %d: %v", i, errs[i])
		}
		if results[i].Replayed {
			replayed++
		} else {
			founded++
		}
		if intentID == "" {
			intentID = results[i].IntentID
		} else if results[i].IntentID != intentID {
			t.Fatalf("runner %d settled another acceptance", i)
		}
	}
	if founded != 1 || replayed != runners-1 {
		t.Fatalf("founded = %d, replayed = %d; want 1 and %d", founded, replayed, runners-1)
	}
	if intentCount(t, ctx, pool) != 1 {
		t.Fatalf("race key provisioned twice")
	}
}

// TestPurchaseIntentLeavesThirdPartiesUntouched proves the acceptance
// is scoped: foreign escrows keep every unit while the vault pays the
// hold, and unknown accounts refuse without writing.
func TestPurchaseIntentLeavesThirdPartiesUntouched(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	repo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 600000)
	quote := sealQuote(t, ctx, pool, clock, 5*time.Minute)
	account := intentAccount(t, ctx, pool)
	uc, err := application.NewAcceptPurchaseUseCase(repo)
	if err != nil {
		t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('escrow', 'stranger')`); err != nil {
		t.Fatalf("open stranger: %v", err)
	}
	var stranger string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'escrow' AND label = 'stranger'`).Scan(&stranger); err != nil {
		t.Fatalf("resolve stranger: %v", err)
	}
	var funding string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&funding); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	var home string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main'`).Scan(&home); err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', 700), ($1::uuid, $3::uuid, 'credit', 700)`,
		funding, home, stranger); err != nil {
		t.Fatalf("fund stranger: %v", err)
	}

	if _, err := uc.Execute(ctx, acceptCmd("intent-scoped", account, quote, 10000)); err != nil {
		t.Fatalf("AcceptPurchase: %v", err)
	}
	if got := vaultBalance(t, ctx, pool, "escrow", "stranger"); got != 700 {
		t.Fatalf("stranger = %d, want 700 untouched", got)
	}
	if _, err := uc.Execute(ctx, acceptCmd("intent-ghost-account",
		"00000000-0000-4000-8000-000000000099", quote, 10000)); !errors.Is(err, application.ErrPurchaserNotFound) {
		t.Fatalf("ghost account = %v, want ErrPurchaserNotFound", err)
	}
	if intentCount(t, ctx, pool) != 1 {
		t.Fatalf("ghost account recorded an intent")
	}
}
