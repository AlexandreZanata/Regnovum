package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/exportjson"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

const (
	exportSessionToken      = "export-session-token"
	exportStaleSessionToken = "export-stale-session-token"
	exportOtherSessionToken = "export-other-session-token"

	exportFirstAccount = "018f6b2a-0000-7000-8000-0000000000c1"
	exportOtherAccount = "018f6b2a-0000-7000-8000-0000000000c2"

	exportFirstMarker = "export-owner@arena.example.com"
	exportOtherMarker = "export-other@arena.example.com"
)

var exportNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

type exportFixedClock struct {
	now time.Time
}

func (c exportFixedClock) Now() time.Time { return c.now }

type exportFixedRandom struct{}

func (exportFixedRandom) Read(buffer []byte) (int, error) {
	for i := range buffer {
		buffer[i] = 0x11
	}
	return len(buffer), nil
}

type exportSessionAges struct {
	ages map[string]time.Duration
}

func (d exportSessionAges) SessionAgeAt(_ context.Context, sessionID string, _ time.Time) (time.Duration, error) {
	age, ok := d.ages[sessionID]
	if !ok {
		return 0, application.ErrUnknownSession
	}
	return age, nil
}

// exportStore is an in-memory persistence fake for the HTTP contract tests.
type exportStore struct {
	nextID    int
	records   map[string]*application.PersonalExportRecord
	documents map[string][]byte
	hashes    map[string]string
}

func newExportStore() *exportStore {
	return &exportStore{
		records:   map[string]*application.PersonalExportRecord{},
		documents: map[string][]byte{},
		hashes:    map[string]string{},
	}
}

func (s *exportStore) RequestPersonalExport(_ context.Context, request application.PersonalExportRequest) (*application.PersonalExportRecord, error) {
	for _, record := range s.records {
		if record.AccountID == request.AccountID.String() && record.Status != domain.ExportStatusExpired {
			record.DownloadTokenHash = request.DownloadTokenHash
			record.DownloadCount = 0
			return record, nil
		}
	}
	s.nextID++
	record := &application.PersonalExportRecord{
		ID:                fmt.Sprintf("export-%d", s.nextID),
		AccountID:         request.AccountID.String(),
		Status:            domain.ExportStatusRequested,
		RequestedAt:       request.RequestedAt,
		MaxDownloads:      request.MaxDownloads,
		DownloadTokenHash: request.DownloadTokenHash,
	}
	s.records[record.ID] = record
	return record, nil
}

func (s *exportStore) GetPersonalExportForGeneration(_ context.Context, exportID string) (*application.PersonalExportRecord, error) {
	record, ok := s.records[exportID]
	if !ok {
		return nil, application.ErrExportNotFound
	}
	return record, nil
}

func (s *exportStore) MarkPersonalExportReady(_ context.Context, ready application.PersonalExportReady) (bool, error) {
	record, ok := s.records[ready.ExportID]
	if !ok {
		return false, application.ErrExportNotFound
	}
	if record.Status != domain.ExportStatusRequested {
		return false, nil
	}
	record.Status = domain.ExportStatusReady
	record.GeneratedAt = &ready.GeneratedAt
	record.ExpiresAt = &ready.ExpiresAt
	s.documents[record.ID] = append([]byte{}, ready.Document...)
	s.hashes[record.ID] = ready.DocumentSHA256
	return true, nil
}

func (s *exportStore) GetPersonalExportDownloadGuard(_ context.Context, accountID domain.AccountID, exportID string) (*application.PersonalExportRecord, error) {
	record, ok := s.records[exportID]
	if !ok || record.AccountID != accountID.String() {
		return nil, application.ErrExportNotFound
	}
	return record, nil
}

func (s *exportStore) ConsumePersonalExportDownload(_ context.Context, consumption application.PersonalExportConsumption) (*application.PersonalExportDownload, error) {
	record, ok := s.records[consumption.ExportID]
	if !ok || record.AccountID != consumption.AccountID.String() {
		return nil, application.ErrExportUnavailable
	}
	if !bytes.Equal(record.DownloadTokenHash, consumption.DownloadTokenHash) ||
		record.Status != domain.ExportStatusReady ||
		record.ExpiresAt == nil || !consumption.ConsumedAt.Before(*record.ExpiresAt) ||
		record.DownloadCount >= record.MaxDownloads {
		return nil, application.ErrExportUnavailable
	}
	record.DownloadCount++
	return &application.PersonalExportDownload{
		Document:       append([]byte{}, s.documents[record.ID]...),
		DocumentSHA256: s.hashes[record.ID],
	}, nil
}

