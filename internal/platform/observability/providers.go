package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// The transport defaults of both provider adapters. They mirror the Resend
// adapter's precedent: a bounded attempt, a bounded response, no retries
// beyond what the caller allows, and a body that is never quoted into a log.
const (
	sentryEnvelopePath = "/api/%s/envelope/"
	posthogBatchPath   = "/batch/"

	defaultProviderTimeout = 5 * time.Second
	maxResponseBytes       = 4 * 1024
	maxMessageLength       = 512

	sentryQueueSize         = 64
	posthogQueueSize        = 512
	defaultPostHogFlush     = 5 * time.Second
	defaultPostHogBatchSize = 100
)

// sentryConfig is the adapter configuration from the composition root. The
// timeout and queue size are fields (not constants) so the tests can drive
// them without sleeping on provider timeouts.
type sentryConfig struct {
	DSN string
	// BaseURL overrides the origin the envelope is posted to. Empty uses the
	// origin of the DSN. It exists for the loopback fake of the tests, the
	// same allowance the PostHog host has.
	BaseURL     string
	Environment string
	Clock       ports.Clock
	Logger      *slog.Logger
	Timeout     time.Duration
	QueueSize   int
	Dropped     *Counter
}

// sentryReporter reports failures through the Sentry envelope API. It is an
// adapter: it converts ErrorReport values into the provider's document and
// posts them; every provider type stays inside this file.
//
// Report is non-blocking by contract: the report goes to a bounded queue and
// one goroutine posts it. A full queue drops the report and counts the drop.
type sentryReporter struct {
	endpoint    string
	authHeader  string
	environment string
	client      *http.Client
	clock       ports.Clock
	logger      *slog.Logger

	queue   chan ErrorReport
	dropped *Counter

	done    chan struct{}
	stopped chan struct{}
	once    sync.Once

	sequence atomic.Uint64
}

// newSentryReporter validates the configuration and builds the adapter. A
// DSN that does not parse is a composition refusal, not a runtime surprise.
func newSentryReporter(config sentryConfig) (*sentryReporter, error) {
	host, project, key, err := parseSentryDSN(config.DSN)
	if err != nil {
		return nil, fmt.Errorf("observability: sentry: %w", err)
	}
	base := "https://" + host
	if config.BaseURL != "" {
		base = config.BaseURL
	}
	endpoint, err := sentryEndpoint(base, project)
	if err != nil {
		return nil, fmt.Errorf("observability: sentry: %w", err)
	}
	if config.Clock == nil {
		return nil, errors.New("observability: sentry: clock is required")
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultProviderTimeout
	}
	queueSize := config.QueueSize
	if queueSize <= 0 {
		queueSize = sentryQueueSize
	}

	reporter := &sentryReporter{
		endpoint:    endpoint,
		authHeader:  "Sentry sentry_key=" + key + ", sentry_version=7, sentry_client=regnovum/1.0",
		environment: config.Environment,
		client:      &http.Client{Timeout: timeout},
		clock:       config.Clock,
		logger:      loggerOrDiscard(config.Logger),
		queue:       make(chan ErrorReport, queueSize),
		dropped:     config.Dropped,
		done:        make(chan struct{}),
		stopped:     make(chan struct{}),
	}
	go reporter.run()
	return reporter, nil
}

// Report implements ErrorReporter. It never blocks and never panics.
func (reporter *sentryReporter) Report(report ErrorReport) {
	if reporter == nil {
		return
	}
	select {
	case reporter.queue <- report:
	default:
		if reporter.dropped != nil {
			reporter.dropped.Inc()
		}
	}
}

// Close stops the reporter without draining: a report still queued at
// shutdown is dropped, never awaited.
func (reporter *sentryReporter) Close() {
	if reporter == nil {
		return
	}
	reporter.once.Do(func() {
		close(reporter.done)
		<-reporter.stopped
	})
}

// run posts reports until stopped.
func (reporter *sentryReporter) run() {
	defer close(reporter.stopped)
	for {
		select {
		case <-reporter.done:
			return
		case report := <-reporter.queue:
			if err := reporter.send(report); err != nil {
				reporter.logger.Warn("observability: sentry report not delivered", slog.String("error", err.Error()))
			}
		}
	}
}

