package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// mustCheckoutAccount creates an account and drives it to the requested
// lifecycle state: verified accounts go through the real verification update,
// so the eligibility predicate is exercised against the schema's own values.
func mustCheckoutAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email, status string, verified bool) platformpg.AppAccount {
	t.Helper()
	account := mustBillingAccount(t, ctx, q, email)
	if verified {
		updated, err := q.SetEmailVerified(ctx, account.ID)
		if err != nil {
			t.Fatalf("verify account %s: %v", email, err)
		}
		account = updated
	}
	if status != "" && status != account.Status {
		updated, err := q.UpdateAccountStatus(ctx, platformpg.UpdateAccountStatusParams{ID: account.ID, Status: status})
		if err != nil {
			t.Fatalf("set account %s status: %v", email, err)
		}
		account = updated
	}
	return account
}

func assertPgViolation(t *testing.T, err error, wantCode, wantConstraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want a PostgreSQL error", err)
	}
	if pgErr.Code != wantCode {
		t.Fatalf("sqlstate = %s, want %s (%v)", pgErr.Code, wantCode, err)
	}
	if wantConstraint != "" && pgErr.ConstraintName != wantConstraint {
		t.Fatalf("constraint = %q, want %q (%v)", pgErr.ConstraintName, wantConstraint, err)
	}
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, accountID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE account_id = $1", accountID).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func mustCheckoutIntentRequest(t *testing.T, accountID domain.AccountID, sessionID string, livemode bool, status domain.CheckoutIntentStatus, closedAt *time.Time) application.RecordCheckoutIntentRequest {
	t.Helper()
	market, err := domain.ParseMarket("BR")
	if err != nil {
		t.Fatalf("ParseMarket: %v", err)
	}
	productID, err := domain.ParseProductID("ink_10000")
	if err != nil {
		t.Fatalf("ParseProductID: %v", err)
	}
	amount, err := domain.NewMoney(990, domain.CurrencyBRL)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	return application.RecordCheckoutIntentRequest{
		AccountID:      accountID,
		Market:         market,
		ProductID:      productID,
		CatalogVersion: 7,
		Amount:         amount,
		Livemode:       livemode,
		SessionID:      domain.StripeCheckoutSessionID(sessionID),
		Status:         status,
		ClosedAt:       closedAt,
	}
}

// TestRepository_PurchaserForCheckoutEligibility drives the eligibility port
// against real account lifecycles: only an active account with a verified email
// may purchase, and an unknown account is reported as unknown.
func TestRepository_PurchaserForCheckoutEligibility(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	eligible := mustCheckoutAccount(t, ctx, q, "checkout-verified@arena.example.com", "active", true)
	suspended := mustCheckoutAccount(t, ctx, q, "checkout-suspended@arena.example.com", "suspended", true)
	unverified := mustCheckoutAccount(t, ctx, q, "checkout-unverified@arena.example.com", "active", false)
	pending := mustCheckoutAccount(t, ctx, q, "checkout-pending@arena.example.com", "pending", false)
	deleted := mustCheckoutAccount(t, ctx, q, "checkout-deleted@arena.example.com", "deleted", true)

	cases := []struct {
		name string
		id   domain.AccountID
		want bool
	}{
		{name: "active and verified", id: domain.AccountID(uuidString(eligible.ID)), want: true},
		{name: "suspended", id: domain.AccountID(uuidString(suspended.ID)), want: false},
		{name: "unverified email", id: domain.AccountID(uuidString(unverified.ID)), want: false},
		{name: "pending", id: domain.AccountID(uuidString(pending.ID)), want: false},
		{name: "deleted but verified", id: domain.AccountID(uuidString(deleted.ID)), want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			purchaser, err := repo.PurchaserForCheckout(ctx, testCase.id)
			if err != nil {
				t.Fatalf("PurchaserForCheckout error = %v", err)
			}
			if purchaser.Eligible != testCase.want {
				t.Fatalf("eligible = %v, want %v", purchaser.Eligible, testCase.want)
			}
			if purchaser.AccountID != testCase.id {
				t.Fatalf("account = %q", purchaser.AccountID)
			}
		})
	}

	_, err := repo.PurchaserForCheckout(ctx, domain.AccountID("018f6b2a-0000-7000-8000-0000000000aa"))
	if !errors.Is(err, application.ErrPurchaserNotFound) {
		t.Fatalf("unknown account error = %v, want ErrPurchaserNotFound", err)
	}

	if _, err := repo.PurchaserForCheckout(ctx, domain.AccountID("not-a-uuid")); err == nil {
		t.Fatal("a malformed account identifier must be refused")
	}
}

