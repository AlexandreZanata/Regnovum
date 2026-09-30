package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of versioned HTTP routes exposed by the
// positions module (P09-T06): the authenticated position reads and writes
// under /api/v1/me and the public aggregate under /api/v1/arenas.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodPost, Path: "/api/v1/me/arenas/{id}/position"},
		{Method: http.MethodGet, Path: "/api/v1/me/arenas/{id}/position"},
		{Method: http.MethodPost, Path: "/api/v1/me/arenas/{id}/position/changes"},
		{Method: http.MethodGet, Path: "/api/v1/me/arenas/{id}/position/changes"},
		{Method: http.MethodGet, Path: "/api/v1/arenas/{id}/positions"},
	}
}
