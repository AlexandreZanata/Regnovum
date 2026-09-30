package turnstile

import (
	"errors"
	"mime"
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
)

// DefaultChallengeHeaderName is where a JSON client carries the token. It is
// the widget's own field name spelled as a header, so the browser side has one
// name to remember, and it sits next to the CSRF header the product already
// requires on mutating requests.
const DefaultChallengeHeaderName = "CF-Turnstile-Response"

// DefaultChallengeFormField is where a plain HTML form carries the token — the
// widget's own field name, so a form that degrades without JavaScript still
// works and needs no custom script.
const DefaultChallengeFormField = "cf-turnstile-response"

// Challenger is the port the inbound adapters depend on.
//
// The adapters depend on this and not on the Verifier because the two jobs are
// different: this one decides *when* a challenge is required (the policy
// table, the risk signal) and *what a refusal looks like* (RFC 9457 with a
// stable code), while the Verifier only answers whether one token is good. A
// test can therefore supply a fake challenger, and a module test can supply a
// fake verifier without losing the real policy.
type Challenger interface {
	// Challenge wraps a handler with the requirement of the action.
	Challenge(action Action, next http.Handler) http.Handler
	// Observe records the outcome of an action, so a risk-gated action can
	// tell a caller who has been failing from one who has not.
	Observe(request *http.Request, failed bool)
}

// Enforcer implements Challenger over a Verifier, a risk signal and a resolver.
type Enforcer struct {
	verifier Verifier
	tracker  *FailureTracker
	resolver clientip.Resolver
	policy   FailPolicy
}

// NewEnforcer builds the enforcer of an environment.
//
// A nil verifier disables the protection, which is what a composition that has
// not installed one gets (the same contract as the rate limiter, and for the
// same reason: the alternative is a handler that cannot be constructed at all
// before the composition is written). Production installs one; the composition
// root is where that is visible.
//
// A nil tracker is *not* a way to disable the risk signal: a risk-gated action
// with no signal challenges every caller, which keeps the action working and
// the protection in place. The safe reading of a missing signal is elevation.
func NewEnforcer(configuration Config, verifier Verifier, tracker *FailureTracker, resolver clientip.Resolver) *Enforcer {
	return &Enforcer{
		verifier: verifier,
		tracker:  tracker,
		resolver: resolver,
		policy:   configuration.policy(),
	}
}

// Challenge implements Challenger.
//
// The wrapper runs before the handler decodes anything, so a request without a
// valid challenge costs one lookup and never reaches a use case. A request
// that carries no token is refused without consulting the verifier. The placement
// is the adapter's responsibility and carries meaning, exactly as it does for
// the rate limiter: inside the authentication middleware (so a challenged
// publication is an authenticated publication), inside the private cache
// middleware (so a refusal is still `private, no-store`) and inside the rate
// limiter (so a caller who is already over its budget is not also made to
// spend a challenge).
func (enforcer *Enforcer) Challenge(action Action, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if enforcer == nil || enforcer.verifier == nil {
			next.ServeHTTP(writer, request)
			return
		}

		requirement, _ := RequirementFor(action)
		if requirement == RequiredWhenElevated && !enforcer.elevated(request) {
			next.ServeHTTP(writer, request)
			return
		}

		// A missing token is a precondition, not a verification: it is
		// refused here rather than asked of the verifier, so the answer is the
		// same whether the provider is reachable or not. That is what keeps the
		// open fail policy from turning "no token at all" into "served
		// through": the policy is about an answer we did not get, never about
		// an input we never had.
		token := tokenOf(request)
		if token == "" {
			WriteRefusal(writer, request, refusal(apperr.KindForbidden, CodeChallengeRequired, "a challenge token is required for this action"))
			return
		}

		err := enforcer.verifier.Verify(request.Context(), Verification{
			Token:  token,
			Action: action,
			Client: enforcer.client(request),
		})
		if err == nil {
			next.ServeHTTP(writer, request)
			return
		}

		// The one refusal the fail policy is about: nobody told us whether
		// the token is good. It is the operator's trade to make, and it is
		// made only for this answer — a replayed token or a token minted for
		// another site is refused whatever the policy says.
		if IsUnavailable(err) && enforcer.policy.failsOpen() {
			next.ServeHTTP(writer, request)
			return
		}

		WriteRefusal(writer, request, err)
	})
}

// Observe implements Challenger: it records the outcome of one action against
// the caller's address.
func (enforcer *Enforcer) Observe(request *http.Request, failed bool) {
	if enforcer == nil {
		return
	}
	enforcer.tracker.Observe(enforcer.client(request), failed)
}

// elevated answers the risk question of one request. A missing signal is
// elevation (see NewEnforcer).
func (enforcer *Enforcer) elevated(request *http.Request) bool {
	if enforcer.tracker == nil {
		return true
	}
	address := enforcer.client(request)
	if address == "" {
		// A caller that cannot be identified cannot be shown to be safe.
		return true
	}
	return enforcer.tracker.Elevated(address)
}

// client is the caller's address as clientip decides it, which is what makes
// forwarding headers evidence only from a trusted proxy. An empty result means
// the address could not be read: verification proceeds without it, since the
// provider treats it as advisory.
func (enforcer *Enforcer) client(request *http.Request) string {
	client := enforcer.resolver.Client(request)
	if !client.IsValid() {
		return ""
	}
	return client.String()
}

// tokenOf extracts the presented token: the header first, and the widget's
// form field only for a form-encoded body.
//
// The form field is read through PostFormValue, which parses the request body;
// it is deliberately not consulted for a JSON body, where parsing would
// consume the payload the handler still has to decode. The widget's own form
// posts are form-encoded, so the honest fallback is exactly the case that
// needs it.
func tokenOf(request *http.Request) string {
	if token := request.Header.Get(DefaultChallengeHeaderName); token != "" {
		return token
	}

	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return ""
	}
	return request.PostFormValue(DefaultChallengeFormField)
}

// WriteRefusal writes the standard refusal of a failed challenge: the RFC 9457
// problem the verifier described, with its stable code and its status.
//
// The caller is told which code it is, and nothing about which one it was in
// any other sense: whether the token was replayed, minted for another action
// or minted for another hostname is information about our configuration, not
// about the caller's mistake, and a script that can distinguish them can
// probe. Every detail stays generic for the same reason.
func WriteRefusal(writer http.ResponseWriter, request *http.Request, err error) int {
	return httperror.WriteProblem(writer, request, refusalProblem(err))
}

// refusalProblem turns a verifier error into the problem that reaches the
// client. An error that is not one of ours is reported as an invalid challenge
// rather than forwarded: a verifier that failed in a way it did not describe
// must not get to decide what the caller is told.
func refusalProblem(err error) *apperr.Error {
	var appError *apperr.Error
	if errors.As(err, &appError) {
		return appError
	}
	return refusal(apperr.KindForbidden, CodeChallengeInvalid, "the challenge token is not valid")
}
