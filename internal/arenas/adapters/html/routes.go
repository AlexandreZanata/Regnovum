package html

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of HTML routes exposed by the arenas
// module: the cacheable public Arena document (P08-T08) and the browser
// participation journey (P18-T06). They stay outside /api/v1 because they serve
// HTML, not the JSON API, and the journey is listed route by route because each
// one is a decision about who may call it.
func Routes() []httpserver.Route {
	routes := make([]httpserver.Route, 0, 6)
	routes = append(routes, httpserver.Route{Method: http.MethodGet, Path: "/d/{slug}"})
	return append(routes, ParticipationRoutes()...)
}

// ParticipationRoutes returns the routes of the browser participation journey
// (P18-T06): the page one Arena shows and the four transitions a person submits
// from it. They are separated from Routes because a composition mounts exactly
// what a surface answers — the cacheable public document of /d/{slug} belongs
// to the read handler, and a surface that declared it would claim a route it
// never serves.
//
// The order is the order of the journey, and the adapter is its only owner: a
// change here is a change to the route registry and to the contract test that
// compares it with api/openapi.json.
func ParticipationRoutes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodGet, Path: "/arenas/{slug}"},
		{Method: http.MethodPost, Path: "/arenas/{slug}/position"},
		{Method: http.MethodPost, Path: "/arenas/{slug}/position/change"},
		{Method: http.MethodPost, Path: "/arenas/{slug}/arguments"},
		{Method: http.MethodPost, Path: "/arenas/{slug}/attributions"},
	}
}
