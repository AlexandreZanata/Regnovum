package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of public transparency routes (P14-T03,
// P14-T04): the versioned JSON metrics document, the HTML transparency
// document and the versioned public Arena export. All are unauthenticated
// and cacheable.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodGet, Path: "/api/v1/public/transparency"},
		{Method: http.MethodGet, Path: "/transparency"},
		{Method: http.MethodGet, Path: "/api/v1/arenas/{id}/export"},
	}
}
