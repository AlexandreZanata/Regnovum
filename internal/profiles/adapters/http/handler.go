// Package http is the inbound HTTP adapter of the profiles module (P05-T04).
// It exposes the separated public and private profile routes under /api/v1
// and renders explicit response documents: the public projection never
// includes email, internal identifiers, payment data, antifraud flags or
// administrative notes; the private projection is served only to the
// authenticated owner with the mandatory private cache policy (THR-CACHE-01).
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// publicProfileResponse is the explicit JSON document of GET
// /api/v1/profiles/{username}. Dates are RFC 3339 in UTC (I18N_STANDARD.md
// §5); no localized phrases are produced here.
type publicProfileResponse struct {
	Username        string `json:"username"`
	InterfaceLocale string `json:"interface_locale"`
	CreatedAt       string `json:"created_at"`
}

// privateProfileResponse is the explicit JSON document of GET
// /api/v1/me/profile, served only to the owner.
type privateProfileResponse struct {
	Username        string `json:"username"`
	InterfaceLocale string `json:"interface_locale"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// HandlerConfig aggregates the profile query use cases and the security
// manager required to serve the profile API.
type HandlerConfig struct {
	GetPublicProfileUseCase  *application.GetPublicProfileUseCase
	GetPrivateProfileUseCase *application.GetPrivateProfileUseCase
	SecurityManager          *security.Manager
}

// Handler serves the versioned profile API.
type Handler struct {
	getPublicProfile  *application.GetPublicProfileUseCase
	getPrivateProfile *application.GetPrivateProfileUseCase
	security          *security.Manager
}

// NewHandler constructs a profiles HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		getPublicProfile:  cfg.GetPublicProfileUseCase,
		getPrivateProfile: cfg.GetPrivateProfileUseCase,
		security:          cfg.SecurityManager,
	}
}

// setPublicNoStoreHeaders keeps public profile responses out of shared
// caches until a deliberate public caching policy exists (phase 17).
func setPublicNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// setPrivateNoStoreHeaders enforces THR-CACHE-01 on authenticated routes.
func setPrivateNoStoreHeaders(w http.ResponseWriter) {
	httpcache.Private(w)
}

// writeJSON renders a JSON response document.
func writeJSON(w http.ResponseWriter, status int, document any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(document)
}

// writeProfileError maps profile errors to RFC 9457 Problem Details. Unknown
// values are never echoed: a profile that cannot be found is simply absent.
func writeProfileError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrProfileNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "profile_not_found", "profile not found"))
	case errors.Is(err, domain.ErrEmptyAccountID):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
	case errors.Is(err, application.ErrAccountNotEligible):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "account_not_eligible", "account is not eligible for this operation"))
	default:
		_ = httperror.WriteProblem(w, r, err)
	}
}

// GetPublicProfile handles GET /api/v1/profiles/{username}.
func (h *Handler) GetPublicProfile(w http.ResponseWriter, r *http.Request) {
	setPublicNoStoreHeaders(w)

	if h.getPublicProfile == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "profile query unavailable"))
		return
	}

	profile, err := h.getPublicProfile.Execute(r.Context(), r.PathValue("username"))
	if err != nil {
		writeProfileError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, publicProfileResponse{
		Username:        profile.Username,
		InterfaceLocale: profile.InterfaceLocale,
		CreatedAt:       profile.CreatedAt.UTC().Format(time.RFC3339),
	})
}

// GetPrivateProfile handles GET /api/v1/me/profile for the authenticated owner.
func (h *Handler) GetPrivateProfile(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	if h.getPrivateProfile == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "profile query unavailable"))
		return
	}

	profile, err := h.getPrivateProfile.Execute(r.Context(), domain.AccountID(identity.AccountID))
	if err != nil {
		writeProfileError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, privateProfileResponse{
		Username:        profile.Username,
		InterfaceLocale: profile.InterfaceLocale,
		CreatedAt:       profile.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       profile.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

// withPrivateNoStore guarantees the THR-CACHE-01 headers even for
// rejections produced by middleware before the private handler runs.
func withPrivateNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setPrivateNoStoreHeaders(w)
		next.ServeHTTP(w, r)
	})
}

// RegisterRoutes wires the profile endpoints into the provided ServeMux. The
// private route additionally enforces an authenticated context and the
// mandatory private cache policy.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/profiles/{username}", h.GetPublicProfile)

	privateProfile := http.Handler(http.HandlerFunc(h.GetPrivateProfile))
	if h.security != nil {
		privateProfile = h.security.RequireAuthMiddleware()(privateProfile)
	}
	mux.Handle("GET /api/v1/me/profile", withPrivateNoStore(privateProfile))
}
