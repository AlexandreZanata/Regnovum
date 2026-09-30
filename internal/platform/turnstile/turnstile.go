// Package turnstile verifies anti-bot challenges server-side (P16-T04).
//
// The layer exists because the rate limiter of P16-T03 bounds how often one
// caller may repeat an action, and that is still not an answer to a caller who
// is not repeating anything: one address can create its first account once,
// request its first reset once, and publish its first Arena once, and a
// thousand addresses can do it a thousand times. A challenge in front of those
// actions makes the caller pay a price the script does not want to pay, and the
// price is decided by the anti-bot provider rather than by a heuristic here.
//
// The design is the same shape as the two security layers before it — a
// policy table, one bounded mechanism, and a port the adapters depend on:
//
//   - an Action names the challenged operation and the table says when a
//     challenge is required. The table is the policy, in one place, next to
//     the reason, so a requirement is reviewed rather than discovered inside a
//     handler;
//   - verification happens on the server, always: the browser solves a
//     challenge and receives a token, and only the server can decide whether
//     the token is real, because only the server holds the secret. The secret
//     never reaches the browser, and this package exists partly to keep that
//     true (a test drives every guarded route and fails if a response ever
//     carries the secret);
//   - a token is single-use: the caller that redeems it spends it. Where the
//     provider also enforces single use, its refusal (Cloudflare's
//     `timeout-or-duplicate`) is reported as the same replay, so the two
//     answers agree;
//   - an action can require a challenge **always** — creating an account,
//     sending a reset mail, publishing an Arena — or **only under elevated
//     risk**, which is how consecutive authentication failures are handled
//     (THR-AUTH-02): a person who mistypes a password never sees a challenge,
//     a script guessing passwords meets one;
//   - the fail policy is explicit and configured rather than accidental. The
//     default is closed: a challenge that cannot be verified refuses the
//     action, because a challenge that is skipped exactly when the provider is
//     unreachable is not a challenge. The open policy exists for an operator
//     who prefers availability of account creation during a provider outage,
//     it must be asked for by name, and it is documented as the trade it is
//     (docs/SECURITY.md);
//   - what happens locally is explicit too. With no secret configured in
//     development or test the composition installs a documented local fake
//     that accepts only the tokens Cloudflare's always-passing test widget
//     produces and never touches the network; with no secret in production it
//     refuses to build at all, so a misconfigured deployment fails at boot
//     instead of running unprotected.
//
// What this package deliberately does not do: it never stores a challenge
// token (only a fingerprint of one that was redeemed, for the lifetime the
// token could have lived), it never logs a token or an address, it never
// treats a solved challenge as authorization (it is a precondition, not an
// identity), and it does not render the widget — the browser side, including
// the public site key and the content security policy the widget will need,
// belongs to the frontend task.
package turnstile

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
)

// Action is one challenged operation of the product. Actions are stable names:
// they appear in the policy table and in the provider request, and they are
// verified against the provider's answer, so a token minted for one action
// cannot be spent on another.
type Action string

// The declared actions. Each one creates something a script would like to
// create at volume: an account, an outbound email, a published Arena.
const (
	// ActionSignup creates an account: an Argon2id hash, a verification token
	// and an email, plus any promotional INK the new account is granted
	// (THR-WAL-03).
	ActionSignup Action = "signup"
	// ActionPasswordReset makes the product send mail to an address the
	// caller claims to own.
	ActionPasswordReset Action = "password_reset"
	// ActionArenaPublish publishes an Arena, which spends INK and creates
	// public content.
	ActionArenaPublish Action = "arena_publish"
	// ActionLoginElevated guards password guessing that has already failed
	// repeatedly (THR-AUTH-02). The token's action is named separately from
	// the others because the widget that mints it is the elevated-risk one,
	// and a token minted for a signup must not be spendable on a login.
	ActionLoginElevated Action = "login_elevated"
)

// Requirement says when an action must present a challenge.
type Requirement string

const (
	// Required means every call must carry a fresh challenge.
	Required Requirement = "required"
	// RequiredWhenElevated means the challenge is demanded only when the risk
	// signal says the caller is under elevated risk.
	RequiredWhenElevated Requirement = "required_when_elevated"
)

// requirements is the whole policy, in one readable place. The reason next to
// each row is the reason it is challenged: what a script would gain by
// repeating it, and what the challenge costs it.
var requirements = map[Action]Requirement{
	// Creating accounts is what a Sybil farm repeats: the challenge is on
	// every call, because the first account of an address is already valuable
	// to a farm that has many addresses.
	ActionSignup: Required,
	// A reset request sends mail to an address the caller may not own; the
	// challenge keeps the product from being someone else's mail relay and a
	// cheap existence oracle.
	ActionPasswordReset: Required,
	// Publishing creates public content and spends INK, so the challenge is
	// on every call rather than only on suspicion.
	ActionArenaPublish: Required,
	// A login is challenged only once the caller has already failed
	// repeatedly: a person who mistypes a password pays nothing, and a
	// password-guessing loop pays with a challenge it cannot solve at scale.
	ActionLoginElevated: RequiredWhenElevated,
}

