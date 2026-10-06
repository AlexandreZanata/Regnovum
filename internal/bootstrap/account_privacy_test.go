// Tests of the account privacy surface composed in the process (P49-T02):
// the public profile, the owner's private reads, the personal export and
// the deletion workflow run on the platform mux over disposable PostgreSQL,
// driven by real HTTP with the same pool and security boundary as the
// account journey — the session the JSON login opened is the session these
// routes require, and no second authentication exists.
package bootstrap_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/exportjson"
	profilespostgres "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	profilesdomain "github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
	walletpostgres "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// privacyJourney is the account plus privacy surfaces on a real listener,
// sharing one pool and one security boundary like `arena server` does.
type privacyJourney struct {
	account *bootstrap.AccountSurface
	privacy *bootstrap.PrivacySurface
	server  *httptest.Server
	pool    *pgxpool.Pool
}

// newPrivacyJourney composes both surfaces the way the process does.
func newPrivacyJourney(t *testing.T) *privacyJourney {
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
	privacy, err := bootstrap.ComposeAccountPrivacy(bootstrap.Options{
		Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool: pool, Clock: clock, Random: random, Assets: manifestFixture(t), Security: manager,
		CursorSecret: []byte(cursorSecret),
	})
	if err != nil {
		t.Fatalf("ComposeAccountPrivacy() error = %v", err)
	}
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clock)
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{},
		[]httpserver.Surface{account.Surface(), privacy.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &privacyJourney{account: account, privacy: privacy, server: server, pool: pool}
}

// privacyLogin registers, verifies and signs in one account over the JSON
// API, returning the client holding its session plus the account identifier.
func privacyLogin(t *testing.T, journey *privacyJourney, email, password string) (*http.Client, string) {
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

// quoteJSON quotes one JSON string.
func quoteJSON(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// mustCreateProfile persists the verified account's profile.
func mustCreateProfile(t *testing.T, journey *privacyJourney, accountID, username string) {
	t.Helper()

	repository := profilespostgres.NewRepository(journey.pool)
	created, err := profilesapp.NewCreateProfileUseCase(
		repository, repository, profilesdomain.DefaultUsernamePolicy(), clockseed.NewClock(),
	).Execute(context.Background(), profilesapp.CreateProfileCommand{
		AccountID: accountID, Username: username, Locale: "pt-BR",
	})
	if err != nil {
		t.Fatalf("CreateProfile(%q) error = %v", username, err)
	}
	if created == nil {
		t.Fatalf("CreateProfile(%q) answered nothing", username)
	}
}

// mustCreditINK funds one account through the same path a free cycle takes.
func mustCreditINK(t *testing.T, journey *privacyJourney, accountID string, amount int64, key string) {
	t.Helper()

	_, err := walletapp.NewCreditInkUseCase(
		walletpostgres.NewRepository(journey.pool), clockseed.NewClock(),
	).Execute(context.Background(), walletapp.CreditInkCommand{
		AccountID:      accountID,
		Bucket:         string(walletdomain.BucketFree),
		OperationType:  walletdomain.OperationCreditFree.String(),
		Amount:         amount,
		Reference:      "privacy-journey-credit",
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("CreditInk(%s) error = %v", accountID, err)
	}
}

// getHeaders reads one GET and returns its status, body and headers.
func getHeaders(t *testing.T, client *http.Client, server *httptest.Server, path string) (int, []byte, http.Header) {
	t.Helper()

	response, err := client.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	return response.StatusCode, raw, response.Header
}

// TestPrivacyProfilesSeparatePublicFromPrivate proves the boundary: the
// public document serves anonymously without private fields, the private
// one serves only its owner with no-store, and another owner reads only
// their own.
func TestPrivacyProfilesSeparatePublicFromPrivate(t *testing.T) {
	t.Parallel()

	journey := newPrivacyJourney(t)
	owner, _ := privacyLogin(t, journey, "privacy-owner@example.test", "correct horse battery staple")
	other, _ := privacyLogin(t, journey, "privacy-other@example.test", "correct horse battery staple")
	anonymous := browser(t)

	ownerID := accountIDOf(t, journey, "privacy-owner@example.test")
	otherID := accountIDOf(t, journey, "privacy-other@example.test")
	mustCreateProfile(t, journey, ownerID, "PrivacyOwner")
	mustCreateProfile(t, journey, otherID, "PrivacyOther")

	status, raw := jsonGet(t, anonymous, journey.server, "/api/v1/profiles/privacyowner")
	if status != http.StatusOK {
		t.Fatalf("GET public profile status = %d, want 200 (body: %.200s)", status, raw)
	}
	if strings.Contains(string(raw), "privacy-owner@example.test") {
		t.Error("public profile leaks the email address")
	}

	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/me/profile"); status != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/me/profile anonymous status = %d, want 401", status)
	}

	status, raw, header := getHeaders(t, owner, journey.server, "/api/v1/me/profile")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/profile owner status = %d, want 200 (body: %.200s)", status, raw)
	}
	assertPrivateNoStore(t, header)
	if !strings.Contains(string(raw), "PrivacyOwner") {
		t.Errorf("owner private profile body = %.200s, want their username", raw)
	}

	status, raw = jsonGet(t, other, journey.server, "/api/v1/me/profile")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/profile other status = %d, want 200", status)
	}
	if strings.Contains(string(raw), "PrivacyOwner") || !strings.Contains(string(raw), "PrivacyOther") {
		t.Errorf("other owner reads %.200s, want only their own profile", raw)
	}

	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/profiles/no-such-handle"); status != http.StatusNotFound {
		t.Errorf("GET unknown public profile status = %d, want 404", status)
	}
}

