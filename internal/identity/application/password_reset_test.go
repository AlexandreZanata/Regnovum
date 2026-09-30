package application_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/fakeemail"
	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

type inMemoryResetTokenRepo struct {
	mu     sync.Mutex
	tokens map[string]*application.PasswordResetTokenRecord
	seq    int
}

func newInMemoryResetTokenRepo() *inMemoryResetTokenRepo {
	return &inMemoryResetTokenRepo{
		tokens: make(map[string]*application.PasswordResetTokenRecord),
	}
}

func (r *inMemoryResetTokenRepo) CreatePasswordResetToken(ctx context.Context, accountID domain.AccountID, tokenHash []byte, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	id := fmt.Sprintf("reset_%d", r.seq)
	r.tokens[string(tokenHash)] = &application.PasswordResetTokenRecord{
		ID:        id,
		AccountID: accountID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
		UsedAt:    nil,
		CreatedAt: time.Now().UTC(),
	}
	return nil
}

func (r *inMemoryResetTokenRepo) GetPasswordResetToken(ctx context.Context, tokenHash []byte) (*application.PasswordResetTokenRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.tokens[string(tokenHash)]
	if !ok {
		return nil, application.ErrInvalidToken
	}
	copyRec := *rec
	return &copyRec, nil
}

func (r *inMemoryResetTokenRepo) MarkPasswordResetTokenUsed(ctx context.Context, tokenID string, usedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, rec := range r.tokens {
		if rec.ID == tokenID {
			if rec.UsedAt != nil {
				return application.ErrTokenAlreadyUsed
			}
			t := usedAt
			rec.UsedAt = &t
			return nil
		}
	}
	return application.ErrInvalidToken
}

func (r *inMemoryResetTokenRepo) InvalidateActivePasswordResetTokens(ctx context.Context, accountID domain.AccountID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	for _, rec := range r.tokens {
		if rec.AccountID == accountID && rec.UsedAt == nil {
			rec.UsedAt = &now
		}
	}
	return nil
}

func TestRequestPasswordReset_AntiEnumeration(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	resetRepo := newInMemoryResetTokenRepo()
	sender := fakeemail.NewSender()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	policy := domain.DefaultPasswordResetPolicy()
	ctx := context.Background()

	// Seed accounts in various states
	seedAccount := func(emailStr string, status domain.AccountStatus) *domain.Account {
		email, _ := domain.ParseEmail(emailStr)
		h, _ := hasher.HashPassword("InitialPass123!")
		acc, _ := accRepo.CreateAccountWithPassword(ctx, email, h)
		credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
			AccountID:    acc.ID(),
			PasswordHash: h,
			Algorithm:    "argon2id",
			Version:      1,
		}
		switch status {
		case domain.AccountStatusActive:
			_ = acc.VerifyEmail(clock.Now())
		case domain.AccountStatusSuspended:
			_ = acc.VerifyEmail(clock.Now())
			_ = acc.Suspend(clock.Now())
		case domain.AccountStatusDeleted:
			_ = acc.VerifyEmail(clock.Now())
			_ = acc.MarkDeleted(clock.Now())
		}
		return acc
	}

	seedAccount("active@arena.local", domain.AccountStatusActive)
	seedAccount("suspended@arena.local", domain.AccountStatusSuspended)
	seedAccount("deleted@arena.local", domain.AccountStatusDeleted)

	requestUC := application.NewRequestPasswordResetUseCase(accRepo, resetRepo, sender, clock, rnd, policy)

	tests := []struct {
		name        string
		email       string
		shouldEmail bool
	}{
		{name: "non-existent email", email: "nonexistent@arena.local", shouldEmail: false},
		{name: "invalid email format", email: "not-an-email", shouldEmail: false},
		{name: "suspended account", email: "suspended@arena.local", shouldEmail: false},
		{name: "deleted account", email: "deleted@arena.local", shouldEmail: false},
		{name: "active account", email: "active@arena.local", shouldEmail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender.Reset()
			err := requestUC.Execute(ctx, application.RequestPasswordResetCommand{Email: tt.email})
			if err != nil {
				t.Fatalf("expected uniform nil error, got %v", err)
			}

			sent := sender.SentResetEmails()
			if tt.shouldEmail && len(sent) != 1 {
				t.Fatalf("expected 1 reset email sent, got %d", len(sent))
			}
			if !tt.shouldEmail && len(sent) != 0 {
				t.Fatalf("expected 0 reset emails sent, got %d", len(sent))
			}
		})
	}
}

