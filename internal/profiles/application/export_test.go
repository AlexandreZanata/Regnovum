package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

const (
	testInstant       = "2026-09-18T12:00:00Z"
	testOtherAccount  = "018f6b2a-0000-7000-8000-0000000000b2"
	testAccountMarker = "first-account@arena.example.com"
	testOtherMarker   = "second-account@arena.example.com"
)

var testNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

// testEncoder renders documents with the standard library encoder; the
// application itself never imports serialization (architecture gate).
type testEncoder struct{}

func (testEncoder) EncodePersonalExport(document application.PersonalExportDocument) ([]byte, error) {
	return json.Marshal(document)
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

type fixedRandom struct {
	fill byte
}

func (r fixedRandom) Read(buffer []byte) (int, error) {
	for i := range buffer {
		buffer[i] = r.fill
	}
	return len(buffer), nil
}

// fakeExportStore is a faithful in-memory implementation of the persistence
// contract: account scoping, token rotation, expiry and the atomic download
// budget all behave like the PostgreSQL adapter.
type fakeExportStore struct {
	nextID    int
	records   map[string]*application.PersonalExportRecord
	documents map[string][]byte
	hashes    map[string]string
	sections  map[string]*application.PersonalExportSections
}

func newFakeExportStore() *fakeExportStore {
	return &fakeExportStore{
		records:   map[string]*application.PersonalExportRecord{},
		documents: map[string][]byte{},
		hashes:    map[string]string{},
		sections:  map[string]*application.PersonalExportSections{},
	}
}

func (s *fakeExportStore) RequestPersonalExport(_ context.Context, request application.PersonalExportRequest) (*application.PersonalExportRecord, error) {
	for _, record := range s.records {
		if record.AccountID == request.AccountID.String() && record.Status != domain.ExportStatusExpired {
			record.DownloadTokenHash = request.DownloadTokenHash
			record.DownloadCount = 0
			return copyExportRecord(record), nil
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
	return copyExportRecord(record), nil
}

func (s *fakeExportStore) GetPersonalExportForGeneration(_ context.Context, exportID string) (*application.PersonalExportRecord, error) {
	record, ok := s.records[exportID]
	if !ok {
		return nil, application.ErrExportNotFound
	}
	return copyExportRecord(record), nil
}

func (s *fakeExportStore) MarkPersonalExportReady(_ context.Context, ready application.PersonalExportReady) (bool, error) {
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

func (s *fakeExportStore) GetPersonalExportDownloadGuard(_ context.Context, accountID domain.AccountID, exportID string) (*application.PersonalExportRecord, error) {
	record, ok := s.records[exportID]
	if !ok || record.AccountID != accountID.String() {
		return nil, application.ErrExportNotFound
	}
	return copyExportRecord(record), nil
}

func (s *fakeExportStore) ConsumePersonalExportDownload(_ context.Context, consumption application.PersonalExportConsumption) (*application.PersonalExportDownload, error) {
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

func copyExportRecord(record *application.PersonalExportRecord) *application.PersonalExportRecord {
	copied := *record
	copied.DownloadTokenHash = append([]byte{}, record.DownloadTokenHash...)
	return &copied
}

type fakeExportData struct {
	sections map[string]*application.PersonalExportSections
}

func (f *fakeExportData) GetPersonalExportSections(_ context.Context, accountID domain.AccountID) (*application.PersonalExportSections, error) {
	sections, ok := f.sections[accountID.String()]
	if !ok {
		return nil, application.ErrAccountNotEligible
	}
	return sections, nil
}

func mustRequestUseCase(t *testing.T, store *fakeExportStore) *application.RequestPersonalExportUseCase {
	t.Helper()
	useCase, err := application.NewRequestPersonalExportUseCase(store, fixedRandom{fill: 0x2a}, fixedClock{now: testNow})
	if err != nil {
		t.Fatalf("NewRequestPersonalExportUseCase: %v", err)
	}
	return useCase
}

func mustGenerateUseCase(t *testing.T, store *fakeExportStore, data *fakeExportData) *application.GeneratePersonalExportUseCase {
	t.Helper()
	useCase, err := application.NewGeneratePersonalExportUseCase(store, data, testEncoder{}, fixedClock{now: testNow})
	if err != nil {
		t.Fatalf("NewGeneratePersonalExportUseCase: %v", err)
	}
	return useCase
}

func mustDownloadUseCase(t *testing.T, store *fakeExportStore) *application.DownloadPersonalExportUseCase {
	t.Helper()
	useCase, err := application.NewDownloadPersonalExportUseCase(store, fixedClock{now: testNow})
	if err != nil {
		t.Fatalf("NewDownloadPersonalExportUseCase: %v", err)
	}
	return useCase
}

func testSections(email, username string) *application.PersonalExportSections {
	return &application.PersonalExportSections{
		Account: application.PersonalExportAccount{
			ID:            username + "-id",
			Email:         email,
			Status:        "active",
			EmailVerified: true,
			CreatedAt:     testNow.Add(-24 * time.Hour),
		},
		Positions: []application.PersonalExportPosition{{
			ArenaID:         username + "-arena",
			ArenaSlug:       username + "-slug",
			ArenaStatement:  "Statement of " + username,
			InitialPosition: "agree",
			CurrentPosition: "disagree",
			Version:         2,
			CreatedAt:       testNow.Add(-time.Hour),
			UpdatedAt:       testNow,
		}},
	}
}

func TestRequestPersonalExportIssuesTokenAndRotatesOnReplay(t *testing.T) {
	t.Parallel()

	store := newFakeExportStore()
	useCase := mustRequestUseCase(t, store)
	accountID := domain.AccountID("018f6b2a-0000-7000-8000-0000000000a1")

	first, err := useCase.Execute(context.Background(), accountID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if first.ExportID == "" || first.Status != domain.ExportStatusRequested || first.DownloadToken == "" {
		t.Fatalf("requested export = %+v", first)
	}
	record := store.records[first.ExportID]
	if len(record.DownloadTokenHash) != 32 {
		t.Fatalf("stored token hash has %d bytes, want 32", len(record.DownloadTokenHash))
	}
	expected := sha256.Sum256([]byte(first.DownloadToken))
	if !bytes.Equal(record.DownloadTokenHash, expected[:]) {
		t.Fatal("stored hash does not match the issued token")
	}
	if record.MaxDownloads != domain.ExportMaxDownloads {
		t.Fatalf("max downloads = %d, want %d", record.MaxDownloads, domain.ExportMaxDownloads)
	}

	secondRandom := fixedRandom{fill: 0x2b}
	rotated, err := application.NewRequestPersonalExportUseCase(store, secondRandom, fixedClock{now: testNow})
	if err != nil {
		t.Fatalf("NewRequestPersonalExportUseCase: %v", err)
	}
	replay, err := rotated.Execute(context.Background(), accountID)
	if err != nil {
		t.Fatalf("replay Execute: %v", err)
	}
	if replay.ExportID != first.ExportID {
		t.Fatalf("replay created a new record: %s != %s", replay.ExportID, first.ExportID)
	}
	if replay.DownloadToken == first.DownloadToken {
		t.Fatal("replay must rotate the download capability")
	}
	if !bytes.Equal(store.records[replay.ExportID].DownloadTokenHash, func() []byte { sum := sha256.Sum256([]byte(replay.DownloadToken)); return sum[:] }()) {
		t.Fatal("rotated hash does not match the new token")
	}
}

func TestRequestPersonalExportValidatesInputAndComposition(t *testing.T) {
	t.Parallel()

	store := newFakeExportStore()
	useCase := mustRequestUseCase(t, store)
	if _, err := useCase.Execute(context.Background(), domain.AccountID("")); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
	}
	if _, err := application.NewRequestPersonalExportUseCase(nil, fixedRandom{}, fixedClock{}); !errors.Is(err, application.ErrInvalidExportConfig) {
		t.Fatalf("nil repository error = %v, want ErrInvalidExportConfig", err)
	}
	if _, err := application.NewRequestPersonalExportUseCase(store, nil, fixedClock{}); !errors.Is(err, application.ErrInvalidExportConfig) {
		t.Fatalf("nil random error = %v, want ErrInvalidExportConfig", err)
	}
}

func TestGeneratePersonalExportBuildsVersionedDocument(t *testing.T) {
	t.Parallel()

	store := newFakeExportStore()
	data := &fakeExportData{sections: map[string]*application.PersonalExportSections{
		"018f6b2a-0000-7000-8000-0000000000a1": testSections(testAccountMarker, "first-account"),
	}}
	requested, err := mustRequestUseCase(t, store).Execute(context.Background(), domain.AccountID("018f6b2a-0000-7000-8000-0000000000a1"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	generated, err := mustGenerateUseCase(t, store, data).Execute(context.Background(), requested.ExportID)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if generated.Replayed || generated.Status != domain.ExportStatusReady {
		t.Fatalf("generated = %+v", generated)
	}

	record := store.records[requested.ExportID]
	if record.ExpiresAt == nil || !record.ExpiresAt.Equal(testNow.Add(domain.ExportTTL)) {
		t.Fatalf("expiry = %v, want %v", record.ExpiresAt, testNow.Add(domain.ExportTTL))
	}
	document := store.documents[requested.ExportID]
	sum := sha256.Sum256(document)
	if record.DownloadCount != 0 || store.hashes[requested.ExportID] != fmt.Sprintf("%x", sum) {
		t.Fatal("stored document hash does not match the persisted bytes")
	}

	var decoded map[string]any
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	if decoded["schema_version"] != float64(domain.ExportSchemaVersion) {
		t.Fatalf("schema_version = %v, want %d", decoded["schema_version"], domain.ExportSchemaVersion)
	}
	if decoded["generated_at"] != testInstant {
		t.Fatalf("generated_at = %v, want %s", decoded["generated_at"], testInstant)
	}
	excluded, _ := decoded["excluded_categories"].([]any)
	if len(excluded) != len(application.ExportExcludedCategories) {
		t.Fatalf("excluded_categories = %v", decoded["excluded_categories"])
	}
	account, _ := decoded["account"].(map[string]any)
	if account["email"] != testAccountMarker {
		t.Fatalf("account email = %v", account["email"])
	}

	// Generation is idempotent: the recorded document is never rebuilt.
	replay, err := mustGenerateUseCase(t, store, data).Execute(context.Background(), requested.ExportID)
	if err != nil {
		t.Fatalf("replay generate: %v", err)
	}
	if !replay.Replayed {
		t.Fatal("second generation must report the recorded replay")
	}
	if !bytes.Equal(document, store.documents[requested.ExportID]) {
		t.Fatal("replay rewrote the document")
	}
}

func TestGeneratePersonalExportRejectsUnknownAndUnavailable(t *testing.T) {
	t.Parallel()

	store := newFakeExportStore()
	data := &fakeExportData{}
	useCase := mustGenerateUseCase(t, store, data)
	if _, err := useCase.Execute(context.Background(), ""); !errors.Is(err, application.ErrExportNotFound) {
		t.Fatalf("empty id error = %v, want ErrExportNotFound", err)
	}
	if _, err := useCase.Execute(context.Background(), "missing"); !errors.Is(err, application.ErrExportNotFound) {
		t.Fatalf("missing id error = %v, want ErrExportNotFound", err)
	}

	store.records["expired"] = &application.PersonalExportRecord{ID: "expired", AccountID: "account", Status: domain.ExportStatusExpired}
	if _, err := useCase.Execute(context.Background(), "expired"); !errors.Is(err, application.ErrExportUnavailable) {
		t.Fatalf("expired error = %v, want ErrExportUnavailable", err)
	}
}

func TestDownloadPersonalExportEnforcesOwnerTokenExpiryAndBudget(t *testing.T) {
	t.Parallel()

	store := newFakeExportStore()
	data := &fakeExportData{sections: map[string]*application.PersonalExportSections{
		"018f6b2a-0000-7000-8000-0000000000a1": testSections(testAccountMarker, "first-account"),
	}}
	accountID := domain.AccountID("018f6b2a-0000-7000-8000-0000000000a1")
	requested, err := mustRequestUseCase(t, store).Execute(context.Background(), accountID)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := mustGenerateUseCase(t, store, data).Execute(context.Background(), requested.ExportID); err != nil {
		t.Fatalf("generate: %v", err)
	}
	download := mustDownloadUseCase(t, store)

	result, err := download.Execute(context.Background(), application.DownloadPersonalExportCommand{
		AccountID: accountID, ExportID: requested.ExportID, Token: requested.DownloadToken,
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if len(result.Document) == 0 || result.DocumentSHA256 == "" {
		t.Fatal("download returned no document")
	}

	// Replay: the single-use budget is exhausted.
	if _, err := download.Execute(context.Background(), application.DownloadPersonalExportCommand{
		AccountID: accountID, ExportID: requested.ExportID, Token: requested.DownloadToken,
	}); !errors.Is(err, application.ErrExportUnavailable) {
		t.Fatalf("replay error = %v, want ErrExportUnavailable", err)
	}

	// Wrong token and foreign owners are denied without touching the budget.
	if _, err := download.Execute(context.Background(), application.DownloadPersonalExportCommand{
		AccountID: accountID, ExportID: requested.ExportID, Token: "forged-token",
	}); !errors.Is(err, application.ErrInvalidExportToken) {
		t.Fatalf("forged token error = %v, want ErrInvalidExportToken", err)
	}
	if _, err := download.Execute(context.Background(), application.DownloadPersonalExportCommand{
		AccountID: testOtherAccount, ExportID: requested.ExportID, Token: requested.DownloadToken,
	}); !errors.Is(err, application.ErrExportNotFound) {
		t.Fatalf("foreign owner error = %v, want ErrExportNotFound", err)
	}
	if _, err := download.Execute(context.Background(), application.DownloadPersonalExportCommand{
		AccountID: accountID, ExportID: requested.ExportID,
	}); !errors.Is(err, application.ErrInvalidExportToken) {
		t.Fatalf("missing token error = %v, want ErrInvalidExportToken", err)
	}
	if _, err := download.Execute(context.Background(), application.DownloadPersonalExportCommand{
		AccountID: domain.AccountID(""), ExportID: requested.ExportID, Token: requested.DownloadToken,
	}); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty owner error = %v, want ErrEmptyAccountID", err)
	}
}

func TestDownloadPersonalExportRejectsExpiredLinks(t *testing.T) {
	t.Parallel()

	store := newFakeExportStore()
	data := &fakeExportData{sections: map[string]*application.PersonalExportSections{
		"018f6b2a-0000-7000-8000-0000000000a1": testSections(testAccountMarker, "first-account"),
	}}
	accountID := domain.AccountID("018f6b2a-0000-7000-8000-0000000000a1")
	requested, err := mustRequestUseCase(t, store).Execute(context.Background(), accountID)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := mustGenerateUseCase(t, store, data).Execute(context.Background(), requested.ExportID); err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Age the link past its window: the application clock is the authority.
	expired := testNow.Add(domain.ExportTTL + time.Second)
	download, err := application.NewDownloadPersonalExportUseCase(store, fixedClock{now: expired})
	if err != nil {
		t.Fatalf("NewDownloadPersonalExportUseCase: %v", err)
	}
	if _, err := download.Execute(context.Background(), application.DownloadPersonalExportCommand{
		AccountID: accountID, ExportID: requested.ExportID, Token: requested.DownloadToken,
	}); !errors.Is(err, application.ErrExportUnavailable) {
		t.Fatalf("expired link error = %v, want ErrExportUnavailable", err)
	}
}

func TestPersonalExportDocumentsDoNotMixAccounts(t *testing.T) {
	t.Parallel()

	store := newFakeExportStore()
	data := &fakeExportData{sections: map[string]*application.PersonalExportSections{
		"018f6b2a-0000-7000-8000-0000000000a1": testSections(testAccountMarker, "first-account"),
		testOtherAccount:                       testSections(testOtherMarker, "second-account"),
	}}
	firstAccount := domain.AccountID("018f6b2a-0000-7000-8000-0000000000a1")
	secondAccount := domain.AccountID(testOtherAccount)

	firstRequest, err := mustRequestUseCase(t, store).Execute(context.Background(), firstAccount)
	if err != nil {
		t.Fatalf("request first: %v", err)
	}
	secondRequest, err := mustRequestUseCase(t, store).Execute(context.Background(), secondAccount)
	if err != nil {
		t.Fatalf("request second: %v", err)
	}
	if _, err := mustGenerateUseCase(t, store, data).Execute(context.Background(), firstRequest.ExportID); err != nil {
		t.Fatalf("generate first: %v", err)
	}
	if _, err := mustGenerateUseCase(t, store, data).Execute(context.Background(), secondRequest.ExportID); err != nil {
		t.Fatalf("generate second: %v", err)
	}

	firstDocument := string(store.documents[firstRequest.ExportID])
	secondDocument := string(store.documents[secondRequest.ExportID])
	if !strings.Contains(firstDocument, testAccountMarker) || strings.Contains(firstDocument, testOtherMarker) {
		t.Fatalf("first document mixed accounts: %s", firstDocument)
	}
	if !strings.Contains(secondDocument, testOtherMarker) || strings.Contains(secondDocument, testAccountMarker) {
		t.Fatalf("second document mixed accounts: %s", secondDocument)
	}
	if firstDocument == secondDocument {
		t.Fatal("distinct accounts must produce distinct documents")
	}
}

func TestBuildPersonalExportDocumentSerializesEmptyArrays(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(application.BuildPersonalExportDocument(testNow, nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, marker := range []string{`"positions":[]`, `"position_changes":[]`, `"arena_drafts":[]`, `"arguments":[]`, `"username_history":[]`, `"sessions":[]`, `"transactions":[]`, `"lots":[]`, `"consumptions":[]`, `"checkout_intents":[]`, `"subscriptions":[]`, `"profile":null`, `"preferences":null`} {
		if !strings.Contains(string(encoded), marker) {
			t.Fatalf("document must serialize %s: %s", marker, encoded)
		}
	}
}

func TestBuildPersonalExportDocumentRendersNilSlicesAsEmptyArrays(t *testing.T) {
	t.Parallel()

	// Explicit nil slices (as opposed to an absent sections struct) must
	// still serialize as []: a null where a list belongs breaks typed
	// clients (mutation gate: export_document.go:266).
	encoded, err := json.Marshal(application.BuildPersonalExportDocument(testNow, &application.PersonalExportSections{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, marker := range []string{`"positions":[]`, `"arguments":[]`, `"username_history":[]`, `"sessions":[]`, `"transactions":[]`, `"lots":[]`} {
		if !strings.Contains(string(encoded), marker) {
			t.Fatalf("document must serialize %s: %s", marker, encoded)
		}
	}
}
