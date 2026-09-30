package application_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

var testCursorSecret = []byte("0123456789abcdef0123456789abcdef")

func newTestStatementUseCase(t *testing.T, queries application.WalletQueryRepository) *application.GetWalletStatementUseCase {
	t.Helper()
	codec, err := application.NewStatementCursorCodec(testCursorSecret)
	if err != nil {
		t.Fatalf("build cursor codec: %v", err)
	}
	return application.NewGetWalletStatementUseCase(queries, codec)
}

func signedCursor(payload string) string {
	mac := hmac.New(sha256.New, testCursorSecret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type fakeQueryRepo struct {
	balance    *application.WalletBalance
	balanceErr error
	entries    []application.StatementEntry
	pageErr    error

	lastAccount domain.AccountID
	lastAfter   *application.StatementPosition
	lastLimit   int
	pages       int
}

func (r *fakeQueryRepo) DerivedBalance(_ context.Context, accountID domain.AccountID) (*application.WalletBalance, error) {
	r.lastAccount = accountID
	if r.balanceErr != nil {
		return nil, r.balanceErr
	}
	return r.balance, nil
}

func (r *fakeQueryRepo) ListStatementPage(_ context.Context, accountID domain.AccountID, after *application.StatementPosition, limit int) ([]application.StatementEntry, error) {
	r.lastAccount = accountID
	r.lastAfter = after
	r.lastLimit = limit
	r.pages++
	if r.pageErr != nil {
		return nil, r.pageErr
	}

	start := 0
	if after != nil {
		for i, entry := range r.entries {
			if entry.TransactionID == after.TransactionID {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	if end > len(r.entries) {
		end = len(r.entries)
	}
	page := append([]application.StatementEntry(nil), r.entries[start:end]...)
	return page, nil
}

func statementEntries(t *testing.T, count int) []application.StatementEntry {
	t.Helper()
	entries := make([]application.StatementEntry, count)
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for i := range entries {
		entries[i] = application.StatementEntry{
			TransactionID: "transaction-" + string(rune('a'+i)),
			OperationID:   "operation-" + string(rune('a'+i)),
			OperationType: domain.OperationCreditFree,
			Reference:     mustReference(t, "free:2026-09"),
			Bucket:        domain.BucketFree,
			Amount:        100,
			CreatedAt:     base.Add(-time.Duration(i) * time.Minute),
		}
	}
	return entries
}

func TestGetWalletBalanceUseCase(t *testing.T) {
	repo := &fakeQueryRepo{balance: &application.WalletBalance{
		Free:      mustInk(t, 5000),
		Purchased: mustInk(t, 10000),
	}}
	useCase := application.NewGetWalletBalanceUseCase(repo)

	balance, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if balance.Free.Int64() != 5000 || balance.Purchased.Int64() != 10000 {
		t.Fatalf("balance = %d/%d, want 5000/10000", balance.Free.Int64(), balance.Purchased.Int64())
	}
	if repo.lastAccount.String() != testAccountID {
		t.Errorf("queried account = %q", repo.lastAccount)
	}

	if _, err := useCase.Execute(context.Background(), domain.AccountID("")); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
	}

	repo.balanceErr = errors.New("ledger unavailable")
	if _, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID)); !errors.Is(err, repo.balanceErr) {
		t.Fatalf("repository error not propagated: %v", err)
	}
}

func TestWalletStatementPaginatesWithoutDuplicatesOrGaps(t *testing.T) {
	repo := &fakeQueryRepo{entries: statementEntries(t, 5)}
	useCase := newTestStatementUseCase(t, repo)

	seen := make([]string, 0, 5)
	cursor := ""
	for page := 0; page < 10; page++ {
		statement, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), cursor, 2)
		if err != nil {
			t.Fatalf("page %d Execute() error = %v", page, err)
		}
		for _, entry := range statement.Entries {
			seen = append(seen, entry.TransactionID)
		}
		if statement.NextCursor == "" {
			break
		}
		cursor = statement.NextCursor
	}

	if len(seen) != 5 {
		t.Fatalf("entries delivered = %d, want 5 (no duplicates, no gaps)", len(seen))
	}
	unique := map[string]bool{}
	for _, id := range seen {
		if unique[id] {
			t.Fatalf("duplicate entry %q in statement", id)
		}
		unique[id] = true
	}
	for i, entry := range repo.entries {
		if !unique[entry.TransactionID] {
			t.Fatalf("entry %d (%s) was skipped", i, entry.TransactionID)
		}
	}
	if repo.lastLimit != 3 {
		t.Errorf("last page limit requested = %d, want 3 (limit+1 lookahead)", repo.lastLimit)
	}
	if repo.lastAfter == nil || repo.lastAfter.TransactionID != "transaction-d" {
		t.Errorf("last cursor position = %+v, want the last delivered entry", repo.lastAfter)
	}
}

