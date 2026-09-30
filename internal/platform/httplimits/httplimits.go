// Package httplimits bounds what one request may consume from the arena
// binary (P16-T02): a budget per route for the request body, the JSON nesting
// depth and the processing deadline.
//
// Why this exists as a platform layer and not only as the adapters' own
// http.MaxBytesReader calls: a limit that lives inside each handler protects
// exactly the handlers that remembered to install it, and it protects nothing
// about the routes no module has mounted yet, about the paths that match no
// route at all, or about the moment a body is read before the handler's check
// runs. The budget here is a property of the route, resolved from the method
// and the path in one auditable table, and the tests walk the registered
// route registry to prove every route declares one.
//
// What one request may consume, in the order the middleware enforces it:
//
//   - Content-Encoding: the server never decompresses a request body, so a
//     body that declares a compression is refused before a byte is read —
//     decompressing is how a small body turns into a large allocation.
//   - multipart/form-data: uploads are not in the MVP, so the only multipart
//     the server would accept is the one nobody needs; it is refused.
//   - body size: a declared Content-Length beyond the budget is refused
//     without reading anything, and a streamed body is read through a bound
//     that stops one byte past it, so an oversized body is detected without
//     ever allocating more than the budget allows.
//   - JSON depth: a JSON body nests no deeper than the budget, judged by its
//     declared type or by its shape.
//   - deadline: the route's processing deadline travels in the request
//     context, so the work that honours a context stops; and a handler that
//     overruns it cannot deliver a late success (see deadlineGate).
//
// Every refusal is an RFC 9457 problem through internal/platform/httperror —
// the same vocabulary the handlers use — with a stable code, so clients never
// parse prose and never learn anything about the server's internals.
//
// Headers and form bodies are bounded too, but not here, and the placement is
// deliberate rather than an omission:
//
//   - request headers are bounded by the transport, before any route is
//     known (httpserver Options.MaxHeaderBytes, 64 KiB, with
//     ReadHeaderTimeout bounding how long a client may take to send them). A
//     header limit inside a route would run after net/http has already parsed
//     and allocated the headers, which is the very memory the bound exists to
//     prevent.
//   - an application/x-www-form-urlencoded body is counted by the same byte
//     budget as any other body, and is not parsed here: this layer bounds what
//     a request may cost, not what it means.
package httplimits

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
)

// Route classes. Each class is one row of budget, and every registered route
// resolves to exactly one of them.
const (
	// ClassHealth is the liveness and readiness surface: no body, and the
	// shortest deadline in the product, because a probe that waits is a probe
	// that failed.
	ClassHealth = "health"
	// ClassRead is every read-only request: no body is accepted at all. A
	// body on a read is either a client bug or an attempt to make the server
	// parse something it never promised to parse.
	ClassRead = "read"
	// ClassAuthWrite is account creation, login, logout and password reset.
	// The documents are an email, a password and a token: a few hundred
	// bytes, so the budget is deliberately far above the shape and far below
	// anything worth sending.
	ClassAuthWrite = "auth_write"
	// ClassOwnerWrite is the authenticated owner surface: drafts, arguments,
	// positions, profile deletion, billing choices.
	ClassOwnerWrite = "owner_write"
	// ClassModerationWrite is reports, appeals and moderator decisions. It
	// keeps the larger budget its adapter already used, because a report
	// carries a free-text context; it does not grow for anything else.
	ClassModerationWrite = "moderation_write"
	// ClassAdminWrite is the administrative operation surface: a retry with a
	// reason, and nothing bulk.
	ClassAdminWrite = "admin_write"
	// ClassUnrouted is every request that matches no rule: an unknown path or
	// a method that no route owns. It still gets a budget, because "unknown"
	// is not a reason to accept an unbounded body — but a small tolerated
	// body keeps the router free to answer its own 404 instead of this
	// middleware answering for it.
	ClassUnrouted = "unrouted"
)

