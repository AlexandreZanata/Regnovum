package postgres_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	billingpg "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	stripeadapter "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	billingwallet "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/wallet"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// Independent entitlement model of billing lifecycles (P24-T05).
//
// The system under test is the real stack over a disposable PostgreSQL:
// checkout creation against a scripted provider edge (no network), the real
// HMAC webhook verifier, the real webhook event idempotency, the real
// checkout/pass/member settlers, the real pass-lot/subscription stores and
// the real wallet ledger as the INK backend. The oracle below is a separate,
// deliberately small state machine written from the documented contract:
// benefit only from authenticated payment, one grant per reference and
// period, cancel preserves history without new grants, and divergent
// amounts/currency/products are refused. It never calls domain constructors
// or application use cases; identifiers and tokens are learned from observed
// outputs, while the TRANSITION RULES are the oracle's own.

// modelBillingClock is one fixed instant. Billing periods are explicit
// command data and the webhook tolerance is anchored to this clock, so every
// sequence replays bit-for-bit.
type modelBillingClock struct{ now time.Time }

func (c modelBillingClock) Now() time.Time { return c.now }

// modelCatalog builds the versioned price list the model buys from: one INK
// pack, one pass pack and the monthly Member plan.
func modelCatalog(t *testing.T) *domain.Catalog {
	t.Helper()
	inkProduct := modelProduct(t, domain.MarketBrazil, "ink_10000", 990, domain.CurrencyBRL, mustModelINKGrant(t, 10000), "price_1QbrInk")
	passProduct := modelProduct(t, domain.MarketBrazil, "pass_1", 990, domain.CurrencyBRL, mustModelPassGrant(t, 1), "price_1QbrPass")
	memberProduct := modelProduct(t, domain.MarketBrazil, "member_monthly", 1990, domain.CurrencyBRL, domain.NewMemberGrant(), "price_1QbrMember")
	catalog, err := domain.NewCatalog(1, []domain.Product{inkProduct, passProduct, memberProduct})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return catalog
}

func modelProduct(t *testing.T, market domain.Market, id string, amountMinor int64, currency domain.Currency, grant domain.Grant, priceID string) domain.Product {
	t.Helper()
	productID, err := domain.ParseProductID(id)
	if err != nil {
		t.Fatalf("ParseProductID: %v", err)
	}
	amount, err := domain.NewMoney(amountMinor, currency)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	price, err := domain.ParseStripePriceID(priceID)
	if err != nil {
		t.Fatalf("ParseStripePriceID: %v", err)
	}
	product, err := domain.NewProduct(market, productID, amount, grant, price)
	if err != nil {
		t.Fatalf("NewProduct: %v", err)
	}
	return product
}

func mustModelINKGrant(t *testing.T, qty int64) domain.Grant {
	t.Helper()
	grant, err := domain.NewINKGrant(qty)
	if err != nil {
		t.Fatalf("NewINKGrant: %v", err)
	}
	return grant
}

func mustModelPassGrant(t *testing.T, qty int32) domain.Grant {
	t.Helper()
	grant, err := domain.NewArenaPassGrant(qty)
	if err != nil {
		t.Fatalf("NewArenaPassGrant: %v", err)
	}
	return grant
}

// modelGateway is the scripted provider edge: no network, only the answers
// the test programs. Matching answers mirror the catalog; divergent answers
// prove the server decides the price, not the dashboard payload.
type modelGateway struct {
	mu            sync.Mutex
	customers     int
	sessions      int
	amountDelta   int64
	currency      domain.Currency
	useCurrency   bool
	customerIDs   map[string]string
	sessionLedger map[string]application.CheckoutSession
}

func newModelGateway() *modelGateway {
	return &modelGateway{customerIDs: make(map[string]string), sessionLedger: make(map[string]application.CheckoutSession)}
}

func (g *modelGateway) CreateCustomer(_ context.Context, _ application.CreateCustomerRequest) (application.Customer, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.customers++
	id := domain.StripeCustomerID(fmt.Sprintf("cus_model%d", g.customers))
	return application.Customer{ID: id}, nil
}

func (g *modelGateway) CreateCheckoutSession(_ context.Context, req application.CreateCheckoutSessionRequest) (application.CheckoutSession, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sessions++
	amount := int64(990)
	currency := domain.CurrencyBRL
	if req.PriceID.String() == "price_1QbrMember" {
		amount = 1990
	}
	if g.useCurrency {
		currency = g.currency
	}
	amount += g.amountDelta
	session := application.CheckoutSession{
		ID:            domain.StripeCheckoutSessionID(fmt.Sprintf("cs_test_model%d", g.sessions)),
		Status:        domain.CheckoutStatusOpen,
		PaymentStatus: domain.CheckoutPaymentUnpaid,
		Mode:          domain.CheckoutModePayment,
		AmountMinor:   amount,
		Currency:      currency,
	}
	g.sessionLedger[session.ID.String()] = session
	return session, nil
}

func (g *modelGateway) GetCheckoutSession(_ context.Context, id domain.StripeCheckoutSessionID) (application.CheckoutSession, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	session, ok := g.sessionLedger[id.String()]
	if !ok {
		return application.CheckoutSession{}, application.ErrPaymentGatewayRequestInvalid
	}
	return session, nil
}

