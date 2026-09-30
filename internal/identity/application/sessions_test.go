package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// sessionFixture is the state the session use cases are tested against: one
// account with a password credential and sessions at chosen instants.
type sessionFixture struct {
	ctx      context.Context
	clock    *fakeClock
	accounts *inMemoryAccountRepo
	creds    *inMemoryCredentialRepo
	sessions *inMemorySessionRepo
	account  *domain.Account
	otherAcc *domain.Account
	password string
	policy   domain.SessionPolicy
}

func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	ctx := context.Background()
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	accounts := newInMemoryAccountRepo()
	creds := newInMemoryCredentialRepo()
	sessions := newInMemorySessionRepo()
	password := "CorrectHorse123!"

	account := mustSessionAccount(t, ctx, accounts, creds, "owner@arena.local", password, clock.Now())
	other := mustSessionAccount(t, ctx, accounts, creds, "other@arena.local", password, clock.Now())

	return &sessionFixture{
		ctx: ctx, clock: clock, accounts: accounts, creds: creds, sessions: sessions,
		account: account, otherAcc: other, password: password, policy: domain.DefaultSessionPolicy(),
	}
}

func mustSessionAccount(t *testing.T, ctx context.Context, accounts *inMemoryAccountRepo, creds *inMemoryCredentialRepo, address, password string, now time.Time) *domain.Account {
	t.Helper()
	email, err := domain.ParseEmail(address)
	if err != nil {
		t.Fatalf("parse email: %v", err)
	}
	account, err := accounts.CreateAccountWithPassword(ctx, email, "hashed_"+password)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := account.VerifyEmail(now); err != nil {
		t.Fatalf("verify email: %v", err)
	}
	accounts.accounts[string(account.ID())] = account
	creds.credentials[string(account.ID())] = &application.PasswordCredentialRecord{
		AccountID:    account.ID(),
		PasswordHash: "hashed_" + password,
		Algorithm:    "argon2id",
		Version:      1,
	}
	return account
}

// seedSession inserts a session with the instants the test cares about, which
// is what lets an idle-expired and an absolute-expired session be present
// without waiting for a clock.
func (f *sessionFixture) seedSession(t *testing.T, id string, account *domain.Account, createdAt, lastSeenAt time.Time, revoked bool) *domain.Session {
	t.Helper()
	var revokedAt *time.Time
	if revoked {
		at := createdAt
		revokedAt = &at
	}
	session, err := domain.ReconstituteSession(
		domain.SessionID(id),
		account.ID(),
		[]byte("token-hash-"+id),
		createdAt,
		f.policy.ExpiryInstant(createdAt, lastSeenAt),
		lastSeenAt,
		revokedAt,
		"198.51.100.7",
		"ArenaClient/1.0 ("+id+")",
	)
	if err != nil {
		t.Fatalf("reconstitute session %s: %v", id, err)
	}
	f.sessions.sessions[id] = session
	return session
}

func (f *sessionFixture) isRevoked(t *testing.T, id string) bool {
	t.Helper()
	session, ok := f.sessions.sessions[id]
	if !ok {
		t.Fatalf("session %s is missing from the fixture", id)
	}
	return session.IsRevoked()
}

func TestListSessionsReturnsOnlyTheUsableSessionsOfTheAccount(t *testing.T) {
	fixture := newSessionFixture(t)
	now := fixture.clock.Now()

	fixture.seedSession(t, "live", fixture.account, now.Add(-time.Hour), now.Add(-time.Minute), false)
	fixture.seedSession(t, "idle", fixture.account, now.Add(-48*time.Hour), now.Add(-25*time.Hour), false)
	fixture.seedSession(t, "absolute", fixture.account, now.Add(-15*24*time.Hour), now, false)
	fixture.seedSession(t, "revoked", fixture.account, now.Add(-time.Hour), now.Add(-time.Minute), true)
	fixture.seedSession(t, "foreign", fixture.otherAcc, now.Add(-time.Hour), now.Add(-time.Minute), false)

	useCase := application.NewListSessionsUseCase(fixture.sessions, fixture.clock, fixture.policy)
	summaries, err := useCase.Execute(fixture.ctx, application.ListSessionsCommand{
		AccountID:        fixture.account.ID(),
		CurrentSessionID: "live",
	})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("listed sessions = %d (%+v), want only the usable one of the account", len(summaries), summaries)
	}
	if summaries[0].ID.String() != "live" || !summaries[0].Current {
		t.Fatalf("listed session = %+v, want the calling session marked as current", summaries[0])
	}
	if summaries[0].IPAddress != "198.51.100.7" || !strings.Contains(summaries[0].UserAgent, "live") {
		t.Fatalf("listed session = %+v, want the facts the session recorded", summaries[0])
	}
}

