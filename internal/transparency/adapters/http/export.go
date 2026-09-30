package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/application"
)

// exportCacheSeconds is the public cache lifetime of the export pages; the
// strong ETag over the content hash keeps revalidation cheap.
const exportCacheSeconds = 60

// ExportHandlerConfig aggregates the collaborator required to serve the
// versioned public Arena export.
type ExportHandlerConfig struct {
	UseCase *application.GetArenaExportUseCase
}

// ExportHandler serves the cacheable versioned public Arena export.
type ExportHandler struct {
	getExport *application.GetArenaExportUseCase
}

// NewExportHandler constructs the export HTTP handler.
func NewExportHandler(cfg ExportHandlerConfig) *ExportHandler {
	return &ExportHandler{getExport: cfg.UseCase}
}

// arenaExportResponse is the v1 export document: schema version, Arena
// identity and state, public aggregates and one bounded argument page.
type arenaExportResponse struct {
	SchemaVersion int                             `json:"schema_version"`
	Arena         arenaExportArenaResponse        `json:"arena"`
	Positions     arenaExportPositionsResponse    `json:"positions"`
	Influence     arenaExportInfluenceResponse    `json:"influence"`
	Arguments     arenaExportArgumentPageResponse `json:"arguments"`
}

// arenaExportArenaResponse is the public Arena projection: no creator, no
// internal version.
type arenaExportArenaResponse struct {
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

// arenaExportPositionsResponse is the aggregated position picture. When the
// sample is suppressed every count is zero and suppressed is true.
type arenaExportPositionsResponse struct {
	ParticipantsTotal int64                   `json:"participants_total"`
	Suppressed        bool                    `json:"suppressed"`
	PositionChanges   int64                   `json:"position_changes"`
	Initial           arenaExportDistribution `json:"initial"`
	Current           arenaExportDistribution `json:"current"`
}

// arenaExportDistribution is one count distribution.
type arenaExportDistribution struct {
	Agree     int64 `json:"agree"`
	Disagree  int64 `json:"disagree"`
	Undecided int64 `json:"undecided"`
}

// arenaExportInfluenceResponse is the Arena-level valid influence count.
type arenaExportInfluenceResponse struct {
	ValidAttributions int64 `json:"valid_attributions"`
	InfluencedAuthors int64 `json:"influenced_authors"`
}

// arenaExportArgumentInfluence is the public count of one argument.
type arenaExportArgumentInfluence struct {
	ValidAttributions int64 `json:"valid_attributions"`
	DistinctPeople    int64 `json:"distinct_people"`
}

// arenaExportSourceResponse is one structured source.
type arenaExportSourceResponse struct {
	URL         string  `json:"url"`
	Description *string `json:"description"`
}

// arenaExportArgumentResponse is one public argument. Content and sources
// are withheld (null and empty) while the argument is withdrawn.
type arenaExportArgumentResponse struct {
	ID          string                       `json:"id"`
	ParentID    *string                      `json:"parent_id"`
	Relation    string                       `json:"relation"`
	Content     *string                      `json:"content"`
	Status      string                       `json:"status"`
	CreatedAt   string                       `json:"created_at"`
	WithdrawnAt *string                      `json:"withdrawn_at"`
	Sources     []arenaExportSourceResponse  `json:"sources"`
	Influence   arenaExportArgumentInfluence `json:"influence"`
}

// arenaExportArgumentPageResponse is the cursor page envelope of the
// argument list, following the API pagination convention.
type arenaExportArgumentPageResponse struct {
	Items      []arenaExportArgumentResponse `json:"items"`
	NextCursor *string                       `json:"next_cursor"`
}

// exportDocument converts the application page into the versioned response
// document. The projection carries public data only: no account identifier,
// individual position, individual change history or attributor identity
// ever serializes.
func exportDocument(page *application.ExportPage) arenaExportResponse {
	arguments := make([]arenaExportArgumentResponse, 0, len(page.Arguments))
	for _, argument := range page.Arguments {
		response := arenaExportArgumentResponse{
			ID:          argument.ID,
			Relation:    argument.Relation,
			Status:      argument.Status,
			CreatedAt:   argument.CreatedAt.UTC().Format(time.RFC3339),
			WithdrawnAt: optionalInstant(argument.WithdrawnAt),
			Sources:     make([]arenaExportSourceResponse, 0, len(argument.Sources)),
			Influence: arenaExportArgumentInfluence{
				ValidAttributions: argument.Influence.ValidAttributions,
				DistinctPeople:    argument.Influence.DistinctPeople,
			},
		}
		if argument.ParentID != "" {
			parent := argument.ParentID
			response.ParentID = &parent
		}
		// Defense in depth: content and sources only serialize while the
		// argument is published, whatever the upstream projection carried.
		if argument.Status == "published" {
			response.Content = argument.Content
			for _, source := range argument.Sources {
				response.Sources = append(response.Sources, arenaExportSourceResponse{
					URL:         source.URL,
					Description: source.Description,
				})
			}
		}
		arguments = append(arguments, response)
	}

	var nextCursor *string
	if page.NextCursor != "" {
		cursor := page.NextCursor
		nextCursor = &cursor
	}

	return arenaExportResponse{
		SchemaVersion: page.SchemaVersion,
		Arena: arenaExportArenaResponse{
			ID:          page.Arena.ID,
			Slug:        page.Arena.Slug,
			Statement:   page.Arena.Statement,
			Context:     optionalString(page.Arena.Context),
			Category:    page.Arena.Category,
			Language:    page.Arena.Language,
			Status:      page.Arena.Status,
			PublishedAt: page.Arena.PublishedAt.UTC().Format(time.RFC3339),
			ClosesAt:    optionalInstant(page.Arena.ClosesAt),
		},
		Positions: arenaExportPositionsResponse{
			ParticipantsTotal: page.Positions.Participants,
			Suppressed:        page.Positions.Suppressed,
			PositionChanges:   page.Positions.PositionChanges,
			Initial: arenaExportDistribution{
				Agree:     page.Positions.Initial.Agree,
				Disagree:  page.Positions.Initial.Disagree,
				Undecided: page.Positions.Initial.Undecided,
			},
			Current: arenaExportDistribution{
				Agree:     page.Positions.Current.Agree,
				Disagree:  page.Positions.Current.Disagree,
				Undecided: page.Positions.Current.Undecided,
			},
		},
		Influence: arenaExportInfluenceResponse{
			ValidAttributions: page.Influence.ValidAttributions,
			InfluencedAuthors: page.Influence.InfluencedAuthors,
		},
		Arguments: arenaExportArgumentPageResponse{
			Items:      arguments,
			NextCursor: nextCursor,
		},
	}
}

// ServeExport handles GET /api/v1/arenas/{id}/export. The document is
// public and cacheable, unknown Arenas answer 404 without reflection and
// pagination stays within the contract bounds.
func (h *ExportHandler) ServeExport(w http.ResponseWriter, r *http.Request) {
	if h.getExport == nil {
		_ = httperror.WriteProblem(w, r, errExportUnavailable())
		return
	}

	limit, err := parseExportLimit(r)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	page, err := h.getExport.Execute(r.Context(), application.GetArenaExportQuery{
		ArenaID: r.PathValue("id"),
		Cursor:  r.URL.Query().Get("cursor"),
		Limit:   limit,
	})
	if err != nil {
		writeExportProblem(w, r, err)
		return
	}

	body, err := json.Marshal(exportDocument(page))
	if err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "failed to encode response"))
		return
	}
	writeExportJSON(w, r, body)
}

