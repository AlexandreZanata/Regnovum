// Package websurface holds what every server-rendered browser journey shares
// (P18-T06): the request locale catalog, the bounded form read, the CSRF double
// submit carried in a form body, the buffered document write and the
// translation of a platform refusal into a localized page.
//
// Why this is a platform package and not a copy per adapter: two surfaces now
// answer documents to a person — the account journey
// (internal/identity/adapters/html) and the Arena participation journey
// (internal/arenas/adapters/html) — and both must refuse the same way. A
// refusal is written by the platform middleware as an RFC 9457 problem
// document, which is correct for a client and useless to a person submitting a
// form; translating it is one rule, and a rule that lives in two places is a
// rule that drifts. The middlewares themselves stay where they are
// (internal/platform/security, internal/platform/httplimits): this package only
// decides how their answers reach a browser, and how a submitted document is
// read inside the budget the caller declares.
//
// The catalog rule is I18N_STANDARD §5: application and domain layers return
// stable codes, never prose, so localization happens here and in the templates.
// A missing key falls back to the default locale (§8) and is never rendered as
// the raw key.
package websurface

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

const (
	// FormField is the hidden input the platform CSRF middleware reads from a
	// form body. A browser form cannot set a header, so the double submit of
	// this surface travels in the body.
	FormField = "csrf_token"

	// MaxFormBytes bounds the browser form bodies with the same budget the
	// JSON API uses for its documents (64 KiB).
	MaxFormBytes = 64 << 10

	// ProblemMediaType is the media type the platform middleware answers with.
	ProblemMediaType = "application/problem+json"
)

// Locale is the interface locale of the request, as resolved by the platform
// locale middleware, or the default locale when the request carries none.
func Locale(r *http.Request) string {
	if r != nil {
		if tag := locale.FromContext(r.Context()); string(tag) != "" {
			return string(tag)
		}
	}
	return i18n.DefaultLocale
}

// Localized resolves a catalog message in the request locale, falling back to
// the default locale so that a partially translated catalog never renders an
// empty label (I18N_STANDARD §8).
func Localized(r *http.Request, key string, values map[string]string) (string, error) {
	message, err := i18n.Format(Locale(r), key, values)
	if err == nil {
		return message, nil
	}
	return i18n.Format(i18n.DefaultLocale, key, values)
}

// Form parses the submitted document inside the route budget. It reports false
// after answering the refusal, so the caller returns immediately: a body that
// cannot be read is a validation refusal, which is what the platform answers
// for a body it refuses.
//
// The refusal is written as a problem document, not as a page: a surface wraps
// its mutating routes in Refusals, which translates it into a localized page.
// Composition therefore stays the caller's decision, and a route that forgot
// the wrapper answers RFC 9457 like the JSON API instead of half a page.
func Form(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxFormBytes)
	if err := r.ParseForm(); err != nil {
		refuse(w, r, Refusal{Status: http.StatusBadRequest, Kind: apperr.KindValidation})
		return nil, false
	}
	return r.PostForm, true
}

