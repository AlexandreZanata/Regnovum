package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of versioned HTTP routes exposed by the
// persuasion module (P11-T06, P11-T07): the authenticated attribution
// recording under /api/v1/me, the public count reads per argument and per
// profile under /api/v1, and the restricted moderator-only abuse signals
// under /api/v1/moderation. No route ever lists attributors publicly and no
// public route ever exposes a signal.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodPost, Path: "/api/v1/me/position-changes/{id}/attributions"},
		{Method: http.MethodGet, Path: "/api/v1/arguments/{id}/attributions"},
		{Method: http.MethodGet, Path: "/api/v1/profiles/{username}/reputation"},
		{Method: http.MethodGet, Path: "/api/v1/moderation/attribution-signals/{authorID}"},
	}
}