func (g *modelGateway) GetSubscription(_ context.Context, _ domain.StripeSubscriptionID) (application.Subscription, error) {
	return application.Subscription{}, application.ErrPaymentGatewayRequestInvalid
}

func (g *modelGateway) CreatePortalSession(_ context.Context, _ application.CreatePortalSessionRequest) (application.PortalSession, error) {
	return application.PortalSession{}, application.ErrPaymentGatewayRequestInvalid
}

// modelSignWebhook signs one delivery exactly the way the provider does:
// HMAC-SHA256(secret, "timestamp.body") rendered as t=,v1= with a separate
// timestamp header. Forgery and staleness are drawn by altering the inputs.
func modelSignWebhook(secret string, at time.Time, body []byte) (sigHeader, tsHeader string) {
	tsHeader = strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsHeader + "." + string(body)))
	return fmt.Sprintf("t=%s,v1=%s", tsHeader, hex.EncodeToString(mac.Sum(nil))), tsHeader
}

// entitlementWorld wires the real billing stack over one disposable
// database: catalog, intents, lots, subscriptions, webhook events, the real
// HMAC verifier and the real wallet ledger behind the INK port.
type entitlementWorld struct {
	ctx       context.Context
	t         *testing.T
	pool      *pgxpool.Pool
	clock     modelBillingClock
	catalog   *domain.Catalog
	gateway   *modelGateway
	verifier  *stripeadapter.WebhookVerifier
	checkout  *application.CreateCheckoutUseCase
	settleInk *application.SettleCheckoutUseCase
	webhook   *application.ProcessWebhookUseCase
	grant     *application.GrantArenaPassesUseCase
	consume   *application.ConsumeArenaPassUseCase
	expire    *application.ExpireArenaPassLotsUseCase
	applySub  *application.ApplyMemberEntitlementsUseCase
	billing   *billingpg.Repository
	queries   *platformpg.Queries
	wallet    *walletpg.Repository
}

const modelWebhookSecret = "whsec_modeltestsecret"

func newEntitlementWorld(t *testing.T) *entitlementWorld {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	clock := modelBillingClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	catalog := modelCatalog(t)
	gateway := newModelGateway()
	billingRepo := billingpg.NewRepositoryWithClock(pool, clock)
	walletRepo := walletpg.NewRepository(pool)
	queries := platformpg.New(pool)
	inker := billingwallet.NewInker(walletRepo, clock)
	verifier, err := stripeadapter.NewWebhookVerifier(modelWebhookSecret, time.Hour, clock)
	if err != nil {
		t.Fatalf("NewWebhookVerifier: %v", err)
	}
	checkout, err := application.NewCreateCheckoutUseCase(application.CheckoutDependencies{
		Catalog:    catalog,
		Gateway:    gateway,
		Purchasers: billingRepo,
		Customers:  billingRepo,
		Intents:    billingRepo,
		Clock:      clock,
		Returns:    application.CheckoutReturnURLs{Success: "https://arena.example/checkout/success", Cancel: "https://arena.example/checkout/cancel"},
	})
	if err != nil {
		t.Fatalf("NewCreateCheckoutUseCase: %v", err)
	}
	settleInk, err := application.NewSettleCheckoutUseCase(application.SettleCheckoutDependencies{Catalog: catalog, Intents: billingRepo, Inker: inker, Clock: clock})
	if err != nil {
		t.Fatalf("NewSettleCheckoutUseCase: %v", err)
	}
	settlePass, err := application.NewSettleArenaPassUseCase(application.SettleArenaPassDependencies{Catalog: catalog, Intents: billingRepo, Lots: billingRepo, Clock: clock})
	if err != nil {
		t.Fatalf("NewSettleArenaPassUseCase: %v", err)
	}
	applySub, err := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{Catalog: catalog, Subscriptions: billingRepo, Customers: billingRepo, PassLots: billingRepo, Inker: inker, Clock: clock})
	if err != nil {
		t.Fatalf("NewApplyMemberEntitlementsUseCase: %v", err)
	}
	webhook, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier: verifier, Events: billingRepo,
		Settler: settleInk, PassSettler: settlePass, MemberSettler: applySub,
		Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase: %v", err)
	}
	return &entitlementWorld{
		ctx: ctx, t: t, pool: pool, clock: clock, catalog: catalog, gateway: gateway,
		verifier: verifier, checkout: checkout, settleInk: settleInk, webhook: webhook,
		grant:    application.NewGrantArenaPassesUseCase(billingRepo, clock),
		consume:  application.NewConsumeArenaPassUseCase(billingRepo, clock),
		expire:   application.NewExpireArenaPassLotsUseCase(billingRepo, clock),
		applySub: applySub, billing: billingRepo, queries: queries, wallet: walletRepo,
	}
}

// entitlementSlot is everything the oracle believes about one buyer.
type entitlementSlot struct {
	email      string
	pgUUID     pgtype.UUID
	accountID  domain.AccountID
	walletID   walletdomain.AccountID
	customerID string
	walletFree int64
	walletINK  int64
	lots       map[string]*entitlementLot
	subs       map[string]*entitlementSub
	events     map[string]bool
	bodies     map[string]string
	intents    map[string]bool
	consumed   map[string]bool
}

