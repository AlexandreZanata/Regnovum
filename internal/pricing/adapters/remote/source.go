package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/ports"
	"github.com/AlexandreZanata/Regnovum/internal/pricing/application"
	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// maxResponseBytes bounds one source answer: large enough for a price
// document, small enough that a runaway body cannot exhaust the
// collector.
const maxResponseBytes = 1 << 20

var _ application.Source = (*HTTPSource)(nil)

// Breaker guards one source with a closed/open/half-open circuit:
// consecutive failures open it for a cooldown, one probe then decides
// whether it closes. Thresholds arrive per breaker: no ratified
// policy lives here, and later phases set the vigente values where
// quotes are accepted. Calls are safe for sequential collectors; a
// mutex keeps parallel rounds honest too.
type Breaker struct {
	maxFailures int
	cooldown    time.Duration
	clock       ports.Clock
	mutex       sync.Mutex
	failures    int
	openedAt    time.Time
	halfOpen    bool
}

// NewBreaker builds one circuit with explicit thresholds and clock.
func NewBreaker(maxFailures int, cooldown time.Duration, clock ports.Clock) (*Breaker, error) {
	if maxFailures < 1 || cooldown <= 0 || clock == nil {
		return nil, domain.ErrInvalidSource
	}
	return &Breaker{maxFailures: maxFailures, cooldown: cooldown, clock: clock}, nil
}

// Allow reports whether a fetch may proceed now.
func (b *Breaker) Allow() bool {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	if b.openedAt.IsZero() {
		return true
	}
	if b.clock.Now().Before(b.openedAt.Add(b.cooldown)) {
		return false
	}
	if b.halfOpen {
		return false
	}
	b.halfOpen = true
	return true
}

// Succeed closes the circuit: consecutive failures reset and probes end.
func (b *Breaker) Succeed() {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.failures = 0
	b.openedAt = time.Time{}
	b.halfOpen = false
}

// Fail records one failed fetch, opening the circuit once the
// consecutive budget exhausts.
func (b *Breaker) Fail() {
	now := b.clock.Now()
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.failures++
	b.halfOpen = false
	if b.failures >= b.maxFailures {
		b.openedAt = now
	}
}

// HTTPSource fetches one approved source over HTTP: timeout per
// request, breaker per source, strict JSON parsing and provenance
// bound to the raw bytes. Sources are independent by construction:
// client, endpoint, breaker and identity are never shared. No live
// provider is approved here: endpoints arrive per deployment, and no
// URL, key or secret is hardcoded.
type HTTPSource struct {
	id       domain.SourceID
	endpoint string
	client   *http.Client
	breaker  *Breaker
}

// NewHTTPSource builds one HTTP source with explicit wiring. Empty
// identities, unparsable endpoints, missing hosts and non-positive
// timeouts refuse; a nil breaker refuses too, so no fetch ever runs
// unguarded.
func NewHTTPSource(id domain.SourceID, endpoint string, timeout time.Duration, breaker *Breaker) (*HTTPSource, error) {
	if id.String() == "" {
		return nil, domain.ErrInvalidSource
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil, domain.ErrInvalidSource
	}
	if timeout <= 0 {
		return nil, domain.ErrInvalidSource
	}
	if breaker == nil {
		return nil, domain.ErrInvalidSource
	}
	return &HTTPSource{
		id:       id,
		endpoint: endpoint,
		client:   &http.Client{Timeout: timeout},
		breaker:  breaker,
	}, nil
}

// ID names the approved source.
func (s *HTTPSource) ID() domain.SourceID { return s.id }

// Fetch returns the source's current sighting. An open breaker, a
// transport failure, a non-200 status and a malformed document all
// refuse with the source unavailable or invalid: malformed answers
// never become prices, and one dark source never fails the round (the
// registry decides that).
func (s *HTTPSource) Fetch(ctx context.Context) (domain.Observation, error) {
	if !s.breaker.Allow() {
		return domain.Observation{}, fmt.Errorf("%w: source %q", domain.ErrSourceUnavailable, s.id)
	}
	seen, err := s.fetch(ctx)
	if err != nil {
		s.breaker.Fail()
		return domain.Observation{}, err
	}
	s.breaker.Succeed()
	return seen, nil
}

func (s *HTTPSource) fetch(ctx context.Context) (domain.Observation, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint, nil)
	if err != nil {
		return domain.Observation{}, fmt.Errorf("%w: source %q", domain.ErrSourceUnavailable, s.id)
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return domain.Observation{}, fmt.Errorf("%w: source %q", domain.ErrSourceUnavailable, s.id)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return domain.Observation{}, fmt.Errorf("%w: source %q answered %d", domain.ErrSourceUnavailable, s.id, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return domain.Observation{}, fmt.Errorf("%w: source %q", domain.ErrInvalidObservation, s.id)
	}
	if len(body) > maxResponseBytes {
		return domain.Observation{}, fmt.Errorf("%w: source %q answered over the body budget", domain.ErrInvalidObservation, s.id)
	}
	return parseObservation(s.id, body)
}

// rateDocument is the wire shape one source answers: integer minor
// units with the instant the source observed them. Unknown fields
// refuse: a document that carries more than price and instant is not
// the quote this port speaks.
type rateDocument struct {
	PriceMinor int64  `json:"price_minor"`
	ObservedAt string `json:"observed_at"`
}

func parseObservation(id domain.SourceID, body []byte) (domain.Observation, error) {
	var document rateDocument
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return domain.Observation{}, fmt.Errorf("%w: source %q", domain.ErrInvalidObservation, id)
	}
	observedAt, err := time.Parse(time.RFC3339, document.ObservedAt)
	if err != nil {
		return domain.Observation{}, fmt.Errorf("%w: source %q", domain.ErrInvalidObservation, id)
	}
	seen, err := domain.NewObservation(id, document.PriceMinor, observedAt, body)
	if err != nil {
		return domain.Observation{}, fmt.Errorf("%w: source %q", domain.ErrInvalidObservation, id)
	}
	return seen, nil
}