// TestRepository_StripeCustomerMappingIsWrittenOnce covers the account→provider
// customer correlation: the first mapping wins, a retry resolves it, and the
// account never ends up with two provider customers.
func TestRepository_StripeCustomerMappingIsWrittenOnce(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	account := mustBillingAccount(t, ctx, q, "checkout-customer@arena.example.com")
	accountID := domain.AccountID(uuidString(account.ID))

	stored, err := repo.StripeCustomer(ctx, accountID)
	if err != nil {
		t.Fatalf("StripeCustomer error = %v", err)
	}
	if stored != nil {
		t.Fatal("an account without a mapping must resolve to nil")
	}

	first, err := repo.RecordStripeCustomer(ctx, application.RecordStripeCustomerRequest{
		AccountID:  accountID,
		CustomerID: domain.StripeCustomerID("cus_first"),
		Livemode:   false,
	})
	if err != nil {
		t.Fatalf("RecordStripeCustomer error = %v", err)
	}
	if first.CustomerID.String() != "cus_first" || first.Livemode {
		t.Fatalf("stored mapping = %+v", first)
	}
	if first.CreatedAt.Location() != time.UTC {
		t.Fatalf("created at = %v, want UTC", first.CreatedAt)
	}

	replayed, err := repo.RecordStripeCustomer(ctx, application.RecordStripeCustomerRequest{
		AccountID:  accountID,
		CustomerID: domain.StripeCustomerID("cus_second"),
		Livemode:   false,
	})
	if err != nil {
		t.Fatalf("replayed RecordStripeCustomer error = %v", err)
	}
	if replayed.CustomerID.String() != "cus_first" {
		t.Fatalf("replay resolved %q, want the stored mapping", replayed.CustomerID)
	}
	if got := countRows(t, ctx, pool, "app.stripe_customers", accountID.String()); got != 1 {
		t.Fatalf("stored mappings = %d, want exactly 1", got)
	}

	loaded, err := repo.StripeCustomer(ctx, accountID)
	if err != nil {
		t.Fatalf("StripeCustomer error = %v", err)
	}
	if loaded == nil || loaded.CustomerID.String() != "cus_first" {
		t.Fatalf("loaded mapping = %+v", loaded)
	}
}

// TestRepository_RecordCheckoutIntentIsIdempotentPerSession is the replay proof
// of the checkout persistence: one row per provider session, the stored
// decision returned on a retry, and a session never disclosed to another
// account.
func TestRepository_RecordCheckoutIntentIsIdempotentPerSession(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	account := mustBillingAccount(t, ctx, q, "checkout-intent@arena.example.com")
	accountID := domain.AccountID(uuidString(account.ID))
	other := mustBillingAccount(t, ctx, q, "checkout-intent-other@arena.example.com")
	otherID := domain.AccountID(uuidString(other.ID))

	if _, err := repo.RecordStripeCustomer(ctx, application.RecordStripeCustomerRequest{
		AccountID:  accountID,
		CustomerID: domain.StripeCustomerID("cus_intent"),
		Livemode:   false,
	}); err != nil {
		t.Fatalf("record customer: %v", err)
	}
	if _, err := repo.RecordStripeCustomer(ctx, application.RecordStripeCustomerRequest{
		AccountID:  otherID,
		CustomerID: domain.StripeCustomerID("cus_intentother"),
		Livemode:   false,
	}); err != nil {
		t.Fatalf("record other customer: %v", err)
	}

	recorded, err := repo.RecordCheckoutIntent(ctx, mustCheckoutIntentRequest(t, accountID, "cs_test_intent1", false, domain.CheckoutIntentOpen, nil))
	if err != nil {
		t.Fatalf("RecordCheckoutIntent error = %v", err)
	}
	if recorded.Replayed {
		t.Fatal("the first insertion is not a replay")
	}
	intent := recorded.Intent
	switch {
	case intent.ID == "":
		t.Fatal("a stored intent must carry its identifier")
	case intent.AccountID != accountID:
		t.Fatalf("account = %q", intent.AccountID)
	case intent.Market != domain.MarketBrazil || intent.ProductID.String() != "ink_10000":
		t.Fatalf("decision = %+v", intent)
	case intent.CatalogVersion != 7 || intent.Amount.MinorUnits() != 990 || intent.Amount.Currency() != domain.CurrencyBRL:
		t.Fatalf("decision = %+v", intent)
	case intent.Status != domain.CheckoutIntentOpen || intent.Livemode:
		t.Fatalf("status = %s livemode = %v", intent.Status, intent.Livemode)
	case intent.SessionID.String() != "cs_test_intent1":
		t.Fatalf("session = %q", intent.SessionID)
	}

	// A retry of the same operation resolves the stored row.
	replayed, err := repo.RecordCheckoutIntent(ctx, mustCheckoutIntentRequest(t, accountID, "cs_test_intent1", false, domain.CheckoutIntentOpen, nil))
	if err != nil {
		t.Fatalf("replayed RecordCheckoutIntent error = %v", err)
	}
	if !replayed.Replayed || replayed.Intent.ID != intent.ID {
		t.Fatalf("replay = %+v, want the stored intent", replayed)
	}
	if got := countRows(t, ctx, pool, "app.checkout_intents", accountID.String()); got != 1 {
		t.Fatalf("stored intents = %d, want exactly 1", got)
	}

	// Another session of the same account is a distinct intent.
	second, err := repo.RecordCheckoutIntent(ctx, mustCheckoutIntentRequest(t, accountID, "cs_test_intent2", false, domain.CheckoutIntentOpen, nil))
	if err != nil {
		t.Fatalf("second RecordCheckoutIntent error = %v", err)
	}
	if second.Replayed || second.Intent.ID == intent.ID {
		t.Fatalf("second intent = %+v", second)
	}

	// The same session claimed by another account is refused, never disclosed.
	if _, err := repo.RecordCheckoutIntent(ctx, mustCheckoutIntentRequest(t, otherID, "cs_test_intent1", false, domain.CheckoutIntentOpen, nil)); err == nil {
		t.Fatal("a session already recorded for another account must be refused")
	}
	if got := countRows(t, ctx, pool, "app.checkout_intents", otherID.String()); got != 0 {
		t.Fatalf("intents recorded for the other account = %d, want 0", got)
	}
}

