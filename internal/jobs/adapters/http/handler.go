// Package http is the inbound HTTP adapter of the jobs module (P15-T06).
//
// It exposes one restricted surface: read the health of the queue, list what is
// dead, and retry one dead job with a stated reason. Every route is
// authenticated, requires an active administrative assignment, is private with
// no-store (THR-CACHE-01), and answers a rejection with an RFC 9457 problem
// carrying a stable code — never the payload of a job, never a provider
// message, never the reason in a form a client could echo back as content.
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// timeLayout is the RFC 3339 rendering of every instant on this surface, so an
// operator can read the response next to a log line without converting.
const timeLayout = time.RFC3339

// maxRetryBodyBytes bounds the retry request body. It carries one identifier
// and one bounded reason, so the bound is generous and still tiny.
const maxRetryBodyBytes = 8 << 10 // 8 KiB

// healthResponse is the operational view of the queue. It carries counts and
// durations: a payload has no field to travel in.
type healthResponse struct {
	GeneratedAt string        `json:"generated_at"`
	Queue       queueResponse `json:"queue"`
}

type queueResponse struct {
	Queued            int64 `json:"queued"`
	Leased            int64 `json:"leased"`
	Succeeded         int64 `json:"succeeded"`
	Dead              int64 `json:"dead"`
	DueNow            int64 `json:"due_now"`
	LagSeconds        int64 `json:"lag_seconds"`
	OldestDeadSeconds int64 `json:"oldest_dead_seconds"`
}

// deadJobResponse is one dead job as the surface exposes it. The payload of the
// job is absent: the query that read it never selected the column.
type deadJobResponse struct {
	JobID         string `json:"job_id"`
	Type          string `json:"type"`
	Version       int    `json:"version"`
	Attempts      int    `json:"attempts"`
	MaxAttempts   int    `json:"max_attempts"`
	LastErrorCode string `json:"last_error_code,omitempty"`
	AgeSeconds    int64  `json:"age_seconds"`
	Retryable     bool   `json:"retryable"`
}

// deadPageResponse is the cursor-free page envelope of the operational surface.
type deadPageResponse struct {
	GeneratedAt string            `json:"generated_at"`
	Total       int64             `json:"total"`
	Items       []deadJobResponse `json:"items"`
}

// retryRequest is what an operator may state when requeueing a dead job: a
// reason, and nothing else. The job, the workload and the transition come from
// the row, not from the caller.
type retryRequest struct {
	Reason string `json:"reason"`
}

// retryResponse carries the transition that happened.
type retryResponse struct {
	JobID string `json:"job_id"`
	Type  string `json:"type"`
	State string `json:"state"`
}

// HandlerConfig aggregates everything the operational surface needs.
type HandlerConfig struct {
	Health    *application.GetQueueHealthUseCase
	ListDead  *application.ListDeadJobsUseCase
	Retry     *application.RetryJobUseCase
	Operators application.OperatorDirectory
	Sessions  application.SessionAgeDirectory
	Security  *security.Manager
	Clock     ports.Clock
}

// Handler serves the restricted operational API.
type Handler struct {
	health    *application.GetQueueHealthUseCase
	listDead  *application.ListDeadJobsUseCase
	retry     *application.RetryJobUseCase
	operators application.OperatorDirectory
	sessions  application.SessionAgeDirectory
	security  *security.Manager
	clock     ports.Clock
}

// NewHandler constructs the handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		health:    cfg.Health,
		listDead:  cfg.ListDead,
		retry:     cfg.Retry,
		operators: cfg.Operators,
		sessions:  cfg.Sessions,
		security:  cfg.Security,
		clock:     cfg.Clock,
	}
}

// setPrivateNoStoreHeaders enforces THR-CACHE-01: everything on this surface is
// restricted, and an intermediate cache holding it would outlive the
// assignment that allowed it.
func setPrivateNoStoreHeaders(w http.ResponseWriter) {
	httpcache.Private(w)
}

// withPrivateNoStore guarantees the cache headers even for a rejection produced
// by middleware before the handler runs.
func withPrivateNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setPrivateNoStoreHeaders(w)
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, document any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(document)
}

// decodeLimitedBody bounds and decodes one JSON request body.
func decodeLimitedBody(w http.ResponseWriter, r *http.Request, document any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRetryBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(document); err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "body_too_large", "request body exceeds the size limit"))
			return false
		}
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_body", "request body must be a JSON object"))
		return false
	}
	return true
}

// requireOperator enforces the restricted audience: an authenticated account
// with an active administrative assignment and a recent authentication.
//
// Both checks happen before anything is read, and the two denials are
// distinguishable by code only — "forbidden" for the assignment, "step_up"
// for the freshness — so an operator whose session aged out knows to
// re-authenticate instead of believing they lost access.
func (h *Handler) requireOperator(w http.ResponseWriter, r *http.Request) (security.AuthIdentity, bool) {
	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return identity, false
	}
	if h.operators == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "operational authorization unavailable"))
		return identity, false
	}
	allowed, err := h.operators.IsOperator(r.Context(), identity.AccountID)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return identity, false
	}
	if !allowed {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "forbidden", "account lacks operational capability"))
		return identity, false
	}
	return identity, true
}

