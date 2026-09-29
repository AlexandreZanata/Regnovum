package pricing_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	billpostgres "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	billapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	pricepostgres "github.com/AlexandreZanata/Regnovum/internal/pricing/adapters/postgres"
	priceapp "github.com/AlexandreZanata/Regnovum/internal/pricing/application"
	pricedomain "github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// TestAdversarialStaleQuoteSuspendsSale kills the stale-quote
// mutant: an acceptance against a lapsed quotation refuses with
// nothing written anywhere.
func TestAdversarialStaleQuoteSuspendsSale(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := advCtx()
	defer cancel()

	clock := &advClock{now: advInstant().Add(3 * time.Second)}
	advFund(t, ctx, pool, 300000)
	quote := advQuote(t, ctx, pool, clock, time.Minute)
	account := advAccount(t, ctx, pool, "stale-")
	clock.now = advInstant().Add(10 * time.Minute)

	repo, err := billpostgres.NewPurchaseIntentRepository(pool, clock, advSchedule())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	uc, err := billapp.NewAcceptPurchaseUseCase(repo)
	if err != nil {
		t.Fatalf("use case: %v", err)
	}
	if _, err := uc.Execute(ctx, billapp.AcceptPurchaseCommand{
		IntentKey: "stale-1", AccountID: account, QuoteID: quote, FiatMinor: 10000,
	}); !errors.Is(err, billapp.ErrPurchaseQuoteExpired) {
		t.Fatalf("stale acceptance = %v, want ErrPurchaseQuoteExpired", err)
	}
	if got := advCount(t, ctx, pool, "app.billing_ink_intents"); got != 0 {
		t.Fatalf("stale quote recorded %d intents", got)
	}
}

// TestAdversarialFalseSourceSuspendsRound kills the false-source
// mutant: two honest fakes plus one plausible impostor collect fine,
// then the guarded median suspends the round instead of pricing the
// lie.
func TestAdversarialFalseSourceSuspendsRound(t *testing.T) {
	clock := &advClock{now: advInstant()}
	serve := func(price int64) *httptest.Server {
		var hits atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			_, _ = fmt.Fprintf(w, `{"price_minor":%d,"observed_at":%q}`, price, advInstant().Format("2006-01-02T15:04:05Z07:00"))
		}))
		t.Cleanup(server.Close)
		return server
	}
	registry := priceapp.NewRegistry()
	prices := []int64{35000000, 35010000, 350000000}
	for i, price := range prices {
		id := fmt.Sprintf("fonte-%d", i+1)
		if err := registry.Register(advHTTPSource(t, id, serve(price).URL, clock, 3)); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	ctx, cancel := advCtx()
	defer cancel()
	sightings, failures := registry.Collect(ctx, advInstant(), advLimits())
	if len(failures) != 0 || len(sightings) != 3 {
		t.Fatalf("collection = %d sightings %+v, want 3 clean", len(sightings), failures)
	}
	if _, err := pricedomain.MedianPrice(sightings, advPolicy()); !errors.Is(err, pricedomain.ErrDivergentSources) {
		t.Fatalf("MedianPrice = %v, want ErrDivergentSources", err)
	}
}