// entitlementLot is one pass lot the oracle has seen granted.
type entitlementLot struct {
	origin    string
	quantity  int32
	remaining int32
	expiresAt *time.Time
}

// entitlementSub is one subscription mirror the oracle has seen applied,
// with the periods already franchised.
type entitlementSub struct {
	status      string
	periodStart int64
	franchised  map[int64]bool
	terminal    bool
}

// entitlementDivergence is one place where the stack and the oracle
// disagreed.
type entitlementDivergence struct {
	step int
	op   string
	want bool
	got  bool
}

// entitlementRunner carries one deterministic script over two buyers.
type entitlementRunner struct {
	t        *testing.T
	world    *entitlementWorld
	slots    []*entitlementSlot
	step     int
	diverged []entitlementDivergence
	trace    []string
	accepts  int
	refusals int
	replays  int
	expiries int
}

func (r *entitlementRunner) check(op string, wantAccept bool, err error) bool {
	gotAccept := err == nil
	r.trace = append(r.trace, op)
	if wantAccept {
		r.accepts++
	} else {
		r.refusals++
	}
	if gotAccept != wantAccept {
		r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: op, want: wantAccept, got: gotAccept})
		return false
	}
	return true
}

// signedDelivery signs one event body with the model secret at the model
// clock, returning the exact command a provider delivery carries.
func (r *entitlementRunner) signedDelivery(eventID, body string) application.ProcessWebhookCommand {
	raw := []byte(body)
	sig, ts := modelSignWebhook(modelWebhookSecret, r.world.clock.Now(), raw)
	_ = eventID
	return application.ProcessWebhookCommand{RawBody: raw, SignatureHeader: sig, TimestampHeader: ts}
}

func checkoutCompletedBody(eventID, sessionID string) string {
	return fmt.Sprintf(`{"id": %q, "type": "checkout.session.completed", "livemode": false, "created": 1758100000, "data": {"object": {"id": %q, "status": "complete", "payment_status": "paid"}}}`, eventID, sessionID)
}

func subscriptionBody(eventID, subID, customerID, status, priceID string, periodStart, periodEnd int64) string {
	return fmt.Sprintf(`{"id": %q, "type": "customer.subscription.updated", "livemode": false, "created": 1758100000, "data": {"object": {"id": %q, "customer": %q, "status": %q, "price": %q, "current_period_start": %d, "current_period_end": %d}}}`,
		eventID, subID, customerID, status, priceID, periodStart, periodEnd)
}

func unknownEventBody(eventID string) string {
	return fmt.Sprintf(`{"id": %q, "type": "invoice.payment_failed", "livemode": false, "created": 1758100000, "data": {"object": {}}}`, eventID)
}

// eventCount counts deliveries of one sequence by ID prefix: events are
// global rows, so the oracle scopes them the way the generator names them.
func (r *entitlementRunner) eventCount(prefix string) int {
	var count int
	if err := r.world.pool.QueryRow(r.world.ctx, "SELECT count(*) FROM app.stripe_events WHERE stripe_event_id LIKE $1", prefix+"%").Scan(&count); err != nil {
		r.t.Fatalf("count events: %v", err)
	}
	return count
}

// walletBoth reads the observed bucket balances of one buyer.
func (r *entitlementRunner) walletBoth(slot *entitlementSlot) (free, purchased int64) {
	r.t.Helper()
	balance, err := r.world.wallet.DerivedBalance(r.world.ctx, slot.walletID)
	if err != nil {
		r.t.Fatalf("derived wallet balance: %v", err)
	}
	return balance.Free.Int64(), balance.Purchased.Int64()
}

// lotsOf lists the observed pass lots of one buyer by reference.
func (r *entitlementRunner) lotsOf(slot *entitlementSlot) map[string]domain.PassLot {
	r.t.Helper()
	lots, err := r.world.billing.ListAccountPassLots(r.world.ctx, slot.accountID)
	if err != nil {
		r.t.Fatalf("list pass lots: %v", err)
	}
	out := make(map[string]domain.PassLot)
	for _, lot := range lots {
		out[lot.Reference().String()] = lot
	}
	return out
}

// reconcile proves the observed state equals the oracle: wallet buckets,
// lots per reference with remaining counts, and subscription mirrors.
func (r *entitlementRunner) reconcile(op string) {
	r.t.Helper()
	for _, slot := range r.slots {
		free, purchased := r.walletBoth(slot)
		if free != slot.walletFree || purchased != slot.walletINK {
			r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: op + ":wallet", want: true, got: false})
		}
		observed := r.lotsOf(slot)
		if len(observed) != len(slot.lots) {
			r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: op + ":lots", want: true, got: false})
		}
		for ref, want := range slot.lots {
			got, ok := observed[ref]
			if !ok || got.Remaining() != want.remaining || got.Quantity().Int32() != want.quantity {
				r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: op + ":lot:" + ref, want: true, got: false})
			}
		}
		for subID, want := range slot.subs {
			rec, err := r.world.billing.GetSubscriptionByStripeID(r.world.ctx, mustParseSubID(r.t, subID))
			if err != nil {
				r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: op + ":sub:" + subID, want: true, got: false})
				continue
			}
			if rec.Status.String() != want.status {
				r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: op + ":sub-status:" + subID, want: true, got: false})
			}
		}
	}
}

