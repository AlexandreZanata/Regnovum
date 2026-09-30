package observability

// P29-T05 — actionable privacy-safe signals: a synthetic failure driven
// through the composed middleware must produce the expected signal with
// a stable runbook-routable key, bounded cardinality and no request
// payload, and an unconfigured (offline) provider must never change what
// the client sees.
//
// The report Operation is the method plus the matched mux pattern — the
// closed vocabulary a runbook indexes by — never the raw path that
// minted it. Message and Kind are stable machine strings, untranslated
// by construction: no catalog lookup happens on this path.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/requestid"
)

// TestSyntheticFailureProducesRoutableSignal drives a panicking
// parameterized route carrying hostile request details and proves the
// signal is routable, stable and payload-free.
func TestSyntheticFailureProducesRoutableSignal(t *testing.T) {
	t.Parallel()

	metrics := NewMetrics(newTestClock())
	sink := &recordingReporter{}
	telemetry := &Telemetry{Metrics: metrics, Errors: sink}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /arenas/{slug}", func(http.ResponseWriter, *http.Request) {
		panic("synthetic datastore failure")
	})

	request := httptest.NewRequest(http.MethodGet, "/arenas/viral-slug-123?token=secret-token&email=owner@example.com", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Cookie", "arena_session=secret-session")
	request = request.WithContext(requestid.WithID(request.Context(), "req-synthetic-7"))

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("the panic must be re-thrown after being reported")
			}
		}()
		telemetry.HTTPMiddleware(mux).ServeHTTP(httptest.NewRecorder(), request)
	}()

	reports := sink.reports()
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want exactly 1", len(reports))
	}
	report := reports[0]
	if report.Kind != "panic" || report.Message != "http handler panic" {
		t.Errorf("report = %+v, want the stable panic signal", report)
	}
	if report.Operation != "GET /arenas/{slug}" {
		t.Errorf("operation = %q, want the matched pattern, the runbook key", report.Operation)
	}
	if report.RequestID != "req-synthetic-7" {
		t.Errorf("request ID = %q, want the correlated identifier", report.RequestID)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("report does not marshal: %v", err)
	}
	for _, secret := range []string{"viral-slug-123", "secret-token", "owner@example.com", "secret-session", "Bearer"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("report leaks %q: %s", secret, encoded)
		}
	}

	rendered := metrics.Render()
	if !strings.Contains(rendered, `http_requests_total{method="GET",route="/arenas/{slug}",status="500"} 1`) {
		t.Errorf("the failure was not counted by pattern:\n%s", rendered)
	}
	if strings.Contains(rendered, "viral-slug-123") {
		t.Errorf("the exposition mints series from raw paths:\n%s", rendered)
	}
}

// TestSignalCardinalityStaysBounded hammers distinct URLs and proves the
// exposition grows by vocabulary, never by stranger input: one series per
// route, one for the unmatched, and one per job type/outcome pair.
func TestSignalCardinalityStaysBounded(t *testing.T) {
	t.Parallel()

	metrics := NewMetrics(newTestClock())
	telemetry := &Telemetry{Metrics: metrics, Errors: nopReporter{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /arenas/{slug}", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	handler := telemetry.HTTPMiddleware(mux)

	for i := 0; i < 25; i++ {
		request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/arenas/slug-%d?x=%d", i, i), nil)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	for i := 0; i < 10; i++ {
		request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/nope/%d", i), nil)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	metrics.ObserveJob("email_delivery", "succeeded", 100*time.Millisecond)
	metrics.ObserveJob("email_delivery", "failed", 200*time.Millisecond)
	metrics.ObserveJob("export_build", "succeeded", 300*time.Millisecond)

	rendered := metrics.Render()
	series := func(prefix string) []string {
		var matched []string
		for _, line := range strings.Split(rendered, "\n") {
			if strings.HasPrefix(line, prefix) {
				matched = append(matched, line)
			}
		}
		return matched
	}

	matched := series(`http_requests_total{method="GET",route="/arenas/{slug}",status="200"}`)
	if len(matched) != 1 || !strings.HasSuffix(matched[0], " 25") {
		t.Errorf("matched route series = %v, want one series counting 25", matched)
	}
	unmatched := series(`http_requests_total{method="GET",route="unmatched",status="404"}`)
	if len(unmatched) != 1 || !strings.HasSuffix(unmatched[0], " 10") {
		t.Errorf("unmatched series = %v, want one series counting 10", unmatched)
	}
	jobs := series("jobs_processed_total{")
	if len(jobs) != 3 {
		t.Errorf("job series = %v, want exactly the three type/outcome pairs", jobs)
	}
}

// TestUnconfiguredProviderPathServesAndCounts proves telemetry outage is
// never a product outage: with no error reporter (and then with no
// telemetry state at all) requests are still served, counted and, on
// panic, re-thrown.
func TestUnconfiguredProviderPathServesAndCounts(t *testing.T) {
	t.Parallel()

	metrics := NewMetrics(newTestClock())
	telemetry := &Telemetry{Metrics: metrics}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("synthetic failure without a reporter")
	})
	mux.HandleFunc("GET /fine", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	handler := telemetry.HTTPMiddleware(mux)

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("the panic must still be re-thrown with no reporter")
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	}()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/fine", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	rendered := metrics.Render()
	if !strings.Contains(rendered, `http_requests_total{method="GET",route="/boom",status="500"} 1`) {
		t.Errorf("the unreported failure was not counted:\n%s", rendered)
	}
	if !strings.Contains(rendered, `http_requests_total{method="GET",route="/fine",status="200"} 1`) {
		t.Errorf("the healthy request was not counted:\n%s", rendered)
	}

	bare := (&Telemetry{}).HTTPMiddleware(mux)
	bareRecorder := httptest.NewRecorder()
	bare.ServeHTTP(bareRecorder, httptest.NewRequest(http.MethodGet, "/fine", nil))
	if bareRecorder.Code != http.StatusOK {
		t.Fatalf("bare telemetry status = %d, want 200", bareRecorder.Code)
	}
}
