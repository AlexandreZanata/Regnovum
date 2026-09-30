package locale

// Negotiation matrix of P29-T01: every precedence source of
// I18N_STANDARD.md §4 crossed with every signal class, resolved end to
// end through the HTTP middleware.
//
// Rows prove three product promises at once: the winner of the
// route/profile/cookie/header/default chain, the echo of the resolved
// locale in X-Interface-Locale, and that identifiers traveling in the
// path and query never participate in the negotiation (the Arena content
// language is an explicit domain input parsed by
// internal/arenas/domain.ParseLanguage, never a function of this
// resolution).
//
// There are no localized route prefixes at this stage, so the route level
// is covered as explicitly inapplicable: a locale-looking path resolves
// exactly like the same request on a neutral path.
//
// Rows that end on the default with no usable signal resolve through the
// pure resolver instead of the middleware: the middleware counts true
// fallbacks in a process-global metric owned by another test, and this
// matrix must not move it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

type matrixProfile struct {
	tag Tag
	ok  bool
	set bool
}

func TestNegotiationMatrixEndToEnd(t *testing.T) {
	t.Parallel()

	// The pseudo-locale negotiates exactly when the build carries it;
	// otherwise it is refused at every level like any unknown tag.
	pseudo := Tag(i18n.PseudoLocale)
	headerPseudoWant, headerPseudoMiddleware := BrazilianPortuguese, false
	cookiePseudoWant, cookiePseudoMiddleware := AmericanEnglish, true
	alonePseudoWant, alonePseudoMiddleware := BrazilianPortuguese, false
	if i18n.PseudoEnabled {
		headerPseudoWant, headerPseudoMiddleware = pseudo, true
		cookiePseudoWant, cookiePseudoMiddleware = pseudo, true
		alonePseudoWant, alonePseudoMiddleware = pseudo, true
	}

	tests := []struct {
		name       string
		profile    matrixProfile
		cookie     string
		header     string
		target     string
		want       Tag
		middleware bool
	}{
		{name: "no signals fall back to default", target: "/health/live", want: BrazilianPortuguese},
		{name: "header pt-BR", header: "pt-BR", target: "/health/live", want: BrazilianPortuguese, middleware: true},
		{name: "header en-US", header: "en-US", target: "/health/live", want: AmericanEnglish, middleware: true},
		{name: "header weights reorder", header: "en-US;q=0.5, pt-BR;q=0.9", target: "/health/live", want: BrazilianPortuguese, middleware: true},
		{name: "header tie keeps order", header: "en-US, pt-BR", target: "/health/live", want: AmericanEnglish, middleware: true},
		{name: "header q=0 excludes", header: "pt-BR;q=0, en-US", target: "/health/live", want: AmericanEnglish, middleware: true},
		{name: "header garbage skipped", header: "!!;q=1, en-US", target: "/health/live", want: AmericanEnglish, middleware: true},
		{name: "header only unknown falls back", header: "es-ES, fr-FR", target: "/health/live", want: BrazilianPortuguese},
		{name: "header pseudo resolves only when carried", header: "qps-Ploc, en-US;q=0", target: "/health/live", want: headerPseudoWant, middleware: headerPseudoMiddleware},
		{name: "cookie beats header", cookie: "en-US", header: "pt-BR", target: "/health/live", want: AmericanEnglish, middleware: true},
		{name: "cookie pt-BR beats header en-US", cookie: "pt-BR", header: "en-US", target: "/health/live", want: BrazilianPortuguese, middleware: true},
		{name: "unknown cookie falls through to header", cookie: "xx-XX", header: "en-US", target: "/health/live", want: AmericanEnglish, middleware: true},
		{name: "malicious cookie falls through to header", cookie: "pt-BR; domain=.evil.com", header: "pt-BR", target: "/health/live", want: BrazilianPortuguese, middleware: true},
		{name: "pseudo cookie bows to header unless carried", cookie: "qps-Ploc", header: "en-US", target: "/health/live", want: cookiePseudoWant, middleware: cookiePseudoMiddleware},
		{name: "pseudo cookie alone resolves only when carried", cookie: "qps-Ploc", target: "/health/live", want: alonePseudoWant, middleware: alonePseudoMiddleware},
		{name: "profile beats cookie and header", profile: matrixProfile{tag: AmericanEnglish, ok: true, set: true}, cookie: "pt-BR", header: "pt-BR", target: "/health/live", want: AmericanEnglish, middleware: true},
		{name: "unknown profile value falls through to cookie", profile: matrixProfile{tag: "es-ES", ok: true, set: true}, cookie: "pt-BR", header: "en-US", target: "/health/live", want: BrazilianPortuguese, middleware: true},
		{name: "absent profile falls through to cookie", profile: matrixProfile{set: true}, cookie: "en-US", header: "pt-BR", target: "/health/live", want: AmericanEnglish, middleware: true},
		{name: "locale-looking path never injects", header: "pt-BR", target: "/en-US/health/live", want: BrazilianPortuguese, middleware: true},
		{name: "arena slug and query never participate", header: "en-US", target: "/arenas/viral-slug-123?reveal=1", want: AmericanEnglish, middleware: true},
		{name: "slug path without signals falls back", target: "/arenas/viral-slug-123/arguments/456", want: BrazilianPortuguese},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var resolver *Resolver
			if test.profile.set {
				profile := test.profile
				resolver = NewResolver(WithProfileSource(func(*http.Request) (Tag, bool) {
					return profile.tag, profile.ok
				}))
			} else {
				resolver = NewResolver()
			}

			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: CookieName, Value: test.cookie})
			}
			if test.header != "" {
				request.Header.Set("Accept-Language", test.header)
			}

			if !test.middleware {
				if got := resolver.Resolve(request); got != test.want {
					t.Errorf("resolved locale = %q, want %q", got, test.want)
				}
				return
			}

			var seen Tag
			handler := SetHandler(resolver, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				seen = FromContext(request.Context())
				writer.WriteHeader(http.StatusOK)
			}))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if seen != test.want {
				t.Errorf("resolved locale = %q, want %q", seen, test.want)
			}
			if got := recorder.Header().Get(InterfaceLocaleHeader); got != string(test.want) {
				t.Errorf("%s = %q, want %q", InterfaceLocaleHeader, got, test.want)
			}
		})
	}
}