// writeExportJSON writes the public export with a strong ETag computed over
// the content hash of the exact body and honors If-None-Match with 304.
func writeExportJSON(w http.ResponseWriter, r *http.Request, body []byte) {
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`

	httpcache.Public(w, exportCacheSeconds)
	w.Header().Set("ETag", etag)

	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// writeExportProblem maps export errors to RFC 9457 Problem Details with
// stable codes; the requested Arena or cursor is never echoed back.
func writeExportProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrArenaNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "arena_not_found", "arena not found"))
	case errors.Is(err, application.ErrInvalidCursor):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_cursor", "cursor is invalid"))
	default:
		_ = httperror.WriteProblem(w, r, err)
	}
}

// parseExportLimit reads the optional limit query parameter; an empty value
// selects the default (clamped later by the use case) and anything
// non-numeric or negative is a client error.
func parseExportLimit(r *http.Request) (int, error) {
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

// errExportUnavailable reports a wiring gap without leaking internals.
func errExportUnavailable() error {
	return apperr.New(apperr.KindInternal, "server_error", "arena export unavailable")
}

// RegisterRoutes wires the versioned public Arena export into the provided
// ServeMux.
func (h *ExportHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/arenas/{id}/export", h.ServeExport)
}

// optionalString renders an absent optional string as null.
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// optionalInstant renders an absent instant as null.
func optionalInstant(instant *time.Time) *string {
	if instant == nil {
		return nil
	}
	formatted := instant.UTC().Format(time.RFC3339)
	return &formatted
}