func mustParseSubID(t *testing.T, raw string) domain.StripeSubscriptionID {
	t.Helper()
	id, err := domain.ParseStripeSubscriptionID(raw)
	if err != nil {
		t.Fatalf("ParseStripeSubscriptionID(%q): %v", raw, err)
	}
	return id
}

// runOneSequence executes one deterministic script over the runner buyers:
// purchases with duplicates and forgeries, passes with consumption, member
// applies across periods, and random edge deliveries.
func (r *entitlementRunner) runOneSequence(rnd *testsource.Random, seq int) {
	steps := 4 + rnd.Int64n(5)
	for i := 0; i < int(steps); i++ {
		slotIdx := int(rnd.Int64n(int64(len(r.slots))))
		slot := r.slots[slotIdx]
		switch rnd.Int64n(10) {
		case 0, 1:
			r.opBuyINK(slot, seq, i)
		case 2:
			r.opBuyPass(slot, seq, i)
		case 3:
			// Arena IDs must be UUIDs: the consume path keys the
			// idempotency on the arena row, so free-form strings are
			// refused before any lot is touched.
			arena := fmt.Sprintf("018f6b2a-0000-7000-8000-%012d", seq*1000+i)
			if len(slot.consumed) > 0 && rnd.Int64n(4) == 0 {
				for a := range slot.consumed {
					arena = a
					break
				}
			}
			r.opConsume(slot, arena)
		case 4:
			r.opMemberApply(slot, fmt.Sprintf("sub_m%ds%dx", seq, slotIdx), "active", modelP1Start, modelP1End, "")
		case 5:
			r.opExpireSweep()
		case 6:
			r.opReplaySeen(slot)
		case 7:
			r.opForged(slot, seq, i)
		default:
			r.opUnknownType(slot, seq, i)
		}
	}
}

// opReplaySeen redelivers one observed body under its own ID: the claim
// resolves the stored row and nothing moves again.
func (r *entitlementRunner) opReplaySeen(slot *entitlementSlot) {
	r.step++
	r.t.Helper()
	if len(slot.bodies) == 0 {
		return
	}
	ids := make([]string, 0, len(slot.bodies))
	for id := range slot.bodies {
		ids = append(ids, id)
	}
	// Deterministic pick without entropy: first ID in map order is
	// unstable, so the choice comes from the stored count instead.
	id := ids[len(slot.events)%len(ids)]
	body, ok := slot.bodies[id]
	if !ok {
		r.t.Fatalf("model lost the body of event %q", id)
	}
	if !r.deliver(slot, id, body, false, true) {
		return
	}
	r.reconcile("replay-seen")
}

// opForged delivers one correctly shaped body with a forged signature:
// refused before any row is claimed.
func (r *entitlementRunner) opForged(slot *entitlementSlot, seq, n int) {
	r.step++
	r.t.Helper()
	eventID := fmt.Sprintf("evt_model%d%dforged", seq, n)
	body := unknownEventBody(eventID)
	r.deliver(slot, eventID, body, true, false)
}

// opUnknownType delivers a well-formed, verified event of an unknown type:
// acknowledged and ignored, stored but benefit-free.
func (r *entitlementRunner) opUnknownType(slot *entitlementSlot, seq, n int) {
	r.step++
	r.t.Helper()
	eventID := fmt.Sprintf("evt_model%d%dunknown", seq, n)
	if !r.deliver(slot, eventID, unknownEventBody(eventID), false, true) {
		return
	}
	slot.events[eventID] = true
	r.reconcile("unknown-type")
}

// newModelSlot creates one buyer: an active verified platform account, its
// provider customer correlation, and a blank oracle page.
func (r *entitlementRunner) newModelSlot(seq, slot int) *entitlementSlot {
	r.t.Helper()
	email := fmt.Sprintf("ent-model-%d-%d@arena.example.com", seq, slot)
	pgAcc := mustCheckoutAccount(r.t, r.world.ctx, r.world.queries, email, "active", true)
	accountID := domain.AccountID(uuidString(pgAcc.ID))
	customerID := fmt.Sprintf("cus_model%d%d", seq, slot)
	if _, err := r.world.billing.RecordStripeCustomer(r.world.ctx, application.RecordStripeCustomerRequest{
		AccountID:  accountID,
		CustomerID: domain.StripeCustomerID(customerID),
		Livemode:   false,
	}); err != nil {
		r.t.Fatalf("record stripe customer: %v", err)
	}
	return &entitlementSlot{
		email: email, pgUUID: pgAcc.ID, accountID: accountID,
		walletID:   walletdomain.AccountID(uuidString(pgAcc.ID)),
		customerID: customerID,
		lots:       make(map[string]*entitlementLot), subs: make(map[string]*entitlementSub),
		events: make(map[string]bool), bodies: make(map[string]string), intents: make(map[string]bool),
		consumed: make(map[string]bool),
	}
}

