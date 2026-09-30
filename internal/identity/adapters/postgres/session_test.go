package postgres_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/fakeemail"
	identitypg "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func TestRepository_SessionLifecycle(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	email, err := domain.ParseEmail("sessionuser@example.com")
	if err != nil {
		t.Fatalf("parse email failed: %v", err)
	}

	// 1. Create account with password
	initialHash := "$argon2id$v=19$m=8192,t=1,p=1$fakeSalt$fakeHash"
	acc, err := repo.CreateAccountWithPassword(ctx, email, initialHash)
	if err != nil {
		t.Fatalf("CreateAccountWithPassword failed: %v", err)
	}

	// 2. Test GetPasswordCredential
	cred, err := repo.GetPasswordCredential(ctx, acc.ID())
	if err != nil {
		t.Fatalf("GetPasswordCredential failed: %v", err)
	}
	if cred.AccountID != acc.ID() {
		t.Errorf("cred AccountID = %s, want %s", cred.AccountID, acc.ID())
	}
	if cred.PasswordHash != initialHash {
		t.Errorf("cred PasswordHash = %s, want %s", cred.PasswordHash, initialHash)
	}

	// 3. Test UpdatePasswordCredential
	updatedHash := "$argon2id$v=19$m=8192,t=2,p=1$fakeSalt$updatedHash"
	if err := repo.UpdatePasswordCredential(ctx, acc.ID(), updatedHash, "argon2id", 2); err != nil {
		t.Fatalf("UpdatePasswordCredential failed: %v", err)
	}
	credUpdated, err := repo.GetPasswordCredential(ctx, acc.ID())
	if err != nil {
		t.Fatalf("GetPasswordCredential after update failed: %v", err)
	}
	if credUpdated.PasswordHash != updatedHash {
		t.Errorf("cred PasswordHash = %s, want %s", credUpdated.PasswordHash, updatedHash)
	}

	// 4. Test CreateSession
	rawToken := "session-token-bytes-for-postgres-test"
	tokenHash := sha256.Sum256([]byte(rawToken))
	now := time.Now().UTC()
	expiresAt := now.Add(24 * time.Hour)

	sess, err := repo.CreateSession(ctx, acc.ID(), tokenHash[:], expiresAt, "198.51.100.1", "ArenaClient/1.0")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if sess.ID().IsZero() {
		t.Fatal("expected non-empty session ID")
	}
	if sess.AccountID() != acc.ID() {
		t.Errorf("session AccountID = %s, want %s", sess.AccountID(), acc.ID())
	}
	if !sess.HasTokenHash(tokenHash[:]) {
		t.Error("session token hash mismatch")
	}
	if sess.IPAddress() != "198.51.100.1" {
		t.Errorf("IP = %s, want 198.51.100.1", sess.IPAddress())
	}
	if sess.UserAgent() != "ArenaClient/1.0" {
		t.Errorf("UserAgent = %s, want ArenaClient/1.0", sess.UserAgent())
	}
	if sess.IsRevoked() {
		t.Error("new session should not be revoked")
	}

	// 5. Test GetSessionByTokenHash
	fetched, err := repo.GetSessionByTokenHash(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("GetSessionByTokenHash failed: %v", err)
	}
	if fetched.ID() != sess.ID() {
		t.Errorf("fetched ID = %s, want %s", fetched.ID(), sess.ID())
	}

	// 6. Test TouchSession
	newLastSeen := now.Add(1 * time.Hour)
	newExpiresAt := now.Add(25 * time.Hour)
	if err := repo.TouchSession(ctx, sess.ID(), newLastSeen, newExpiresAt); err != nil {
		t.Fatalf("TouchSession failed: %v", err)
	}
	touched, err := repo.GetSessionByTokenHash(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("GetSessionByTokenHash after touch failed: %v", err)
	}
	if touched.LastSeenAt().Before(newLastSeen.Add(-time.Second)) {
		t.Errorf("LastSeenAt = %v, expected at least %v", touched.LastSeenAt(), newLastSeen)
	}

	// 7. Test RevokeSession
	if err := repo.RevokeSession(ctx, tokenHash[:]); err != nil {
		t.Fatalf("RevokeSession failed: %v", err)
	}
	revoked, err := repo.GetSessionByTokenHash(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("GetSessionByTokenHash after revoke failed: %v", err)
	}
	if !revoked.IsRevoked() {
		t.Error("session should be revoked")
	}
	if revoked.RevokedAt() == nil {
		t.Error("expected non-nil RevokedAt")
	}

	// 8. Test RevokeAllAccountSessions
	sess2Hash := sha256.Sum256([]byte("token-sess2"))
	sess3Hash := sha256.Sum256([]byte("token-sess3"))
	s2, err := repo.CreateSession(ctx, acc.ID(), sess2Hash[:], expiresAt, "10.0.0.1", "Client2")
	if err != nil {
		t.Fatalf("CreateSession 2 failed: %v", err)
	}
	s3, err := repo.CreateSession(ctx, acc.ID(), sess3Hash[:], expiresAt, "10.0.0.2", "Client3")
	if err != nil {
		t.Fatalf("CreateSession 3 failed: %v", err)
	}

	if err := repo.RevokeAllAccountSessions(ctx, acc.ID()); err != nil {
		t.Fatalf("RevokeAllAccountSessions failed: %v", err)
	}

	s2Rev, err := repo.GetSessionByTokenHash(ctx, sess2Hash[:])
	if err != nil || !s2Rev.IsRevoked() {
		t.Errorf("expected session 2 to be revoked, got err=%v, s2=%v", err, s2)
	}
	s3Rev, err := repo.GetSessionByTokenHash(ctx, sess3Hash[:])
	if err != nil || !s3Rev.IsRevoked() {
		t.Errorf("expected session 3 to be revoked, got err=%v, s3=%v", err, s3)
	}
}