type exportData struct {
	sections map[string]*application.PersonalExportSections
}

func (d *exportData) GetPersonalExportSections(_ context.Context, accountID domain.AccountID) (*application.PersonalExportSections, error) {
	sections, ok := d.sections[accountID.String()]
	if !ok {
		return nil, application.ErrAccountNotEligible
	}
	return sections, nil
}

type exportHarness struct {
	mux      http.Handler
	store    *exportStore
	request  *application.RequestPersonalExportUseCase
	generate *application.GeneratePersonalExportUseCase
	download *application.DownloadPersonalExportUseCase
}

func setupExportHarness(t *testing.T) *exportHarness {
	t.Helper()

	store := newExportStore()
	data := &exportData{sections: map[string]*application.PersonalExportSections{
		exportFirstAccount: exportSections(exportFirstMarker, "export-first"),
		exportOtherAccount: exportSections(exportOtherMarker, "export-other"),
	}}
	clock := exportFixedClock{now: exportNow}

	request, err := application.NewRequestPersonalExportUseCase(store, exportFixedRandom{}, clock)
	if err != nil {
		t.Fatalf("NewRequestPersonalExportUseCase: %v", err)
	}
	generate, err := application.NewGeneratePersonalExportUseCase(store, data, exportjson.NewEncoder(), clock)
	if err != nil {
		t.Fatalf("NewGeneratePersonalExportUseCase: %v", err)
	}
	download, err := application.NewDownloadPersonalExportUseCase(store, clock)
	if err != nil {
		t.Fatalf("NewDownloadPersonalExportUseCase: %v", err)
	}

	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false,
		Clock:          clockseed.NewClock(),
		Random:         clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("security.New: %v", err)
	}

	handler := adapterhttp.NewExportHandler(adapterhttp.ExportHandlerConfig{
		RequestUseCase:  request,
		DownloadUseCase: download,
		Sessions: exportSessionAges{ages: map[string]time.Duration{
			exportSessionToken:      time.Minute,
			exportOtherSessionToken: time.Minute,
			exportStaleSessionToken: domain.ExportStepUpWindow + time.Minute,
		}},
		SecurityManager: secMgr,
		Clock:           clock,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case exportSessionToken, exportStaleSessionToken:
			return security.AuthIdentity{AccountID: exportFirstAccount, SessionID: rawToken}, nil
		case exportOtherSessionToken:
			return security.AuthIdentity{AccountID: exportOtherAccount, SessionID: rawToken}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})

	return &exportHarness{
		mux:      secMgr.AuthenticateMiddleware(validator)(mux),
		store:    store,
		request:  request,
		generate: generate,
		download: download,
	}
}

func exportSections(email, username string) *application.PersonalExportSections {
	return &application.PersonalExportSections{
		Account: application.PersonalExportAccount{
			ID:            username + "-id",
			Email:         email,
			Status:        "active",
			EmailVerified: true,
			CreatedAt:     exportNow.Add(-24 * time.Hour),
		},
		Positions: []application.PersonalExportPosition{{
			ArenaID:         username + "-arena",
			ArenaSlug:       username + "-slug",
			ArenaStatement:  "Statement of " + username,
			InitialPosition: "agree",
			CurrentPosition: "disagree",
			Version:         2,
			CreatedAt:       exportNow.Add(-time.Hour),
			UpdatedAt:       exportNow,
		}},
	}
}

func exportRequest(t *testing.T, harness *exportHarness, sessionToken, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, target, nil)
	if sessionToken != "" {
		request.AddCookie(&http.Cookie{Name: security.DefaultSessionCookieName, Value: sessionToken})
	}
	harness.mux.ServeHTTP(recorder, request)
	return recorder
}

func exportDownload(t *testing.T, harness *exportHarness, sessionToken, exportID, token string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/v1/me/exports/" + exportID + "/download"
	if token != "" {
		target += "?token=" + token
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if sessionToken != "" {
		request.AddCookie(&http.Cookie{Name: security.DefaultSessionCookieName, Value: sessionToken})
	}
	harness.mux.ServeHTTP(recorder, request)
	return recorder
}

func assertProblemCode(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body: %s)", recorder.Code, wantStatus, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
	}
	problem := decodeObject(t, recorder.Body.Bytes())
	if problem["code"] != wantCode {
		t.Fatalf("problem code = %v, want %s (body: %s)", problem["code"], wantCode, recorder.Body.String())
	}
}

