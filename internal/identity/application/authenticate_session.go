package application

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// DefaultTouchThreshold is the minimum duration that must elapse before writing an updated last_seen_at
// to persistent storage, preventing write amplification on high-traffic requests.
const DefaultTouchThreshold = 5 * time.Minute

// AuthenticateSessionCommand holds the raw opaque token extracted from cookie or header.
type AuthenticateSessionCommand struct {
	RawToken string
}

// AuthenticateSessionResult contains the authenticated account and active session.
type AuthenticateSessionResult struct {
	Account *domain.Account
	Session *domain.Session
}

// AuthenticateSessionUseCase authenticates an incoming request by opaque session token hash,
// verifies revocation and expiration, enforces account active invariants, and updates last_seen_at
// with write-throttling.
type AuthenticateSessionUseCase struct {
	accounts       AccountRepository
	sessions       SessionRepository
	clock          Clock
	sessionPolicy  domain.SessionPolicy
	touchThreshold time.Duration
}

// NewAuthenticateSessionUseCase constructs an AuthenticateSessionUseCase.
func NewAuthenticateSessionUseCase(
	accounts AccountRepository,
	sessions SessionRepository,
	clock Clock,
	policy domain.SessionPolicy,
	touchThreshold time.Duration,
) *AuthenticateSessionUseCase {
	if touchThreshold <= 0 {
		touchThreshold = DefaultTouchThreshold
	}
	return &AuthenticateSessionUseCase{
		accounts:       accounts,
		sessions:       sessions,
		clock:          clock,
		sessionPolicy:  policy,
		touchThreshold: touchThreshold,
	}
}

// Execute validates the token, verifies status, throttles last_seen updates, and returns the account and session.
func (uc *AuthenticateSessionUseCase) Execute(ctx context.Context, cmd AuthenticateSessionCommand) (*AuthenticateSessionResult, error) {
	trimmedToken := strings.TrimSpace(cmd.RawToken)
	if trimmedToken == "" {
		return nil, ErrInvalidSession
	}

	tokenHash := sha256.Sum256([]byte(trimmedToken))
	session, err := uc.sessions.GetSessionByTokenHash(ctx, tokenHash[:])
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("lookup session: %w", err)
	}

	if session.IsRevoked() {
		return nil, ErrSessionRevoked
	}

	now := uc.clock.Now()
	if session.IsExpired(now, uc.sessionPolicy) {
		return nil, ErrSessionExpired
	}

	account, err := uc.accounts.GetAccountByID(ctx, session.AccountID())
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

	// Throttled update of last_seen_at to avoid writing on every request
	if session.ShouldTouch(now, uc.touchThreshold) {
		session.Touch(now, uc.sessionPolicy)
		_ = uc.sessions.TouchSession(ctx, session.ID(), session.LastSeenAt(), session.ExpiresAt())
	}

	return &AuthenticateSessionResult{
		Account: account,
		Session: session,
	}, nil
}
