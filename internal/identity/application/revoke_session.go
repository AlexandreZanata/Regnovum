package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// RevokeSessionCommand ends one session of the authenticated account.
type RevokeSessionCommand struct {
	AccountID domain.AccountID
	// SessionID is the session to end.
	SessionID domain.SessionID
	// CurrentSessionID is the session of the caller, which this route never
	// ends: ending it is logging out, and it has its own route.
	CurrentSessionID domain.SessionID
	// Password re-authenticates the owner. A session cookie alone is not
	// enough to end another session — that is the whole point of the check.
	Password string
}

// RevokeSessionUseCase ends another session of the calling account after
// re-authenticating the owner (P16-T06).
//
// Why re-authenticate at all: ending sessions is the operation an attacker with
// a stolen cookie wants most, because it evicts the owner and hides the theft,
// and it is also the operation that turns a momentary cookie theft into a
// permanent one. Requiring the password means a stolen session cannot evict the
// sessions of the person it was stolen from, which is exactly the case the
// device list exists for.
//
// The order is the design: the password is verified *before* the session is
// looked at, so a wrong password and an unknown session identifier produce one
// answer, and the endpoint cannot be asked which session identifiers exist. A
// failed re-authentication changes nothing at all.
type RevokeSessionUseCase struct {
	sessions    SessionRepository
	credentials PasswordCredentialRepository
	hasher      PasswordHasher
}

// NewRevokeSessionUseCase constructs a RevokeSessionUseCase.
func NewRevokeSessionUseCase(
	sessions SessionRepository,
	credentials PasswordCredentialRepository,
	hasher PasswordHasher,
) *RevokeSessionUseCase {
	return &RevokeSessionUseCase{sessions: sessions, credentials: credentials, hasher: hasher}
}

// Execute re-authenticates the owner and ends the addressed session.
func (uc *RevokeSessionUseCase) Execute(ctx context.Context, cmd RevokeSessionCommand) error {
	if cmd.AccountID.IsZero() {
		return domain.ErrEmptyAccountID
	}
	if cmd.SessionID.IsZero() {
		return ErrSessionNotFound
	}
	if cmd.SessionID == cmd.CurrentSessionID {
		return ErrCannotRevokeCurrentSession
	}
	if err := uc.reauthenticate(ctx, cmd.AccountID, cmd.Password); err != nil {
		return err
	}

	revoked, err := uc.sessions.RevokeSessionByID(ctx, cmd.AccountID, cmd.SessionID)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if !revoked {
		// Unknown, another account's, or already ended: one answer, so the
		// endpoint is not a probe for session identifiers.
		return ErrSessionNotFound
	}
	return nil
}

// reauthenticate verifies the account's password. Every failure that is not an
// infrastructure failure is one refusal: a missing credential is not an
// invitation to skip the check, it is a reason to refuse (fail closed).
func (uc *RevokeSessionUseCase) reauthenticate(ctx context.Context, accountID domain.AccountID, password string) error {
	if uc.credentials == nil || uc.hasher == nil {
		return ErrReauthUnavailable
	}
	trimmed := strings.TrimSpace(password)
	if trimmed == "" {
		return ErrReauthFailed
	}

	credential, err := uc.credentials.GetPasswordCredential(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrCredentialNotFound) {
			return ErrReauthFailed
		}
		return fmt.Errorf("load credential for reauthentication: %w", err)
	}
	verified, err := uc.hasher.VerifyPassword(trimmed, credential.PasswordHash)
	if err != nil {
		return fmt.Errorf("verify reauthentication: %w", err)
	}
	if !verified {
		return ErrReauthFailed
	}
	return nil
}