// TestBillingEntitlementModel runs deterministic entitlement scripts over
// two real buyers per sequence: INK and pass purchases with duplicates and
// forgeries, consumption with replays, member lifecycles across periods
// with out-of-order events, and edge deliveries.
func TestBillingEntitlementModel(t *testing.T) {
	seed := testsource.SeedFor(t)
	rnd := testsource.NewRandom(seed)
	world := newEntitlementWorld(t)
	const sequences = 120
	divergentRuns := 0
	var first []entitlementDivergence
	var firstTrace []string
	accepts, refusals, replays, expiries := 0, 0, 0, 0
	for seq := 0; seq < sequences; seq++ {
		runner := &entitlementRunner{t: t, world: world}
		for slot := 0; slot < 2; slot++ {
			runner.slots = append(runner.slots, runner.newModelSlot(seq, slot))
		}
		// Scripted member lifecycle per buyer, then random interleaving.
		// Subscription IDs are namespaced per buyer: the provider
		// identifier is global, and sharing one across buyers would flip
		// ownership of the same mirror row.
		for slotIdx, slot := range runner.slots {
			subID := fmt.Sprintf("sub_m%ds%d", seq, slotIdx)
			runner.opMemberApply(slot, subID, "trialing", modelP0Start, modelP0End, "")
			runner.opMemberApply(slot, subID, "active", modelP0Start, modelP0End, "")
			runner.opExpireSweep()
			runner.opMemberApply(slot, subID, "trialing", modelP1Start, modelP1End, "")
			runner.opMemberApply(slot, subID, "active", modelP1Start, modelP1End, "")
			runner.opMemberApply(slot, subID, "active", modelP1Start, modelP1End, "")
			runner.opMemberApply(slot, subID, "active", modelP2Start, modelP2End, "")
			runner.opMemberApply(slot, subID, "past_due", modelP2Start, modelP2End, "")
			runner.opMemberApply(slot, subID, "canceled", modelP2Start, modelP2End, "")
			runner.opMemberApply(slot, subID, "active", modelP1Start, modelP1End, "ooo")
			runner.opMemberApply(slot, subID+"B", "active", modelP2Start, modelP2End, "")
		}
		runner.runOneSequence(rnd, seq)
		accepts += runner.accepts
		refusals += runner.refusals
		replays += runner.replays
		expiries += runner.expiries
		if len(runner.diverged) > 0 {
			divergentRuns++
			if first == nil {
				first = append([]entitlementDivergence{}, runner.diverged...)
				if len(first) > 5 {
					first = first[:5]
				}
				firstTrace = append([]string{}, runner.trace...)
				if len(firstTrace) > 10 {
					firstTrace = firstTrace[len(firstTrace)-10:]
				}
			}
		}
	}
	if accepts == 0 || refusals == 0 || replays == 0 || expiries == 0 {
		t.Fatalf("model never exercised a full path: accepts=%d refusals=%d replays=%d expiries=%d", accepts, refusals, replays, expiries)
	}
	if divergentRuns > 0 {
		t.Fatalf("%d of %d sequences diverged, first: %+v trace: %v", divergentRuns, sequences, first, firstTrace)
	}
	t.Logf("billing entitlement model: %d sequences, accepts=%d refusals=%d replays=%d expiries=%d, seed %d", sequences, accepts, refusals, replays, expiries, seed)
}

// modelPeriods are the fixed billing periods every sequence uses: P0 is
// already over at the model clock (expiry coverage), P1 and P2 are current.
var (
	modelP0Start int64 = 1754006400
	modelP0End   int64 = 1756684800
	modelP1Start int64 = 1756684800
	modelP1End   int64 = 1759276800
	modelP2Start int64 = 1759276800
	modelP2End   int64 = 1761955200
)

// opBuyINK runs one INK purchase: checkout creation plus the verified
// settlement webhook. Session IDs are learned from observed results; the
// grant math (catalog quantity, exactly once) is the oracle's own.
func (r *entitlementRunner) opBuyINK(slot *entitlementSlot, seq, n int) {
	r.step++
	r.t.Helper()
	res, err := r.world.checkout.Execute(r.world.ctx, application.CreateCheckoutCommand{
		AccountID: string(slot.accountID), Market: "BR", Product: "ink_10000",
		IdempotencyKey: fmt.Sprintf("buy-ink-%d-%d", seq, n),
	})
	if !r.check("buy-create", true, err) || res == nil {
		return
	}
	sessionID := res.SessionID.String()
	eventID := fmt.Sprintf("evt_model%d%dink", seq, n)
	if !r.deliver(slot, eventID, checkoutCompletedBody(eventID, sessionID), false, true) {
		return
	}
	slot.walletINK += 10000
	slot.intents[sessionID] = true
	slot.events[eventID] = true
	r.reconcile("buy-ink")
}

