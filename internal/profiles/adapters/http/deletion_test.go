package http_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

const (
	deletionSessionToken = "deletion-session-token"
	deletionOtherToken   = "deletion-other-session-token"

	deletionAccountID = "018f6b2a-0000-7000-8000-0000000000e1"
	deletionOtherID   = "018f6b2a-0000-7000-8000-0000000000e2"
)

var deletionNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

type httpDeletionClock struct {
	now time.Time
}

func (c httpDeletionClock) Now() time.Time { return c.now }

// httpDeletionStore is a faithful in-memory state machine for the HTTP
// contract tests.
type httpDeletionStore struct {
	nextID   int
	requests map[string]*application.DeletionRequest
	active   map[string]string
}

func newHTTPDeletionStore() *httpDeletionStore {
	return &httpDeletionStore{
		requests: map[string]*application.DeletionRequest{},
		active:   map[string]string{},
	}
}

func (s *httpDeletionStore) CreateDeletionRequest(_ context.Context, accountID domain.AccountID, requestedAt time.Time) (*application.DeletionRequest, bool, error) {
	if active, ok := s.active[accountID.String()]; ok {
		copied := *s.requests[active]
		return &copied, true, nil
	}
	s.nextID++
	request := &application.DeletionRequest{
		ID:          fmt.Sprintf("request-%d", s.nextID),
		AccountID:   accountID,
		Status:      domain.DeletionStatusRequested,
		RequestedAt: requestedAt,
	}
	s.requests[request.ID] = request
	s.active[accountID.String()] = request.ID
	return request, false, nil
}

func (s *httpDeletionStore) GetDeletionRequest(_ context.Context, accountID domain.AccountID) (*application.DeletionRequest, error) {
	if active, ok := s.active[accountID.String()]; ok {
		copied := *s.requests[active]
		return &copied, nil
	}
	var latest *application.DeletionRequest
	for _, request := range s.requests {
		if request.AccountID != accountID {
			continue
		}
		if latest == nil || request.RequestedAt.After(latest.RequestedAt) || (request.RequestedAt.Equal(latest.RequestedAt) && request.ID > latest.ID) {
			latest = request
		}
	}
	if latest == nil {
		return nil, application.ErrDeletionRequestNotFound
	}
	copied := *latest
	return &copied, nil
}

func (s *httpDeletionStore) CancelDeletionRequest(_ context.Context, accountID domain.AccountID, _ string, canceledAt time.Time) (*application.DeletionRequest, error) {
	active, ok := s.active[accountID.String()]
	if !ok {
		return nil, application.ErrDeletionNotCancellable
	}
	request := s.requests[active]
	request.Status = domain.DeletionStatusCanceled
	request.CanceledAt = &canceledAt
	delete(s.active, accountID.String())
	return request, nil
}

func (s *httpDeletionStore) ListDueDeletionRequests(_ context.Context, _ time.Time) ([]application.DeletionRequest, error) {
	return []application.DeletionRequest{}, nil
}

func (s *httpDeletionStore) ExecuteDeletionRequest(_ context.Context, _ domain.AccountID, _ time.Time) error {
	return nil
}

type httpDeletionAudit struct{}

func (httpDeletionAudit) RecordAccountDeletion(_ context.Context, _ application.DeletionAuditEvent) error {
	return nil
}

type httpDeletionUow struct{}

func (httpDeletionUow) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

type deletionHTTPHarness struct {
	mux     http.Handler
	store   *httpDeletionStore
	request *application.RequestDeletionUseCase
	status  *application.GetDeletionStatusUseCase
	cancel  *application.CancelDeletionUseCase
}

