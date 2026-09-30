package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// In-memory stubs for unit testing

type inMemoryAccountRepo struct {
	mu            sync.Mutex
	accounts      map[string]*domain.Account
	verifiedCalls []string
	getByEmailErr error
}

func newInMemoryAccountRepo() *inMemoryAccountRepo {
	return &inMemoryAccountRepo{
		accounts: make(map[string]*domain.Account),
	}
}

func (r *inMemoryAccountRepo) CreateAccountWithPassword(ctx context.Context, email domain.Email, passwordHash string) (*domain.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, acc := range r.accounts {
		if acc.Email().Equals(email) {
			return nil, application.ErrDuplicateEmail
		}
	}

	id := domain.AccountID("acc_" + email.Local())
	now := time.Now().UTC()
	acc, err := domain.NewAccount(id, email, now)
	if err != nil {
		return nil, err
	}
	r.accounts[string(id)] = acc
	return acc, nil
}

func (r *inMemoryAccountRepo) GetAccountByEmail(ctx context.Context, email domain.Email) (*domain.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.getByEmailErr != nil {
		return nil, r.getByEmailErr
	}
	for _, acc := range r.accounts {
		if acc.Email().Equals(email) {
			return acc, nil
		}
	}
	return nil, application.ErrAccountNotFound
}

func (r *inMemoryAccountRepo) GetAccountByID(ctx context.Context, id domain.AccountID) (*domain.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	acc, ok := r.accounts[string(id)]
	if !ok {
		return nil, application.ErrAccountNotFound
	}
	return acc, nil
}

func (r *inMemoryAccountRepo) SetEmailVerified(ctx context.Context, id domain.AccountID, verifiedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	acc, ok := r.accounts[string(id)]
	if !ok {
		return application.ErrAccountNotFound
	}
	r.verifiedCalls = append(r.verifiedCalls, string(id))
	if acc.Status() == domain.AccountStatusPending {
		return acc.VerifyEmail(verifiedAt)
	}
	return nil
}

type inMemoryTokenRepo struct {
	mu     sync.Mutex
	tokens map[string]*application.VerificationTokenRecord
	seq    int
}

func newInMemoryTokenRepo() *inMemoryTokenRepo {
	return &inMemoryTokenRepo{
		tokens: make(map[string]*application.VerificationTokenRecord),
	}
}

func (r *inMemoryTokenRepo) CreateVerificationToken(ctx context.Context, accountID domain.AccountID, tokenHash []byte, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	id := string(rune(r.seq))
	r.tokens[string(tokenHash)] = &application.VerificationTokenRecord{
		ID:        id,
		AccountID: accountID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
		UsedAt:    nil,
		CreatedAt: time.Now().UTC(),
	}
	return nil
}

func (r *inMemoryTokenRepo) GetVerificationToken(ctx context.Context, tokenHash []byte) (*application.VerificationTokenRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.tokens[string(tokenHash)]
	if !ok {
		return nil, application.ErrInvalidToken
	}
	copyRec := *rec
	return &copyRec, nil
}

func (r *inMemoryTokenRepo) MarkTokenUsed(ctx context.Context, tokenID string, usedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, rec := range r.tokens {
		if rec.ID == tokenID {
			t := usedAt
			rec.UsedAt = &t
			return nil
		}
	}
	return application.ErrInvalidToken
}

func (r *inMemoryTokenRepo) InvalidateActiveTokens(ctx context.Context, accountID domain.AccountID) error {
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

type fakePasswordHasher struct{}

func (f fakePasswordHasher) HashPassword(p string) (string, error) {
	return "hashed_" + p, nil
}

func (f fakePasswordHasher) VerifyPassword(p, h string) (bool, error) {
	return h == "hashed_"+p, nil
}

func (f fakePasswordHasher) NeedsRehash(h string) bool { return false }
func (f fakePasswordHasher) DummyHash() string         { return "dummy_hash" }

// failingPasswordHasher proves the anti-enumeration path never touches the
// hasher: a pending address re-registering gets the uniform response and a
// fresh challenge even when hashing is unavailable (mutation gate:
// register_account.go:69 — either negation must keep failing closed on the
// lookup result instead of hashing first).
type failingPasswordHasher struct{ fakePasswordHasher }

func (f failingPasswordHasher) HashPassword(p string) (string, error) {
	return "", errors.New("hashing unavailable")
}

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

type stubRandom struct {
	val byte
}

func (s stubRandom) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = s.val
	}
	return len(b), nil
}

type memoryEmailSender struct {
	mu     sync.Mutex
	emails []struct {
		Email domain.Email
		Token string
	}
}

