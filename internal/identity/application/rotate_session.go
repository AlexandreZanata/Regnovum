package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// RotateSessionCommand contains the current raw token and optional updated client context.
type RotateSessionCommand struct {
	CurrentRawToken string
	IPAddress       string
	UserAgent       string
}

// RotateSessionResult contains the newly issued session and raw opaque token.
type RotateSessionResult struct {
	Account  *domain.Account
	Session  *domain.Session
	RawToken string
}

// RotateSessionUseCase rotates an active session: validates current session, revokes it,
// and issues a fresh opaque session for the account.
type RotateSessionUseCase struct {
	accounts      AccountRepository
	sessions      SessionRepository
	clock         Clock
	random        Random
	sessionPolicy domain.SessionPolicy
}

// NewRotateSessionUseCase constructs a RotateSessionUseCase.
func NewRotateSessionUseCase(
	accounts AccountRepository,
	sessions SessionRepository,
	clock Clock,
	random Random,
	policy domain.SessionPolicy,
) *RotateSessionUseCase {
	return &RotateSessionUseCase{
		accounts:      accounts,
		sessions:      sessions,
		clock:         clock,
		random:        random,
		sessionPolicy: policy,
	}
}

// Execute validates and revokes the existing session, then creates a new session.
func (uc *RotateSessionUseCase) Execute(ctx context.Context, cmd RotateSessionCommand) (*RotateSessionResult, error) {
	trimmedToken := strings.TrimSpace(cmd.CurrentRawToken)
	if trimmedToken == "" {
		return nil, ErrInvalidSession
	}

	oldTokenHash := sha256.Sum256([]byte(trimmedToken))
	oldSession, err := uc.sessions.GetSessionByTokenHash(ctx, oldTokenHash[:])
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("lookup current session: %w", err)
	}

	if oldSession.IsRevoked() {
		return nil, ErrSessionRevoked
	}

	now := uc.clock.Now()
	if oldSession.IsExpired(now, uc.sessionPolicy) {
		return nil, ErrSessionExpired
	}

	account, err := uc.accounts.GetAccountByID(ctx, oldSession.AccountID())
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return nil, ErrAccountNotFound
		}
		return nil, fmt.Errorf("lookup account: %w", err)
	}

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

	// Revoke current session
	if err := uc.sessions.RevokeSession(ctx, oldTokenHash[:]); err != nil {
		return nil, fmt.Errorf("revoke current session: %w", err)
	}

	// Inherit client context if not provided
	ip := cmd.IPAddress
	if strings.TrimSpace(ip) == "" {
		ip = oldSession.IPAddress()
	}
	ua := cmd.UserAgent
	if strings.TrimSpace(ua) == "" {
		ua = oldSession.UserAgent()
	}

	// Generate new opaque token
	tokenBytes := make([]byte, 32)
	if _, err := uc.random.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate session token entropy: %w", err)
	}

	newRawToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	newTokenHash := sha256.Sum256([]byte(newRawToken))
	expiresAt := uc.sessionPolicy.ExpiryInstant(now, now)

	newSession, err := uc.sessions.CreateSession(
		ctx,
		account.ID(),
		newTokenHash[:],
		expiresAt,
		ip,
		ua,
	)
	if err != nil {
		return nil, fmt.Errorf("create rotated session: %w", err)
	}

	return &RotateSessionResult{
		Account:  account,
		Session:  newSession,
		RawToken: newRawToken,
	}, nil
}
