// Composition of the account JSON API (P49-T01): authentication, second
// factor and sessions over the same pool, security boundary, throttle and
// risk signal as the HTML journey.
//
// Why a second handler and not a second surface: the browser pages and the
// JSON API are two encodings of one account journey. They share the
// repository, the hasher, the mail sender, the throttle and the CSRF cookie
// of the origin — a person moving between a page and a JSON call must not be
// refused by a second manager that cannot verify the other's token. The use
// cases are fresh instances over the same dependencies, never reimplemented
// rules; the policies come from the identity domain as everywhere else.
//
// MFA seal debt (P49-T01): the seal key is derived from the cursor secret
// when one is configured and ephemeral otherwise. The delivered server does
// not pass the cursor secret to this surface yet, so the seal is ephemeral
// there: a restart invalidates pending enrollments. Wiring a stable key is
// future work that needs a dedicated secret, not a reuse.
package bootstrap

import (
	"crypto/sha256"
	"fmt"
	"log/slog"

	auditpostgres "github.com/AlexandreZanata/Regnovum/internal/audit/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/auditbridge"
	identityhttp "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/mfamechanism"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/mfa"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

// mfaSealContext separates the MFA seal subkey from the cursor signing use.
const mfaSealContext = "regnovum-mfa-seal-v1"

// accountJSONDeps are the shared edges the JSON handler reuses from the
// pages. One struct keeps the composer under the parameter budget.
type accountJSONDeps struct {
	repository *identitypostgres.Repository
	hasher     identityapp.PasswordHasher
	emails     identityapp.EmailSender
	manager    *security.Manager
	throttle   ratelimit.Protector
	risk       turnstile.Challenger
}

// mfaSealKey resolves the 32-byte seal key: stable from the cursor secret,
// ephemeral otherwise (logged by the caller).
func mfaSealKey(options Options) ([]byte, bool) {
	if len(options.CursorSecret) >= mfa.KeySize {
		sum := sha256.Sum256(append([]byte(mfaSealContext), options.CursorSecret...))
		key := make([]byte, mfa.KeySize)
		copy(key, sum[:])
		return key, true
	}
	key := make([]byte, mfa.KeySize)
	_, _ = options.Random.Read(key)
	return key, false
}

// composeMFAMechanism builds the second-factor mechanism.
func composeMFAMechanism(options Options) (*mfamechanism.Mechanism, error) {
	key, stable := mfaSealKey(options)
	if !stable {
		options.Logger.Warn("account journey: MFA seal is ephemeral; a restart invalidates pending enrollments",
			slog.String("env", string(options.Env)))
	}
	sealer, err := mfa.NewSealer(key, options.Random)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account JSON API: MFA sealer: %w", err)
	}
	mechanism, err := mfamechanism.New(mfa.Config{}, sealer, options.Random)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account JSON API: MFA mechanism: %w", err)
	}
	return mechanism, nil
}

// composeMFAAudit bridges the second-factor facts to the audit trail.
func composeMFAAudit(options Options) (identityapp.MFAAudit, error) {
	bridge, err := auditbridge.NewRecorder(auditpostgres.NewRepository(options.Pool), options.Clock)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account JSON API: MFA audit: %w", err)
	}
	return bridge, nil
}

// composeAccountJSONHandler builds the JSON handler over the shared edges.
func composeAccountJSONHandler(options Options, deps accountJSONDeps) (*identityhttp.Handler, error) {
	policy := identitydomain.DefaultSessionPolicy()
	mechanism, err := composeMFAMechanism(options)
	if err != nil {
		return nil, err
	}
	audit, err := composeMFAAudit(options)
	if err != nil {
		return nil, err
	}
	repository := deps.repository
	return identityhttp.NewHandler(identityhttp.HandlerConfig{
		RegisterUseCase: identityapp.NewRegisterAccountUseCase(
			repository, repository, deps.hasher, deps.emails, options.Clock, options.Random, identitydomain.DefaultVerificationPolicy(),
		).WithKeyRepository(repository),
		VerifyEmailUseCase: identityapp.NewVerifyEmailUseCase(repository, repository, options.Clock),
		LoginUseCase: identityapp.NewLoginUseCase(
			repository, repository, repository, deps.hasher, options.Clock, options.Random, policy,
		).WithKeyRepository(repository),
		LogoutUseCase: identityapp.NewLogoutUseCase(repository),
		RequestPasswordResetUseCase: identityapp.NewRequestPasswordResetUseCase(
			repository, repository, deps.emails, options.Clock, options.Random, identitydomain.DefaultPasswordResetPolicy(),
		),
		CompletePasswordResetUseCase: identityapp.NewCompletePasswordResetUseCase(
			repository, repository, repository, repository, repository, deps.hasher, deps.emails, options.Clock,
		),
		AuthenticateSessionUseCase:  identityapp.NewAuthenticateSessionUseCase(repository, repository, options.Clock, policy, 0),
		SecurityManager:             deps.manager,
		RateLimit:                   deps.throttle,
		Challenge:                   deps.risk,
		BeginMFAEnrollmentUseCase:   identityapp.NewBeginMFAEnrollmentUseCase(repository, mechanism),
		ConfirmMFAEnrollmentUseCase: identityapp.NewConfirmMFAEnrollmentUseCase(repository, mechanism, deps.hasher, options.Clock, audit),
		StepUpMFAUseCase:            identityapp.NewStepUpMFAUseCase(repository, mechanism, options.Clock),
		RecoverMFAUseCase:           identityapp.NewRecoverMFAUseCase(repository, mechanism, deps.hasher, options.Clock, audit),
		ListSessionsUseCase:         identityapp.NewListSessionsUseCase(repository, options.Clock, policy),
		RevokeSessionUseCase:        identityapp.NewRevokeSessionUseCase(repository, repository, deps.hasher),
		RotateSessionUseCase:        identityapp.NewRotateSessionUseCase(repository, repository, options.Clock, options.Random, policy),
	}), nil
}