func (m *memoryEmailSender) SendVerificationEmail(ctx context.Context, email domain.Email, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emails = append(m.emails, struct {
		Email domain.Email
		Token string
	}{Email: email, Token: token})
	return nil
}

func (m *memoryEmailSender) SendPasswordResetEmail(ctx context.Context, email domain.Email, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emails = append(m.emails, struct {
		Email domain.Email
		Token string
	}{Email: email, Token: token})
	return nil
}

// SendPasswordChangedEmail records a password change notice. The change
// identifier is what tells two notices apart; the notice carries no token.
func (m *memoryEmailSender) SendPasswordChangedEmail(ctx context.Context, email domain.Email, changeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emails = append(m.emails, struct {
		Email domain.Email
		Token string
	}{Email: email, Token: changeID})
	return nil
}

func (m *memoryEmailSender) LastToken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.emails) == 0 {
		return ""
	}
	return m.emails[len(m.emails)-1].Token
}

func (m *memoryEmailSender) sentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.emails)
}

func TestRegisterAccount_SuccessAndVerification(t *testing.T) {
	ctx := context.Background()
	accRepo := newInMemoryAccountRepo()
	tokenRepo := newInMemoryTokenRepo()
	emailSender := &memoryEmailSender{}
	clock := &fakeClock{now: time.Unix(1700000000, 0).UTC()}
	policy := domain.DefaultVerificationPolicy()

	registerUC := application.NewRegisterAccountUseCase(
		accRepo, tokenRepo, fakePasswordHasher{}, emailSender, clock, stubRandom{val: 0x01}, policy,
	)
	verifyUC := application.NewVerifyEmailUseCase(accRepo, tokenRepo, clock)

	// 1. Register new user
	res, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "alice@example.com",
		Password: "SecurePassword123!",
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	if res.Email != "alice@example.com" {
		t.Errorf("res.Email = %q, want alice@example.com", res.Email)
	}

	// 2. Token was sent
	token := emailSender.LastToken()
	if token == "" {
		t.Fatal("expected verification email to be sent")
	}

	// 3. Verify email with token
	if err := verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: token}); err != nil {
		t.Fatalf("verify failed: %v", err)
	}

	// 4. Account is now Active
	email, _ := domain.ParseEmail("alice@example.com")
	acc, err := accRepo.GetAccountByEmail(ctx, email)
	if err != nil {
		t.Fatalf("get account failed: %v", err)
	}
	if acc.Status() != domain.AccountStatusActive {
		t.Errorf("expected status Active, got %s", acc.Status())
	}
	if !acc.IsVerified() {
		t.Error("expected account to be verified")
	}
}

func TestRegisterAccount_WeakPasswordAndInvalidEmail(t *testing.T) {
	ctx := context.Background()
	accRepo := newInMemoryAccountRepo()
	tokenRepo := newInMemoryTokenRepo()
	emailSender := &memoryEmailSender{}
	clock := &fakeClock{now: time.Unix(1700000000, 0).UTC()}

	registerUC := application.NewRegisterAccountUseCase(
		accRepo, tokenRepo, fakePasswordHasher{}, emailSender, clock, stubRandom{val: 0x01}, domain.DefaultVerificationPolicy(),
	)

	// Weak password
	_, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "user@example.com",
		Password: "short",
	})
	if !errors.Is(err, application.ErrWeakPassword) {
		t.Errorf("expected ErrWeakPassword, got %v", err)
	}

	// Invalid email
	_, err = registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "not-an-email",
		Password: "ValidPassword123!",
	})
	if !errors.Is(err, domain.ErrInvalidEmail) {
		t.Errorf("expected ErrInvalidEmail, got %v", err)
	}
}

func TestVerifyEmail_ReplayProtection(t *testing.T) {
	ctx := context.Background()
	accRepo := newInMemoryAccountRepo()
	tokenRepo := newInMemoryTokenRepo()
	emailSender := &memoryEmailSender{}
	clock := &fakeClock{now: time.Unix(1700000000, 0).UTC()}

	registerUC := application.NewRegisterAccountUseCase(
		accRepo, tokenRepo, fakePasswordHasher{}, emailSender, clock, stubRandom{val: 0x01}, domain.DefaultVerificationPolicy(),
	)
	verifyUC := application.NewVerifyEmailUseCase(accRepo, tokenRepo, clock)

	_, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "replay@example.com",
		Password: "ValidPassword123!",
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	token := emailSender.LastToken()

	// First verification: success
	if err := verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: token}); err != nil {
		t.Fatalf("first verification failed: %v", err)
	}

	// The verification must persist through the store: the domain mutation
	// alone is invisible to a real store, so skipping the call would leave
	// the account pending there (mutation gate: verify_email.go:70).
	if len(accRepo.verifiedCalls) != 1 {
		t.Fatalf("SetEmailVerified calls = %d, want exactly 1", len(accRepo.verifiedCalls))
	}

	// Second verification (replay attack): must fail
	err = verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: token})
	if !errors.Is(err, application.ErrTokenAlreadyUsed) {
		t.Errorf("expected ErrTokenAlreadyUsed on replay, got: %v", err)
	}
}

