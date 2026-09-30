package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// IssueEmailVerificationCommand holds the email address requesting a verification link.
type IssueEmailVerificationCommand struct {
	Email string
}

// IssueEmailVerificationUseCase issues a new email verification token and invalidates active ones.
type IssueEmailVerificationUseCase struct {
	accounts           AccountRepository
	tokens             VerificationTokenRepository
	sender             EmailSender
	clock              Clock
	random             Random
	verificationPolicy domain.VerificationPolicy
}

// NewIssueEmailVerificationUseCase constructs an IssueEmailVerificationUseCase.
func NewIssueEmailVerificationUseCase(
	accounts AccountRepository,
	tokens VerificationTokenRepository,
	sender EmailSender,
	clock Clock,
	random Random,
	policy domain.VerificationPolicy,
) *IssueEmailVerificationUseCase {
	return &IssueEmailVerificationUseCase{
		accounts:           accounts,
		tokens:             tokens,
		sender:             sender,
		clock:              clock,
		random:             random,
		verificationPolicy: policy,
	}
}

// Execute issues a new token for the given account if eligible.
// To prevent user enumeration, non-existent or ineligible accounts return without error.
func (uc *IssueEmailVerificationUseCase) Execute(ctx context.Context, cmd IssueEmailVerificationCommand) error {
	email, err := domain.ParseEmail(cmd.Email)
	if err != nil {
		return err
	}

	account, err := uc.accounts.GetAccountByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			// Anti-enumeration: return nil silently
			return nil
		}
		return err
	}

	if err := uc.verificationPolicy.CanIssueVerification(account); err != nil {
		// If account is already active, suspended, or deleted, do not disclose
		return nil
	}

	tokenBytes := make([]byte, 32)
	if _, err := uc.random.Read(tokenBytes); err != nil {
		return err
	}

	rawToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(rawToken))
	expiresAt := uc.verificationPolicy.ExpiryInstant(uc.clock.Now())

	// Re-issuance invalidates previously active tokens (enforcing single-token policy)
	if err := uc.tokens.InvalidateActiveTokens(ctx, account.ID()); err != nil {
		return err
	}

	if err := uc.tokens.CreateVerificationToken(ctx, account.ID(), tokenHash[:], expiresAt); err != nil {
		return err
	}

	return uc.sender.SendVerificationEmail(ctx, account.Email(), rawToken)
}
