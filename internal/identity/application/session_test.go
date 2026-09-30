package application_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

type inMemoryCredentialRepo struct {
	mu          sync.Mutex
	credentials map[string]*application.PasswordCredentialRecord
}

func newInMemoryCredentialRepo() *inMemoryCredentialRepo {
	return &inMemoryCredentialRepo{
		credentials: make(map[string]*application.PasswordCredentialRecord),
	}
}

func (r *inMemoryCredentialRepo) GetPasswordCredential(ctx context.Context, accountID domain.AccountID) (*application.PasswordCredentialRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cred, ok := r.credentials[string(accountID)]
	if !ok {
		return nil, application.ErrCredentialNotFound
	}
	return cred, nil
}

func (r *inMemoryCredentialRepo) UpdatePasswordCredential(ctx context.Context, accountID domain.AccountID, passwordHash string, algorithm string, version int32) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	cred, ok := r.credentials[string(accountID)]
	if !ok {
		return application.ErrCredentialNotFound
	}
	cred.PasswordHash = passwordHash
	cred.Algorithm = algorithm
	cred.Version = version
	return nil
}

type inMemorySessionRepo struct {
	mu         sync.Mutex
	sessions   map[string]*domain.Session
	touchCount int64
	seq        int
}

func newInMemorySessionRepo() *inMemorySessionRepo {
	return &inMemorySessionRepo{
		sessions: make(map[string]*domain.Session),
	}
}

func (r *inMemorySessionRepo) CreateSession(
	ctx context.Context,
	accountID domain.AccountID,
	tokenHash []byte,
	expiresAt time.Time,
	ipAddress, userAgent string,
) (*domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	id := domain.SessionID(fmt.Sprintf("sess_%d", r.seq))
	createdAt := expiresAt.Add(-24 * time.Hour)
	lastSeenAt := createdAt

	sess, err := domain.ReconstituteSession(
		id, accountID, tokenHash,
		createdAt, expiresAt, lastSeenAt,
		nil, ipAddress, userAgent,
	)
	if err != nil {
		return nil, err
	}

	r.sessions[string(id)] = sess
	return sess, nil
}

type seqRandom struct {
	mu  sync.Mutex
	seq byte
}

func (s *seqRandom) Read(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	for i := range b {
		b[i] = byte(i) ^ s.seq
	}
	return len(b), nil
}

func (r *inMemorySessionRepo) GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (*domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, sess := range r.sessions {
		if sess.HasTokenHash(tokenHash) {
			return sess, nil
		}
	}
	return nil, application.ErrSessionNotFound
}

func (r *inMemorySessionRepo) TouchSession(ctx context.Context, id domain.SessionID, lastSeenAt, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	atomic.AddInt64(&r.touchCount, 1)

	sess, ok := r.sessions[string(id)]
	if !ok {
		return application.ErrSessionNotFound
	}
	sess.Touch(lastSeenAt, domain.DefaultSessionPolicy())
	return nil
}

func (r *inMemorySessionRepo) RevokeSession(ctx context.Context, tokenHash []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, sess := range r.sessions {
		if sess.HasTokenHash(tokenHash) {
			_ = sess.Revoke(time.Now().UTC())
		}
	}
	return nil
}

func (r *inMemorySessionRepo) RevokeAllAccountSessions(ctx context.Context, accountID domain.AccountID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, sess := range r.sessions {
		if sess.AccountID() == accountID {
			_ = sess.Revoke(time.Now().UTC())
		}
	}
	return nil
}