// Budgets. Body sizes mirror the constants the adapters already enforce, so
// the platform bound never loosens what a route accepted before; a class may
// only be tighter than its adapter, never looser.
const (
	healthBodyBytes     int64 = 0
	readBodyBytes       int64 = 0
	authBodyBytes       int64 = 64 << 10
	ownerBodyBytes      int64 = 64 << 10
	moderationBodyBytes int64 = 1 << 20
	adminBodyBytes      int64 = 8 << 10
	unroutedBodyBytes   int64 = 1 << 10

	healthTimeout     = 2 * time.Second
	readTimeout       = 10 * time.Second
	authTimeout       = 10 * time.Second
	ownerTimeout      = 10 * time.Second
	moderationTimeout = 15 * time.Second
	adminTimeout      = 15 * time.Second
	unroutedTimeout   = 5 * time.Second

	authJSONDepth       = 8
	ownerJSONDepth      = 12
	moderationJSONDepth = 12
	adminJSONDepth      = 8
)

// Limits is the resource budget of one route class.
type Limits struct {
	// Class is the audit label of the budget, echoed in tests and logs.
	Class string
	// BodyBytes is the cap on the request body. Zero means the route accepts
	// no body: any body is refused instead of being read and discarded.
	BodyBytes int64
	// JSONDepth is the maximum nesting depth accepted in a JSON body; zero
	// disables the check, which is only sound where no body is accepted.
	JSONDepth int
	// Timeout is the processing deadline of the request.
	Timeout time.Duration
}

// Budget resolves the budget of one request. The binary composes Default;
// tests compose a smaller budget to exercise the deadline without waiting for
// the shipped one.
type Budget func(method, path string) Limits

// methodSet is a closed set of HTTP methods.
type methodSet uint8

const (
	methodRead methodSet = 1 << iota
	methodWrite
)

// rule assigns a budget to a method class and a path prefix. The table is
// ordered and the first match wins, so the more specific prefixes come first.
type rule struct {
	class   Limits
	methods methodSet
	prefix  string
}

// rules is the whole policy, in one readable place.
var rules = []rule{
	{class: Limits{Class: ClassHealth, BodyBytes: healthBodyBytes, Timeout: healthTimeout}, methods: methodRead | methodWrite, prefix: "/health/"},
	{class: Limits{Class: ClassAuthWrite, BodyBytes: authBodyBytes, JSONDepth: authJSONDepth, Timeout: authTimeout}, methods: methodWrite, prefix: "/api/v1/auth/"},
	// The browser journey of the account (P18-T05) submits the same documents
	// — an email, a password and a code — to the HTML routes, so it shares
	// the auth_write budget rather than inventing a second one. The routes are
	// listed one by one because a prefix is what the table matches on and
	// these are the only paths the journey owns.
	{class: Limits{Class: ClassAuthWrite, BodyBytes: authBodyBytes, JSONDepth: authJSONDepth, Timeout: authTimeout}, methods: methodWrite, prefix: "/register"},
	{class: Limits{Class: ClassAuthWrite, BodyBytes: authBodyBytes, JSONDepth: authJSONDepth, Timeout: authTimeout}, methods: methodWrite, prefix: "/login"},
	{class: Limits{Class: ClassAuthWrite, BodyBytes: authBodyBytes, JSONDepth: authJSONDepth, Timeout: authTimeout}, methods: methodWrite, prefix: "/logout"},
	{class: Limits{Class: ClassAuthWrite, BodyBytes: authBodyBytes, JSONDepth: authJSONDepth, Timeout: authTimeout}, methods: methodWrite, prefix: "/verify"},
	{class: Limits{Class: ClassAuthWrite, BodyBytes: authBodyBytes, JSONDepth: authJSONDepth, Timeout: authTimeout}, methods: methodWrite, prefix: "/reset"},
	// The browser participation journey of the Arena (P18-T06) submits the
	// documents of the owner surface — a position, an argument, an attribution
	// — so it shares the owner_write budget rather than inventing a second one.
	{class: Limits{Class: ClassOwnerWrite, BodyBytes: ownerBodyBytes, JSONDepth: ownerJSONDepth, Timeout: ownerTimeout}, methods: methodWrite, prefix: "/arenas/"},
	{class: Limits{Class: ClassAdminWrite, BodyBytes: adminBodyBytes, JSONDepth: adminJSONDepth, Timeout: adminTimeout}, methods: methodWrite, prefix: "/api/v1/admin/"},
	{class: Limits{Class: ClassModerationWrite, BodyBytes: moderationBodyBytes, JSONDepth: moderationJSONDepth, Timeout: moderationTimeout}, methods: methodWrite, prefix: "/api/v1/moderation/"},
	{class: Limits{Class: ClassModerationWrite, BodyBytes: moderationBodyBytes, JSONDepth: moderationJSONDepth, Timeout: moderationTimeout}, methods: methodWrite, prefix: "/api/v1/me/moderation/"},
	{class: Limits{Class: ClassOwnerWrite, BodyBytes: ownerBodyBytes, JSONDepth: ownerJSONDepth, Timeout: ownerTimeout}, methods: methodWrite, prefix: "/api/v1/me/"},
	{class: Limits{Class: ClassRead, BodyBytes: readBodyBytes, Timeout: readTimeout}, methods: methodRead, prefix: "/"},
	{class: Limits{Class: ClassUnrouted, BodyBytes: unroutedBodyBytes, Timeout: unroutedTimeout}},
}

