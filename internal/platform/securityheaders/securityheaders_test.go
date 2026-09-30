// Tests of the browser security policy (P16-T01): the header set per
// environment, every class of response that must carry it, the exact policy
// text, and the local scanner that walks the real route registry.
package securityheaders_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// stubIDs satisfies the request ID port of the composition under test.
type stubIDs struct{}

func (stubIDs) NewID() string { return "security-headers-test" }

const (
	contentSecurityPolicyHeader = "Content-Security-Policy"
	contentTypeOptionsHeader    = "X-Content-Type-Options"
	referrerPolicyHeader        = "Referrer-Policy"
	permissionsPolicyHeader     = "Permissions-Policy"
	hstsHeader                  = "Strict-Transport-Security"
)

// policyHeaders are the headers the policy always carries, in a fixed order so
// a failure lists them predictably.
var policyHeaders = []string{
	contentSecurityPolicyHeader,
	contentTypeOptionsHeader,
	referrerPolicyHeader,
	permissionsPolicyHeader,
}

// assertPolicy checks the header set of one response against the environment.
func assertPolicy(t *testing.T, where string, header http.Header, production bool) {
	t.Helper()

	for _, name := range policyHeaders {
		if got := header.Get(name); got == "" {
			t.Errorf("%s: %s is missing", where, name)
		}
	}
	if got := header.Get(contentTypeOptionsHeader); got != "nosniff" {
		t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", where, got)
	}
	// The exact value is load-bearing (P18-T07D): "no-referrer" also
	// serialises the Origin of a form submission as "null", which the CSRF
	// boundary refuses, so no browser could submit a form. "same-origin"
	// keeps the privacy decision — nothing is sent to another origin — and
	// leaves the origin of a same-origin submission readable.
	if got := header.Get(referrerPolicyHeader); got != "same-origin" {
		t.Errorf("%s: Referrer-Policy = %q, want same-origin", where, got)
	}
	if got := header.Get(permissionsPolicyHeader); got == "" {
		t.Errorf("%s: Permissions-Policy is empty", where)
	}
	// The policy is the same value in every environment: an environment that
	// silently weakened it would be a production-only surprise.
	if got := header.Values(contentSecurityPolicyHeader); len(got) != 1 {
		t.Errorf("%s: Content-Security-Policy appears %d times, want exactly 1", where, len(got))
	}
	for _, forbidden := range []string{"'unsafe-inline'", "'unsafe-eval'", "'nonce-", "'strict-dynamic'"} {
		if got := header.Get(contentSecurityPolicyHeader); strings.Contains(got, forbidden) {
			t.Errorf("%s: policy contains %s: %s", where, forbidden, got)
		}
	}

	hsts := header.Get(hstsHeader)
	if production {
		if !strings.Contains(hsts, "max-age=31536000") || !strings.Contains(hsts, "includeSubDomains") {
			t.Errorf("%s: Strict-Transport-Security = %q, want a one-year max-age with includeSubDomains", where, hsts)
		}
		return
	}
	if hsts != "" {
		// Over plain HTTP the header does nothing useful, and a browser that
		// honored it for http://127.0.0.1 would pin the development server to
		// HTTPS until the max-age expired.
		t.Errorf("%s: Strict-Transport-Security = %q, must not be sent outside production", where, hsts)
	}
}

// serve runs one request through the policy middleware over a handler that
// answers with a fixed status.
func serve(t *testing.T, production bool, status int) *httptest.ResponseRecorder {
	t.Helper()

	handler := securityheaders.Middleware(securityheaders.Config{Production: production})(
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(status)
		}),
	)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/anything", nil))
	return recorder
}

// TestPolicyIsAppliedPerEnvironment pins the environment split: HSTS is
// production-only, everything else is always present.
func TestPolicyIsAppliedPerEnvironment(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		name       string
		production bool
	}{
		{name: "development", production: false},
		{name: "production", production: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			recorder := serve(t, scenario.production, http.StatusOK)
			assertPolicy(t, "200 OK", recorder.Header(), scenario.production)

			if !scenario.production {
				if got := recorder.Header().Get(hstsHeader); got != "" {
					t.Fatalf("%s: Strict-Transport-Security = %q", scenario.name, got)
				}
			}
		})
	}
}