// TestNegotiationIsDeterministic resolves the same request twice: equal
// inputs always negotiate identically, no matter which sources spoke.
func TestNegotiationIsDeterministic(t *testing.T) {
	t.Parallel()

	resolver := NewResolver(WithProfileSource(func(*http.Request) (Tag, bool) {
		return AmericanEnglish, true
	}))
	request := httptest.NewRequest(http.MethodGet, "/arenas/slug?x=1", nil)
	request.AddCookie(&http.Cookie{Name: CookieName, Value: "pt-BR"})
	request.Header.Set("Accept-Language", "pt-BR, en-US;q=0.9")

	first, second := resolver.Resolve(request), resolver.Resolve(request)
	if first != AmericanEnglish || second != first {
		t.Fatalf("same request resolved to %q then %q, want stable en-US", first, second)
	}
}

// TestLocaleResolutionPreservesRequest proves the standard promise that
// the API never trusts locale signals for authorization, rules or
// identifiers: everything the middleware did not come to resolve crosses
// it byte-identical, and only the locale context value is added.
func TestLocaleResolutionPreservesRequest(t *testing.T) {
	t.Parallel()

	type privateKey struct{}

	resolver := NewResolver()
	outer := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request = request.WithContext(context.WithValue(request.Context(), privateKey{}, "untouched"))
		SetHandler(resolver, http.HandlerFunc(func(writer http.ResponseWriter, inner *http.Request) {
			if got := inner.Header.Get("Authorization"); got != "Bearer secret-token" {
				t.Errorf("Authorization crossed as %q", got)
			}
			if got := inner.Header.Get("X-Custom"); got != "custom-value" {
				t.Errorf("X-Custom crossed as %q", got)
			}
			if got := inner.URL.RequestURI(); got != "/arenas/viral-slug-123?reveal=1" {
				t.Errorf("target crossed as %q", got)
			}
			if got, ok := inner.Context().Value(privateKey{}).(string); !ok || got != "untouched" {
				t.Errorf("foreign context value crossed as %q", got)
			}
			if got := FromContext(inner.Context()); got != AmericanEnglish {
				t.Errorf("resolved locale = %q, want en-US", got)
			}
			writer.WriteHeader(http.StatusOK)
		})).ServeHTTP(writer, request)
	})

	request := httptest.NewRequest(http.MethodGet, "/arenas/viral-slug-123?reveal=1", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("X-Custom", "custom-value")
	request.AddCookie(&http.Cookie{Name: CookieName, Value: "en-US"})
	request.Header.Set("Accept-Language", "pt-BR")

	recorder := httptest.NewRecorder()
	outer.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
}
