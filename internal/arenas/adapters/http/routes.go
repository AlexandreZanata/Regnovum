package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of versioned HTTP routes exposed by the
// arenas module (P08-T07): the authenticated draft CRUD, publication and
// closing, plus the public feed and the public Arena document. Moderation
// stays internal: no public route restricts or removes an Arena.
//
// Drafts live in their own collection, /api/v1/me/arena-drafts, and not nested
// under /api/v1/me/arenas. The segment after /me/arenas belongs to the
// per-Arena subresource namespace ({id}/close, {id}/position,
// {id}/arguments), so a literal collection name in that same position is
// ambiguous with the wildcard: `/me/arenas/drafts/position` matches both
// `.../drafts/{id}` and `.../{id}/position`, and net/http refuses to mount two
// patterns that ambiguous, which means the two collections could never be
// served by one binary (P16-T01 found this with the route scanner).
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodPost, Path: "/api/v1/me/arena-drafts"},
		{Method: http.MethodGet, Path: "/api/v1/me/arena-drafts"},
		{Method: http.MethodGet, Path: "/api/v1/me/arena-drafts/{id}"},
		{Method: http.MethodPatch, Path: "/api/v1/me/arena-drafts/{id}"},
		{Method: http.MethodDelete, Path: "/api/v1/me/arena-drafts/{id}"},
		{Method: http.MethodPost, Path: "/api/v1/me/arena-drafts/{id}/publish"},
		{Method: http.MethodPost, Path: "/api/v1/me/arenas/{id}/close"},
		{Method: http.MethodGet, Path: "/api/v1/arenas"},
		{Method: http.MethodGet, Path: "/api/v1/arenas/{slug}"},
	}
}
