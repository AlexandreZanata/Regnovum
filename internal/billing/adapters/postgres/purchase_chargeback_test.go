package postgres_test

// P35-T08 — disputed liquidations reverse with linked compensation
// on real PostgreSQL.
//
// One authenticated dispute revokes the holder balance up to the INK
// due and covers the remainder from operating cash in a single
// transaction: the defrauding holder answers, the operator covers,
// and good-faith third parties keep every unit. The tests prove on a
// disposable database: full revocation, prior spending with treasury
// cover, partial disputes, duplicate redeliveries, suspended
// accounts, short treasuries refusing without minting or negatives,
// empty holders covered whole, nothing to reverse, and racers
// collapsing to one chargeback.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// chargebackWorld wires a liquidated intent with the dispute port:
// the buyer holds the INK, the verifier signs test deliveries.
type chargebackWorld struct {
	repo    *postgres.ChargebackRepository
	settle  *settleWorld
	clock   *intentClock
	account string
}

func newChargebackWorld(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *chargebackWorld {
	t.Helper()
	world := newSettleWorld(t, ctx, pool)
	if _, err := settleDeliver(t, ctx, world,
		settleBody(world.key, world.account, world.fiat, "BRL", "paid", "evt-1"), world.clock.now, settleSecret); err != nil {
		t.Fatalf("settle: %v", err)
	}
	verifier, err := stripe.NewWebhookVerifier(settleSecret, 5*time.Minute, world.clock)
	if err != nil {
		t.Fatalf("NewWebhookVerifier: %v", err)
	}
	repo, err := postgres.NewChargebackRepository(pool, world.clock, verifier)
	if err != nil {
		t.Fatalf("NewChargebackRepository: %v", err)
	}
	return &chargebackWorld{repo: repo, settle: world, clock: world.clock, account: world.account}
}

func disputeDeliver(t *testing.T, ctx context.Context, world *chargebackWorld, body []byte, secret string) (*application.SettleChargebackResult, error) {
	t.Helper()
	sig, ts := settleSign(secret, world.clock.now, body)
	uc, err := application.NewSettleChargebackUseCase(world.repo)
	if err != nil {
		t.Fatalf("NewSettleChargebackUseCase: %v", err)
	}
	return uc.Execute(ctx, application.SettleChargebackCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts})
}

func disputeBody(world *chargebackWorld, amount int64, currency, event string) []byte {
	return settleBody(world.settle.key, world.account, amount, currency, "disputed", event)
}

func chargebackCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.billing_ink_chargebacks`).Scan(&count); err != nil {
		t.Fatalf("count chargebacks: %v", err)
	}
	return count
}

// fundOperating opens the operator cash vault with a balanced funding
// pair, so it holds exactly millis with zero net supply effect.
func fundOperating(t *testing.T, ctx context.Context, pool *pgxpool.Pool, millis int64) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', 'operating_cash') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("open operating cash: %v", err)
	}
	var home, vault string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main'`).Scan(&home); err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'operating_cash'`).Scan(&vault); err != nil {
		t.Fatalf("resolve operating: %v", err)
	}
	var transfer string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $3), ($1::uuid, $4::uuid, 'credit', $3)`,
		transfer, home, millis, vault); err != nil {
		t.Fatalf("fund operating: %v", err)
	}
}

// moveToStranger transfers holder INK to a good-faith third party,
// returning the stranger balance for the untouched proof.
func moveToStranger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, holder string, millis int64) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', 'stranger-x') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("open stranger: %v", err)
	}
	var from, to, transfer string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'user' AND label = $1`, holder).Scan(&from); err != nil {
		t.Fatalf("resolve holder: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'user' AND label = 'stranger-x'`).Scan(&to); err != nil {
		t.Fatalf("resolve stranger: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $3), ($1::uuid, $4::uuid, 'credit', $3)`,
		transfer, from, millis, to); err != nil {
		t.Fatalf("move to stranger: %v", err)
	}
}

func journalSupply(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var credits, debits int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'credit'`).Scan(&credits); err != nil {
		t.Fatalf("sum credits: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'debit'`).Scan(&debits); err != nil {
		t.Fatalf("sum debits: %v", err)
	}
	return credits - debits
}