func setupDeletionHTTPHarness(t *testing.T) *deletionHTTPHarness {
	t.Helper()

	store := newHTTPDeletionStore()
	clock := httpDeletionClock{now: deletionNow}
	uow := httpDeletionUow{}
	audit := httpDeletionAudit{}

	request, err := application.NewRequestDeletionUseCase(store, audit, uow, clock)
	if err != nil {
		t.Fatalf("NewRequestDeletionUseCase: %v", err)
	}
	status, err := application.NewGetDeletionStatusUseCase(store)
	if err != nil {
		t.Fatalf("NewGetDeletionStatusUseCase: %v", err)
	}
	cancel, err := application.NewCancelDeletionUseCase(store, audit, uow, clock)
	if err != nil {
		t.Fatalf("NewCancelDeletionUseCase: %v", err)
	}

	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false,
		Clock:          clock,
		Random:         deletionHTTPRandom{},
	})
	if err != nil {
		t.Fatalf("security.New: %v", err)
	}

	handler := adapterhttp.NewDeletionHandler(adapterhttp.DeletionHandlerConfig{
		RequestUseCase:  request,
		StatusUseCase:   status,
		CancelUseCase:   cancel,
		SecurityManager: secMgr,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case deletionSessionToken:
			return security.AuthIdentity{AccountID: deletionAccountID, SessionID: "session-1"}, nil
		case deletionOtherToken:
			return security.AuthIdentity{AccountID: deletionOtherID, SessionID: "session-2"}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})

	return &deletionHTTPHarness{
		mux:     secMgr.AuthenticateMiddleware(validator)(mux),
		store:   store,
		request: request,
		status:  status,
		cancel:  cancel,
	}
}

type deletionHTTPRandom struct{}

func (deletionHTTPRandom) Read(buffer []byte) (int, error) {
	for i := range buffer {
		buffer[i] = 0x44
	}
	return len(buffer), nil
}

func deletionRequest(t *testing.T, harness *deletionHTTPHarness, method, target, sessionToken, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	if sessionToken != "" {
		request.AddCookie(&http.Cookie{Name: security.DefaultSessionCookieName, Value: sessionToken})
	}
	harness.mux.ServeHTTP(recorder, request)
	return recorder
}

func TestDeletionHTTPRequiresAuth(t *testing.T) {
	harness := setupDeletionHTTPHarness(t)

	for _, route := range []struct {
		method string
		target string
	}{
		{method: http.MethodPost, target: "/api/v1/me/deletion"},
		{method: http.MethodGet, target: "/api/v1/me/deletion"},
		{method: http.MethodPost, target: "/api/v1/me/deletion/cancel"},
	} {
		recorder := deletionRequest(t, harness, route.method, route.target, "", "")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", route.method, route.target, recorder.Code)
		}
		if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
			t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
		}
		if cacheControl := recorder.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
			t.Fatalf("Cache-Control = %q, want private no-store", cacheControl)
		}
	}
}

func TestDeletionHTTPJourneyRequestStatusCancel(t *testing.T) {
	harness := setupDeletionHTTPHarness(t)

	requested := deletionRequest(t, harness, http.MethodPost, "/api/v1/me/deletion", deletionSessionToken, "")
	if requested.Code != http.StatusAccepted {
		t.Fatalf("request status = %d, want 202 (body: %s)", requested.Code, requested.Body.String())
	}
	for _, marker := range []string{"no-store", "no-cache"} {
		if cacheControl := requested.Header().Get("Cache-Control"); !strings.Contains(cacheControl, marker) {
			t.Fatalf("Cache-Control = %q, want %s", cacheControl, marker)
		}
	}
	document := decodeObject(t, requested.Body.Bytes())
	assertExactKeys(t, document, "status", "requested_at", "executed_at", "canceled_at")
	if document["status"] != "requested" || document["executed_at"] != nil || document["canceled_at"] != nil {
		t.Fatalf("request document = %v", document)
	}
	if requestedAt, _ := document["requested_at"].(string); requestedAt == "" {
		t.Fatal("requested_at must be an RFC 3339 instant")
	}

	// Replay is marked idempotent.
	replayed := deletionRequest(t, harness, http.MethodPost, "/api/v1/me/deletion", deletionSessionToken, "")
	if replayed.Code != http.StatusAccepted || replayed.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay = %d, %q", replayed.Code, replayed.Header().Get("Idempotency-Replayed"))
	}

	// Status reflects the owner record.
	status := deletionRequest(t, harness, http.MethodGet, "/api/v1/me/deletion", deletionSessionToken, "")
	if status.Code != http.StatusOK {
		t.Fatalf("status code = %d (body: %s)", status.Code, status.Body.String())
	}
	if resolved := decodeObject(t, status.Body.Bytes()); resolved["status"] != "requested" {
		t.Fatalf("status document = %v", resolved)
	}

	// Cancellation accepts the optional reason and never echoes it.
	canceled := deletionRequest(t, harness, http.MethodPost, "/api/v1/me/deletion/cancel", deletionSessionToken, `{"reason":"mudei de ideia"}`)
	if canceled.Code != http.StatusOK {
		t.Fatalf("cancel status = %d (body: %s)", canceled.Code, canceled.Body.String())
	}
	canceledDocument := decodeObject(t, canceled.Body.Bytes())
	assertExactKeys(t, canceledDocument, "status", "requested_at", "executed_at", "canceled_at")
	if canceledDocument["status"] != "canceled" {
		t.Fatalf("canceled document = %v", canceledDocument)
	}
	if strings.Contains(canceled.Body.String(), "mudei de ideia") {
		t.Fatal("the cancel reason must never be reflected")
	}

	// Terminal state: a second cancellation conflicts.
	again := deletionRequest(t, harness, http.MethodPost, "/api/v1/me/deletion/cancel", deletionSessionToken, "")
	if again.Code != http.StatusConflict {
		t.Fatalf("second cancel status = %d, want 409", again.Code)
	}
	if problem := decodeObject(t, again.Body.Bytes()); problem["code"] != "deletion_not_cancellable" {
		t.Fatalf("problem = %v", problem)
	}
}

