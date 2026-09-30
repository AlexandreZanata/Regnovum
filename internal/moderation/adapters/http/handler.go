// Package http is the inbound HTTP adapter of the moderation module
// (P13-T07). It exposes the user report/appeal routes and the restricted
// case queue/action routes. Every route is authenticated and private with
// no-store (THR-CACHE-01); administrative routes additionally require an
// active assignment, and every denial answers without leaking restricted
// evidence, reporter context or justifications.
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// maxModerationBodyBytes bounds every moderation request body. Bodies
// beyond the bound fail with a validation problem before decoding, so an
// attacker cannot exhaust memory or CPU with an oversized payload.
const maxModerationBodyBytes = 1 << 20 // 1 MiB

// reportRequest is what an authenticated caller may name when contesting
// content. Priorities and outcomes are deliberately absent: volume never
// removes content automatically.
type reportRequest struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Reason     string `json:"reason"`
	Context    string `json:"context,omitempty"`
}

// reportResponse carries the stored report identity with the dedup and
// rate signals. Reporter context never serializes.
type reportResponse struct {
	ReportID        string `json:"report_id"`
	Replayed        bool   `json:"replayed"`
	RateLimited     bool   `json:"rate_limited"`
	ReportsInWindow int    `json:"reports_in_window"`
}

// appealRequest names the contested action with appellant context.
type appealRequest struct {
	ActionID string `json:"action_id"`
	Context  string `json:"context"`
}

// appealResponse carries the stored appeal identity. Appellant context
// never serializes.
type appealResponse struct {
	AppealID string `json:"appeal_id"`
	ActionID string `json:"action_id"`
	Replayed bool   `json:"replayed"`
}

// queueItemResponse is one triage routing entry. Restricted evidence never
// serializes.
type queueItemResponse struct {
	CaseID    string  `json:"case_id"`
	Target    string  `json:"target_type"`
	TargetID  string  `json:"target_id"`
	Status    string  `json:"status"`
	Priority  string  `json:"priority"`
	CreatedAt string  `json:"created_at"`
	ClaimedBy *string `json:"claimed_by,omitempty"`
}

