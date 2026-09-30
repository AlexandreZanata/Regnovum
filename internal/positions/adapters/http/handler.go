// Package http is the inbound HTTP adapter of the positions module
// (P09-T06). It exposes the authenticated position reads and writes under
// /api/v1/me and the public aggregate under /api/v1/arenas. Private routes
// are no-store (THR-CACHE-01); the public aggregate is a short-cache,
// ETag-revalidated read. The local visitor choice is a UI concern: the API
// never treats the aggregate as a secret.
package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/positions/application"
	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

const (
	// maxPositionBodyBytes bounds JSON bodies to 64 KiB.
	maxPositionBodyBytes = 64 << 10

	// aggregateCacheSeconds is the short public cache lifetime of the
	// aggregate; the strong ETag keeps revalidation cheap.
	aggregateCacheSeconds = 60
)

// confirmPositionRequest is the JSON body of an initial confirmation.
type confirmPositionRequest struct {
	Position string `json:"position"`
}

// changePositionRequest is the JSON body of a position change.
type changePositionRequest struct {
	Position string `json:"position"`
}

// privatePositionResponse is the owner projection. It never carries account
// identifiers beyond the addressed pair.
type privatePositionResponse struct {
	ArenaID         string `json:"arena_id"`
	InitialPosition string `json:"initial_position"`
	CurrentPosition string `json:"current_position"`
	Version         int32  `json:"version"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// confirmationResponse is the outcome of an idempotent confirmation.
type confirmationResponse struct {
	Position privatePositionResponse `json:"position"`
	Replayed bool                    `json:"replayed"`
}

// changeResponse is the outcome of one recorded change.
type changeResponse struct {
	ChangeID string                  `json:"change_id"`
	Position privatePositionResponse `json:"position"`
}

// changeHistoryEntry is one row of the owner history.
type changeHistoryEntry struct {
	ChangeID     string `json:"change_id"`
	FromPosition string `json:"from_position"`
	ToPosition   string `json:"to_position"`
	Version      int32  `json:"version"`
	ChangedAt    string `json:"changed_at"`
}

// changeHistoryResponse is the bounded owner collection.
type changeHistoryResponse struct {
	Items []changeHistoryEntry `json:"items"`
}

// distributionResponse is one dimension of the public aggregate.
type distributionResponse struct {
	Agree     int64 `json:"agree"`
	Disagree  int64 `json:"disagree"`
	Undecided int64 `json:"undecided"`
}

// aggregateResponse is the privacy-safe public aggregate. Below the
// configured threshold every count is withheld.
type aggregateResponse struct {
	ParticipantsTotal int64                `json:"participants_total"`
	Suppressed        bool                 `json:"suppressed"`
	Initial           distributionResponse `json:"initial"`
	Current           distributionResponse `json:"current"`
	CheckedAt         string               `json:"checked_at"`
}

// HandlerConfig aggregates the positions use cases and the security manager
// required to serve the positions API.
type HandlerConfig struct {
	ConfirmUseCase   *application.ConfirmInitialPositionUseCase
	ChangeUseCase    *application.ChangePositionUseCase
	GetMineUseCase   *application.GetMyPositionUseCase
	ListMineUseCase  *application.ListPositionChangesUseCase
	AggregateUseCase *application.GetPositionAggregateUseCase
	SecurityManager  *security.Manager
	RateLimit        ratelimit.Protector
}

// Handler serves the positions API.
type Handler struct {
	confirm   *application.ConfirmInitialPositionUseCase
	change    *application.ChangePositionUseCase
	getMine   *application.GetMyPositionUseCase
	listMine  *application.ListPositionChangesUseCase
	aggregate *application.GetPositionAggregateUseCase
	security  *security.Manager
	rateLimit ratelimit.Protector
}

// NewHandler constructs a positions HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		confirm:   cfg.ConfirmUseCase,
		change:    cfg.ChangeUseCase,
		getMine:   cfg.GetMineUseCase,
		listMine:  cfg.ListMineUseCase,
		aggregate: cfg.AggregateUseCase,
		security:  cfg.SecurityManager,
		rateLimit: cfg.RateLimit,
	}
}

// setPrivateNoStoreHeaders enforces THR-CACHE-01 on authenticated routes.
func setPrivateNoStoreHeaders(w http.ResponseWriter) {
	httpcache.Private(w)
}

// withPrivateNoStore guarantees the private cache headers even for
// rejections produced by middleware before the handler runs.
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

// writeCacheableJSON serves the public aggregate, whose body carries the
// instant of its own derivation.
//
// The validator covers the aggregate without that instant, and it is weak. A
// strong validator over the whole body was wrong, and the cost was measurable:
// the instant moves on every request, so two reads of the same counts never
// compared equal and a client revalidating after the cache window was sent the
// whole document again — the ETag saved nothing. RFC 9110 section 8.8.1
// requires a strong validator to be unique across every representation, which
// no validator can be while the annotation is part of the body; section 8.8.2
// is for exactly this case, a representation that stays equivalent while its
// metadata moves, and If-None-Match performs the weak comparison for GET.
//
// facts is the aggregate with the annotation left empty; document is the same
// document as it is served. The helper mirrors the other public reads, and the
// copy keeps the adapters independent.
func writeCacheableJSON(w http.ResponseWriter, r *http.Request, facts, document any) {
	factsBody, err := json.Marshal(facts)
	if err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "failed to encode response"))
		return
	}
	body, err := json.Marshal(document)
	if err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "failed to encode response"))
		return
	}

	sum := sha256.Sum256(factsBody)
	etag := `W/"` + hex.EncodeToString(sum[:]) + `"`

	httpcache.Public(w, aggregateCacheSeconds)
	w.Header().Set("ETag", etag)

	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// etagMatches implements the weak comparison of RFC 9110 for If-None-Match:
// the opaque tags are compared, and the weakness prefix of either side is not
// part of the identity of the representation.
func etagMatches(header, etag string) bool {
	etag = strings.TrimPrefix(etag, "W/")
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag {
			return true
		}
	}
	return false
}

// writePositionProblem maps positions errors to RFC 9457 Problem Details
// with stable codes; domain codes surface lowercased so clients depend on
// the code, not on titles.
func writePositionProblem(w http.ResponseWriter, r *http.Request, err error) {
	var domainErr domain.DomainError
	hasDomainError := errors.As(err, &domainErr)
	switch {
	case errors.Is(err, application.ErrPositionNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "position_not_found", "no position was confirmed for this arena"))
	case errors.Is(err, application.ErrInitialPositionAlreadySet):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "initial_position_already_set", "the initial position is immutable history"))
	case errors.Is(err, application.ErrVersionConflict):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "version_conflict", "the position changed since it was read"))
	case errors.Is(err, application.ErrAccountNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "account_not_found", "account not found"))
	case errors.Is(err, application.ErrAccountNotEligible):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "account_not_eligible", "the account is not eligible to participate"))
	case errors.Is(err, application.ErrAccountSuspended):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "account_suspended", "the account is suspended"))
	case errors.Is(err, application.ErrArenaNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "arena_not_found", "arena not found"))
	case errors.Is(err, application.ErrArenaNotOpen):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "arena_not_open", "the arena accepts no new positions"))
	case errors.Is(err, domain.ErrSamePosition):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, strings.ToLower(string(domainErr.Code)), domainErr.Message))
	case hasDomainError:
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, strings.ToLower(string(domainErr.Code)), domainErr.Message))
	default:
		_ = httperror.WriteProblem(w, r, err)
	}
}

// identity extracts the authenticated account id.
func (h *Handler) identity(r *http.Request) (string, bool) {
	identity, ok := security.FromContext(r.Context())
	if !ok {
		return "", false
	}
	return identity.AccountID, true
}

// privatePosition converts the projection into the owner response.
func privatePosition(position domain.DebatePosition) privatePositionResponse {
	return privatePositionResponse{
		ArenaID:         position.ArenaID().String(),
		InitialPosition: position.InitialPosition().String(),
		CurrentPosition: position.CurrentPosition().String(),
		Version:         position.Version(),
		CreatedAt:       position.CreatedAt().UTC().Format(time.RFC3339),
		UpdatedAt:       position.UpdatedAt().UTC().Format(time.RFC3339),
	}
}

// decodeBody reads a bounded JSON body into target.
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPositionBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON within the size limit"))
		return false
	}
	return true
}

// ConfirmPosition handles POST /api/v1/me/arenas/{id}/position.
func (h *Handler) ConfirmPosition(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	var request confirmPositionRequest
	if !decodeBody(w, r, &request) {
		return
	}

	result, err := h.confirm.Execute(r.Context(), application.ConfirmInitialPositionCommand{
		AccountID: accountID,
		ArenaID:   r.PathValue("id"),
		Position:  request.Position,
	})
	if err != nil {
		writePositionProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, confirmationResponse{
		Position: privatePosition(*result.Position),
		Replayed: result.Replayed,
	})
}

// ChangePosition handles POST /api/v1/me/arenas/{id}/position/changes.
func (h *Handler) ChangePosition(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	var request changePositionRequest
	if !decodeBody(w, r, &request) {
		return
	}

	result, err := h.change.Execute(r.Context(), application.ChangePositionCommand{
		AccountID: accountID,
		ArenaID:   r.PathValue("id"),
		Position:  request.Position,
	})
	if err != nil {
		writePositionProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, changeResponse{
		ChangeID: result.ChangeID,
		Position: privatePosition(*result.Position),
	})
}

// GetMyPosition handles GET /api/v1/me/arenas/{id}/position.
func (h *Handler) GetMyPosition(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	position, err := h.getMine.Execute(r.Context(), application.GetMyPositionQuery{
		AccountID: accountID,
		ArenaID:   r.PathValue("id"),
	})
	if err != nil {
		writePositionProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, privatePosition(*position))
}

// ListMyChanges handles GET /api/v1/me/arenas/{id}/position/changes.
func (h *Handler) ListMyChanges(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	records, err := h.listMine.Execute(r.Context(), application.ListPositionChangesQuery{
		AccountID: accountID,
		ArenaID:   r.PathValue("id"),
	})
	if err != nil {
		writePositionProblem(w, r, err)
		return
	}

	items := make([]changeHistoryEntry, 0, len(records))
	for _, record := range records {
		items = append(items, changeHistoryEntry{
			ChangeID:     record.ID,
			FromPosition: record.Change.From().String(),
			ToPosition:   record.Change.To().String(),
			Version:      record.Change.Version(),
			ChangedAt:    record.Change.ChangedAt().UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, changeHistoryResponse{Items: items})
}

// GetAggregate handles GET /api/v1/arenas/{id}/positions. The local visitor
// choice gates the UI, never the API: the aggregate is a public, short-cache
// read with no individual data.
func (h *Handler) GetAggregate(w http.ResponseWriter, r *http.Request) {
	aggregate, err := h.aggregate.Execute(r.Context(), application.GetPositionAggregateQuery{
		ArenaID: r.PathValue("id"),
	})
	if err != nil {
		writePositionProblem(w, r, err)
		return
	}

	facts := aggregateResponse{
		ParticipantsTotal: aggregate.Total,
		Suppressed:        aggregate.Suppressed,
		Initial: distributionResponse{
			Agree:     aggregate.Initial.Agree,
			Disagree:  aggregate.Initial.Disagree,
			Undecided: aggregate.Initial.Undecided,
		},
		Current: distributionResponse{
			Agree:     aggregate.Current.Agree,
			Disagree:  aggregate.Current.Disagree,
			Undecided: aggregate.Current.Undecided,
		},
	}
	served := facts
	served.CheckedAt = aggregate.CheckedAt.UTC().Format(time.RFC3339)
	writeCacheableJSON(w, r, facts, served)
}

// privateRoute applies the authentication requirement when a security
// manager is configured.
func (h *Handler) privateRoute(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}

// protect applies the rate limit policy of one action.
func (h *Handler) protect(action ratelimit.Action, next http.Handler) http.Handler {
	if h.rateLimit == nil {
		return next
	}
	return h.rateLimit.Protect(action, next)
}

// RegisterRoutes wires the positions endpoints into the provided ServeMux.
//
// The two writes carry a rate limit policy. It sits inside the authentication
// middleware, because the policy bounds the account as well as the network
// address and the account is only known after authentication.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/me/arenas/{id}/position", withPrivateNoStore(h.privateRoute(h.protect(ratelimit.ActionPositionConfirm, http.HandlerFunc(h.ConfirmPosition)))))
	mux.Handle("GET /api/v1/me/arenas/{id}/position", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.GetMyPosition))))
	mux.Handle("POST /api/v1/me/arenas/{id}/position/changes", withPrivateNoStore(h.privateRoute(h.protect(ratelimit.ActionPositionChange, http.HandlerFunc(h.ChangePosition)))))
	mux.Handle("GET /api/v1/me/arenas/{id}/position/changes", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.ListMyChanges))))

	mux.HandleFunc("GET /api/v1/arenas/{id}/positions", h.GetAggregate)
}
