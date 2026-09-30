package application

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// LogoutCommand contains the session token to terminate.
type LogoutCommand struct {
	RawToken string
}

// LogoutUseCase terminates a single authenticated session.
type LogoutUseCase struct {
	sessions SessionRepository
}

// NewLogoutUseCase constructs a LogoutUseCase.
func NewLogoutUseCase(sessions SessionRepository) *LogoutUseCase {
	return &LogoutUseCase{sessions: sessions}
}

// Execute revokes the session associated with the provided raw token.
// Revoking an already revoked or non-existent token succeeds idempotently.
func (uc *LogoutUseCase) Execute(ctx context.Context, cmd LogoutCommand) error {
	trimmedToken := strings.TrimSpace(cmd.RawToken)
	if trimmedToken == "" {
		return nil
	}

	tokenHash := sha256.Sum256([]byte(trimmedToken))
	if err := uc.sessions.RevokeSession(ctx, tokenHash[:]); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// LogoutAllCommand contains the account identifier whose sessions should all be terminated.
type LogoutAllCommand struct {
	AccountID domain.AccountID
}

// LogoutAllUseCase terminates all active sessions belonging to an account.
type LogoutAllUseCase struct {
	sessions SessionRepository
}

// NewLogoutAllUseCase constructs a LogoutAllUseCase.
func NewLogoutAllUseCase(sessions SessionRepository) *LogoutAllUseCase {
	return &LogoutAllUseCase{sessions: sessions}
}

// Execute revokes all sessions belonging to the account.
func (uc *LogoutAllUseCase) Execute(ctx context.Context, cmd LogoutAllCommand) error {
	if cmd.AccountID.IsZero() {
		return domain.ErrEmptyAccountID
	}

	if err := uc.sessions.RevokeAllAccountSessions(ctx, cmd.AccountID); err != nil {
		return fmt.Errorf("revoke all account sessions: %w", err)
	}
	return nil
}
