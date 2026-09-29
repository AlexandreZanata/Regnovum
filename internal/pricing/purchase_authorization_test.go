package pricing_test

// P35-T09 — the purchase authorization matrix is closed on real
// PostgreSQL, and no purchase route serves.
//
// Every purchase port scopes by account: one account accepts,
// settles and disputes through its own keys only, the same key in
// another account opens an isolated intent that never touches the
// first, and unknown keys resolve to absence instead of a guess. The
// product surface carries no purchase route either: the registry and
// the published contract hold no INK purchase mark. The tests prove
// the matrix cell by cell on a disposable database.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	_ "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	_ "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/search/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/http"

	billpostgres "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	billapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
)

// matrixWorld wires two buyers with one funded vault and one live
// quote: the ground every authorization cell plays on.
type matrixWorld struct {
	clock *advClock
	anna  string
	beto  string
	quote string
}

func newMatrixWorld(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *matrixWorld {
	t.Helper()
	clock := &advClock{now: advInstant().Add(3 * time.Second)}
	advFund(t, ctx, pool, 600000)
	quote := advQuote(t, ctx, pool, clock, 5*time.Minute)
	return &matrixWorld{
		clock: clock,
		anna:  advAccount(t, ctx, pool, "matrix-anna-"),
		beto:  advAccount(t, ctx, pool, "matrix-beto-"),
		quote: quote,
	}
}

func matrixAccept(t *testing.T, ctx context.Context, pool *pgxpool.Pool, world *matrixWorld, key, account string) (string, error) {
	t.Helper()
	repo, err := billpostgres.NewPurchaseIntentRepository(pool, world.clock, advSchedule())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	uc, err := billapp.NewAcceptPurchaseUseCase(repo)
	if err != nil {
		t.Fatalf("use case: %v", err)
	}
	result, err := uc.Execute(ctx, billapp.AcceptPurchaseCommand{
		IntentKey: key, AccountID: account, QuoteID: world.quote, FiatMinor: 10000,
	})
	if err != nil {
		return "", err
	}
	return result.IntentID, nil
}

func matrixSettle(t *testing.T, ctx context.Context, pool *pgxpool.Pool, world *matrixWorld, key, account, event string) (*billapp.SettlePurchaseResult, error) {
	t.Helper()
	return advSettle(t, ctx, pool, world.clock,
		advEventBody(key, account, 10000, "BRL", "paid", event), world.clock.now, settleSecretForTest())
}

func matrixDispute(t *testing.T, ctx context.Context, pool *pgxpool.Pool, world *matrixWorld, key, account, event string) (*billapp.SettleChargebackResult, error) {
	t.Helper()
	verifier, err := stripe.NewWebhookVerifier(settleSecretForTest(), 5*time.Minute, world.clock)
	if err != nil {
		t.Fatalf("NewWebhookVerifier: %v", err)
	}
	repo, err := billpostgres.NewChargebackRepository(pool, world.clock, verifier)
	if err != nil {
		t.Fatalf("NewChargebackRepository: %v", err)
	}
	uc, err := billapp.NewSettleChargebackUseCase(repo)
	if err != nil {
		t.Fatalf("NewSettleChargebackUseCase: %v", err)
	}
	body := advEventBody(key, account, 10000, "BRL", "disputed", event)
	sig, ts := advSign(settleSecretForTest(), world.clock.now, body)
	return uc.Execute(ctx, billapp.SettleChargebackCommand{Payload: body, SignatureHeader: sig, TimestampHeader: ts})
}

// TestPurchaseAuthorizationMatrix proves the closed cells: own keys
// settle and dispute, foreign keys resolve to absence, and one key
// in two accounts opens two isolated intents that never meet.
func TestPurchaseAuthorizationMatrix(t *testing.T) {
	t.Run("accept own key", func(t *testing.T) {
		testDB := dbtest.New(t)
		pool := testDB.Pool.Pool()
		ctx, cancel := advCtx()
		defer cancel()
		world := newMatrixWorld(t, ctx, pool)
		if _, err := matrixAccept(t, ctx, pool, world, "matrix-1", world.anna); err != nil {
			t.Fatalf("own acceptance refused: %v", err)
		}
	})

	t.Run("same key isolates accounts", func(t *testing.T) {
		testDB := dbtest.New(t)
		pool := testDB.Pool.Pool()
		ctx, cancel := advCtx()
		defer cancel()
		world := newMatrixWorld(t, ctx, pool)
		annaIntent, err := matrixAccept(t, ctx, pool, world, "shared-key", world.anna)
		if err != nil {
			t.Fatalf("anna acceptance: %v", err)
		}
		betoIntent, err := matrixAccept(t, ctx, pool, world, "shared-key", world.beto)
		if err != nil {
			t.Fatalf("beto acceptance: %v", err)
		}
		if annaIntent == betoIntent {
			t.Fatalf("shared key collapsed two accounts into one intent")
		}
		var owner string
		if err := pool.QueryRow(ctx,
			`SELECT account_id::text FROM app.billing_ink_intents WHERE id::text = $1`, betoIntent).Scan(&owner); err != nil {
			t.Fatalf("read beto intent: %v", err)
		}
		if owner != world.beto {
			t.Fatalf("beto intent owned by %q", owner)
		}
	})

	t.Run("settle stranger key", func(t *testing.T) {
		testDB := dbtest.New(t)
		pool := testDB.Pool.Pool()
		ctx, cancel := advCtx()
		defer cancel()
		world := newMatrixWorld(t, ctx, pool)
		if _, err := matrixAccept(t, ctx, pool, world, "anna-key", world.anna); err != nil {
			t.Fatalf("anna acceptance: %v", err)
		}
		if _, err := matrixSettle(t, ctx, pool, world, "anna-key", world.beto, "evt-x"); !errors.Is(err, billapp.ErrPurchaseIntentNotFound) {
			t.Fatalf("stranger settlement = %v, want ErrPurchaseIntentNotFound", err)
		}
		if got := advBalance(t, ctx, pool, "user", world.beto); got != 0 {
			t.Fatalf("stranger credited %d", got)
		}
	})

	t.Run("settle own key", func(t *testing.T) {
		testDB := dbtest.New(t)
		pool := testDB.Pool.Pool()
		ctx, cancel := advCtx()
		defer cancel()
		world := newMatrixWorld(t, ctx, pool)
		if _, err := matrixAccept(t, ctx, pool, world, "anna-key", world.anna); err != nil {
			t.Fatalf("anna acceptance: %v", err)
		}
		result, err := matrixSettle(t, ctx, pool, world, "anna-key", world.anna, "evt-1")
		if err != nil {
			t.Fatalf("own settlement refused: %v", err)
		}
		if result.InkMilli != 253833 {
			t.Fatalf("settlement changed: %+v", result)
		}
	})

	t.Run("dispute stranger key", func(t *testing.T) {
		testDB := dbtest.New(t)
		pool := testDB.Pool.Pool()
		ctx, cancel := advCtx()
		defer cancel()
		world := newMatrixWorld(t, ctx, pool)
		if _, err := matrixAccept(t, ctx, pool, world, "anna-key", world.anna); err != nil {
			t.Fatalf("anna acceptance: %v", err)
		}
		if _, err := matrixSettle(t, ctx, pool, world, "anna-key", world.anna, "evt-1"); err != nil {
			t.Fatalf("anna settlement: %v", err)
		}
		if _, err := matrixDispute(t, ctx, pool, world, "anna-key", world.beto, "dp-x"); !errors.Is(err, billapp.ErrPurchaseIntentNotFound) {
			t.Fatalf("stranger dispute = %v, want ErrPurchaseIntentNotFound", err)
		}
		if got := advCount(t, ctx, pool, "app.billing_ink_chargebacks"); got != 0 {
			t.Fatalf("stranger dispute recorded %d chargebacks", got)
		}
	})

	t.Run("dispute own key", func(t *testing.T) {
		testDB := dbtest.New(t)
		pool := testDB.Pool.Pool()
		ctx, cancel := advCtx()
		defer cancel()
		world := newMatrixWorld(t, ctx, pool)
		if _, err := matrixAccept(t, ctx, pool, world, "anna-key", world.anna); err != nil {
			t.Fatalf("anna acceptance: %v", err)
		}
		if _, err := matrixSettle(t, ctx, pool, world, "anna-key", world.anna, "evt-1"); err != nil {
			t.Fatalf("anna settlement: %v", err)
		}
		result, err := matrixDispute(t, ctx, pool, world, "anna-key", world.anna, "dp-1")
		if err != nil {
			t.Fatalf("own dispute refused: %v", err)
		}
		if result.InkRevoked != 253833 {
			t.Fatalf("dispute changed: %+v", result)
		}
	})
}

// purchaseRouteMarks are substrings no served route or contract path
// may carry: an INK purchase surface would turn a disabled product
// into a live one. Legacy billing and wallet routes stay out of this
// list on purpose: they are the vigente MVP, not the pending
// purchase.
var purchaseRouteMarks = []string{
	"ink", "pricing", "quote", "purchase-intent", "settle-purchase",
	"chargeback", "commercial", "oracle", "treasury", "milliink",
	"genesis", "economy",
}

// TestNoPurchaseRouteServes proves the disabled product stays
// disabled: the route registry and the published contract hold none
// of its marks.
func TestNoPurchaseRouteServes(t *testing.T) {
	t.Parallel()

	for _, route := range httpserver.RegisteredRoutes() {
		lowered := strings.ToLower(route.String())
		for _, mark := range purchaseRouteMarks {
			if strings.Contains(lowered, mark) {
				t.Errorf("registered route %q carries %q: a purchase entrypoint", route.String(), mark)
			}
		}
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	raw, err := os.ReadFile(filepath.Join(root, "api", "openapi.json"))
	if err != nil {
		t.Fatalf("read openapi.json: %v", err)
	}
	var contract struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("decode openapi.json: %v", err)
	}
	for path := range contract.Paths {
		lowered := strings.ToLower(path)
		for _, mark := range purchaseRouteMarks {
			if strings.Contains(lowered, mark) {
				t.Errorf("contract path %q carries %q: a purchase entrypoint", path, mark)
			}
		}
	}
}
