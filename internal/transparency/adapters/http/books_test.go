package http_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestPublicBooksVaryByLanguageNotByUser proves the public books
// never mix languages or users in cache: both locales render,
// bodies differ, every representation varies on the locale, and
// a request carrying a cookie still receives a cacheable
// document that sets no cookie back.
func TestPublicBooksVaryByLanguageNotByUser(t *testing.T) {
	mux, _ := setupTransparencyHarness(t)

	portuguese := httptest.NewRecorder()
	mux.ServeHTTP(portuguese, httptest.NewRequest(http.MethodGet, "/transparency", nil))
	if portuguese.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", portuguese.Code)
	}
	english := httptest.NewRecorder()
	mux.ServeHTTP(english, httptest.NewRequest(http.MethodGet, "/transparency?locale=en-US", nil))
	if english.Code != http.StatusOK {
		t.Fatalf("english status = %d, want 200", english.Code)
	}
	if portuguese.Body.String() == english.Body.String() {
		t.Fatal("both locales rendered the same body: one of them is not localized")
	}
	for name, recorder := range map[string]*httptest.ResponseRecorder{"pt-BR": portuguese, "en-US": english} {
		if vary := recorder.Header().Get("Vary"); !strings.Contains(vary, "Accept-Language") {
			t.Fatalf("%s Vary = %q, want the language dimension", name, vary)
		}
		assertPublicCache(t, recorder)
		if etag := recorder.Header().Get("ETag"); etag == "" {
			t.Fatalf("%s carries no validator", name)
		}
	}
	if portuguese.Header().Get("ETag") == english.Header().Get("ETag") {
		t.Fatal("both locales share one validator: representations must not share a cache entry")
	}

	withCookie := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/transparency", nil)
	request.Header.Set("Cookie", "session=somebody-elses-session")
	mux.ServeHTTP(withCookie, request)
	if withCookie.Code != http.StatusOK {
		t.Fatalf("cookie status = %d, want 200", withCookie.Code)
	}
	assertPublicCache(t, withCookie)
	if setCookie := withCookie.Header().Get("Set-Cookie"); setCookie != "" {
		t.Fatalf("Set-Cookie = %q, want none on public books", setCookie)
	}
	if withCookie.Body.String() != portuguese.Body.String() {
		t.Fatal("a cookie changed the public body: user state leaked into a shared representation")
	}
}

// TestPublicBooksExposeNeitherProofNorBalance proves public
// books carry aggregates, never evidence: no OpenGraph tags, no
// sealed digests and no per-record proof or balance markers in
// either locale or in the JSON books.
func TestPublicBooksExposeNeitherProofNorBalance(t *testing.T) {
	mux, _ := setupTransparencyHarness(t)
	hexDigest := regexp.MustCompile(`[0-9a-f]{64}`)

	for _, target := range []string{"/transparency", "/transparency?locale=en-US"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", target, recorder.Code)
		}
		body := recorder.Body.String()
		if strings.Contains(body, "property=\"og:") {
			t.Fatalf("%s exposes OpenGraph tags: %.200s", target, body)
		}
		if hexDigest.MatchString(body) {
			t.Fatalf("%s exposes a sealed digest", target)
		}
		for _, marker := range []string{"prova", "digest", "saldo", "milliINK", "cus_", "sub_", "attributor", "position\":", "@"} {
			if strings.Contains(body, marker) {
				t.Fatalf("%s leaks marker %q", target, marker)
			}
		}
	}

	jsonRecorder := httptest.NewRecorder()
	mux.ServeHTTP(jsonRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/public/transparency", nil))
	if jsonRecorder.Code != http.StatusOK {
		t.Fatalf("json status = %d, want 200", jsonRecorder.Code)
	}
	serialized := jsonRecorder.Body.String()
	for _, marker := range []string{"digest", "proof", "balance", "wealth", "spendable", "cus_", "attributor"} {
		if strings.Contains(serialized, marker) {
			t.Fatalf("json books leak marker %q", marker)
		}
	}
}

// TestHistoricMetricsNeverReadAsSpendableWealth proves the books
// frame themselves as history: a methodology version with a
// closed UTC period, aggregate codes only, and no wealth,
// spendable or per-account balance anywhere.
func TestHistoricMetricsNeverReadAsSpendableWealth(t *testing.T) {
	mux, _ := setupTransparencyHarness(t)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/public/transparency", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	document := decodeTransparencyJSON(t, recorder.Body.Bytes())
	if document["methodology_version"] != float64(1) || document["timezone"] != "UTC" {
		t.Fatalf("envelope = %v, want a versioned UTC history frame", document)
	}
	metrics, _ := document["metrics"].(map[string]any)
	if len(metrics) == 0 {
		t.Fatal("metrics are empty: a book with no aggregates frames nothing")
	}
	for key, value := range metrics {
		count, ok := value.(float64)
		if !ok || count < 0 {
			t.Fatalf("metric %q = %v, want a non-negative aggregate count", key, value)
		}
		for _, lure := range []string{"wealth", "spendable", "balance", "account_balance", "digest"} {
			if strings.Contains(key, lure) {
				t.Fatalf("metric %q reads as spendable wealth", key)
			}
		}
	}
	topKeys := map[string]bool{}
	for key := range document {
		topKeys[key] = true
	}
	for _, key := range []string{"methodology_version", "period_start", "period_end", "timezone", "updated_at", "metrics"} {
		if !topKeys[key] {
			t.Fatalf("envelope is missing %q", key)
		}
		delete(topKeys, key)
	}
	for key := range topKeys {
		t.Fatalf("envelope carries unexpected %q: history frames aggregates, never accounts", key)
	}
}
