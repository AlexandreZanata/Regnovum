package locale

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

func TestNegotiatedTagsQualityWeights(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   []Tag
	}{
		{name: "empty", header: "", want: nil},
		{name: "blank", header: "   ", want: nil},
		{name: "wildcard only", header: "*", want: nil},
		{name: "wildcard with ranges", header: "*, pt-BR;q=0.8", want: []Tag{"pt-BR"}},
		{name: "simple", header: "pt-BR,en-US", want: []Tag{"pt-BR", "en-US"}},
		{name: "weights reorder", header: "en-US;q=0.5, pt-BR;q=0.9", want: []Tag{"pt-BR", "en-US"}},
		{name: "implicit q=1 beats lower", header: "en-US;q=0.9, pt-BR", want: []Tag{"pt-BR", "en-US"}},
		{name: "tie keeps header order", header: "en-US, pt-BR;q=1.0", want: []Tag{"en-US", "pt-BR"}},
		{name: "zero excludes", header: "pt-BR;q=0, en-US", want: []Tag{"en-US"}},
		{name: "malformed qvalue ignored member", header: "pt-BR;q=banana, en-US", want: []Tag{"en-US"}},
		{name: "q=1.0 canonical", header: "pt-BR;q=1.0", want: []Tag{"pt-BR"}},
		{name: "q=0.5", header: "pt-BR;q=0.5", want: []Tag{"pt-BR"}},
		{name: "out of range rejected", header: "pt-BR;q=2", want: nil},
		{name: "garbage ranges skipped", header: "!!;q=1, pt-BR", want: []Tag{"pt-BR"}},
		{name: "canonicalizes case", header: "PT-BR, EN-us;q=0.9", want: []Tag{"pt-BR", "en-US"}},
	}

	for _, test := range tests {
		got := NegotiatedTags(test.header)
		if len(got) != len(test.want) {
			t.Errorf("%s: NegotiatedTags(%q) = %v, want %v", test.name, test.header, got, test.want)
			continue
		}
		for index := range got {
			if got[index] != test.want[index] {
				t.Errorf("%s: NegotiatedTags(%q) = %v, want %v", test.name, test.header, got, test.want)
				break
			}
		}
	}
}

func TestNegotiateFiltersByAllowlist(t *testing.T) {
	t.Parallel()

	if tag, ok := Negotiate("es-ES, pt-BR"); !ok || tag != BrazilianPortuguese {
		t.Errorf("Negotiate with unsupported first = (%q, %v), want pt-BR", tag, ok)
	}
	if _, ok := Negotiate("es-ES, fr-FR"); ok {
		t.Error("Negotiate with only unsupported locales must miss")
	}
}

