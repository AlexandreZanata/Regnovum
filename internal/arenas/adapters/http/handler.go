// Package http is the inbound HTTP adapter of the arenas module (P08-T07).
// It exposes the authenticated draft CRUD, publication, closing and the
// public reads under /api/v1. Draft routes are private and no-store
// (THR-CACHE-01); public reads carry an ETag and public cache headers. There
// is no endpoint that publishes or closes without the audited application
// use cases.
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

const (
	// maxArenaBodyBytes bounds JSON bodies to 64 KiB.
	maxArenaBodyBytes = 64 << 10

	// publicCacheSeconds is the provisional public cache lifetime of arena
	// reads; ETag revalidation keeps it safe.
	publicCacheSeconds = 60
)

// createDraftRequest is the JSON body of a draft creation.
type createDraftRequest struct {
	Statement string `json:"statement"`
	Context   string `json:"context"`
	Category  string `json:"category"`
	Language  string `json:"language"`
}

// updateDraftRequest replaces a draft; expected_version comes from the last
// read and drives the optimistic check.
type updateDraftRequest struct {
	Statement       string `json:"statement"`
	Context         string `json:"context"`
	Category        string `json:"category"`
	Language        string `json:"language"`
	ExpectedVersion int32  `json:"expected_version"`
}

// privateArenaResponse is the owner projection: drafts and the owner's
// published Arena share it. It never carries email, payment or moderation
// data.
type privateArenaResponse struct {
	ID          string  `json:"id"`
	Slug        *string `json:"slug"`
	Statement   string  `json:"statement"`
	Context     *string `json:"context"`
	Category    string  `json:"category"`
	Language    string  `json:"language"`
	Status      string  `json:"status"`
	Version     int32   `json:"version"`
	CreatedAt   string  `json:"created_at"`
	PublishedAt *string `json:"published_at"`
	ClosesAt    *string `json:"closes_at"`
}

// privateArenaListResponse is the owner draft list.
type privateArenaListResponse struct {
	Items []privateArenaResponse `json:"items"`
}

// publicArenaSummaryResponse is one feed entry: no context, no version and
// no internal identifiers beyond the stable public id and slug.
type publicArenaSummaryResponse struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Statement   string  `json:"statement"`
	Category    string  `json:"category"`
	Language    string  `json:"language"`
	Status      string  `json:"status"`
	PublishedAt string  `json:"published_at"`
	ClosesAt    *string `json:"closes_at"`
}

// publicArenaResponse is the public Arena document.
type publicArenaResponse struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Statement   string  `json:"statement"`
	Context     *string `json:"context"`
	Category    string  `json:"category"`
	Language    string  `json:"language"`
	Status      string  `json:"status"`
	PublishedAt string  `json:"published_at"`
	ClosesAt    *string `json:"closes_at"`
}

// publicArenaFeedResponse is the cursor page envelope fixed by the API
// conventions.
type publicArenaFeedResponse struct {
	Items      []publicArenaSummaryResponse `json:"items"`
	NextCursor *string                      `json:"next_cursor"`
}

// HandlerConfig aggregates the arenas use cases and the security manager
// required to serve the arena API.
type HandlerConfig struct {
	CreateDraftUseCase *application.CreateArenaDraftUseCase
	GetDraftUseCase    *application.GetArenaDraftUseCase
	ListDraftsUseCase  *application.ListArenaDraftsUseCase
	UpdateDraftUseCase *application.UpdateArenaDraftUseCase
	DeleteDraftUseCase *application.DeleteArenaDraftUseCase
	PublishUseCase     *application.PublishArenaUseCase
	CloseUseCase       *application.CloseArenaUseCase
	FeedUseCase        *application.GetArenaFeedUseCase
	GetPublicUseCase   *application.GetPublicArenaUseCase
	SecurityManager    *security.Manager
	Challenge          turnstile.Challenger
}

