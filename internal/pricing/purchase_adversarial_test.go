package pricing_test

// P35-T09 — adversarial purchase integrity on real PostgreSQL.
//
// Every guard the phase built is attacked end to end over one
// disposable database with local fakes: stale quotes, false
// sources, rounded-up terms, stockless sales, replayed webhooks,
// forged redirects, failure bursts, poisoned caches, mismatched
// prices, clock games, fraud and PII in errors. Each mutant dies:
// refusals write nothing, replays pay once, and no person-bound
// string reaches an error, a document or a route.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	billpostgres "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	billapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	pricepostgres "github.com/AlexandreZanata/Regnovum/internal/pricing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/pricing/adapters/remote"
	priceapp "github.com/AlexandreZanata/Regnovum/internal/pricing/application"
	pricedomain "github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

func advCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// advClock fixes every instant of the round: collection, acceptance
// and delivery share it, and advances never touch the wall clock.
type advClock struct {
	now time.Time
}

func (c *advClock) Now() time.Time { return c.now }

func advInstant() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

func advLimits() pricedomain.ObservationLimits {
	return pricedomain.ObservationLimits{MaxFutureSkew: time.Minute, MaxAge: 5 * time.Minute}
}

func advPolicy() pricedomain.MedianPolicy {
	return pricedomain.MedianPolicy{MinSources: 3, MaxSpread: 100000}
}

func advSchedule() pricedomain.FeeSchedule {
	return pricedomain.FeeSchedule{
		Version: 1, SatsPerInk: 100, SpreadBps: 100, FeeMinor: 30,
		TaxBps: 1000, MinFiatMinor: 100, StepMilliInk: 1, Rounding: pricedomain.RoundDown,
	}
}

func advAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tag string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ($1 || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`, tag).Scan(&id); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

// advFund opens the Treasury home with the commercial vault holding
// exactly millis through a balanced pair: zero net supply effect.
func advFund(t *testing.T, ctx context.Context, pool *pgxpool.Pool, millis int64) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', 'main'), ('treasury', 'commercial_stock') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("open vaults: %v", err)
	}
	var home, vault, transfer string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main'`).Scan(&home); err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'commercial_stock'`).Scan(&vault); err != nil {
		t.Fatalf("resolve vault: %v", err)
	}
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

func advQuote(t *testing.T, ctx context.Context, pool *pgxpool.Pool, clock *advClock, ttl time.Duration) string {
	t.Helper()
	repo := pricepostgres.NewRepository(pool)
	uc, err := priceapp.NewAcceptQuoteUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewAcceptQuoteUseCase: %v", err)
	}
	quote, err := uc.Execute(ctx, priceapp.AcceptQuoteCommand{
		PriceMinor: 35000000,
		Sightings: []priceapp.SightingInput{
			{Source: "fonte-1", PriceMinor: 35000000, ObservedAt: advInstant(), Payload: []byte(`{"a":1}`)},
			{Source: "fonte-2", PriceMinor: 35010000, ObservedAt: advInstant().Add(time.Second), Payload: []byte(`{"a":2}`)},
			{Source: "fonte-3", PriceMinor: 34990000, ObservedAt: advInstant().Add(2 * time.Second), Payload: []byte(`{"a":3}`)},
		},
		TTL: ttl, MaxSkew: time.Minute, MaxAge: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("AcceptQuote: %v", err)
	}
	return quote.ID
}

func advSign(secret string, at time.Time, body []byte) (sigHeader, tsHeader string) {
	tsHeader = strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsHeader + "." + string(body)))
	return fmt.Sprintf("t=%s,v1=%s", tsHeader, hex.EncodeToString(mac.Sum(nil))), tsHeader
}

func advSettle(t *testing.T, ctx context.Context, pool *pgxpool.Pool, clock *advClock, body []byte, at time.Time, secret string) (*billapp.SettlePurchaseResult, error) {
	t.Helper()
	verifier, err := stripe.NewWebhookVerifier(secret, 5*time.Minute, clock)
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
	sig, ts := advSign(secret, at, body)
	return uc.Execute(ctx, billapp.SettlePurchaseCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts})
}

func advEventBody(key, account string, amount int64, currency, status, event string) []byte {
	return []byte(fmt.Sprintf(
		`{"intent_key":%q,"account_id":%q,"amount_minor":%d,"currency":%q,"status":%q,"event_id":%q}`,
		key, account, amount, currency, status, event))
}

func advCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func advBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind, label string) int64 {
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

// advSourceID parses a test slug or fails the test.
func advSourceID(t *testing.T, raw string) pricedomain.SourceID {
	t.Helper()
	source, err := pricedomain.ParseSourceID(raw)
	if err != nil {
		t.Fatalf("ParseSourceID: %v", err)
	}
	return source
}

// advHTTPSource wires one fake-backed source with an explicit
// breaker for burst tests.
func advHTTPSource(t *testing.T, id, endpoint string, clock *advClock, maxFailures int) *remote.HTTPSource {
	t.Helper()
	breaker, err := remote.NewBreaker(maxFailures, time.Hour, clock)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	source, err := remote.NewHTTPSource(advSourceID(t, id), endpoint, 2*time.Second, breaker)
	if err != nil {
		t.Fatalf("NewHTTPSource: %v", err)
	}
	return source
}