func TestDeletionHTTPOwnerIsolationAndUnknownRequest(t *testing.T) {
	harness := setupDeletionHTTPHarness(t)

	missing := deletionRequest(t, harness, http.MethodGet, "/api/v1/me/deletion", deletionSessionToken, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", missing.Code)
	}
	if problem := decodeObject(t, missing.Body.Bytes()); problem["code"] != "deletion_request_not_found" {
		t.Fatalf("problem = %v", problem)
	}

	// The other account never sees the first account's request.
	if recorder := deletionRequest(t, harness, http.MethodPost, "/api/v1/me/deletion", deletionSessionToken, ""); recorder.Code != http.StatusAccepted {
		t.Fatalf("request status = %d", recorder.Code)
	}
	otherStatus := deletionRequest(t, harness, http.MethodGet, "/api/v1/me/deletion", deletionOtherToken, "")
	if otherStatus.Code != http.StatusNotFound {
		t.Fatalf("other account status = %d, want 404", otherStatus.Code)
	}
	otherCancel := deletionRequest(t, harness, http.MethodPost, "/api/v1/me/deletion/cancel", deletionOtherToken, "")
	if otherCancel.Code != http.StatusNotFound {
		t.Fatalf("other account cancel = %d, want 404", otherCancel.Code)
	}
	if problem := decodeObject(t, otherCancel.Body.Bytes()); problem["code"] != "deletion_request_not_found" {
		t.Fatalf("problem = %v", problem)
	}
}

func TestDeletionHTTPValidatesCancelBody(t *testing.T) {
	harness := setupDeletionHTTPHarness(t)

	if recorder := deletionRequest(t, harness, http.MethodPost, "/api/v1/me/deletion", deletionSessionToken, ""); recorder.Code != http.StatusAccepted {
		t.Fatalf("request status = %d", recorder.Code)
	}
	tooLong := strings.Repeat("a", 501)
	recorder := deletionRequest(t, harness, http.MethodPost, "/api/v1/me/deletion/cancel", deletionSessionToken, `{"reason":"`+tooLong+`"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("long reason status = %d, want 400", recorder.Code)
	}
	if problem := decodeObject(t, recorder.Body.Bytes()); problem["code"] != "invalid_cancel_reason" {
		t.Fatalf("problem = %v", problem)
	}

	invalid := deletionRequest(t, harness, http.MethodPost, "/api/v1/me/deletion/cancel", deletionSessionToken, `{not-json`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid body status = %d, want 400", invalid.Code)
	}
	if problem := decodeObject(t, invalid.Body.Bytes()); problem["code"] != "invalid_json" {
		t.Fatalf("problem = %v", problem)
	}
}
