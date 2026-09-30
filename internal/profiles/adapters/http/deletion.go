package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// DeletionHandlerConfig aggregates the deletion use cases and the security
// manager required to serve the account deletion API.
type DeletionHandlerConfig struct {
	RequestUseCase  *application.RequestDeletionUseCase
	StatusUseCase   *application.GetDeletionStatusUseCase
	CancelUseCase   *application.CancelDeletionUseCase
	SecurityManager *security.Manager
}

// DeletionHandler serves the authenticated account deletion workflow.
type DeletionHandler struct {
	request  *application.RequestDeletionUseCase
	status   *application.GetDeletionStatusUseCase
	cancel   *application.CancelDeletionUseCase
	security *security.Manager
}

// NewDeletionHandler constructs the deletion HTTP handler.
func NewDeletionHandler(cfg DeletionHandlerConfig) *DeletionHandler {
	return &DeletionHandler{
		request:  cfg.RequestUseCase,
		status:   cfg.StatusUseCase,
		cancel:   cfg.CancelUseCase,
		security: cfg.SecurityManager,
	}
}

// deletionResponse is the owner projection of one deletion request. The
// cancel reason is restricted evidence and never serializes.
type deletionResponse struct {
	Status      string  `json:"status"`
	RequestedAt string  `json:"requested_at"`
	ExecutedAt  *string `json:"executed_at"`
	CanceledAt  *string `json:"canceled_at"`
}

// cancelDeletionRequest is the optional JSON body of a cancellation.
type cancelDeletionRequest struct {
	Reason string `json:"reason"`
}

// deletionState converts the application record into the response document.
func deletionState(request *application.DeletionRequest) deletionResponse {
	response := deletionResponse{
		Status:      string(request.Status),
		RequestedAt: request.RequestedAt.UTC().Format(time.RFC3339),
	}
	if request.ExecutedAt != nil {
		executed := request.ExecutedAt.UTC().Format(time.RFC3339)
		response.ExecutedAt = &executed
	}
	if request.CanceledAt != nil {
		canceled := request.CanceledAt.UTC().Format(time.RFC3339)
		response.CanceledAt = &canceled
	}
	return response
}

// RequestDeletion handles POST /api/v1/me/deletion. The request starts the
// cooling-off window; a replayed call resolves the existing record.
func (h *DeletionHandler) RequestDeletion(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.request == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "deletion workflow unavailable"))
		return
	}

	outcome, err := h.request.Execute(r.Context(), application.RequestDeletionCommand{
		AccountID: domain.AccountID(identity.AccountID),
	})
	if err != nil {
		writeDeletionProblem(w, r, err)
		return
	}
	if outcome.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusAccepted, deletionState(&outcome.Request))
}

// GetDeletionStatus handles GET /api/v1/me/deletion.
func (h *DeletionHandler) GetDeletionStatus(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.status == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "deletion workflow unavailable"))
		return
	}

	request, err := h.status.Execute(r.Context(), domain.AccountID(identity.AccountID))
	if err != nil {
		writeDeletionProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deletionState(request))
}

// CancelDeletion handles POST /api/v1/me/deletion/cancel.
func (h *DeletionHandler) CancelDeletion(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.cancel == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "deletion workflow unavailable"))
		return
	}

	var body cancelDeletionRequest
	if r.Body != nil && r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxDeletionBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON within the size limit"))
			return
		}
	}

	canceled, err := h.cancel.Execute(r.Context(), application.CancelDeletionCommand{
		AccountID: domain.AccountID(identity.AccountID),
		Reason:    body.Reason,
	})
	if err != nil {
		writeDeletionProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deletionState(canceled))
}

// maxDeletionBodyBytes bounds the optional cancellation body.
const maxDeletionBodyBytes = 4 << 10

// writeDeletionProblem maps deletion errors to RFC 9457 Problem Details
// with stable codes; the cancel reason and foreign records are never
// reflected.
func writeDeletionProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrEmptyAccountID):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
	case errors.Is(err, application.ErrDeletionRequestNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "deletion_request_not_found", "account deletion request not found"))
	case errors.Is(err, application.ErrDeletionNotCancellable),
		errors.Is(err, application.ErrDeletionNotExecutable):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "deletion_not_cancellable", "the deletion request cannot be canceled in its current state"))
	case errors.Is(err, application.ErrInvalidCancelReason):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_cancel_reason", "the cancellation reason is invalid"))
	case errors.Is(err, application.ErrAccountNotEligible):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "account_not_eligible", "account is not eligible for this operation"))
	default:
		_ = httperror.WriteProblem(w, r, err)
	}
}

// RegisterRoutes wires the deletion endpoints into the provided ServeMux.
// All routes require the owner session and the private cache policy.
func (h *DeletionHandler) RegisterRoutes(mux *http.ServeMux) {
	request := http.Handler(http.HandlerFunc(h.RequestDeletion))
	status := http.Handler(http.HandlerFunc(h.GetDeletionStatus))
	cancel := http.Handler(http.HandlerFunc(h.CancelDeletion))
	if h.security != nil {
		request = h.security.RequireAuthMiddleware()(request)
		status = h.security.RequireAuthMiddleware()(status)
		cancel = h.security.RequireAuthMiddleware()(cancel)
	}
	mux.Handle("POST /api/v1/me/deletion", withPrivateNoStore(request))
	mux.Handle("GET /api/v1/me/deletion", withPrivateNoStore(status))
	mux.Handle("POST /api/v1/me/deletion/cancel", withPrivateNoStore(cancel))
}
