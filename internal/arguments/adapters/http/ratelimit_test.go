// Wiring test for the argument rate limit (P16-T03): publishing and replying
// are wrapped, and the wrapper refuses before any use case runs.
//
// The use cases are nil on purpose: a throttled request must never reach the
// code that would dereference them, so a wiring regression fails loudly rather
// than quietly. The refusal shape is asserted once, in
// internal/platform/ratelimit.
package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
)

func TestArgumentWritesCarryThePolicy(t *testing.T) {
	t.Parallel()

	enforcer := ratelimit.New(ratelimit.GuardFunc(
		func(context.Context, ratelimit.Action, ...ratelimit.Subject) (ratelimit.Decision, error) {
			return ratelimit.Decision{Allowed: false, Refused: ratelimit.SubjectAddress, RetryAfter: 30 * time.Second, Limit: 12}, nil
		},
	), clientip.New(nil))

	handler := adapterhttp.NewHandler(adapterhttp.HandlerConfig{RateLimit: enforcer})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	const (
		arenaID    = "22222222-2222-2222-2222-222222222222"
		argumentID = "33333333-3333-3333-3333-333333333333"
	)

	for _, path := range []string{
		"/api/v1/me/arenas/" + arenaID + "/arguments",
		"/api/v1/me/arenas/" + arenaID + "/arguments/" + argumentID + "/replies",
	} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusTooManyRequests {
			t.Errorf("%s: status = %d, want 429 (body: %s)", path, recorder.Code, recorder.Body.String())
			continue
		}
		if retryAfter := recorder.Header().Get("Retry-After"); retryAfter != "30" {
			t.Errorf("%s: Retry-After = %q, want the wait the policy reported", path, retryAfter)
		}
	}
}