// ListActiveSessions answers the same question the storage statement does:
// the three boundaries are compared, never re-derived, so the fake cannot be
// more generous than the adapter it stands in for.
func (r *inMemorySessionRepo) ListActiveSessions(ctx context.Context, accountID domain.AccountID, window application.SessionWindow) ([]application.SessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	records := make([]application.SessionRecord, 0, len(r.sessions))
	for _, sess := range r.sessions {
		if sess.AccountID() != accountID || sess.IsRevoked() {
			continue
		}
		if !sess.ExpiresAt().After(window.Now) ||
			!sess.LastSeenAt().After(window.IdleCutoff) ||
			!sess.CreatedAt().After(window.AbsoluteCutoff) {
			continue
		}
		records = append(records, application.SessionRecord{
			ID:         sess.ID(),
			AccountID:  sess.AccountID(),
			CreatedAt:  sess.CreatedAt(),
			LastSeenAt: sess.LastSeenAt(),
			ExpiresAt:  sess.ExpiresAt(),
			IPAddress:  sess.IPAddress(),
			UserAgent:  sess.UserAgent(),
		})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].LastSeenAt.Equal(records[j].LastSeenAt) {
			return records[i].ID.String() < records[j].ID.String()
		}
		return records[i].LastSeenAt.After(records[j].LastSeenAt)
	})
	max := window.MaxRows
	if max <= 0 {
		max = application.DefaultSessionListingRows
	}
	if len(records) > max {
		records = records[:max]
	}
	return records, nil
}

// RevokeSessionByID ends one session of one account, reporting whether an
// active row changed, exactly as the statement does.
func (r *inMemorySessionRepo) RevokeSessionByID(ctx context.Context, accountID domain.AccountID, sessionID domain.SessionID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	sess, ok := r.sessions[sessionID.String()]
	if !ok || sess.AccountID() != accountID || sess.IsRevoked() {
		return false, nil
	}
	if err := sess.Revoke(time.Now().UTC()); err != nil {
		return false, err
	}
	return true, nil
}

// RevokeSessionsPastDeadline ends every session past its policy deadline and
// never deletes one, which is the property the real statement keeps.
func (r *inMemorySessionRepo) RevokeSessionsPastDeadline(ctx context.Context, window application.SessionWindow) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var revoked int64
	for _, sess := range r.sessions {
		if sess.IsRevoked() {
			continue
		}
		if sess.ExpiresAt().After(window.Now) &&
			sess.LastSeenAt().After(window.IdleCutoff) &&
			sess.CreatedAt().After(window.AbsoluteCutoff) {
			continue
		}
		if err := sess.Revoke(time.Now().UTC()); err == nil {
			revoked++
		}
	}
	return revoked, nil
}

func (r *inMemorySessionRepo) TouchCount() int64 {
	return atomic.LoadInt64(&r.touchCount)
}

func TestLoginTimingUniformity(t *testing.T) {
	// Uses real Argon2id FastParams to measure actual execution timing parity per THR-AUTH-02
	hasher, err := argon2id.New(argon2id.FastParams(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to init hasher: %v", err)
	}

	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	sessRepo := newInMemorySessionRepo()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	policy := domain.DefaultSessionPolicy()

	ctx := context.Background()

	// Seed one active user with known password
	realEmail, _ := domain.ParseEmail("existing@arena.local")
	realPass := "Secret123!"
	realHash, err := hasher.HashPassword(realPass)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	acc, err := accRepo.CreateAccountWithPassword(ctx, realEmail, realHash)
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: realHash,
		Algorithm:    "argon2id",
		Version:      1,
	}

	loginUC := application.NewLoginUseCase(accRepo, credRepo, sessRepo, hasher, clock, rnd, policy)

	// Measure non-existent email login
	startNonExistent := time.Now()
	_, errNonExistent := loginUC.Execute(ctx, application.LoginCommand{
		Email:    "nonexistent@arena.local",
		Password: "SomeAttemptedPassword123!",
	})
	elapsedNonExistent := time.Since(startNonExistent)

	// Measure existing email with wrong password
	startWrongPass := time.Now()
	_, errWrongPass := loginUC.Execute(ctx, application.LoginCommand{
		Email:    "existing@arena.local",
		Password: "SomeAttemptedPassword123!",
	})
	elapsedWrongPass := time.Since(startWrongPass)

	if !errors.Is(errNonExistent, application.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for non-existent email, got %v", errNonExistent)
	}
	if !errors.Is(errWrongPass, application.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for wrong password, got %v", errWrongPass)
	}

	// Verify both executions took genuine hashing time (e.g. > 1ms) proving neither short-circuited
	if elapsedNonExistent < 1*time.Millisecond {
		t.Errorf("non-existent account check completed suspiciously fast (%v), dummy hashing was not executed", elapsedNonExistent)
	}
	if elapsedWrongPass < 1*time.Millisecond {
		t.Errorf("wrong password check completed suspiciously fast (%v)", elapsedWrongPass)
	}

	// Verify timing ratio is within balanced bounds proving uniform execution time
	ratio := float64(elapsedNonExistent) / float64(elapsedWrongPass)
	if ratio < 0.2 || ratio > 5.0 {
		t.Errorf("timing divergence between non-existent (%v) and wrong-password (%v) ratio: %v", elapsedNonExistent, elapsedWrongPass, ratio)
	}

	t.Logf("Timing check (THR-AUTH-02): non-existent=%v, wrong-password=%v, ratio=%.2f", elapsedNonExistent, elapsedWrongPass, ratio)
}

