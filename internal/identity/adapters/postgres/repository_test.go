package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
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

func TestRepository_CreateAccountAndVerifyFlow(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	email, err := domain.ParseEmail("pgtest@example.com")
	if err != nil {
		t.Fatalf("parse email failed: %v", err)
	}

	// 1. Create account with password
	acc, err := repo.CreateAccountWithPassword(ctx, email, "dummy_argon2id_hash")
	if err != nil {
		t.Fatalf("CreateAccountWithPassword failed: %v", err)
	}

	if acc.ID().IsZero() {
		t.Fatal("expected non-empty account ID")
	}
	if acc.Status() != domain.AccountStatusPending {
		t.Errorf("status = %s, want pending", acc.Status())
	}
	if acc.IsVerified() {
		t.Error("new account should not be verified")
	}

	// 2. Lookup by email (case-insensitive)
	lookupEmail, _ := domain.ParseEmail("PGTEST@EXAMPLE.COM")
	accByEmail, err := repo.GetAccountByEmail(ctx, lookupEmail)
	if err != nil {
		t.Fatalf("GetAccountByEmail failed: %v", err)
	}
	if accByEmail.ID() != acc.ID() {
		t.Errorf("got account ID %s, want %s", accByEmail.ID(), acc.ID())
	}

	// 3. Lookup by ID
	accByID, err := repo.GetAccountByID(ctx, acc.ID())
	if err != nil {
		t.Fatalf("GetAccountByID failed: %v", err)
	}
	if !accByID.Email().Equals(email) {
		t.Errorf("email = %v, want %v", accByID.Email(), email)
	}

	// 4. Create verification token
	rawToken := "sample-opaque-token-123"
	tokenHash := sha256.Sum256([]byte(rawToken))
	expiresAt := time.Now().UTC().Add(24 * time.Hour)

	if err := repo.CreateVerificationToken(ctx, acc.ID(), tokenHash[:], expiresAt); err != nil {
		t.Fatalf("CreateVerificationToken failed: %v", err)
	}

	// 5. Fetch token record
	rec, err := repo.GetVerificationToken(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("GetVerificationToken failed: %v", err)
	}
	if rec.AccountID != acc.ID() {
		t.Errorf("token AccountID = %s, want %s", rec.AccountID, acc.ID())
	}
	if rec.UsedAt != nil {
		t.Errorf("expected UsedAt = nil, got %v", rec.UsedAt)
	}

	// 6. Mark token used
	now := time.Now().UTC()
	if err := repo.MarkTokenUsed(ctx, rec.ID, now); err != nil {
		t.Fatalf("MarkTokenUsed failed: %v", err)
	}

	recUsed, err := repo.GetVerificationToken(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("GetVerificationToken after mark failed: %v", err)
	}
	if recUsed.UsedAt == nil {
		t.Fatal("expected UsedAt to be populated")
	}

	// 7. Set account verified
	if err := repo.SetEmailVerified(ctx, acc.ID(), now); err != nil {
		t.Fatalf("SetEmailVerified failed: %v", err)
	}

	accActive, err := repo.GetAccountByID(ctx, acc.ID())
	if err != nil {
		t.Fatalf("GetAccountByID after verification failed: %v", err)
	}
	if accActive.Status() != domain.AccountStatusActive {
		t.Errorf("status = %s, want active", accActive.Status())
	}
	if !accActive.IsVerified() {
		t.Error("expected account to be verified")
	}
}

