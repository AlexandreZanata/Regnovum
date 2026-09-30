// Package mfamechanism implements the identity module's second-factor
// mechanism port over internal/platform/mfa (P16-T05).
//
// The adapter exists for one reason: the application layer owns the policy of
// the second factor (who may enroll, when a session is elevated, what is
// recorded) and must not import a transport or a cryptographic detail, while
// the mechanism owns the RFC. This is the only place where the two vocabularies
// meet, and translating the mechanism's errors into the module's is most of
// what it does.
package mfamechanism

import (
	"errors"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/mfa"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Mechanism adapts a configuration, a sealer and the entropy source to the
// module's port.
type Mechanism struct {
	config  mfa.Config
	sealer  *mfa.Sealer
	entropy ports.Random
}

var _ application.MFAMechanism = (*Mechanism)(nil)

// DefaultIssuer is the label an authenticator application shows.
const DefaultIssuer = "Regnovum"

// New builds the mechanism. A nil sealer or a missing entropy source fails at
// construction, because a second factor that cannot be stored sealed, or whose
// secret cannot be drawn, must not be enrollable at all.
func New(config mfa.Config, sealer *mfa.Sealer, entropy ports.Random) (*Mechanism, error) {
	if sealer == nil {
		return nil, errors.New("identity: the second factor needs a sealer for its secret")
	}
	if entropy == nil {
		return nil, errors.New("identity: the second factor needs an entropy source")
	}
	return &Mechanism{config: config, sealer: sealer, entropy: entropy}, nil
}

// GenerateSecret implements application.MFAMechanism.
func (mechanism *Mechanism) GenerateSecret() ([]byte, error) {
	return mechanism.config.GenerateSecret(mechanism.entropy)
}

// EncodeSecret implements application.MFAMechanism.
func (mechanism *Mechanism) EncodeSecret(secret []byte) string {
	return mfa.EncodeSecret(secret)
}

// EnrollmentURI implements application.MFAMechanism.
func (mechanism *Mechanism) EnrollmentURI(account string, secret []byte) (string, error) {
	return mechanism.config.URI(DefaultIssuer, account, secret)
}

// VerifyCode implements application.MFAMechanism, translating the mechanism's
// errors into the module's: a replayed step and a wrong code are different
// facts, and the caller reports them differently.
func (mechanism *Mechanism) VerifyCode(secret []byte, code string, now time.Time, lastAcceptedStep int64) (int64, error) {
	step, err := mechanism.config.Verify(secret, code, now, lastAcceptedStep)
	switch {
	case err == nil:
		return step, nil
	case errors.Is(err, mfa.ErrReplayedStep):
		return 0, application.ErrMFACodeReplayed
	case errors.Is(err, mfa.ErrInvalidCode):
		return 0, application.ErrMFACodeInvalid
	case errors.Is(err, mfa.ErrInvalidConfig):
		// A configuration the mechanism refuses is our bug, not the caller's
		// guess: it is reported as an unavailable secret so the endpoint
		// answers "we could not verify" instead of "your code is wrong".
		return 0, application.ErrMFASecretUnavailable
	default:
		return 0, application.ErrMFASecretUnavailable
	}
}

// SealSecret implements application.MFAMechanism.
func (mechanism *Mechanism) SealSecret(accountID string, secret []byte) ([]byte, error) {
	return mechanism.sealer.Seal(secret, []byte(accountID))
}

// OpenSecret implements application.MFAMechanism.
func (mechanism *Mechanism) OpenSecret(accountID string, sealed []byte) ([]byte, error) {
	secret, err := mechanism.sealer.Open(sealed, []byte(accountID))
	if err != nil {
		return nil, application.ErrMFASecretUnavailable
	}
	return secret, nil
}

// GenerateBackupCodes implements application.MFAMechanism.
func (mechanism *Mechanism) GenerateBackupCodes() ([]string, error) {
	return mechanism.config.GenerateBackupCodes(mechanism.entropy)
}

// NormalizeBackupCode implements application.MFAMechanism.
func (mechanism *Mechanism) NormalizeBackupCode(code string) string {
	return mfa.NormalizeBackupCode(code)
}

// ValidBackupCodeShape implements application.MFAMechanism.
func (mechanism *Mechanism) ValidBackupCodeShape(code string) bool {
	return mfa.ValidBackupCodeShape(code)
}