// queueResponse is the cursor page envelope fixed by the API conventions:
// { items, next_cursor }.
type queueResponse struct {
	Items      []queueItemResponse `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

// claimResponse carries the claimed review routing.
type claimResponse struct {
	CaseID    string `json:"case_id"`
	Status    string `json:"status"`
	ClaimedBy string `json:"claimed_by"`
}

// decisionRequest names the explicit measure with rule, justification and
// optional expiry. Severity is never derived: the moderator's choice
// travels untouched.
type decisionRequest struct {
	Action        string  `json:"action"`
	Rule          string  `json:"rule"`
	Justification string  `json:"justification"`
	ExpiresAt     *string `json:"expires_at,omitempty"`
}

// decisionResponse carries the recorded sanction identity. The
// justification never serializes.
type decisionResponse struct {
	ActionID string `json:"action_id"`
	CaseID   string `json:"case_id"`
	Action   string `json:"action"`
}

// HandlerConfig aggregates the moderation use cases and the security
// manager required to serve the moderation API.
type HandlerConfig struct {
	FileReport      *application.FileReportUseCase
	FileAppeal      *application.FileAppealUseCase
	GetQueue        *application.GetCaseQueueUseCase
	ClaimCase       *application.ClaimCaseUseCase
	DecideCase      *application.DecideCaseUseCase
	Roles           application.RoleRepository
	Sessions        application.SessionAgeDirectory
	QueueCodec      *application.QueueCursorCodec
	SecurityManager *security.Manager
	Clock           ports.Clock
	RateLimit       ratelimit.Protector
}

// Handler serves the moderation API.
type Handler struct {
	fileReport *application.FileReportUseCase
	fileAppeal *application.FileAppealUseCase
	getQueue   *application.GetCaseQueueUseCase
	claimCase  *application.ClaimCaseUseCase
	decideCase *application.DecideCaseUseCase
	roles      application.RoleRepository
	sessions   application.SessionAgeDirectory
	codec      *application.QueueCursorCodec
	security   *security.Manager
	clock      ports.Clock
	rateLimit  ratelimit.Protector
}

// NewHandler constructs a moderation HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		fileReport: cfg.FileReport,
		fileAppeal: cfg.FileAppeal,
		getQueue:   cfg.GetQueue,
		claimCase:  cfg.ClaimCase,
		decideCase: cfg.DecideCase,
		roles:      cfg.Roles,
		sessions:   cfg.Sessions,
		codec:      cfg.QueueCodec,
		security:   cfg.SecurityManager,
		clock:      cfg.Clock,
		rateLimit:  cfg.RateLimit,
	}
}

// setPrivateNoStoreHeaders enforces THR-CACHE-01 on moderation routes:
// everything here is authenticated or restricted.
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

// decodeLimitedBody bounds and decodes one JSON request body. Oversized
// bodies fail as validation problems before allocation.
func decodeLimitedBody(w http.ResponseWriter, r *http.Request, document any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxModerationBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(document); err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "body_too_large", "request body exceeds the size limit"))
			return false
		}
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_body", "request body is invalid"))
		return false
	}
	return true
}

// writeModerationProblem maps moderation use-case errors to RFC 9457
// Problem Details with stable codes. Restricted evidence (reporter
// context, justifications, appeal contexts) is never reflected: denials
// name the rule, never the content.
func writeModerationProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrEmptyAccountID):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
	case errors.Is(err, application.ErrNotAuthorized),
		errors.Is(err, application.ErrRoleRevoked),
		errors.Is(err, application.ErrNotAppealOwner),
		errors.Is(err, domain.ErrRoleNotAuthorized):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "forbidden", "account lacks moderation capability"))
	case errors.Is(err, application.ErrStepUpRequired),
		errors.Is(err, application.ErrUnknownSession):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "step_up_required", "recent authentication required"))
	case errors.Is(err, application.ErrTargetNotFound),
		errors.Is(err, application.ErrActionNotFound),
		errors.Is(err, application.ErrAppealNotFound),
		errors.Is(err, application.ErrCaseNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "not_found", "resource was not found"))
	case errors.Is(err, application.ErrTargetRemoved),
		errors.Is(err, application.ErrCaseAlreadyClaimed),
		errors.Is(err, application.ErrAppealAlreadyClaimed),
		errors.Is(err, application.ErrAppealDuplicate),
		errors.Is(err, application.ErrLeaseExpired),
		errors.Is(err, application.ErrInvalidCaseTransition),
		errors.Is(err, application.ErrInvalidAppealTransition),
		errors.Is(err, application.ErrConflictOfInterest):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "conflict", "request conflicts with the current state"))
	case errors.Is(err, application.ErrAppealExpired),
		errors.Is(err, application.ErrActionNotAppealable),
		errors.Is(err, application.ErrSameReviewer),
		errors.Is(err, domain.ErrTargetActionMismatch),
		errors.Is(err, domain.ErrInvalidTargetType),
		errors.Is(err, domain.ErrInvalidReason),
		errors.Is(err, domain.ErrInvalidContext),
		errors.Is(err, domain.ErrEmptyTargetID),
		errors.Is(err, domain.ErrInvalidAction),
		errors.Is(err, domain.ErrInvalidRule),
		errors.Is(err, domain.ErrInvalidJustification),
		errors.Is(err, domain.ErrInvalidExpiry),
		errors.Is(err, domain.ErrInvalidOutcome),
		errors.Is(err, application.ErrInvalidCursor),
		errors.Is(err, application.ErrInvalidQueueFilter):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_request", "request is invalid"))
	default:
		_ = httperror.WriteProblem(w, r, err)
	}
}

// FileReport handles POST /api/v1/me/moderation/reports.
func (h *Handler) FileReport(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.fileReport == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "report filing unavailable"))
		return
	}

	var body reportRequest
	if !decodeLimitedBody(w, r, &body) {
		return
	}

	result, err := h.fileReport.Execute(r.Context(), application.FileReportCommand{
		Reporter: domain.AccountID(identity.AccountID),
		Target:   body.TargetType,
		TargetID: body.TargetID,
		Reason:   body.Reason,
		Context:  body.Context,
	})
	if err != nil {
		writeModerationProblem(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, reportResponse{
		ReportID:        result.ReportID,
		Replayed:        result.Replayed,
		RateLimited:     result.RateLimited,
		ReportsInWindow: result.ReportsInWindow,
	})
}

// FileAppeal handles POST /api/v1/me/moderation/appeals.
func (h *Handler) FileAppeal(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.fileAppeal == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "appeal filing unavailable"))
		return
	}

	var body appealRequest
	if !decodeLimitedBody(w, r, &body) {
		return
	}

	result, err := h.fileAppeal.Execute(r.Context(), application.FileAppealCommand{
		ActionID:  body.ActionID,
		Appellant: identity.AccountID,
		Context:   body.Context,
	})
	if err != nil {
		writeModerationProblem(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, appealResponse{
		AppealID: result.AppealID,
		ActionID: result.ActionID,
		Replayed: result.Replayed,
	})
}

// requireAdminGate enforces the restricted audience of triage routes: the
// caller must hold an active assignment. Revoked and unknown assignments
// deny identically, so the gate never oracles role state.
func (h *Handler) requireAdminGate(w http.ResponseWriter, r *http.Request, identity security.AuthIdentity) bool {
	if h.roles == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "moderation roles unavailable"))
		return false
	}
	assignment, err := h.roles.AssignmentFor(r.Context(), domain.AccountID(identity.AccountID))
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return false
	}
	if assignment == nil || assignment.Revoked {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "forbidden", "account lacks moderation capability"))
		return false
	}

	return h.requireRecentSecondFactor(w, r, identity)
}

// requireRecentSecondFactor enforces the step-up rule of the administrative
// surface (P16-T05): holding the capability is not enough, the session must
// have presented a second factor recently.
//
// The rule is stated here rather than inside the assignment lookup because the
// two facts are different: the assignment says who may, and the session's
// elevation says which session proved it holds the factor. A session that
// never presented one is refused, so an operator whose account has a confirmed
// enrollment but whose session predates it is sent to the step-up endpoint
// instead of being served the queue.
func (h *Handler) requireRecentSecondFactor(w http.ResponseWriter, r *http.Request, identity security.AuthIdentity) bool {
	if h.sessions == nil || h.clock == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "second factor freshness unavailable"))
		return false
	}

	verifiedAt, elevated, err := h.sessions.MFAVerifiedAt(r.Context(), identity.SessionID)
	if err != nil {
		writeModerationProblem(w, r, err)
		return false
	}
	if !elevated {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "mfa_step_up_required", "administrative access requires a recent second factor"))
		return false
	}

	// The window is the module's own step-up window, so the second factor and
	// the high-impact actions expire together instead of drifting apart.
	if h.clock.Now().UTC().Sub(verifiedAt) > domain.StepUpWindow {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "mfa_step_up_required", "administrative access requires a recent second factor"))
		return false
	}
	return true
}

// sessionAge resolves step-up freshness for the calling session from the
// server-observed session creation instant, never from a client claim.
func (h *Handler) sessionAge(w http.ResponseWriter, r *http.Request, identity security.AuthIdentity) (time.Duration, bool) {
	if h.sessions == nil || h.clock == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "session freshness unavailable"))
		return 0, false
	}
	age, err := h.sessions.SessionAgeAt(r.Context(), identity.SessionID, h.clock.Now())
	if err != nil {
		writeModerationProblem(w, r, err)
		return 0, false
	}
	return age, true
}

// GetQueue handles GET /api/v1/moderation/cases.
func (h *Handler) GetQueue(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if !h.requireAdminGate(w, r, identity) {
		return
	}
	if h.getQueue == nil || h.codec == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "triage queue unavailable"))
		return
	}

	query := r.URL.Query()
	limit := 0
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_limit", "limit must be a non-negative integer"))
			return
		}
		limit = parsed
	}

	page, err := h.getQueue.Execute(r.Context(), domain.AccountID(identity.AccountID), query.Get("status"), query.Get("cursor"), limit, h.codec)
	if err != nil {
		writeModerationProblem(w, r, err)
		return
	}

	items := make([]queueItemResponse, 0, len(page.Items))
	for _, entry := range page.Items {
		var claimedBy *string
		if !entry.ClaimedBy.IsZero() {
			holder := entry.ClaimedBy.String()
			claimedBy = &holder
		}
		items = append(items, queueItemResponse{
			CaseID:    entry.CaseID,
			Target:    entry.Target.String(),
			TargetID:  entry.TargetID,
			Status:    string(entry.Status),
			Priority:  entry.Priority,
			CreatedAt: entry.CreatedAt.UTC().Format(time.RFC3339),
			ClaimedBy: claimedBy,
		})
	}

	var nextCursor *string
	if page.NextCursor != "" {
		nextCursor = &page.NextCursor
	}

	writeJSON(w, http.StatusOK, queueResponse{Items: items, NextCursor: nextCursor})
}

// ClaimCase handles POST /api/v1/moderation/cases/{id}/claim.
func (h *Handler) ClaimCase(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if !h.requireAdminGate(w, r, identity) {
		return
	}
	if h.claimCase == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "case claiming unavailable"))
		return
	}
	age, ok := h.sessionAge(w, r, identity)
	if !ok {
		return
	}

	record, err := h.claimCase.Execute(r.Context(), application.ClaimCaseCommand{
		CaseID:     r.PathValue("id"),
		Actor:      identity.AccountID,
		SessionAge: age,
	})
	if err != nil {
		writeModerationProblem(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, claimResponse{
		CaseID:    record.ID,
		Status:    string(record.Status),
		ClaimedBy: record.ClaimedBy.String(),
	})
}

// DecideCase handles POST /api/v1/moderation/cases/{id}/decisions.
func (h *Handler) DecideCase(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if !h.requireAdminGate(w, r, identity) {
		return
	}
	if h.decideCase == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "case decision unavailable"))
		return
	}
	age, ok := h.sessionAge(w, r, identity)
	if !ok {
		return
	}

	var body decisionRequest
	if !decodeLimitedBody(w, r, &body) {
		return
	}

	var expiresAt *time.Time
	if body.ExpiresAt != nil && *body.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err != nil {
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_expires_at", "expires_at must be RFC 3339"))
			return
		}
		expiresAt = &parsed
	}

	result, err := h.decideCase.Execute(r.Context(), application.DecideCaseCommand{
		CaseID:        r.PathValue("id"),
		Actor:         identity.AccountID,
		Action:        body.Action,
		Rule:          body.Rule,
		Justification: body.Justification,
		ExpiresAt:     expiresAt,
		SessionAge:    age,
	})
	if err != nil {
		writeModerationProblem(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, decisionResponse{
		ActionID: result.ActionID,
		CaseID:   result.CaseID,
		Action:   result.Action.String(),
	})
}

// protect applies the rate limit policy of one action, inside the
// authentication middleware so the account dimension is available.
func (h *Handler) protect(action ratelimit.Action, next http.Handler) http.Handler {
	if h.rateLimit == nil {
		return next
	}
	return h.rateLimit.Protect(action, next)
}

// RegisterRoutes wires the moderation endpoints into the provided ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Filing a report spends a moderator's attention, so it carries a policy.
	// Filing an appeal does not need one: an appeal is bound to a moderation
	// action against the caller, and the use case already rejects a second
	// appeal for the same action, which bounds the volume at its source.
	mux.Handle("POST /api/v1/me/moderation/reports", withPrivateNoStore(h.privateRoute(h.protect(ratelimit.ActionReportFile, http.HandlerFunc(h.FileReport)))))
	mux.Handle("POST /api/v1/me/moderation/appeals", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.FileAppeal))))
	mux.Handle("GET /api/v1/moderation/cases", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.GetQueue))))
	mux.Handle("POST /api/v1/moderation/cases/{id}/claim", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.ClaimCase))))
	mux.Handle("POST /api/v1/moderation/cases/{id}/decisions", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.DecideCase))))
}

// privateRoute applies the authentication requirement when a security
// manager is configured.
func (h *Handler) privateRoute(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}