// TestPolicyCoversEveryResponseClass fails if any response can lose the
// policy: success, revalidation, a client error, a rejected method and an
// internal error all have to carry it, because the middleware writes the
// headers before the handler runs and the browser stores them with whatever
// it caches.
func TestPolicyCoversEveryResponseClass(t *testing.T) {
	t.Parallel()

	for name, status := range map[string]int{
		"success":             http.StatusOK,
		"not-modified":        http.StatusNotModified,
		"redirect":            http.StatusFound,
		"not-found":           http.StatusNotFound,
		"method-not-allowed":  http.StatusMethodNotAllowed,
		"server-error":        http.StatusInternalServerError,
		"service-unavailable": http.StatusServiceUnavailable,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recorder := serve(t, true, status)
			if recorder.Code != status {
				t.Fatalf("status = %d, want %d", recorder.Code, status)
			}
			assertPolicy(t, name, recorder.Header(), true)
		})
	}
}

// TestPolicyTextIsExactAndForbidsInlineExecution is the policy-change detector.
// Widening the policy has to be a reviewed edit of both the constant and this
// expectation; there is no way to add a source, a scheme or an eval token
// without failing here.
func TestPolicyTextIsExactAndForbidsInlineExecution(t *testing.T) {
	t.Parallel()

	policy := serve(t, false, http.StatusOK).Header().Get(contentSecurityPolicyHeader)

	directives := map[string]string{}
	for _, directive := range strings.Split(policy, ";") {
		directive = strings.TrimSpace(directive)
		if directive == "" {
			continue
		}
		name, value, _ := strings.Cut(directive, " ")
		if _, duplicate := directives[name]; duplicate {
			t.Errorf("directive %q is declared twice", name)
		}
		directives[name] = strings.TrimSpace(value)
	}

	want := map[string]string{
		"default-src":     "'self'",
		"script-src":      "'self'",
		"style-src":       "'self'",
		"img-src":         "'self'",
		"font-src":        "'self'",
		"connect-src":     "'self'",
		"form-action":     "'self'",
		"frame-ancestors": "'none'",
		"base-uri":        "'self'",
		"object-src":      "'none'",
	}
	for name, value := range want {
		if got, ok := directives[name]; !ok {
			t.Errorf("directive %s is missing from the policy", name)
		} else if got != value {
			t.Errorf("directive %s = %q, want %q", name, got, value)
		}
	}
	for name := range directives {
		if _, expected := want[name]; !expected {
			t.Errorf("policy declares %s, which the reviewed policy does not list", name)
		}
	}

	// No wildcard and no scheme source: a policy that allows any origin is
	// not the policy docs/SECURITY.md promises.
	for _, token := range strings.Fields(strings.NewReplacer(";", " ").Replace(policy)) {
		if strings.Contains(token, "*") {
			t.Errorf("policy contains a wildcard source %q", token)
		}
	}
}

// TestEveryRegisteredRouteCarriesThePolicy is the local header scanner: it
// walks the routes the binary actually registers (every module provider, via
// the same registry the contract test compares against OpenAPI) and demands
// the full policy on each of them, in both environments, plus the two
// responses no route owns — an unknown path and a wrong method.
func TestEveryRegisteredRouteCarriesThePolicy(t *testing.T) {
	t.Parallel()

	routes := httpserver.RegisteredRoutes()
	if len(routes) <= len(httpserver.HealthRoutes()) {
		t.Fatalf("registry has %d routes; module providers were not registered, so the scan would be vacuous", len(routes))
	}
	moduleRoutes := 0
	for _, route := range routes {
		if strings.HasPrefix(route.Path, "/api/v1/") {
			moduleRoutes++
		}
	}
	if moduleRoutes == 0 {
		t.Fatal("registry has no /api/v1 route; the scan would not cover the API surface")
	}

	for _, production := range []bool{false, true} {
		environment := "development"
		if production {
			environment = "production"
		}

		handler, err := httpserver.NewMux(stubIDs{}, nil, securityheaders.Config{Production: production})
		if err != nil {
			t.Fatalf("NewMux() error = %v", err)
		}

		for _, route := range routes {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(route.Method, route.Path, nil))
			assertPolicy(t, environment+" "+route.String(), recorder.Header(), production)
		}

		unmatched := []struct {
			method string
			path   string
		}{
			{method: http.MethodGet, path: "/api/v1/not-a-route"},
			{method: http.MethodGet, path: "/not-a-route"},
			{method: http.MethodPost, path: "/health/live"},
		}
		for _, request := range unmatched {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, nil))
			assertPolicy(t, environment+" "+request.method+" "+request.path, recorder.Header(), production)
		}
	}
}
