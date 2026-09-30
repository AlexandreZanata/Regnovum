package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of versioned HTTP routes exposed by the
// arguments module (P10-T08): authenticated publication, replies and
// withdrawal under /api/v1/me, plus the public list, replies and get under
// /api/v1.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodPost, Path: "/api/v1/me/arenas/{id}/arguments"},
		{Method: http.MethodPost, Path: "/api/v1/me/arenas/{id}/arguments/{argumentID}/replies"},
		{Method: http.MethodPost, Path: "/api/v1/me/arguments/{id}/withdraw"},
		{Method: http.MethodGet, Path: "/api/v1/arenas/{id}/arguments"},
		{Method: http.MethodGet, Path: "/api/v1/arguments/{id}/replies"},
		{Method: http.MethodGet, Path: "/api/v1/arguments/{id}"},
	}
}
