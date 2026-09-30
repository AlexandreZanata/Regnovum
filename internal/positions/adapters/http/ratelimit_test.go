// Wiring test for the position rate limit (P16-T03): the two writes are
// wrapped, and the wrapper refuses before any use case runs.
//
// The use cases of this handler are left nil on purpose. The throttle sits in
// front of the handler, so a throttled request must never reach the code that
// would dereference them; a wiring regression therefore fails loudly instead of
// quietly passing. The refusal shape itself (status, code, Retry-After, the
// absence of a body echo) is asserted once, in internal/platform/ratelimit.
package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/http"
)

// refusingEnforcer is the real enforcer over a guard that refuses everything,
// which is what makes the wrapper itself visible to the test.
func refusingEnforcer(t *testing.T) *ratelimit.Enforcer {
	t.Helper()

	return ratelimit.New(ratelimit.GuardFunc(
		func(context.Context, ratelimit.Action, ...ratelimit.Subject) (ratelimit.Decision, error) {
			return ratelimit.Decision{Allowed: false, Refused: ratelimit.SubjectAddress, RetryAfter: time.Minute, Limit: 10}, nil
		},
	), clientip.New(nil))
}

func TestPositionWritesCarryThePolicy(t *testing.T) {
	t.Parallel()

	handler := adapterhttp.NewHandler(adapterhttp.HandlerConfig{RateLimit: refusingEnforcer(t)})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// A concrete arena id: the throttle runs before the route's own handler, so
	// the id never has to exist.
	const arenaID = "11111111-1111-1111-1111-111111111111"

	for _, path := range []string{
		"/api/v1/me/arenas/" + arenaID + "/position",
		"/api/v1/me/arenas/" + arenaID + "/position/changes",
	} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusTooManyRequests {
			t.Errorf("%s: status = %d, want 429 (body: %s)", path, recorder.Code, recorder.Body.String())
			continue
		}
		if retryAfter := recorder.Header().Get("Retry-After"); retryAfter != "60" {
			t.Errorf("%s: Retry-After = %q, want the wait the policy reported", path, retryAfter)
		}
		if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl == "" {
			t.Errorf("%s: a refusal lost the private cache headers", path)
		}
	}
}
