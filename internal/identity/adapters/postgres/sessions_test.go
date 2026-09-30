package postgres_test

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	identitypg "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// harness is the storage fixture of these tests: two accounts and the ability
// to place a session at a chosen instant in the past, which is what the idle
// and absolute deadlines need.
type sessionStoreHarness struct {
	ctx    context.Context
	repo   *identitypg.Repository
	db     *dbtest.TestDB
	policy domain.SessionPolicy
	first  *domain.Account
	second *domain.Account
}

func newSessionStoreHarness(t *testing.T) *sessionStoreHarness {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	first := createSessionStoreAccount(t, ctx, repo, "listing-owner@example.com")
	second := createSessionStoreAccount(t, ctx, repo, "listing-other@example.com")

	return &sessionStoreHarness{ctx: ctx, repo: repo, db: testDB, policy: domain.DefaultSessionPolicy(), first: first, second: second}
}

func createSessionStoreAccount(t *testing.T, ctx context.Context, repo *identitypg.Repository, address string) *domain.Account {
	t.Helper()
	email, err := domain.ParseEmail(address)
	if err != nil {
		t.Fatalf("parse email: %v", err)
	}
	account, err := repo.CreateAccountWithPassword(ctx, email, "$argon2id$v=19$m=8192,t=1,p=1$fakeSalt$fakeHash")
	if err != nil {
		t.Fatalf("create account %s: %v", address, err)
	}
	return account
}

// createSession stores a session and then moves its instants, so a session can
// be idle-expired or past its absolute lifetime without waiting.
func (h *sessionStoreHarness) createSession(t *testing.T, account *domain.Account, token, userAgent string, createdAgo, seenAgo time.Duration) *domain.Session {
	t.Helper()
	hash := sha256.Sum256([]byte(token))
	session, err := h.repo.CreateSession(h.ctx, account.ID(), hash[:], time.Now().UTC().Add(h.policy.AbsoluteLifetime), "198.51.100.11", userAgent)
	if err != nil {
		t.Fatalf("create session %s: %v", token, err)
	}
	if _, err := h.db.Pool.Pool().Exec(h.ctx,
		`UPDATE app.sessions SET created_at = now() - $2::interval, last_seen_at = now() - $3::interval, expires_at = (now() - $3::interval) + $4::interval WHERE id = $1`,
		session.ID().String(), createdAgo.String(), seenAgo.String(), h.policy.IdleTimeout.String(),
	); err != nil {
		t.Fatalf("place session %s in time: %v", token, err)
	}
	return session
}

