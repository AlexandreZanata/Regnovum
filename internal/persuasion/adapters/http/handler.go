// Package http is the inbound HTTP adapter of the persuasion module
// (P11-T06). It exposes the authenticated attribution recording under
// /api/v1/me and the public count reads under /api/v1.
//
// Privacy is structural here, not a formatting choice: the public documents
// are explicit structs that declare only counts and public identifiers
// (argument, Arena, username), so no attributor identifier, account
// identifier or email can reach a response even by accident (BR §5.1,
// REQ-PERS-05). Private routes are no-store (THR-CACHE-01); public counts are
// short-cache reads with a strong ETag, like the other public reads.
package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

const (
	// maxAttributionBodyBytes bounds JSON bodies to 16 KiB: a selection is at
	// most three opaque identifiers.
	maxAttributionBodyBytes = 16 << 10

	// publicCacheSeconds is the short public cache lifetime of count reads;
	// the strong ETag keeps revalidation cheap.
	publicCacheSeconds = 60
)

// recordAttributionsRequest is the JSON body of an attribution selection:
// zero to three argument identifiers credited by one position change.
type recordAttributionsRequest struct {
	ArgumentIDs []string `json:"argument_ids"`
}

// recordAttributionsResponse is the recorded selection. It echoes identifiers
// the caller already knows and never exposes who else credited the arguments.
type recordAttributionsResponse struct {
	ArgumentIDs []string `json:"argument_ids"`
	Replayed    bool     `json:"replayed"`
}

// argumentMetricsResponse is the public count document of one argument: two
// counts and the derivation instant, nothing else.
type argumentMetricsResponse struct {
	ValidAttributions int64  `json:"valid_attributions"`
	DistinctPeople    int64  `json:"distinct_people"`
	CheckedAt         string `json:"checked_at"`
}

// dimensionReputationResponse is one bucket of a public reputation
// distribution (category or language).
type dimensionReputationResponse struct {
	Label             string `json:"label"`
	DistinctPeople    int64  `json:"distinct_people"`
	ValidAttributions int64  `json:"valid_attributions"`
}

// arenaReputationResponse is one Arena slice of a public reputation. The
// Arena identifier is public; the author and the attributors are not part of
// the document.
type arenaReputationResponse struct {
	ArenaID           string `json:"arena_id"`
	Category          string `json:"category"`
	Language          string `json:"language"`
	DistinctPeople    int64  `json:"distinct_people"`
	ValidAttributions int64  `json:"valid_attributions"`
}

// profileReputationResponse is the public reputation document of one profile.
// It is addressed by the canonical username; the internal author identifier
// used to derive the facts is never serialized. The headline
// influenced_people is the sum of the per-Arena distinct people (BR §6), so
// the document is self-consistent and recomputable from arenas.
type profileReputationResponse struct {
	Username          string                        `json:"username"`
	InfluencedPeople  int64                         `json:"influenced_people"`
	ValidAttributions int64                         `json:"valid_attributions"`
	Arenas            []arenaReputationResponse     `json:"arenas"`
	ByCategory        []dimensionReputationResponse `json:"by_category"`
	ByLanguage        []dimensionReputationResponse `json:"by_language"`
	CheckedAt         string                        `json:"checked_at"`
}

// attributionSignalResponse is one advisory signal on the restricted
// moderation surface. It carries the kind, the counterpart account involved
// and the counts that crossed the threshold — never a score, a severity, a
// weight or a recommended action: a signal is a reason to look, and a human
// decides (MODERATION §5, §10). Kind-specific counts are omitted when they do
// not apply.
type attributionSignalResponse struct {
	Kind             string `json:"kind"`
	CounterpartID    string `json:"counterpart_id"`
	MutualEvents     int64  `json:"mutual_events,omitempty"`
	DominantEvents   int64  `json:"dominant_events,omitempty"`
	ShareBasisPoints int64  `json:"share_basis_points,omitempty"`
	Changes          int64  `json:"changes,omitempty"`
	Reversals        int64  `json:"reversals,omitempty"`
}

// attributionSignalsResponse is the restricted assessment document: the
// subject, the policy revision and window that produced the signals, and the
// signals themselves. It is served only to authorized moderators with the
// private cache policy (THR-CACHE-01) and is never part of a public page,
// metric or export (CONSTITUTION §Dados pessoais).
type attributionSignalsResponse struct {
	AuthorID      string                      `json:"author_id"`
	PolicyVersion string                      `json:"policy_version"`
	WindowSeconds int64                       `json:"window_seconds"`
	CheckedAt     string                      `json:"checked_at"`
	Signals       []attributionSignalResponse `json:"signals"`
}

