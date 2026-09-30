// Package http is the inbound HTTP adapter of the arguments module
// (P10-T08). It exposes the authenticated publication, replies and
// withdrawal under /api/v1/me and the cacheable public reads under
// /api/v1. Private routes are no-store (THR-CACHE-01); public reads carry a
// strong ETag and short public cache headers. State-changing POSTs consume
// the Idempotency-Key header and mark replays with Idempotency-Replayed.
package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

const (
	// maxArgumentBodyBytes bounds JSON bodies to 64 KiB.
	maxArgumentBodyBytes = 64 << 10

	// publicCacheSeconds is the short public cache lifetime of argument
	// reads; the strong ETag keeps revalidation cheap.
	publicCacheSeconds = 60
)

// sourceRequest is one requested source of a publication.
type sourceRequest struct {
	URL         string `json:"url"`
	Description string `json:"description"`
}

// publishRequest is the JSON body of a publication or a reply.
type publishRequest struct {
	Relation string          `json:"relation"`
	Content  string          `json:"content"`
	Sources  []sourceRequest `json:"sources"`
}

// publicArgumentResponse is the cache-safe public projection: it never
// carries account identifiers, and content is null when the author
// retracted the argument.
type publicArgumentResponse struct {
	ID        string  `json:"id"`
	ArenaID   string  `json:"arena_id"`
	ParentID  *string `json:"parent_id"`
	Relation  string  `json:"relation"`
	Content   *string `json:"content"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
}

// argumentListItemResponse adds the derived reply count, computed in the
// same statement as the page.
type argumentListItemResponse struct {
	publicArgumentResponse
	ReplyCount int64 `json:"reply_count"`
}

// publishResponse is the outcome of a publication attempt.
type publishResponse struct {
	Argument publicArgumentResponse `json:"argument"`
	Replayed bool                   `json:"replayed"`
}

// withdrawResponse is the outcome of a withdrawal attempt.
type withdrawResponse struct {
	Argument publicArgumentResponse `json:"argument"`
	Replayed bool                   `json:"replayed"`
}

// argumentPageResponse is the cursor page envelope fixed by the API
// conventions.
type argumentPageResponse struct {
	Items      []argumentListItemResponse `json:"items"`
	NextCursor *string                    `json:"next_cursor"`
}

// HandlerConfig aggregates the arguments use cases and the security manager
// required to serve the arguments API.
type HandlerConfig struct {
	PublishUseCase   *application.PublishArgumentUseCase
	WithdrawUseCase  *application.WithdrawArgumentUseCase
	ListArenaUseCase *application.ListArenaArgumentsUseCase
	ListRepliesCase  *application.ListRepliesUseCase
	GetPublicUseCase *application.GetPublicArgumentUseCase
	SecurityManager  *security.Manager
	RateLimit        ratelimit.Protector
}

// Handler serves the versioned arguments API.
type Handler struct {
	publish     *application.PublishArgumentUseCase
	withdraw    *application.WithdrawArgumentUseCase
	listArena   *application.ListArenaArgumentsUseCase
	listReplies *application.ListRepliesUseCase
	getPublic   *application.GetPublicArgumentUseCase
	security    *security.Manager
	rateLimit   ratelimit.Protector
}

// NewHandler constructs an arguments HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		publish:     cfg.PublishUseCase,
		withdraw:    cfg.WithdrawUseCase,
		listArena:   cfg.ListArenaUseCase,
		listReplies: cfg.ListRepliesCase,
		getPublic:   cfg.GetPublicUseCase,
		security:    cfg.SecurityManager,
		rateLimit:   cfg.RateLimit,
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

// writeCacheableJSON renders a public document with a strong ETag computed
// over its canonical JSON body and honors If-None-Match with 304. The
// helper mirrors the other public reads; the copy keeps the adapters
// independent.
func writeCacheableJSON(w http.ResponseWriter, r *http.Request, document any) {
	body, err := json.Marshal(document)
	if err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "failed to encode response"))
		return
	}

	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`

	httpcache.Public(w, publicCacheSeconds)
	w.Header().Set("ETag", etag)

	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// etagMatches implements the weak comparison of RFC 9110 for If-None-Match.
func etagMatches(header, etag string) bool {
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

// writeArgumentProblem maps arguments errors to RFC 9457 Problem Details
// with stable codes; domain codes surface lowercased so clients depend on
// the code, not on titles.
func writeArgumentProblem(w http.ResponseWriter, r *http.Request, err error) {
	var domainErr domain.DomainError
	hasDomainError := errors.As(err, &domainErr)
	switch {
	case errors.Is(err, application.ErrArgumentNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "argument_not_found", "argument not found"))
	case errors.Is(err, application.ErrParentNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "parent_not_found", "parent argument not found"))
	case errors.Is(err, application.ErrParentNotAvailable):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "parent_not_available", "the parent argument is not available for replies"))
	case errors.Is(err, application.ErrReplyDepthExceeded):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "reply_depth_exceeded", "the reply exceeds the accepted depth"))
	case errors.Is(err, application.ErrArgumentNotWithdrawable):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "argument_not_withdrawable", "the argument cannot be withdrawn in its current state"))
	case errors.Is(err, application.ErrInsufficientInk):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "insufficient_ink", "the account cannot pay the publication cost"))
	case errors.Is(err, application.ErrAccountNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "account_not_found", "account not found"))
	case errors.Is(err, application.ErrAccountNotEligible):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "account_not_eligible", "the account is not eligible to publish"))
	case errors.Is(err, application.ErrAccountSuspended):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "account_suspended", "the account is suspended"))
	case errors.Is(err, application.ErrArenaNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "arena_not_found", "arena not found"))
	case errors.Is(err, application.ErrArenaNotOpen):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "arena_not_open", "the arena accepts no new arguments"))
	case errors.Is(err, application.ErrInvalidCursor):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_cursor", "cursor is invalid"))
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

