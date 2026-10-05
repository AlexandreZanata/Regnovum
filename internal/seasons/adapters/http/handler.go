package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	seasonapp "github.com/AlexandreZanata/Regnovum/internal/seasons/application"
	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

// HandlerConfig aggregates the season lifecycle reads required to
// serve the staged seasons API.
type HandlerConfig struct {
	Reads     seasonapp.SeasonReads
	Champions ChampionsReader
	Security  *security.Manager
}

// Handler serves the staged versioned seasons API.
type Handler struct {
	reads     seasonapp.SeasonReads
	champions ChampionsReader
	security  *security.Manager
}

// NewHandler constructs a seasons HTTP handler, refusing incomplete
// composition.
func NewHandler(cfg HandlerConfig) (*Handler, error) {
	if cfg.Reads == nil {
		return nil, apperr.New(apperr.KindInternal, "seasons_misconfigured", "seasons handler needs reads")
	}
	return &Handler{reads: cfg.Reads, champions: cfg.Champions, security: cfg.Security}, nil
}

// RegisterRoutes mounts the staged seasons routes on mux without
// touching the process registry: the caller owns the mount, which
// happens only after the activation gate.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/me/seasons/current", h.private(http.HandlerFunc(h.getCurrent)))
	mux.Handle("GET /api/v1/me/seasons/history", h.private(http.HandlerFunc(h.getHistory)))
	mux.Handle("GET /api/v1/me/seasons/{season_key}", h.private(http.HandlerFunc(h.getSeason)))
	mux.Handle("GET /api/v1/me/seasons/{season_key}/champions", h.private(http.HandlerFunc(h.getChampions)))
}

// private applies the authentication requirement when a security
// manager is configured.
func (h *Handler) private(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}

// identity extracts the authenticated account id.
func (h *Handler) identity(r *http.Request) (string, bool) {
	identity, ok := security.FromContext(r.Context())
	if !ok {
		return "", false
	}
	return identity.AccountID, true
}

// titles selects the season title dictionary by Accept-Language:
// Portuguese or English. Dates and ordinals travel untouched either
// way; the locale only renders the surrounding words.
func titles(r *http.Request) seasondomain.SeasonTitles {
	if locale.PrefersPortuguese(r.Header.Get("Accept-Language")) {
		return seasondomain.SeasonTitlesFor(seasondomain.SeasonLocalePortuguese)
	}
	return seasondomain.SeasonTitlesFor(seasondomain.SeasonLocaleEnglish)
}

// writeJSON stores one private document: no-store on every seasons
// response, canonical integer ordinals, UTC instants.
func writeJSON(w http.ResponseWriter, status int, document any) {
	httpcache.NoStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(document)
}

// fail maps season read failures to stable problem+json answers:
// inactive books to 403 mismatch, suspended reads to 409 closed or
// archived, unknown books to 404. Failure documents are private
// no-store like every seasons answer.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	httpcache.NoStore(w)
	var domainErr seasondomain.DomainError
	if errors.As(err, &domainErr) {
		switch domainErr.Code {
		case seasondomain.CodeSeasonMismatch:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "season_mismatch", "the compatibility namespace is explicitly inactive"))
			return
		case seasondomain.CodeSeasonClosed:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "season_closed", "no active season: closing is still draining or the service is suspended"))
			return
		case seasondomain.CodeSeasonArchived:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "season_archived", "predecessor archived: the successor is not open yet"))
			return
		case seasondomain.CodeSeasonUnknown:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "season_unknown", "no such season for this account"))
			return
		}
	}
	_ = httperror.WriteProblem(w, r, err)
}

// deny answers authentication failures as private no-store problems.
func deny(w http.ResponseWriter, r *http.Request, err error) {
	httpcache.NoStore(w)
	_ = httperror.WriteProblem(w, r, err)
}

// iso renders one instant in canonical UTC text.
func iso(instant time.Time) string {
	return instant.UTC().Format(time.RFC3339)
}