// Handler serves the versioned arena API.
type Handler struct {
	createDraft *application.CreateArenaDraftUseCase
	getDraft    *application.GetArenaDraftUseCase
	listDrafts  *application.ListArenaDraftsUseCase
	updateDraft *application.UpdateArenaDraftUseCase
	deleteDraft *application.DeleteArenaDraftUseCase
	publish     *application.PublishArenaUseCase
	close       *application.CloseArenaUseCase
	feed        *application.GetArenaFeedUseCase
	getPublic   *application.GetPublicArenaUseCase
	security    *security.Manager
	challenge   turnstile.Challenger
}

// NewHandler constructs an arenas HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		createDraft: cfg.CreateDraftUseCase,
		getDraft:    cfg.GetDraftUseCase,
		listDrafts:  cfg.ListDraftsUseCase,
		updateDraft: cfg.UpdateDraftUseCase,
		deleteDraft: cfg.DeleteDraftUseCase,
		publish:     cfg.PublishUseCase,
		close:       cfg.CloseUseCase,
		feed:        cfg.FeedUseCase,
		getPublic:   cfg.GetPublicUseCase,
		security:    cfg.SecurityManager,
		challenge:   cfg.Challenge,
	}
}

// setPrivateNoStoreHeaders enforces THR-CACHE-01 on authenticated routes.
func setPrivateNoStoreHeaders(w http.ResponseWriter) {
	httpcache.Private(w)
}

// setPublicCacheHeaders marks a public read as cacheable and revalidatable.
func setPublicCacheHeaders(w http.ResponseWriter) {
	httpcache.Public(w, publicCacheSeconds)
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

// writePublicJSON renders a cacheable public document with an ETag computed
// over its canonical JSON body and honors If-None-Match with 304.
func writePublicJSON(w http.ResponseWriter, r *http.Request, status int, document any) {
	body, err := json.Marshal(document)
	if err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "failed to encode response"))
		return
	}

	etag := httpcache.Validator(body)

	setPublicCacheHeaders(w)
	w.Header().Set("ETag", etag)

	if httpcache.Matches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writerProblem maps arena errors to RFC 9457 Problem Details with stable
// codes; domain codes surface lowercased so clients depend on the code, not
// on titles.
func writeArenaProblem(w http.ResponseWriter, r *http.Request, err error) {
	var domainErr domain.DomainError
	hasDomainError := errors.As(err, &domainErr)
	switch {
	case errors.Is(err, application.ErrArenaNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "arena_not_found", "arena not found"))
	case errors.Is(err, application.ErrNoPassAvailable):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "no_pass_available", "no arena pass is available for this publication"))
	case errors.Is(err, application.ErrSlugConflict):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "slug_conflict", "the arena address is already taken"))
	case errors.Is(err, application.ErrVersionConflict):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "version_conflict", "the arena changed since it was read"))
	case errors.Is(err, application.ErrInvalidCursor):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_cursor", "cursor is invalid"))
	case errors.Is(err, application.ErrInvalidFeedFilter):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_filter", "feed filter is invalid"))
	case errors.Is(err, application.ErrNotAuthorized):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "not_authorized", "actor is not authorized for arena moderation"))
	case errors.Is(err, domain.ErrInvalidStatusChange), errors.Is(err, domain.ErrArenaNotOpen), errors.Is(err, domain.ErrArenaNotDraft):
		code := "invalid_status_change"
		message := "the arena status transition is not permitted"
		if hasDomainError {
			code = strings.ToLower(string(domainErr.Code))
			message = domainErr.Message
		}
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, code, message))
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

// privateArena converts an Arena into the owner projection.
func privateArena(arena domain.Arena) privateArenaResponse {
	return privateArenaResponse{
		ID:          arena.ID().String(),
		Slug:        optionalString(arena.Slug().String()),
		Statement:   arena.Statement().String(),
		Context:     optionalString(arena.Context().String()),
		Category:    arena.Category().String(),
		Language:    arena.Language().String(),
		Status:      arena.Status().String(),
		Version:     arena.Version(),
		CreatedAt:   arena.CreatedAt().UTC().Format(time.RFC3339),
		PublishedAt: optionalInstant(arena.PublishedAt()),
		ClosesAt:    optionalInstant(arena.ClosesAt()),
	}
}

