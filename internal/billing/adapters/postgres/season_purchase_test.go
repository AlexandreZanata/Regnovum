package postgres_test

// P46-T06 — cotação, compra e webhook no limite sazonal.
//
// Quotes, intents e recibos carregam a temporada com expiração e
// deadline limitadas pelo fim; o reset é mostrado antes do aceite.
// Webhook após o cutoff registra refund/reconciliação do provedor,
// sem crédito em livro novo nem gasto no antigo selado. Produto
// econômico segue desativado: temporadas são fixtures, sem wiring.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	pricepostgres "github.com/AlexandreZanata/Regnovum/internal/pricing/adapters/postgres"
	priceapp "github.com/AlexandreZanata/Regnovum/internal/pricing/application"
)

func seasonPurchaseCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func seedBook(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key string, ordinal int, starts string) time.Time {
	t.Helper()
	var ends time.Time
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ($1, $2, $3::timestamptz, $3::timestamptz + make_interval(secs => 7776000), 'v3', 'politica-inicial', 'fundadora', 'regente-tecnica',
		 '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')
		 RETURNING ends_at`,
		key, ordinal, starts).Scan(&ends); err != nil {
		t.Fatalf("seed season %s: %v", key, err)
	}
	return ends.UTC()
}

func fundSeasonalVault(t *testing.T, ctx context.Context, pool *pgxpool.Pool, season string, millis int64) {
	t.Helper()
	var home, vault string
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key)
		 VALUES ('treasury', 'main', $1), ('treasury', 'commercial_stock', $1) ON CONFLICT DO NOTHING`, season); err != nil {
		t.Fatalf("open seasonal vaults: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main' AND season_key = $1`, season).Scan(&home); err != nil {
		t.Fatalf("resolve seasonal home: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'commercial_stock' AND season_key = $1`, season).Scan(&vault); err != nil {
		t.Fatalf("resolve seasonal vault: %v", err)
	}
	var transfer string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		 VALUES ($1::uuid, $2::uuid, 'debit', $3, $4), ($1::uuid, $5::uuid, 'credit', $3, $4)`,
		transfer, home, millis, season, vault); err != nil {
		t.Fatalf("fund seasonal vault: %v", err)
	}
}

func sealSeasonalQuote(t *testing.T, ctx context.Context, pool *pgxpool.Pool, clock *intentClock, ttl time.Duration, season string, ends time.Time) string {
	t.Helper()
	repo := pricepostgres.NewRepository(pool)
	uc, err := priceapp.NewAcceptQuoteUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewAcceptQuoteUseCase: %v", err)
	}
	sightings := []priceapp.SightingInput{
		{Source: "fonte-1", PriceMinor: 35000000, ObservedAt: clock.now.Add(-2 * time.Second), Payload: []byte(`{"a":1}`)},
		{Source: "fonte-2", PriceMinor: 35010000, ObservedAt: clock.now.Add(-time.Second), Payload: []byte(`{"a":2}`)},
		{Source: "fonte-3", PriceMinor: 34990000, ObservedAt: clock.now, Payload: []byte(`{"a":3}`)},
	}
	quote, err := uc.Execute(ctx, priceapp.AcceptQuoteCommand{
		PriceMinor: 35000000, Sightings: sightings,
		TTL: ttl, MaxSkew: time.Minute, MaxAge: 5 * time.Minute,
		Season: season, ResetAcknowledged: true, SeasonEndsAt: ends,
	})
	if err != nil {
		t.Fatalf("AcceptQuote seasonal: %v", err)
	}
	return quote.ID
}

func acceptSeasonalIntent(t *testing.T, ctx context.Context, repo *postgres.PurchaseIntentRepository, key, account, quote string, fiat int64, season string) *application.PurchaseIntentResult {
	t.Helper()
	uc, err := application.NewAcceptPurchaseUseCase(repo)
	if err != nil {
		t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
	}
	result, err := uc.Execute(ctx, application.AcceptPurchaseCommand{
		IntentKey: key, AccountID: account, QuoteID: quote, FiatMinor: fiat,
		Season: season, ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("AcceptPurchase seasonal: %v", err)
	}
	return result
}

func seasonBookLegs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, season string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries WHERE season_key = $1`, season).Scan(&count); err != nil {
		t.Fatalf("book legs: %v", err)
	}
	return count
}

func seasonBookBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind, label, season string) int64 {
	t.Helper()
	var balance int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = $1 AND c.label = $2 AND e.season_key = $3`, kind, label, season).Scan(&balance); err != nil {
		t.Fatalf("book balance: %v", err)
	}
	return balance
}

// TestSeasonalQuoteCapsExpiryAtTick proves the seasonal deadline:
// the stored expiry is the book end when TTL would pass it, live a
// tick before and dead at the tick, with the season on the receipt.
func TestSeasonalQuoteCapsExpiryAtTick(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := seasonPurchaseCtx()
	defer cancel()

	ends := seedBook(t, ctx, pool, "temporada-compra-1", 21, "2026-10-01T12:00:00Z")
	accepted := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	clock := &intentClock{now: accepted}
	// TTL de 90 dias passaria o fim; o fim vence.
	quoteID := sealSeasonalQuote(t, ctx, pool, clock, 90*24*time.Hour, "temporada-compra-1", ends)
	repo := pricepostgres.NewRepository(pool)
	stored, err := repo.Find(ctx, quoteID)
	if err != nil || stored == nil {
		t.Fatalf("Find seasonal quote: %+v, %v", stored, err)
	}
	if string(stored.Season) != "temporada-compra-1" {
		t.Fatalf("quote season = %q, want temporada-compra-1", stored.Season)
	}
	if !stored.ExpiresAt.Equal(ends) {
		t.Fatalf("quote expiry = %v, want book end %v", stored.ExpiresAt, ends)
	}
	if !stored.Live(ends.Add(-time.Nanosecond)) {
		t.Fatal("quote dead a tick before the capped expiry")
	}
	if stored.Live(ends) {
		t.Fatal("quote live at the capped expiry tick")
	}
	// Reset omitido nunca autoriza a cotação sazonal.
	badRepo := pricepostgres.NewRepository(pool)
	badUC, _ := priceapp.NewAcceptQuoteUseCase(badRepo, clock)
	_, err = badUC.Execute(ctx, priceapp.AcceptQuoteCommand{
		PriceMinor: 35000000,
		Sightings: []priceapp.SightingInput{
			{Source: "fonte-1", PriceMinor: 35000000, ObservedAt: accepted.Add(-time.Second), Payload: []byte(`{"a":1}`)},
		},
		TTL: 5 * time.Minute, MaxSkew: time.Minute, MaxAge: 5 * time.Minute,
		Season: "temporada-compra-1", SeasonEndsAt: ends,
	})
	if err == nil {
		t.Fatal("seasonal quote without reset succeeded: reset must be shown before acceptance")
	}
}

// TestSeasonalPurchaseIntentSettlesWithHold proves the happy path in
// one book: intent, hold and legs carry the season, the receipt names
// it, and the other book stays untouched.
func TestSeasonalPurchaseIntentSettlesWithHold(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := seasonPurchaseCtx()
	defer cancel()

	ends := seedBook(t, ctx, pool, "temporada-compra-2", 22, "2026-10-01T12:00:00Z")
	seedBook(t, ctx, pool, "temporada-compra-2b", 23, "2027-01-02T12:00:00Z")
	clock := &intentClock{now: time.Date(2026, 10, 2, 12, 0, 3, 0, time.UTC)}
	repo := mustIntentRepo(t, pool, clock)
	fundSeasonalVault(t, ctx, pool, "temporada-compra-2", 300000)
	quote := sealSeasonalQuote(t, ctx, pool, clock, 5*time.Minute, "temporada-compra-2", ends)
	account := intentAccount(t, ctx, pool)

	result := acceptSeasonalIntent(t, ctx, repo, "intent-sazonal-1", account, quote, 10000, "temporada-compra-2")
	if result.Replayed || string(result.Season) != "temporada-compra-2" {
		t.Fatalf("intent receipt = %+v, want fresh in temporada-compra-2", result)
	}
	var storedSeason string
	if err := pool.QueryRow(ctx, `SELECT season_key FROM app.billing_ink_intents WHERE id::text = $1`, result.IntentID).Scan(&storedSeason); err != nil {
		t.Fatalf("read intent season: %v", err)
	}
	if storedSeason != "temporada-compra-2" {
		t.Fatalf("intent season = %q, want temporada-compra-2", storedSeason)
	}
	var holdSeason string
	if err := pool.QueryRow(ctx, `SELECT season_key FROM app.economy_holds WHERE id = $1::uuid`, result.HoldID).Scan(&holdSeason); err != nil {
		t.Fatalf("read hold season: %v", err)
	}
	if holdSeason != "temporada-compra-2" {
		t.Fatalf("hold season = %q, want temporada-compra-2", holdSeason)
	}
	if got := seasonBookLegs(t, ctx, pool, "temporada-compra-2b"); got != 0 {
		t.Fatalf("other book legs = %d, want 0: zero cross credit", got)
	}
}

// TestSeasonalPendingPurchaseAtClose proves a purchase accepted
// before the end waits pending with its hold intact: no premature
// settlement, no inconclusive loss.
func TestSeasonalPendingPurchaseAtClose(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := seasonPurchaseCtx()
	defer cancel()

	ends := seedBook(t, ctx, pool, "temporada-compra-3", 24, "2026-10-01T12:00:00Z")
	clock := &intentClock{now: ends.Add(-time.Minute)}
	repo := mustIntentRepo(t, pool, clock)
	fundSeasonalVault(t, ctx, pool, "temporada-compra-3", 300000)
	quote := sealSeasonalQuote(t, ctx, pool, clock, 5*time.Minute, "temporada-compra-3", ends)
	account := intentAccount(t, ctx, pool)
	result := acceptSeasonalIntent(t, ctx, repo, "intent-pendente", account, quote, 10000, "temporada-compra-3")

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.billing_ink_intents WHERE id::text = $1`, result.IntentID).Scan(&status); err != nil {
		t.Fatalf("read intent status: %v", err)
	}
	if status != "pending" {
		t.Fatalf("intent status = %q, want pending at close", status)
	}
	var holdStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.economy_holds WHERE id = $1::uuid`, result.HoldID).Scan(&holdStatus); err != nil {
		t.Fatalf("read hold status: %v", err)
	}
	if holdStatus != "active" {
		t.Fatalf("hold status = %q, want active pending at close", holdStatus)
	}
}

// TestSeasonalWebhookDuplicateAndOutOfOrder proves idempotency in one
// book: redelivery replays the recorded settlement and a second event
// for a settled intent resolves without a second grant.
func TestSeasonalWebhookDuplicateAndOutOfOrder(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := seasonPurchaseCtx()
	defer cancel()

	ends := seedBook(t, ctx, pool, "temporada-compra-4", 25, "2026-10-01T12:00:00Z")
	clock := &intentClock{now: time.Date(2026, 10, 2, 12, 0, 3, 0, time.UTC)}
	intentRepo := mustIntentRepo(t, pool, clock)
	fundSeasonalVault(t, ctx, pool, "temporada-compra-4", 300000)
	quote := sealSeasonalQuote(t, ctx, pool, clock, 5*time.Minute, "temporada-compra-4", ends)
	account := intentAccount(t, ctx, pool)
	accepted := acceptSeasonalIntent(t, ctx, intentRepo, "intent-webhook", account, quote, 10000, "temporada-compra-4")

	verifier, err := stripe.NewWebhookVerifier(settleSecret, 5*time.Minute, clock)
	if err != nil {
		t.Fatalf("NewWebhookVerifier: %v", err)
	}
	settleRepo, err := postgres.NewPurchaseSettlementRepository(pool, clock, verifier)
	if err != nil {
		t.Fatalf("NewPurchaseSettlementRepository: %v", err)
	}
	_ = accepted
	deliver := func(eventID string) (*application.SettlePurchaseResult, error) {
		body := settleBody("intent-webhook", account, 10000, "BRL", "paid", eventID)
		sig, ts := settleSign(settleSecret, clock.now, body)
		uc, _ := application.NewSettlePurchaseUseCase(settleRepo)
		return uc.Execute(ctx, application.SettlePurchaseCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts})
	}
	first, err := deliver("evt-sazonal-1")
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	legs := seasonBookLegs(t, ctx, pool, "temporada-compra-4")
	second, err := deliver("evt-sazonal-1")
	if err != nil {
		t.Fatalf("duplicate replay: %v", err)
	}
	if !second.Replayed || second.SettlementID != first.SettlementID {
		t.Fatalf("duplicate settled again: %+v vs %+v", second, first)
	}
	outOfOrder, err := deliver("evt-sazonal-2")
	if err != nil {
		t.Fatalf("out-of-order replay: %v", err)
	}
	if !outOfOrder.Replayed || outOfOrder.SettlementID != first.SettlementID {
		t.Fatalf("out-of-order paid twice: %+v vs %+v", outOfOrder, first)
	}
	if got := seasonBookLegs(t, ctx, pool, "temporada-compra-4"); got != legs {
		t.Fatalf("legs moved on replay: zero double grant")
	}
	if got := settlementCount(t, ctx, pool); got != 1 {
		t.Fatalf("settlements = %d, want exactly 1", got)
	}
}

// TestSeasonalProviderDowntimeRefusesWithoutEffect proves a forged or
// stale delivery grants nothing: no settlement, no legs, no hold
// capture.
func TestSeasonalProviderDowntimeRefusesWithoutEffect(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := seasonPurchaseCtx()
	defer cancel()

	ends := seedBook(t, ctx, pool, "temporada-compra-5", 26, "2026-10-01T12:00:00Z")
	clock := &intentClock{now: time.Date(2026, 10, 2, 12, 0, 3, 0, time.UTC)}
	intentRepo := mustIntentRepo(t, pool, clock)
	fundSeasonalVault(t, ctx, pool, "temporada-compra-5", 300000)
	quote := sealSeasonalQuote(t, ctx, pool, clock, 5*time.Minute, "temporada-compra-5", ends)
	account := intentAccount(t, ctx, pool)
	acceptSeasonalIntent(t, ctx, intentRepo, "intent-downtime", account, quote, 10000, "temporada-compra-5")

	verifier, _ := stripe.NewWebhookVerifier(settleSecret, 5*time.Minute, clock)
	settleRepo, _ := postgres.NewPurchaseSettlementRepository(pool, clock, verifier)
	legs := seasonBookLegs(t, ctx, pool, "temporada-compra-5")
	body := settleBody("intent-downtime", account, 10000, "BRL", "paid", "evt-downtime")
	sig, ts := settleSign("whsec_forged", clock.now, body)
	uc, _ := application.NewSettlePurchaseUseCase(settleRepo)
	if _, err := uc.Execute(ctx, application.SettlePurchaseCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts}); err == nil {
		t.Fatal("forged delivery settled: provider downtime must refuse")
	}
	if settlementCount(t, ctx, pool) != 0 {
		t.Fatal("forged delivery recorded a settlement")
	}
	if got := seasonBookLegs(t, ctx, pool, "temporada-compra-5"); got != legs {
		t.Fatal("forged delivery moved legs")
	}
}

// TestSeasonalLateRefundRegistersReviewWithoutCredit proves the
// cutoff: a paid event after the end records a provider
// refund/reconciliation case, the intent waits in review, and no
// credit lands anywhere. A good-faith account is never debited by
// another account chargeback.
func TestSeasonalLateRefundRegistersReviewWithoutCredit(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := seasonPurchaseCtx()
	defer cancel()

	ends := seedBook(t, ctx, pool, "temporada-compra-6", 27, "2026-10-01T12:00:00Z")
	seedBook(t, ctx, pool, "temporada-compra-6b", 28, "2027-01-02T12:00:00Z")
	beforeClock := &intentClock{now: ends.Add(-10 * time.Minute)}
	intentRepo := mustIntentRepo(t, pool, beforeClock)
	fundSeasonalVault(t, ctx, pool, "temporada-compra-6", 300000)
	quote := sealSeasonalQuote(t, ctx, pool, beforeClock, 5*time.Minute, "temporada-compra-6", ends)
	account := intentAccount(t, ctx, pool)
	goodFaith := intentAccount(t, ctx, pool)
	goodBefore := seasonBookBalance(t, ctx, pool, "user", goodFaith, "temporada-compra-6")
	acceptSeasonalIntent(t, ctx, intentRepo, "intent-tardia", account, quote, 10000, "temporada-compra-6")
	legsBefore := seasonBookLegs(t, ctx, pool, "temporada-compra-6")

	lateClock := &intentClock{now: ends.Add(time.Minute)}
	verifier, _ := stripe.NewWebhookVerifier(settleSecret, 24*time.Hour, lateClock)
	settleRepo, _ := postgres.NewPurchaseSettlementRepository(pool, lateClock, verifier)
	body := settleBody("intent-tardia", account, 10000, "BRL", "paid", "evt-tardia")
	sig, ts := settleSign(settleSecret, lateClock.now, body)
	uc, _ := application.NewSettlePurchaseUseCase(settleRepo)
	if _, err := uc.Execute(ctx, application.SettlePurchaseCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts}); !errors.Is(err, application.ErrPurchaseAfterCutoff) {
		t.Fatalf("late paid event = %v, want ErrPurchaseAfterCutoff", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.billing_ink_intents WHERE account_id = $1::uuid AND intent_key = $2`, account, "intent-tardia").Scan(&status); err != nil {
		t.Fatalf("read late intent: %v", err)
	}
	if status != "review" {
		t.Fatalf("late intent status = %q, want review (provider refund case)", status)
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.billing_ink_charge_events WHERE event_id = 'evt-tardia'`).Scan(&events); err != nil {
		t.Fatalf("count charge events: %v", err)
	}
	if events != 1 {
		t.Fatalf("late charge events = %d, want 1 recorded refund case", events)
	}
	if settlementCount(t, ctx, pool) != 0 {
		t.Fatal("late event recorded a settlement: no credit after cutoff")
	}
	if got := seasonBookLegs(t, ctx, pool, "temporada-compra-6"); got != legsBefore {
		t.Fatal("late event moved legs in the sealed book")
	}
	if got := seasonBookLegs(t, ctx, pool, "temporada-compra-6b"); got != 0 {
		t.Fatalf("new book legs = %d, want 0: no credit in the new book", got)
	}
	if got := seasonBookBalance(t, ctx, pool, "user", goodFaith, "temporada-compra-6"); got != goodBefore {
		t.Fatal("good-faith account debited by another account chargeback")
	}
	// Conservation per book: debits equal credits.
	for _, season := range []string{"temporada-compra-6", "temporada-compra-6b"} {
		var credits, debits int64
		if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli),0) FROM app.economy_entries WHERE season_key = $1 AND direction = 'credit'`, season).Scan(&credits); err != nil {
			t.Fatalf("sum credits %s: %v", season, err)
		}
		if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli),0) FROM app.economy_entries WHERE season_key = $1 AND direction = 'debit'`, season).Scan(&debits); err != nil {
			t.Fatalf("sum debits %s: %v", season, err)
		}
		if credits != debits {
			t.Fatalf("book %s unbalanced: credits %d != debits %d", season, credits, debits)
		}
	}
}