// HandlerConfig aggregates the persuasion use cases and the security manager
// required to serve the persuasion API.
type HandlerConfig struct {
	RecordUseCase          *application.RecordAttributionsUseCase
	ArgumentMetricsUseCase *application.GetArgumentMetricsUseCase
	ProfileReputationCase  *application.GetProfileReputationUseCase
	SignalsUseCase         *application.GetAttributionSignalsUseCase
	SecurityManager        *security.Manager
}

// Handler serves the versioned persuasion API.
type Handler struct {
	record            *application.RecordAttributionsUseCase
	argumentMetrics   *application.GetArgumentMetricsUseCase
	profileReputation *application.GetProfileReputationUseCase
	signals           *application.GetAttributionSignalsUseCase
	security          *security.Manager
}

// NewHandler constructs a persuasion HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		record:            cfg.RecordUseCase,
		argumentMetrics:   cfg.ArgumentMetricsUseCase,
		profileReputation: cfg.ProfileReputationCase,
		signals:           cfg.SignalsUseCase,
		security:          cfg.SecurityManager,
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

// writeCacheableJSON serves a public document whose body carries the instant
// of its own derivation.
//
// The validator covers that document without the instant, and it is weak.
// A strong validator over the whole body was wrong, and the mistake had a
// measurable cost: the instant moves on every request, so two reads of the
// same facts never compared equal and a client revalidating after the cache
// window was sent the entire document again — the ETag saved nothing at all.
// RFC 9110 section 8.8.1 requires a strong validator to be unique across every
// representation, which no validator can be while the annotation is part of
// the body; section 8.8.2 is for exactly this case, a representation that
// stays equivalent while its metadata moves, and If-None-Match performs the
// weak comparison for GET.
//
// facts is the document with the annotation left empty; document is the same
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

// writePersuasionProblem maps persuasion errors to RFC 9457 Problem Details
// with stable codes; domain codes surface lowercased so clients depend on the
// code, not on titles. A missing profile, change or argument is a plain 404
// and never names the identity that was looked up.
func writePersuasionProblem(w http.ResponseWriter, r *http.Request, err error) {
	var domainErr domain.DomainError
	hasDomainError := errors.As(err, &domainErr)
	switch {
	case errors.Is(err, application.ErrChangeNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "change_not_found", "position change not found"))
	case errors.Is(err, application.ErrArgumentNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "argument_not_found", "argument not found"))
	case errors.Is(err, application.ErrProfileNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "profile_not_found", "profile not found"))
	case errors.Is(err, application.ErrNotAuthorized):
		// The restricted surface answers the same way for every unauthorized
		// caller: whether a signal exists is itself restricted information.
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "not_authorized", "the account may not read attribution signals"))
	case errors.Is(err, application.ErrInvalidAuthorID):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_author_id", "author identifier is invalid"))
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
	r.Body = http.MaxBytesReader(w, r.Body, maxAttributionBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON within the size limit"))
		return false
	}
	return true
}

// RecordAttributions handles POST /api/v1/me/position-changes/{id}/attributions.
// The change comes from the path and is loaded scoped to the authenticated
// account by the use case, so a foreign change stays indistinguishable from a
// missing one. An empty selection is a valid skip.
func (h *Handler) RecordAttributions(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	var request recordAttributionsRequest
	if !decodeBody(w, r, &request) {
		return
	}

	result, err := h.record.Execute(r.Context(), application.RecordAttributionsCommand{
		AccountID:   accountID,
		ChangeID:    r.PathValue("id"),
		ArgumentIDs: request.ArgumentIDs,
	})
	if err != nil {
		writePersuasionProblem(w, r, err)
		return
	}

	argumentIDs := make([]string, 0, len(result.ArgumentIDs))
	for _, argumentID := range result.ArgumentIDs {
		argumentIDs = append(argumentIDs, argumentID.String())
	}

	status := http.StatusCreated
	if result.Replayed {
		// The selection was already recorded: the response resolves the
		// recorded set instead of writing a second time.
		status = http.StatusOK
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, status, recordAttributionsResponse{ArgumentIDs: argumentIDs, Replayed: result.Replayed})
}