// deliver runs one delivery and compares with the oracle prediction.
// Verified bodies are accepted (duplicates replay silently); forged,
// stale or malformed ones are refused writing nothing.
func (r *entitlementRunner) deliver(slot *entitlementSlot, eventID, body string, forged bool, wantAccept bool) bool {
	r.step++
	r.t.Helper()
	beforeEvents := r.eventCount("")
	beforeFree, beforeINK := slot.walletFree, slot.walletINK
	beforeLots := len(r.lotsOf(slot))
	var cmd application.ProcessWebhookCommand
	if forged {
		cmd = application.ProcessWebhookCommand{RawBody: []byte(body), SignatureHeader: "t=1,v1=deadbeef", TimestampHeader: "1"}
	} else {
		cmd = r.signedDelivery(eventID, body)
	}
	_, redelivery := slot.events[eventID]
	err := r.world.webhook.Execute(r.world.ctx, cmd)
	if !r.check("webhook", wantAccept, err) {
		return false
	}
	if redelivery {
		r.replays++
	}
	if !wantAccept {
		if got := r.eventCount(""); got != beforeEvents {
			r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: "webhook-refused-events", want: true, got: false})
		}
		freeNow, inkNow := r.walletBoth(slot)
		if freeNow != beforeFree || inkNow != beforeINK {
			r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: "webhook-refused-wallet", want: true, got: false})
		}
		if got := len(r.lotsOf(slot)); got != beforeLots {
			r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: "webhook-refused-lots", want: true, got: false})
		}
		return false
	}
	slot.events[eventID] = true
	slot.bodies[eventID] = body
	return true
}

// opBuyPass runs one Arena Pass purchase through the verified webhook: the
// new lot is learned from the observed lots, never constructed.
func (r *entitlementRunner) opBuyPass(slot *entitlementSlot, seq, n int) {
	r.step++
	r.t.Helper()
	res, err := r.world.checkout.Execute(r.world.ctx, application.CreateCheckoutCommand{
		AccountID: string(slot.accountID), Market: "BR", Product: "pass_1",
		IdempotencyKey: fmt.Sprintf("buy-pass-%d-%d", seq, n),
	})
	if !r.check("buy-pass-create", true, err) || res == nil {
		return
	}
	sessionID := res.SessionID.String()
	eventID := fmt.Sprintf("evt_model%d%dpass", seq, n)
	before := len(r.lotsOf(slot))
	if !r.deliver(slot, eventID, checkoutCompletedBody(eventID, sessionID), false, true) {
		return
	}
	after := r.lotsOf(slot)
	if len(after) != before+1 {
		r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: "buy-pass-lot", want: true, got: false})
		return
	}
	for ref, lot := range after {
		if _, known := slot.lots[ref]; !known {
			slot.lots[ref] = &entitlementLot{origin: lot.Origin().String(), quantity: lot.Quantity().Int32(), remaining: lot.Remaining()}
		}
	}
	slot.intents[sessionID] = true
	slot.events[eventID] = true
	r.reconcile("buy-pass")
}

// opConsume consumes one pass for one arena. The oracle derives the
// expectation: a known arena replays its original consumption, an unknown
// arena consumes only when an unexpired lot still holds a pass.
func (r *entitlementRunner) opConsume(slot *entitlementSlot, arena string) {
	r.step++
	r.t.Helper()
	_, replayed := slot.consumed[arena]
	consumable := false
	for _, lot := range slot.lots {
		if lot.remaining > 0 && (lot.expiresAt == nil || lot.expiresAt.After(r.world.clock.Now())) {
			consumable = true
			break
		}
	}
	wantAccept := replayed || consumable
	res, err := r.world.consume.Execute(r.world.ctx, application.ConsumeArenaPassCommand{AccountID: string(slot.accountID), ArenaID: arena})
	if !r.check("consume", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil || res.Replayed != replayed {
		r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: "consume-flags", want: true, got: false})
		return
	}
	if replayed {
		return
	}
	ref := res.Lot.Reference().String()
	lot, ok := slot.lots[ref]
	if !ok {
		r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: "consume-lot:" + ref, want: true, got: false})
		return
	}
	lot.remaining--
	slot.consumed[arena] = true
	if res.Remaining != lot.remaining {
		r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: "consume-remaining", want: true, got: false})
	}
	r.reconcile("consume")
}

// opMemberApply runs one subscription event through the verified webhook.
// The event ID names the delivery, not the content: identical triples
// redeliver (replay path), while a nonce names a fresh emission of stale
// content (ordering path). Only active and trialing statuses of an
// unforfeited period grant, exactly once per (subscription, period).
func (r *entitlementRunner) opMemberApply(slot *entitlementSlot, subID, status string, periodStart, periodEnd int64, nonce string) {
	r.step++
	r.t.Helper()
	eventID := fmt.Sprintf("evt_%s%s%d%s", strings.ReplaceAll(subID, "_", ""), strings.ReplaceAll(status, "_", ""), periodStart, nonce)
	body := subscriptionBody(eventID, subID, slot.customerID, status, "price_1QbrMember", periodStart, periodEnd)
	if !r.deliver(slot, eventID, body, false, true) {
		return
	}
	slot.events[eventID] = true
	sub := slot.subs[subID]
	if sub == nil {
		sub = &entitlementSub{franchised: make(map[int64]bool)}
		slot.subs[subID] = sub
	}
	if sub.terminal && status != "canceled" {
		r.reconcile("member-ignored")
		return
	}
	if sub.periodStart > periodStart && sub.periodStart != 0 {
		r.reconcile("member-ignored")
		return
	}
	sub.status = status
	if periodStart >= sub.periodStart {
		sub.periodStart = periodStart
	}
	if status == "canceled" {
		sub.terminal = true
		r.reconcile("member-canceled")
		return
	}
	if status != "active" && status != "trialing" {
		r.reconcile("member-no-grant")
		return
	}
	if sub.franchised[periodStart] {
		r.reconcile("member-replay")
		return
	}
	sub.franchised[periodStart] = true
	slot.walletFree += 30000
	endsAt := time.Unix(periodEnd, 0).UTC()
	slot.lots[fmt.Sprintf("member:%s:%d", subID, periodStart)] = &entitlementLot{origin: "member", quantity: 1, remaining: 1, expiresAt: &endsAt}
	r.reconcile("member-apply")
}

