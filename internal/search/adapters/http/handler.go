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
	"github.com/AlexandreZanata/Regnovum/internal/search/application"
)

type Handler struct {
	arenas    *application.ArenaSearchUseCase
	arguments *application.ArgumentSearchUseCase
}
type HandlerConfig struct {
	Arenas    *application.ArenaSearchUseCase
	Arguments *application.ArgumentSearchUseCase
}

func NewHandler(config HandlerConfig) *Handler {
	return &Handler{arenas: config.Arenas, arguments: config.Arguments}
}

type arenaResult struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Statement   string  `json:"statement"`
	Category    string  `json:"category"`
	Language    string  `json:"language"`
	PublishedAt string  `json:"published_at"`
	Score       float64 `json:"score"`
}
type argumentResult struct {
	ID        string  `json:"id"`
	ArenaID   string  `json:"arena_id"`
	Relation  string  `json:"relation"`
	Content   string  `json:"content"`
	Language  string  `json:"language"`
	CreatedAt string  `json:"created_at"`
	Score     float64 `json:"score"`
}
type arenaPage struct {
	Items      []arenaResult `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}
type argumentPage struct {
	Items      []argumentResult `json:"items"`
	NextCursor *string          `json:"next_cursor"`
}

func (h *Handler) SearchArenas(w http.ResponseWriter, r *http.Request) {
	query, language, cursor, limit, err := params(r)
	if err != nil {
		searchProblem(w, r, err)
		return
	}
	page, err := h.arenas.Execute(r.Context(), query, language, cursor, limit)
	if err != nil {
		searchProblem(w, r, err)
		return
	}
	items := make([]arenaResult, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, arenaResult{ID: item.ID, Slug: item.Slug, Statement: item.Statement, Category: item.Category, Language: item.Language, PublishedAt: item.PublishedAt.UTC().Format(time.RFC3339), Score: item.Score})
	}
	var next *string
	if page.NextCursor != "" {
		next = &page.NextCursor
	}
	writePublic(w, r, arenaPage{Items: items, NextCursor: next})
}

func (h *Handler) SearchArguments(w http.ResponseWriter, r *http.Request) {
	query, language, cursor, limit, err := params(r)
	if err != nil {
		searchProblem(w, r, err)
		return
	}
	page, err := h.arguments.Execute(r.Context(), query, language, cursor, limit)
	if err != nil {
		searchProblem(w, r, err)
		return
	}
	items := make([]argumentResult, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, argumentResult{ID: item.ID, ArenaID: item.ArenaID, Relation: item.Relation, Content: item.Content, Language: item.Language, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339), Score: item.Score})
	}
	var next *string
	if page.NextCursor != "" {
		next = &page.NextCursor
	}
	writePublic(w, r, argumentPage{Items: items, NextCursor: next})
}

func params(r *http.Request) (string, string, string, int, error) {
	query := r.URL.Query().Get("q")
	language := r.URL.Query().Get("language")
	cursor := r.URL.Query().Get("cursor")
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return "", "", "", 0, apperr.New(apperr.KindValidation, "invalid_limit", "limit must be a non-negative integer")
		}
		limit = parsed
	}
	return query, language, cursor, limit, nil
}

func writePublic(w http.ResponseWriter, r *http.Request, document any) {
	body, err := json.Marshal(document)
	if err != nil {
		searchProblem(w, r, err)
		return
	}
	etag := httpcache.Validator(body)
	httpcache.Public(w, 60)
	w.Header().Set("ETag", etag)
	if httpcache.Matches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// RegisterRoutes wires the public search handlers into a ServeMux. Composition
// may choose the module handler directly while the route registry remains the
// contract source of truth.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/search/arenas", h.SearchArenas)
	mux.HandleFunc("GET /api/v1/search/arguments", h.SearchArguments)
}

func searchProblem(w http.ResponseWriter, r *http.Request, err error) {
	code := "search_error"
	kind := apperr.KindInternal
	message := "search is unavailable"
	if errors.Is(err, application.ErrInvalidQuery) || apperr.KindOf(err) == apperr.KindValidation {
		code = "invalid_query"
		kind = apperr.KindValidation
		message = "query, language or limit is invalid"
	}
	if errors.Is(err, application.ErrInvalidCursor) {
		code = "invalid_cursor"
		kind = apperr.KindValidation
		message = "cursor is invalid"
	}
	_ = httperror.WriteProblem(w, r, apperr.New(kind, code, message))
}