// writeMethods are the methods that may carry a body.
func writeMethods(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// readMethods are the methods that must not carry one.
func readMethods(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// methodsOf maps a method to its class bit, or zero for an unknown method.
func methodsOf(method string) methodSet {
	switch {
	case writeMethods(method):
		return methodWrite
	case readMethods(method):
		return methodRead
	default:
		return 0
	}
}

// Default resolves the budget of a request against the shipped table. A
// request whose method or path the table does not name gets the unrouted
// budget: bounded, and never unbounded by omission.
func Default(method, path string) Limits {
	class := methodsOf(method)
	for _, candidate := range rules {
		if candidate.methods&class == 0 {
			continue
		}
		if candidate.prefix == "" || strings.HasPrefix(path, candidate.prefix) {
			return candidate.class
		}
	}
	return Limits{Class: ClassUnrouted, BodyBytes: unroutedBodyBytes, Timeout: unroutedTimeout}
}

// LongestTimeout is the largest deadline the table can hand out. Composition
// asserts it stays below the transport write timeout, because a route
// deadline beyond that budget could never be honored.
func LongestTimeout() time.Duration {
	longest := time.Duration(0)
	for _, candidate := range rules {
		if candidate.class.Timeout > longest {
			longest = candidate.class.Timeout
		}
	}
	return longest
}

// Stable problem codes. They are part of the response contract: clients branch
// on the code, never on the title or the detail.
const (
	// CodeBodyTooLarge marks a body beyond the route budget. It is the same
	// code the moderation adapter already uses for its own bound.
	CodeBodyTooLarge = "body_too_large"
	// CodeBodyNotAllowed marks a body sent to a route that accepts none.
	CodeBodyNotAllowed = "body_not_allowed"
	// CodeBodyUnreadable marks a body the server could not read to the end,
	// which is what a client that stalls mid-body produces.
	CodeBodyUnreadable = "body_unreadable"
	// CodeJSONTooDeep marks a JSON body nested deeper than the budget.
	CodeJSONTooDeep = "json_too_deep"
	// CodeUnsupportedContentEncoding marks a compressed request body. The
	// server does not decompress, so this is not a "try again later": it is a
	// request the server will never serve.
	CodeUnsupportedContentEncoding = "unsupported_content_encoding"
	// CodeMultipartNotSupported marks multipart form data, which the MVP has
	// no use for.
	CodeMultipartNotSupported = "multipart_not_supported"
	// CodeRequestTimeout marks a handler that overran its route deadline: the
	// server failed to answer inside its own budget.
	CodeRequestTimeout = "request_timeout"
)

// Middleware bounds every request before the handler sees it.
//
// Placement matters and is part of the contract: the middleware runs inside
// the request correlation and locale layers, so a refusal still carries the
// request id and a localized title, and inside the browser security policy so
// a refusal still carries the security headers.
func Middleware(budget Budget, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		limits := budget(request.Method, request.URL.Path)

		body, refusal := boundedBody(request, limits)
		if refusal != nil {
			httperror.WriteProblem(writer, request, refusal)
			return
		}
		// The handler reads a body that was already proven to fit, and it
		// reads it from memory instead of from a socket that a slow client
		// controls the pace of.
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
		request.Header.Set("Content-Length", strconv.Itoa(len(body)))

		ctx, cancel := context.WithTimeout(request.Context(), limits.Timeout)
		defer cancel()

		gate := &deadlineGate{ResponseWriter: writer, request: request, deadline: ctx}
		next.ServeHTTP(gate, request.WithContext(ctx))
	})
}