// publicArenaSummary converts an Arena into the feed projection.
func publicArenaSummary(arena domain.Arena) publicArenaSummaryResponse {
	publishedAt := ""
	if instant := arena.PublishedAt(); instant != nil {
		publishedAt = instant.UTC().Format(time.RFC3339)
	}
	return publicArenaSummaryResponse{
		ID:          arena.ID().String(),
		Slug:        arena.Slug().String(),
		Statement:   arena.Statement().String(),
		Category:    arena.Category().String(),
		Language:    arena.Language().String(),
		Status:      arena.Status().String(),
		PublishedAt: publishedAt,
		ClosesAt:    optionalInstant(arena.ClosesAt()),
	}
}

// publicArena converts an Arena into the public document projection.
func publicArena(arena domain.Arena) publicArenaResponse {
	summary := publicArenaSummary(arena)
	return publicArenaResponse{
		ID:          summary.ID,
		Slug:        summary.Slug,
		Statement:   summary.Statement,
		Context:     optionalString(arena.Context().String()),
		Category:    summary.Category,
		Language:    summary.Language,
		Status:      summary.Status,
		PublishedAt: summary.PublishedAt,
		ClosesAt:    summary.ClosesAt,
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalInstant(instant *time.Time) *string {
	if instant == nil {
		return nil
	}
	formatted := instant.UTC().Format(time.RFC3339)
	return &formatted
}

// decodeBody reads a bounded JSON body into target.
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxArenaBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON within the size limit"))
		return false
	}
	return true
}

// CreateDraft handles POST /api/v1/me/arena-drafts.
func (h *Handler) CreateDraft(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	var request createDraftRequest
	if !decodeBody(w, r, &request) {
		return
	}

	arena, err := h.createDraft.Execute(r.Context(), application.CreateArenaDraftCommand{
		AccountID: accountID,
		Statement: request.Statement,
		Context:   request.Context,
		Category:  request.Category,
		Language:  request.Language,
	})
	if err != nil {
		writeArenaProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, privateArena(*arena))
}

// ListDrafts handles GET /api/v1/me/arena-drafts.
func (h *Handler) ListDrafts(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	drafts, err := h.listDrafts.Execute(r.Context(), accountID)
	if err != nil {
		writeArenaProblem(w, r, err)
		return
	}

	items := make([]privateArenaResponse, 0, len(drafts))
	for _, draft := range drafts {
		items = append(items, privateArena(draft))
	}
	writeJSON(w, http.StatusOK, privateArenaListResponse{Items: items})
}

// GetDraft handles GET /api/v1/me/arena-drafts/{id}.
func (h *Handler) GetDraft(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	arena, err := h.getDraft.Execute(r.Context(), accountID, domain.ArenaID(r.PathValue("id")))
	if err != nil {
		writeArenaProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, privateArena(*arena))
}

// UpdateDraft handles PATCH /api/v1/me/arena-drafts/{id}.
func (h *Handler) UpdateDraft(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	var request updateDraftRequest
	if !decodeBody(w, r, &request) {
		return
	}

	arena, err := h.updateDraft.Execute(r.Context(), application.UpdateArenaDraftCommand{
		AccountID:       accountID,
		ArenaID:         r.PathValue("id"),
		Statement:       request.Statement,
		Context:         request.Context,
		Category:        request.Category,
		Language:        request.Language,
		ExpectedVersion: request.ExpectedVersion,
	})
	if err != nil {
		writeArenaProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, privateArena(*arena))
}