// Actions lists every declared action, so completeness is testable and so
// documentation can enumerate the policy without reading the map.
func Actions() []Action {
	actions := make([]Action, 0, len(requirements))
	for action := range requirements {
		actions = append(actions, action)
	}
	return actions
}

// RequirementFor resolves the requirement of an action. The second result
// reports whether the table declares the action.
//
// An undeclared action is *required* rather than free, which is the
// conservative reading of a gap: the alternative would make a forgotten row
// mean "unprotected", and the cost of being wrong in this direction is a
// challenge on a route that did not need one. A test asserts that the table
// covers every declared action, so the fallback is a safety net rather than a
// path in use.
func RequirementFor(action Action) (Requirement, bool) {
	requirement, declared := requirements[action]
	if !declared {
		return Required, false
	}
	return requirement, true
}

// The stable problem codes of a refusal. Codes are the contract clients branch
// on (I18N_STANDARD section 5): titles and details may be localized, the code
// may not.
const (
	// CodeChallengeRequired means the request carried no challenge token at
	// all. Like the CSRF header check, it is a 403: the caller did not prove
	// something it was required to prove.
	CodeChallengeRequired = "challenge_required"
	// CodeChallengeInvalid means the provider rejected the token.
	CodeChallengeInvalid = "challenge_invalid"
	// CodeChallengeReplayed means the token had already been redeemed here, or
	// the provider reported it as already used or expired (`timeout-or-duplicate`,
	// which the provider does not separate).
	CodeChallengeReplayed = "challenge_replayed"
	// CodeChallengeActionMismatch means the token was minted for another
	// action.
	CodeChallengeActionMismatch = "challenge_action_mismatch"
	// CodeChallengeHostnameMismatch means the token was solved for another
	// hostname.
	CodeChallengeHostnameMismatch = "challenge_hostname_mismatch"
	// CodeChallengeUnavailable means the challenge could not be checked at
	// all: the provider timed out or failed. The configured fail policy
	// decides what happens; when the policy is closed the answer is an
	// internal error with this code, because telling the caller it was
	// refused would be a lie about who failed.
	CodeChallengeUnavailable = "challenge_unavailable"
	// CodeChallengeMisconfigured means our own configuration is wrong — the
	// provider rejected the secret, or answered that the request was
	// malformed. It is kept apart from CodeChallengeUnavailable on purpose:
	// an outage is transient and the operator may choose to serve through it,
	// while a wrong secret is a broken deployment that must not be served
	// through at all.
	CodeChallengeMisconfigured = "challenge_misconfigured"
)

// FailPolicy is what happens to an action whose challenge cannot be verified.
type FailPolicy string

const (
	// FailClosed refuses the action when the challenge cannot be checked. It
	// is the default, and it is the default because the moment the provider is
	// unreachable is exactly the moment an automated client would like to
	// proceed.
	FailClosed FailPolicy = "closed"
	// FailOpen lets the action through when the challenge cannot be checked.
	// It is a deliberate availability trade: account creation keeps working
	// during a provider outage, and the action runs unprotected for as long
	// as the outage lasts. It must be asked for by name; it is never inferred.
	FailOpen FailPolicy = "open"
)

// failsOpen reports whether an unverifiable challenge lets the action through.
// Any value other than the explicit open policy behaves closed, so a zero
// Config is the safe one.
func (policy FailPolicy) failsOpen() bool { return policy == FailOpen }

// verified reports whether the policy is one this package ships. An
// unrecognized value is refused at construction instead of being silently read
// as its zero value.
func (policy FailPolicy) verified() bool {
	return policy == "" || policy == FailClosed || policy == FailOpen
}

// Defaults of the verification. They are deliberately conservative: a
// verification call is on the critical path of a user action, so the timeout
// has to be short enough that a hanging provider cannot hold a request for
// long, and long enough that ordinary internet latency never fails a real
// person.
const (
	// DefaultTimeout bounds the outbound verification call.
	DefaultTimeout = 5 * time.Second
	// DefaultEndpoint is Cloudflare's verification endpoint.
	DefaultEndpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	// MaxTokenBytes bounds an accepted token. Cloudflare's tokens are well
	// under this; the bound exists so that a caller cannot make the server
	// carry an arbitrary amount of data into an outbound request.
	MaxTokenBytes = 2048
	// maxResponseBytes bounds the provider's answer, so a provider (or
	// something in front of it) cannot make the process read without limit.
	maxResponseBytes = 16 << 10
	// DefaultTokenLifetime is how long a redeemed token is remembered: the
	// lifetime Cloudflare gives its own tokens, so a replay is refused here
	// for exactly as long as the provider would have refused it too.
	DefaultTokenLifetime = 5 * time.Minute
	// DefaultRedeemedCapacity bounds the replay memory, in tokens. A busy
	// minute of challenged actions fits far below it.
	DefaultRedeemedCapacity = 4096
)