func TestWalletStatementCursorRoundTrip(t *testing.T) {
	repo := &fakeQueryRepo{entries: statementEntries(t, 3)}
	useCase := newTestStatementUseCase(t, repo)

	first, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), "", 1)
	if err != nil {
		t.Fatalf("first page error = %v", err)
	}
	if len(first.Entries) != 1 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want one entry plus a cursor", first)
	}
	if repo.lastAfter != nil {
		t.Fatalf("first page position = %+v, want nil", repo.lastAfter)
	}

	second, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), first.NextCursor, 1)
	if err != nil {
		t.Fatalf("second page error = %v", err)
	}
	if repo.lastAfter == nil {
		t.Fatal("second page must carry a decoded position")
	}
	if repo.lastAfter.TransactionID != first.Entries[0].TransactionID {
		t.Fatalf("decoded cursor = %q, want %q", repo.lastAfter.TransactionID, first.Entries[0].TransactionID)
	}
	if !repo.lastAfter.CreatedAt.Equal(first.Entries[0].CreatedAt) {
		t.Fatalf("decoded cursor time = %v, want %v", repo.lastAfter.CreatedAt, first.Entries[0].CreatedAt)
	}
	if len(second.Entries) != 1 || second.Entries[0].TransactionID == first.Entries[0].TransactionID {
		t.Fatalf("second page = %+v, want the next entry", second.Entries)
	}

	// The last page reports no further cursor.
	last, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), second.NextCursor, 5)
	if err != nil {
		t.Fatalf("last page error = %v", err)
	}
	if last.NextCursor != "" {
		t.Fatalf("last page cursor = %q, want empty", last.NextCursor)
	}
}

func TestWalletStatementLimitClamping(t *testing.T) {
	repo := &fakeQueryRepo{entries: statementEntries(t, 3)}
	useCase := newTestStatementUseCase(t, repo)

	if _, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), "", 0); err != nil {
		t.Fatalf("default limit error = %v", err)
	}
	if repo.lastLimit != application.DefaultStatementLimit+1 {
		t.Errorf("default limit + lookahead = %d, want %d", repo.lastLimit, application.DefaultStatementLimit+1)
	}

	if _, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), "", 5000); err != nil {
		t.Fatalf("maximum limit error = %v", err)
	}
	if repo.lastLimit != application.MaxStatementLimit+1 {
		t.Errorf("clamped limit + lookahead = %d, want %d", repo.lastLimit, application.MaxStatementLimit+1)
	}
}

func TestWalletStatementRejectsInvalidCursors(t *testing.T) {
	repo := &fakeQueryRepo{entries: statementEntries(t, 1)}
	useCase := newTestStatementUseCase(t, repo)

	notBase64 := "%%%"
	unsigned := base64.RawURLEncoding.EncodeToString([]byte("v1|2026-09-17T12:00:00Z|transaction-a"))
	forgedMac := hmac.New(sha256.New, []byte("a-different-secret-key-32-bytes-long"))
	forgedMac.Write([]byte("v1|2026-09-17T12:00:00Z|transaction-a"))
	forged := base64.RawURLEncoding.EncodeToString([]byte("v1|2026-09-17T12:00:00Z|transaction-a")) +
		"." + base64.RawURLEncoding.EncodeToString(forgedMac.Sum(nil))
	wrongVersion := signedCursor("v9|2026-09-17T12:00:00Z|transaction-a")
	badTime := signedCursor("v1|not-a-time|transaction-a")
	emptyID := signedCursor("v1|2026-09-17T12:00:00Z|")
	nonASCII := signedCursor("v1|2026-09-17T12:00:00Z|trans\nação")
	tooManyParts := signedCursor("v1|2026-09-17T12:00:00Z|transaction-a|extra")
	spaceID := signedCursor("v1|2026-09-17T12:00:00Z|trans action")
	delID := signedCursor("v1|2026-09-17T12:00:00Z|trans\x7faction")

	for _, cursor := range []string{notBase64, unsigned, forged, wrongVersion, badTime, emptyID, nonASCII, tooManyParts, spaceID, delID} {
		if _, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), cursor, 5); !errors.Is(err, application.ErrInvalidCursor) {
			t.Errorf("cursor %q error = %v, want ErrInvalidCursor", cursor, err)
		}
	}
	if repo.pages != 0 {
		t.Fatalf("repository was queried %d times for invalid cursors", repo.pages)
	}

	// The printable range edges are valid identifier bytes: a cursor
	// carrying '!' or '~' decodes and reaches the repository (mutation
	// gate: statement_cursor.go:97).
	for _, cursor := range []string{
		signedCursor("v1|2026-09-17T12:00:00Z|trans!action"),
		signedCursor("v1|2026-09-17T12:00:00Z|trans~action"),
	} {
		if _, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), cursor, 5); err != nil {
			t.Errorf("edge cursor %q error = %v, want success", cursor, err)
		}
	}

	if _, err := useCase.Execute(context.Background(), domain.AccountID(""), "", 5); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
	}

	repo.pageErr = errors.New("ledger unavailable")
	if _, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID), "", 5); !errors.Is(err, repo.pageErr) {
		t.Fatalf("repository error not propagated: %v", err)
	}
}
