package postgres_test

// P35-T06 — paid provider events settle INK on real PostgreSQL.
//
// One authenticated delivery moves the committed INK to the buyer
// exactly once: amount, currency, intent and timeliness confer
// against the sealed intent and the signature headers. The tests
// prove on a disposable database: exact liquidation with captured
// hold and legs, forged/success-page/stale/divergent/unknown
// deliveries refusing without effect, duplicates and out-of-order
// events resolving without extra INK, a pre-commit crash retrying
// clean and eight racers collapsing to one.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func settleCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

const settleSecret = "whsec_settle_test_secret"

// settleWorld wires the real stack over one disposable database: a
// live quotation, an accepted intent with its hold and the real HMAC
// verifier behind the settlement port.
type settleWorld struct {
	repo    *postgres.PurchaseSettlementRepository
	clock   *intentClock
	key     string
	account string
	ink     int64
	fiat    int64
}

func newSettleWorld(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *settleWorld {
	t.Helper()
	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	intentRepo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 300000)
	quote := sealQuote(t, ctx, pool, clock, 5*time.Minute)
	account := intentAccount(t, ctx, pool)
	accepted := mustAccept(t, ctx, intentRepo, acceptCmd("settle-1", account, quote, 10000))
	if accepted.InkMilli != 253833 {
		t.Fatalf("intent ink = %d, want 253833", accepted.InkMilli)
	}
	verifier, err := stripe.NewWebhookVerifier(settleSecret, 5*time.Minute, clock)
	if err != nil {
		t.Fatalf("NewWebhookVerifier: %v", err)
	}
	repo, err := postgres.NewPurchaseSettlementRepository(pool, clock, verifier)
	if err != nil {
		t.Fatalf("NewPurchaseSettlementRepository: %v", err)
	}
	return &settleWorld{repo: repo, clock: clock, key: "settle-1", account: account, ink: 253833, fiat: 10000}
}

// settleSign signs one delivery exactly the way the provider does:
// HMAC-SHA256(secret, "timestamp.body") rendered as t=,v1=.
func settleSign(secret string, at time.Time, body []byte) (sigHeader, tsHeader string) {
	tsHeader = strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsHeader + "." + string(body)))
	return fmt.Sprintf("t=%s,v1=%s", tsHeader, hex.EncodeToString(mac.Sum(nil))), tsHeader
}

func settleBody(key, account string, amount int64, currency, status, event string) []byte {
	body, err := json.Marshal(map[string]any{
		"intent_key": key, "account_id": account, "amount_minor": amount,
		"currency": currency, "status": status, "event_id": event,
	})
	if err != nil {
		panic(err)
	}
	return body
}

func settleDeliver(t *testing.T, ctx context.Context, world *settleWorld, body []byte, at time.Time, secret string) (*application.SettlePurchaseResult, error) {
	t.Helper()
	sig, ts := settleSign(secret, at, body)
	uc, err := application.NewSettlePurchaseUseCase(world.repo)
	if err != nil {
		t.Fatalf("NewSettlePurchaseUseCase: %v", err)
	}
	return uc.Execute(ctx, application.SettlePurchaseCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts})
}

func settlementCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.billing_ink_settlements`).Scan(&count); err != nil {
		t.Fatalf("count settlements: %v", err)
	}
	return count
}

func holdStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, intentID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT h.status FROM app.economy_holds h
		 JOIN app.billing_ink_intents i ON i.hold_id = h.id WHERE i.id::text = $1`, intentID).Scan(&status); err != nil {
		t.Fatalf("read hold status: %v", err)
	}
	return status
}

