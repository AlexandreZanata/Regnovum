package application_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

var testHistorySecret = []byte("0123456789abcdef0123456789abcdef")

func newTestHistoryUseCase(t *testing.T, repo application.PassLotQueryRepository) *application.GetArenaPassHistoryUseCase {
	t.Helper()
	codec, err := application.NewHistoryCursorCodec(testHistorySecret)
	if err != nil {
		t.Fatalf("build history cursor codec: %v", err)
	}
	return application.NewGetArenaPassHistoryUseCase(repo, codec)
}

func signedHistoryCursor(payload string) string {
	mac := hmac.New(sha256.New, testHistorySecret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func historyRecords(t *testing.T, count int) []application.PassConsumptionRecord {
	t.Helper()
	records := make([]application.PassConsumptionRecord, count)
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for i := range records {
		reference, err := domain.ParseReference("stripe:evt_history_" + string(rune('a'+i)))
		if err != nil {
			t.Fatalf("ParseReference: %v", err)
		}
		records[i] = application.PassConsumptionRecord{
			ConsumptionID: "00000000-0000-7000-8000-00000000000" + string(rune('1'+i)),
			ArenaID:       "00000000-0000-7000-8000-00000000000" + string(rune('a'+i)),
			Origin:        domain.OriginPurchase,
			Reference:     reference,
			ConsumedAt:    base.Add(-time.Duration(i) * time.Minute),
		}
	}
	return records
}

func TestHistoryCursorCodec(t *testing.T) {
	if _, err := application.NewHistoryCursorCodec([]byte(strings.Repeat("s", 31))); !errors.Is(err, application.ErrWeakHistoryCursorSecret) {
		t.Fatalf("short secret error = %v, want ErrWeakHistoryCursorSecret", err)
	}
	codec, err := application.NewHistoryCursorCodec(testHistorySecret)
	if err != nil {
		t.Fatalf("NewHistoryCursorCodec: %v", err)
	}

	entry := historyRecords(t, 1)[0]
	cursor := codec.Encode(entry)
	decoded, err := codec.Decode(cursor)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded == nil || decoded.ConsumptionID != entry.ConsumptionID || !decoded.ConsumedAt.Equal(entry.ConsumedAt) {
		t.Fatalf("decoded = %+v, want %+v", decoded, entry)
	}
	if position, err := codec.Decode("   "); err != nil || position != nil {
		t.Fatalf("empty cursor = %+v/%v, want nil position without error", position, err)
	}

	unsigned := base64.RawURLEncoding.EncodeToString([]byte("v1|2026-09-17T12:00:00Z|entry-a"))
	forgedMac := hmac.New(sha256.New, []byte("a-different-secret-key-32-bytes-long"))
	forgedMac.Write([]byte("v1|2026-09-17T12:00:00Z|entry-a"))
	forged := base64.RawURLEncoding.EncodeToString([]byte("v1|2026-09-17T12:00:00Z|entry-a")) +
		"." + base64.RawURLEncoding.EncodeToString(forgedMac.Sum(nil))

	invalid := []string{
		"%%%",
		unsigned,
		forged,
		signedHistoryCursor("v9|2026-09-17T12:00:00Z|entry-a"),
		signedHistoryCursor("v1|not-a-time|entry-a"),
		signedHistoryCursor("v1|2026-09-17T12:00:00Z|"),
		signedHistoryCursor("v1|2026-09-17T12:00:00Z|entry\nbroken"),
		signedHistoryCursor("v1|2026-09-17T12:00:00Z|entry-a|extra"),
		signedHistoryCursor("v1|2026-09-17T12:00:00Z|entry broken"),
		signedHistoryCursor("v1|2026-09-17T12:00:00Z|entry\x7fbroken"),
	}
	for _, raw := range invalid {
		if _, err := codec.Decode(raw); !errors.Is(err, application.ErrInvalidCursor) {
			t.Errorf("Decode(%q) error = %v, want ErrInvalidCursor", raw, err)
		}
	}

	// The printable range edges are valid identifier bytes (mutation
	// gate: history_cursor.go:97).
	edged, err := codec.Decode(signedHistoryCursor("v1|2026-09-17T12:00:00Z|edge!~id"))
	if err != nil || edged.ConsumptionID != "edge!~id" {
		t.Fatalf("edge cursor = (%+v, %v), want edge!~id decoded", edged, err)
	}
}

func TestGetArenaPassHistoryUseCasePaginatesWithoutDuplicatesOrGaps(t *testing.T) {
	repo := &fakePassLotQueryRepo{consumptions: historyRecords(t, 5)}
	useCase := newTestHistoryUseCase(t, repo)

	seen := make([]string, 0, 5)
	cursor := ""
	for page := 0; page < 10; page++ {
		history, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), cursor, 2)
		if err != nil {
			t.Fatalf("page %d Execute() error = %v", page, err)
		}
		for _, entry := range history.Entries {
			seen = append(seen, entry.ConsumptionID)
		}
		if history.NextCursor == "" {
			break
		}
		cursor = history.NextCursor
	}

	if len(seen) != 5 {
		t.Fatalf("entries delivered = %d, want 5 (no duplicates, no gaps)", len(seen))
	}
	unique := map[string]bool{}
	for _, id := range seen {
		if unique[id] {
			t.Fatalf("duplicate entry %q", id)
		}
		unique[id] = true
	}
	for _, record := range repo.consumptions {
		if !unique[record.ConsumptionID] {
			t.Fatalf("entry %q was skipped", record.ConsumptionID)
		}
	}
	if repo.lastLimit != 3 {
		t.Errorf("last page limit = %d, want 3 (limit+1 lookahead)", repo.lastLimit)
	}
	if repo.lastAfter == nil || repo.lastAfter.ConsumptionID != repo.consumptions[3].ConsumptionID {
		t.Errorf("last cursor = %+v, want the last delivered entry", repo.lastAfter)
	}
}