// Config is the verification configuration.
//
// It is a value rather than a bag of environment reads because the
// environment is read in one place (internal/platform/config, enforced by the
// architecture test); this package is handed the result.
type Config struct {
	// SecretKey is the server-side secret of the challenge widget. It is
	// never printed, never logged and never rendered: String and GoString
	// redact it, so a Config can be logged safely.
	SecretKey string
	// Hostname is the hostname a challenge must have been solved for. The
	// provider's answer is checked against it, which is what stops a token
	// minted for another site from being spent here.
	Hostname string
	// Timeout bounds the outbound verification call. Zero means
	// DefaultTimeout.
	Timeout time.Duration
	// FailPolicy decides what happens when the challenge cannot be verified.
	// Empty means FailClosed.
	FailPolicy FailPolicy
	// Endpoint overrides the verification endpoint. Zero value means
	// DefaultEndpoint; it exists so tests can point the verifier at a local
	// server without a network.
	Endpoint string
	// Client overrides the outbound HTTP client. Nil builds one with the
	// configured timeout.
	Client *http.Client
	// TokenLifetime is how long a redeemed token is remembered. Zero means
	// DefaultTokenLifetime.
	TokenLifetime time.Duration
	// RedeemedCapacity is how many redeemed tokens are remembered at most.
	// Zero means DefaultRedeemedCapacity.
	RedeemedCapacity int
	// Now is the clock. Nil means the system clock, read through the package
	// that owns that effect; a test injects its own source instead, so that the
	// single-use window of a challenge is decided against an instant the test
	// chose rather than against the machine it runs on.
	Now func() time.Time
}

// String implements fmt.Stringer with the secret redacted, so a Config can be
// logged.
func (config Config) String() string {
	return fmt.Sprintf(
		"turnstile{hostname:%s timeout:%s fail_policy:%s secret_key:%s}",
		config.Hostname, config.timeout(), config.policy(), config.secret(),
	)
}

// GoString implements fmt.GoStringer with the secret redacted, which closes
// the %#v leak path.
func (config Config) GoString() string { return config.String() }

// secret renders the configured secret as a presence flag, never as a value.
func (config Config) secret() string {
	if config.SecretKey == "" {
		return "[UNSET]"
	}
	return "[REDACTED]"
}

func (config Config) timeout() time.Duration {
	if config.Timeout <= 0 {
		return DefaultTimeout
	}
	return config.Timeout
}

func (config Config) endpoint() string {
	if config.Endpoint == "" {
		return DefaultEndpoint
	}
	return config.Endpoint
}

func (config Config) policy() FailPolicy {
	if config.FailPolicy == "" {
		return FailClosed
	}
	return config.FailPolicy
}

func (config Config) tokenLifetime() time.Duration {
	if config.TokenLifetime <= 0 {
		return DefaultTokenLifetime
	}
	return config.TokenLifetime
}

func (config Config) redeemedCapacity() int {
	if config.RedeemedCapacity <= 0 {
		return DefaultRedeemedCapacity
	}
	return config.RedeemedCapacity
}

// Configuration errors. They are returned by New, which runs at boot: a
// deployment that cannot verify a challenge should not start serving the
// actions that require one.
var (
	// ErrMissingSecretKey is returned when production has no secret: the
	// alternative would be to run every challenged action unprotected.
	ErrMissingSecretKey = errors.New("turnstile: ARENA_TURNSTILE_SECRET_KEY is required in production")
	// ErrMissingHostname is returned when a secret is configured without the
	// hostname its tokens must belong to, which would accept a token minted
	// for another site.
	ErrMissingHostname = errors.New("turnstile: ARENA_TURNSTILE_HOSTNAME is required when a secret key is configured")
	// ErrUnknownFailPolicy is returned for a fail policy this package does not
	// implement, rather than silently treating it as closed.
	ErrUnknownFailPolicy = errors.New("turnstile: unknown fail policy (allowed: closed, open)")
)

// New builds the verifier of an environment.
//
// The three outcomes are the three configurations that exist:
//
//   - a secret is configured: the real verifier, whatever the environment, so
//     that a developer can exercise the actual provider with its test keys;
//   - no secret in development or test: the documented local fake, which never
//     touches the network and accepts only the tokens Cloudflare's
//     always-passing test widget produces. There is no password to send
//     anywhere, so there is nothing to verify against;
//   - no secret in production: an error, because the only alternatives would be
//     a fake in production or an unprotected production.
//
// It is the composition root's job to call this and to fail the boot on the
// error; the package cannot enforce that from here, which is why the
// configuration errors above are exported and named after their variables.
func New(configuration Config, env config.Env) (Verifier, error) {
	if !configuration.FailPolicy.verified() {
		return nil, fmt.Errorf("%w: %q", ErrUnknownFailPolicy, configuration.FailPolicy)
	}

	if configuration.SecretKey == "" {
		if env == config.EnvProduction {
			return nil, ErrMissingSecretKey
		}
		return NewLocalFake(), nil
	}

	if configuration.Hostname == "" {
		return nil, ErrMissingHostname
	}

	return newSiteverify(configuration), nil
}
