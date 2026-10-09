package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// LoginCommand holds the input parameters for account authentication and session establishment.
type LoginCommand struct {
	Email      string
	Password   string
	AccountKey string
	IPAddress  string
	UserAgent  string
}

// LoginResult contains the authenticated account, session, and raw opaque token.
type LoginResult struct {
	Account  *domain.Account
	Session  *domain.Session
	RawToken string
}

// LoginUseCase coordinates credential verification, anti-enumeration timing guarantees (THR-AUTH-02),
// transparent credential rehashing, and session creation.
type LoginUseCase struct {
	accounts      AccountRepository
	credentials   PasswordCredentialRepository
	sessions      SessionRepository
	keyRepo       AccountKeyCredentialRepository
	hasher        PasswordHasher
	clock         Clock
	random        Random
	sessionPolicy domain.SessionPolicy
}

// NewLoginUseCase constructs a LoginUseCase.
func NewLoginUseCase(
	accounts AccountRepository,
	credentials PasswordCredentialRepository,
	sessions SessionRepository,
	hasher PasswordHasher,
	clock Clock,
	random Random,
	policy domain.SessionPolicy,
) *LoginUseCase {
	return &LoginUseCase{
		accounts:      accounts,
		credentials:   credentials,
		sessions:      sessions,
		hasher:        hasher,
		clock:         clock,
		random:        random,
		sessionPolicy: policy,
	}
}

// WithKeyRepository attaches the account key repository to the use case.
func (uc *LoginUseCase) WithKeyRepository(repo AccountKeyCredentialRepository) *LoginUseCase {
	uc.keyRepo = repo
	return uc
}

// Execute performs login authentication.
// To mitigate user enumeration timing attacks (THR-AUTH-02), whenever an account is missing,
// has no password credential, or has an invalid email format, password verification is
// performed against a pre-calibrated DummyHash, ensuring constant-time response behavior.
func (uc *LoginUseCase) Execute(ctx context.Context, cmd LoginCommand) (*LoginResult, error) {
	if cmd.AccountKey != "" {
		return uc.loginWithKey(ctx, cmd)
	}
	return uc.loginWithPassword(ctx, cmd)
}

func (uc *LoginUseCase) loginWithKey(ctx context.Context, cmd LoginCommand) (*LoginResult, error) {
	canonicalKey, err := domain.CanonicalizeAccountKey(cmd.AccountKey)
	if err != nil || uc.keyRepo == nil {
		return nil, ErrInvalidCredentials
	}
	lookup := domain.ComputeKeyLookup(canonicalKey)
	cred, account, err := uc.keyRepo.GetAccountKeyCredentialByLookup(ctx, lookup)
	if err != nil || cred == nil || account == nil {
		return nil, ErrInvalidCredentials
	}
	if !domain.VerifyKeyHash(cred.KeySalt, canonicalKey, cred.KeyHash) {
		return nil, ErrInvalidCredentials
	}
	if err := checkAccountAuthentication(account); err != nil {
		return nil, err
	}
	return uc.createLoginSession(ctx, cmd, account)
}

func (uc *LoginUseCase) loginWithPassword(ctx context.Context, cmd LoginCommand) (*LoginResult, error) {
	email, err := domain.ParseEmail(cmd.Email)
	if err != nil {
		_, _ = uc.hasher.VerifyPassword(cmd.Password, uc.hasher.DummyHash())
		return nil, ErrInvalidCredentials
	}

	account, err := uc.accounts.GetAccountByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			_, _ = uc.hasher.VerifyPassword(cmd.Password, uc.hasher.DummyHash())
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	cred, err := uc.credentials.GetPasswordCredential(ctx, account.ID())
	if err != nil {
		if errors.Is(err, ErrCredentialNotFound) {
			_, _ = uc.hasher.VerifyPassword(cmd.Password, uc.hasher.DummyHash())
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	valid, err := uc.hasher.VerifyPassword(cmd.Password, cred.PasswordHash)
	if err != nil || !valid {
		return nil, ErrInvalidCredentials
	}

	if err := checkAccountAuthentication(account); err != nil {
		return nil, err
	}

	if uc.hasher.NeedsRehash(cred.PasswordHash) {
		if newHash, hashErr := uc.hasher.HashPassword(cmd.Password); hashErr == nil {
			_ = uc.credentials.UpdatePasswordCredential(ctx, account.ID(), newHash, "argon2id", cred.Version)
		}
	}

	return uc.createLoginSession(ctx, cmd, account)
}

func checkAccountAuthentication(account *domain.Account) error {
	if !account.CanAuthenticate() {
		switch account.Status() {
		case domain.AccountStatusSuspended:
			return domain.ErrAccountSuspended
		case domain.AccountStatusDeleted:
			return domain.ErrAccountDeleted
		default:
			return domain.ErrAccountNotActive
		}
	}
	return nil
}

func (uc *LoginUseCase) createLoginSession(ctx context.Context, cmd LoginCommand, account *domain.Account) (*LoginResult, error) {
	tokenBytes := make([]byte, 32)
	if _, err := uc.random.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate session token entropy: %w", err)
	}

	rawToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(rawToken))

	now := uc.clock.Now()
	expiresAt := uc.sessionPolicy.ExpiryInstant(now, now)

	session, err := uc.sessions.CreateSession(
		ctx,
		account.ID(),
		tokenHash[:],
		expiresAt,
		cmd.IPAddress,
		cmd.UserAgent,
	)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	return &LoginResult{
		Account:  account,
		Session:  session,
		RawToken: rawToken,
	}, nil
}
