// Package observability carries the telemetry of the process (P19-T05): an
// error reporter (Sentry), a product analytics sink (PostHog) and the metrics
// surface (RED for HTTP, USE for the database pool and the job queue).
//
// The rules the plan demands, and how the package enforces them:
//
//   - Provider SDKs stay out of the process. Sentry and PostHog are reached
//     through their HTTP APIs by their own adapters (the precedent is the
//     Resend email adapter: net/http, a bounded timeout, a response body never
//     quoted into a log); what crosses this package are plain values.
//   - Analytics never blocks a request. Capture validates, samples and hands
//     the event to each sink's own bounded queue; a full queue drops the event
//     and counts the drop. Telemetry is the first thing allowed to fail, never
//     the request.
//   - Analytics is allowlisted. An event name outside events.go is a
//     programming error and is refused in every mode; a property outside the
//     event's admitted set is refused too. Nothing request-derived can widen
//     what is sent.
//   - Nothing sensitive is sent, by construction and not by blacklist: the
//     admitted properties are a locale tag (validated against the catalog
//     allowlist) and a bounded integer. There is no admitted string property
//     an email, an argument or a provider payload could travel in, and an
//     event without account attribution is refused rather than aggregated
//     under a lie.
//   - Metrics never leave the process here: the registry is rendered in the
//     Prometheus text format by the administrative listener (cmd/arena
//     wiring), because a metrics scrape is an operator concern, not a
//     provider upload.
package observability

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/AlexandreZanata/Regnovum/internal/platform/requestid"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Event is one allowlisted product analytics event. Name is a constant from
// events.go; Properties carries only the names that event's allowlist admits,
// as a locale tag or a bounded integer.
type Event struct {
	// Name is the allowlisted event name, for example
	// EventAccountSignedIn. A name outside the allowlist refuses the event.
	Name string
	// Properties are the admitted properties of this event. Nil is valid.
	Properties map[string]any
	// AccountID is the authenticated account the event belongs to. Every
	// admitted event carries one: an event with no attribution has no
	// distinct identifier to aggregate under, and is refused.
	AccountID string
	// RequestID correlates the event with the server log line of the same
	// request and makes the sampling decision stable per request.
	RequestID string
}

// ErrorReport is one captured failure for the error reporter. Message is a
// stable machine string; no request payload ever travels with a report, and
// the tags are the bounded operational fields below.
type ErrorReport struct {
	// Message names what failed, for example "job email_delivery failed".
	Message string
	// Kind is the failure class, for example "panic" or "handler".
	Kind string
	// Operation names the step that failed, for example a job type or an
	// HTTP route.
	Operation string
	// RequestID correlates the report with the request log line.
	RequestID string
}

// ErrorReporter accepts failure reports. Implementations must not panic and
// must not block the caller beyond their internal bound: reporting is
// best-effort by contract.
type ErrorReporter interface {
	Report(report ErrorReport)
}

// EventSink accepts one allowlisted event. Implementations must be safe for
// concurrent use and must never block the caller on provider I/O.
type EventSink interface {
	Capture(event Event)
}

// nopReporter is the reporter of a process composed without an error
// reporter credential: the call sites stay the same and cost one branch.
type nopReporter struct{}

// Report implements ErrorReporter.
func (nopReporter) Report(ErrorReport) {}

// Config composes the telemetry of the process. Credentials arrive as plain
// values unredacted by the composition root and are never logged here; an
// empty credential disables that provider instead of failing the boot,
// because telemetry is optional and the application is not.
type Config struct {
	// Logger records what the telemetry decided (a refused event, a batch
	// the provider did not accept). Required.
	Logger *slog.Logger
	// Clock stamps the events and measures the request durations. Required:
	// the architecture gate refuses time.Now outside the clockseed package.
	Clock ports.Clock
	// Environment is the provider environment tag ("production", "test").
	// Empty omits the tag.
	Environment string
	// SentryDSN is the provider data source name. Empty disables error
	// reporting.
	SentryDSN string
	// PostHogAPIKey is the project write key of product analytics. Empty
	// disables analytics.
	PostHogAPIKey string
	// PostHogHost overrides the API root. Empty selects the US cloud.
	PostHogHost string
	// SentryBaseURL overrides the origin the error envelope is posted to.
	// Empty uses the origin of the DSN. It is the same loopback allowance
	// PostHogHost carries, and it is what lets a hermetic suite point the
	// reporter at the fake error tracker of the test platform instead of at
	// the internet (P22-T05).
	SentryBaseURL string
	// SampleRatePercent is the deterministic sampling rate of analytics,
	// 0..100, where 100 records every event and 0 records none. Errors are
	// never sampled.
	SampleRatePercent int
}