// GetArgumentMetrics handles GET /api/v1/arguments/{id}/attributions.
func (h *Handler) GetArgumentMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := h.argumentMetrics.Execute(r.Context(), application.GetArgumentMetricsQuery{
		ArgumentID: r.PathValue("id"),
	})
	if err != nil {
		writePersuasionProblem(w, r, err)
		return
	}

	facts := argumentMetricsResponse{
		ValidAttributions: metrics.ValidAttributions,
		DistinctPeople:    metrics.DistinctPeople,
	}
	served := facts
	served.CheckedAt = metrics.CheckedAt.UTC().Format(time.RFC3339)
	writeCacheableJSON(w, r, facts, served)
}

// GetProfileReputation handles GET /api/v1/profiles/{username}/reputation.
func (h *Handler) GetProfileReputation(w http.ResponseWriter, r *http.Request) {
	reputation, err := h.profileReputation.Execute(r.Context(), application.GetProfileReputationQuery{
		Username: r.PathValue("username"),
	})
	if err != nil {
		writePersuasionProblem(w, r, err)
		return
	}

	arenas := make([]arenaReputationResponse, 0, len(reputation.Arenas))
	for _, arena := range reputation.Arenas {
		arenas = append(arenas, arenaReputationResponse{
			ArenaID:           arena.ArenaID.String(),
			Category:          arena.Category,
			Language:          arena.Language,
			DistinctPeople:    arena.DistinctPeople,
			ValidAttributions: arena.ValidAttributions,
		})
	}

	facts := profileReputationResponse{
		Username:          reputation.Username,
		InfluencedPeople:  reputation.InfluencedPeople(),
		ValidAttributions: reputation.TotalValidAttributions(),
		Arenas:            arenas,
		ByCategory:        dimensionResponses(reputation.ByCategory()),
		ByLanguage:        dimensionResponses(reputation.ByLanguage()),
	}
	served := facts
	served.CheckedAt = reputation.CheckedAt.UTC().Format(time.RFC3339)
	writeCacheableJSON(w, r, facts, served)
}

// GetAttributionSignals handles GET
// /api/v1/moderation/attribution-signals/{authorID}. The route is
// authenticated, and the use case authorizes the moderator before reading
// anything: a signal never blocks, reweights or hides content on its own.
func (h *Handler) GetAttributionSignals(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	accountID, ok := h.identity(r)
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	assessment, err := h.signals.Execute(r.Context(), application.AssessAttributionSignalsQuery{
		ActorAccountID: accountID,
		SubjectID:      r.PathValue("authorID"),
	})
	if err != nil {
		writePersuasionProblem(w, r, err)
		return
	}

	signals := make([]attributionSignalResponse, 0, len(assessment.Signals))
	for _, signal := range assessment.Signals {
		signals = append(signals, attributionSignalResponse{
			Kind:             string(signal.Kind),
			CounterpartID:    signal.Counterpart.String(),
			MutualEvents:     signal.MutualEvents,
			DominantEvents:   signal.DominantEvents,
			ShareBasisPoints: signal.ShareBasisPoints,
			Changes:          signal.Changes,
			Reversals:        signal.Reversals,
		})
	}

	writeJSON(w, http.StatusOK, attributionSignalsResponse{
		AuthorID:      assessment.Subject.String(),
		PolicyVersion: assessment.PolicyVersion,
		WindowSeconds: int64(assessment.Window / time.Second),
		CheckedAt:     assessment.AssessedAt.UTC().Format(time.RFC3339),
		Signals:       signals,
	})
}

// dimensionResponses converts a distribution into its response documents.
func dimensionResponses(distribution []application.DimensionReputation) []dimensionReputationResponse {
	responses := make([]dimensionReputationResponse, 0, len(distribution))
	for _, bucket := range distribution {
		responses = append(responses, dimensionReputationResponse{
			Label:             bucket.Label,
			DistinctPeople:    bucket.DistinctPeople,
			ValidAttributions: bucket.ValidAttributions,
		})
	}
	return responses
}

// privateRoute applies the authentication requirement when a security
// manager is configured.
func (h *Handler) privateRoute(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}

// RegisterRoutes wires the persuasion endpoints into the provided ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/me/position-changes/{id}/attributions", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.RecordAttributions))))

	// The restricted moderation surface: authenticated, private (no-store)
	// and moderator-only inside the use case.
	mux.Handle("GET /api/v1/moderation/attribution-signals/{authorID}", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.GetAttributionSignals))))

	mux.HandleFunc("GET /api/v1/arguments/{id}/attributions", h.GetArgumentMetrics)
	mux.HandleFunc("GET /api/v1/profiles/{username}/reputation", h.GetProfileReputation)
}