// accountIDOf reads back the identifier the database assigned to one email.
func accountIDOf(t *testing.T, journey *privacyJourney, email string) string {
	t.Helper()

	address, err := identitydomain.ParseEmail(email)
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	stored, err := identitypostgres.NewRepository(journey.pool).GetAccountByEmail(context.Background(), address)
	if err != nil {
		t.Fatalf("GetAccountByEmail(%q) error = %v", email, err)
	}
	return stored.ID().String()
}

// TestPrivacyWalletReadsAreOwnerScoped proves the legacy ledger reads: the
// owner sees their funded balance and statement with no-store, another
// account sees only their own empty balance, a forged cursor is refused,
// and anonymous calls fail closed.
func TestPrivacyWalletReadsAreOwnerScoped(t *testing.T) {
	t.Parallel()

	journey := newPrivacyJourney(t)
	owner, _ := privacyLogin(t, journey, "privacy-wallet@example.test", "correct horse battery staple")
	other, _ := privacyLogin(t, journey, "privacy-wallet-other@example.test", "correct horse battery staple")
	anonymous := browser(t)

	mustCreditINK(t, journey, accountIDOf(t, journey, "privacy-wallet@example.test"), 500, "privacy-wallet-credit-1")

	status, raw, header := getHeaders(t, owner, journey.server, "/api/v1/me/wallet")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/wallet owner status = %d, want 200 (body: %.200s)", status, raw)
	}
	assertPrivateNoStore(t, header)
	var balance map[string]any
	if err := json.Unmarshal(raw, &balance); err != nil {
		t.Fatalf("wallet balance is not JSON: %v", err)
	}

	status, raw = jsonGet(t, owner, journey.server, "/api/v1/me/wallet/transactions?limit=10")
	if status != http.StatusOK {
		t.Fatalf("GET statement owner status = %d, want 200 (body: %.200s)", status, raw)
	}
	if !strings.Contains(string(raw), "privacy-journey-credit") {
		t.Errorf("statement body = %.300s, want the funding reference", raw)
	}

	status, raw = jsonGet(t, other, journey.server, "/api/v1/me/wallet")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/wallet other status = %d, want 200", status)
	}
	if strings.Contains(string(raw), "500") {
		t.Errorf("other owner reads %.200s, want only their own balance", raw)
	}

	if status, _ := jsonGet(t, owner, journey.server, "/api/v1/me/wallet/transactions?cursor=forged"); status != http.StatusBadRequest {
		t.Errorf("GET statement with forged cursor status = %d, want 400", status)
	}
	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/me/wallet"); status != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/me/wallet anonymous status = %d, want 401", status)
	}
	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/me/wallet/transactions"); status != http.StatusUnauthorized {
		t.Errorf("GET statement anonymous status = %d, want 401", status)
	}
}

