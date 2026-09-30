package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the public PostgreSQL full-text search endpoints.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodGet, Path: "/api/v1/search/arenas"},
		{Method: http.MethodGet, Path: "/api/v1/search/arguments"},
	}
}