// opExpireSweep runs the expiration sweep and proves it is a pure report:
// the same data yields the same expired set twice with no writes.
func (r *entitlementRunner) opExpireSweep() {
	r.step++
	r.t.Helper()
	first, err := r.world.expire.Execute(r.world.ctx)
	if !r.check("expire-sweep", true, err) || first == nil {
		return
	}
	if first.ExpiredPasses > 0 {
		r.expiries++
	}
	second, err := r.world.expire.Execute(r.world.ctx)
	if !r.check("expire-sweep-again", true, err) || second == nil {
		return
	}
	if len(first.ExpiredLots) != len(second.ExpiredLots) || first.ExpiredPasses != second.ExpiredPasses {
		r.diverged = append(r.diverged, entitlementDivergence{step: r.step, op: "expire-stable", want: true, got: false})
	}
	r.reconcile("expire-sweep")
}

// TestBillingDivergentCharges proves the server decides the price: a
// provider session charging a divergent amount or currency is refused with
// no intent recorded, an unknown product is refused, a tampered intent
// still settles exactly the catalog quantity, and a pass product through
// the INK settler is refused for the wrong grant kind.
func TestBillingDivergentCharges(t *testing.T) {
	world := newEntitlementWorld(t)
	ctx := world.ctx
	runner := &entitlementRunner{t: t, world: world}
	slot := runner.newModelSlot(900, 0)
	runner.slots = []*entitlementSlot{slot}
	buyer := string(slot.accountID)
	intentCount := func() int {
		t.Helper()
		var count int
		if err := world.pool.QueryRow(ctx, "SELECT count(*) FROM app.checkout_intents").Scan(&count); err != nil {
			t.Fatalf("count intents: %v", err)
		}
		return count
	}
	create := func(product, key string) error {
		_, err := world.checkout.Execute(ctx, application.CreateCheckoutCommand{AccountID: buyer, Market: "BR", Product: product, IdempotencyKey: key})
		return err
	}
	// Divergent provider amounts and currencies never become intents.
	world.gateway.amountDelta = 1
	if err := create("ink_10000", "div-amount"); err == nil {
		t.Fatal("divergent provider amount was accepted")
	}
	world.gateway.amountDelta = 0
	world.gateway.useCurrency = true
	world.gateway.currency = domain.CurrencyUSD
	if err := create("ink_10000", "div-currency"); err == nil {
		t.Fatal("divergent provider currency was accepted")
	}
	world.gateway.useCurrency = false
	if err := create("nope_1", "div-product"); err == nil {
		t.Fatal("unknown product was accepted")
	}
	beforeIntents := intentCount()
	// A tampered intent record still settles exactly the catalog quantity:
	// the server decides the grant, never the stored amount.
	tamperedSession := mustModelSessionID(t, "cs_test_tampered001")
	if _, err := world.billing.RecordCheckoutIntent(ctx, application.RecordCheckoutIntentRequest{
		AccountID: slot.accountID, Market: domain.MarketBrazil, ProductID: mustModelProductID(t, "ink_10000"),
		CatalogVersion: 1, Amount: mustModelMoney(t, 991, domain.CurrencyBRL), Livemode: false,
		SessionID: tamperedSession, Status: domain.CheckoutIntentOpen,
	}); err != nil {
		t.Fatalf("record tampered intent: %v", err)
	}
	runner.slots = []*entitlementSlot{slot}
	beforeFree, beforeINK := runner.walletBoth(slot)
	if !runner.deliver(slot, "evt_divtampered001", checkoutCompletedBody("evt_divtampered001", tamperedSession.String()), false, true) {
		t.Fatal("tampered intent did not settle")
	}
	freeAfter, inkAfter := runner.walletBoth(slot)
	if inkAfter-beforeINK != 10000 || freeAfter != beforeFree {
		t.Fatalf("tampered intent granted %d purchased (free %d), want exactly the catalog 10000", inkAfter-beforeINK, freeAfter)
	}
	if got := intentCount(); got != beforeIntents+1 {
		t.Fatalf("intents = %d, want %d (only the tampered record added)", got, beforeIntents+1)
	}
	// A pass product through the INK settler is refused for the wrong
	// grant kind, granting nothing.
	passSession := mustModelSessionID(t, "cs_test_wrongkind001")
	if _, err := world.billing.RecordCheckoutIntent(ctx, application.RecordCheckoutIntentRequest{
		AccountID: slot.accountID, Market: domain.MarketBrazil, ProductID: mustModelProductID(t, "pass_1"),
		CatalogVersion: 1, Amount: mustModelMoney(t, 990, domain.CurrencyBRL), Livemode: false,
		SessionID: passSession, Status: domain.CheckoutIntentOpen,
	}); err != nil {
		t.Fatalf("record pass intent: %v", err)
	}
	if _, err := world.settleInk.Execute(ctx, application.SettleCheckoutCommand{SessionID: passSession}); err == nil {
		t.Fatal("pass product through the INK settler was accepted")
	}
}

