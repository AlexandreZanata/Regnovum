package html

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func init() {
	httpserver.RegisterRouteProvider(Routes)
}

// Routes returns the canonical list of HTML routes exposed by the identity
// module (P18-T05): the browser journey of the account. Like the Arena
// document, it stays outside /api/v1 because it serves documents to a person,
// not the JSON API to a program.
//
// The list is the single source of truth the mux is built from and the route
// registry is compared against, so a page that is mounted and not declared —
// or declared and not mounted — fails the contract gate.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodGet, Path: "/register"},
		{Method: http.MethodPost, Path: "/register"},
		{Method: http.MethodGet, Path: "/verify"},
		{Method: http.MethodPost, Path: "/verify"},
		{Method: http.MethodGet, Path: "/login"},
		{Method: http.MethodPost, Path: "/login"},
		{Method: http.MethodGet, Path: "/logout"},
		{Method: http.MethodPost, Path: "/logout"},
		{Method: http.MethodGet, Path: "/reset"},
		{Method: http.MethodPost, Path: "/reset"},
		{Method: http.MethodGet, Path: "/reset/confirm"},
		{Method: http.MethodPost, Path: "/reset/confirm"},
	}
}