func TestLoginSuccessAndSessionIssuance(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	sessRepo := newInMemorySessionRepo()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	policy := domain.DefaultSessionPolicy()

	ctx := context.Background()
	email, _ := domain.ParseEmail("player@arena.local")
	pass := "StrongPassword123!"
	hash, _ := hasher.HashPassword(pass)

	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, hash)
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: hash,
		Algorithm:    "argon2id",
		Version:      1,
	}

	loginUC := application.NewLoginUseCase(accRepo, credRepo, sessRepo, hasher, clock, rnd, policy)

	res, err := loginUC.Execute(ctx, application.LoginCommand{
		Email:     "player@arena.local",
		Password:  pass,
		IPAddress: "203.0.113.10",
		UserAgent: "GoyimClient/1.0",
	})
	if err != nil {
		t.Fatalf("unexpected login error: %v", err)
	}

	if res.Account.ID() != acc.ID() {
		t.Errorf("expected account ID %v, got %v", acc.ID(), res.Account.ID())
	}
	if res.Session == nil {
		t.Fatalf("expected session to be issued")
	}
	if res.Session.AccountID() != acc.ID() {
		t.Errorf("session account ID mismatch")
	}
	if res.RawToken == "" {
		t.Fatalf("expected raw opaque token")
	}

	// Verify token hash corresponds to sha256(rawToken)
	expectedHash := sha256.Sum256([]byte(res.RawToken))
	if !bytes.Equal(res.Session.TokenHash(), expectedHash[:]) {
		t.Errorf("session token hash does not match sha256 of raw token")
	}
}

func TestLoginAccountStatusRestrictions(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	sessRepo := newInMemorySessionRepo()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	policy := domain.DefaultSessionPolicy()
	ctx := context.Background()

	setupUser := func(emailStr string) (*domain.Account, string) {
		email, _ := domain.ParseEmail(emailStr)
		pass := "Password123!"
		h, _ := hasher.HashPassword(pass)
		acc, _ := accRepo.CreateAccountWithPassword(ctx, email, h)
		credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
			AccountID:    acc.ID(),
			PasswordHash: h,
			Algorithm:    "argon2id",
			Version:      1,
		}
		return acc, pass
	}

	loginUC := application.NewLoginUseCase(accRepo, credRepo, sessRepo, hasher, clock, rnd, policy)

	// 1. Pending account (not verified)
	pendingAcc, pendingPass := setupUser("pending@arena.local")
	_, err := loginUC.Execute(ctx, application.LoginCommand{
		Email:    pendingAcc.Email().String(),
		Password: pendingPass,
	})
	if !errors.Is(err, domain.ErrAccountNotActive) {
		t.Errorf("expected ErrAccountNotActive for pending user, got %v", err)
	}

	// 2. Suspended account
	suspendedAcc, suspendedPass := setupUser("suspended@arena.local")
	_ = suspendedAcc.VerifyEmail(clock.Now())
	_ = suspendedAcc.Suspend(clock.Now())
	_, err = loginUC.Execute(ctx, application.LoginCommand{
		Email:    suspendedAcc.Email().String(),
		Password: suspendedPass,
	})
	if !errors.Is(err, domain.ErrAccountSuspended) {
		t.Errorf("expected ErrAccountSuspended for suspended user, got %v", err)
	}

	// 3. Deleted account
	deletedAcc, deletedPass := setupUser("deleted@arena.local")
	_ = deletedAcc.VerifyEmail(clock.Now())
	_ = deletedAcc.MarkDeleted(clock.Now())
	_, err = loginUC.Execute(ctx, application.LoginCommand{
		Email:    deletedAcc.Email().String(),
		Password: deletedPass,
	})
	if !errors.Is(err, domain.ErrAccountDeleted) {
		t.Errorf("expected ErrAccountDeleted for deleted user, got %v", err)
	}
}

