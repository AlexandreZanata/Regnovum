package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of versioned HTTP routes exposed by the identity module (P04-T08).
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodPost, Path: "/api/v1/auth/register"},
		{Method: http.MethodGet, Path: "/api/v1/auth/verify"},
		{Method: http.MethodPost, Path: "/api/v1/auth/login"},
		{Method: http.MethodPost, Path: "/api/v1/auth/logout"},
		{Method: http.MethodPost, Path: "/api/v1/auth/password-reset/request"},
		{Method: http.MethodGet, Path: "/api/v1/auth/password-reset"},
		{Method: http.MethodPost, Path: "/api/v1/auth/password-reset/confirm"},
		// The second factor of the administrative surface (P16-T05). The
		// enrollment pair is open to any authenticated account — an operator
		// must be able to hold a factor before holding a role — while the
		// step-up pair is what an administrative action demands.
		{Method: http.MethodPost, Path: "/api/v1/me/mfa/enrollment"},
		{Method: http.MethodPost, Path: "/api/v1/me/mfa/enrollment/confirm"},
		{Method: http.MethodPost, Path: "/api/v1/me/mfa/step-up"},
		{Method: http.MethodPost, Path: "/api/v1/me/mfa/recovery"},
		// The owner's own device list and the critical transition of a session
		// (P16-T06). The second route is a POST and not a DELETE because it is
		// one endpoint for one decision with a body: the password that
		// re-authenticates the owner.
		{Method: http.MethodGet, Path: "/api/v1/me/sessions"},
		{Method: http.MethodPost, Path: "/api/v1/me/sessions/revocation"},
		{Method: http.MethodPost, Path: "/api/v1/me/sessions/rotation"},
	}
}