func TestListSessionsBoundsTheUserAgent(t *testing.T) {
	fixture := newSessionFixture(t)
	now := fixture.clock.Now()
	session := fixture.seedSession(t, "live", fixture.account, now.Add(-time.Hour), now, false)

	oversized := strings.Repeat("a", 4096)
	tampered, err := domain.ReconstituteSession(
		session.ID(), session.AccountID(), session.TokenHash(),
		session.CreatedAt(), session.ExpiresAt(), session.LastSeenAt(), nil,
		session.IPAddress(), oversized,
	)
	if err != nil {
		t.Fatalf("reconstitute tampered session: %v", err)
	}
	fixture.sessions.sessions["live"] = tampered

	useCase := application.NewListSessionsUseCase(fixture.sessions, fixture.clock, fixture.policy)
	summaries, err := useCase.Execute(fixture.ctx, application.ListSessionsCommand{AccountID: fixture.account.ID()})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("listed sessions = %d, want 1", len(summaries))
	}
	if got := len(summaries[0].UserAgent); got != 256 {
		t.Fatalf("user agent length = %d, want the bound of 256", got)
	}
}

func TestListSessionsRefusesAnEmptyAccount(t *testing.T) {
	fixture := newSessionFixture(t)
	useCase := application.NewListSessionsUseCase(fixture.sessions, fixture.clock, fixture.policy)
	if _, err := useCase.Execute(fixture.ctx, application.ListSessionsCommand{}); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("list without an account: error = %v, want ErrEmptyAccountID", err)
	}
}

func TestRevokeSessionRequiresThePasswordAndChangesNothingWithoutIt(t *testing.T) {
	fixture := newSessionFixture(t)
	now := fixture.clock.Now()
	fixture.seedSession(t, "caller", fixture.account, now.Add(-time.Hour), now, false)
	fixture.seedSession(t, "target", fixture.account, now.Add(-time.Hour), now, false)

	useCase := application.NewRevokeSessionUseCase(fixture.sessions, fixture.creds, fakePasswordHasher{})
	for _, password := range []string{"", "   ", "WrongPassword!"} {
		err := useCase.Execute(fixture.ctx, application.RevokeSessionCommand{
			AccountID:        fixture.account.ID(),
			SessionID:        "target",
			CurrentSessionID: "caller",
			Password:         password,
		})
		if !errors.Is(err, application.ErrReauthFailed) {
			t.Fatalf("revoke with password %q: error = %v, want ErrReauthFailed", password, err)
		}
		if fixture.isRevoked(t, "target") {
			t.Fatalf("revoke with password %q ended the session: a refused re-authentication must change nothing", password)
		}
	}
}

func TestRevokeSessionFailsClosedWhenTheCredentialIsMissing(t *testing.T) {
	fixture := newSessionFixture(t)
	now := fixture.clock.Now()
	fixture.seedSession(t, "caller", fixture.account, now.Add(-time.Hour), now, false)
	fixture.seedSession(t, "target", fixture.account, now.Add(-time.Hour), now, false)
	delete(fixture.creds.credentials, string(fixture.account.ID()))

	useCase := application.NewRevokeSessionUseCase(fixture.sessions, fixture.creds, fakePasswordHasher{})
	err := useCase.Execute(fixture.ctx, application.RevokeSessionCommand{
		AccountID:        fixture.account.ID(),
		SessionID:        "target",
		CurrentSessionID: "caller",
		Password:         fixture.password,
	})
	if !errors.Is(err, application.ErrReauthFailed) {
		t.Fatalf("revoke without a stored credential: error = %v, want ErrReauthFailed", err)
	}
	if fixture.isRevoked(t, "target") {
		t.Fatal("a missing credential let the revocation through")
	}
}