func TestPasswordReset_SuccessAndReplayRejection(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	resetRepo := newInMemoryResetTokenRepo()
	sessRepo := newInMemorySessionRepo()
	sender := fakeemail.NewSender()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	resetPolicy := domain.DefaultPasswordResetPolicy()
	sessPolicy := domain.DefaultSessionPolicy()
	ctx := context.Background()

	email, _ := domain.ParseEmail("resetuser@arena.local")
	oldPass := "OriginalPassword123!"
	h, _ := hasher.HashPassword(oldPass)
	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, h)
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: h,
		Algorithm:    "argon2id",
		Version:      1,
	}

	requestUC := application.NewRequestPasswordResetUseCase(accRepo, resetRepo, sender, clock, rnd, resetPolicy)
	completeUC := application.NewCompletePasswordResetUseCase(accRepo, credRepo, resetRepo, nil, sessRepo, hasher, sender, clock)
	loginUC := application.NewLoginUseCase(accRepo, credRepo, sessRepo, hasher, clock, rnd, sessPolicy)

	// Step 1: Request reset
	if err := requestUC.Execute(ctx, application.RequestPasswordResetCommand{Email: email.String()}); err != nil {
		t.Fatalf("request reset failed: %v", err)
	}

	resetToken, ok := sender.LastResetTokenForEmail(email)
	if !ok || resetToken == "" {
		t.Fatalf("expected reset token to be sent")
	}

	// Step 2: Complete reset with new password
	newPass := "NewFreshPassword999!"
	if err := completeUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token:       resetToken,
		NewPassword: newPass,
	}); err != nil {
		t.Fatalf("complete reset failed: %v", err)
	}

	// Step 3: Old password must fail login
	_, err := loginUC.Execute(ctx, application.LoginCommand{Email: email.String(), Password: oldPass})
	if !errors.Is(err, application.ErrInvalidCredentials) {
		t.Errorf("expected old password to fail login, got %v", err)
	}

	// Step 4: New password must succeed login
	loginRes, err := loginUC.Execute(ctx, application.LoginCommand{Email: email.String(), Password: newPass})
	if err != nil {
		t.Fatalf("login with new password failed: %v", err)
	}
	if loginRes.Session == nil {
		t.Fatalf("expected session after login")
	}

	// Step 5: Replay rejection - attempting to use the token a second time must fail
	err = completeUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token:       resetToken,
		NewPassword: "AnotherPassword123!",
	})
	if !errors.Is(err, application.ErrTokenAlreadyUsed) {
		t.Errorf("expected ErrTokenAlreadyUsed on replay, got %v", err)
	}
}

