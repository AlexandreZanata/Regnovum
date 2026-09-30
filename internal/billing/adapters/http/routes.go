package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of versioned HTTP routes exposed by the
// billing module (P07-T06, P12-T10): authenticated read-only pass summary
// and consumption history plus the private billing API (checkout,
// subscription status and portal). Granting, consuming and reconciling have
// no public endpoints beyond these.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodGet, Path: "/api/v1/me/passes"},
		{Method: http.MethodGet, Path: "/api/v1/me/passes/history"},
		{Method: http.MethodPost, Path: "/api/v1/me/billing/checkout"},
		{Method: http.MethodGet, Path: "/api/v1/me/billing/subscription"},
		{Method: http.MethodPost, Path: "/api/v1/me/billing/portal"},
	}
}
