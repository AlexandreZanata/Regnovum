// Package http is the inbound HTTP adapter of the billing module (P07-T06).
// It exposes the authenticated, read-only Arena Pass API under /api/v1/me:
// the derived summary (available total and private per-lot breakdown) and
// the paginated consumption history. There is deliberately no public grant
// or consume endpoint — those only happen through the audited application
// use cases. Every response is private and no-store (THR-CACHE-01) and only
// minimal entitlement data serializes.
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// passLotResponse is one private breakdown entry: derived availability, the
// grant origin and the optional immutable expiration.
type passLotResponse struct {
	Origin    string  `json:"origin"`
	Quantity  int32   `json:"quantity"`
	Remaining int32   `json:"remaining"`
	ExpiresAt *string `json:"expires_at"`
	Expired   bool    `json:"expired"`
	CreatedAt string  `json:"created_at"`
}

// passSummaryResponse is the explicit JSON document of GET
// /api/v1/me/passes.
type passSummaryResponse struct {
	AvailableTotal int64             `json:"available_total"`
	CheckedAt      string            `json:"checked_at"`
	Lots           []passLotResponse `json:"lots"`
}

// passHistoryEntryResponse is one consumption line of the owner history.
type passHistoryEntryResponse struct {
	ConsumptionID string `json:"consumption_id"`
	ArenaID       string `json:"arena_id"`
	Origin        string `json:"origin"`
	Reference     string `json:"reference"`
	ConsumedAt    string `json:"consumed_at"`
}

// passHistoryResponse is the cursor page envelope fixed by the API
// conventions: { items, next_cursor }.
type passHistoryResponse struct {
	Items      []passHistoryEntryResponse `json:"items"`
	NextCursor *string                    `json:"next_cursor"`
}

// HandlerConfig aggregates the pass query use cases and the security manager
// required to serve the pass API.
type HandlerConfig struct {
	GetArenaPassSummaryUseCase *application.GetArenaPassSummaryUseCase
	GetArenaPassHistoryUseCase *application.GetArenaPassHistoryUseCase
	SecurityManager            *security.Manager
}

// Handler serves the versioned pass entitlement API.
type Handler struct {
	getSummary *application.GetArenaPassSummaryUseCase
	getHistory *application.GetArenaPassHistoryUseCase
	security   *security.Manager
}

// NewHandler constructs a billing HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		getSummary: cfg.GetArenaPassSummaryUseCase,
		getHistory: cfg.GetArenaPassHistoryUseCase,
		security:   cfg.SecurityManager,
	}
}

// setPrivateNoStoreHeaders enforces THR-CACHE-01 on authenticated routes.
func setPrivateNoStoreHeaders(w http.ResponseWriter) {
	httpcache.Private(w)
}

// withPrivateNoStore guarantees the cache headers even for rejections
// produced by middleware before the handler runs.
func withPrivateNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setPrivateNoStoreHeaders(w)
		next.ServeHTTP(w, r)
	})
}

// writeJSON renders a JSON response document.
func writeJSON(w http.ResponseWriter, status int, document any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(document)
}

// writePassProblem maps pass query errors to RFC 9457 Problem Details.
// Unknown values are never echoed: an invalid cursor is simply invalid.
func writePassProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrInvalidCursor):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_cursor", "history cursor is invalid"))
	case errors.Is(err, domain.ErrEmptyAccountID):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
	default:
		_ = httperror.WriteProblem(w, r, err)
	}
}

// GetArenaPassSummary handles GET /api/v1/me/passes.
func (h *Handler) GetArenaPassSummary(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.getSummary == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "pass query unavailable"))
		return
	}

	summary, err := h.getSummary.Execute(r.Context(), domain.AccountID(identity.AccountID))
	if err != nil {
		writePassProblem(w, r, err)
		return
	}

	lots := make([]passLotResponse, 0, len(summary.Lots))
	for _, entry := range summary.Lots {
		var expiresAt *string
		if instant := entry.Lot.ExpiresAt(); instant != nil {
			formatted := instant.UTC().Format(time.RFC3339)
			expiresAt = &formatted
		}
		lots = append(lots, passLotResponse{
			Origin:    entry.Lot.Origin().String(),
			Quantity:  entry.Lot.Quantity().Int32(),
			Remaining: entry.Lot.Remaining(),
			ExpiresAt: expiresAt,
			Expired:   entry.Expired,
			CreatedAt: entry.Lot.CreatedAt().UTC().Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, passSummaryResponse{
		AvailableTotal: summary.AvailableTotal,
		CheckedAt:      summary.CheckedAt.UTC().Format(time.RFC3339),
		Lots:           lots,
	})
}

// GetArenaPassHistory handles GET /api/v1/me/passes/history with cursor
// pagination.
func (h *Handler) GetArenaPassHistory(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.getHistory == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "pass query unavailable"))
		return
	}

	limit, err := parseHistoryLimit(r)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	history, err := h.getHistory.Execute(r.Context(), domain.AccountID(identity.AccountID), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writePassProblem(w, r, err)
		return
	}

	items := make([]passHistoryEntryResponse, 0, len(history.Entries))
	for _, entry := range history.Entries {
		items = append(items, passHistoryEntryResponse{
			ConsumptionID: entry.ConsumptionID,
			ArenaID:       entry.ArenaID,
			Origin:        entry.Origin.String(),
			Reference:     entry.Reference.String(),
			ConsumedAt:    entry.ConsumedAt.UTC().Format(time.RFC3339),
		})
	}

	var nextCursor *string
	if history.NextCursor != "" {
		nextCursor = &history.NextCursor
	}

	writeJSON(w, http.StatusOK, passHistoryResponse{Items: items, NextCursor: nextCursor})
}

// parseHistoryLimit reads the optional limit query parameter; an empty value
// selects the default (clamped later by the use case) and anything
// non-numeric or negative is a client error.
func parseHistoryLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, apperr.New(apperr.KindValidation, "invalid_limit", "limit must be a non-negative integer")
	}
	return value, nil
}

// RegisterRoutes wires the authenticated pass read endpoints into the
// provided ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/me/passes", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.GetArenaPassSummary))))
	mux.Handle("GET /api/v1/me/passes/history", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.GetArenaPassHistory))))
}

// privateRoute applies the authentication requirement when a security
// manager is configured.
func (h *Handler) privateRoute(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}