// TestRepository_RecordCheckoutIntentLifecycle covers the state the checkout
// writes and the states it must never write: an expired intent is closed at a
// known instant, an open one is not, and the provider mode of a session can
// never contradict the recorded mode.
func TestRepository_RecordCheckoutIntentLifecycle(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	account := mustBillingAccount(t, ctx, q, "checkout-lifecycle@arena.example.com")
	accountID := domain.AccountID(uuidString(account.ID))
	if _, err := repo.RecordStripeCustomer(ctx, application.RecordStripeCustomerRequest{
		AccountID:  accountID,
		CustomerID: domain.StripeCustomerID("cus_lifecycle"),
		Livemode:   false,
	}); err != nil {
		t.Fatalf("record customer: %v", err)
	}

	closedAt := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	expired, err := repo.RecordCheckoutIntent(ctx, mustCheckoutIntentRequest(t, accountID, "cs_test_expired", false, domain.CheckoutIntentExpired, &closedAt))
	if err != nil {
		t.Fatalf("expired intent error = %v", err)
	}
	if expired.Intent.Status != domain.CheckoutIntentExpired {
		t.Fatalf("status = %s", expired.Intent.Status)
	}
	var storedClosedAt *time.Time
	if err := pool.QueryRow(ctx,
		"SELECT closed_at FROM app.checkout_intents WHERE stripe_checkout_session_id = $1", "cs_test_expired").Scan(&storedClosedAt); err != nil {
		t.Fatalf("read closed_at: %v", err)
	}
	if storedClosedAt == nil || !storedClosedAt.Equal(closedAt) {
		t.Fatalf("closed_at = %v, want %v", storedClosedAt, closedAt)
	}
	// An open intent of the same account leaves the closing instant unset.
	if _, err := repo.RecordCheckoutIntent(ctx, mustCheckoutIntentRequest(t, accountID, "cs_test_stillopen", false, domain.CheckoutIntentOpen, nil)); err != nil {
		t.Fatalf("open intent error = %v", err)
	}
	var openClosedAt *time.Time
	if err := pool.QueryRow(ctx,
		"SELECT closed_at FROM app.checkout_intents WHERE stripe_checkout_session_id = $1", "cs_test_stillopen").Scan(&openClosedAt); err != nil {
		t.Fatalf("read open closed_at: %v", err)
	}
	if openClosedAt != nil {
		t.Fatalf("an open intent must not carry a closing instant, got %v", openClosedAt)
	}

	// The adapter refuses the incoherent combinations before the database has
	// to, and the database refuses the provider mode contradiction itself.
	if _, err := repo.RecordCheckoutIntent(ctx, mustCheckoutIntentRequest(t, accountID, "cs_test_openclosed", false, domain.CheckoutIntentOpen, &closedAt)); err == nil {
		t.Fatal("an open intent must not be closed")
	}
	if _, err := repo.RecordCheckoutIntent(ctx, mustCheckoutIntentRequest(t, accountID, "cs_test_expiredopen", false, domain.CheckoutIntentExpired, nil)); err == nil {
		t.Fatal("an expired intent must be closed")
	}
	if _, err := repo.RecordCheckoutIntent(ctx, mustCheckoutIntentRequest(t, accountID, "cs_test_created", false, domain.CheckoutIntentCreated, nil)); !errors.Is(err, domain.ErrInvalidCheckoutIntentStatus) {
		t.Fatalf("created status error = %v", err)
	}

	_, err = repo.RecordCheckoutIntent(ctx, mustCheckoutIntentRequest(t, accountID, "cs_live_wrongmode", false, domain.CheckoutIntentOpen, nil))
	assertPgViolation(t, err, "23514", "checkout_intents_session_mode_check")
}
