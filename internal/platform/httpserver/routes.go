// Route registry (P02-T06): the single source of truth of the routes the
// arena binary registers. The contract test (internal/contract) compares
// this registry against api/openapi.json so route drift fails the build.
//
// net/http's ServeMux does not expose registered patterns, so the mux is
// built from this registry instead of registering routes ad hoc: the
// handler map and the contract are two views of the same list.
package httpserver

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Route is one registered endpoint.
type Route struct {
	// Method is the HTTP method ("GET", "POST", ...).
	Method string

	// Path is the absolute path, for example "/health/live".
	Path string
}

// String renders the canonical "METHOD /path" form.
func (route Route) String() string {
	return route.Method + " " + route.Path
}

var (
	routeProvidersMu sync.Mutex
	routeProviders   []func() []Route
)

// RegisterRouteProvider records a route provider for dynamic module aggregation.
func RegisterRouteProvider(provider func() []Route) {
	routeProvidersMu.Lock()
	defer routeProvidersMu.Unlock()
	routeProviders = append(routeProviders, provider)
}

// HealthRoutes lists the platform health endpoints registered by the
// process. Future route groups (API v1) extend the registry in their own
// package and are aggregated at composition time.
func HealthRoutes() []Route {
	return []Route{
		{Method: http.MethodGet, Path: "/health/live"},
		{Method: http.MethodGet, Path: "/health/ready"},
	}
}

// RegisteredRoutes returns every route the process registers, sorted in
// canonical "METHOD /path" order, so comparisons are deterministic.
func RegisteredRoutes(extra ...[]Route) []Route {
	routeProvidersMu.Lock()
	defer routeProvidersMu.Unlock()

	routes := append([]Route{}, HealthRoutes()...)
	for _, provider := range routeProviders {
		routes = append(routes, provider()...)
	}
	for _, group := range extra {
		routes = append(routes, group...)
	}
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].String() < routes[j].String()
	})
	return routes
}

// ValidateRegistry rejects duplicate or malformed registry entries before
// they can reach the mux or the contract: a typo'd method or a path missing
// the leading slash must fail fast instead of silently diverging from the
// contract.
func ValidateRegistry(routes []Route) error {
	seen := make(map[string]bool, len(routes))
	for _, route := range routes {
		if route.Method == "" {
			return fmt.Errorf("httpserver: route %q has an empty method", route.Path)
		}
		if !strings.HasPrefix(route.Path, "/") {
			return fmt.Errorf("httpserver: route %q must have an absolute path", route.String())
		}
		key := route.String()
		if seen[key] {
			return fmt.Errorf("httpserver: duplicate route %q", key)
		}
		seen[key] = true
	}
	return nil
}

// RegisterAll wires the registered routes into a mux with explicit method
// patterns, so wrong methods answer 405 automatically.
func RegisterAll(mux *http.ServeMux, routes []Route, readyCheckers ...ReadyChecker) error {
	if err := ValidateRegistry(routes); err != nil {
		return err
	}
	for _, route := range routes {
		pattern := route.Method + " " + route.Path
		if route.Path == "/" {
			pattern = route.Method + " /{$}"
		}
		mux.Handle(pattern, handlerFor(route, readyCheckers...))
	}
	return nil
}

// handlerFor returns the handler of a registered route.
func handlerFor(route Route, readyCheckers ...ReadyChecker) http.Handler {
	switch route.String() {
	case "GET /health/live":
		return LiveHandler()
	case "GET /health/ready":
		return ReadyHandler(readyCheckers...)
	default:
		return http.NotFoundHandler()
	}
}
