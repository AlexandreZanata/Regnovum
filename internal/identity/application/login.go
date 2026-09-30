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
	Email     string
	Password  string
	IPAddress string
	UserAgent string
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

// Execute performs login authentication.
// To mitigate user enumeration timing attacks (THR-AUTH-02), whenever an account is missing,
// has no password credential, or has an invalid email format, password verification is
// performed against a pre-calibrated DummyHash, ensuring constant-time response behavior.
func (uc *LoginUseCase) Execute(ctx context.Context, cmd LoginCommand) (*LoginResult, error) {
	email, err := domain.ParseEmail(cmd.Email)
	if err != nil {
		// Run dummy verification to prevent timing leaks for syntactically invalid input
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

	// Password is verified; now check account lifecycle invariants
	if !account.CanAuthenticate() {
		switch account.Status() {
		case domain.AccountStatusSuspended:
			return nil, domain.ErrAccountSuspended
		case domain.AccountStatusDeleted:
			return nil, domain.ErrAccountDeleted
		default:
			return nil, domain.ErrAccountNotActive
		}
	}

	// Transparent rehash if hasher cost parameters have evolved
	if uc.hasher.NeedsRehash(cred.PasswordHash) {
		if newHash, hashErr := uc.hasher.HashPassword(cmd.Password); hashErr == nil {
			_ = uc.credentials.UpdatePasswordCredential(ctx, account.ID(), newHash, "argon2id", cred.Version)
		}
	}

	// Generate 32 bytes (256 bits) of CSPRNG entropy for the opaque session token
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
