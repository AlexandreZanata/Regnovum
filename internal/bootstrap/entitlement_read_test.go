// Tests of the read-only entitlement surface composed in the process
// (P49-T05): the pass summary, the paginated consumption history and the
// Member subscription projection run on the platform mux over disposable
// PostgreSQL, driven by real HTTP with the same pool and security boundary
// as the account journey — the session the JSON login opened is the session
// the entitlement reads require, and no second authentication exists.
//
// Reads never grant: both projections are derived from rows already written
// by the audited use cases, so a reload returns the same stored state.
package bootstrap_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	billingpg "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// entitlementJourney is the account plus entitlement-read surfaces on a real
// listener, sharing one pool and one security boundary like `arena server`
// does.
type entitlementJourney struct {
	account      *bootstrap.AccountSurface
	entitlements *bootstrap.EntitlementSurface
	server       *httptest.Server
	pool         *pgxpool.Pool
}

// newEntitlementJourney composes both surfaces the way the process does.
func newEntitlementJourney(t *testing.T) *entitlementJourney {
	t.Helper()

	database := dbtest.New(t)
	pool := database.Pool.Pool()
	clock := clockseed.NewClock()
	random := clockseed.NewRandom()
	manager, err := security.New(security.Options{Env: config.EnvTest, Clock: clock, Random: random})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}
	account, err := bootstrap.ComposeAccount(bootstrap.Options{
		Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool: pool, Clock: clock, Random: random, Assets: manifestFixture(t), Security: manager,
	})
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}
	entitlements, err := bootstrap.ComposeEntitlementReads(bootstrap.Options{
		Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool: pool, Clock: clock, Random: random, Assets: manifestFixture(t), Security: manager,
		CursorSecret: []byte(cursorSecret),
	})
	if err != nil {
		t.Fatalf("ComposeEntitlementReads() error = %v", err)
	}
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clock)
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{},
		[]httpserver.Surface{account.Surface(), entitlements.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &entitlementJourney{account: account, entitlements: entitlements, server: server, pool: pool}
}