// boundedBody validates the request body against the budget and returns it in
// memory. A nil refusal means the body is acceptable and complete.
func boundedBody(request *http.Request, limits Limits) ([]byte, error) {
	if !acceptsBody(request) {
		return nil, nil
	}

	if encoding := strings.TrimSpace(request.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return nil, problem(CodeUnsupportedContentEncoding, "compressed request bodies are not accepted")
	}
	if mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type")); err == nil && mediaType == "multipart/form-data" {
		return nil, problem(CodeMultipartNotSupported, "multipart form data is not accepted")
	}

	if limits.BodyBytes == 0 {
		// No body is part of this route's contract. A declared length is
		// enough to refuse, without reading anything.
		if request.ContentLength > 0 {
			return nil, problem(CodeBodyNotAllowed, "this route does not accept a request body")
		}
		head, err := readUpTo(request.Body, 0)
		if err != nil {
			return nil, problem(CodeBodyUnreadable, "the request body could not be read")
		}
		if len(head) > 0 {
			return nil, problem(CodeBodyNotAllowed, "this route does not accept a request body")
		}
		return nil, nil
	}

	// A declared length beyond the budget is refused without reading a byte.
	if request.ContentLength > limits.BodyBytes {
		return nil, problem(CodeBodyTooLarge, "the request body exceeds the size this route accepts")
	}

	body, err := readUpTo(request.Body, limits.BodyBytes)
	if err != nil {
		return nil, problem(CodeBodyUnreadable, "the request body could not be read")
	}
	if int64(len(body)) > limits.BodyBytes {
		return nil, problem(CodeBodyTooLarge, "the request body exceeds the size this route accepts")
	}

	if depth := jsonDepth(body, limits.JSONDepth, request.Header.Get("Content-Type")); depth > limits.JSONDepth {
		return nil, problem(CodeJSONTooDeep, "the request body nests deeper than this route accepts")
	}
	return body, nil
}

// acceptsBody reports whether the request declares a body at all. A request
// with a known-zero length has none, and neither has one whose length is
// unknown only because the client used a method that never carries one.
func acceptsBody(request *http.Request) bool {
	return request.Body != nil && request.Body != http.NoBody
}

// readUpTo reads at most limit+1 bytes: the extra byte is what turns "the
// body fits" into a decision, and it is the only reason a streamed oversized
// body costs a byte more than the budget instead of as much as the attacker
// sends.
func readUpTo(body io.Reader, limit int64) ([]byte, error) {
	capacity := limit + 1
	if capacity > 64<<10 {
		capacity = 64 << 10
	}
	buffer := make([]byte, 0, capacity)
	reader := io.LimitReader(body, limit+1)
	chunk := make([]byte, 32<<10)
	for {
		read, err := reader.Read(chunk)
		if read > 0 {
			buffer = append(buffer, chunk[:read]...)
		}
		if err == io.EOF {
			return buffer, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// jsonDepth returns the deepest nesting level of a JSON body, or the budget
// plus one as soon as the body exceeds it. A body that is not JSON — or not
// valid JSON, which the handler is free to reject with its own problem — is
// reported as depth zero: this middleware bounds nesting, it does not judge
// meaning.
func jsonDepth(body []byte, budget int, contentType string) int {
	if budget <= 0 || len(body) == 0 {
		return 0
	}
	if !declaresJSON(contentType) && !looksLikeJSON(body) {
		return 0
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	depth, deepest := 0, 0
	for {
		token, err := decoder.Token()
		if err != nil {
			// Malformed JSON reaches the handler, which answers with its own
			// validation problem; what was read so far still counts, because
			// a body can be deep and broken at the same time.
			return deepest
		}
		switch delimiter := token.(type) {
		case json.Delim:
			switch delimiter {
			case '{', '[':
				depth++
				if depth > deepest {
					deepest = depth
				}
				if depth > budget {
					return depth
				}
			case '}', ']':
				if depth > 0 {
					depth--
				}
			}
		}
	}
}

// declaresJSON reports whether the client declared a JSON media type, with the
// structured-suffix and charset forms included.
func declaresJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// looksLikeJSON reports whether the body starts like a JSON object or array.
// Judging a body by its declared type alone would let a client skip the depth
// bound by omitting the header.
func looksLikeJSON(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

// problem builds a validation problem with a stable code.
func problem(code, detail string) error {
	return apperr.New(apperr.KindValidation, code, detail)
}