func TestRepository_SessionConcurrentRace(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	email, _ := domain.ParseEmail("racetest@example.com")
	acc, err := repo.CreateAccountWithPassword(ctx, email, "hash")
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	const workers = 10
	var wg sync.WaitGroup
	errCh := make(chan error, workers*3)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			tokenBytes := make([]byte, 32)
			if _, err := rand.Read(tokenBytes); err != nil {
				errCh <- fmt.Errorf("worker %d rand read: %w", workerID, err)
				return
			}
			tokenHash := sha256.Sum256(tokenBytes)
			expiresAt := time.Now().UTC().Add(24 * time.Hour)

			// Concurrent CreateSession
			sess, err := repo.CreateSession(ctx, acc.ID(), tokenHash[:], expiresAt, fmt.Sprintf("192.0.2.%d", workerID), "RaceWorker")
			if err != nil {
				errCh <- fmt.Errorf("worker %d create session: %w", workerID, err)
				return
			}

			// Concurrent TouchSession
			now := time.Now().UTC()
			if err := repo.TouchSession(ctx, sess.ID(), now, expiresAt.Add(time.Hour)); err != nil {
				errCh <- fmt.Errorf("worker %d touch session: %w", workerID, err)
				return
			}

			// Concurrent Lookup
			fetched, err := repo.GetSessionByTokenHash(ctx, tokenHash[:])
			if err != nil {
				errCh <- fmt.Errorf("worker %d lookup session: %w", workerID, err)
				return
			}
			if fetched.ID() != sess.ID() {
				errCh <- fmt.Errorf("worker %d ID mismatch: %v vs %v", workerID, fetched.ID(), sess.ID())
				return
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrency error: %v", err)
	}
}

