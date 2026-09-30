package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// RequestPasswordResetCommand holds the email address submitted for password recovery.
type RequestPasswordResetCommand struct {
	Email string
}

// RequestPasswordResetUseCase coordinates secure, anti-enumeration password recovery token issuance.
type RequestPasswordResetUseCase struct {
	accounts    AccountRepository
	resetTokens PasswordResetTokenRepository
	sender      EmailSender
	clock       Clock
	random      Random
	resetPolicy domain.PasswordResetPolicy
}

// NewRequestPasswordResetUseCase constructs a RequestPasswordResetUseCase.
func NewRequestPasswordResetUseCase(
	accounts AccountRepository,
	resetTokens PasswordResetTokenRepository,
	sender EmailSender,
	clock Clock,
	random Random,
	policy domain.PasswordResetPolicy,
) *RequestPasswordResetUseCase {
	return &RequestPasswordResetUseCase{
		accounts:    accounts,
		resetTokens: resetTokens,
		sender:      sender,
		clock:       clock,
		random:      random,
		resetPolicy: policy,
	}
}

// Execute initiates a password recovery flow.
// To prevent user enumeration attacks (THR-AUTH-02), this method returns nil without error
// regardless of whether the email is registered, unregistered, malformed, or in an ineligible state
// (e.g. suspended or deleted).
func (uc *RequestPasswordResetUseCase) Execute(ctx context.Context, cmd RequestPasswordResetCommand) error {
	email, err := domain.ParseEmail(cmd.Email)
	if err != nil {
		// Return success to avoid leaking email format/domain validity rules
		return nil
	}

	account, err := uc.accounts.GetAccountByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return nil
		}
		return fmt.Errorf("lookup account: %w", err)
	}

	// Validate eligibility without disclosing account status
	if err := uc.resetPolicy.CanIssueReset(account); err != nil {
		// Suspended or deleted accounts do not receive reset tokens, but we suppress the error
		return nil
	}

	now := uc.clock.Now()

	// Invalidate any previously active reset tokens for this account per THR-AUTH-04
	if err := uc.resetTokens.InvalidateActivePasswordResetTokens(ctx, account.ID()); err != nil {
		return fmt.Errorf("invalidate active reset tokens: %w", err)
	}

	// Generate 32 bytes (256 bits) of CSPRNG entropy
	tokenBytes := make([]byte, 32)
	if _, err := uc.random.Read(tokenBytes); err != nil {
		return fmt.Errorf("generate reset token entropy: %w", err)
	}

	rawToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(rawToken))
	expiresAt := uc.resetPolicy.ExpiryInstant(now)

	if err := uc.resetTokens.CreatePasswordResetToken(ctx, account.ID(), tokenHash[:], expiresAt); err != nil {
		return fmt.Errorf("persist reset token: %w", err)
	}

	if err := uc.sender.SendPasswordResetEmail(ctx, email, rawToken); err != nil {
		return fmt.Errorf("send reset email: %w", err)
	}

	return nil
}
