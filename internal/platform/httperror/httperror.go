// Package httperror maps the HTTP-free application error vocabulary
// (internal/platform/apperr) to RFC 9457 Problem Details responses
// (P02-T03, localized in P02-T08). It is the only place where the
// vocabulary gains HTTP semantics: statuses, media type and public bodies
// live here.
//
// Security invariants, enforced by table tests:
//   - the wrapped internal cause never serializes into the body;
//   - unknown errors render as generic internal problems, never echoing
//     foreign error strings to the client;
//   - every problem carries a stable code, a title derived from the kind,
//     and the request correlation ID.
//
// Since P02-T08, titles localize through the typed i18n catalog using the
// request's negotiated interface locale (internal/platform/locale), and
// problems are built from the *http.Request: the request ID and the locale
// come from the request context, so handlers cannot mix up the values.
package httperror

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/requestid"
)

// mediaType is the RFC 9457 problem media type mandated by the master plan.
const mediaType = "application/problem+json"

// genericInternalMessage keeps unexpected failures generic when the
// catalog itself is missing the internal title: the public body stays
// generic; details go to logs, not to clients.
const genericInternalMessage = "internal error"

// statusByKind fixes the public HTTP status for each stable kind.
var statusByKind = map[apperr.Kind]int{
	apperr.KindValidation:   http.StatusBadRequest,
	apperr.KindUnauthorized: http.StatusUnauthorized,
	apperr.KindForbidden:    http.StatusForbidden,
	apperr.KindNotFound:     http.StatusNotFound,
	apperr.KindConflict:     http.StatusConflict,
	apperr.KindRateLimited:  http.StatusTooManyRequests,
	apperr.KindInternal:     http.StatusInternalServerError,
}

// Problem is the RFC 9457 problem details document sent to clients.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	RequestID string `json:"request_id,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// WriteProblem renders an error as an application/problem+json response and
// returns the rendered status, so handlers stay one-liners while tests can
// assert on the mapping. The request ID and the interface locale come from
// the request context (requestid and locale middlewares).
func WriteProblem(writer http.ResponseWriter, request *http.Request, err error) int {
	problem := ProblemFor(request, err)

	writer.Header().Set("Content-Type", mediaType)
	writer.WriteHeader(problem.Status)
	_ = json.NewEncoder(writer).Encode(problem)
	return problem.Status
}

// ProblemFor builds the public problem document for an error. Foreign errors
// collapse into a generic internal problem; wrapped causes never leak.
func ProblemFor(request *http.Request, err error) Problem {
	var requestID string
	if request != nil {
		requestID = requestid.FromRequest(request)
	}

	kind := apperr.KindOf(err)
	status := statusByKind[kind]
	title := localizedTitle(request, kind)
	detail := ""

	var appError *apperr.Error
	if errors.As(err, &appError) && appError.Code() != "" {
		return Problem{
			Type:      typeFor(kind),
			Title:     title,
			Status:    status,
			Code:      appError.Code(),
			RequestID: requestID,
			Detail:    publicDetail(appError, kind),
		}
	}

	return Problem{
		Type:      typeFor(kind),
		Title:     title,
		Status:    status,
		Code:      codeFor(kind),
		RequestID: requestID,
		Detail:    detail,
	}
}

// localizedTitle renders the kind title in the request's interface locale
// via the typed i18n catalog. When there is no request (or no catalog
// entry), it falls back to the generated default-locale catalog and then to
// the deterministic English titles, never echoing request-controlled data.
func localizedTitle(request *http.Request, kind apperr.Kind) string {
	key := "errors." + string(kind) + ".title"

	if request != nil {
		tag := locale.FromContext(request.Context())
		if message, err := i18n.Message(string(tag), key); err == nil {
			return message
		}
	}
	if message, err := i18n.Message(i18n.DefaultLocale, key); err == nil {
		return message
	}
	return titleByKind[kind]
}

// titleByKind is the last-resort deterministic fallback for titles when the
// catalog is missing a key. It is intentionally request-independent.
var titleByKind = map[apperr.Kind]string{
	apperr.KindValidation:   "the request is invalid",
	apperr.KindUnauthorized: "authentication is required",
	apperr.KindForbidden:    "you are not allowed to do this",
	apperr.KindNotFound:     "resource not found",
	apperr.KindConflict:     "the request conflicts with the current state",
	apperr.KindRateLimited:  "too many requests",
	apperr.KindInternal:     genericInternalMessage,
}

// publicDetail exposes the apperr detail only when the kind is safe to do
// so; internal failures keep the body generic.
func publicDetail(appError *apperr.Error, kind apperr.Kind) string {
	if kind == apperr.KindInternal {
		return ""
	}
	return appError.Detail()
}

// typeFor renders the problem type URI; a stable fragment keeps clients from
// string-matching titles.
func typeFor(kind apperr.Kind) string {
	return "https://goyim-arena.dev/problems/" + string(kind)
}

// codeFor renders the fallback code for non-vocabulary errors.
func codeFor(kind apperr.Kind) string {
	return "ARENA-" + string(kind)
}