func TestRevokeSessionEndsOnlyTheAddressedSessionOfTheAccount(t *testing.T) {
	fixture := newSessionFixture(t)
	now := fixture.clock.Now()
	fixture.seedSession(t, "caller", fixture.account, now.Add(-time.Hour), now, false)
	fixture.seedSession(t, "target", fixture.account, now.Add(-time.Hour), now, false)
	fixture.seedSession(t, "bystander", fixture.account, now.Add(-time.Hour), now, false)
	foreign := fixture.seedSession(t, "foreign", fixture.otherAcc, now.Add(-time.Hour), now, false)

	useCase := application.NewRevokeSessionUseCase(fixture.sessions, fixture.creds, fakePasswordHasher{})

	if err := useCase.Execute(fixture.ctx, application.RevokeSessionCommand{
		AccountID: fixture.account.ID(), SessionID: "target", CurrentSessionID: "caller", Password: fixture.password,
	}); err != nil {
		t.Fatalf("revoke the addressed session: %v", err)
	}
	if !fixture.isRevoked(t, "target") {
		t.Fatal("the addressed session is still usable after a revocation")
	}
	if fixture.isRevoked(t, "caller") || fixture.isRevoked(t, "bystander") {
		t.Fatal("revoking one session ended another session of the account")
	}

	// Another account's session is not addressable, and the refusal is the
	// same as an unknown identifier: the endpoint cannot be asked whether a
	// session exists.
	err := useCase.Execute(fixture.ctx, application.RevokeSessionCommand{
		AccountID: fixture.account.ID(), SessionID: foreign.ID(), CurrentSessionID: "caller", Password: fixture.password,
	})
	if !errors.Is(err, application.ErrSessionNotFound) {
		t.Fatalf("revoke another account's session: error = %v, want ErrSessionNotFound", err)
	}
	if fixture.isRevoked(t, "foreign") {
		t.Fatal("a foreign session was ended by another account's request")
	}

	err = useCase.Execute(fixture.ctx, application.RevokeSessionCommand{
		AccountID: fixture.account.ID(), SessionID: "does-not-exist", CurrentSessionID: "caller", Password: fixture.password,
	})
	if !errors.Is(err, application.ErrSessionNotFound) {
		t.Fatalf("revoke an unknown session: error = %v, want ErrSessionNotFound", err)
	}
}

func TestRevokeSessionRefusesTheCurrentSession(t *testing.T) {
	fixture := newSessionFixture(t)
	now := fixture.clock.Now()
	fixture.seedSession(t, "caller", fixture.account, now.Add(-time.Hour), now, false)

	useCase := application.NewRevokeSessionUseCase(fixture.sessions, fixture.creds, fakePasswordHasher{})
	err := useCase.Execute(fixture.ctx, application.RevokeSessionCommand{
		AccountID: fixture.account.ID(), SessionID: "caller", CurrentSessionID: "caller", Password: fixture.password,
	})
	if !errors.Is(err, application.ErrCannotRevokeCurrentSession) {
		t.Fatalf("revoke the calling session: error = %v, want ErrCannotRevokeCurrentSession", err)
	}
	if fixture.isRevoked(t, "caller") {
		t.Fatal("the calling session was ended by the revocation route")
	}
}

func TestCleanupSessionsRevokesOnlyTheSessionsPastTheirDeadline(t *testing.T) {
	fixture := newSessionFixture(t)
	now := fixture.clock.Now()
	fixture.seedSession(t, "live", fixture.account, now.Add(-time.Hour), now.Add(-time.Minute), false)
	fixture.seedSession(t, "idle", fixture.account, now.Add(-48*time.Hour), now.Add(-25*time.Hour), false)
	fixture.seedSession(t, "absolute", fixture.account, now.Add(-15*24*time.Hour), now, false)

	useCase := application.NewCleanupSessionsUseCase(fixture.sessions, fixture.clock, fixture.policy)
	revoked, err := useCase.Execute(fixture.ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if revoked != 2 {
		t.Fatalf("revoked sessions = %d, want the idle and absolute ones only", revoked)
	}
	if fixture.isRevoked(t, "live") {
		t.Fatal("the cleanup ended a session that was still inside its policy deadlines")
	}
	if !fixture.isRevoked(t, "idle") || !fixture.isRevoked(t, "absolute") {
		t.Fatal("the cleanup left a session past its deadline usable")
	}

	// The pass deletes nothing, so the rows the retention policy owns are
	// still there for it.
	if len(fixture.sessions.sessions) != 3 {
		t.Fatalf("sessions after the cleanup = %d, want the rows preserved", len(fixture.sessions.sessions))
	}

	again, err := useCase.Execute(fixture.ctx)
	if err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if again != 0 {
		t.Fatalf("second cleanup revoked %d sessions, want 0: the pass must be idempotent", again)
	}
}
