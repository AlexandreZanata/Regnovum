package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// RegisterAccountCommand holds the parameters for new user registration.
type RegisterAccountCommand struct {
	Email    string
	Password string
	Username string
}

// RegisterAccountResult represents the outcome of a registration request.
type RegisterAccountResult struct {
	AccountID  string
	Email      string
	Username   string
	AccountKey string
}

// RegisterAccountUseCase coordinates account creation and initial email verification.
type RegisterAccountUseCase struct {
	accounts           AccountRepository
	tokens             VerificationTokenRepository
	keyRepo            AccountKeyCredentialRepository
	hasher             PasswordHasher
	sender             EmailSender
	clock              Clock
	random             Random
	verificationPolicy domain.VerificationPolicy
}

// NewRegisterAccountUseCase creates an instance of RegisterAccountUseCase.
func NewRegisterAccountUseCase(
	accounts AccountRepository,
	tokens VerificationTokenRepository,
	hasher PasswordHasher,
	sender EmailSender,
	clock Clock,
	random Random,
	policy domain.VerificationPolicy,
) *RegisterAccountUseCase {
	return &RegisterAccountUseCase{
		accounts:           accounts,
		tokens:             tokens,
		hasher:             hasher,
		sender:             sender,
		clock:              clock,
		random:             random,
		verificationPolicy: policy,
	}
}

// WithKeyRepository attaches the account key repository to the use case.
func (uc *RegisterAccountUseCase) WithKeyRepository(repo AccountKeyCredentialRepository) *RegisterAccountUseCase {
	uc.keyRepo = repo
	return uc
}

// Execute performs account registration while preventing user enumeration.
func (uc *RegisterAccountUseCase) Execute(ctx context.Context, cmd RegisterAccountCommand) (*RegisterAccountResult, error) {
	if cmd.Username != "" {
		return uc.registerWithUsername(ctx, cmd)
	}
	return uc.registerWithEmail(ctx, cmd)
}

func (uc *RegisterAccountUseCase) registerWithUsername(ctx context.Context, cmd RegisterAccountCommand) (*RegisterAccountResult, error) {
	if err := domain.ValidateUsername(cmd.Username); err != nil {
		return nil, err
	}
	if uc.keyRepo == nil {
		return nil, errors.New("key repository not configured")
	}
	rawKey, err := domain.GenerateAccountKey()
	if err != nil {
		return nil, err
	}
	salt, err := domain.GenerateKeySalt()
	if err != nil {
		return nil, err
	}
	keyLookup := domain.ComputeKeyLookup(rawKey)
	keyHash := domain.ComputeKeyHash(salt, rawKey)
	usernameHash := domain.ComputeUsernameHash(cmd.Username)
	record := AccountKeyCredentialRecord{
		UsernameHash: usernameHash,
		KeyLookup:    keyLookup,
		KeySalt:      salt,
		KeyHash:      keyHash,
	}
	account, err := uc.keyRepo.CreateAccountWithKey(ctx, cmd.Username, record)
	if err != nil {
		return nil, err
	}
	return &RegisterAccountResult{
		AccountID:  account.ID().String(),
		Email:      account.Email().String(),
		Username:   cmd.Username,
		AccountKey: rawKey,
	}, nil
}

func (uc *RegisterAccountUseCase) registerWithEmail(ctx context.Context, cmd RegisterAccountCommand) (*RegisterAccountResult, error) {
	email, err := domain.ParseEmail(cmd.Email)
	if err != nil {
		return nil, err
	}

	if len(cmd.Password) < 8 {
		return nil, ErrWeakPassword
	}

	// Prevent user enumeration by checking if the email is already registered.
	existing, err := uc.accounts.GetAccountByEmail(ctx, email)
	if err == nil && existing != nil {
		if existing.Status() == domain.AccountStatusPending {
			_ = uc.issueVerification(ctx, existing.ID(), email)
		}
		// Return uniform response without leaking account state
		return &RegisterAccountResult{
			AccountID: existing.ID().String(),
			Email:     email.String(),
		}, nil
	} else if err != nil && !errors.Is(err, ErrAccountNotFound) {
		return nil, err
	}

	passwordHash, err := uc.hasher.HashPassword(cmd.Password)
	if err != nil {
		return nil, err
	}

	account, err := uc.accounts.CreateAccountWithPassword(ctx, email, passwordHash)
	if err != nil {
		if errors.Is(err, ErrDuplicateEmail) {
			// Concurrent race condition: account was inserted between our lookup and insert
			existingAcc, fetchErr := uc.accounts.GetAccountByEmail(ctx, email)
			if fetchErr == nil && existingAcc != nil {
				if existingAcc.Status() == domain.AccountStatusPending {
					_ = uc.issueVerification(ctx, existingAcc.ID(), email)
				}
				return &RegisterAccountResult{
					AccountID: existingAcc.ID().String(),
					Email:     email.String(),
				}, nil
			}
			return &RegisterAccountResult{Email: email.String()}, nil
		}
		return nil, err
	}

	if err := uc.issueVerification(ctx, account.ID(), email); err != nil {
		return nil, err
	}

	return &RegisterAccountResult{
		AccountID: account.ID().String(),
		Email:     email.String(),
	}, nil
}

func (uc *RegisterAccountUseCase) issueVerification(ctx context.Context, accountID domain.AccountID, email domain.Email) error {
	tokenBytes := make([]byte, 32)
	if _, err := uc.random.Read(tokenBytes); err != nil {
		return err
	}

	rawToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(rawToken))
	expiresAt := uc.verificationPolicy.ExpiryInstant(uc.clock.Now())

	// Invalidate any previously active verification tokens for this account
	if err := uc.tokens.InvalidateActiveTokens(ctx, accountID); err != nil {
		return err
	}

	if err := uc.tokens.CreateVerificationToken(ctx, accountID, tokenHash[:], expiresAt); err != nil {
		return err
	}

	return uc.sender.SendVerificationEmail(ctx, email, rawToken)
}
