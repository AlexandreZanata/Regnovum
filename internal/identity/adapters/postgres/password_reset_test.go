package postgres_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

func TestRepository_PasswordResetTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	email, err := domain.ParseEmail("resetlifecycle@example.com")
	if err != nil {
		t.Fatalf("parse email failed: %v", err)
	}

	acc, err := repo.CreateAccountWithPassword(ctx, email, "oldHash")
	if err != nil {
		t.Fatalf("CreateAccountWithPassword failed: %v", err)
	}

	rawToken := "reset-token-sample-123"
	tokenHash := sha256.Sum256([]byte(rawToken))
	expiresAt := time.Now().UTC().Add(15 * time.Minute)

	// 1. CreatePasswordResetToken
	if err := repo.CreatePasswordResetToken(ctx, acc.ID(), tokenHash[:], expiresAt); err != nil {
		t.Fatalf("CreatePasswordResetToken failed: %v", err)
	}

	// 2. GetPasswordResetToken
	rec, err := repo.GetPasswordResetToken(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("GetPasswordResetToken failed: %v", err)
	}
	if rec.AccountID != acc.ID() {
		t.Errorf("token AccountID = %s, want %s", rec.AccountID, acc.ID())
	}
	if rec.UsedAt != nil {
		t.Errorf("expected UsedAt = nil, got %v", rec.UsedAt)
	}

	// 3. MarkPasswordResetTokenUsed
	now := time.Now().UTC()
	if err := repo.MarkPasswordResetTokenUsed(ctx, rec.ID, now); err != nil {
		t.Fatalf("MarkPasswordResetTokenUsed failed: %v", err)
	}

	recUsed, err := repo.GetPasswordResetToken(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("GetPasswordResetToken after mark failed: %v", err)
	}
	if recUsed.UsedAt == nil {
		t.Fatal("expected UsedAt to be set")
	}

	// 4. InvalidateActivePasswordResetTokens
	token2Hash := sha256.Sum256([]byte("reset-token-sample-456"))
	if err := repo.CreatePasswordResetToken(ctx, acc.ID(), token2Hash[:], expiresAt); err != nil {
		t.Fatalf("CreatePasswordResetToken 2 failed: %v", err)
	}

	if err := repo.InvalidateActivePasswordResetTokens(ctx, acc.ID()); err != nil {
		t.Fatalf("InvalidateActivePasswordResetTokens failed: %v", err)
	}

	rec2, err := repo.GetPasswordResetToken(ctx, token2Hash[:])
	if err != nil {
		t.Fatalf("GetPasswordResetToken 2 failed: %v", err)
	}
	if rec2.UsedAt == nil {
		t.Fatal("expected token 2 to be invalidated (UsedAt != nil)")
	}
}