// send posts one report as a single-event envelope. The response body is
// mined for nothing: the status is the outcome.
func (reporter *sentryReporter) send(report ErrorReport) error {
	payload, err := reporter.envelope(report)
	if err != nil {
		return err
	}

	request, err := http.NewRequest(http.MethodPost, reporter.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-sentry-envelope")
	request.Header.Set("X-Sentry-Auth", reporter.authHeader)

	response, err := reporter.client.Do(request)
	if err != nil {
		return fmt.Errorf("sentry transport: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("sentry provider answered %d", response.StatusCode)
	}
	return nil
}

// envelope renders the provider document: a header line, an item header line
// and the event. The message is bounded and the tags are the sanitized
// operational fields; no free-form detail travels, because a detail is where
// an unexpected failure echoes its input.
func (reporter *sentryReporter) envelope(report ErrorReport) ([]byte, error) {
	eventID := reporter.eventID(report)
	event := map[string]any{
		"event_id":  eventID,
		"timestamp": reporter.clock.Now().UTC().Format(time.RFC3339Nano),
		"platform":  "go",
		"level":     "error",
		"logger":    "regnovum",
		"message":   bounded(report.Message, maxMessageLength),
		"tags":      SanitizedTags(report),
	}
	if reporter.environment != "" {
		event["environment"] = bounded(reporter.environment, maxTagLength)
	}

	header, err := json.Marshal(map[string]any{"event_id": eventID})
	if err != nil {
		return nil, err
	}
	itemHeader, err := json.Marshal(map[string]any{"type": "event", "content_type": "application/json"})
	if err != nil {
		return nil, err
	}
	item, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	return bytes.Join([][]byte{header, itemHeader, item}, []byte("\n")), nil
}

// eventID derives the 32 hexadecimal characters Sentry expects from the
// report itself plus a per-process sequence. It is not random because the
// architecture gate keeps randomness behind an injected port, and a
// correlation identifier that repeats across restarts for the same report is
// the same report as far as the provider is concerned.
func (reporter *sentryReporter) eventID(report ErrorReport) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(report.Message + "\x00" + report.Kind + "\x00" + report.Operation + "\x00" + report.RequestID))
	return fmt.Sprintf("%016x%016x", hash.Sum64(), reporter.sequence.Add(1))
}

// parseSentryDSN splits the provider DSN into its host, project and
// credential. The shape is documented by the provider:
//
//	https://<publicKey>@<host>/<projectID>
//
// The endpoint never carries the key: the credential travels in the
// X-Sentry-Auth header, so an error from the transport cannot quote it.
func parseSentryDSN(raw string) (host, project, key string, err error) {
	if strings.TrimSpace(raw) != raw || raw == "" {
		return "", "", "", errors.New("DSN must be a non-empty value without surrounding whitespace")
	}
	rest, found := strings.CutPrefix(raw, "https://")
	if !found {
		return "", "", "", errors.New("DSN must start with https://<key>@<host>/<project>")
	}
	key, rest, found = strings.Cut(rest, "@")
	if !found || key == "" {
		return "", "", "", errors.New("DSN is missing the public key before @")
	}
	host, project, found = strings.Cut(rest, "/")
	if !found || host == "" || project == "" {
		return "", "", "", errors.New("DSN is missing the host or the project id")
	}
	if strings.ContainsAny(project, "/?#") {
		return "", "", "", errors.New("DSN has a malformed project id")
	}
	if _, convErr := strconv.ParseUint(project, 10, 64); convErr != nil {
		return "", "", "", errors.New("DSN project id is not numeric")
	}
	if strings.ContainsAny(host, " /?#") {
		return "", "", "", errors.New("DSN has a malformed host")
	}
	return host, project, key, nil
}

// sentryEndpoint renders the envelope endpoint of one project from an origin.
// Plain http is accepted only for a loopback host, the test fake.
func sentryEndpoint(base, project string) (string, error) {
	base = strings.TrimSuffix(base, "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("origin %q is not an absolute URL", base)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return "", errors.New("the origin must use https (plain http is accepted only for a loopback host)")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("the origin must not carry a path, query, fragment or credentials")
	}
	return base + fmt.Sprintf(sentryEnvelopePath, project), nil
}

// posthogConfig is the adapter configuration from the composition root.
type posthogConfig struct {
	APIKey        string
	Host          string
	Clock         ports.Clock
	Logger        *slog.Logger
	Timeout       time.Duration
	FlushInterval time.Duration
	BatchSize     int
	QueueSize     int
	Dropped       *Counter
}

