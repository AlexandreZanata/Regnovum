// Tests of the budget against the real registry (P16-T02): every route the
// binary serves declares a class, and a refusal composed by the real mux still
// carries the request id, the security policy and the router's own answer for
// paths that match no route.
//
// The blank imports register the route providers of every inbound adapter,
// exactly like the contract test does, so this scan walks the surface the
// binary will serve rather than the health routes alone.
package httplimits_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/html"
	_ "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/html"
	_ "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httplimits"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
	_ "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/http"
)

// isReadOnly reports whether a method is one the budget table must leave
// without a body.
func isReadOnly(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// budgetProblems reports every route whose budget is missing or inconsistent
// with its method: a route nobody decided a bound for, a read that accepts a
// body, a mutation bounded in neither size nor depth, a route without a
// deadline.
//
// The resolver is a parameter rather than the shipped function directly so
// that each branch of this scan can be proven to speak: several of them are
// unreachable through the shipped table (an HTTP read can never carry a body
// budget), and an unreachable branch that is never exercised is
// indistinguishable from a dead one.
func budgetProblems(resolve func(method, path string) httplimits.Limits, routes []httpserver.Route) []string {
	var problems []string
	for _, route := range routes {
		limits := resolve(route.Method, route.Path)

		if limits.Class == httplimits.ClassUnrouted {
			problems = append(problems, route.String()+" falls to the unrouted budget")
			continue
		}
		if limits.Timeout <= 0 {
			problems = append(problems, route.String()+" has no deadline")
		}
		if isReadOnly(route.Method) {
			if limits.BodyBytes != 0 {
				problems = append(problems, route.String()+" is a read that accepts a body")
			}
			continue
		}
		if limits.BodyBytes <= 0 {
			problems = append(problems, route.String()+" is a mutation with no body budget")
		}
		if limits.JSONDepth <= 0 {
			problems = append(problems, route.String()+" is a mutation without a JSON depth budget")
		}
	}
	return problems
}

// TestEveryRegisteredRouteDeclaresItsBudget is the completeness proof: a route
// that falls to the unrouted budget is a route nobody decided a bound for, and
// the failure message says what to do about it.
func TestEveryRegisteredRouteDeclaresItsBudget(t *testing.T) {
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

	for _, problem := range budgetProblems(httplimits.Default, routes) {
		t.Errorf("%s: declare the class of the prefix in the httplimits table, or bound the route", problem)
	}

	// Every branch of the scan is proven to speak, so a green scan above means
	// the routes are bounded and not that the scanner is mute.
	const brokenPath = "/api/v1/me/arena-drafts"
	broken := func(method, path string) httplimits.Limits {
		limits := httplimits.Default(method, path)
		if path != brokenPath {
			return limits
		}
		switch method {
		case http.MethodGet:
			limits.BodyBytes = 1 // a read that accepts a body
		case http.MethodPost:
			limits.BodyBytes = 0
			limits.JSONDepth = 0
			limits.Timeout = 0
		}
		return limits
	}
	negative := []httpserver.Route{
		{Method: http.MethodPost, Path: "/api/v1/undeclared-namespace/thing"},
		{Method: http.MethodGet, Path: brokenPath},
		{Method: http.MethodPost, Path: brokenPath},
	}
	if problems := budgetProblems(broken, negative); len(problems) != 5 {
		t.Fatalf("budgetProblems() reported %v, want five problems: an undeclared namespace, a read with a body budget, and a mutation with no size, depth or deadline", problems)
	}
}

// TestCompositionCarriesThePolicyAndTheRequestIDOnRefusal is the ordering
// proof: the bound sits inside the correlation, locale and security layers, so
// even a refused request is traceable and still carries the browser policy.
func TestCompositionCarriesThePolicyAndTheRequestIDOnRefusal(t *testing.T) {
	t.Parallel()

	handler, err := httpserver.NewMux(policyIDs{}, nil, securityheaders.Config{})
	if err != nil {
		t.Fatalf("NewMux() error = %v", err)
	}

	// Take a mutation from the registry so the fixture follows the surface
	// instead of hard-coding a route that may move.
	route := firstWriteRoute(t)
	budget := httplimits.Default(route.Method, route.Path).BodyBytes
	if budget <= 0 {
		t.Fatalf("%s has no body budget", route.String())
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(route.Method, route.Path, strings.NewReader(strings.Repeat("x", int(budget)+1))))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("%s: status = %d, want 400 (body: %s)", route.String(), recorder.Code, recorder.Body.String())
	}
	if code := problemCode(t, recorder); code != httplimits.CodeBodyTooLarge {
		t.Errorf("code = %q, want %q", code, httplimits.CodeBodyTooLarge)
	}
	if got := recorder.Header().Get("X-Request-Id"); got == "" {
		t.Error("the refusal has no request id: the bound must run inside the correlation layer")
	}
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Permissions-Policy"} {
		if got := recorder.Header().Get(header); got == "" {
			t.Errorf("the refusal is missing %s: the bound must run inside the security policy", header)
		}
	}
}

// TestUnknownPathKeepsTheRoutersAnswer pins the deliberate tolerance of the
// unrouted class: a small body on a path that matches no route still lets the
// router answer 404, because the bound is not the router's voice.
func TestUnknownPathKeepsTheRoutersAnswer(t *testing.T) {
	t.Parallel()

	handler, err := httpserver.NewMux(policyIDs{}, nil, securityheaders.Config{})
	if err != nil {
		t.Fatalf("NewMux() error = %v", err)
	}

	small := httptest.NewRecorder()
	handler.ServeHTTP(small, httptest.NewRequest(http.MethodPost, "/api/v1/not-a-route", strings.NewReader(`{"a":1}`)))
	if small.Code != http.StatusNotFound {
		t.Errorf("unknown path with a small body: status = %d, want 404 (body: %s)", small.Code, small.Body.String())
	}

	large := httptest.NewRecorder()
	handler.ServeHTTP(large, httptest.NewRequest(http.MethodPost, "/api/v1/not-a-route", strings.NewReader(strings.Repeat("x", 8<<10))))
	if large.Code != http.StatusBadRequest {
		t.Errorf("unknown path with an oversized body: status = %d, want 400 (body: %s)", large.Code, large.Body.String())
	}
}

// firstWriteRoute returns a mutating route of the registry, preferring a
// concrete path over one with a parameter so the request below names a real
// address.
func firstWriteRoute(t *testing.T) httpserver.Route {
	t.Helper()

	var fallback *httpserver.Route
	for _, route := range httpserver.RegisteredRoutes() {
		if isReadOnly(route.Method) {
			continue
		}
		if !strings.Contains(route.Path, "{") {
			return route
		}
		candidate := route
		fallback = &candidate
	}
	if fallback == nil {
		t.Fatal("registry has no mutating route")
	}
	return *fallback
}

// policyIDs is the deterministic correlation source the mux needs.
type policyIDs struct{}

func (policyIDs) NewID() string { return "httplimits-test-id" }