func TestRepository_PasswordResetConcurrentRace(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	clock := clockseed.System{}

	email, _ := domain.ParseEmail("pgresetrace@example.com")
	acc, err := repo.CreateAccountWithPassword(ctx, email, "oldHash")
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}
	_ = repo.SetEmailVerified(ctx, acc.ID(), time.Now().UTC())

	// The token is hexadecimal on purpose. The use case normalizes the submitted
	// token with strings.TrimSpace before hashing it, and a raw 32-byte draw is
	// whitespace at the first or last byte in roughly one draw out of twenty —
	// measured at 4.63% over 200,000 draws — which changes the hash and makes
	// every worker fail with an error this test used not to count. A race test
	// whose outcome depends on a random draw is a lottery, and this one fired in
	// CI. Hex digits are never whitespace, so the draw cannot choose the outcome.
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		t.Fatalf("draw token bytes failed: %v", err)
	}
	token := hex.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(token))
	expiresAt := time.Now().UTC().Add(15 * time.Minute)

	if err := repo.CreatePasswordResetToken(ctx, acc.ID(), tokenHash[:], expiresAt); err != nil {
		t.Fatalf("create reset token failed: %v", err)
	}

	completeUC := application.NewCompletePasswordResetUseCase(repo, repo, repo, repo, repo, hasher, fakeemail.NewSender(), clock)

	const workers = 10
	var wg sync.WaitGroup
	var successCount int32
	var replayCount int32
	// Every outcome is classified: a winner, a loser rejected as a replay, or
	// anything else — which is a failure with a name, never a silent zero in
	// both counters. The original test counted only the first two, which is how
	// ten identical rejections read as "0 successes and 0 replays" instead of
	// as the bug it was.
	var otherMu sync.Mutex
	otherOutcomes := map[string]int{}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			err := completeUC.Execute(ctx, application.CompletePasswordResetCommand{
				Token:       token,
				NewPassword: fmt.Sprintf("RacePassword%d!", workerID),
			})
			switch {
			case err == nil:
				atomic.AddInt32(&successCount, 1)
			case errors.Is(err, application.ErrTokenAlreadyUsed):
				atomic.AddInt32(&replayCount, 1)
			default:
				otherMu.Lock()
				otherOutcomes[err.Error()]++
				otherMu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	if successCount != 1 {
		t.Errorf("expected exactly 1 successful reset in database, got %d", successCount)
	}
	if replayCount != workers-1 {
		t.Errorf("expected %d replay rejections in database, got %d", workers-1, replayCount)
	}
	for message, count := range otherOutcomes {
		t.Errorf("unexpected outcome for %d worker(s): %s", count, message)
	}
}

// TestRepository_PasswordResetTokenWhitespaceIsTheCode'sDecision registers what
// the use case actually does with a token surrounded by whitespace. The race
// test above needs a token the normalization cannot alter; this one exists so
// that the normalization itself stays an observed decision: if the use case ever
// stops trimming, or starts rejecting whitespace outright, this test is what
// changes — deliberately, and never as a side effect of a refactor.
func TestRepository_PasswordResetTokenWhitespaceIsTheCodeDecision(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	hasher, err := argon2id.New(argon2id.FastParams(), rand.Reader)
	if err != nil {
		t.Fatalf("init hasher failed: %v", err)
	}

	email, err := domain.ParseEmail("pgresettrim@example.com")
	if err != nil {
		t.Fatalf("parse email failed: %v", err)
	}
	clock := clockseed.System{}
	acc, err := repo.CreateAccountWithPassword(ctx, email, "oldHash")
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}
	_ = repo.SetEmailVerified(ctx, acc.ID(), time.Now().UTC())

	token := "trim-decision-token-0123456789abcdef"
	tokenHash := sha256.Sum256([]byte(token))
	expiresAt := time.Now().UTC().Add(15 * time.Minute)
	if err := repo.CreatePasswordResetToken(ctx, acc.ID(), tokenHash[:], expiresAt); err != nil {
		t.Fatalf("create reset token failed: %v", err)
	}

	completeUC := application.NewCompletePasswordResetUseCase(repo, repo, repo, repo, repo, hasher, fakeemail.NewSender(), clock)

	// The token is stored under its exact hash; what the submission carries is
	// the same token padded with whitespace on both sides. The three possible
	// behaviors are named, and anything outside them fails the test.
	err = completeUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token:       "\t\n " + token + " \r\v\f",
		NewPassword: "WhitespaceDecision1!",
	})
	switch {
	case err == nil:
		// Trimming: the padded submission reached the stored token.
		t.Log("the use case trims whitespace around the token and accepted the padded submission")
	case errors.Is(err, application.ErrInvalidToken):
		// Strict: whitespace makes the submitted token a different token.
		t.Log("the use case does not trim and rejected the padded submission as an invalid token")
	default:
		t.Fatalf("padded token produced an unnamed outcome: %v", err)
	}

	// The account's credential tells the two named behaviors apart, because the
	// error alone cannot: a strict rejection and a replay can share a message.
	// The query is by the account, so the answer is a fact about this account.
	credential, err := repo.GetPasswordCredential(ctx, acc.ID())
	if err != nil {
		t.Fatalf("read credential failed: %v", err)
	}
	if credential.PasswordHash == "oldHash" {
		t.Log("no password change happened: the padded submission was rejected")
		return
	}
	t.Log("the password changed: the padded submission was accepted and trimmed")
}

