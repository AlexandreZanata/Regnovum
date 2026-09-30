package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	meteringapp "github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// maxPreviewBodyBytes bounds quote and publication request bodies:
// candidates are measured in thousands of clusters, never megabytes.
const maxPreviewBodyBytes = 65536

// HandlerConfig aggregates the metering use cases, reads and the
// deployment parameters required to serve the staged metering API.
// Treasury endpoints, unit budget and quote lifetime arrive from the
// deployment: no price, vault or duration is hardcoded in a handler.
type HandlerConfig struct {
	Preview  *meteringapp.PreviewUseCase
	Publish  *meteringapp.PublishUseCase
	Reads    meteringapp.ReceiptsRepository
	Clock    ports.Clock
	Security *security.Manager
	FromKind string
	ToKind   string
	ToLabel  string
	MaxUnits int
	TTL      time.Duration
}

// Handler serves the staged versioned metering API.
type Handler struct {
	preview  *meteringapp.PreviewUseCase
	publish  *meteringapp.PublishUseCase
	reads    meteringapp.ReceiptsRepository
	security *security.Manager
	clock    ports.Clock
	fromKind string
	toKind   string
	toLabel  string
	maxUnits int
	ttl      time.Duration
}

// NewHandler constructs a metering HTTP handler, refusing
// incomplete composition.
func NewHandler(cfg HandlerConfig) (*Handler, error) {
	if cfg.Preview == nil || cfg.Publish == nil || cfg.Reads == nil || cfg.Clock == nil {
		return nil, apperr.New(apperr.KindInternal, "metering_misconfigured", "metering handler needs preview, publish, reads and clock")
	}
	if strings.TrimSpace(cfg.FromKind) == "" || strings.TrimSpace(cfg.ToKind) == "" ||
		strings.TrimSpace(cfg.ToLabel) == "" || cfg.MaxUnits <= 0 || cfg.TTL <= 0 {
		return nil, apperr.New(apperr.KindInternal, "metering_misconfigured", "metering handler needs endpoints, unit budget and lifetime")
	}
	return &Handler{
		preview: cfg.Preview, publish: cfg.Publish, reads: cfg.Reads,
		security: cfg.Security, clock: cfg.Clock,
		fromKind: strings.TrimSpace(cfg.FromKind),
		toKind:   strings.TrimSpace(cfg.ToKind),
		toLabel:  strings.TrimSpace(cfg.ToLabel),
		maxUnits: cfg.MaxUnits, ttl: cfg.TTL,
	}, nil
}

// RegisterRoutes mounts the staged metering routes on mux without
// touching the process registry: the caller owns the mount, which
// happens only after the activation gate.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/me/metering/quotes", h.private(h.csrf(http.HandlerFunc(h.previewQuote))))
	mux.Handle("POST /api/v1/me/metering/publications", h.private(h.csrf(http.HandlerFunc(h.confirmPublication))))
	mux.Handle("GET /api/v1/me/metering/publications/{id}", h.private(http.HandlerFunc(h.getReceipt)))
	mux.Handle("GET /api/v1/me/metering/statement", h.private(http.HandlerFunc(h.getStatement)))
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

// titles selects the price title dictionary by Accept-Language:
// Portuguese or English. Amounts travel untouched either way; the
// locale only renders the surrounding words.
func titles(r *http.Request) meteringdomain.PriceTitles {
	tag, ok := locale.Negotiate(r.Header.Get("Accept-Language"))
	if !ok {
		tag = locale.Default()
	}
	if strings.HasPrefix(string(tag), "pt") {
		return meteringdomain.PriceTitlesFor(meteringdomain.PriceLocalePortuguese)
	}
	return meteringdomain.PriceTitlesFor(meteringdomain.PriceLocaleEnglish)
}

// writeJSON stores one private document: no-store on every metering
// response, canonical integer amounts, UTC instants.
func writeJSON(w http.ResponseWriter, status int, document any) {
	httpcache.NoStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(document)
}

// fail maps metering failures to stable problem+json answers:
// validation to 400, account mismatch to 403, key reuse with
// divergent terms to 409, foreign or missing rows to 404. Failure
// documents are private no-store like every metering answer.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	httpcache.NoStore(w)
	var domainErr meteringdomain.DomainError
	if errors.As(err, &domainErr) {
		switch domainErr.Code {
		case meteringdomain.CodeQuoteMismatch:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "quote_mismatch", "the acceptance binds another account or content"))
			return
		case meteringdomain.CodePublishConflict:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "publish_conflict", "the key already settled different terms"))
			return
		case meteringdomain.CodePriceNotFound:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "price_unavailable", "no approved price covers this service and instant"))
			return
		case meteringdomain.CodeUnknownPublication:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "publication_unknown", "no such publication for this account"))
			return
		case meteringdomain.CodeQuoteExpired:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "quote_expired", "the acceptance lapsed and needs a new quote"))
			return
		}
	}
	_ = httperror.WriteProblem(w, r, err)
}

// deny answers authentication and body failures as private
// no-store problems.
func deny(w http.ResponseWriter, r *http.Request, err error) {
	httpcache.NoStore(w)
	_ = httperror.WriteProblem(w, r, err)
}
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPreviewBodyBytes)
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