func TestPasswordReset_ExpiredTokenRejection(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	resetRepo := newInMemoryResetTokenRepo()
	sessRepo := newInMemorySessionRepo()
	sender := fakeemail.NewSender()
	startTime := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: startTime}
	rnd := &seqRandom{}
	resetPolicy := domain.DefaultPasswordResetPolicy() // 15 minutes
	ctx := context.Background()

	email, _ := domain.ParseEmail("expiredreset@arena.local")
	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, "oldHash")
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: "oldHash",
		Algorithm:    "argon2id",
		Version:      1,
	}

	requestUC := application.NewRequestPasswordResetUseCase(accRepo, resetRepo, sender, clock, rnd, resetPolicy)
	completeUC := application.NewCompletePasswordResetUseCase(accRepo, credRepo, resetRepo, nil, sessRepo, hasher, sender, clock)

	_ = requestUC.Execute(ctx, application.RequestPasswordResetCommand{Email: email.String()})
	token, _ := sender.LastResetTokenForEmail(email)

	// Advance time by 16 minutes (> 15-minute expiration window)
	clock.Advance(16 * time.Minute)

	err := completeUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token:       token,
		NewPassword: "NewValidPassword123!",
	})
	if !errors.Is(err, application.ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestPasswordReset_ReissuanceInvalidatesPreviousToken(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	resetRepo := newInMemoryResetTokenRepo()
	sessRepo := newInMemorySessionRepo()
	sender := fakeemail.NewSender()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	resetPolicy := domain.DefaultPasswordResetPolicy()
	ctx := context.Background()

	email, _ := domain.ParseEmail("reissuance@arena.local")
	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, "oldHash")
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: "oldHash",
		Algorithm:    "argon2id",
		Version:      1,
	}

	requestUC := application.NewRequestPasswordResetUseCase(accRepo, resetRepo, sender, clock, rnd, resetPolicy)
	completeUC := application.NewCompletePasswordResetUseCase(accRepo, credRepo, resetRepo, nil, sessRepo, hasher, sender, clock)

	// Request #1
	_ = requestUC.Execute(ctx, application.RequestPasswordResetCommand{Email: email.String()})
	token1, _ := sender.LastResetTokenForEmail(email)

	// Advance clock slightly
	clock.Advance(2 * time.Minute)

	// Request #2 (should invalidate token1 per THR-AUTH-04)
	_ = requestUC.Execute(ctx, application.RequestPasswordResetCommand{Email: email.String()})
	token2, _ := sender.LastResetTokenForEmail(email)

	if token1 == token2 {
		t.Fatalf("token1 and token2 should be different")
	}

	// Attempting to complete with token1 must fail as already invalidated/used
	err := completeUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token:       token1,
		NewPassword: "NewValidPassword123!",
	})
	if !errors.Is(err, application.ErrTokenAlreadyUsed) {
		t.Errorf("expected ErrTokenAlreadyUsed for token1, got %v", err)
	}

	// Attempting to complete with token2 must succeed
	err = completeUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token:       token2,
		NewPassword: "NewValidPassword123!",
	})
	if err != nil {
		t.Fatalf("completing with token2 failed: %v", err)
	}
}

func TestPasswordReset_SuspendedAccountRejection(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	resetRepo := newInMemoryResetTokenRepo()
	sessRepo := newInMemorySessionRepo()
	sender := fakeemail.NewSender()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	resetPolicy := domain.DefaultPasswordResetPolicy()
	ctx := context.Background()

	email, _ := domain.ParseEmail("suspendduringreset@arena.local")
	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, "oldHash")
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: "oldHash",
		Algorithm:    "argon2id",
		Version:      1,
	}

	requestUC := application.NewRequestPasswordResetUseCase(accRepo, resetRepo, sender, clock, rnd, resetPolicy)
	completeUC := application.NewCompletePasswordResetUseCase(accRepo, credRepo, resetRepo, nil, sessRepo, hasher, sender, clock)

	// Issue token while active
	_ = requestUC.Execute(ctx, application.RequestPasswordResetCommand{Email: email.String()})
	token, _ := sender.LastResetTokenForEmail(email)

	// Account gets suspended before token is consumed
	_ = acc.Suspend(clock.Now())

	err := completeUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token:       token,
		NewPassword: "NewPassword123!",
	})
	if !errors.Is(err, domain.ErrAccountSuspended) {
		t.Errorf("expected ErrAccountSuspended, got %v", err)
	}
}

func TestPasswordReset_RevokesExistingSessions(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	resetRepo := newInMemoryResetTokenRepo()
	sessRepo := newInMemorySessionRepo()
	sender := fakeemail.NewSender()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	resetPolicy := domain.DefaultPasswordResetPolicy()
	sessPolicy := domain.DefaultSessionPolicy()
	ctx := context.Background()

	email, _ := domain.ParseEmail("sessionrevoketest@arena.local")
	pass := "OriginalPassword123!"
	h, _ := hasher.HashPassword(pass)
	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, h)
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: h,
		Algorithm:    "argon2id",
		Version:      1,
	}

	loginUC := application.NewLoginUseCase(accRepo, credRepo, sessRepo, hasher, clock, rnd, sessPolicy)
	authUC := application.NewAuthenticateSessionUseCase(accRepo, sessRepo, clock, sessPolicy, 5*time.Minute)
	requestUC := application.NewRequestPasswordResetUseCase(accRepo, resetRepo, sender, clock, rnd, resetPolicy)
	completeUC := application.NewCompletePasswordResetUseCase(accRepo, credRepo, resetRepo, nil, sessRepo, hasher, sender, clock)

	// Establish session
	loginRes, _ := loginUC.Execute(ctx, application.LoginCommand{Email: email.String(), Password: pass})

	// Authenticate works
	authRes, err := authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: loginRes.RawToken})
	if err != nil || authRes.Account == nil {
		t.Fatalf("initial session authentication failed: %v", err)
	}

	// Request and complete password reset
	_ = requestUC.Execute(ctx, application.RequestPasswordResetCommand{Email: email.String()})
	token, _ := sender.LastResetTokenForEmail(email)

	if err := completeUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token:       token,
		NewPassword: "BrandNewPassword123!",
	}); err != nil {
		t.Fatalf("complete reset failed: %v", err)
	}

	// Session must now be revoked!
	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: loginRes.RawToken})
	if !errors.Is(err, application.ErrSessionRevoked) {
		t.Errorf("expected ErrSessionRevoked for existing session after password reset, got %v", err)
	}
}