func TestVerifyEmail_ExpiredToken(t *testing.T) {
	ctx := context.Background()
	accRepo := newInMemoryAccountRepo()
	tokenRepo := newInMemoryTokenRepo()
	emailSender := &memoryEmailSender{}
	clock := &fakeClock{now: time.Unix(1700000000, 0).UTC()}

	registerUC := application.NewRegisterAccountUseCase(
		accRepo, tokenRepo, fakePasswordHasher{}, emailSender, clock, stubRandom{val: 0x01}, domain.DefaultVerificationPolicy(),
	)
	verifyUC := application.NewVerifyEmailUseCase(accRepo, tokenRepo, clock)

	_, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "expired@example.com",
		Password: "ValidPassword123!",
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	token := emailSender.LastToken()

	// Advance clock past 24 hours (expiration window)
	clock.Advance(24*time.Hour + 1*time.Minute)

	err = verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: token})
	if !errors.Is(err, application.ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got: %v", err)
	}
}

func TestIssueEmailVerification_ReissuanceInvalidatesPreviousToken(t *testing.T) {
	ctx := context.Background()
	accRepo := newInMemoryAccountRepo()
	tokenRepo := newInMemoryTokenRepo()
	emailSender := &memoryEmailSender{}
	clock := &fakeClock{now: time.Unix(1700000000, 0).UTC()}
	policy := domain.DefaultVerificationPolicy()

	registerUC := application.NewRegisterAccountUseCase(
		accRepo, tokenRepo, fakePasswordHasher{}, emailSender, clock, stubRandom{val: 0x01}, policy,
	)
	issueUC := application.NewIssueEmailVerificationUseCase(
		accRepo, tokenRepo, emailSender, clock, stubRandom{val: 0x02}, policy,
	)
	verifyUC := application.NewVerifyEmailUseCase(accRepo, tokenRepo, clock)

	// 1. Initial registration generates Token 1
	_, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "reissue@example.com",
		Password: "ValidPassword123!",
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	token1 := emailSender.LastToken()

	// 2. User requests a new verification email -> generates Token 2
	err = issueUC.Execute(ctx, application.IssueEmailVerificationCommand{
		Email: "reissue@example.com",
	})
	if err != nil {
		t.Fatalf("reissue failed: %v", err)
	}
	token2 := emailSender.LastToken()

	if token1 == token2 {
		t.Fatal("token1 and token2 must be distinct")
	}

	// 3. Attempt to verify with Token 1 must fail because reissuance invalidated it
	err = verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: token1})
	if !errors.Is(err, application.ErrTokenAlreadyUsed) {
		t.Errorf("expected ErrTokenAlreadyUsed for superseded token1, got: %v", err)
	}

	// 4. Token 2 must succeed
	if err := verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: token2}); err != nil {
		t.Fatalf("token2 verification failed: %v", err)
	}
}

func TestRegisterAccount_AntiEnumeration(t *testing.T) {
	ctx := context.Background()
	accRepo := newInMemoryAccountRepo()
	tokenRepo := newInMemoryTokenRepo()
	emailSender := &memoryEmailSender{}
	clock := &fakeClock{now: time.Unix(1700000000, 0).UTC()}
	policy := domain.DefaultVerificationPolicy()

	registerUC := application.NewRegisterAccountUseCase(
		accRepo, tokenRepo, fakePasswordHasher{}, emailSender, clock, stubRandom{val: 0x01}, policy,
	)
	verifyUC := application.NewVerifyEmailUseCase(accRepo, tokenRepo, clock)

	// 1. Register and verify account
	_, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "active@example.com",
		Password: "ValidPassword123!",
	})
	if err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	token := emailSender.LastToken()
	_ = verifyUC.Execute(ctx, application.VerifyEmailCommand{Token: token})

	// 2. Attempt to register again with same email
	res, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "active@example.com",
		Password: "DifferentPassword123!",
	})
	if err != nil {
		t.Fatalf("second register should not error (anti-enumeration): %v", err)
	}
	if res.Email != "active@example.com" {
		t.Errorf("res.Email = %q, want active@example.com", res.Email)
	}
}