// Telemetry is the composed telemetry of the process: the two provider-facing
// ports, the metrics registry every surface writes to, and the lifecycle that
// stops the provider goroutines.
type Telemetry struct {
	// Events is the analytics front. It is always safe to call, even when no
	// provider is configured.
	Events EventSink
	// Errors is the error reporter. It is always safe to call.
	Errors ErrorReporter
	// Metrics is the process metrics registry.
	Metrics *Metrics

	closers   []func()
	closeOnce sync.Once
}

// New validates the configuration and builds the telemetry. It fails closed:
// a malformed DSN or host refuses the composition instead of failing at the
// first dropped event.
func New(config Config) (*Telemetry, error) {
	if config.Logger == nil {
		return nil, fmt.Errorf("observability: logger is required")
	}
	if config.Clock == nil {
		return nil, fmt.Errorf("observability: clock is required")
	}
	if config.SampleRatePercent < 0 || config.SampleRatePercent > 100 {
		return nil, fmt.Errorf("observability: sample rate %d is outside 0..100", config.SampleRatePercent)
	}

	metrics := NewMetrics(config.Clock)

	errors := ErrorReporter(nopReporter{})
	closers := make([]func(), 0, 2)
	if config.SentryDSN != "" {
		reporter, err := newSentryReporter(sentryConfig{
			DSN:         config.SentryDSN,
			BaseURL:     config.SentryBaseURL,
			Environment: config.Environment,
			Clock:       config.Clock,
			Logger:      config.Logger,
			Dropped: metrics.Counter(
				"telemetry_errors_dropped_total",
				"Error reports dropped because the reporter queue was full.",
				nil,
			),
		})
		if err != nil {
			return nil, err
		}
		errors = reporter
		closers = append(closers, reporter.Close)
	}

	sinks := make([]EventSink, 0, 1)
	if config.PostHogAPIKey != "" {
		sink, err := newPostHogSink(posthogConfig{
			APIKey:  config.PostHogAPIKey,
			Host:    config.PostHogHost,
			Clock:   config.Clock,
			Logger:  config.Logger,
			Dropped: metrics.Counter("telemetry_events_dropped_total", "Analytics events dropped because a provider queue was full.", nil),
		})
		if err != nil {
			return nil, err
		}
		sinks = append(sinks, sink)
		closers = append(closers, sink.Close)
	}

	events := newAnalytics(analyticsConfig{
		Sinks:   sinks,
		Sampler: NewSampler(uint64(config.SampleRatePercent), 100),
		Logger:  config.Logger,
		Metrics: metrics,
	})

	return &Telemetry{
		Events:  events,
		Errors:  errors,
		Metrics: metrics,
		closers: closers,
	}, nil
}

// Close stops the provider goroutines. Telemetry that has not left by then is
// dropped, never awaited: shutdown latency belongs to the requests, not to
// analytics. It is safe to call more than once and on a nil Telemetry.
func (telemetry *Telemetry) Close() {
	if telemetry == nil {
		return
	}
	telemetry.closeOnce.Do(func() {
		for index := len(telemetry.closers) - 1; index >= 0; index-- {
			telemetry.closers[index]()
		}
	})
}

// MetricsHandler renders the metrics registry for one scrape. The listener
// that serves it is the loopback administrative one; nothing else mounts it.
func (telemetry *Telemetry) MetricsHandler() http.Handler {
	if telemetry == nil || telemetry.Metrics == nil {
		return http.NotFoundHandler()
	}
	return telemetry.Metrics.Handler()
}

// HTTPMiddleware observes every request (RED: rate, errors, duration) and
// reports a handler panic to the error reporter before letting net/http
// recover it, so an unhandled panic is never silent. It is transparent: it
// writes nothing itself and preserves the ResponseWriter contract through
// Unwrap.
func (telemetry *Telemetry) HTTPMiddleware(next http.Handler) http.Handler {
	if telemetry == nil {
		return next
	}
	return telemetry.Metrics.httpMiddleware(next, telemetry.Errors)
}

// requestOperation names the request in an error report: the method and the
// matched route pattern, or "unmatched" when the mux answered 404.
func requestOperation(request *http.Request) string {
	return request.Method + " " + requestRoute(request)
}

// requestCorrelation reads the correlation identifier the platform middleware
// placed on the request.
func requestCorrelation(request *http.Request) string {
	return requestid.FromRequest(request)
}