// decodeBody reads a bounded JSON body into target.
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxArgumentBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON within the size limit"))
		return false
	}
	return true
}

// publicArgument converts the public read projection into the response
// document.
func publicArgument(argument application.PublicArgument) publicArgumentResponse {
	response := publicArgumentResponse{
		ID:        argument.ID.String(),
		ArenaID:   argument.ArenaID.String(),
		Relation:  argument.Relation.String(),
		Status:    argument.Status,
		CreatedAt: argument.CreatedAt.UTC().Format(time.RFC3339),
	}
	if !argument.ParentID.IsZero() {
		parent := argument.ParentID.String()
		response.ParentID = &parent
	}
	if argument.Content != nil {
		content := argument.Content.String()
		response.Content = &content
	}
	return response
}

// storedArgument converts the write result into the response document: the
// content is withheld when the argument is no longer published.
func storedArgument(argument application.PublishedArgument) publicArgumentResponse {
	response := publicArgumentResponse{
		ID:        argument.ID.String(),
		ArenaID:   argument.ArenaID.String(),
		Relation:  argument.Relation.String(),
		Status:    argument.Status,
		CreatedAt: argument.CreatedAt.UTC().Format(time.RFC3339),
	}
	if !argument.ParentID.IsZero() {
		parent := argument.ParentID.String()
		response.ParentID = &parent
	}
	if argument.Status == "published" && !argument.Content.IsZero() {
		content := argument.Content.String()
		response.Content = &content
	}
	return response
}

// publish handles both publications and replies: the parent comes from the
// reply route when present.
func (h *Handler) publishArgument(w http.ResponseWriter, r *http.Request, parentID string) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "missing_idempotency_key", "the Idempotency-Key header is required"))
		return
	}

	var request publishRequest
	if !decodeBody(w, r, &request) {
		return
	}

	sources := make([]application.SourceCommand, 0, len(request.Sources))
	for _, source := range request.Sources {
		sources = append(sources, application.SourceCommand{URL: source.URL, Description: source.Description})
	}

	result, err := h.publish.Execute(r.Context(), application.PublishArgumentCommand{
		AccountID:      accountID,
		ArenaID:        r.PathValue("id"),
		ParentID:       parentID,
		Relation:       request.Relation,
		Content:        request.Content,
		Sources:        sources,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeArgumentProblem(w, r, err)
		return
	}

	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, status, publishResponse{Argument: storedArgument(result.Argument), Replayed: result.Replayed})
}

// PublishArgument handles POST /api/v1/me/arenas/{id}/arguments.
func (h *Handler) PublishArgument(w http.ResponseWriter, r *http.Request) {
	h.publishArgument(w, r, "")
}

// ReplyToArgument handles POST /api/v1/me/arenas/{id}/arguments/{argumentID}/replies.
// The Arena travels in the path so the use case keeps validating that the
// parent belongs to it (cross-Arena replies stay refused).
func (h *Handler) ReplyToArgument(w http.ResponseWriter, r *http.Request) {
	h.publishArgument(w, r, r.PathValue("argumentID"))
}