// TestPrivacyExportRequestReplayAndDownload proves the durable export: the
// request is accepted with a single-use token, a replay resolves the same
// export without duplicating work, the worker generation answers Replayed
// on a second run, the download serves once, and foreign or unknown
// exports answer not-found without an oracle.
func TestPrivacyExportRequestReplayAndDownload(t *testing.T) {
	t.Parallel()

	journey := newPrivacyJourney(t)
	owner, _ := privacyLogin(t, journey, "privacy-export@example.test", "correct horse battery staple")
	other, _ := privacyLogin(t, journey, "privacy-export-other@example.test", "correct horse battery staple")

	status, raw, _ := jsonPost(t, owner, journey.server, "/api/v1/me/exports", `{}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/v1/me/exports status = %d, want 202 (body: %.200s)", status, raw)
	}
	var first map[string]string
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatalf("export request is not JSON: %v", err)
	}
	if first["export_id"] == "" || first["download_token"] == "" {
		t.Fatalf("export request = %.200s, want export_id and a single-use download_token", raw)
	}

	status, raw, header := jsonPost(t, owner, journey.server, "/api/v1/me/exports", `{}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/v1/me/exports replay status = %d, want 202", status)
	}
	var replay map[string]string
	if err := json.Unmarshal(raw, &replay); err != nil {
		t.Fatalf("export replay is not JSON: %v", err)
	}
	if replay["export_id"] != first["export_id"] {
		t.Errorf("export replay id = %q, want the same %q: a replay must not duplicate work", replay["export_id"], first["export_id"])
	}
	if replay["download_token"] == "" || replay["download_token"] == first["download_token"] {
		t.Error("export replay must rotate the single-use download capability")
	}
	_ = header

	repository := profilespostgres.NewRepository(journey.pool)
	generate, err := profilesapp.NewGeneratePersonalExportUseCase(repository, repository, exportjson.NewEncoder(), clockseed.NewClock())
	if err != nil {
		t.Fatalf("NewGeneratePersonalExportUseCase: %v", err)
	}
	generated, err := generate.Execute(context.Background(), first["export_id"])
	if err != nil {
		t.Fatalf("GeneratePersonalExport: %v", err)
	}
	if generated.Replayed {
		t.Fatal("first generation answers Replayed")
	}
	again, err := generate.Execute(context.Background(), first["export_id"])
	if err != nil {
		t.Fatalf("GeneratePersonalExport replay: %v", err)
	}
	if !again.Replayed {
		t.Error("second generation rebuilds: the worker run must answer Replayed")
	}

	if status, _ := jsonGet(t, owner, journey.server, "/api/v1/me/exports/"+first["export_id"]+"/download?token="+first["download_token"]); status != http.StatusForbidden {
		t.Errorf("GET download with rotated token status = %d, want 403", status)
	}
	download := "/api/v1/me/exports/" + first["export_id"] + "/download?token=" + replay["download_token"]
	status, raw, header = getHeaders(t, owner, journey.server, download)
	if status != http.StatusOK {
		t.Fatalf("GET download status = %d, want 200 (body: %.200s)", status, raw)
	}
	if header.Get("Content-Disposition") == "" {
		t.Error("download answers without Content-Disposition: attachment")
	}
	assertPrivateNoStore(t, header)

	if status, _ := jsonGet(t, owner, journey.server, download); status != http.StatusNotFound {
		t.Errorf("GET download replay status = %d, want 404: the single-use link must not serve twice", status)
	}
	if status, _ := jsonGet(t, other, journey.server, "/api/v1/me/exports/"+first["export_id"]+"/download?token="+replay["download_token"]); status != http.StatusNotFound {
		t.Errorf("GET foreign download status = %d, want 404 without exposing existence", status)
	}
	if status, _ := jsonGet(t, owner, journey.server, "/api/v1/me/exports/no-such-export/download?token="+replay["download_token"]); status != http.StatusNotFound {
		t.Errorf("GET unknown download status = %d, want 404", status)
	}
}