// sessionAge resolves the age of the calling session from a server-observed
// fact. It is what the step-up rule is applied to, and it is never read from
// the request.
// The age travels as a duration and is never rounded to whole seconds: a
// session that authenticated half a second ago is fresh, and truncating it to
// zero would turn it into the "unknown age" the rule refuses.
func (h *Handler) sessionAge(w http.ResponseWriter, r *http.Request, identity security.AuthIdentity) (time.Duration, bool) {
	if h.sessions == nil || h.clock == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "session freshness unavailable"))
		return 0, false
	}
	age, err := h.sessions.SessionAgeAt(r.Context(), identity.SessionID, h.clock.Now())
	if err != nil {
		if errors.Is(err, application.ErrUnknownSession) {
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "step_up_required", "recent authentication required"))
			return 0, false
		}
		_ = httperror.WriteProblem(w, r, err)
		return 0, false
	}
	return age, true
}

// GetHealth handles GET /api/v1/admin/jobs/health.
func (h *Handler) GetHealth(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	if h.health == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "queue health unavailable"))
		return
	}
	report, err := h.health.Execute(r.Context())
	if err != nil {
		writeJobsProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{
		GeneratedAt: report.GeneratedAt.Format(timeLayout),
		Queue: queueResponse{
			Queued:            report.Queue.Queued,
			Leased:            report.Queue.Leased,
			Succeeded:         report.Queue.Succeeded,
			Dead:              report.Queue.Dead,
			DueNow:            report.Queue.DueNow,
			LagSeconds:        report.Queue.LagSeconds,
			OldestDeadSeconds: report.Queue.OldestDeadSeconds,
		},
	})
}

// ListDead handles GET /api/v1/admin/jobs/dead.
func (h *Handler) ListDead(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	if h.listDead == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "dead job listing unavailable"))
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_limit", "limit must be a non-negative integer"))
			return
		}
		limit = parsed
	}
	report, err := h.listDead.Execute(r.Context(), limit)
	if err != nil {
		writeJobsProblem(w, r, err)
		return
	}
	items := make([]deadJobResponse, 0, len(report.Items))
	for _, item := range report.Items {
		items = append(items, deadJobResponse{
			JobID:         item.ID,
			Type:          string(item.Type),
			Version:       item.Version,
			Attempts:      item.Attempts,
			MaxAttempts:   item.MaxAttempts,
			LastErrorCode: string(item.LastErrorCode),
			AgeSeconds:    item.AgeSeconds,
			Retryable:     item.Retryable,
		})
	}
	writeJSON(w, http.StatusOK, deadPageResponse{
		GeneratedAt: report.GeneratedAt.Format(timeLayout),
		Total:       report.Total,
		Items:       items,
	})
}

// Retry handles POST /api/v1/admin/jobs/{id}/retry.
func (h *Handler) Retry(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)
	identity, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	if h.retry == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "job retry unavailable"))
		return
	}
	age, ok := h.sessionAge(w, r, identity)
	if !ok {
		return
	}
	var body retryRequest
	if !decodeLimitedBody(w, r, &body) {
		return
	}

	// The actor is the authenticated account and the age is server-observed:
	// neither is taken from the body, so a caller cannot claim either.
	result, err := h.retry.Execute(r.Context(), application.RetryJobCommand{
		Actor:      identity.AccountID,
		SessionAge: age,
		JobID:      r.PathValue("id"),
		Reason:     body.Reason,
	})
	if err != nil {
		writeJobsProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, retryResponse{
		JobID: result.JobID,
		Type:  string(result.Type),
		State: string(result.State),
	})
}

// RegisterRoutes wires the operational endpoints into the provided mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/admin/jobs/health", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.GetHealth))))
	mux.Handle("GET /api/v1/admin/jobs/dead", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.ListDead))))
	mux.Handle("POST /api/v1/admin/jobs/{id}/retry", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.Retry))))
}

// privateRoute applies the authentication requirement when a security manager
// is configured.
func (h *Handler) privateRoute(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}

// writeJobsProblem maps a jobs failure to its problem kind, keeping the stable
// code the client branches on.
func writeJobsProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrStepUpRequired):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "step_up_required", "recent authentication required"))
	case errors.Is(err, application.ErrUnknownSession):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "step_up_required", "recent authentication required"))
	case errors.Is(err, domain.ErrRetryNotAllowed):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "retry_not_allowed", "workload is not retryable"))
	case errors.Is(err, domain.ErrEmptyActor):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "forbidden", "operation requires an authenticated actor"))
	case errors.Is(err, domain.ErrEmptyReason):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "reason_required", "a retry requires a stated reason"))
	case errors.Is(err, domain.ErrJobNotDead):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "job_not_dead", "job is not in the dead state"))
	case errors.Is(err, application.ErrJobNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "job_not_found", "job was not found"))
	default:
		_ = httperror.WriteProblem(w, r, err)
	}
}
