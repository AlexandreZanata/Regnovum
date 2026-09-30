package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of versioned HTTP routes exposed by the
// jobs module (P15-T06). All three are administrative: they are registered here
// so the contract document, the route registry and the mux cannot drift, and so
// a new operational route is a visible addition to this list.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodGet, Path: "/api/v1/admin/jobs/health"},
		{Method: http.MethodGet, Path: "/api/v1/admin/jobs/dead"},
		{Method: http.MethodPost, Path: "/api/v1/admin/jobs/{id}/retry"},
	}
}