func TestRegisterAccount_BoundaryPasswordLength(t *testing.T) {
	ctx := context.Background()
	accRepo := newInMemoryAccountRepo()
	tokenRepo := newInMemoryTokenRepo()
	emailSender := &memoryEmailSender{}
	clock := &fakeClock{now: time.Unix(1700000000, 0).UTC()}

	registerUC := application.NewRegisterAccountUseCase(
		accRepo, tokenRepo, fakePasswordHasher{}, emailSender, clock, stubRandom{val: 0x01}, domain.DefaultVerificationPolicy(),
	)

	// Seven characters refuse; exactly eight is the boundary and accepts.
	if _, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "seven@example.com",
		Password: "1234567",
	}); !errors.Is(err, application.ErrWeakPassword) {
		t.Errorf("7-char password error = %v, want ErrWeakPassword", err)
	}
	res, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "eight@example.com",
		Password: "12345678",
	})
	if err != nil {
		t.Fatalf("8-char password rejected: %v", err)
	}
	if res.Email != "eight@example.com" {
		t.Errorf("res.Email = %q, want eight@example.com", res.Email)
	}
}

func TestRegisterAccount_PendingEmailReissuesVerification(t *testing.T) {
	ctx := context.Background()
	accRepo := newInMemoryAccountRepo()
	tokenRepo := newInMemoryTokenRepo()
	emailSender := &memoryEmailSender{}
	clock := &fakeClock{now: time.Unix(1700000000, 0).UTC()}
	policy := domain.DefaultVerificationPolicy()

	registerUC := application.NewRegisterAccountUseCase(
		accRepo, tokenRepo, fakePasswordHasher{}, emailSender, clock, stubRandom{val: 0x01}, policy,
	)

	if _, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "pending@example.com",
		Password: "ValidPassword123!",
	}); err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	if got := emailSender.sentCount(); got != 1 {
		t.Fatalf("sent emails = %d, want 1", got)
	}

	// A pending address re-registering gets the uniform response and a
	// fresh verification challenge, without leaking the pending state.
	res, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "pending@example.com",
		Password: "AnotherPassword123!",
	})
	if err != nil {
		t.Fatalf("pending re-register should not error (anti-enumeration): %v", err)
	}
	if res.Email != "pending@example.com" {
		t.Errorf("res.Email = %q, want pending@example.com", res.Email)
	}
	if got := emailSender.sentCount(); got != 2 {
		t.Errorf("sent emails = %d, want 2 (reissued verification)", got)
	}
}

func TestRegisterAccount_RepositoryErrorPropagates(t *testing.T) {

	ctx := context.Background()
	accRepo := newInMemoryAccountRepo()
	tokenRepo := newInMemoryTokenRepo()
	emailSender := &memoryEmailSender{}
	clock := &fakeClock{now: time.Unix(1700000000, 0).UTC()}

	accRepo.getByEmailErr = errors.New("store unavailable")
	registerUC := application.NewRegisterAccountUseCase(
		accRepo, tokenRepo, fakePasswordHasher{}, emailSender, clock, stubRandom{val: 0x01}, domain.DefaultVerificationPolicy(),
	)
	if _, err := registerUC.Execute(ctx, application.RegisterAccountCommand{
		Email:    "user@example.com",
		Password: "ValidPassword123!",
	}); err == nil || errors.Is(err, application.ErrAccountNotFound) {
		t.Errorf("store error = %v, want the store failure to propagate", err)
	}
}

func TestRegisterAccount_PendingPathNeverHashes(t *testing.T) {
	ctx := context.Background()
	accRepo := newInMemoryAccountRepo()
	tokenRepo := newInMemoryTokenRepo()
	emailSender := &memoryEmailSender{}
	clock := &fakeClock{now: time.Unix(1700000000, 0).UTC()}
	policy := domain.DefaultVerificationPolicy()

	working := application.NewRegisterAccountUseCase(
		accRepo, tokenRepo, fakePasswordHasher{}, emailSender, clock, stubRandom{val: 0x01}, policy,
	)
	if _, err := working.Execute(ctx, application.RegisterAccountCommand{
		Email:    "nohash@example.com",
		Password: "ValidPassword123!",
	}); err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	broken := application.NewRegisterAccountUseCase(
		accRepo, tokenRepo, failingPasswordHasher{}, emailSender, clock, stubRandom{val: 0x01}, policy,
	)
	res, err := broken.Execute(ctx, application.RegisterAccountCommand{
		Email:    "nohash@example.com",
		Password: "AnotherPassword123!",
	})
	if err != nil {
		t.Fatalf("pending re-register with failing hasher should not error: %v", err)
	}
	if res.Email != "nohash@example.com" {
		t.Errorf("res.Email = %q, want nohash@example.com", res.Email)
	}
	if got := emailSender.sentCount(); got != 2 {
		t.Errorf("sent emails = %d, want 2 (reissued verification)", got)
	}
}
