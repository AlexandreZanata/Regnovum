package ratelimit

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// Protector is the port the inbound adapters depend on: it wraps a handler with
// the policy of one action.
//
// The adapters depend on this and not on the Limiter because the two decisions
// are different: the budget table and the mechanism (Guard) are platform
// policy, while the adapter only has to say which action a route is. A test can
// therefore supply a fake protector, and a module test can supply a fake guard
// without losing the real subject extraction.
type Protector interface {
	// Protect wraps a handler with the policy of the action.
	Protect(action Action, next http.Handler) http.Handler
}

// CodeRateLimited is the stable problem code of a refusal. It is the same code
// the identity adapter already published, so clients that branch on it keep
// working.
const CodeRateLimited = "rate_limited"

// CodeRateLimitUnavailable is the stable problem code for a limiter that could
// not decide. It is a server failure, and it is named so that an operator can
// tell an outage of the limiter from a client being throttled.
const CodeRateLimitUnavailable = "rate_limit_unavailable"

// unknownAddress keys every request whose peer address could not be read. They
// share one bucket on purpose: an unidentifiable caller must be bounded, and a
// shared bucket is the restrictive direction. It is unreachable in practice —
// net/http always writes RemoteAddr — which is exactly why it must not be the
// unbounded direction.
const unknownAddress = "unknown"

// Enforcer binds a Guard to the transport facts of a request: the client
// address (as clientip decides, which is what makes forwarding headers
// evidence or noise) and the authenticated account, when the middleware chain
// has already resolved one.
type Enforcer struct {
	guard    Guard
	resolver clientip.Resolver
}

// New builds an enforcer. A nil guard disables the protection, which is what a
// composition that has not installed a limiter gets; production composition
// installs one (the package documents the single-process limit).
func New(guard Guard, resolver clientip.Resolver) *Enforcer {
	return &Enforcer{guard: guard, resolver: resolver}
}

// Protect implements Protector.
//
// The wrapper runs before the handler decodes anything, so a throttled request
// costs one map lookup and never reaches a use case. Placement is the
// adapter's responsibility and carries meaning: inside the authentication
// middleware (so the account dimension exists) and inside the private cache
// middleware (so a refusal is still `private, no-store`).
func (enforcer *Enforcer) Protect(action Action, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if enforcer == nil || enforcer.guard == nil {
			next.ServeHTTP(writer, request)
			return
		}

		decision, err := enforcer.guard.Allow(request.Context(), action, enforcer.subjects(request)...)
		if err != nil {
			// The limiter could not decide. Failing closed is the whole point
			// of a throttle, and the client is told that the server failed
			// rather than that it was throttled: a fabricated 429 with a real
			// Retry-After would be a lie that also invites a retry loop.
			_ = httperror.WriteProblem(writer, request,
				apperr.New(apperr.KindInternal, CodeRateLimitUnavailable, "the request could not be throttled").WithCause(err))
			return
		}

		if !decision.Allowed {
			WriteRefusal(writer, request, decision)
			return
		}

		next.ServeHTTP(writer, request)
	})
}

// subjects extracts the dimensions of one request. The address is always
// present, so no policy can end up with nothing to key on.
func (enforcer *Enforcer) subjects(request *http.Request) []Subject {
	address := unknownAddress
	if client := enforcer.resolver.Client(request); client.IsValid() {
		address = client.String()
	}

	subjects := []Subject{{Kind: SubjectAddress, Value: address}}
	if identity, ok := security.FromContext(request.Context()); ok && identity.AccountID != "" {
		subjects = append(subjects, Subject{Kind: SubjectAccount, Value: identity.AccountID})
	}
	return subjects
}

// WriteRefusal writes the standard refusal: the Retry-After header and the
// RFC 9457 problem with the stable rate limit code.
//
// Retry-After carries delta-seconds (RFC 9110 section 10.2.3), rounded *up*: a
// client told to retry in zero seconds after 400 milliseconds would retry
// immediately and be refused again, which is how a throttle turns into a
// hammer. A refusal with nothing to wait for (an unusable key) omits the
// header rather than promising an instant.
func WriteRefusal(writer http.ResponseWriter, request *http.Request, decision Decision) int {
	if decision.RetryAfter > 0 {
		writer.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(decision.RetryAfter)))
	}
	// The title comes from the localized catalog through httperror; the code
	// is the contract, and the detail stays generic so a refusal never
	// reflects what the caller sent or which dimension refused.
	return httperror.WriteProblem(writer, request,
		apperr.New(apperr.KindRateLimited, CodeRateLimited, "too many requests, please retry later"))
}

// retryAfterSeconds rounds a wait up to whole seconds, never down to zero.
func retryAfterSeconds(wait time.Duration) int {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}
