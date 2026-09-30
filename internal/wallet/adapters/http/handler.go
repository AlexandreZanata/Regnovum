// Package http is the inbound HTTP adapter of the wallet module (P06-T08).
// It exposes the authenticated, read-only wallet API under /api/v1/me:
// derived balances and the paginated ledger statement. There is deliberately
// no generic public credit or debit route — mutations only happen through
// the audited application use cases. Every response is private and no-store
// (THR-CACHE-01) and restricted administrative fields (reason, actor,
// antifraud data) never serialize.
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// walletBalanceResponse is the explicit JSON document of GET
// /api/v1/me/wallet: bucket balances in integer INK units, never formatted
// money and never floats.
type walletBalanceResponse struct {
	BalanceFree      int64 `json:"balance_free"`
	BalancePurchased int64 `json:"balance_purchased"`
}

// statementEntryResponse is one public ledger line of the owner statement.
// The administrative reason and actor stay restricted: the stable operation
// type carries the motive.
type statementEntryResponse struct {
	TransactionID string `json:"transaction_id"`
	OperationID   string `json:"operation_id"`
	OperationType string `json:"operation_type"`
	Bucket        string `json:"bucket"`
	Amount        int64  `json:"amount"`
	Reference     string `json:"reference"`
	CreatedAt     string `json:"created_at"`
}

// statementResponse is the cursor page envelope fixed by the API
// conventions: { items, next_cursor }.
type statementResponse struct {
	Items      []statementEntryResponse `json:"items"`
	NextCursor *string                  `json:"next_cursor"`
}

// HandlerConfig aggregates the wallet query use cases and the security
// manager required to serve the wallet API.
type HandlerConfig struct {
	GetWalletBalanceUseCase   *application.GetWalletBalanceUseCase
	GetWalletStatementUseCase *application.GetWalletStatementUseCase
	SecurityManager           *security.Manager
}

// Handler serves the versioned wallet API.
type Handler struct {
	getBalance   *application.GetWalletBalanceUseCase
	getStatement *application.GetWalletStatementUseCase
	security     *security.Manager
}

// NewHandler constructs a wallet HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		getBalance:   cfg.GetWalletBalanceUseCase,
		getStatement: cfg.GetWalletStatementUseCase,
		security:     cfg.SecurityManager,
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

// writeWalletProblem maps wallet query errors to RFC 9457 Problem Details.
// Unknown values are never echoed: an invalid cursor is simply invalid.
func writeWalletProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrInvalidCursor):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_cursor", "statement cursor is invalid"))
	case errors.Is(err, domain.ErrEmptyAccountID):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
	default:
		_ = httperror.WriteProblem(w, r, err)
	}
}

// GetWalletBalance handles GET /api/v1/me/wallet.
func (h *Handler) GetWalletBalance(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.getBalance == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "wallet query unavailable"))
		return
	}

	balance, err := h.getBalance.Execute(r.Context(), domain.AccountID(identity.AccountID))
	if err != nil {
		writeWalletProblem(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, walletBalanceResponse{
		BalanceFree:      balance.Free.Int64(),
		BalancePurchased: balance.Purchased.Int64(),
	})
}

// GetWalletStatement handles GET /api/v1/me/wallet/transactions with cursor
// pagination.
func (h *Handler) GetWalletStatement(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.getStatement == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "wallet query unavailable"))
		return
	}

	limit, err := parseStatementLimit(r)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	statement, err := h.getStatement.Execute(r.Context(), domain.AccountID(identity.AccountID), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeWalletProblem(w, r, err)
		return
	}

	items := make([]statementEntryResponse, 0, len(statement.Entries))
	for _, entry := range statement.Entries {
		items = append(items, statementEntryResponse{
			TransactionID: entry.TransactionID,
			OperationID:   entry.OperationID,
			OperationType: entry.OperationType.String(),
			Bucket:        entry.Bucket.String(),
			Amount:        entry.Amount,
			Reference:     entry.Reference.String(),
			CreatedAt:     entry.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	var nextCursor *string
	if statement.NextCursor != "" {
		nextCursor = &statement.NextCursor
	}

	writeJSON(w, http.StatusOK, statementResponse{Items: items, NextCursor: nextCursor})
}

// parseStatementLimit reads the optional limit query parameter; an empty
// value selects the default (clamped later by the use case) and anything
// non-numeric or negative is a client error.
func parseStatementLimit(r *http.Request) (int, error) {
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

// RegisterRoutes wires the authenticated wallet read endpoints into the
// provided ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/me/wallet", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.GetWalletBalance))))
	mux.Handle("GET /api/v1/me/wallet/transactions", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.GetWalletStatement))))
}

// privateRoute applies the authentication requirement when a security
// manager is configured.
func (h *Handler) privateRoute(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}
