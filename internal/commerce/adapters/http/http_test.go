package http_test

// P37-T07 — the staged trade API serves private receipts and extracts
// on real PostgreSQL without mounting anything on the process router.
//
// One owned formal trade resolves with its gross, tithe, net,
// escrow status, instants and compensations; buyer and provider read
// identical amounts through their own side. Missing sessions refuse
// with 401 and foreign rows with 404; every answer is private
// no-store, and the same bytes price identically in pt and en. The
// route list and the OpenAPI fragment describe each other exactly.
// The suite runs on a disposable database and activates nothing:
// the package registers no route on import and the tests mount only
// a local mux.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/http"
	commercepg "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/postgres"
	commerceapp "github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	economypg "github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	economyapp "github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

const (
	commerceOwnerToken = "commerce-owner-session-token"
	commerceOtherToken = "commerce-other-session-token"
)

type commerceHarness struct {
	mux        http.Handler
	sec        *security.Manager
	buyerID    string
	providerID string
	contractID string
}

func commerceInstant() time.Time {
	return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
}

func mustCommerceSecurity(t *testing.T) *security.Manager {
	t.Helper()
	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false,
		Clock:          clockseed.NewClock(),
		Random:         clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("setup security manager: %v", err)
	}
	return secMgr
}

func fundCommerceCitizen(t *testing.T, ctx context.Context, db *dbtest.TestDB, funds int64, citizen string) {
	t.Helper()
	pool := db.Pool.Pool()
	repo := economypg.NewRepository(pool)
	key, err := economydomain.ParseGenesisKey("commerce-receipt-funding")
	if err != nil {
		t.Fatalf("ParseGenesisKey: %v", err)
	}
	if _, err := repo.RunGenesis(ctx, economyapp.GenesisRequest{Key: key, Season: economydomain.SeasonKey(economydomain.CompatSeasonKey)}); err != nil {
		if !errors.Is(err, economydomain.ErrGenesisAlreadyExists) {
			t.Fatalf("seed Genesis: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1) ON CONFLICT DO NOTHING`, citizen); err != nil {
		t.Fatalf("create custody: %v", err)
	}
	amount, _ := economydomain.NewMilliInk(funds)
	fromKind, _ := economydomain.ParseCustodyKind("treasury")
	toKind, _ := economydomain.ParseCustodyKind("user")
	if _, err := repo.Transfer(ctx, economyapp.TransferRequest{
		FromSeason: economydomain.SeasonKey(economydomain.CompatSeasonKey),
		FromKind:   fromKind, FromLabel: "main",
		ToSeason: economydomain.SeasonKey(economydomain.CompatSeasonKey),
		ToKind:   toKind, ToLabel: citizen, Amount: amount,
	}); err != nil {
		t.Fatalf("fund citizen: %v", err)
	}
}

func commerceAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('commerce-receipt-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return id
}

func newCommerceHarness(t *testing.T) (*commerceHarness, *dbtest.TestDB) {
	t.Helper()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	buyer := commerceAccount(t, ctx, testDB)
	provider := commerceAccount(t, ctx, testDB)
	fundCommerceCitizen(t, ctx, testDB, 100000, buyer)
	if _, err := pool.Exec(ctx, `INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1) ON CONFLICT DO NOTHING`, provider); err != nil {
		t.Fatalf("create provider custody: %v", err)
	}

	escrows, err := commercepg.NewEscrowRepository(pool)
	if err != nil {
		t.Fatalf("NewEscrowRepository: %v", err)
	}
	fundUC, err := commerceapp.NewFundContractUseCase(escrows)
	if err != nil {
		t.Fatalf("NewFundContractUseCase: %v", err)
	}
	acceptUC, err := commerceapp.NewAcceptDeliveryUseCase(escrows)
	if err != nil {
		t.Fatalf("NewAcceptDeliveryUseCase: %v", err)
	}
	releaseUC, err := commerceapp.NewReleaseContractUseCase(escrows)
	if err != nil {
		t.Fatalf("NewReleaseContractUseCase: %v", err)
	}
	now := commerceInstant()
	funded, err := fundUC.Execute(ctx, commerceapp.FundCommand{
		Key: "receipt-harness-1", Object: "serviço com recibo", Buyer: buyer, Provider: provider,
		AmountMill: 20000, ExpiresAt: now.Add(time.Hour), Now: now,
	})
	if err != nil {
		t.Fatalf("fund contract: %v", err)
	}
	if _, err := acceptUC.Execute(ctx, "receipt-harness-1", buyer); err != nil {
		t.Fatalf("accept delivery: %v", err)
	}
	if _, err := releaseUC.Execute(ctx, "receipt-harness-1", buyer); err != nil {
		t.Fatalf("release contract: %v", err)
	}

	reads, err := commercepg.NewTradeReceiptRepository(pool)
	if err != nil {
		t.Fatalf("NewTradeReceiptRepository: %v", err)
	}
	secMgr := mustCommerceSecurity(t)
	handler, err := adapterhttp.NewHandler(adapterhttp.HandlerConfig{Reads: reads, Security: secMgr})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if _, err := adapterhttp.NewHandler(adapterhttp.HandlerConfig{}); err == nil {
		t.Fatal("nil reads must refuse composition")
	}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case commerceOwnerToken:
			return security.AuthIdentity{AccountID: buyer, SessionID: "session-buyer"}, nil
		case commerceOtherToken:
			return security.AuthIdentity{AccountID: provider, SessionID: "session-provider"}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})
	return &commerceHarness{
		mux: secMgr.AuthenticateMiddleware(validator)(mux), sec: secMgr,
		buyerID: buyer, providerID: provider, contractID: funded.ID,
	}, testDB
}

func commerceRequest(t *testing.T, h *commerceHarness, method, path, token, locale string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	if token != "" {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	}
	if locale != "" {
		request.Header.Set("Accept-Language", locale)
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func commerceDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, string(body))
	}
	return object
}

func assertCommerceNoStore(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
