package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of versioned HTTP routes exposed by the
// profiles module (P05-T04, P14-T05, P14-T06): the separated public and
// private profiles, the authenticated personal data export and the account
// deletion workflow.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodGet, Path: "/api/v1/profiles/{username}"},
		{Method: http.MethodGet, Path: "/api/v1/me/profile"},
		{Method: http.MethodPost, Path: "/api/v1/me/exports"},
		{Method: http.MethodGet, Path: "/api/v1/me/exports/{id}/download"},
		{Method: http.MethodPost, Path: "/api/v1/me/deletion"},
		{Method: http.MethodGet, Path: "/api/v1/me/deletion"},
		{Method: http.MethodPost, Path: "/api/v1/me/deletion/cancel"},
	}
}
