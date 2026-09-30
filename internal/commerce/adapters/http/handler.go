package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	commerceapp "github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// HandlerConfig aggregates the commerce reads required to serve the
// staged trade API. No amount, vault or duration is hardcoded in a
// handler.
type HandlerConfig struct {
	Reads    commerceapp.TradeReceiptsRepository
	Security *security.Manager
}

// Handler serves the staged versioned commerce API.
type Handler struct {
	reads    commerceapp.TradeReceiptsRepository
	security *security.Manager
}

// NewHandler constructs a commerce HTTP handler, refusing incomplete
// composition.
func NewHandler(cfg HandlerConfig) (*Handler, error) {
	if cfg.Reads == nil {
		return nil, apperr.New(apperr.KindInternal, "commerce_misconfigured", "commerce handler needs reads")
	}
	return &Handler{reads: cfg.Reads, security: cfg.Security}, nil
}

// RegisterRoutes mounts the staged commerce routes on mux without
// touching the process registry: the caller owns the mount, which
// happens only after the activation gate.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/me/commerce/contracts/{id}", h.private(http.HandlerFunc(h.getReceipt)))
	mux.Handle("GET /api/v1/me/commerce/statement", h.private(http.HandlerFunc(h.getStatement)))
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

// titles selects the receipt title dictionary by Accept-Language:
// Portuguese or English. Amounts travel untouched either way; the
// locale only renders the surrounding words.
func titles(r *http.Request) commercedomain.ReceiptTitles {
	tag, ok := locale.Negotiate(r.Header.Get("Accept-Language"))
	if !ok {
		tag = locale.Default()
	}
	if strings.HasPrefix(string(tag), "pt") {
		return commercedomain.ReceiptTitlesFor(commercedomain.ReceiptLocalePortuguese)
	}
	return commercedomain.ReceiptTitlesFor(commercedomain.ReceiptLocaleEnglish)
}

// writeJSON stores one private document: no-store on every commerce
// response, canonical integer amounts, UTC instants.
func writeJSON(w http.ResponseWriter, status int, document any) {
	httpcache.NoStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(document)
}

// fail maps commerce failures to stable problem+json answers:
// unknown contracts to 404, malformed reads to 400. Failure
// documents are private no-store like every commerce answer.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	httpcache.NoStore(w)
	var domainErr commercedomain.DomainError
	if errors.As(err, &domainErr) {
		switch domainErr.Code {
		case commercedomain.CodeContractNotFound:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "contract_unknown", "no such contract for this account"))
			return
		case commercedomain.CodeInvalidReceipt:
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "receipt_invalid", "the receipt request is malformed"))
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