func TestExportHTTPRequiresAuthAndStepUp(t *testing.T) {
	harness := setupExportHarness(t)

	anonymous := exportRequest(t, harness, "", "/api/v1/me/exports")
	assertProblemCode(t, anonymous, http.StatusUnauthorized, "unauthorized")
	anonymousDownload := exportDownload(t, harness, "", "export-1", "token")
	assertProblemCode(t, anonymousDownload, http.StatusUnauthorized, "unauthorized")

	stale := exportRequest(t, harness, exportStaleSessionToken, "/api/v1/me/exports")
	assertProblemCode(t, stale, http.StatusUnauthorized, "step_up_required")
	if cacheControl := stale.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
		t.Fatalf("rejection Cache-Control = %q, want private no-store", cacheControl)
	}
}

func TestExportHTTPRequestResponseAndGoldenDownload(t *testing.T) {
	harness := setupExportHarness(t)

	accepted := exportRequest(t, harness, exportSessionToken, "/api/v1/me/exports")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", accepted.Code, accepted.Body.String())
	}
	for _, marker := range []string{"no-store", "no-cache"} {
		if cacheControl := accepted.Header().Get("Cache-Control"); !strings.Contains(cacheControl, marker) {
			t.Fatalf("Cache-Control = %q, want %s", cacheControl, marker)
		}
	}
	job := decodeObject(t, accepted.Body.Bytes())
	assertExactKeys(t, job, "export_id", "status", "download_token")
	exportID, _ := job["export_id"].(string)
	token, _ := job["download_token"].(string)
	if exportID == "" || token == "" || job["status"] != "requested" {
		t.Fatalf("job = %v", job)
	}

	if _, err := harness.generate.Execute(context.Background(), exportID); err != nil {
		t.Fatalf("generate: %v", err)
	}

	downloaded := exportDownload(t, harness, exportSessionToken, exportID, token)
	if downloaded.Code != http.StatusOK {
		t.Fatalf("download status = %d, want 200 (body: %s)", downloaded.Code, downloaded.Body.String())
	}
	if contentType := downloaded.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	if disposition := downloaded.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment;") || strings.Contains(disposition, token) {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	if cacheControl := downloaded.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}

	var document map[string]any
	if err := json.Unmarshal(downloaded.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	if document["schema_version"] != float64(domain.ExportSchemaVersion) {
		t.Fatalf("schema_version = %v", document["schema_version"])
	}
	account, _ := document["account"].(map[string]any)
	if account["email"] != exportFirstMarker {
		t.Fatalf("account email = %v, want the owner's email", account["email"])
	}
	serialized := downloaded.Body.String()
	if strings.Contains(serialized, exportOtherMarker) {
		t.Fatal("document leaked another account's data")
	}
	for _, marker := range []string{"stripe", "cus_", "sub_", "price_", "password", "user_agent", "ip_address"} {
		if strings.Contains(serialized, marker) {
			t.Fatalf("document leaks forbidden marker %q", marker)
		}
	}

	// Replay is denied: the single-use link never serves twice.
	replayed := exportDownload(t, harness, exportSessionToken, exportID, token)
	assertProblemCode(t, replayed, http.StatusNotFound, "export_not_found")

	// A forged token is forbidden; a foreign owner sees the same not found
	// as an unknown export, without an existence oracle.
	forged := exportDownload(t, harness, exportSessionToken, exportID, "forged-token")
	assertProblemCode(t, forged, http.StatusForbidden, "invalid_export_token")
	foreign := exportDownload(t, harness, exportOtherSessionToken, exportID, token)
	assertProblemCode(t, foreign, http.StatusNotFound, "export_not_found")
	missing := exportDownload(t, harness, exportSessionToken, "018f6b2a-0000-7000-8000-00000000dead", token)
	assertProblemCode(t, missing, http.StatusNotFound, "export_not_found")
}

func TestExportHTTPRejectsExpiredLinks(t *testing.T) {
	harness := setupExportHarness(t)

	accepted := exportRequest(t, harness, exportSessionToken, "/api/v1/me/exports")
	job := decodeObject(t, accepted.Body.Bytes())
	exportID, _ := job["export_id"].(string)
	token, _ := job["download_token"].(string)
	if _, err := harness.generate.Execute(context.Background(), exportID); err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Age the link past its window: the stale session still authenticates,
	// but the capability is gone.
	record := harness.store.records[exportID]
	expired := exportNow.Add(-time.Second)
	record.ExpiresAt = &expired

	denied := exportDownload(t, harness, exportSessionToken, exportID, token)
	assertProblemCode(t, denied, http.StatusNotFound, "export_not_found")
}
