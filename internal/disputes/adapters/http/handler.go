package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	disputesapp "github.com/AlexandreZanata/Regnovum/internal/disputes/application"
	disputesdomain "github.com/AlexandreZanata/Regnovum/internal/disputes/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// maxCaseBodyBytes bounds acceptance, defense and appeal request
// bodies: digests and reasons travel in hundreds of runes, never
// megabytes.
const maxCaseBodyBytes = 4096

// HandlerConfig aggregates the lifecycle use cases, the clock and
// the security manager required to serve the staged disputes API.
// No verdict, amount, vault or duration is hardcoded in a handler.
type HandlerConfig struct {
	Accept   *disputesapp.AcceptCaseUseCase
	Defend   *disputesapp.DefendCaseUseCase
	Appeal   *disputesapp.AppealCaseUseCase
	Read     *disputesapp.ReadCaseFileUseCase
	Clock    ports.Clock
	Security *security.Manager
	Logger   func(entry string)
}

// Handler serves the staged versioned disputes API.
type Handler struct {
	accept   *disputesapp.AcceptCaseUseCase
	defend   *disputesapp.DefendCaseUseCase
	appeal   *disputesapp.AppealCaseUseCase
	read     *disputesapp.ReadCaseFileUseCase
	clock    ports.Clock
	security *security.Manager
	logger   func(entry string)
}

// NewHandler constructs a disputes HTTP handler, refusing incomplete
// composition.
func NewHandler(cfg HandlerConfig) (*Handler, error) {
	if cfg.Accept == nil || cfg.Defend == nil || cfg.Appeal == nil || cfg.Read == nil || cfg.Clock == nil {
		return nil, apperr.New(apperr.KindInternal, "disputes_misconfigured", "disputes handler needs lifecycle use cases and clock")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = func(string) {}
	}
	return &Handler{
		accept: cfg.Accept, defend: cfg.Defend, appeal: cfg.Appeal, read: cfg.Read,
		clock: cfg.Clock, security: cfg.Security, logger: logger,
	}, nil
}

// RegisterRoutes mounts the staged disputes routes on mux without
// touching the process registry: the caller owns the mount, which
// happens only after the activation gate.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/me/disputes/cases/{key}", h.private(http.HandlerFunc(h.getCaseFile)))
	mux.Handle("POST /api/v1/me/disputes/cases/{key}/accepts", h.private(h.csrf(http.HandlerFunc(h.postAccept))))
	mux.Handle("POST /api/v1/me/disputes/cases/{key}/defenses", h.private(h.csrf(http.HandlerFunc(h.postDefense))))
	mux.Handle("GET /api/v1/me/disputes/cases/{key}/ruling", h.private(http.HandlerFunc(h.getRuling)))
	mux.Handle("POST /api/v1/me/disputes/cases/{key}/appeals", h.private(h.csrf(http.HandlerFunc(h.postAppeal))))
}

// private applies the authentication requirement when a security
// manager is configured.
func (h *Handler) private(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}

// csrf applies the double-submit CSRF check to mutations when a
// security manager is configured. Safe reads never mutate state and
// never fail on CSRF.
func (h *Handler) csrf(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.CSRFMiddleware()(next)
}

// identity extracts the authenticated account id.
func (h *Handler) identity(r *http.Request) (string, bool) {
	identity, ok := security.FromContext(r.Context())
	if !ok {
		return "", false
	}
	return identity.AccountID, true
}

// titles selects the case title dictionary by Accept-Language:
// Portuguese or English. Versions, amounts and instants travel
// untouched either way; the locale only renders the surrounding
// words.
func titles(r *http.Request) disputesdomain.NoticeTitles {
	tag, ok := locale.Negotiate(r.Header.Get("Accept-Language"))
	if !ok {
		tag = locale.Default()
	}
	if strings.HasPrefix(string(tag), "pt") {
		return disputesdomain.NoticeTitlesFor(disputesdomain.NoticeLocalePortuguese)
	}
	return disputesdomain.NoticeTitlesFor(disputesdomain.NoticeLocaleEnglish)
}

// log records one served request with method, route, status and case
// key only: proof digests, grounds, objects and accounts never reach
// a log line.
func (h *Handler) log(method, route string, status int, key string) {
	h.logger(method + " " + route + " " + http.StatusText(status) + " key=" + key)
}

// writeJSON stores one private document: no-store on every disputes
// response, canonical integers, UTC instants.
func writeJSON(w http.ResponseWriter, status int, document any) {
	httpcache.NoStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(document)
}

// conflict maps domain refusals to stable problem+json answers with
// the caller-chosen stranger status: reads answer absence (404) so
// third parties learn nothing, mutations answer forbidden (403).
func conflict(w http.ResponseWriter, r *http.Request, err error, stranger int) {
	httpcache.NoStore(w)
	var domainErr disputesdomain.DomainError
	if errors.As(err, &domainErr) {
		switch domainErr.Code {
		case disputesdomain.CodeUnknownCase:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "case_unknown", "no such private case for this account"))
			return
		case disputesdomain.CodeCaseNotParty:
			if stranger == http.StatusNotFound {
				_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "case_unknown", "no such private case for this account"))
			} else {
				_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "case_forbidden", "only a named party moves its own case"))
			}
			return
		case disputesdomain.CodeTermsExpired, disputesdomain.CodeLateEvidence,
			disputesdomain.CodeDuplicateAppeal, disputesdomain.CodeSettlementConflict,
			disputesdomain.CodeAlreadyReleased, disputesdomain.CodeNotFinal:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "case_conflict", "the lifecycle refused this move"))
			return
		case disputesdomain.CodeInvalidTerms, disputesdomain.CodeInvalidCase,
			disputesdomain.CodeInvalidRuling, disputesdomain.CodeInvalidSettlement,
			disputesdomain.CodeNoEscrow, disputesdomain.CodeCaseNeedsConsent,
			disputesdomain.CodeMissingDefense, disputesdomain.CodeBeyondContract,
			disputesdomain.CodeTermsNotParty, disputesdomain.CodeRulingConflict:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "case_invalid", "the case request is malformed"))
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

// decodeBody bounds and decodes one mutation body.
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxCaseBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		deny(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON within the size limit"))
		return false
	}
	return true
}

// iso renders one instant in canonical UTC text.
func iso(instant time.Time) string {
	return instant.UTC().Format(time.RFC3339)
}
