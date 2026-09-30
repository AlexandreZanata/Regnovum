package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of versioned HTTP routes exposed by the
// moderation module (P13-T07): user report/appeal filing and the restricted
// case queue/action surface. Sanction application and appeal review happen
// only through these audited use cases.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodPost, Path: "/api/v1/me/moderation/reports"},
		{Method: http.MethodPost, Path: "/api/v1/me/moderation/appeals"},
		{Method: http.MethodGet, Path: "/api/v1/moderation/cases"},
		{Method: http.MethodPost, Path: "/api/v1/moderation/cases/{id}/claim"},
		{Method: http.MethodPost, Path: "/api/v1/moderation/cases/{id}/decisions"},
	}
}