func TestRepository_SessionListingIsScopedToTheAccountAndTheWindow(t *testing.T) {
	harness := newSessionStoreHarness(t)

	live := harness.createSession(t, harness.first, "live-token", "ArenaClient/1.0 (live)", time.Hour, time.Minute)
	harness.createSession(t, harness.first, "idle-token", "ArenaClient/1.0 (idle)", 48*time.Hour, 25*time.Hour)
	harness.createSession(t, harness.first, "absolute-token", "ArenaClient/1.0 (absolute)", 15*24*time.Hour, time.Minute)
	revoked := harness.createSession(t, harness.first, "revoked-token", "ArenaClient/1.0 (revoked)", time.Hour, time.Minute)
	if _, err := harness.db.Pool.Pool().Exec(harness.ctx, `UPDATE app.sessions SET revoked_at = now() WHERE id = $1`, revoked.ID().String()); err != nil {
		t.Fatalf("revoke the fixture session: %v", err)
	}
	harness.createSession(t, harness.second, "foreign-token", "ArenaClient/1.0 (foreign)", time.Hour, time.Minute)

	window := application.SessionWindowFor(time.Now().UTC(), harness.policy, application.DefaultSessionListingRows)
	records, err := harness.repo.ListActiveSessions(harness.ctx, harness.first.ID(), window)
	if err != nil {
		t.Fatalf("list active sessions: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("listed sessions = %d (%+v), want only the usable session of the account", len(records), records)
	}
	if records[0].ID != live.ID() {
		t.Fatalf("listed session = %s, want %s", records[0].ID, live.ID())
	}
	if records[0].IPAddress != "198.51.100.11" || records[0].UserAgent != "ArenaClient/1.0 (live)" {
		t.Fatalf("listed session = %+v, want the recorded address and user agent", records[0])
	}
	if records[0].CreatedAt.IsZero() || records[0].LastSeenAt.IsZero() || records[0].ExpiresAt.IsZero() {
		t.Fatalf("listed session = %+v, want the three instants", records[0])
	}
}

func TestRepository_SessionListingIsBoundedAndOrderedByActivity(t *testing.T) {
	harness := newSessionStoreHarness(t)

	older := harness.createSession(t, harness.first, "older-token", "ArenaClient/1.0 (older)", 3*time.Hour, 2*time.Hour)
	newer := harness.createSession(t, harness.first, "newer-token", "ArenaClient/1.0 (newer)", time.Hour, time.Minute)

	window := application.SessionWindowFor(time.Now().UTC(), harness.policy, 1)
	records, err := harness.repo.ListActiveSessions(harness.ctx, harness.first.ID(), window)
	if err != nil {
		t.Fatalf("list active sessions: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("listed sessions = %d, want the listing bounded to one row", len(records))
	}
	if records[0].ID != newer.ID() {
		t.Fatalf("bounded listing returned %s, want the most recently seen %s", records[0].ID, newer.ID())
	}

	records, err = harness.repo.ListActiveSessions(harness.ctx, harness.first.ID(), application.SessionWindowFor(time.Now().UTC(), harness.policy, 0))
	if err != nil {
		t.Fatalf("list active sessions without a bound: %v", err)
	}
	if len(records) != 2 || records[0].ID != newer.ID() || records[1].ID != older.ID() {
		t.Fatalf("listing = %+v, want both sessions, most recently seen first", records)
	}
}

func TestRepository_RevokeSessionByIDIsScopedAndSingleWinner(t *testing.T) {
	harness := newSessionStoreHarness(t)

	own := harness.createSession(t, harness.first, "own-token", "ArenaClient/1.0", time.Hour, time.Minute)
	foreign := harness.createSession(t, harness.second, "foreign-token", "ArenaClient/1.0", time.Hour, time.Minute)

	// Another account's identifier is not addressable.
	revoked, err := harness.repo.RevokeSessionByID(harness.ctx, harness.first.ID(), foreign.ID())
	if err != nil {
		t.Fatalf("revoke a foreign session: %v", err)
	}
	if revoked {
		t.Fatal("another account's session was revoked")
	}

	// The first revoke changes the row, every later one reports none.
	revoked, err = harness.repo.RevokeSessionByID(harness.ctx, harness.first.ID(), own.ID())
	if err != nil || !revoked {
		t.Fatalf("first revoke = (%v, %v), want (true, nil)", revoked, err)
	}
	revoked, err = harness.repo.RevokeSessionByID(harness.ctx, harness.first.ID(), own.ID())
	if err != nil || revoked {
		t.Fatalf("second revoke = (%v, %v), want (false, nil)", revoked, err)
	}

	// Concurrent revokes of the same session agree on exactly one winner: the
	// statement is the whole decision, so no caller can observe a half-applied
	// transition.
	contended := harness.createSession(t, harness.first, "contended-token", "ArenaClient/1.0", time.Hour, time.Minute)
	const workers = 8
	var wg sync.WaitGroup
	results := make([]bool, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			results[worker], errs[worker] = harness.repo.RevokeSessionByID(harness.ctx, harness.first.ID(), contended.ID())
		}(i)
	}
	wg.Wait()

	winners := 0
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if results[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent revokes reported %d winners, want exactly 1", winners)
	}
}

func TestRepository_CleanupRevokesPastDeadlineWithoutDeleting(t *testing.T) {
	harness := newSessionStoreHarness(t)

	live := harness.createSession(t, harness.first, "live-token", "ArenaClient/1.0 (live)", time.Hour, time.Minute)
	idle := harness.createSession(t, harness.first, "idle-token", "ArenaClient/1.0 (idle)", 48*time.Hour, 25*time.Hour)
	absolute := harness.createSession(t, harness.first, "absolute-token", "ArenaClient/1.0 (absolute)", 15*24*time.Hour, time.Minute)
	already := harness.createSession(t, harness.first, "revoked-token", "ArenaClient/1.0 (revoked)", 40*time.Hour, 30*time.Hour)
	if _, err := harness.db.Pool.Pool().Exec(harness.ctx, `UPDATE app.sessions SET revoked_at = now() WHERE id = $1`, already.ID().String()); err != nil {
		t.Fatalf("revoke the fixture session: %v", err)
	}
	other := harness.createSession(t, harness.second, "foreign-token", "ArenaClient/1.0 (foreign)", 48*time.Hour, 25*time.Hour)

	window := application.SessionWindowFor(time.Now().UTC(), harness.policy, 0)
	revoked, err := harness.repo.RevokeSessionsPastDeadline(harness.ctx, window)
	if err != nil {
		t.Fatalf("revoke sessions past deadline: %v", err)
	}
	if revoked != 3 {
		t.Fatalf("revoked sessions = %d, want the idle, absolute and foreign ones", revoked)
	}

	// Idempotence: the same instant sweeps nothing the second time.
	again, err := harness.repo.RevokeSessionsPastDeadline(harness.ctx, window)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if again != 0 {
		t.Fatalf("second sweep revoked %d sessions, want 0", again)
	}

	// The live session is untouched, and no row was removed: deletion belongs
	// to the retention pass, with its own window and holds.
	var liveRow int
	if err := harness.db.Pool.Pool().QueryRow(harness.ctx,
		`SELECT count(*) FROM app.sessions WHERE id = $1 AND revoked_at IS NULL`, live.ID().String()).Scan(&liveRow); err != nil {
		t.Fatalf("count the live session: %v", err)
	}
	if liveRow != 1 {
		t.Fatal("the sweep ended a session that was still inside its policy deadlines")
	}
	for _, session := range []*domain.Session{idle, absolute, other} {
		var revokedAt *time.Time
		if err := harness.db.Pool.Pool().QueryRow(harness.ctx,
			`SELECT revoked_at FROM app.sessions WHERE id = $1`, session.ID().String()).Scan(&revokedAt); err != nil {
			t.Fatalf("read the row of %s: %v", session.ID(), err)
		}
		if revokedAt == nil {
			t.Fatalf("session %s is still active after the sweep", session.ID())
		}
	}
}
