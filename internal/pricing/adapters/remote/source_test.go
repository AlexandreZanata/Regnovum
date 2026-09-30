package remote_test

// P35-T01 — independent HTTP rate sources on fake local servers.
//
// One approved source fetches price, instant and provenance over HTTP
// with its own timeout and circuit breaker: three fakes prove
// independence, malformed answers refuse before pricing, slow sources
// time out and repeated failures open the breaker until a probe
// recovers it. No test touches the internet: every server is local.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/adapters/remote"
	"github.com/AlexandreZanata/Regnovum/internal/pricing/application"
	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// fakeClock moves the breaker cooldown without waiting it out.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func rateInstant() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

func rateLimits() domain.ObservationLimits {
	return domain.ObservationLimits{MaxFutureSkew: time.Minute, MaxAge: 5 * time.Minute}
}

func rateBody(priceMinor int64, observedAt time.Time) string {
	return fmt.Sprintf(`{"price_minor":%d,"observed_at":%q}`, priceMinor, observedAt.Format(time.RFC3339))
}

// rateServer answers one canned body counting its hits.
func rateServer(t *testing.T, status int, body string, hits *atomic.Int64, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func mustHTTPSource(t *testing.T, id, endpoint string, timeout time.Duration, clock *fakeClock) *remote.HTTPSource {
	t.Helper()
	sourceID, err := domain.ParseSourceID(id)
	if err != nil {
		t.Fatalf("ParseSourceID: %v", err)
	}
	breaker, err := remote.NewBreaker(3, time.Minute, clock)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	source, err := remote.NewHTTPSource(sourceID, endpoint, timeout, breaker)
	if err != nil {
		t.Fatalf("NewHTTPSource: %v", err)
	}
	return source
}

func fetchCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// TestHTTPSourceCollectsThreeIndependentSources proves the round: three
// fakes with distinct prices settle through the registry with source,
// price, instant and provenance intact.
func TestHTTPSourceCollectsThreeIndependentSources(t *testing.T) {
	clock := &fakeClock{now: rateInstant()}
	prices := []int64{35000000, 35100000, 34900000}
	registry := application.NewRegistry()
	for i, price := range prices {
		var hits atomic.Int64
		server := rateServer(t, http.StatusOK, rateBody(price, rateInstant()), &hits, 0)
		t.Cleanup(server.Close)
		id := fmt.Sprintf("fonte-%d", i+1)
		if err := registry.Register(mustHTTPSource(t, id, server.URL, 2*time.Second, clock)); err != nil {
			t.Fatalf("Register %s: %v", id, err)
		}
	}
	if got := registry.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3 approved sources", got)
	}
	ctx, cancel := fetchCtx()
	defer cancel()
	sightings, failures := registry.Collect(ctx, rateInstant(), rateLimits())
	if len(failures) != 0 {
		t.Fatalf("failures = %+v, want none", failures)
	}
	if len(sightings) != 3 {
		t.Fatalf("sightings = %d, want 3", len(sightings))
	}
	seen := map[string]int64{}
	for _, sighting := range sightings {
		seen[sighting.Source.String()] = sighting.Price.Int64()
		if sighting.PayloadHash == "" {
			t.Fatalf("sighting without provenance: %+v", sighting)
		}
		if !sighting.ObservedAt.Equal(rateInstant()) {
			t.Fatalf("instant not carried: %+v", sighting)
		}
	}
	for i, price := range prices {
		if seen[fmt.Sprintf("fonte-%d", i+1)] != price {
			t.Fatalf("prices = %v, want %v", seen, prices)
		}
	}
}

