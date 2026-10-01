// Package http is the staged inbound HTTP adapter of the seasons
// module (P46-T11). It serves the current ACTIVE season, one
// allowlisted season and the allowlisted history over
// /api/v1/me/seasons with JSON documents, problem+json failures and
// private no-store responses.
//
// Staged means unmounted: this package registers nothing on import
// (no init) and no bootstrap wires it. Routes mounts only after the
// P44 gate and the separate holder authorization; until then the
// live router, the published contract and the economy stay
// untouched. Handlers render from the deployment dictionaries and
// the stored lifecycle: translated text never enters a computation
// and dates and ordinals travel as canonical values in both locales.
package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

// Routes returns the canonical list of versioned HTTP routes served
// by the seasons module once mounted: current season, one season
// and history. Genesis, closing and archiving keep no public
// endpoints beyond these three reads.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodGet, Path: "/api/v1/me/seasons/current"},
		{Method: http.MethodGet, Path: "/api/v1/me/seasons/history"},
		{Method: http.MethodGet, Path: "/api/v1/me/seasons/{season_key}"},
	}
}