// postHogSink captures analytics events through the PostHog batch API. It
// owns one goroutine and one bounded queue: Capture only enqueues, and the
// goroutine flushes a batch when it fills or when the interval elapses.
type postHogSink struct {
	endpoint string
	apiKey   string
	client   *http.Client
	clock    ports.Clock
	logger   *slog.Logger
	dropped  *Counter

	queue         chan queuedEvent
	flushInterval time.Duration
	batchSize     int

	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

// queuedEvent is one captured event with the instant it was captured, so the
// provider timestamp is when the event happened and not when the batch left.
type queuedEvent struct {
	event      Event
	capturedAt time.Time
}

// newPostHogSink validates the configuration and builds the adapter.
func newPostHogSink(config posthogConfig) (*postHogSink, error) {
	if config.APIKey == "" {
		return nil, errors.New("observability: posthog: the project API key is required")
	}
	if config.Clock == nil {
		return nil, errors.New("observability: posthog: clock is required")
	}
	host := config.Host
	if host == "" {
		host = "https://us.i.posthog.com"
	}
	parsed, err := url.Parse(host)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("observability: posthog: host %q is not an absolute URL", host)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("observability: posthog: the host must use https (plain http is accepted only for a loopback host)")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("observability: posthog: the host must be an origin without path, query, fragment or credentials")
	}

	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultProviderTimeout
	}
	flushInterval := config.FlushInterval
	if flushInterval <= 0 {
		flushInterval = defaultPostHogFlush
	}
	batchSize := config.BatchSize
	if batchSize <= 0 || batchSize > defaultPostHogBatchSize {
		batchSize = defaultPostHogBatchSize
	}
	queueSize := config.QueueSize
	if queueSize <= 0 {
		queueSize = posthogQueueSize
	}

	sink := &postHogSink{
		endpoint:      strings.TrimSuffix(host, "/") + posthogBatchPath,
		apiKey:        config.APIKey,
		client:        &http.Client{Timeout: timeout},
		clock:         config.Clock,
		logger:        loggerOrDiscard(config.Logger),
		dropped:       config.Dropped,
		queue:         make(chan queuedEvent, queueSize),
		flushInterval: flushInterval,
		batchSize:     batchSize,
		done:          make(chan struct{}),
		stopped:       make(chan struct{}),
	}
	go sink.run()
	return sink, nil
}

// Capture implements EventSink. It never blocks: a full queue drops the
// event and counts the drop.
func (sink *postHogSink) Capture(event Event) {
	if sink == nil {
		return
	}
	select {
	case sink.queue <- queuedEvent{event: event, capturedAt: sink.clock.Now()}:
	default:
		if sink.dropped != nil {
			sink.dropped.Inc()
		}
	}
}

// Close stops the batching goroutine without draining.
func (sink *postHogSink) Close() {
	if sink == nil {
		return
	}
	sink.once.Do(func() {
		close(sink.done)
		<-sink.stopped
	})
}

// run batches until stopped.
func (sink *postHogSink) run() {
	defer close(sink.stopped)
	ticker := time.NewTicker(sink.flushInterval)
	defer ticker.Stop()

	batch := make([]queuedEvent, 0, sink.batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := sink.send(batch); err != nil {
			sink.logger.Warn("observability: analytics batch not delivered", slog.String("error", err.Error()))
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-sink.done:
			return
		case queued := <-sink.queue:
			batch = append(batch, queued)
			if len(batch) >= sink.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// send posts one batch document. The response body is discarded without
// being read into a value: the status is the outcome.
func (sink *postHogSink) send(batch []queuedEvent) error {
	items := make([]map[string]any, 0, len(batch))
	for _, queued := range batch {
		properties := make(map[string]any, len(queued.event.Properties)+1)
		for name, value := range queued.event.Properties {
			properties[name] = value
		}
		properties["distinct_id"] = queued.event.AccountID
		items = append(items, map[string]any{
			"event":      queued.event.Name,
			"properties": properties,
			"timestamp":  queued.capturedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	document := map[string]any{"api_key": sink.apiKey, "batch": items}
	payload, err := json.Marshal(document)
	if err != nil {
		return err
	}

	request, err := http.NewRequest(http.MethodPost, sink.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := sink.client.Do(request)
	if err != nil {
		return fmt.Errorf("posthog transport: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("posthog provider answered %d", response.StatusCode)
	}
	return nil
}

// isLoopbackHost reports whether the host is a loopback literal, the only
// place a plain http provider endpoint is accepted (the test fake).
func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// bounded truncates a string to at most limit bytes without splitting a
// multi-byte rune.
func bounded(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8Start(value[cut]) {
		cut--
	}
	return value[:cut]
}

// utf8Start reports whether the byte begins a UTF-8 sequence.
func utf8Start(character byte) bool {
	return character&0xC0 != 0x80
}

// loggerOrDiscard returns the configured logger or a logger that writes
// nowhere, so an adapter built without one cannot panic on a hot path.
func loggerOrDiscard(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.DiscardHandler)
}