// TestHTTPSourceRefusesMalformedAnswers proves malformed documents
// never become prices: bad JSON, wrong types, non-positive prices,
// missing or unparsable instants, unknown fields, oversized bodies
// and non-200 statuses all refuse.
func TestHTTPSourceRefusesMalformedAnswers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "bad json", status: http.StatusOK, body: `{"price_minor":`, want: domain.ErrInvalidObservation},
		{name: "price as string", status: http.StatusOK, body: `{"price_minor":"35000000","observed_at":"2026-10-02T12:00:00Z"}`, want: domain.ErrInvalidObservation},
		{name: "zero price", status: http.StatusOK, body: rateBody(0, rateInstant()), want: domain.ErrInvalidObservation},
		{name: "negative price", status: http.StatusOK, body: rateBody(-5, rateInstant()), want: domain.ErrInvalidObservation},
		{name: "missing instant", status: http.StatusOK, body: `{"price_minor":35000000}`, want: domain.ErrInvalidObservation},
		{name: "bad instant", status: http.StatusOK, body: `{"price_minor":35000000,"observed_at":"amanha"}`, want: domain.ErrInvalidObservation},
		{name: "unknown field", status: http.StatusOK, body: `{"price_minor":35000000,"observed_at":"2026-10-02T12:00:00Z","confiança":1}`, want: domain.ErrInvalidObservation},
		{name: "oversized body", status: http.StatusOK, body: strings.Repeat("x", (1<<20)+1), want: domain.ErrInvalidObservation},
		{name: "server error", status: http.StatusInternalServerError, body: `{}`, want: domain.ErrSourceUnavailable},
		{name: "not found", status: http.StatusNotFound, body: `{}`, want: domain.ErrSourceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			server := rateServer(t, tc.status, tc.body, &hits, 0)
			t.Cleanup(server.Close)
			source := mustHTTPSource(t, "fonte-x", server.URL, 2*time.Second, &fakeClock{now: rateInstant()})
			ctx, cancel := fetchCtx()
			defer cancel()
			if _, err := source.Fetch(ctx); !errors.Is(err, tc.want) {
				t.Fatalf("Fetch = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestHTTPSourceTimesOut proves a slow source fails the fetch without
// hanging the round: the timeout, not the server, decides.
func TestHTTPSourceTimesOut(t *testing.T) {
	var hits atomic.Int64
	server := rateServer(t, http.StatusOK, rateBody(35000000, rateInstant()), &hits, 500*time.Millisecond)
	t.Cleanup(server.Close)
	source := mustHTTPSource(t, "fonte-lenta", server.URL, 100*time.Millisecond, &fakeClock{now: rateInstant()})
	ctx, cancel := fetchCtx()
	defer cancel()
	if _, err := source.Fetch(ctx); !errors.Is(err, domain.ErrSourceUnavailable) {
		t.Fatalf("Fetch = %v, want ErrSourceUnavailable", err)
	}
}

// TestBreakerOpensAndRecovers proves the guard: consecutive failures
// open the circuit without touching the network again, one probe past
// the cooldown decides, and a success closes it.
func TestBreakerOpensAndRecovers(t *testing.T) {
	var hits atomic.Int64
	status := atomic.Int64{}
	status.Store(http.StatusInternalServerError)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(rateBody(35000000, rateInstant())))
	}))
	t.Cleanup(server.Close)

	clock := &fakeClock{now: rateInstant()}
	sourceID, err := domain.ParseSourceID("fonte-circuito")
	if err != nil {
		t.Fatalf("ParseSourceID: %v", err)
	}
	breaker, err := remote.NewBreaker(2, time.Minute, clock)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	source, err := remote.NewHTTPSource(sourceID, server.URL, 2*time.Second, breaker)
	if err != nil {
		t.Fatalf("NewHTTPSource: %v", err)
	}
	ctx, cancel := fetchCtx()
	defer cancel()
	if _, err := source.Fetch(ctx); !errors.Is(err, domain.ErrSourceUnavailable) {
		t.Fatalf("first Fetch = %v, want ErrSourceUnavailable", err)
	}
	if _, err := source.Fetch(ctx); !errors.Is(err, domain.ErrSourceUnavailable) {
		t.Fatalf("second Fetch = %v, want ErrSourceUnavailable", err)
	}
	if _, err := source.Fetch(ctx); !errors.Is(err, domain.ErrSourceUnavailable) {
		t.Fatalf("open Fetch = %v, want ErrSourceUnavailable", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("hits = %d, want 2: the open breaker must not dial", got)
	}
	clock.now = rateInstant().Add(61 * time.Second)
	status.Store(http.StatusOK)
	seen, err := source.Fetch(ctx)
	if err != nil {
		t.Fatalf("probe Fetch: %v", err)
	}
	if seen.Price.Int64() != 35000000 {
		t.Fatalf("probe price = %d, want 35000000", seen.Price.Int64())
	}
	if _, err := source.Fetch(ctx); err != nil {
		t.Fatalf("closed Fetch: %v", err)
	}
	if got := hits.Load(); got != 4 {
		t.Fatalf("hits = %d, want 4: probe plus closed fetch", got)
	}
}

// TestHTTPSourceRefusesIncompleteComposition proves explicit wiring:
// no identity, endpoint, timeout or breaker is ever defaulted.
func TestHTTPSourceRefusesIncompleteComposition(t *testing.T) {
	clock := &fakeClock{now: rateInstant()}
	breaker, err := remote.NewBreaker(3, time.Minute, clock)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	sourceID, err := domain.ParseSourceID("fonte-1")
	if err != nil {
		t.Fatalf("ParseSourceID: %v", err)
	}
	for _, tc := range []struct {
		name     string
		id       domain.SourceID
		endpoint string
		timeout  time.Duration
		breaker  *remote.Breaker
	}{
		{name: "empty identity", id: "", endpoint: "http://invalid.example", timeout: time.Second, breaker: breaker},
		{name: "bad endpoint", id: sourceID, endpoint: "://malformed", timeout: time.Second, breaker: breaker},
		{name: "missing host", id: sourceID, endpoint: "/sem-host", timeout: time.Second, breaker: breaker},
		{name: "no timeout", id: sourceID, endpoint: "http://invalid.example", timeout: 0, breaker: breaker},
		{name: "no breaker", id: sourceID, endpoint: "http://invalid.example", timeout: time.Second, breaker: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := remote.NewHTTPSource(tc.id, tc.endpoint, tc.timeout, tc.breaker); !errors.Is(err, domain.ErrInvalidSource) {
				t.Fatalf("NewHTTPSource = %v, want ErrInvalidSource", err)
			}
		})
	}
	if _, err := remote.NewBreaker(0, time.Minute, clock); !errors.Is(err, domain.ErrInvalidSource) {
		t.Fatalf("zero failures = %v, want ErrInvalidSource", err)
	}
	if _, err := remote.NewBreaker(3, 0, clock); !errors.Is(err, domain.ErrInvalidSource) {
		t.Fatalf("zero cooldown = %v, want ErrInvalidSource", err)
	}
	if _, err := remote.NewBreaker(3, time.Minute, nil); !errors.Is(err, domain.ErrInvalidSource) {
		t.Fatalf("nil clock = %v, want ErrInvalidSource", err)
	}
}
