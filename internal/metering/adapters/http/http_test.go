package http_test

// P36-T08 — the staged metering API serves preview, confirmation,
// receipt and extract on real PostgreSQL without mounting anything
// on the process router.
//
// Cost preview prices without storing, confirmation settles the
// exact quoted charge, receipts bind legs and compensations and the
// extract lists owned lines beside the journal balance. Missing
// sessions refuse with 401, missing CSRF with 403, divergent keys
// with 409 and foreign rows with 404; every answer is private
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

	economypg "github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	economyapp "github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/metering/adapters/http"
	meteringpg "github.com/AlexandreZanata/Regnovum/internal/metering/adapters/postgres"
	meteringapp "github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

const (
	meteringOwnerToken = "metering-owner-session-token"
	meteringOtherToken = "metering-other-session-token"
)

type meteringClock struct{ now time.Time }

func (c meteringClock) Now() time.Time { return c.now }

type meteringHarness struct {
	mux     http.Handler
	sec     *security.Manager
	ownerID string
	otherID string
	now     time.Time
}

func meteringInstant() time.Time {
	return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
}

func mustMeteringSecurity(t *testing.T) *security.Manager {
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

func meteringCatalog(t *testing.T, at time.Time) meteringdomain.Catalog {
	t.Helper()
	service, _ := meteringdomain.ParseServiceID("argument-publish")
	price, err := meteringdomain.NewPriceEntry(meteringdomain.PriceRequest{
		Service: service, Version: 2,
		ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour),
		PriceMilli: 250, Unit: meteringdomain.UnitGraphemeCluster, Authority: "test-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry: %v", err)
	}
	var catalog meteringdomain.Catalog
	if err := catalog.Add(price); err != nil {
		t.Fatalf("Add price: %v", err)
	}
	return catalog
}

func fundMeteringCitizens(t *testing.T, ctx context.Context, db *dbtest.TestDB, funds int64, citizens ...string) {
	t.Helper()
	pool := db.Pool.Pool()
	repo := economypg.NewRepository(pool)
	key, err := economydomain.ParseGenesisKey("metering-http-funding")
	if err != nil {
		t.Fatalf("ParseGenesisKey: %v", err)
	}
	if _, err := repo.RunGenesis(ctx, economyapp.GenesisRequest{Key: key}); err != nil {
		t.Fatalf("seed Genesis: %v", err)
	}
	fromKind, _ := economydomain.ParseCustodyKind("treasury")
	toKind, _ := economydomain.ParseCustodyKind("user")
	amount, _ := economydomain.NewMilliInk(funds)
	for _, citizen := range citizens {
		if _, err := pool.Exec(ctx, `INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1)`, citizen); err != nil {
			t.Fatalf("create citizen custody: %v", err)
		}
		if _, err := repo.Transfer(ctx, economyapp.TransferRequest{
			FromKind: fromKind, FromLabel: "main", ToKind: toKind, ToLabel: citizen, Amount: amount,
		}); err != nil {
			t.Fatalf("fund citizen: %v", err)
		}
	}
}

func newMeteringHarness(t *testing.T) (*meteringHarness, *dbtest.TestDB) {
	t.Helper()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ownerID := "metering-owner-" + t.Name()
	otherID := "metering-other-" + t.Name()
	fundMeteringCitizens(t, ctx, testDB, 1000000, ownerID, otherID)

	at := meteringInstant()
	now := at.Add(time.Minute)
	catalog := meteringCatalog(t, at)
	clock := meteringClock{now: now}
	previewUC, err := meteringapp.NewPreviewUseCase(catalog, text.GraphemeCount)
	if err != nil {
		t.Fatalf("NewPreviewUseCase: %v", err)
	}
	publishRepo, err := meteringpg.NewRepository(pool, clock, catalog)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	publishUC, err := meteringapp.NewPublishUseCase(publishRepo)
	if err != nil {
		t.Fatalf("NewPublishUseCase: %v", err)
	}
	secMgr := mustMeteringSecurity(t)
	handler, err := adapterhttp.NewHandler(adapterhttp.HandlerConfig{
		Preview: previewUC, Publish: publishUC, Reads: publishRepo,
		Clock: clock, Security: secMgr,
		FromKind: "user", ToKind: "treasury", ToLabel: "main",
		MaxUnits: 3000, TTL: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case meteringOwnerToken:
			return security.AuthIdentity{AccountID: ownerID, SessionID: "session-owner"}, nil
		case meteringOtherToken:
			return security.AuthIdentity{AccountID: otherID, SessionID: "session-other"}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})
	return &meteringHarness{
		mux: secMgr.AuthenticateMiddleware(validator)(mux), sec: secMgr,
		ownerID: ownerID, otherID: otherID, now: now,
	}, testDB
}

func meteringRequest(t *testing.T, h *meteringHarness, method, path, token, body, locale string) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	}
	if locale != "" {
		request.Header.Set("Accept-Language", locale)
	}
	if method == http.MethodPost {
		csrf, err := h.sec.CSRF().GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		request.AddCookie(&http.Cookie{Name: "arena_csrf", Value: csrf})
		request.Header.Set("X-CSRF-Token", csrf)
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func meteringDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, string(body))
	}
	return object
}

func assertNoStore(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func serveBareRequest(t *testing.T, h *meteringHarness, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	request.AddCookie(&http.Cookie{Name: "arena_session", Value: meteringOwnerToken})
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}