// RequiredErrors translates the empty required fields of one submission into
// the field-to-message map of a failed form. Only the fields the caller read
// are considered, so a form never reports a field it does not render.
func RequiredErrors(r *http.Request, messageKey string, submitted map[string]string) (map[string]string, error) {
	missing := make([]string, 0, len(submitted))
	for name, content := range submitted {
		if strings.TrimSpace(content) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	message, err := Localized(r, messageKey, nil)
	if err != nil {
		return nil, err
	}
	found := make(map[string]string, len(missing))
	for _, name := range missing {
		found[name] = message
	}
	return found, nil
}

// CSRF returns the double-submit token the form must carry.
//
// The page can only render the value the cookie holds, so a request without a
// valid signed cookie is issued a fresh one: a form rendered without a token
// would be refused by the middleware on the way back, which looks exactly like
// a broken page.
func CSRF(m *security.Manager, w http.ResponseWriter, r *http.Request) (string, error) {
	if token, err := m.Cookies().GetCSRFToken(r); err == nil && m.CSRF().VerifyToken(token) {
		return token, nil
	}
	return m.IssueCSRFToken(w)
}

// Missing reports whether a dependency a surface was composed with cannot be
// called. It covers both the nil interface and the typed nil pointer a
// composition passes by accident, which a plain `== nil` comparison accepts.
func Missing(dependency any) bool {
	if dependency == nil {
		return true
	}
	value := reflect.ValueOf(dependency)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Renderer renders one document into the given writer.
type Renderer func(writer io.Writer) error

// Document renders a document into memory. A template that cannot render must
// not leave a half-written page on the connection, so the caller writes only
// the finished document.
func Document(render Renderer) ([]byte, error) {
	var body bytes.Buffer
	if err := render(&body); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

// WritePrivate writes a document that is never stored: a page of a browser
// journey carries a token, a submitted value or a person's own state, so a
// shared cache must not keep it.
func WritePrivate(w http.ResponseWriter, status int, body []byte) {
	httpcache.Private(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// Refusal is one platform answer to present as a page: the status to write, the
// kind of the failure and, for a CSRF refusal, the flag that selects the message
// telling the person what to do about it.
type Refusal struct {
	// Status is the HTTP status of the refusal.
	Status int
	// Kind is the stable category of the failure, as the JSON API classifies
	// it too.
	Kind apperr.Kind
	// CSRF reports a refusal of the double submit, whose message has to ask
	// for a reload instead of naming a category.
	CSRF bool
}

// Presenter writes one refusal page.
type Presenter func(w http.ResponseWriter, r *http.Request, refusal Refusal)

// refuse answers a browser request with the problem document of a refusal this
// package composes itself; the middleware refusals reach the page through
// Refusals.
func refuse(w http.ResponseWriter, r *http.Request, refusal Refusal) {
	writeProblem(w, r, refusal.Status, string(refusal.Kind))
}

// writeProblem writes a minimal problem document for a refusal this package
// composes. The code is the stable kind, which is what the translation of the
// surrounding middleware reads.
//
// The writer answers nothing: the document is built from a status and strings,
// so the encoder cannot fail on it, and the status line is already on the wire
// by the time the encode is attempted — a second failure has nowhere to go. It
// is the same decision the problem writer of the platform records for every JSON
// surface, and the reason the caller has nothing to handle.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, code string) {
	w.Header().Set("Content-Type", ProblemMediaType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "about:blank",
		"title":  "request refused",
		"status": status,
		"code":   code,
	})
}

// Refusals turns the problem documents the platform middleware produces around
// a browser surface into pages.
//
// Without it, a spent rate-limit budget or a missing session would answer a form
// submission with `application/problem+json` — technically correct and useless
// to a person. The middleware's refusal is written into a buffer, and only a
// problem document is translated: a document the surface rendered passes
// through untouched, so the translation can never rewrite a page or a redirect.
func Refusals(present Presenter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffered := &bufferedResponse{header: make(http.Header)}
		next.ServeHTTP(buffered, r)

		if problem, ok := refusalOf(buffered); ok {
			present(w, r, Refusal{
				Status: buffered.status,
				Kind:   KindForStatus(buffered.status),
				CSRF:   strings.HasPrefix(problem.Code, "csrf_"),
			})
			return
		}
		buffered.flushTo(w)
	})
}

// KindForStatus maps an answer to the error vocabulary. It mirrors the table the
// JSON clients use, so a refusal keeps one meaning across the surfaces.
func KindForStatus(status int) apperr.Kind {
	switch status {
	case http.StatusUnauthorized:
		return apperr.KindUnauthorized
	case http.StatusForbidden:
		return apperr.KindForbidden
	case http.StatusNotFound:
		return apperr.KindNotFound
	case http.StatusConflict:
		return apperr.KindConflict
	case http.StatusTooManyRequests:
		return apperr.KindRateLimited
	default:
		if status >= http.StatusInternalServerError {
			return apperr.KindInternal
		}
		return apperr.KindValidation
	}
}

// problemDocument is the subset of the RFC 9457 body the translation reads.
// Nothing else is used: the title and the detail of the wire document are
// deliberately ignored, because the page is localized from the stable code and
// kind (I18N_STANDARD §5).
type problemDocument struct {
	Code string `json:"code"`
}

// refusalOf reports whether the buffered response is a problem document the
// translation must replace.
func refusalOf(response *bufferedResponse) (problemDocument, bool) {
	if response.status < http.StatusBadRequest {
		return problemDocument{}, false
	}
	mediaType, _, err := mime.ParseMediaType(response.header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, ProblemMediaType) {
		return problemDocument{}, false
	}
	var document problemDocument
	if err := json.Unmarshal(response.body.Bytes(), &document); err != nil {
		return problemDocument{}, false
	}
	return document, true
}

// bufferedResponse collects a response so it can be inspected before it is sent.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

// Header returns the header the buffered response accumulates.
func (b *bufferedResponse) Header() http.Header { return b.header }

// WriteHeader records the status of the buffered response.
func (b *bufferedResponse) WriteHeader(status int) {
	if b.wrote {
		return
	}
	b.status = status
	b.wrote = true
}

// Write collects the body of the buffered response.
func (b *bufferedResponse) Write(content []byte) (int, error) {
	if !b.wrote {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(content)
}

// flushTo replays the buffered response on the real writer.
func (b *bufferedResponse) flushTo(w http.ResponseWriter) {
	target := w.Header()
	for name, values := range b.header {
		for _, value := range values {
			target.Add(name, value)
		}
	}
	if !b.wrote {
		b.status = http.StatusOK
	}
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}
