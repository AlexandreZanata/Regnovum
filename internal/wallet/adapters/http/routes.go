package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of versioned HTTP routes exposed by the
// wallet module (P06-T08): authenticated read-only balance and statement.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodGet, Path: "/api/v1/me/wallet"},
		{Method: http.MethodGet, Path: "/api/v1/me/wallet/transactions"},
	}
}