// TestPrivacyDeletionRequestCancelWorkflow proves the cooling-off workflow:
// the request is accepted and replayed idempotently, the status serves only
// its owner, the cancel carries no reason back, and a terminal request
// refuses a second cancel.
func TestPrivacyDeletionRequestCancelWorkflow(t *testing.T) {
	t.Parallel()

	journey := newPrivacyJourney(t)
	owner, _ := privacyLogin(t, journey, "privacy-deletion@example.test", "correct horse battery staple")
	other, _ := privacyLogin(t, journey, "privacy-deletion-other@example.test", "correct horse battery staple")
	anonymous := browser(t)

	status, raw, _ := jsonPost(t, owner, journey.server, "/api/v1/me/deletion", `{}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/v1/me/deletion status = %d, want 202 (body: %.200s)", status, raw)
	}
	var first map[string]any
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatalf("deletion request is not JSON: %v", err)
	}
	if first["status"] != "requested" {
		t.Errorf("deletion status = %v, want requested", first["status"])
	}

	status, _, header := jsonPost(t, owner, journey.server, "/api/v1/me/deletion", `{}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/v1/me/deletion replay status = %d, want 202", status)
	}
	if header.Get("Idempotency-Replayed") != "true" {
		t.Error("deletion replay answers without Idempotency-Replayed: true")
	}

	status, raw, header = getHeaders(t, owner, journey.server, "/api/v1/me/deletion")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/deletion owner status = %d, want 200 (body: %.200s)", status, raw)
	}
	assertPrivateNoStore(t, header)
	if status, _ := jsonGet(t, other, journey.server, "/api/v1/me/deletion"); status != http.StatusNotFound {
		t.Errorf("GET other deletion status = %d, want 404: no request of theirs exists", status)
	}
	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/me/deletion"); status != http.StatusUnauthorized {
		t.Errorf("GET deletion anonymous status = %d, want 401", status)
	}

	status, raw, _ = jsonPost(t, owner, journey.server, "/api/v1/me/deletion/cancel", `{"reason":"changed my mind"}`)
	if status != http.StatusOK {
		t.Fatalf("POST /api/v1/me/deletion/cancel status = %d, want 200 (body: %.200s)", status, raw)
	}
	if strings.Contains(string(raw), "changed my mind") {
		t.Error("cancel response echoes the reason: restricted evidence must never serialize")
	}

	status, raw = jsonGet(t, owner, journey.server, "/api/v1/me/deletion")
	if status != http.StatusOK {
		t.Fatalf("GET deletion after cancel status = %d, want 200", status)
	}
	var canceled map[string]any
	if err := json.Unmarshal(raw, &canceled); err != nil {
		t.Fatalf("canceled deletion is not JSON: %v", err)
	}
	if canceled["status"] != "canceled" {
		t.Errorf("deletion status = %v, want canceled", canceled["status"])
	}
	if status, _, _ := jsonPost(t, owner, journey.server, "/api/v1/me/deletion/cancel", `{"reason":"again"}`); status != http.StatusConflict {
		t.Errorf("POST cancel on terminal request status = %d, want 409", status)
	}
}
