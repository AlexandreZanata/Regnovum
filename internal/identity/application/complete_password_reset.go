package application

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// CompletePasswordResetCommand holds the parameters required to reset an account's password.
type CompletePasswordResetCommand struct {
	Token       string
	NewPassword string
}

// CompletePasswordResetUseCase validates the single-use recovery token, updates the password credential,
// consumes the token, invalidates remaining active tokens, revokes all active sessions, and notifies the
// owner that the change happened.
type CompletePasswordResetUseCase struct {
	accounts           AccountRepository
	credentials        PasswordCredentialRepository
	resetTokens        PasswordResetTokenRepository
	verificationTokens VerificationTokenRepository
	sessions           SessionRepository
	hasher             PasswordHasher
	emails             EmailSender
	clock              Clock
}

// NewCompletePasswordResetUseCase constructs a CompletePasswordResetUseCase.
func NewCompletePasswordResetUseCase(
	accounts AccountRepository,
	credentials PasswordCredentialRepository,
	resetTokens PasswordResetTokenRepository,
	verificationTokens VerificationTokenRepository,
	sessions SessionRepository,
	hasher PasswordHasher,
	emails EmailSender,
	clock Clock,
) *CompletePasswordResetUseCase {
	return &CompletePasswordResetUseCase{
		accounts:           accounts,
		credentials:        credentials,
		resetTokens:        resetTokens,
		verificationTokens: verificationTokens,
		sessions:           sessions,
		hasher:             hasher,
		emails:             emails,
		clock:              clock,
	}
}

// Execute validates the token, applies the new password, and invalidates all existing sessions and tokens.
func (uc *CompletePasswordResetUseCase) Execute(ctx context.Context, cmd CompletePasswordResetCommand) error {
	if len(cmd.NewPassword) < 8 {
		return ErrWeakPassword
	}

	trimmedToken := strings.TrimSpace(cmd.Token)
	if trimmedToken == "" {
		return ErrInvalidToken
	}

	tokenHash := sha256.Sum256([]byte(trimmedToken))
	record, err := uc.resetTokens.GetPasswordResetToken(ctx, tokenHash[:])
	if err != nil {
		return err
	}

	now := uc.clock.Now()

	// Replay protection: token has already been consumed or superseded
	if record.UsedAt != nil {
		return ErrTokenAlreadyUsed
	}

	// Expiration check: strictly within configured lifetime (15 minutes per THR-AUTH-04)
	if now.After(record.ExpiresAt) {
		return ErrTokenExpired
	}

	account, err := uc.accounts.GetAccountByID(ctx, record.AccountID)
	if err != nil {
		return fmt.Errorf("lookup account: %w", err)
	}

	// Account lifecycle validation: suspended or deleted accounts cannot reset password
	switch account.Status() {
	case domain.AccountStatusSuspended:
		return domain.ErrAccountSuspended
	case domain.AccountStatusDeleted:
		return domain.ErrAccountDeleted
	}

	// Derive secure Argon2id hash for the new password
	newHash, err := uc.hasher.HashPassword(cmd.NewPassword)
	if err != nil {
		return fmt.Errorf("hash new password: %w", err)
	}

	// Update password credential
	if err := uc.credentials.UpdatePasswordCredential(ctx, account.ID(), newHash, "argon2id", 1); err != nil {
		return fmt.Errorf("update credential: %w", err)
	}

	// Consume the token single-use
	if err := uc.resetTokens.MarkPasswordResetTokenUsed(ctx, record.ID, now); err != nil {
		return fmt.Errorf("mark reset token used: %w", err)
	}

	// Invalidate any other active reset tokens for this account
	if err := uc.resetTokens.InvalidateActivePasswordResetTokens(ctx, account.ID()); err != nil {
		return fmt.Errorf("invalidate active reset tokens: %w", err)
	}

	// Revoke all active sessions for this account per THR-AUTH-01
	if err := uc.sessions.RevokeAllAccountSessions(ctx, account.ID()); err != nil {
		return fmt.Errorf("revoke account sessions: %w", err)
	}

	// Invalidate any active email verification tokens for this account
	if uc.verificationTokens != nil {
		if err := uc.verificationTokens.InvalidateActiveTokens(ctx, account.ID()); err != nil {
			return fmt.Errorf("invalidate active verification tokens: %w", err)
		}
	}

	// The owner is told, and the notice is queued inside the same transaction
	// as the change it announces (P16-T06): the message claims the password
	// changed and every older session ended, which is only true once those
	// writes are the ones that commit, and a queue that cannot accept the job
	// fails the reset instead of leaving an undetected change behind. The
	// identifier of the consumed token anchors the event, so a retry of this
	// reset resolves the notice it already queued while the next reset of the
	// same account is a new message.
	if uc.emails == nil {
		return ErrMissingEmailSender
	}
	if err := uc.emails.SendPasswordChangedEmail(ctx, account.Email(), record.ID); err != nil {
		return fmt.Errorf("notify password change: %w", err)
	}

	return nil
}