// entitlementLogin registers, verifies and signs in one account over the
// JSON API, returning the client holding its session plus the account
// identifier.
func entitlementLogin(t *testing.T, journey *entitlementJourney, email, password string) (*http.Client, string) {
	t.Helper()

	client := browser(t)
	status, _, _ := jsonPost(t, client, journey.server, "/api/v1/auth/register", `{"email":`+quoteJSON(email)+`,"password":`+quoteJSON(password)+`}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /api/v1/auth/register status = %d, want 201", status)
	}
	address, err := identitydomain.ParseEmail(email)
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	token, found := journey.account.LocalSink().LastTokenForEmail(address)
	if !found {
		t.Fatalf("registration of %s issued no token", email)
	}
	if status, _ := jsonGet(t, client, journey.server, "/api/v1/auth/verify?token="+token); status != http.StatusOK {
		t.Fatalf("GET /api/v1/auth/verify status = %d, want 200", status)
	}
	status, raw, _ := jsonPost(t, client, journey.server, "/api/v1/auth/login", `{"email":`+quoteJSON(email)+`,"password":`+quoteJSON(password)+`}`)
	if status != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/login status = %d, want 200 (body: %.200s)", status, raw)
	}
	var document map[string]string
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("login is not JSON: %v", err)
	}
	return client, document["account_id"]
}

// entitlementGet issues one authenticated GET and returns status, body and
// headers, so the tests can prove the private cache policy on every answer.
func entitlementGet(t *testing.T, client *http.Client, server *httptest.Server, path string) (int, []byte, http.Header) {
	t.Helper()

	response, err := client.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	return response.StatusCode, body, response.Header
}

// assertEntitlementPrivate requires the THR-CACHE-01 policy on a private
// read: no-store, so no intermediary keeps another person's passes.
func assertEntitlementPrivate(t *testing.T, header http.Header) {
	t.Helper()

	if !strings.Contains(header.Get("Cache-Control"), "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", header.Get("Cache-Control"))
	}
}

// grantEntitlementPasses persists one pass lot for the account under test.
func grantEntitlementPasses(t *testing.T, repository *billingpg.Repository, accountID string, origin billingdomain.PassOrigin, quantity int32, reference string, expiresAt *time.Time) {
	t.Helper()

	parsedQuantity, err := billingdomain.NewQuantity(quantity)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	parsedReference, err := billingdomain.ParseReference(reference)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if _, err := repository.GrantPassLot(context.Background(), billingapp.GrantPassLotRequest{
		AccountID: billingdomain.AccountID(accountID),
		Origin:    origin,
		Quantity:  parsedQuantity,
		Reference: parsedReference,
		ExpiresAt: expiresAt,
		GrantedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("grant %s: %v", reference, err)
	}
}

// consumeEntitlementPass spends one pass of the account under test.
func consumeEntitlementPass(t *testing.T, repository *billingpg.Repository, accountID string, arenaSuffix int) {
	t.Helper()

	arenaID, err := billingdomain.ParseArenaID(fmt.Sprintf("00000000-0000-7000-8000-%012d", arenaSuffix))
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	if _, err := repository.ConsumeArenaPass(context.Background(), billingapp.ConsumePassRequest{
		AccountID:  billingdomain.AccountID(accountID),
		ArenaID:    arenaID,
		ConsumedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("consume: %v", err)
	}
}

// mirrorEntitlementSubscription writes the webhook mirror row the
// subscription read projects: production webhooks do the same, and the read
// never reaches the provider.
func mirrorEntitlementSubscription(t *testing.T, repository *billingpg.Repository, accountID string) {
	t.Helper()

	customerID, err := billingdomain.ParseStripeCustomerID("cus_t05entitlementread")
	if err != nil {
		t.Fatalf("ParseStripeCustomerID: %v", err)
	}
	if _, err := repository.RecordStripeCustomer(context.Background(), billingapp.RecordStripeCustomerRequest{
		AccountID:  billingdomain.AccountID(accountID),
		CustomerID: customerID,
		Livemode:   false,
	}); err != nil {
		t.Fatalf("record stripe customer: %v", err)
	}
	subscriptionID, err := billingdomain.ParseStripeSubscriptionID("sub_t05entitlementread1")
	if err != nil {
		t.Fatalf("ParseStripeSubscriptionID: %v", err)
	}
	status, err := billingdomain.ParseSubscriptionStatus("active")
	if err != nil {
		t.Fatalf("ParseSubscriptionStatus: %v", err)
	}
	productID, err := billingdomain.ParseProductID("member_1")
	if err != nil {
		t.Fatalf("ParseProductID: %v", err)
	}
	priceID, err := billingdomain.ParseStripePriceID("price_1Qt05entitlement")
	if err != nil {
		t.Fatalf("ParseStripePriceID: %v", err)
	}
	periodStart := time.Now().UTC()
	periodEnd := periodStart.Add(30 * 24 * time.Hour)
	if _, err := repository.UpsertSubscription(context.Background(), billingapp.UpsertSubscriptionRequest{
		AccountID:            billingdomain.AccountID(accountID),
		StripeSubscriptionID: subscriptionID,
		Status:               status,
		Livemode:             false,
		Market:               billingdomain.MarketBrazil,
		ProductID:            productID,
		CatalogVersion:       1,
		StripePriceID:        priceID,
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
	}); err != nil {
		t.Fatalf("upsert subscription: %v", err)
	}
}

// seedEntitlementOwner grants the owner a purchase lot with one consumption
// plus a Member lot, and mirrors an active subscription for them.
func seedEntitlementOwner(t *testing.T, repository *billingpg.Repository, accountID string) {
	t.Helper()

	grantEntitlementPasses(t, repository, accountID, billingdomain.OriginPurchase, 3, "stripe:evt_t05_purchase", nil)
	consumeEntitlementPass(t, repository, accountID, 11)
	soonEnd := time.Now().UTC().Add(24 * time.Hour)
	grantEntitlementPasses(t, repository, accountID, billingdomain.OriginMember, 1, "member:2026-10", &soonEnd)
	mirrorEntitlementSubscription(t, repository, accountID)
}

// TestEntitlementPassReadsAreOwnerScoped proves the summary and the history
// are derived per account: the owner reads their own lots and consumptions,
// another account reads only their own, and nobody reads without a session.
func TestEntitlementPassReadsAreOwnerScoped(t *testing.T) {
	t.Parallel()

	journey := newEntitlementJourney(t)
	repository := billingpg.NewRepository(journey.pool)
	owner, ownerID := entitlementLogin(t, journey, "passes-owner-t05@arena.example.com", "Correct Horse 11!")
	other, otherID := entitlementLogin(t, journey, "passes-other-t05@arena.example.com", "Correct Horse 11!")
	grantEntitlementPasses(t, repository, ownerID, billingdomain.OriginPurchase, 3, "stripe:evt_t05_scoped", nil)
	consumeEntitlementPass(t, repository, ownerID, 12)
	soonEnd := time.Now().UTC().Add(24 * time.Hour)
	grantEntitlementPasses(t, repository, ownerID, billingdomain.OriginMember, 1, "member:2026-10", &soonEnd)
	grantEntitlementPasses(t, repository, otherID, billingdomain.OriginPurchase, 1, "stripe:evt_t05_other", nil)

	status, raw, header := entitlementGet(t, owner, journey.server, "/api/v1/me/passes")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/passes owner status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertEntitlementPrivate(t, header)
	var summary struct {
		AvailableTotal int64 `json:"available_total"`
		Lots           []struct {
			Origin    string `json:"origin"`
			Quantity  int32  `json:"quantity"`
			Remaining int32  `json:"remaining"`
			Expired   bool   `json:"expired"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("summary is not JSON: %v", err)
	}
	if summary.AvailableTotal != 3 {
		t.Fatalf("available_total = %d, want 3 (3 bought - 1 consumed + 1 member)", summary.AvailableTotal)
	}
	if len(summary.Lots) != 2 {
		t.Fatalf("lots = %d, want 2", len(summary.Lots))
	}

	status, raw, header = entitlementGet(t, owner, journey.server, "/api/v1/me/passes/history")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/passes/history owner status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertEntitlementPrivate(t, header)
	var history struct {
		Items []struct {
			ArenaID    string `json:"arena_id"`
			Origin     string `json:"origin"`
			Reference  string `json:"reference"`
			ConsumedAt string `json:"consumed_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatalf("history is not JSON: %v", err)
	}
	if len(history.Items) != 1 {
		t.Fatalf("history items = %d, want 1", len(history.Items))
	}
	if history.Items[0].Reference != "stripe:evt_t05_scoped" {
		t.Fatalf("history reference = %q, want the consumed lot reference", history.Items[0].Reference)
	}

	status, raw, _ = entitlementGet(t, other, journey.server, "/api/v1/me/passes")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/passes other status = %d, want 200", status)
	}
	var otherSummary struct {
		AvailableTotal int64 `json:"available_total"`
	}
	if err := json.Unmarshal(raw, &otherSummary); err != nil {
		t.Fatalf("other summary is not JSON: %v", err)
	}
	if otherSummary.AvailableTotal != 1 {
		t.Fatalf("other available_total = %d, want 1 (only their own grant)", otherSummary.AvailableTotal)
	}
	status, raw, _ = entitlementGet(t, other, journey.server, "/api/v1/me/passes/history")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/passes/history other status = %d, want 200", status)
	}
	var otherHistory struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(raw, &otherHistory); err != nil {
		t.Fatalf("other history is not JSON: %v", err)
	}
	if len(otherHistory.Items) != 0 {
		t.Fatalf("other history items = %d, want 0", len(otherHistory.Items))
	}

	anonymous := browser(t)
	for _, path := range []string{"/api/v1/me/passes", "/api/v1/me/passes/history", "/api/v1/me/billing/subscription"} {
		status, _, header := entitlementGet(t, anonymous, journey.server, path)
		if status != http.StatusUnauthorized {
			t.Fatalf("GET %s anonymous status = %d, want 401", path, status)
		}
		assertEntitlementPrivate(t, header)
	}
}

// TestEntitlementReloadGrantsNothing proves a read is derived, not minted:
// two consecutive summaries carry the same total and the stored lots do not
// grow between them.
func TestEntitlementReloadGrantsNothing(t *testing.T) {
	t.Parallel()

	journey := newEntitlementJourney(t)
	repository := billingpg.NewRepository(journey.pool)
	owner, ownerID := entitlementLogin(t, journey, "passes-reload-t05@arena.example.com", "Correct Horse 11!")
	seedEntitlementOwner(t, repository, ownerID)

	status, first, _ := entitlementGet(t, owner, journey.server, "/api/v1/me/passes")
	if status != http.StatusOK {
		t.Fatalf("first GET /api/v1/me/passes status = %d, want 200", status)
	}
	status, second, _ := entitlementGet(t, owner, journey.server, "/api/v1/me/passes")
	if status != http.StatusOK {
		t.Fatalf("second GET /api/v1/me/passes status = %d, want 200", status)
	}
	var firstSummary, secondSummary struct {
		AvailableTotal int64 `json:"available_total"`
		Lots           []any `json:"lots"`
	}
	if err := json.Unmarshal(first, &firstSummary); err != nil {
		t.Fatalf("first summary is not JSON: %v", err)
	}
	if err := json.Unmarshal(second, &secondSummary); err != nil {
		t.Fatalf("second summary is not JSON: %v", err)
	}
	if firstSummary.AvailableTotal != secondSummary.AvailableTotal || len(firstSummary.Lots) != len(secondSummary.Lots) {
		t.Fatalf("reload changed the projection: %+v vs %+v", firstSummary, secondSummary)
	}
	var lots int
	if err := journey.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app.arena_pass_lots WHERE account_id = $1::uuid`,
		ownerID).Scan(&lots); err != nil {
		t.Fatalf("count stored lots: %v", err)
	}
	if lots != 2 {
		t.Fatalf("stored lots = %d, want 2: a read must not write", lots)
	}
}

// TestEntitlementHistoryCursorIsHonest proves the history pages on a signed
// cursor: a forged one is refused, and the issued next cursor walks forward.
func TestEntitlementHistoryCursorIsHonest(t *testing.T) {
	t.Parallel()

	journey := newEntitlementJourney(t)
	repository := billingpg.NewRepository(journey.pool)
	owner, ownerID := entitlementLogin(t, journey, "passes-cursor-t05@arena.example.com", "Correct Horse 11!")
	grantEntitlementPasses(t, repository, ownerID, billingdomain.OriginPurchase, 3, "stripe:evt_t05_cursor", nil)
	consumeEntitlementPass(t, repository, ownerID, 13)
	consumeEntitlementPass(t, repository, ownerID, 14)

	status, raw, _ := entitlementGet(t, owner, journey.server, "/api/v1/me/passes/history?cursor=forged.cursor&limit=1")
	if status != http.StatusBadRequest {
		t.Fatalf("GET history with forged cursor status = %d, want 400 (body: %.300s)", status, raw)
	}
	status, raw, _ = entitlementGet(t, owner, journey.server, "/api/v1/me/passes/history?limit=1")
	if status != http.StatusOK {
		t.Fatalf("GET history limit=1 status = %d, want 200", status)
	}
	var page struct {
		Items []struct {
			ConsumptionID string `json:"consumption_id"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("history page is not JSON: %v", err)
	}
	if len(page.Items) != 1 || page.NextCursor == nil || *page.NextCursor == "" {
		t.Fatalf("history page = %.300s, want 1 item plus a next cursor", raw)
	}
	status, raw, _ = entitlementGet(t, owner, journey.server, "/api/v1/me/passes/history?cursor="+*page.NextCursor+"&limit=1")
	if status != http.StatusOK {
		t.Fatalf("GET history next page status = %d, want 200 (body: %.300s)", status, raw)
	}
}

// TestEntitlementSubscriptionProjection proves the subscription read projects
// the webhook mirror without provider identifiers: the subscribed owner sees
// lifecycle and period, another account sees an explicit absence.
func TestEntitlementSubscriptionProjection(t *testing.T) {
	t.Parallel()

	journey := newEntitlementJourney(t)
	repository := billingpg.NewRepository(journey.pool)
	owner, ownerID := entitlementLogin(t, journey, "member-owner-t05@arena.example.com", "Correct Horse 11!")
	other, _ := entitlementLogin(t, journey, "member-other-t05@arena.example.com", "Correct Horse 11!")
	mirrorEntitlementSubscription(t, repository, ownerID)

	status, raw, header := entitlementGet(t, owner, journey.server, "/api/v1/me/billing/subscription")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/billing/subscription owner status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertEntitlementPrivate(t, header)
	var projection struct {
		HasSubscription   bool    `json:"has_subscription"`
		Status            *string `json:"status"`
		Product           *string `json:"product"`
		Market            *string `json:"market"`
		CurrentPeriodEnd  *string `json:"current_period_end"`
		CancelAtPeriodEnd bool    `json:"cancel_at_period_end"`
	}
	if err := json.Unmarshal(raw, &projection); err != nil {
		t.Fatalf("subscription is not JSON: %v", err)
	}
	if !projection.HasSubscription || projection.Status == nil || *projection.Status != "active" {
		t.Fatalf("subscription projection = %.300s, want an active subscription", raw)
	}
	if projection.Product == nil || *projection.Product != "member_1" {
		t.Fatalf("subscription product = %.300s, want member_1", raw)
	}
	if projection.Market == nil || *projection.Market != "BR" {
		t.Fatalf("subscription market = %.300s, want BR", raw)
	}
	if projection.CurrentPeriodEnd == nil || *projection.CurrentPeriodEnd == "" {
		t.Fatalf("subscription period end = %.300s, want the mirrored period", raw)
	}
	for _, leaked := range []string{"sub_t05entitlementread1", "cus_", "price_1Qt05entitlement"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("subscription body leaks a provider identifier %q: %.300s", leaked, raw)
		}
	}

	status, raw, _ = entitlementGet(t, other, journey.server, "/api/v1/me/billing/subscription")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/billing/subscription other status = %d, want 200", status)
	}
	var absence struct {
		HasSubscription bool `json:"has_subscription"`
	}
	if err := json.Unmarshal(raw, &absence); err != nil {
		t.Fatalf("other subscription is not JSON: %v", err)
	}
	if absence.HasSubscription {
		t.Fatalf("other subscription = %.300s, want an explicit absence", raw)
	}
}