func TestGetArenaPassHistoryUseCaseClampsLimitsAndValidates(t *testing.T) {
	repo := &fakePassLotQueryRepo{consumptions: historyRecords(t, 2)}
	useCase := newTestHistoryUseCase(t, repo)

	if _, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), "", 0); err != nil {
		t.Fatalf("default limit error = %v", err)
	}
	if repo.lastLimit != application.DefaultPassHistoryLimit+1 {
		t.Errorf("default limit + lookahead = %d, want %d", repo.lastLimit, application.DefaultPassHistoryLimit+1)
	}

	if _, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), "", 5000); err != nil {
		t.Fatalf("maximum limit error = %v", err)
	}
	if repo.lastLimit != application.MaxPassHistoryLimit+1 {
		t.Errorf("clamped limit + lookahead = %d, want %d", repo.lastLimit, application.MaxPassHistoryLimit+1)
	}

	pagesBefore := repo.lastLimit
	if _, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), "%%not-a-cursor", 5); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("invalid cursor error = %v, want ErrInvalidCursor", err)
	}
	if repo.lastLimit != pagesBefore {
		t.Error("invalid cursor must not reach the repository")
	}

	if _, err := useCase.Execute(context.Background(), domain.AccountID(""), "", 5); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
	}

	repo.historyErr = errors.New("storage unavailable")
	if _, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), "", 5); !errors.Is(err, repo.historyErr) {
		t.Fatalf("repository error not propagated: %v", err)
	}
}

func TestGetArenaPassHistoryExactPageCarriesNoCursor(t *testing.T) {
	repo := &fakePassLotQueryRepo{consumptions: historyRecords(t, 2)}
	useCase := newTestHistoryUseCase(t, repo)

	// Exactly the requested rows complete the page: no cursor may point
	// past it (mutation gate: get_pass_history.go:50).
	history, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), "", 2)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(history.Entries) != 2 || history.NextCursor != "" {
		t.Fatalf("exact page = %d entries/cursor %q, want two with no cursor", len(history.Entries), history.NextCursor)
	}
}
