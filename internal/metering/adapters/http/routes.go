// Package http is the staged inbound HTTP adapter of the metering
// module (P36-T08). It serves cost preview, confirmation, receipt
// and extract over /api/v1/me/metering with JSON documents,
// problem+json failures, private no-store responses and CSRF-checked
// mutations.
//
// Staged means unmounted: this package registers nothing on import
// (no init) and no bootstrap wires it. Routes mounts only after the
// P44 gate and the separate holder authorization; until then the
// live router, the published contract and the economy stay
// untouched, as the no-activation suite proves. Handlers price from
// the deployment catalog and the injected counter: translated text
// never enters a computation.
package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

// Routes returns the canonical list of versioned HTTP routes served
// by the metering module once mounted: priced preview, confirmation,
// receipt and extract. Granting, holding and refunding have no
// public endpoints beyond these four.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodPost, Path: "/api/v1/me/metering/quotes"},
		{Method: http.MethodPost, Path: "/api/v1/me/metering/publications"},
		{Method: http.MethodGet, Path: "/api/v1/me/metering/publications/{id}"},
		{Method: http.MethodGet, Path: "/api/v1/me/metering/statement"},
	}
}