func TestLoginTransparentRehash(t *testing.T) {
	// Create legacy hasher with 1 iteration, then current hasher with 2 iterations
	legacyParams := argon2id.FastParams()
	legacyParams.Iterations = 1
	legacyHasher, _ := argon2id.New(legacyParams, rand.Reader)

	currentParams := argon2id.FastParams()
	currentParams.Iterations = 2
	currentHasher, _ := argon2id.New(currentParams, rand.Reader)

	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	sessRepo := newInMemorySessionRepo()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	policy := domain.DefaultSessionPolicy()
	ctx := context.Background()

	email, _ := domain.ParseEmail("rehash@arena.local")
	pass := "Password123!"
	oldHash, _ := legacyHasher.HashPassword(pass)

	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, oldHash)
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: oldHash,
		Algorithm:    "argon2id",
		Version:      1,
	}

	loginUC := application.NewLoginUseCase(accRepo, credRepo, sessRepo, currentHasher, clock, rnd, policy)

	_, err := loginUC.Execute(ctx, application.LoginCommand{
		Email:    "rehash@arena.local",
		Password: pass,
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	// Verify credential was transparently updated with current hasher parameters
	updatedCred := credRepo.credentials[string(acc.ID())]
	if updatedCred.PasswordHash == oldHash {
		t.Errorf("credential was not rehashed")
	}
	if currentHasher.NeedsRehash(updatedCred.PasswordHash) {
		t.Errorf("updated hash still reports NeedsRehash = true")
	}
}

func TestAuthenticateSessionLifecycleAndThrottling(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	sessRepo := newInMemorySessionRepo()
	startTime := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: startTime}
	rnd := &seqRandom{}
	policy := domain.DefaultSessionPolicy()
	ctx := context.Background()

	email, _ := domain.ParseEmail("sessiontest@arena.local")
	pass := "Password123!"
	h, _ := hasher.HashPassword(pass)
	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, h)
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: h,
		Algorithm:    "argon2id",
		Version:      1,
	}

	loginUC := application.NewLoginUseCase(accRepo, credRepo, sessRepo, hasher, clock, rnd, policy)
	loginRes, err := loginUC.Execute(ctx, application.LoginCommand{
		Email:    "sessiontest@arena.local",
		Password: pass,
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	authUC := application.NewAuthenticateSessionUseCase(accRepo, sessRepo, clock, policy, 5*time.Minute)

	// 1. Immediate authentication succeeds
	authRes, err := authUC.Execute(ctx, application.AuthenticateSessionCommand{
		RawToken: loginRes.RawToken,
	})
	if err != nil {
		t.Fatalf("authenticate failed: %v", err)
	}
	if authRes.Account.ID() != acc.ID() {
		t.Errorf("expected account ID %v, got %v", acc.ID(), authRes.Account.ID())
	}

	// Touch count should be 0 because 0 seconds elapsed (below 5-minute threshold)
	if sessRepo.TouchCount() != 0 {
		t.Errorf("expected 0 touches, got %d", sessRepo.TouchCount())
	}

	// 2. Advance time by 2 minutes (still under 5-minute threshold)
	clock.Advance(2 * time.Minute)
	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{
		RawToken: loginRes.RawToken,
	})
	if err != nil {
		t.Fatalf("authenticate failed: %v", err)
	}
	if sessRepo.TouchCount() != 0 {
		t.Errorf("expected 0 touches at 2m, got %d", sessRepo.TouchCount())
	}

	// 3. Advance time by 4 more minutes (total 6 minutes > 5 minutes threshold)
	clock.Advance(4 * time.Minute)
	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{
		RawToken: loginRes.RawToken,
	})
	if err != nil {
		t.Fatalf("authenticate failed: %v", err)
	}
	if sessRepo.TouchCount() != 1 {
		t.Errorf("expected 1 touch at 6m, got %d", sessRepo.TouchCount())
	}

	// 4. Another request immediately afterwards (0 elapsed since touch)
	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{
		RawToken: loginRes.RawToken,
	})
	if err != nil {
		t.Fatalf("authenticate failed: %v", err)
	}
	if sessRepo.TouchCount() != 1 {
		t.Errorf("expected still 1 touch, got %d", sessRepo.TouchCount())
	}

	// 5. Expiration check: advance by 25 hours (past 24h idle timeout)
	clock.Advance(25 * time.Hour)
	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{
		RawToken: loginRes.RawToken,
	})
	if !errors.Is(err, application.ErrSessionExpired) {
		t.Errorf("expected ErrSessionExpired, got %v", err)
	}
}

