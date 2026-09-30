// Wiring test for the report rate limit (P16-T03): filing a report is wrapped,
// and the wrapper refuses before the use case runs.
//
// The use case is nil on purpose: a throttled request must never reach the code
// that would dereference it, so a wiring regression fails loudly rather than
// quietly. The refusal shape is asserted once, in internal/platform/ratelimit.
package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
)

func TestReportFilingCarriesThePolicy(t *testing.T) {
	t.Parallel()

	enforcer := ratelimit.New(ratelimit.GuardFunc(
		func(context.Context, ratelimit.Action, ...ratelimit.Subject) (ratelimit.Decision, error) {
			return ratelimit.Decision{Allowed: false, Refused: ratelimit.SubjectAccount, RetryAfter: 2 * time.Minute, Limit: 6}, nil
		},
	), clientip.New(nil))

	handler := adapterhttp.NewHandler(adapterhttp.HandlerConfig{RateLimit: enforcer})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/me/moderation/reports", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if retryAfter := recorder.Header().Get("Retry-After"); retryAfter != "120" {
		t.Errorf("Retry-After = %q, want the wait the policy reported", retryAfter)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl == "" {
		t.Error("a refusal lost the private cache headers")
	}
}
