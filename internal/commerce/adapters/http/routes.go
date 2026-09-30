// Package http is the staged inbound HTTP adapter of the commerce
// module (P37-T07). It serves private trade receipts and extracts
// over /api/v1/me/commerce with JSON documents, problem+json
// failures, private no-store responses and authenticated reads.
//
// Staged means unmounted: this package registers nothing on import
// (no init) and no bootstrap wires it. Routes mounts only after the
// P44 gate and the separate holder authorization; until then the
// live router, the published contract and the economy stay
// untouched. Handlers render from the deployment dictionaries and
// the stored receipt: translated text never enters a computation and
// amounts travel as canonical integers in both locales.
package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

// Routes returns the canonical list of versioned HTTP routes served
// by the commerce module once mounted: private receipt and extract.
// Funding, settlement and review keep no public endpoints beyond
// these two reads.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodGet, Path: "/api/v1/me/commerce/contracts/{id}"},
		{Method: http.MethodGet, Path: "/api/v1/me/commerce/statement"},
	}
}