func TestResolverPrecedence(t *testing.T) {
	t.Parallel()

	request := func(cookie Tag, acceptLanguage string) *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		if cookie != "" {
			request.AddCookie(&http.Cookie{Name: CookieName, Value: string(cookie)})
		}
		if acceptLanguage != "" {
			request.Header.Set("Accept-Language", acceptLanguage)
		}
		return request
	}

	t.Run("profile beats cookie", func(t *testing.T) {
		resolver := NewResolver(WithProfileSource(func(*http.Request) (Tag, bool) {
			return AmericanEnglish, true
		}))
		got := resolver.Resolve(request(BrazilianPortuguese, "pt-BR"))
		if got != AmericanEnglish {
			t.Errorf("profile precedence: got %q, want en-US", got)
		}
	})

	t.Run("profile miss falls through to cookie", func(t *testing.T) {
		resolver := NewResolver(WithProfileSource(func(*http.Request) (Tag, bool) {
			return "es-ES", true // persisted value no longer allowlisted
		}))
		got := resolver.Resolve(request(BrazilianPortuguese, "en-US"))
		if got != BrazilianPortuguese {
			t.Errorf("profile miss: got %q, want pt-BR cookie value", got)
		}
	})

	t.Run("cookie beats Accept-Language", func(t *testing.T) {
		resolver := NewResolver()
		got := resolver.Resolve(request("en-US", "pt-BR"))
		if got != AmericanEnglish {
			t.Errorf("cookie precedence: got %q, want en-US", got)
		}
	})

	t.Run("malicious cookie value falls through to Accept-Language", func(t *testing.T) {
		resolver := NewResolver()
		got := resolver.Resolve(request("pt-BR; domain=.evil.com", "en-US"))
		if got != AmericanEnglish {
			t.Errorf("malicious cookie: got %q, want en-US", got)
		}
	})

	t.Run("unknown cookie locale falls through", func(t *testing.T) {
		resolver := NewResolver()
		got := resolver.Resolve(request(Tag("xx-XX"), "pt-BR"))
		if got != BrazilianPortuguese {
			t.Errorf("unknown cookie: got %q, want pt-BR", got)
		}
	})

	t.Run("Accept-Language beats default", func(t *testing.T) {
		resolver := NewResolver()
		got := resolver.Resolve(request("", "es-ES, pt-BR;q=0.9"))
		if got != BrazilianPortuguese {
			t.Errorf("accept-language: got %q, want pt-BR", got)
		}
	})

	t.Run("default when nothing matches", func(t *testing.T) {
		resolver := NewResolver()
		if got := resolver.Resolve(request("", "es-ES")); got != Default() {
			t.Errorf("no match: got %q, want default", got)
		}
		if got := resolver.Resolve(request("", "")); got != Default() {
			t.Errorf("no header: got %q, want default", got)
		}
	})

	t.Run("unknown custom default is ignored", func(t *testing.T) {
		resolver := NewResolver(WithDefault("es-ES"))
		if got := resolver.Resolve(request("", "")); got != BrazilianPortuguese {
			t.Errorf("WithDefault(es-ES) must be ignored, got %q", got)
		}
	})
}

func TestMiddlewareEchoesHeaderAndStoresContext(t *testing.T) {
	t.Parallel()

	resolver := NewResolver()
	var seen Tag
	handler := SetHandler(resolver, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = FromContext(request.Context())
		writer.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	request.Header.Set("Accept-Language", "en-US")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get(InterfaceLocaleHeader); got != "en-US" {
		t.Errorf("X-Interface-Locale = %q, want en-US", got)
	}
	if seen != AmericanEnglish {
		t.Errorf("context locale = %q, want en-US", seen)
	}
}

// TestFallbackMetricCountsOnlyTrueFallbacks proves the metric: requests
// with an explicit signal never count; header-less requests do.
func TestFallbackMetricCountsOnlyTrueFallbacks(t *testing.T) {
	t.Parallel()

	resolver := NewResolver()
	handler := SetHandler(resolver, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))

	// Requests with explicit signals.
	for _, header := range []string{"pt-BR", "en-US;q=0.8, pt-BR;q=0.9"} {
		request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		request.Header.Set("Accept-Language", header)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	request.AddCookie(&http.Cookie{Name: CookieName, Value: "en-US"})
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if got := FallbackCount(); got != 0 {
		t.Fatalf("FallbackCount with explicit signals = %d, want 0", got)
	}

	// Requests without any signal (fallback path).
	before := FallbackCount()
	const fallbacks = 3
	var wait sync.WaitGroup
	for i := 0; i < fallbacks; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health/live", nil))
		}()
	}
	wait.Wait()

	if got := FallbackCount() - before; got != fallbacks {
		t.Errorf("FallbackCount increment = %d, want %d", got, fallbacks)
	}
}

// TestSupportedMatchesCatalog ties the allowlist to the generated catalog.
func TestSupportedMatchesCatalog(t *testing.T) {
	t.Parallel()

	locales := i18n.SupportedLocales()
	if len(locales) < 2 {
		t.Fatalf("catalog must have at least two locales, got %v", locales)
	}
	for _, name := range locales {
		if !IsSupported(Tag(name)) {
			t.Errorf("catalog locale %s must be supported", name)
		}
	}
}