// WithdrawArgument handles POST /api/v1/me/arguments/{id}/withdraw.
func (h *Handler) WithdrawArgument(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	result, err := h.withdraw.Execute(r.Context(), application.WithdrawArgumentCommand{
		AccountID:  accountID,
		ArgumentID: r.PathValue("id"),
	})
	if err != nil {
		writeArgumentProblem(w, r, err)
		return
	}

	if result.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusOK, withdrawResponse{Argument: storedArgument(result.Argument), Replayed: result.Replayed})
}

// ListArenaArguments handles GET /api/v1/arenas/{id}/arguments.
func (h *Handler) ListArenaArguments(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	page, err := h.listArena.Execute(r.Context(), application.ListArenaArgumentsQuery{
		ArenaID:  r.PathValue("id"),
		Relation: r.URL.Query().Get("relation"),
		Cursor:   r.URL.Query().Get("cursor"),
		Limit:    limit,
	})
	if err != nil {
		writeArgumentProblem(w, r, err)
		return
	}
	writeCacheableJSON(w, r, pageResponse(page))
}

// ListReplies handles GET /api/v1/arguments/{id}/replies.
func (h *Handler) ListReplies(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	page, err := h.listReplies.Execute(r.Context(), application.ListRepliesQuery{
		ParentID: r.PathValue("id"),
		Cursor:   r.URL.Query().Get("cursor"),
		Limit:    limit,
	})
	if err != nil {
		writeArgumentProblem(w, r, err)
		return
	}
	writeCacheableJSON(w, r, pageResponse(page))
}

// GetPublicArgument handles GET /api/v1/arguments/{id}.
func (h *Handler) GetPublicArgument(w http.ResponseWriter, r *http.Request) {
	argument, err := h.getPublic.Execute(r.Context(), application.GetPublicArgumentQuery{
		ArgumentID: r.PathValue("id"),
	})
	if err != nil {
		writeArgumentProblem(w, r, err)
		return
	}
	writeCacheableJSON(w, r, publicArgument(*argument))
}

// pageResponse converts a page into the response envelope.
func pageResponse(page *application.ArgumentPage) argumentPageResponse {
	items := make([]argumentListItemResponse, 0, len(page.Arguments))
	for _, argument := range page.Arguments {
		items = append(items, argumentListItemResponse{
			publicArgumentResponse: publicArgument(argument),
			ReplyCount:             argument.ReplyCount,
		})
	}
	var nextCursor *string
	if page.NextCursor != "" {
		nextCursor = &page.NextCursor
	}
	return argumentPageResponse{Items: items, NextCursor: nextCursor}
}

// parseLimit reads the optional limit query parameter; an empty value
// selects the default (clamped later by the use case) and anything
// non-numeric or negative is a client error.
func parseLimit(r *http.Request) (int, error) {
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

// privateRoute applies the authentication requirement when a security
// manager is configured.
func (h *Handler) privateRoute(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}

// protect applies the rate limit policy of one action. It is applied inside
// privateRoute so that the policy can bound the authenticated account as well
// as the network address.
func (h *Handler) protect(action ratelimit.Action, next http.Handler) http.Handler {
	if h.rateLimit == nil {
		return next
	}
	return h.rateLimit.Protect(action, next)
}

// RegisterRoutes wires the arguments endpoints into the provided ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Publishing and replying share one policy: both create user-generated
	// content, and the ledger already bounds what publishing costs in INK. The
	// withdrawal route is deliberately not throttled: it removes content, and
	// its state machine already rejects a second withdrawal.
	mux.Handle("POST /api/v1/me/arenas/{id}/arguments", withPrivateNoStore(h.privateRoute(h.protect(ratelimit.ActionArgumentPublish, http.HandlerFunc(h.PublishArgument)))))
	mux.Handle("POST /api/v1/me/arenas/{id}/arguments/{argumentID}/replies", withPrivateNoStore(h.privateRoute(h.protect(ratelimit.ActionArgumentPublish, http.HandlerFunc(h.ReplyToArgument)))))
	mux.Handle("POST /api/v1/me/arguments/{id}/withdraw", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.WithdrawArgument))))

	mux.HandleFunc("GET /api/v1/arenas/{id}/arguments", h.ListArenaArguments)
	mux.HandleFunc("GET /api/v1/arguments/{id}/replies", h.ListReplies)
	mux.HandleFunc("GET /api/v1/arguments/{id}", h.GetPublicArgument)
}
