package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/http"
	profilespg "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

const (
	validSessionToken  = "valid-session-token"
	secondSessionToken = "second-session-token"
)

type testHarness struct {
	mux          http.Handler
	email        string
	username     string
	secondEmail  string
	secondUserID domain.AccountID
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func setupProfilesHarness(t *testing.T) *testHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	repo := profilespg.NewRepository(pool)
	q := platformpg.New(pool)

	const email = "http-profile@arena.example.com"
	acc, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "pending"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	verified, err := q.SetEmailVerified(ctx, acc.ID)
	if err != nil {
		t.Fatalf("verify account: %v", err)
	}
	accountID := domain.AccountID(uuidString(verified.ID))

	const secondEmail = "http-no-profile@arena.example.com"
	second, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: secondEmail, Status: "pending"})
	if err != nil {
		t.Fatalf("create second account: %v", err)
	}
	secondVerified, err := q.SetEmailVerified(ctx, second.ID)
	if err != nil {
		t.Fatalf("verify second account: %v", err)
	}
	secondAccountID := domain.AccountID(uuidString(secondVerified.ID))

	clock := clockseed.NewClock()
	random := clockseed.NewRandom()
	policy := domain.DefaultUsernamePolicy()
	createProfile := application.NewCreateProfileUseCase(repo, repo, policy, clock)
	created, err := createProfile.Execute(ctx, application.CreateProfileCommand{
		AccountID: accountID.String(),
		Username:  "ArenaUser",
		Locale:    "en-US",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false,
		Clock:          clock,
		Random:         random,
	})
	if err != nil {
		t.Fatalf("setup security manager: %v", err)
	}

	handler := adapterhttp.NewHandler(adapterhttp.HandlerConfig{
		GetPublicProfileUseCase:  application.NewGetPublicProfileUseCase(repo),
		GetPrivateProfileUseCase: application.NewGetPrivateProfileUseCase(repo),
		SecurityManager:          secMgr,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case validSessionToken:
			return security.AuthIdentity{AccountID: accountID.String(), SessionID: "session-1"}, nil
		case secondSessionToken:
			return security.AuthIdentity{AccountID: secondAccountID.String(), SessionID: "session-2"}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})

	return &testHarness{
		mux:          secMgr.AuthenticateMiddleware(validator)(mux),
		email:        email,
		username:     created.Username().String(),
		secondEmail:  secondEmail,
		secondUserID: secondAccountID,
	}
}

func decodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, string(body))
	}
	return object
}

func assertExactKeys(t *testing.T, object map[string]any, want ...string) {
	t.Helper()
	if len(object) != len(want) {
		t.Fatalf("response keys = %v, want exactly %v", keysOf(object), want)
	}
	for _, key := range want {
		if _, ok := object[key]; !ok {
			t.Fatalf("response is missing key %q (keys: %v)", key, keysOf(object))
		}
	}
}

func keysOf(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}

func assertProblemNotFound(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
	}
	problem := decodeObject(t, recorder.Body.Bytes())
	if problem["code"] != "profile_not_found" {
		t.Fatalf("problem code = %v, want profile_not_found", problem["code"])
	}
}

func TestPublicProfileHTTPReturnsOnlyAllowedFields(t *testing.T) {
	harness := setupProfilesHarness(t)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/ArenaUser", nil)
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cacheControl)
	}

	body := recorder.Body.String()
	if strings.Contains(body, harness.email) {
		t.Fatalf("SECURITY VIOLATION: public profile leaked the account email: %s", body)
	}

	profile := decodeObject(t, recorder.Body.Bytes())
	assertExactKeys(t, profile, "username", "interface_locale", "created_at")
	if profile["username"] != "ArenaUser" {
		t.Errorf("username = %v, want ArenaUser", profile["username"])
	}
	if profile["interface_locale"] != "en-US" {
		t.Errorf("interface_locale = %v, want en-US", profile["interface_locale"])
	}
	createdAt, ok := profile["created_at"].(string)
	if !ok {
		t.Fatalf("created_at = %v, want RFC 3339 string", profile["created_at"])
	}
	if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
		t.Errorf("created_at %q is not RFC 3339: %v", createdAt, err)
	}
}