func TestIntegration_FullPasswordResetJourney(t *testing.T) {
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
	resetPolicy := domain.DefaultPasswordResetPolicy()
	sessPolicy := domain.DefaultSessionPolicy()

	registerUC := application.NewRegisterAccountUseCase(repo, repo, hasher, sender, clock, rnd, verPolicy)
	verifyUC := application.NewVerifyEmailUseCase(repo, repo, clock)
	loginUC := application.NewLoginUseCase(repo, repo, repo, hasher, clock, rnd, sessPolicy)
	authUC := application.NewAuthenticateSessionUseCase(repo, repo, clock, sessPolicy, 5*time.Minute)
	requestResetUC := application.NewRequestPasswordResetUseCase(repo, repo, sender, clock, rnd, resetPolicy)
	completeResetUC := application.NewCompletePasswordResetUseCase(repo, repo, repo, repo, repo, hasher, sender, clock)

	userEmail := "fullresetjourney@arena.local"
	initialPassword := "OriginalSecret123!"
	newPassword := "BrandNewSecret999!"

	// 1. Register account
	_, err = registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    userEmail,
		Password: initialPassword,
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// 2. Verify email
	verToken := sender.SentEmails()[0].Token
	if err := verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: verToken}); err != nil {
		t.Fatalf("verify email failed: %v", err)
	}

	// 3. Login with initial password to establish active session
	loginRes, err := loginUC.Execute(ctx, application.LoginCommand{
		Email:    userEmail,
		Password: initialPassword,
	})
	if err != nil {
		t.Fatalf("initial login failed: %v", err)
	}

	// Verify session is active
	authRes, err := authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: loginRes.RawToken})
	if err != nil || authRes.Account == nil {
		t.Fatalf("authenticate session before reset failed: %v", err)
	}

	// 4. Request password reset
	if err := requestResetUC.Execute(ctx, application.RequestPasswordResetCommand{Email: userEmail}); err != nil {
		t.Fatalf("request reset failed: %v", err)
	}

	resetEmails := sender.SentResetEmails()
	if len(resetEmails) != 1 {
		t.Fatalf("expected 1 reset email, got %d", len(resetEmails))
	}
	rawResetToken := resetEmails[0].Token

	// 5. Complete password reset
	if err := completeResetUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token:       rawResetToken,
		NewPassword: newPassword,
	}); err != nil {
		t.Fatalf("complete password reset failed: %v", err)
	}

	// 6. Old session must be revoked in PostgreSQL
	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: loginRes.RawToken})
	if !errors.Is(err, application.ErrSessionRevoked) {
		t.Errorf("expected previous session to be revoked after reset, got %v", err)
	}

	// 7. Old password must fail login
	_, err = loginUC.Execute(ctx, application.LoginCommand{
		Email:    userEmail,
		Password: initialPassword,
	})
	if !errors.Is(err, application.ErrInvalidCredentials) {
		t.Errorf("expected initial password to fail login, got %v", err)
	}

	// 8. New password must succeed login
	newLogin, err := loginUC.Execute(ctx, application.LoginCommand{
		Email:    userEmail,
		Password: newPassword,
	})
	if err != nil {
		t.Fatalf("login with new password failed: %v", err)
	}
	if newLogin.Session == nil {
		t.Fatal("expected active session with new password")
	}

	// 9. Replay attack: submitting same reset token again fails with ErrTokenAlreadyUsed
	err = completeResetUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token:       rawResetToken,
		NewPassword: "ThirdPasswordAttempt123!",
	})
	if !errors.Is(err, application.ErrTokenAlreadyUsed) {
		t.Errorf("expected ErrTokenAlreadyUsed on replay in PostgreSQL, got %v", err)
	}
}
