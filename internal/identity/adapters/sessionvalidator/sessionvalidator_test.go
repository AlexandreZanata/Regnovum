package sessionvalidator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/sessionvalidator"
	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// sessions is the use case double: it answers one recorded result or one error,
// which is all the adapter asks of the identity module.
type sessions struct {
	result *application.AuthenticateSessionResult
	err    error
	seen   string
}

func (fake *sessions) Execute(_ context.Context, command application.AuthenticateSessionCommand) (*application.AuthenticateSessionResult, error) {
	fake.seen = command.RawToken
	if fake.err != nil {
		return nil, fake.err
	}
	return fake.result, nil
}

// storedSession builds the pair the use case answers with.
func storedSession(t *testing.T) *application.AuthenticateSessionResult {
	t.Helper()

	email, err := identitydomain.ParseEmail("participant@example.test")
	if err != nil {
		t.Fatalf("ParseEmail() error = %v", err)
	}
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	account, err := identitydomain.ReconstituteAccount(
		identitydomain.AccountID("00000000-0000-0000-0000-0000000000c1"), email,
		identitydomain.AccountStatusActive, &now, now, now,
	)
	if err != nil {
		t.Fatalf("ReconstituteAccount() error = %v", err)
	}
	session, err := identitydomain.ReconstituteSession(
		identitydomain.SessionID("00000000-0000-0000-0000-0000000000c2"),
		account.ID(), []byte{0x01}, now, now.Add(time.Hour), now, nil, "127.0.0.1", "test",
	)
	if err != nil {
		t.Fatalf("ReconstituteSession() error = %v", err)
	}
	return &application.AuthenticateSessionResult{Account: account, Session: session}
}

// TestNewAnswersWithTheIdentityOfTheSession is the adapter's whole contract:
// the raw token is handed to the use case and its answer becomes the identity
// the middleware attaches to the request.
func TestNewAnswersWithTheIdentityOfTheSession(t *testing.T) {
	t.Parallel()

	fake := &sessions{result: storedSession(t)}
	validate, err := sessionvalidator.New(fake)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	identity, err := validate(context.Background(), "raw-session-token")
	if err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if fake.seen != "raw-session-token" {
		t.Errorf("the use case received %q, want the raw token", fake.seen)
	}
	if identity.AccountID != "00000000-0000-0000-0000-0000000000c1" {
		t.Errorf("identity.AccountID = %q, want the stored account", identity.AccountID)
	}
	if identity.SessionID != "00000000-0000-0000-0000-0000000000c2" {
		t.Errorf("identity.SessionID = %q, want the stored session", identity.SessionID)
	}
}

// TestTheRefusalOfTheUseCaseReachesTheCaller: the adapter must not translate a
// refused session into an anonymous one, which is the difference between a page
// that asks a person to sign in and a page that silently shows them a stranger.
func TestTheRefusalOfTheUseCaseReachesTheCaller(t *testing.T) {
	t.Parallel()

	refused := identitydomain.ErrSessionRevoked
	validate, err := sessionvalidator.New(&sessions{err: refused})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	identity, err := validate(context.Background(), "revoked")
	if !errors.Is(err, refused) {
		t.Errorf("validate() error = %v, want %v", err, refused)
	}
	if identity.AccountID != "" || identity.SessionID != "" {
		t.Errorf("validate() identity = %+v, want the zero value beside the error", identity)
	}
}

func TestNewRefusesAMissingUseCase(t *testing.T) {
	t.Parallel()

	if _, err := sessionvalidator.New(nil); err == nil || !strings.Contains(err.Error(), "session use case") {
		t.Errorf("New(nil) error = %v, want a refusal naming the use case", err)
	}
}

// TestAnAnswerWithoutAnAccountIsRefused covers the defensive branch: an empty
// identity that looked like a successful answer would authenticate nobody while
// telling the middleware somebody was authenticated.
func TestAnAnswerWithoutAnAccountIsRefused(t *testing.T) {
	t.Parallel()

	validate, err := sessionvalidator.New(&sessions{result: &application.AuthenticateSessionResult{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := validate(context.Background(), "token"); err == nil {
		t.Error("validate() accepted an answer carrying neither account nor session")
	}
}