func TestRotateSession(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	sessRepo := newInMemorySessionRepo()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	policy := domain.DefaultSessionPolicy()
	ctx := context.Background()

	email, _ := domain.ParseEmail("rotate@arena.local")
	pass := "Password123!"
	h, _ := hasher.HashPassword(pass)
	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, h)
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: h,
		Algorithm:    "argon2id",
		Version:      1,
	}

	loginUC := application.NewLoginUseCase(accRepo, credRepo, sessRepo, hasher, clock, rnd, policy)
	loginRes, _ := loginUC.Execute(ctx, application.LoginCommand{
		Email:     "rotate@arena.local",
		Password:  pass,
		IPAddress: "192.0.2.1",
		UserAgent: "OldUA/1.0",
	})

	rotateUC := application.NewRotateSessionUseCase(accRepo, sessRepo, clock, rnd, policy)
	authUC := application.NewAuthenticateSessionUseCase(accRepo, sessRepo, clock, policy, 5*time.Minute)

	// Rotate session with updated client context
	rotateRes, err := rotateUC.Execute(ctx, application.RotateSessionCommand{
		CurrentRawToken: loginRes.RawToken,
		IPAddress:       "192.0.2.99",
		UserAgent:       "NewUA/2.0",
	})
	if err != nil {
		t.Fatalf("rotate failed: %v", err)
	}

	if rotateRes.RawToken == loginRes.RawToken {
		t.Errorf("rotated raw token must differ from old raw token")
	}

	// Old token should be rejected as revoked
	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{
		RawToken: loginRes.RawToken,
	})
	if !errors.Is(err, application.ErrSessionRevoked) {
		t.Errorf("expected ErrSessionRevoked for old token, got %v", err)
	}

	// New token must authenticate successfully
	newAuth, err := authUC.Execute(ctx, application.AuthenticateSessionCommand{
		RawToken: rotateRes.RawToken,
	})
	if err != nil {
		t.Fatalf("new token failed to authenticate: %v", err)
	}
	if newAuth.Account.ID() != acc.ID() {
		t.Errorf("expected account ID %v, got %v", acc.ID(), newAuth.Account.ID())
	}
	if newAuth.Session.IPAddress() != "192.0.2.99" {
		t.Errorf("expected updated IP address 192.0.2.99, got %s", newAuth.Session.IPAddress())
	}
}