func TestRepository_ConcurrentDuplicateAccount(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	email, err := domain.ParseEmail("concurrent@example.com")
	if err != nil {
		t.Fatalf("parse email failed: %v", err)
	}

	concurrency := 10
	var successCount atomic.Int32
	var duplicateCount atomic.Int32
	var otherErrors atomic.Int32

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // synchronize all goroutines to hit PostgreSQL simultaneously
			_, err := repo.CreateAccountWithPassword(ctx, email, "hash")
			if err == nil {
				successCount.Add(1)
			} else if errors.Is(err, application.ErrDuplicateEmail) {
				duplicateCount.Add(1)
			} else {
				otherErrors.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	if successCount.Load() != 1 {
		t.Errorf("expected exactly 1 successful account creation, got %d", successCount.Load())
	}
	if duplicateCount.Load() != int32(concurrency-1) {
		t.Errorf("expected %d duplicate errors, got %d", concurrency-1, duplicateCount.Load())
	}
	if otherErrors.Load() != 0 {
		t.Errorf("expected 0 other errors, got %d", otherErrors.Load())
	}
}

func TestRepository_ReissuanceInvalidatesPreviousTokens(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	email, _ := domain.ParseEmail("reissue-pg@example.com")
	acc, err := repo.CreateAccountWithPassword(ctx, email, "hash")
	if err != nil {
		t.Fatalf("CreateAccountWithPassword failed: %v", err)
	}

	// 1. Issue Token 1
	token1Hash := sha256.Sum256([]byte("token-1"))
	if err := repo.CreateVerificationToken(ctx, acc.ID(), token1Hash[:], time.Now().UTC().Add(24*time.Hour)); err != nil {
		t.Fatalf("create token 1 failed: %v", err)
	}

	// 2. Invalidate active tokens upon reissuance
	if err := repo.InvalidateActiveTokens(ctx, acc.ID()); err != nil {
		t.Fatalf("InvalidateActiveTokens failed: %v", err)
	}

	// 3. Issue Token 2
	token2Hash := sha256.Sum256([]byte("token-2"))
	if err := repo.CreateVerificationToken(ctx, acc.ID(), token2Hash[:], time.Now().UTC().Add(24*time.Hour)); err != nil {
		t.Fatalf("create token 2 failed: %v", err)
	}

	// 4. Token 1 must be marked as used (invalidated)
	rec1, err := repo.GetVerificationToken(ctx, token1Hash[:])
	if err != nil {
		t.Fatalf("get token 1 failed: %v", err)
	}
	if rec1.UsedAt == nil {
		t.Error("token 1 must have UsedAt populated after invalidation")
	}

	// 5. Token 2 must remain active
	rec2, err := repo.GetVerificationToken(ctx, token2Hash[:])
	if err != nil {
		t.Fatalf("get token 2 failed: %v", err)
	}
	if rec2.UsedAt != nil {
		t.Error("token 2 must be active with UsedAt == nil")
	}
}

func TestRepository_FullApplicationIntegrationFlow(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	hasher, err := argon2id.New(argon2id.FastParams(), clockseed.NewRandom())
	if err != nil {
		t.Fatalf("init hasher failed: %v", err)
	}

	emailSender := fakeemail.NewSender()
	clock := clockseed.NewClock()
	policy := domain.DefaultVerificationPolicy()

	registerUC := application.NewRegisterAccountUseCase(
		repo, repo, hasher, emailSender, clock, clockseed.NewRandom(), policy,
	)
	verifyUC := application.NewVerifyEmailUseCase(repo, repo, clock)
	issueUC := application.NewIssueEmailVerificationUseCase(
		repo, repo, emailSender, clock, clockseed.NewRandom(), policy,
	)

	// A. User registration
	registerEmail := "integration-flow@example.com"
	res, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    registerEmail,
		Password: "StrongPassword2026!",
	})
	if err != nil {
		t.Fatalf("RegisterAccount failed: %v", err)
	}
	if res.Email != registerEmail {
		t.Errorf("res.Email = %q, want %q", res.Email, registerEmail)
	}

	emailObj, _ := domain.ParseEmail(registerEmail)
	token1, ok := emailSender.LastTokenForEmail(emailObj)
	if !ok || token1 == "" {
		t.Fatal("expected verification token in fake email sender")
	}

	// B. Re-issue verification email
	err = issueUC.Execute(ctx, application.IssueEmailVerificationCommand{Email: registerEmail})
	if err != nil {
		t.Fatalf("IssueEmailVerification failed: %v", err)
	}
	token2, ok := emailSender.LastTokenForEmail(emailObj)
	if !ok || token2 == "" {
		t.Fatal("expected second verification token")
	}
	if token1 == token2 {
		t.Fatal("re-issued token must be distinct from token 1")
	}

	// C. Verification with Token 1 (must fail due to invalidation / replay)
	err = verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: token1})
	if !errors.Is(err, application.ErrTokenAlreadyUsed) {
		t.Errorf("expected ErrTokenAlreadyUsed for invalidated token1, got: %v", err)
	}

	// D. Verification with Token 2 (must succeed)
	if err := verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: token2}); err != nil {
		t.Fatalf("verification with token2 failed: %v", err)
	}

	// E. Replay with Token 2 (must fail)
	err = verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: token2})
	if !errors.Is(err, application.ErrTokenAlreadyUsed) {
		t.Errorf("expected ErrTokenAlreadyUsed on token2 replay, got: %v", err)
	}

	// F. Confirm account is Active and verified in PostgreSQL
	acc, err := repo.GetAccountByEmail(ctx, emailObj)
	if err != nil {
		t.Fatalf("GetAccountByEmail failed: %v", err)
	}
	if acc.Status() != domain.AccountStatusActive {
		t.Errorf("status = %s, want active", acc.Status())
	}
	if !acc.IsVerified() {
		t.Error("account must be verified")
	}
}