// TestSettlePurchaseLiquidatesOnce proves the happy path: the paid
// event moves the committed INK to the buyer, captures the hold and
// records the settlement beside the intent.
func TestSettlePurchaseLiquidatesOnce(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newSettleWorld(t, ctx, pool)
	body := settleBody(world.key, world.account, world.fiat, "BRL", "paid", "evt-1")
	result, err := settleDeliver(t, ctx, world, body, world.clock.now, settleSecret)
	if err != nil {
		t.Fatalf("SettlePurchase: %v", err)
	}
	if result.Replayed || result.InkMilli != world.ink {
		t.Fatalf("result = %+v, want fresh with %d", result, world.ink)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != world.ink {
		t.Fatalf("buyer = %d, want %d", got, world.ink)
	}
	if got := holdStatus(t, ctx, pool, result.IntentID); got != "captured" {
		t.Fatalf("hold = %q, want captured", got)
	}
	var intent, event string
	var amount int64
	if err := pool.QueryRow(ctx,
		`SELECT intent_id::text, event_id, amount_minor FROM app.billing_ink_settlements WHERE id::text = $1`,
		result.SettlementID).Scan(&intent, &event, &amount); err != nil {
		t.Fatalf("read settlement: %v", err)
	}
	if intent != result.IntentID || event != "evt-1" || amount != world.fiat {
		t.Fatalf("settlement row changed: %s/%s/%d", intent, event, amount)
	}
}

// TestSettlePurchaseRefusesWithoutEffect proves hostile deliveries
// grant nothing: forged signatures, success pages, stale timestamps,
// divergent amounts and currencies, unknown intents and malformed
// bodies all refuse with the journal, holds and settlements
// untouched.
func TestSettlePurchaseRefusesWithoutEffect(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newSettleWorld(t, ctx, pool)
	now := world.clock.now
	legs := journalLegs(t, ctx, pool)
	cases := []struct {
		name   string
		body   []byte
		at     time.Time
		secret string
		want   error
	}{
		{"forged signature", settleBody(world.key, world.account, world.fiat, "BRL", "paid", "evt-forge"), now, "whsec_forged", application.ErrWebhookSignatureInvalid},
		{"success page", settleBody(world.key, world.account, world.fiat, "BRL", "success_page", "evt-page"), now, settleSecret, application.ErrPurchaseEventNotSettling},
		{"failed event", settleBody(world.key, world.account, world.fiat, "BRL", "failed", "evt-fail"), now, settleSecret, application.ErrPurchaseEventNotSettling},
		{"stale delivery", settleBody(world.key, world.account, world.fiat, "BRL", "paid", "evt-old"), now.Add(-time.Hour), settleSecret, application.ErrWebhookSignatureInvalid},
		{"divergent amount", settleBody(world.key, world.account, world.fiat+1, "BRL", "paid", "evt-amount"), now, settleSecret, application.ErrPurchaseSettlementMismatch},
		{"divergent currency", settleBody(world.key, world.account, world.fiat, "USD", "paid", "evt-currency"), now, settleSecret, application.ErrPurchaseSettlementMismatch},
		{"unknown intent", settleBody("fantasma", world.account, world.fiat, "BRL", "paid", "evt-ghost"), now, settleSecret, application.ErrPurchaseIntentNotFound},
		{"malformed body", []byte(`{"amount_minor":`), now, settleSecret, application.ErrWebhookPayloadMalformed},
		{"unknown field", []byte(`{"intent_key":"x","account_id":"y","amount_minor":1,"currency":"BRL","status":"paid","event_id":"z","extra":1}`), now, settleSecret, application.ErrWebhookPayloadMalformed},
	}
	for _, tc := range cases {
		sig, ts := settleSign(tc.secret, tc.at, tc.body)
		uc, err := application.NewSettlePurchaseUseCase(world.repo)
		if err != nil {
			t.Fatalf("NewSettlePurchaseUseCase: %v", err)
		}
		if _, err := uc.Execute(ctx, application.SettlePurchaseCommand{Payload: tc.body, SignatureHeader: sig, TimestampHeader: ts}); !errors.Is(err, tc.want) {
			t.Fatalf("%s = %v, want %v", tc.name, err, tc.want)
		}
	}
	if settlementCount(t, ctx, pool) != 0 {
		t.Fatalf("refused deliveries recorded settlements")
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != 0 {
		t.Fatalf("buyer = %d after refusals, want 0", got)
	}
	if got := journalLegs(t, ctx, pool); got != legs {
		t.Fatalf("legs moved on refused deliveries")
	}
}

// TestSettlePurchaseReplaysDuplicates proves the event settles once:
// the redelivery resolves the recorded settlement with zero new
// legs and zero new rows.
func TestSettlePurchaseReplaysDuplicates(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newSettleWorld(t, ctx, pool)
	body := settleBody(world.key, world.account, world.fiat, "BRL", "paid", "evt-1")
	first, err := settleDeliver(t, ctx, world, body, world.clock.now, settleSecret)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	legs := journalLegs(t, ctx, pool)
	second, err := settleDeliver(t, ctx, world, body, world.clock.now, settleSecret)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !second.Replayed || second.SettlementID != first.SettlementID {
		t.Fatalf("replay settled again: %+v vs %+v", second, first)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != world.ink {
		t.Fatalf("buyer = %d, want %d once", got, world.ink)
	}
	if settlementCount(t, ctx, pool) != 1 {
		t.Fatalf("duplicate recorded a second settlement")
	}
	if got := journalLegs(t, ctx, pool); got != legs {
		t.Fatalf("legs moved on replay")
	}
}

// TestSettlePurchaseOrdersEvents proves out-of-order delivery is
// safe: a failure first refuses without effect, the paid event
// settles, and a second paid event resolves without extra INK.
func TestSettlePurchaseOrdersEvents(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newSettleWorld(t, ctx, pool)
	now := world.clock.now
	if _, err := settleDeliver(t, ctx, world,
		settleBody(world.key, world.account, world.fiat, "BRL", "failed", "evt-fail"), now, settleSecret); !errors.Is(err, application.ErrPurchaseEventNotSettling) {
		t.Fatalf("failed first = %v, want ErrPurchaseEventNotSettling", err)
	}
	first, err := settleDeliver(t, ctx, world,
		settleBody(world.key, world.account, world.fiat, "BRL", "paid", "evt-1"), now, settleSecret)
	if err != nil {
		t.Fatalf("paid after failure: %v", err)
	}
	second, err := settleDeliver(t, ctx, world,
		settleBody(world.key, world.account, world.fiat, "BRL", "paid", "evt-2"), now, settleSecret)
	if err != nil {
		t.Fatalf("second paid: %v", err)
	}
	if !second.Replayed || second.SettlementID != first.SettlementID {
		t.Fatalf("out-of-order paid again: %+v vs %+v", second, first)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != world.ink {
		t.Fatalf("buyer = %d, want %d once", got, world.ink)
	}
	if settlementCount(t, ctx, pool) != 1 {
		t.Fatalf("two paid events recorded two settlements")
	}
}

// TestSettlePurchaseCrashRetriesClean proves a pre-commit crash
// settles nothing and the retry pays exactly once.
func TestSettlePurchaseCrashRetriesClean(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newSettleWorld(t, ctx, pool)
	body := settleBody(world.key, world.account, world.fiat, "BRL", "paid", "evt-1")
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	uc, err := application.NewSettlePurchaseUseCase(world.repo)
	if err != nil {
		t.Fatalf("NewSettlePurchaseUseCase: %v", err)
	}
	sig, ts := settleSign(settleSecret, world.clock.now, body)
	if _, err := uc.Execute(cancelled, application.SettlePurchaseCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts}); err == nil {
		t.Fatal("cancelled settlement succeeded")
	}
	if settlementCount(t, ctx, pool) != 0 {
		t.Fatalf("torn settlement recorded a row")
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != 0 {
		t.Fatalf("torn settlement paid %d", got)
	}
	result, err := settleDeliver(t, ctx, world, body, world.clock.now, settleSecret)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if result.Replayed || result.InkMilli != world.ink {
		t.Fatalf("retry = %+v, want fresh with %d", result, world.ink)
	}
	if settlementCount(t, ctx, pool) != 1 {
		t.Fatalf("retry settled twice")
	}
}

// TestSettlePurchaseRacesCollapseToOne proves eight simultaneous
// deliveries of one event settle a single liquidation: one executes,
// seven replay.
func TestSettlePurchaseRacesCollapseToOne(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newSettleWorld(t, ctx, pool)
	body := settleBody(world.key, world.account, world.fiat, "BRL", "paid", "evt-1")
	sig, ts := settleSign(settleSecret, world.clock.now, body)

	const runners = 8
	var wg sync.WaitGroup
	results := make([]*application.SettlePurchaseResult, runners)
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uc, err := application.NewSettlePurchaseUseCase(world.repo)
			if err != nil {
				errs[i] = err
				return
			}
			results[i], errs[i] = uc.Execute(ctx, application.SettlePurchaseCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts})
		}(i)
	}
	wg.Wait()
	founded, replayed := 0, 0
	var settlementID string
	for i := range runners {
		if errs[i] != nil {
			t.Fatalf("runner %d: %v", i, errs[i])
		}
		if results[i].Replayed {
			replayed++
		} else {
			founded++
		}
		if settlementID == "" {
			settlementID = results[i].SettlementID
		} else if results[i].SettlementID != settlementID {
			t.Fatalf("runner %d settled another liquidation", i)
		}
	}
	if founded != 1 || replayed != runners-1 {
		t.Fatalf("founded = %d, replayed = %d; want 1 and %d", founded, replayed, runners-1)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != world.ink {
		t.Fatalf("buyer = %d, want %d once", got, world.ink)
	}
	if settlementCount(t, ctx, pool) != 1 {
		t.Fatalf("race recorded twice")
	}
}