// TestAdversarialRoundingUpDetected kills the rounding-up mutant: a
// single milli added to displayed or sealed terms breaks the seal.
func TestAdversarialRoundingUpDetected(t *testing.T) {
	ctx, cancel := advCtx()
	defer cancel()

	document, err := priceapp.NewQuotePurchaseUseCase().Execute(ctx, priceapp.PurchaseTermsCommand{
		FiatMinor: 10000,
		Sightings: []priceapp.SightingInput{
			{Source: "fonte-1", PriceMinor: 35000000, ObservedAt: advInstant(), Payload: []byte(`{}`)},
			{Source: "fonte-2", PriceMinor: 35010000, ObservedAt: advInstant(), Payload: []byte(`{}`)},
			{Source: "fonte-3", PriceMinor: 34990000, ObservedAt: advInstant(), Payload: []byte(`{}`)},
		},
		MinSources: 3, MaxSpread: 100000, Schedule: advSchedule(), Locale: "pt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	mutated := document.Terms
	mutated.InkMilli++
	if err := mutated.VerifyTermsHash(); !errors.Is(err, pricedomain.ErrInvalidTerms) {
		t.Fatalf("rounded-up terms = %v, want ErrInvalidTerms", err)
	}
	price, err := pricedomain.NewPriceMinor(35000000)
	if err != nil {
		t.Fatalf("NewPriceMinor: %v", err)
	}
	quote, err := pricedomain.AcceptQuote(price, []pricedomain.Observation{
		mustAdvObservation(t, "fonte-1", 35000000),
	}, advInstant(), 5*time.Minute, advLimits())
	if err != nil {
		t.Fatalf("AcceptQuote: %v", err)
	}
	quote.Price = 35000001
	if err := quote.VerifyHash(); !errors.Is(err, pricedomain.ErrInvalidQuote) {
		t.Fatalf("rounded-up quote = %v, want ErrInvalidQuote", err)
	}
}

func mustAdvObservation(t *testing.T, id string, price int64) pricedomain.Observation {
	t.Helper()
	seen, err := pricedomain.NewObservation(advSourceID(t, id), price, advInstant(), []byte(`{}`))
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	return seen
}

// TestAdversarialSaleWithoutStockSuspends kills the stockless-sale
// mutant: no cover means no intent, no hold and no legs.
func TestAdversarialSaleWithoutStockSuspends(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := advCtx()
	defer cancel()

	clock := &advClock{now: advInstant().Add(3 * time.Second)}
	quote := advQuote(t, ctx, pool, clock, 5*time.Minute)
	account := advAccount(t, ctx, pool, "nostock-")
	repo, err := billpostgres.NewPurchaseIntentRepository(pool, clock, advSchedule())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	uc, err := billapp.NewAcceptPurchaseUseCase(repo)
	if err != nil {
		t.Fatalf("use case: %v", err)
	}
	if _, err := uc.Execute(ctx, billapp.AcceptPurchaseCommand{
		IntentKey: "nostock-1", AccountID: account, QuoteID: quote, FiatMinor: 10000,
	}); !errors.Is(err, billapp.ErrInsufficientCommercialStock) {
		t.Fatalf("stockless sale = %v, want ErrInsufficientCommercialStock", err)
	}
	if got := advCount(t, ctx, pool, "app.billing_ink_intents"); got != 0 {
		t.Fatalf("stockless sale recorded %d intents", got)
	}
	if got := advCount(t, ctx, pool, "app.economy_entries"); got != 0 {
		t.Fatalf("stockless sale wrote %d legs", got)
	}
}

// TestAdversarialWebhookReplayGrantsNothing kills the replay mutant
// end to end: redelivery and a second event resolve without moving
// a single extra milli.
func TestAdversarialWebhookReplayGrantsNothing(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := advCtx()
	defer cancel()

	clock := &advClock{now: advInstant().Add(3 * time.Second)}
	advFund(t, ctx, pool, 300000)
	quote := advQuote(t, ctx, pool, clock, 5*time.Minute)
	account := advAccount(t, ctx, pool, "replay-")
	intentRepo, err := billpostgres.NewPurchaseIntentRepository(pool, clock, advSchedule())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	acceptUC, err := billapp.NewAcceptPurchaseUseCase(intentRepo)
	if err != nil {
		t.Fatalf("use case: %v", err)
	}
	if _, err := acceptUC.Execute(ctx, billapp.AcceptPurchaseCommand{
		IntentKey: "replay-1", AccountID: account, QuoteID: quote, FiatMinor: 10000,
	}); err != nil {
		t.Fatalf("AcceptPurchase: %v", err)
	}
	paid := advEventBody("replay-1", account, 10000, "BRL", "paid", "evt-1")
	first, err := advSettle(t, ctx, pool, clock, paid, clock.now, settleSecretForTest())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	legs := advCount(t, ctx, pool, "app.economy_entries")
	second, err := advSettle(t, ctx, pool, clock, paid, clock.now, settleSecretForTest())
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if !second.Replayed || second.SettlementID != first.SettlementID {
		t.Fatalf("redelivery paid again: %+v vs %+v", second, first)
	}
	other, err := advSettle(t, ctx, pool, clock,
		advEventBody("replay-1", account, 10000, "BRL", "paid", "evt-2"), clock.now, settleSecretForTest())
	if err != nil {
		t.Fatalf("second event: %v", err)
	}
	if !other.Replayed {
		t.Fatalf("second event paid again: %+v", other)
	}
	if got := advBalance(t, ctx, pool, "user", account); got != 253833 {
		t.Fatalf("buyer = %d, want 253833 once", got)
	}
	if got := advCount(t, ctx, pool, "app.economy_entries"); got != legs {
		t.Fatalf("replays wrote legs")
	}
}

func settleSecretForTest() string { return "whsec_adversarial_test" }

// TestAdversarialRedirectNeverFollowed kills the SSRF mutant: a
// source answering elsewhere is answered with refusal, and the
// inside server sees zero hits.
func TestAdversarialRedirectNeverFollowed(t *testing.T) {
	var inside atomic.Int64
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		inside.Add(1)
		_, _ = w.Write([]byte(`{"price_minor":1,"observed_at":"2026-10-02T12:00:00Z"}`))
	}))
	t.Cleanup(inner.Close)
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("Location", inner.URL)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(outer.Close)

	clock := &advClock{now: advInstant()}
	source := advHTTPSource(t, "fonte-redirect", outer.URL, clock, 3)
	ctx, cancel := advCtx()
	defer cancel()
	if _, err := source.Fetch(ctx); err == nil {
		t.Fatalf("redirect followed")
	}
	if got := inside.Load(); got != 0 {
		t.Fatalf("inside hits = %d, want 0", got)
	}
}

