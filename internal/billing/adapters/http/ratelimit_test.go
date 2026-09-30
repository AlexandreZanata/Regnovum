// Wiring test for the billing rate limit (P16-T03): the two writes that call
// Stripe are wrapped, and the wrapper refuses before the billing handler runs.
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

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
)

func TestBillingWritesCarryThePolicy(t *testing.T) {
	t.Parallel()

	enforcer := ratelimit.New(ratelimit.GuardFunc(
		func(context.Context, ratelimit.Action, ...ratelimit.Subject) (ratelimit.Decision, error) {
			return ratelimit.Decision{Allowed: false, Refused: ratelimit.SubjectAccount, RetryAfter: 45 * time.Second, Limit: 8}, nil
		},
	), clientip.New(nil))

	handler := adapterhttp.NewBillingHandler(adapterhttp.BillingHandlerConfig{RateLimit: enforcer})
	mux := http.NewServeMux()
	handler.RegisterBillingRoutes(mux)

	for _, path := range []string{"/api/v1/me/billing/checkout", "/api/v1/me/billing/portal"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusTooManyRequests {
			t.Errorf("%s: status = %d, want 429 (body: %s)", path, recorder.Code, recorder.Body.String())
			continue
		}
		if retryAfter := recorder.Header().Get("Retry-After"); retryAfter != "45" {
			t.Errorf("%s: Retry-After = %q, want the wait the policy reported", path, retryAfter)
		}
		if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl == "" {
			t.Errorf("%s: a refusal lost the private cache headers", path)
		}
	}
}