func TestPasswordReset_ConcurrentRaceOnSameToken(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	resetRepo := newInMemoryResetTokenRepo()
	sessRepo := newInMemorySessionRepo()
	sender := fakeemail.NewSender()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	resetPolicy := domain.DefaultPasswordResetPolicy()
	ctx := context.Background()

	email, _ := domain.ParseEmail("racetoken@arena.local")
	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, "oldHash")
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: "oldHash",
		Algorithm:    "argon2id",
		Version:      1,
	}

	requestUC := application.NewRequestPasswordResetUseCase(accRepo, resetRepo, sender, clock, rnd, resetPolicy)
	completeUC := application.NewCompletePasswordResetUseCase(accRepo, credRepo, resetRepo, nil, sessRepo, hasher, sender, clock)

	_ = requestUC.Execute(ctx, application.RequestPasswordResetCommand{Email: email.String()})
	token, _ := sender.LastResetTokenForEmail(email)

	// Race: 10 goroutines attempting to complete reset using the same token
	const workers = 10
	var wg sync.WaitGroup
	var successCount int32
	var replayCount int32

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			err := completeUC.Execute(ctx, application.CompletePasswordResetCommand{
				Token:       token,
				NewPassword: fmt.Sprintf("PasswordVariant%d!", workerID),
			})
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			} else if errors.Is(err, application.ErrTokenAlreadyUsed) {
				atomic.AddInt32(&replayCount, 1)
			}
		}(i)
	}

	wg.Wait()

	if successCount != 1 {
		t.Errorf("expected exactly 1 successful reset, got %d", successCount)
	}
	if replayCount != workers-1 {
		t.Errorf("expected %d replay rejections, got %d", workers-1, replayCount)
	}
}

func TestPasswordReset_BoundaryPasswordLength(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	resetRepo := newInMemoryResetTokenRepo()
	sessRepo := newInMemorySessionRepo()
	sender := fakeemail.NewSender()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	ctx := context.Background()

	email, _ := domain.ParseEmail("boundary@arena.local")
	oldPass := "OriginalPassword123!"
	h, _ := hasher.HashPassword(oldPass)
	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, h)
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID: acc.ID(), PasswordHash: h, Algorithm: "argon2id", Version: 1,
	}
	requestUC := application.NewRequestPasswordResetUseCase(accRepo, resetRepo, sender, clock, rnd, domain.DefaultPasswordResetPolicy())
	completeUC := application.NewCompletePasswordResetUseCase(accRepo, credRepo, resetRepo, nil, sessRepo, hasher, sender, clock)
	if err := requestUC.Execute(ctx, application.RequestPasswordResetCommand{Email: email.String()}); err != nil {
		t.Fatalf("request reset failed: %v", err)
	}
	resetToken, ok := sender.LastResetTokenForEmail(email)
	if !ok || resetToken == "" {
		t.Fatalf("expected reset token to be sent")
	}
	// Seven characters refuse; exactly eight is the boundary and completes.
	if err := completeUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token: resetToken, NewPassword: "1234567",
	}); !errors.Is(err, application.ErrWeakPassword) {
		t.Errorf("7-char password error = %v, want ErrWeakPassword", err)
	}
	if err := completeUC.Execute(ctx, application.CompletePasswordResetCommand{
		Token: resetToken, NewPassword: "12345678",
	}); err != nil {
		t.Errorf("8-char password rejected: %v", err)
	}
}