func TestLogoutCurrentAndAllSessions(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	sessRepo := newInMemorySessionRepo()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	policy := domain.DefaultSessionPolicy()
	ctx := context.Background()

	email, _ := domain.ParseEmail("logout@arena.local")
	pass := "Password123!"
	h, _ := hasher.HashPassword(pass)
	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, h)
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID:    acc.ID(),
		PasswordHash: h,
		Algorithm:    "argon2id",
		Version:      1,
	}

	loginUC := application.NewLoginUseCase(accRepo, credRepo, sessRepo, hasher, clock, rnd, policy)
	authUC := application.NewAuthenticateSessionUseCase(accRepo, sessRepo, clock, policy, 5*time.Minute)
	logoutUC := application.NewLogoutUseCase(sessRepo)
	logoutAllUC := application.NewLogoutAllUseCase(sessRepo)

	// Create two sessions for the same account
	sess1, _ := loginUC.Execute(ctx, application.LoginCommand{Email: "logout@arena.local", Password: pass})
	sess2, _ := loginUC.Execute(ctx, application.LoginCommand{Email: "logout@arena.local", Password: pass})

	// Logout single session sess1
	if err := logoutUC.Execute(ctx, application.LogoutCommand{RawToken: sess1.RawToken}); err != nil {
		t.Fatalf("logout sess1 failed: %v", err)
	}

	// sess1 is now revoked
	_, err := authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: sess1.RawToken})
	if !errors.Is(err, application.ErrSessionRevoked) {
		t.Errorf("expected ErrSessionRevoked for sess1, got %v", err)
	}

	// sess2 is still active
	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: sess2.RawToken})
	if err != nil {
		t.Fatalf("expected sess2 to remain active, got %v", err)
	}

	// Now create sess3 and logout all
	sess3, _ := loginUC.Execute(ctx, application.LoginCommand{Email: "logout@arena.local", Password: pass})

	if err := logoutAllUC.Execute(ctx, application.LogoutAllCommand{AccountID: acc.ID()}); err != nil {
		t.Fatalf("logout all failed: %v", err)
	}

	// Both sess2 and sess3 must now be revoked
	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: sess2.RawToken})
	if !errors.Is(err, application.ErrSessionRevoked) {
		t.Errorf("expected ErrSessionRevoked for sess2 after logout all, got %v", err)
	}

	_, err = authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: sess3.RawToken})
	if !errors.Is(err, application.ErrSessionRevoked) {
		t.Errorf("expected ErrSessionRevoked for sess3 after logout all, got %v", err)
	}
}

func TestAuthenticateSession_NonPositiveThresholdFallsBackToDefault(t *testing.T) {
	for _, threshold := range []time.Duration{0, -time.Minute} {
		t.Run(threshold.String(), func(t *testing.T) {
			hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
			accRepo := newInMemoryAccountRepo()
			credRepo := newInMemoryCredentialRepo()
			sessRepo := newInMemorySessionRepo()
			clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
			rnd := &seqRandom{}
			policy := domain.DefaultSessionPolicy()
			ctx := context.Background()

			email, _ := domain.ParseEmail("threshold@arena.local")
			pass := "Password123!"
			h, _ := hasher.HashPassword(pass)
			acc, _ := accRepo.CreateAccountWithPassword(ctx, email, h)
			_ = acc.VerifyEmail(clock.Now())
			credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
				AccountID: acc.ID(), PasswordHash: h, Algorithm: "argon2id", Version: 1,
			}
			loginUC := application.NewLoginUseCase(accRepo, credRepo, sessRepo, hasher, clock, rnd, policy)
			loginRes, err := loginUC.Execute(ctx, application.LoginCommand{
				Email: "threshold@arena.local", Password: pass,
			})
			if err != nil {
				t.Fatalf("login failed: %v", err)
			}
			authUC := application.NewAuthenticateSessionUseCase(accRepo, sessRepo, clock, policy, threshold)

			if _, err := authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: loginRes.RawToken}); err != nil {
				t.Fatalf("authenticate failed: %v", err)
			}
			// A non-positive threshold selects the default: no touch
			// immediately and none at two minutes, exactly as the
			// five-minute default behaves.
			if got := sessRepo.TouchCount(); got != 0 {
				t.Fatalf("touches immediately = %d, want 0", got)
			}
			clock.Advance(2 * time.Minute)
			if _, err := authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: loginRes.RawToken}); err != nil {
				t.Fatalf("authenticate failed: %v", err)
			}
			if got := sessRepo.TouchCount(); got != 0 {
				t.Fatalf("touches at 2m = %d, want 0", got)
			}
			clock.Advance(4 * time.Minute)
			if _, err := authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: loginRes.RawToken}); err != nil {
				t.Fatalf("authenticate failed: %v", err)
			}
			if got := sessRepo.TouchCount(); got != 1 {
				t.Fatalf("touches at 6m = %d, want 1", got)
			}
		})
	}
}