func TestPublicProfileNotFoundNeverEchoesInput(t *testing.T) {
	harness := setupProfilesHarness(t)

	tests := []struct {
		name string
		path string
	}{
		{name: "unknown", path: "/api/v1/profiles/ghost-handle"},
		{name: "reserved but unclaimed", path: "/api/v1/profiles/admin"},
		{name: "too short", path: "/api/v1/profiles/ab"},
		{name: "inner space", path: "/api/v1/profiles/a%20b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			recorder := httptest.NewRecorder()
			harness.mux.ServeHTTP(recorder, request)

			assertProblemNotFound(t, recorder)

			body := recorder.Body.String()
			for _, echo := range []string{"ghost-handle", "admin", "a b"} {
				if strings.Contains(body, echo) {
					t.Fatalf("SECURITY VIOLATION: problem details echoed unknown input %q: %s", echo, body)
				}
			}
		})
	}
}

func TestPrivateProfileRequiresAuthentication(t *testing.T) {
	harness := setupProfilesHarness(t)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/me/profile", nil)
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store on authenticated route", cacheControl)
	}
	if pragma := recorder.Header().Get("Pragma"); pragma != "no-cache" {
		t.Errorf("Pragma = %q, want no-cache", pragma)
	}

	invalid := httptest.NewRequest(http.MethodGet, "/api/v1/me/profile", nil)
	invalid.AddCookie(&http.Cookie{Name: "arena_session", Value: "revoked-token"})
	invalidRecorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(invalidRecorder, invalid)
	if invalidRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid session status = %d, want 401", invalidRecorder.Code)
	}
}

func TestPrivateProfileHTTPReturnsOnlyAllowedFields(t *testing.T) {
	harness := setupProfilesHarness(t)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/me/profile", nil)
	request.AddCookie(&http.Cookie{Name: "arena_session", Value: validSessionToken})
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	cacheControl := recorder.Header().Get("Cache-Control")
	if !strings.Contains(cacheControl, "private") || !strings.Contains(cacheControl, "no-store") {
		t.Errorf("Cache-Control = %q, want private, no-store (THR-CACHE-01)", cacheControl)
	}
	if pragma := recorder.Header().Get("Pragma"); pragma != "no-cache" {
		t.Errorf("Pragma = %q, want no-cache", pragma)
	}

	body := recorder.Body.String()
	if strings.Contains(body, harness.email) {
		t.Fatalf("SECURITY VIOLATION: private profile leaked the account email: %s", body)
	}
	if strings.Contains(body, "account_id") || strings.Contains(body, "stripe") {
		t.Fatalf("SECURITY VIOLATION: private profile leaked identifiers: %s", body)
	}

	profile := decodeObject(t, recorder.Body.Bytes())
	assertExactKeys(t, profile, "username", "interface_locale", "created_at", "updated_at")
	if profile["username"] != "ArenaUser" || profile["interface_locale"] != "en-US" {
		t.Errorf("profile = %v, want ArenaUser/en-US", profile)
	}
	for _, key := range []string{"created_at", "updated_at"} {
		value, ok := profile[key].(string)
		if !ok {
			t.Fatalf("%s = %v, want RFC 3339 string", key, profile[key])
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			t.Errorf("%s %q is not RFC 3339: %v", key, value, err)
		}
	}
}

func TestPrivateProfileMissingProfileIsNotFound(t *testing.T) {
	harness := setupProfilesHarness(t)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/me/profile", nil)
	request.AddCookie(&http.Cookie{Name: "arena_session", Value: secondSessionToken})
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, request)

	assertProblemNotFound(t, recorder)
	if strings.Contains(recorder.Body.String(), harness.secondEmail) {
		t.Fatalf("SECURITY VIOLATION: problem details leaked the account email")
	}
}