// DeleteDraft handles DELETE /api/v1/me/arena-drafts/{id}.
func (h *Handler) DeleteDraft(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	if err := h.deleteDraft.Execute(r.Context(), application.DeleteArenaDraftCommand{
		AccountID: accountID,
		ArenaID:   r.PathValue("id"),
	}); err != nil {
		writeArenaProblem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PublishDraft handles POST /api/v1/me/arena-drafts/{id}/publish.
func (h *Handler) PublishDraft(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	result, err := h.publish.Execute(r.Context(), application.PublishArenaCommand{
		AccountID: accountID,
		ArenaID:   r.PathValue("id"),
	})
	if err != nil {
		writeArenaProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, privateArena(result.Arena))
}

// CloseArena handles POST /api/v1/me/arenas/{id}/close.
func (h *Handler) CloseArena(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	result, err := h.close.Execute(r.Context(), application.CloseArenaCommand{
		AccountID: accountID,
		ArenaID:   r.PathValue("id"),
	})
	if err != nil {
		writeArenaProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, privateArena(result.Arena))
}

// PublicFeed handles GET /api/v1/arenas.
func (h *Handler) PublicFeed(w http.ResponseWriter, r *http.Request) {
	limit, err := parseFeedLimit(r)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	page, err := h.feed.Execute(r.Context(), application.GetArenaFeedCommand{
		Language: r.URL.Query().Get("language"),
		Category: r.URL.Query().Get("category"),
		Status:   r.URL.Query().Get("status"),
		Cursor:   r.URL.Query().Get("cursor"),
		Limit:    limit,
	})
	if err != nil {
		writeArenaProblem(w, r, err)
		return
	}

	items := make([]publicArenaSummaryResponse, 0, len(page.Arenas))
	for _, arena := range page.Arenas {
		items = append(items, publicArenaSummary(arena))
	}

	var nextCursor *string
	if page.NextCursor != "" {
		nextCursor = &page.NextCursor
	}
	writePublicJSON(w, r, http.StatusOK, publicArenaFeedResponse{Items: items, NextCursor: nextCursor})
}

// GetPublicArena handles GET /api/v1/arenas/{slug}.
func (h *Handler) GetPublicArena(w http.ResponseWriter, r *http.Request) {
	arena, err := h.getPublic.Execute(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeArenaProblem(w, r, err)
		return
	}
	writePublicJSON(w, r, http.StatusOK, publicArena(*arena))
}

// parseFeedLimit reads the optional limit query parameter; an empty value
// selects the default (clamped later by the use case) and anything
// non-numeric or negative is a client error.
func parseFeedLimit(r *http.Request) (int, error) {
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

// challenged applies the anti-bot requirement of one arena action. A nil
// challenger leaves the route as it was, which is the contract the platform
// package documents for a composition that has not installed one.
func (h *Handler) challenged(action turnstile.Action, next http.Handler) http.Handler {
	if h.challenge == nil {
		return next
	}
	return h.challenge.Challenge(action, next)
}

// privateRoute applies the authentication requirement when a security
// manager is configured.
func (h *Handler) privateRoute(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}

// RegisterRoutes wires the arena endpoints into the provided ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/me/arena-drafts", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.CreateDraft))))
	mux.Handle("GET /api/v1/me/arena-drafts", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.ListDrafts))))
	mux.Handle("GET /api/v1/me/arena-drafts/{id}", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.GetDraft))))
	mux.Handle("PATCH /api/v1/me/arena-drafts/{id}", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.UpdateDraft))))
	mux.Handle("DELETE /api/v1/me/arena-drafts/{id}", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.DeleteDraft))))
	// Publishing carries a challenge inside the authentication requirement: a
	// publication is always challenged (the action creates public content and
	// spends INK), and the challenge comes after the session is resolved so
	// that an unauthenticated caller is answered about its session rather than
	// sent to solve a challenge first.
	mux.Handle("POST /api/v1/me/arena-drafts/{id}/publish", withPrivateNoStore(h.privateRoute(h.challenged(turnstile.ActionArenaPublish, http.HandlerFunc(h.PublishDraft)))))
	mux.Handle("POST /api/v1/me/arenas/{id}/close", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.CloseArena))))

	mux.HandleFunc("GET /api/v1/arenas", h.PublicFeed)
	mux.HandleFunc("GET /api/v1/arenas/{slug}", h.GetPublicArena)
}