func TestRotateSession_InheritsClientContextWhenEmpty(t *testing.T) {
	hasher, _ := argon2id.New(argon2id.FastParams(), rand.Reader)
	accRepo := newInMemoryAccountRepo()
	credRepo := newInMemoryCredentialRepo()
	sessRepo := newInMemorySessionRepo()
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &seqRandom{}
	policy := domain.DefaultSessionPolicy()
	ctx := context.Background()

	email, _ := domain.ParseEmail("inherit@arena.local")
	pass := "Password123!"
	h, _ := hasher.HashPassword(pass)
	acc, _ := accRepo.CreateAccountWithPassword(ctx, email, h)
	_ = acc.VerifyEmail(clock.Now())
	credRepo.credentials[string(acc.ID())] = &application.PasswordCredentialRecord{
		AccountID: acc.ID(), PasswordHash: h, Algorithm: "argon2id", Version: 1,
	}
	loginUC := application.NewLoginUseCase(accRepo, credRepo, sessRepo, hasher, clock, rnd, policy)
	loginRes, err := loginUC.Execute(ctx, application.LoginCommand{
		Email: "inherit@arena.local", Password: pass,
		IPAddress: "192.0.2.1", UserAgent: "OldUA/1.0",
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	rotateUC := application.NewRotateSessionUseCase(accRepo, sessRepo, clock, rnd, policy)
	rotateRes, err := rotateUC.Execute(ctx, application.RotateSessionCommand{
		CurrentRawToken: loginRes.RawToken,
	})
	if err != nil {
		t.Fatalf("rotate failed: %v", err)
	}
	authUC := application.NewAuthenticateSessionUseCase(accRepo, sessRepo, clock, policy, 5*time.Minute)
	newAuth, err := authUC.Execute(ctx, application.AuthenticateSessionCommand{RawToken: rotateRes.RawToken})
	if err != nil {
		t.Fatalf("new token failed to authenticate: %v", err)
	}
	if newAuth.Session.IPAddress() != "192.0.2.1" {
		t.Errorf("rotated IP = %q, want the inherited 192.0.2.1", newAuth.Session.IPAddress())
	}
	if newAuth.Session.UserAgent() != "OldUA/1.0" {
		t.Errorf("rotated UA = %q, want the inherited OldUA/1.0", newAuth.Session.UserAgent())
	}
}

func TestSessionWindowFor_NonPositiveRowsSelectDefault(t *testing.T) {
	policy := domain.DefaultSessionPolicy()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	for _, rows := range []int{0, -10} {
		window := application.SessionWindowFor(now, policy, rows)
		if window.MaxRows != application.DefaultSessionListingRows {
			t.Errorf("maxRows=%d selects %d, want default %d", rows, window.MaxRows, application.DefaultSessionListingRows)
		}
	}
	window := application.SessionWindowFor(now, policy, 7)
	if window.MaxRows != 7 {
		t.Errorf("maxRows=7 selects %d, want 7", window.MaxRows)
	}
}