func TestIntegration_FullSessionJourney(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	hasher, err := argon2id.New(argon2id.FastParams(), rand.Reader)
	if err != nil {
		t.Fatalf("init hasher failed: %v", err)
	}

	sender := fakeemail.NewSender()
	clock := clockseed.System{}
	rnd := clockseed.CryptoRandom{}
	verPolicy := domain.DefaultVerificationPolicy()
	sessPolicy := domain.DefaultSessionPolicy()

	// Initialize all use cases
	registerUC := application.NewRegisterAccountUseCase(repo, repo, hasher, sender, clock, rnd, verPolicy)
	verifyUC := application.NewVerifyEmailUseCase(repo, repo, clock)
	loginUC := application.NewLoginUseCase(repo, repo, repo, hasher, clock, rnd, sessPolicy)
	authUC := application.NewAuthenticateSessionUseCase(repo, repo, clock, sessPolicy, 5*time.Minute)
	rotateUC := application.NewRotateSessionUseCase(repo, repo, clock, rnd, sessPolicy)
	logoutUC := application.NewLogoutUseCase(repo)
	logoutAllUC := application.NewLogoutAllUseCase(repo)

	userEmail := "journeyuser@arena.local"
	userPassword := "CorrectPassword123!"

	// Step 1: Register account
	regRes, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    userEmail,
		Password: userPassword,
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if regRes.AccountID == "" {
		t.Fatal("expected non-empty account ID")
	}

	// Step 2: Extract verification token and verify email
	sentEmails := sender.SentEmails()
	if len(sentEmails) != 1 {
		t.Fatalf("expected 1 sent email, got %d", len(sentEmails))
	}
	rawVerToken := sentEmails[0].Token

	if err := verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: rawVerToken}); err != nil {
		t.Fatalf("verify email failed: %v", err)
	}

	// Step 3: Login with wrong password (must return ErrInvalidCredentials)
	_, err = loginUC.Execute(ctx, application.LoginCommand{
		Email:    userEmail,
		Password: "WrongPassword999!",
	})
	if !errors.Is(err, application.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for wrong password, got %v", err)
	}

	// Step 4: Login with correct password
	loginRes, err := loginUC.Execute(ctx, application.LoginCommand{
		Email:     userEmail,
		Password:  userPassword,
		IPAddress: "203.0.113.42",
		UserAgent: "Browser/1.0",
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if loginRes.RawToken == "" {
		t.Fatal("expected non-empty raw token")
	}

	// Step 5: Authenticate with session token
	authRes, err := authUC.Execute(ctx, application.AuthenticateSessionCommand{
		RawToken: loginRes.RawToken,
	})
	if err != nil {
		t.Fatalf("authenticate session failed: %v", err)
	}
	if authRes.Account.Email().String() != userEmail {
		t.Errorf("auth email = %s, want %s", authRes.Account.Email(), userEmail)
	}

	// Step 6: Rotate session
	rotateRes, err := rotateUC.Execute(ctx, application.RotateSessionCommand{
		CurrentRawToken: loginRes.RawToken,
		IPAddress:       "203.0.113.99",
		UserAgent:       "Browser/2.0",
	})
	if err != nil {
		t.Fatalf("rotate session failed: %v", err)
	}

	// Old session is revoked
	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{
		RawToken: loginRes.RawToken,
	})
	if !errors.Is(err, application.ErrSessionRevoked) {
		t.Errorf("expected old session to be revoked, got %v", err)
	}

	// New rotated session works
	newAuth, err := authUC.Execute(ctx, application.AuthenticateSessionCommand{
		RawToken: rotateRes.RawToken,
	})
	if err != nil {
		t.Fatalf("new session authenticate failed: %v", err)
	}
	if newAuth.Session.IPAddress() != "203.0.113.99" {
		t.Errorf("IP = %s, want 203.0.113.99", newAuth.Session.IPAddress())
	}

	// Step 7: Issue second session and Logout all
	login2Res, err := loginUC.Execute(ctx, application.LoginCommand{
		Email:    userEmail,
		Password: userPassword,
	})
	if err != nil {
		t.Fatalf("login 2 failed: %v", err)
	}

	if err := logoutAllUC.Execute(ctx, application.LogoutAllCommand{AccountID: authRes.Account.ID()}); err != nil {
		t.Fatalf("logout all failed: %v", err)
	}

	// Both rotated session and login2 session must now be revoked
	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: rotateRes.RawToken})
	if !errors.Is(err, application.ErrSessionRevoked) {
		t.Errorf("expected rotated session to be revoked after logout all, got %v", err)
	}

	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: login2Res.RawToken})
	if !errors.Is(err, application.ErrSessionRevoked) {
		t.Errorf("expected login2 session to be revoked after logout all, got %v", err)
	}

	// Logout single session with already revoked token succeeds idempotently
	rawToken := base64.RawURLEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	if err := logoutUC.Execute(ctx, application.LogoutCommand{RawToken: rawToken}); err != nil {
		t.Errorf("idempotent logout failed: %v", err)
	}
}