// TestAdversarialBurstStopsAtBreaker kills the rate mutant: a
// failing source under a burst stops being dialled after its budget.
func TestAdversarialBurstStopsAtBreaker(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	clock := &advClock{now: advInstant()}
	registry := priceapp.NewRegistry()
	if err := registry.Register(advHTTPSource(t, "fonte-burst", server.URL, clock, 2)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx, cancel := advCtx()
	defer cancel()
	for range 6 {
		_, _ = registry.Collect(ctx, advInstant(), advLimits())
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("hits = %d, want 2: the burst must stop at the breaker", got)
	}
}

// TestAdversarialCollectorSeesNoCache kills the cache-poisoning
// mutant: a source that moves its price is seen moving, and sealed
// terms never follow it.
func TestAdversarialCollectorSeesNoCache(t *testing.T) {
	price := atomic.Int64{}
	price.Store(35000000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"price_minor":%d,"observed_at":%q}`, price.Load(), advInstant().Format("2006-01-02T15:04:05Z07:00"))
	}))
	t.Cleanup(server.Close)

	clock := &advClock{now: advInstant()}
	registry := priceapp.NewRegistry()
	if err := registry.Register(advHTTPSource(t, "fonte-movel", server.URL, clock, 3)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx, cancel := advCtx()
	defer cancel()
	first, failures := registry.Collect(ctx, advInstant(), advLimits())
	if len(failures) != 0 || len(first) != 1 || first[0].Price.Int64() != 35000000 {
		t.Fatalf("first collection changed: %+v %+v", first, failures)
	}
	price.Store(99900000)
	second, failures := registry.Collect(ctx, advInstant(), advLimits())
	if len(failures) != 0 || len(second) != 1 || second[0].Price.Int64() != 99900000 {
		t.Fatalf("moved source not seen moving: %+v %+v", second, failures)
	}
	sealed, err := pricedomain.AcceptQuote(mustAdvPrice(t, 35000000), first, advInstant(), 5*time.Minute, advLimits())
	if err != nil {
		t.Fatalf("AcceptQuote: %v", err)
	}
	if sealed.Price.Int64() != 35000000 {
		t.Fatalf("sealed terms followed the move: %+v", sealed)
	}
}

func mustAdvPrice(t *testing.T, millis int64) pricedomain.PriceMinor {
	t.Helper()
	price, err := pricedomain.NewPriceMinor(millis)
	if err != nil {
		t.Fatalf("NewPriceMinor: %v", err)
	}
	return price
}

// TestAdversarialDisplayedMatchesPersisted kills the price-mismatch
// mutant: the buyer document and the stored intent carry one hash.
func TestAdversarialDisplayedMatchesPersisted(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := advCtx()
	defer cancel()

	clock := &advClock{now: advInstant().Add(3 * time.Second)}
	advFund(t, ctx, pool, 300000)
	sightings := []priceapp.SightingInput{
		{Source: "fonte-1", PriceMinor: 35000000, ObservedAt: advInstant(), Payload: []byte(`{}`)},
		{Source: "fonte-2", PriceMinor: 35010000, ObservedAt: advInstant(), Payload: []byte(`{}`)},
		{Source: "fonte-3", PriceMinor: 34990000, ObservedAt: advInstant(), Payload: []byte(`{}`)},
	}
	document, err := priceapp.NewQuotePurchaseUseCase().Execute(ctx, priceapp.PurchaseTermsCommand{
		FiatMinor: 10000, Sightings: sightings,
		MinSources: 3, MaxSpread: 100000, Schedule: advSchedule(), Locale: "pt",
	})
	if err != nil {
		t.Fatalf("QuotePurchase: %v", err)
	}
	median, err := pricedomain.MedianPrice(mustAdvSightings(t), advPolicy())
	if err != nil {
		t.Fatalf("MedianPrice: %v", err)
	}
	quote := advAcceptAt(t, ctx, pool, clock, median.Int64())
	account := advAccount(t, ctx, pool, "mismatch-")
	intentRepo, err := billpostgres.NewPurchaseIntentRepository(pool, clock, advSchedule())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	acceptUC, err := billapp.NewAcceptPurchaseUseCase(intentRepo)
	if err != nil {
		t.Fatalf("use case: %v", err)
	}
	accepted, err := acceptUC.Execute(ctx, billapp.AcceptPurchaseCommand{
		IntentKey: "mismatch-1", AccountID: account, QuoteID: quote, FiatMinor: 10000,
	})
	if err != nil {
		t.Fatalf("AcceptPurchase: %v", err)
	}
	var stored string
	if err := pool.QueryRow(ctx,
		`SELECT terms_hash FROM app.billing_ink_intents WHERE id::text = $1`, accepted.IntentID).Scan(&stored); err != nil {
		t.Fatalf("read intent terms: %v", err)
	}
	if stored != document.Terms.Hash {
		t.Fatalf("displayed hash %q differs from persisted %q", document.Terms.Hash, stored)
	}
	if accepted.InkMilli != document.Terms.InkMilli {
		t.Fatalf("persisted ink %d differs from displayed %d", accepted.InkMilli, document.Terms.InkMilli)
	}
}

// advSettleForged delivers one event signed with the wrong secret
// against the real verifier: the forgery every hostile test needs.
func advSettleForged(t *testing.T, ctx context.Context, pool *pgxpool.Pool, clock *advClock, body []byte) (*billapp.SettlePurchaseResult, error) {
	t.Helper()
	verifier, err := stripe.NewWebhookVerifier(settleSecretForTest(), 5*time.Minute, clock)
	if err != nil {
		t.Fatalf("NewWebhookVerifier: %v", err)
	}
	repo, err := billpostgres.NewPurchaseSettlementRepository(pool, clock, verifier)
	if err != nil {
		t.Fatalf("NewPurchaseSettlementRepository: %v", err)
	}
	uc, err := billapp.NewSettlePurchaseUseCase(repo)
	if err != nil {
		t.Fatalf("NewSettlePurchaseUseCase: %v", err)
	}
	sig, ts := advSign("whsec_wrong", clock.now, body)
	return uc.Execute(ctx, billapp.SettlePurchaseCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts})
}

func mustAdvSightings(t *testing.T) []pricedomain.Observation {
	t.Helper()
	seen := make([]pricedomain.Observation, 0, 3)
	for _, item := range [][2]any{{"fonte-1", 35000000}, {"fonte-2", 35010000}, {"fonte-3", 34990000}} {
		observation, err := pricedomain.NewObservation(
			advSourceID(t, item[0].(string)), int64(item[1].(int)), advInstant(), []byte(`{}`))
		if err != nil {
			t.Fatalf("NewObservation: %v", err)
		}
		seen = append(seen, observation)
	}
	return seen
}

// TestAdversarialClockNeverExtends kills the clock mutant twice: a
// late webhook still settles the intent accepted in time, while a
// future-dated event refuses.
func TestAdversarialClockNeverExtends(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := advCtx()
	defer cancel()

	clock := &advClock{now: advInstant().Add(3 * time.Second)}
	advFund(t, ctx, pool, 300000)
	quote := advQuote(t, ctx, pool, clock, 5*time.Minute)
	account := advAccount(t, ctx, pool, "clock-")
	intentRepo, err := billpostgres.NewPurchaseIntentRepository(pool, clock, advSchedule())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	acceptUC, err := billapp.NewAcceptPurchaseUseCase(intentRepo)
	if err != nil {
		t.Fatalf("use case: %v", err)
	}
	if _, err := acceptUC.Execute(ctx, billapp.AcceptPurchaseCommand{
		IntentKey: "clock-1", AccountID: account, QuoteID: quote, FiatMinor: 10000,
	}); err != nil {
		t.Fatalf("AcceptPurchase: %v", err)
	}
	clock.now = advInstant().Add(10 * time.Minute)
	late, err := advSettle(t, ctx, pool, clock,
		advEventBody("clock-1", account, 10000, "BRL", "paid", "evt-late"), clock.now, settleSecretForTest())
	if err != nil {
		t.Fatalf("late webhook refused an accepted intent: %v", err)
	}
	if late.InkMilli != 253833 {
		t.Fatalf("late liquidation changed: %+v", late)
	}
	clock.now = advInstant().Add(3 * time.Second)
	future, err := advSettle(t, ctx, pool, clock,
		advEventBody("clock-1", account, 10000, "BRL", "paid", "evt-future"), advInstant().Add(time.Hour), settleSecretForTest())
	if err == nil {
		t.Fatalf("future-dated event settled: %+v", future)
	}
}

// TestAdversarialFraudFindsNoPurchase kills the fraud mutant twice:
// an inflated ticket and somebody else's account both refuse without
// effect.
func TestAdversarialFraudFindsNoPurchase(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := advCtx()
	defer cancel()

	clock := &advClock{now: advInstant().Add(3 * time.Second)}
	advFund(t, ctx, pool, 300000)
	quote := advQuote(t, ctx, pool, clock, 5*time.Minute)
	victim := advAccount(t, ctx, pool, "fraud-victim-")
	intruder := advAccount(t, ctx, pool, "fraud-intruder-")
	intentRepo, err := billpostgres.NewPurchaseIntentRepository(pool, clock, advSchedule())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	acceptUC, err := billapp.NewAcceptPurchaseUseCase(intentRepo)
	if err != nil {
		t.Fatalf("use case: %v", err)
	}
	if _, err := acceptUC.Execute(ctx, billapp.AcceptPurchaseCommand{
		IntentKey: "fraud-1", AccountID: victim, QuoteID: quote, FiatMinor: 10000,
	}); err != nil {
		t.Fatalf("AcceptPurchase: %v", err)
	}
	inflated, err := advSettle(t, ctx, pool, clock,
		advEventBody("fraud-1", victim, 20000, "BRL", "paid", "evt-fraud"), clock.now, settleSecretForTest())
	if !errors.Is(err, billapp.ErrPurchaseSettlementMismatch) {
		t.Fatalf("inflated ticket = %+v, %v; want ErrPurchaseSettlementMismatch", inflated, err)
	}
	seized, err := advSettle(t, ctx, pool, clock,
		advEventBody("fraud-1", intruder, 10000, "BRL", "paid", "evt-seize"), clock.now, settleSecretForTest())
	if !errors.Is(err, billapp.ErrPurchaseIntentNotFound) {
		t.Fatalf("seized intent = %+v, %v; want ErrPurchaseIntentNotFound", seized, err)
	}
	if got := advCount(t, ctx, pool, "app.billing_ink_settlements"); got != 0 {
		t.Fatalf("fraud recorded %d settlements", got)
	}
	if got := advBalance(t, ctx, pool, "user", victim); got != 0 {
		t.Fatalf("victim moved on fraud: %d", got)
	}
}

// TestAdversarialErrorsCarryNoPII kills the disclosure mutant: every
// error a hostile flow returns, plus the buyer documents, carries no
// person-bound string.
func TestAdversarialErrorsCarryNoPII(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := advCtx()
	defer cancel()

	clock := &advClock{now: advInstant().Add(3 * time.Second)}
	advFund(t, ctx, pool, 300000)
	quote := advQuote(t, ctx, pool, clock, 5*time.Minute)
	canary := "canary-" + "0123456789abcdef0123456789abcdef"
	account := advAccount(t, ctx, pool, canary+"-")
	intentRepo, err := billpostgres.NewPurchaseIntentRepository(pool, clock, advSchedule())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	acceptUC, err := billapp.NewAcceptPurchaseUseCase(intentRepo)
	if err != nil {
		t.Fatalf("use case: %v", err)
	}
	if _, err := acceptUC.Execute(ctx, billapp.AcceptPurchaseCommand{
		IntentKey: "pii-1", AccountID: account, QuoteID: quote, FiatMinor: 10000,
	}); err != nil {
		t.Fatalf("AcceptPurchase: %v", err)
	}
	leaks := []string{}
	forged, err := advSettleForged(t, ctx, pool, clock,
		advEventBody("pii-1", account, 10000, "BRL", "paid", "evt-pii"))
	if err != nil {
		leaks = append(leaks, err.Error())
	} else if forged != nil {
		t.Fatalf("forged delivery settled: %+v", forged)
	}
	if _, err := advSettle(t, ctx, pool, clock,
		advEventBody("pii-1", account, 1, "BRL", "paid", "evt-pii2"), clock.now, settleSecretForTest()); err != nil {
		leaks = append(leaks, err.Error())
	} else {
		t.Fatalf("mismatched delivery settled")
	}
	document, err := priceapp.NewQuotePurchaseUseCase().Execute(ctx, priceapp.PurchaseTermsCommand{
		FiatMinor: 10000,
		Sightings: []priceapp.SightingInput{
			{Source: "fonte-1", PriceMinor: 35000000, ObservedAt: advInstant(), Payload: []byte(`{}`)},
		},
		MinSources: 1, MaxSpread: 0, Schedule: advSchedule(), Locale: "pt",
	})
	if err != nil {
		t.Fatalf("QuotePurchase: %v", err)
	}
	leaks = append(leaks, document.Titles.Title, document.Locale)
	for _, leak := range leaks {
		if strings.Contains(leak, canary) {
			t.Fatalf("person-bound string reached an error or document: %q", leak)
		}
		if strings.Contains(strings.ToLower(leak), "@invalid.example") {
			t.Fatalf("account email reached an error or document: %q", leak)
		}
	}
}

func advAcceptAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, clock *advClock, price int64) string {
	t.Helper()
	repo := pricepostgres.NewRepository(pool)
	uc, err := priceapp.NewAcceptQuoteUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewAcceptQuoteUseCase: %v", err)
	}
	quote, err := uc.Execute(ctx, priceapp.AcceptQuoteCommand{
		PriceMinor: price,
		Sightings: []priceapp.SightingInput{
			{Source: "fonte-1", PriceMinor: 35000000, ObservedAt: advInstant(), Payload: []byte(`{}`)},
			{Source: "fonte-2", PriceMinor: 35010000, ObservedAt: advInstant(), Payload: []byte(`{}`)},
			{Source: "fonte-3", PriceMinor: 34990000, ObservedAt: advInstant(), Payload: []byte(`{}`)},
		},
		TTL: 5 * time.Minute, MaxSkew: time.Minute, MaxAge: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("AcceptQuote: %v", err)
	}
	return quote.ID
}
