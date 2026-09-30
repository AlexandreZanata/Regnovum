package providersim

import (
	"net/http"
	"testing"
)

// The observability surface: the two provider endpoints the telemetry adapters
// post to, and the answers they must survive. Telemetry is the one client whose
// failures are never the request's problem — a provider that refuses, is slow
// or is not there at all must stay invisible to the caller — so the states this
// simulator produces are the ones that prove it.
const (
	// SentryEnvelopePath is the documented envelope address. The project is a
	// path segment, so the route is declared by prefix: a fixture whose DSN
	// carries project 42 posts to /api/42/envelope/.
	SentryEnvelopePath = "POST /api/*"
	// SentryProject is the project number of the fixture's DSN.
	SentryProject = "42"
	// PostHogBatchPath is the documented batch address.
	PostHogBatchPath = "/batch/"
	// PostHogBatch is the same address as the simulator's route key.
	PostHogBatch = "POST " + PostHogBatchPath
)

// The synthetic credentials of the two providers, and the delivery contract of
// the analytics one. The credentials are composed so that no scanner reads a
// complete credential in this file.
const (
	SentryKey     = "sim" + "sentrykey"
	PostHogAPIKey = "phc" + "_sim_synthetic_project_key"
	PostHogHost   = "https://us.i.posthog.com"
)

// PostHogBatchSize is the number of captured events at which the delivered
// analytics client flushes a batch instead of waiting for its interval. It is
// the client's own default (defaultPostHogBatchSize in
// internal/platform/observability/providers.go), declared here because a fixture
// has to reach it: the batch leaves on a full batch or on the interval, and a
// fixture that filled the batch asserts about the delivery instead of sleeping
// through the interval. A drift on either side is loud, never silent — a smaller
// batch arrives early and still passes, a larger one makes the fixture time out
// naming the address it never received.
const PostHogBatchSize = 100

// NewSentry builds the fake error tracker with the envelope accepted.
func NewSentry(t testing.TB, options ...Option) *Simulator {
	t.Helper()
	simulator := New(t, "sentry", options...)
	simulator.handler = sentryRefusal
	simulator.Route(SentryEnvelopePath, Reply(http.StatusOK, sentryAcceptedBody()))
	return simulator
}

// NewPostHog builds the fake product analytics service with the batch accepted.
func NewPostHog(t testing.TB, options ...Option) *Simulator {
	t.Helper()
	simulator := New(t, "posthog", options...)
	simulator.handler = postHogRefusal
	simulator.Route(PostHogBatch, Reply(http.StatusOK, postHogAcceptedBody()))
	return simulator
}

// sentryRefusal and postHogRefusal are what each fake telemetry provider
// answers a call nobody scripted. Neither provider's client reads the refusal
// document, and answering the provider's own shape keeps the recorder honest
// about what was sent.
func sentryRefusal(method, path string) (int, string) {
	return UnexpectedStatus, encodedDocument(map[string]any{"detail": method + " " + path + " is not scripted"})
}

func postHogRefusal(method, path string) (int, string) {
	return UnexpectedStatus, encodedDocument(map[string]any{"status": 0, "detail": method + " " + path + " is not scripted"})
}

// sentryAcceptedBody is the receipt of an accepted envelope. The reporter does
// not read it; it exists so that a 200 carries a document, the way the
// provider's API answers.
func sentryAcceptedBody() string {
	return encodedDocument(map[string]any{"id": "event_sim"})
}

// postHogAcceptedBody is the receipt of an accepted batch.
func postHogAcceptedBody() string {
	return encodedDocument(map[string]any{"status": 1})
}