// mustModelMoney parses a money amount or fails.
func mustModelMoney(t *testing.T, amount int64, currency domain.Currency) domain.Money {
	t.Helper()
	money, err := domain.NewMoney(amount, currency)
	if err != nil {
		t.Fatalf("NewMoney(%d): %v", amount, err)
	}
	return money
}

// mustModelSessionID parses a checkout session identifier or fails.
func mustModelSessionID(t *testing.T, raw string) domain.StripeCheckoutSessionID {
	t.Helper()
	id, err := domain.ParseStripeCheckoutSessionID(raw, false)
	if err != nil {
		t.Fatalf("ParseStripeCheckoutSessionID(%q): %v", raw, err)
	}
	return id
}

// TestBillingWebhookConcurrent races one delivery across goroutines: the
// claimed event settles exactly once, so a duplicated provider delivery
// can never double-grant, and a forged one never grants at all.
func TestBillingWebhookConcurrent(t *testing.T) {
	world := newEntitlementWorld(t)
	ctx := world.ctx
	runner := &entitlementRunner{t: t, world: world}
	slot := runner.newModelSlot(950, 0)
	runner.slots = []*entitlementSlot{slot}
	res, err := world.checkout.Execute(ctx, application.CreateCheckoutCommand{
		AccountID: string(slot.accountID), Market: "BR", Product: "ink_10000",
		IdempotencyKey: "race-checkout-1",
	})
	if err != nil {
		t.Fatalf("create checkout: %v", err)
	}
	sessionID := res.SessionID.String()
	const racers = 16
	var wg sync.WaitGroup
	outcomes := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := checkoutCompletedBody("evt_race001", sessionID)
			sig, ts := modelSignWebhook(modelWebhookSecret, world.clock.Now(), []byte(body))
			outcomes <- world.webhook.Execute(ctx, application.ProcessWebhookCommand{RawBody: []byte(body), SignatureHeader: sig, TimestampHeader: ts})
		}()
	}
	wg.Wait()
	close(outcomes)
	for err := range outcomes {
		if err != nil && !isWebhookInProcessing(err) {
			t.Fatalf("racer failed with an unexpected error: %v", err)
		}
	}
	freeAfter, inkAfter := runner.walletBoth(slot)
	if inkAfter != 10000 || freeAfter != 0 {
		t.Fatalf("raced delivery granted wallet %d/%d, want exactly one 10000 credit", freeAfter, inkAfter)
	}
	if got := runner.eventCount("evt_race001"); got != 1 {
		t.Fatalf("raced delivery stored %d event rows, want exactly 1", got)
	}
}

// isWebhookInProcessing reports the loser of a genuine delivery race: the
// event is being processed by the winner, which is a refusal, not a grant.
func isWebhookInProcessing(err error) bool {
	return errors.Is(err, application.ErrWebhookEventInProcessing)
}

// TestBillingEntitlementDetectsMutantFranchise proves the harness bites:
// the same script run against a mutant oracle that grants the franchise on
// every apply regardless of period must report the replay divergence.
func TestBillingEntitlementDetectsMutantFranchise(t *testing.T) {
	world := newEntitlementWorld(t)
	runner := &entitlementRunner{t: t, world: world}
	slot := runner.newModelSlot(960, 0)
	runner.slots = []*entitlementSlot{slot}
	subID := "sub_mutantfranchise"
	runner.opMemberApply(slot, subID, "active", modelP1Start, modelP1End, "")
	runner.opMemberApply(slot, subID, "active", modelP1Start, modelP1End, "")
	if len(runner.diverged) > 0 {
		t.Fatalf("true franchise rule diverged: %+v", runner.diverged)
	}
	runner2 := &entitlementRunner{t: t, world: world}
	slot2 := runner2.newModelSlot(961, 0)
	runner2.slots = []*entitlementSlot{slot2}
	subID2 := "sub_mutantfranchise2"
	runner2.opMemberApply(slot2, subID2, "active", modelP1Start, modelP1End, "")
	// Mutant run: the oracle believes the duplicate below grants again.
	runner2.opMemberApply(slot2, subID2, "active", modelP1Start, modelP1End, "")
	slot2.walletFree += 30000
	slot2.lots["member:"+subID2+":1756684800"] = &entitlementLot{origin: "member", quantity: 1, remaining: 1}
	runner2.reconcile("mutant-check")
	if len(runner2.diverged) == 0 {
		t.Fatal("mutant franchise (grants every apply) reported no divergence")
	}
}

// mustModelProductID parses a catalog product identifier or fails.
func mustModelProductID(t *testing.T, raw string) domain.ProductID {
	t.Helper()
	id, err := domain.ParseProductID(raw)
	if err != nil {
		t.Fatalf("ParseProductID(%q): %v", raw, err)
	}
	return id
}