// TestChargebackRevokesInFull proves the happy path: the holder
// answers the whole due back to commercial stock with no operator
// cover and no one else touched.
func TestChargebackRevokesInFull(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newChargebackWorld(t, ctx, pool)
	before := journalSupply(t, ctx, pool)
	result, err := disputeDeliver(t, ctx, world, disputeBody(world, 10000, "BRL", "dp-1"), settleSecret)
	if err != nil {
		t.Fatalf("SettleChargeback: %v", err)
	}
	if result.Replayed || result.InkRevoked != 253833 || result.TreasuryCover != 0 {
		t.Fatalf("result = %+v, want full revocation without cover", result)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != 0 {
		t.Fatalf("holder = %d, want 0", got)
	}
	if got := vaultBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 300000 {
		t.Fatalf("commercial = %d, want every unit back", got)
	}
	if got := journalSupply(t, ctx, pool); got != before {
		t.Fatalf("supply moved: nothing minted, nothing burned")
	}
	var revoked, covered int64
	if err := pool.QueryRow(ctx,
		`SELECT ink_revoked, treasury_covered FROM app.billing_ink_chargebacks WHERE id::text = $1`,
		result.ChargebackID).Scan(&revoked, &covered); err != nil {
		t.Fatalf("read chargeback: %v", err)
	}
	if revoked != 253833 || covered != 0 {
		t.Fatalf("chargeback row changed: %d/%d", revoked, covered)
	}
}

// TestChargebackCoversSpentDifference proves prior spending: the
// holder answers what remains, the operator covers the spent
// difference at once, and the stranger keeps every unit.
func TestChargebackCoversSpentDifference(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newChargebackWorld(t, ctx, pool)
	fundOperating(t, ctx, pool, 300000)
	moveToStranger(t, ctx, pool, world.account, 200000)
	result, err := disputeDeliver(t, ctx, world, disputeBody(world, 10000, "BRL", "dp-1"), settleSecret)
	if err != nil {
		t.Fatalf("SettleChargeback: %v", err)
	}
	if result.InkRevoked != 53833 || result.TreasuryCover != 200000 {
		t.Fatalf("result = %+v, want 53833 revoked with 200000 covered", result)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != 0 {
		t.Fatalf("holder = %d, want 0 without going negative", got)
	}
	if got := vaultBalance(t, ctx, pool, "user", "stranger-x"); got != 200000 {
		t.Fatalf("stranger = %d, want 200000 untouched", got)
	}
	if got := vaultBalance(t, ctx, pool, "treasury", "operating_cash"); got != 100000 {
		t.Fatalf("operating = %d, want the immediate cover", got)
	}
	if got := vaultBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 300000 {
		t.Fatalf("commercial = %d, want every unit back", got)
	}
}

// TestChargebackSettlesPartialDispute proves a half ticket revokes
// the floored half share with no cover.
func TestChargebackSettlesPartialDispute(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newChargebackWorld(t, ctx, pool)
	result, err := disputeDeliver(t, ctx, world, disputeBody(world, 5000, "BRL", "dp-1"), settleSecret)
	if err != nil {
		t.Fatalf("SettleChargeback: %v", err)
	}
	if result.InkRevoked != 126916 || result.TreasuryCover != 0 {
		t.Fatalf("result = %+v, want floored 126916 without cover", result)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != 253833-126916 {
		t.Fatalf("holder kept %d, want the remainder", got)
	}
}

// TestChargebackReplaysDuplicates proves one liquidation carries one
// dispute: the redelivery resolves without revoking twice, and a
// second dispute conflicts instead of merging silently.
func TestChargebackReplaysDuplicates(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newChargebackWorld(t, ctx, pool)
	first, err := disputeDeliver(t, ctx, world, disputeBody(world, 10000, "BRL", "dp-1"), settleSecret)
	if err != nil {
		t.Fatalf("dispute: %v", err)
	}
	second, err := disputeDeliver(t, ctx, world, disputeBody(world, 10000, "BRL", "dp-1"), settleSecret)
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if !second.Replayed || second.ChargebackID != first.ChargebackID {
		t.Fatalf("redelivery revoked again: %+v vs %+v", second, first)
	}
	other, err := disputeDeliver(t, ctx, world, disputeBody(world, 10000, "BRL", "dp-2"), settleSecret)
	if !errors.Is(err, application.ErrChargebackConflict) {
		t.Fatalf("second dispute = %+v, %v; want ErrChargebackConflict", other, err)
	}
	if chargebackCount(t, ctx, pool) != 1 {
		t.Fatalf("duplicate disputes recorded twice")
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != 0 {
		t.Fatalf("holder moved on replay")
	}
}

// TestChargebackAnswersSuspendedAccounts proves suspension never
// shields a legitimate debt: the suspended holder still answers.
func TestChargebackAnswersSuspendedAccounts(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newChargebackWorld(t, ctx, pool)
	if _, err := pool.Exec(ctx,
		`UPDATE app.accounts SET status = 'suspended' WHERE id::text = $1`, world.account); err != nil {
		t.Fatalf("suspend account: %v", err)
	}
	result, err := disputeDeliver(t, ctx, world, disputeBody(world, 10000, "BRL", "dp-1"), settleSecret)
	if err != nil {
		t.Fatalf("SettleChargeback: %v", err)
	}
	if result.InkRevoked != 253833 {
		t.Fatalf("result = %+v, want full revocation from the suspended holder", result)
	}
}

// TestChargebackRefusesShortTreasury proves short treasuries refuse
// instead of minting: an emptied holder with an uncovered operator
// writes nothing anywhere.
func TestChargebackRefusesShortTreasury(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newChargebackWorld(t, ctx, pool)
	fundOperating(t, ctx, pool, 10000)
	moveToStranger(t, ctx, pool, world.account, 253833)
	legs := journalLegs(t, ctx, pool)
	uc, err := application.NewSettleChargebackUseCase(world.repo)
	if err != nil {
		t.Fatalf("NewSettleChargebackUseCase: %v", err)
	}
	body := disputeBody(world, 10000, "BRL", "dp-1")
	sig, ts := settleSign(settleSecret, world.clock.now, body)
	if _, err := uc.Execute(ctx, application.SettleChargebackCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts}); !errors.Is(err, application.ErrInsufficientTreasuryFunds) {
		t.Fatalf("short treasury = %v, want ErrInsufficientTreasuryFunds", err)
	}
	if chargebackCount(t, ctx, pool) != 0 {
		t.Fatalf("refused dispute recorded a chargeback")
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != 0 {
		t.Fatalf("holder moved on refusal")
	}
	if got := vaultBalance(t, ctx, pool, "user", "stranger-x"); got != 253833 {
		t.Fatalf("stranger moved on refusal")
	}
	if got := journalLegs(t, ctx, pool); got != legs {
		t.Fatalf("legs moved without cover")
	}
}

// TestChargebackCoversEmptiedHolder proves an emptied holder never
// implies a negative: the operator covers the whole due with the
// holder resting at exactly zero.
func TestChargebackCoversEmptiedHolder(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newChargebackWorld(t, ctx, pool)
	fundOperating(t, ctx, pool, 300000)
	moveToStranger(t, ctx, pool, world.account, 253833)
	result, err := disputeDeliver(t, ctx, world, disputeBody(world, 10000, "BRL", "dp-1"), settleSecret)
	if err != nil {
		t.Fatalf("SettleChargeback: %v", err)
	}
	if result.InkRevoked != 0 || result.TreasuryCover != 253833 {
		t.Fatalf("result = %+v, want zero revoked with full cover", result)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != 0 {
		t.Fatalf("holder = %d, want exactly zero", got)
	}
	if got := vaultBalance(t, ctx, pool, "treasury", "operating_cash"); got != 300000-253833 {
		t.Fatalf("operating = %d, want the full cover", got)
	}
}

// TestChargebackNeedsLiquidation proves intents without liquidation
// have nothing to reverse: pending purchases refuse with nothing
// written.
func TestChargebackNeedsLiquidation(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	intentRepo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 300000)
	quote := sealQuote(t, ctx, pool, clock, 5*time.Minute)
	account := intentAccount(t, ctx, pool)
	uc, err := application.NewAcceptPurchaseUseCase(intentRepo)
	if err != nil {
		t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
	}
	if _, err := uc.Execute(ctx, acceptCmd("pending-1", account, quote, 10000)); err != nil {
		t.Fatalf("AcceptPurchase: %v", err)
	}
	verifier, err := stripe.NewWebhookVerifier(settleSecret, 5*time.Minute, clock)
	if err != nil {
		t.Fatalf("NewWebhookVerifier: %v", err)
	}
	repo, err := postgres.NewChargebackRepository(pool, clock, verifier)
	if err != nil {
		t.Fatalf("NewChargebackRepository: %v", err)
	}
	settleUC, err := application.NewSettleChargebackUseCase(repo)
	if err != nil {
		t.Fatalf("NewSettleChargebackUseCase: %v", err)
	}
	body := settleBody("pending-1", account, 10000, "BRL", "disputed", "dp-1")
	sig, ts := settleSign(settleSecret, clock.now, body)
	if _, err := settleUC.Execute(ctx, application.SettleChargebackCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts}); !errors.Is(err, application.ErrSettlementNotFound) {
		t.Fatalf("pending intent = %v, want ErrSettlementNotFound", err)
	}
	if chargebackCount(t, ctx, pool) != 0 {
		t.Fatalf("unliquidated intent recorded a chargeback")
	}
}

// TestChargebackRacesCollapseToOne proves eight simultaneous
// deliveries of one dispute revoke a single compensation: one
// executes, seven replay.
func TestChargebackRacesCollapseToOne(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newChargebackWorld(t, ctx, pool)
	body := disputeBody(world, 10000, "BRL", "dp-1")
	sig, ts := settleSign(settleSecret, world.clock.now, body)

	const runners = 8
	var wg sync.WaitGroup
	results := make([]*application.SettleChargebackResult, runners)
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uc, err := application.NewSettleChargebackUseCase(world.repo)
			if err != nil {
				errs[i] = err
				return
			}
			results[i], errs[i] = uc.Execute(ctx, application.SettleChargebackCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts})
		}(i)
	}
	wg.Wait()
	founded, replayed := 0, 0
	var chargebackID string
	for i := range runners {
		if errs[i] != nil {
			t.Fatalf("runner %d: %v", i, errs[i])
		}
		if results[i].Replayed {
			replayed++
		} else {
			founded++
		}
		if chargebackID == "" {
			chargebackID = results[i].ChargebackID
		} else if results[i].ChargebackID != chargebackID {
			t.Fatalf("runner %d revoked another compensation", i)
		}
	}
	if founded != 1 || replayed != runners-1 {
		t.Fatalf("founded = %d, replayed = %d; want 1 and %d", founded, replayed, runners-1)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != 0 {
		t.Fatalf("holder = %d, want revoked once", got)
	}
	if chargebackCount(t, ctx, pool) != 1 {
		t.Fatalf("race recorded twice")
	}
}
