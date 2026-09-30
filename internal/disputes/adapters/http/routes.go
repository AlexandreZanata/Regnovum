// Package http is the staged inbound HTTP adapter of the disputes
// module (P39-T08). It serves the private case lifecycle over
// /api/v1/me/disputes with JSON documents, problem+json failures,
// private no-store responses, authenticated reads and CSRF-checked
// mutations: proposal, acceptance, defense, ruling and appeal, in
// Portuguese and English.
//
// Staged means unmounted: this package registers nothing on import
// (no init) and no bootstrap wires it. Routes mounts only after the
// P44 gate and the separate holder authorization; until then the
// live router, the published contract and the economy stay
// untouched. Handlers render from the deployment dictionaries and
// the filed case: translated text never enters a computation,
// versions, amounts and instants travel canonical in both locales,
// and proof digests never reach a document or a log line.
package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

// Routes returns the canonical list of versioned HTTP routes served
// by the disputes module once mounted: the private case file, the
// acceptance and defense mutations, the stable ruling read and the
// single appeal. Safety reports keep no endpoint here: moderation
// of risk follows its separate rite.
func Routes() []httpserver.Route {
	return []httpserver.Route{
		{Method: http.MethodGet, Path: "/api/v1/me/disputes/cases/{key}"},
		{Method: http.MethodPost, Path: "/api/v1/me/disputes/cases/{key}/accepts"},
		{Method: http.MethodPost, Path: "/api/v1/me/disputes/cases/{key}/defenses"},
		{Method: http.MethodGet, Path: "/api/v1/me/disputes/cases/{key}/ruling"},
		{Method: http.MethodPost, Path: "/api/v1/me/disputes/cases/{key}/appeals"},
	}
}
