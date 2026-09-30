// Tests of the request budget (P16-T02): the refusals, the arithmetic of the
// bound, the deadline and the two properties the plan asks for by name —
// a body that costs no more than the budget, and a deadline no handler can
// outrun into a success.
package httplimits_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httplimits"
)

// recorderHandler is a handler that records how it was reached and answers
// with a fixed document.
type recorderHandler struct {
	calls    int64
	lastBody []byte
	lastPath string
	answer   func(writer http.ResponseWriter, request *http.Request)
}

func (handler *recorderHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	atomic.AddInt64(&handler.calls, 1)
	handler.lastPath = request.URL.Path
	body, _ := io.ReadAll(request.Body)
	handler.lastBody = body
	if handler.answer != nil {
		handler.answer(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(`{"ok":true}`))
}

func (handler *recorderHandler) called() int64 { return atomic.LoadInt64(&handler.calls) }

// problem decodes the problem document of a refusal and returns its code.
func problemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (body: %s)", got, recorder.Body.String())
	}
	var document struct {
		Code   string `json:"code"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("refusal is not a problem document: %v (body: %s)", err, recorder.Body.String())
	}
	if document.Status != recorder.Code {
		t.Errorf("problem status = %d, want the response status %d", document.Status, recorder.Code)
	}
	return document.Code
}

// serve runs one request through the middleware over the recorder handler.
func serve(t *testing.T, request *http.Request, handler *recorderHandler) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	httplimits.Middleware(httplimits.Default, handler).ServeHTTP(recorder, request)
	return recorder
}

// countingBody is a body that reports how many bytes the server actually
// pulled out of it: the direct measurement of "the server does not grow
// without a limit".
type countingBody struct {
	remaining int64
	read      int64
}

func (body *countingBody) Read(into []byte) (int, error) {
	if body.remaining <= 0 {
		return 0, io.EOF
	}
	length := int64(len(into))
	if length > body.remaining {
		length = body.remaining
	}
	for index := int64(0); index < length; index++ {
		into[index] = 'x'
	}
	body.remaining -= length
	body.read += length
	return int(length), nil
}

func (body *countingBody) Close() error { return nil }

// TestOversizedBodyIsRefusedWithoutReachingTheHandler covers the first item of
// the validation: a large body is refused, and the size of the refusal is the
// budget, not the size of the attack.
func TestOversizedBodyIsRefusedWithoutReachingTheHandler(t *testing.T) {
	t.Parallel()

	const path = "/api/v1/me/arena-drafts"

	t.Run("declared length is refused before a byte is read", func(t *testing.T) {
		t.Parallel()

		handler := &recorderHandler{}
		body := &countingBody{remaining: 8 << 20}
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Body = body
		request.ContentLength = 8 << 20

		recorder := serve(t, request, handler)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", recorder.Code, recorder.Body.String())
		}
		if code := problemCode(t, recorder); code != httplimits.CodeBodyTooLarge {
			t.Errorf("code = %q, want %q", code, httplimits.CodeBodyTooLarge)
		}
		if handler.called() != 0 {
			t.Errorf("handler was called %d times, want 0", handler.called())
		}
		if body.read != 0 {
			t.Errorf("server read %d bytes of an oversized declared body, want 0", body.read)
		}
	})

	t.Run("a streamed body is read one byte past the budget and no further", func(t *testing.T) {
		t.Parallel()

		handler := &recorderHandler{}
		body := &countingBody{remaining: 64 << 20}
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Body = body
		request.ContentLength = -1 // chunked: the length is a promise, not a fact

		recorder := serve(t, request, handler)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", recorder.Code, recorder.Body.String())
		}
		if code := problemCode(t, recorder); code != httplimits.CodeBodyTooLarge {
			t.Errorf("code = %q, want %q", code, httplimits.CodeBodyTooLarge)
		}
		if handler.called() != 0 {
			t.Errorf("handler was called %d times, want 0", handler.called())
		}
		budget := httplimits.Default(http.MethodPost, path).BodyBytes
		if body.read > budget+1 {
			t.Errorf("server read %d bytes from a %d-byte budget, want at most %d", body.read, budget, budget+1)
		}
	})

	t.Run("a body inside the budget reaches the handler unchanged", func(t *testing.T) {
		t.Parallel()

		handler := &recorderHandler{}
		payload := `{"statement":"uma frase curta","language":"pt-BR"}`
		recorder := serve(t, httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload)), handler)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
		}
		if handler.called() != 1 {
			t.Fatalf("handler was called %d times, want 1", handler.called())
		}
		if string(handler.lastBody) != payload {
			t.Errorf("handler read %q, want the original body %q", handler.lastBody, payload)
		}
	})
}

// TestBodyOnAReadRouteIsRefused pins the zero-budget class: a read route takes
// no body at all, and the refusal happens without reading one.
func TestBodyOnAReadRouteIsRefused(t *testing.T) {
	t.Parallel()

	handler := &recorderHandler{}
	body := &countingBody{remaining: 4 << 20}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/me/wallet", nil)
	request.Body = body
	request.ContentLength = -1

	recorder := serve(t, request, handler)
	if code := problemCode(t, recorder); code != httplimits.CodeBodyNotAllowed {
		t.Errorf("code = %q, want %q", code, httplimits.CodeBodyNotAllowed)
	}
	if handler.called() != 0 {
		t.Errorf("handler was called %d times, want 0", handler.called())
	}
	if body.read > 1 {
		t.Errorf("server read %d bytes for a route that accepts none, want at most 1", body.read)
	}

	// The same route without a body is untouched.
	quiet := &recorderHandler{}
	recorder = serve(t, httptest.NewRequest(http.MethodGet, "/api/v1/me/wallet", nil), quiet)
	if recorder.Code != http.StatusOK || quiet.called() != 1 {
		t.Errorf("bodyless read: status = %d, calls = %d, want 200 and 1", recorder.Code, quiet.called())
	}
}

// TestDeepJSONIsRefused covers the third item of the validation, including the
// two escape hatches a naive check would leave open: a client that omits the
// JSON media type, and a body that is deep and malformed at once.
func TestDeepJSONIsRefused(t *testing.T) {
	t.Parallel()

	const path = "/api/v1/me/arena-drafts"
	budget := httplimits.Default(http.MethodPost, path).JSONDepth
	if budget <= 0 {
		t.Fatalf("route %s has no JSON depth budget, so this test would be vacuous", path)
	}

	nested := func(depth int) string {
		return strings.Repeat(`{"a":`, depth) + `1` + strings.Repeat(`}`, depth)
	}

	t.Run("within the budget passes", func(t *testing.T) {
		t.Parallel()

		handler := &recorderHandler{}
		recorder := serve(t, httptest.NewRequest(http.MethodPost, path, strings.NewReader(nested(budget))), handler)
		if recorder.Code != http.StatusOK || handler.called() != 1 {
			t.Fatalf("status = %d, calls = %d, want 200 and 1 (body: %s)", recorder.Code, handler.called(), recorder.Body.String())
		}
	})

	t.Run("beyond the budget is refused", func(t *testing.T) {
		t.Parallel()

		for name, body := range map[string]string{
			"declared json":   nested(budget + 1),
			"undeclared":      nested(budget + 2),
			"deep and broken": strings.Repeat(`{"a":`, budget+3),
		} {
			handler := &recorderHandler{}
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			if name == "undeclared" {
				request.Header.Del("Content-Type")
			}
			recorder := serve(t, request, handler)

			if code := problemCode(t, recorder); code != httplimits.CodeJSONTooDeep {
				t.Errorf("%s: code = %q, want %q (body: %s)", name, code, httplimits.CodeJSONTooDeep, recorder.Body.String())
			}
			if handler.called() != 0 {
				t.Errorf("%s: handler was called %d times, want 0", name, handler.called())
			}
		}
	})

	t.Run("a flat form body is not judged as json", func(t *testing.T) {
		t.Parallel()

		handler := &recorderHandler{}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader("csrf_token=abc&next=%2F"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		recorder := serve(t, request, handler)
		if recorder.Code != http.StatusOK || handler.called() != 1 {
			t.Fatalf("status = %d, calls = %d, want 200 and 1 (body: %s)", recorder.Code, handler.called(), recorder.Body.String())
		}
	})
}

// TestCompressionAndMultipartAreRefused covers the two request shapes the
// server will never serve: decompression is how a small body becomes a large
// allocation, and multipart is the upload path the MVP does not have.
func TestCompressionAndMultipartAreRefused(t *testing.T) {
	t.Parallel()

	const path = "/api/v1/me/arena-drafts"

	for name, prepare := range map[string]func(*http.Request){
		"gzip": func(request *http.Request) {
			request.Header.Set("Content-Encoding", "gzip")
			request.Body = io.NopCloser(strings.NewReader("\x1f\x8b\x08\x00"))
			request.ContentLength = -1
		},
		"br": func(request *http.Request) {
			request.Header.Set("Content-Encoding", "br")
		},
		"multipart": func(request *http.Request) {
			request.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		},
	} {
		handler := &recorderHandler{}
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"statement":"x"}`))
		prepare(request)

		recorder := serve(t, request, handler)
		code := problemCode(t, recorder)
		want := httplimits.CodeMultipartNotSupported
		if name != "multipart" {
			want = httplimits.CodeUnsupportedContentEncoding
		}
		if code != want {
			t.Errorf("%s: code = %q, want %q", name, code, want)
		}
		if handler.called() != 0 {
			t.Errorf("%s: handler was called %d times, want 0", name, handler.called())
		}
	}

	// An explicit identity encoding is not a compression: it stays acceptable.
	handler := &recorderHandler{}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"statement":"x"}`))
	request.Header.Set("Content-Encoding", "identity")
	if recorder := serve(t, request, handler); recorder.Code != http.StatusOK {
		t.Errorf("identity encoding: status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
}

// TestUnreadableBodyIsRefusedSafely covers the client that stalls mid-body:
// the read fails, and the client gets a problem instead of an internal error
// page or a leaked transport message.
func TestUnreadableBodyIsRefusedSafely(t *testing.T) {
	t.Parallel()

	handler := &recorderHandler{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/me/arena-drafts", nil)
	request.Body = failingBody{err: errors.New("read tcp 127.0.0.1:5432: i/o timeout")}
	request.ContentLength = -1

	recorder := serve(t, request, handler)
	if code := problemCode(t, recorder); code != httplimits.CodeBodyUnreadable {
		t.Errorf("code = %q, want %q", code, httplimits.CodeBodyUnreadable)
	}
	if body := recorder.Body.String(); strings.Contains(body, "i/o timeout") || strings.Contains(body, "5432") {
		t.Errorf("the transport error leaked into the response: %s", body)
	}
	if handler.called() != 0 {
		t.Errorf("handler was called %d times, want 0", handler.called())
	}
}

// failingBody fails every read with a transport error.
type failingBody struct{ err error }

func (body failingBody) Read([]byte) (int, error) { return 0, body.err }
func (failingBody) Close() error                  { return nil }

// TestDeadlineTravelsInTheContext covers the context half of the deadline: the
// handler sees a deadline that fits the route's budget, and it sees the
// cancellation when the budget is spent.
func TestDeadlineTravelsInTheContext(t *testing.T) {
	t.Parallel()

	// The cancellation is waited for rather than hoped for: the handler closes
	// `waiting` once it has read its deadline and is about to block on the
	// context, and the client cancels then. A fixed pause would assert that 50ms
	// is longer than the middleware takes to reach the handler, which is a
	// property of the machine and not of the product.
	waiting := make(chan struct{})
	handler := &recorderHandler{answer: func(writer http.ResponseWriter, request *http.Request) {
		deadline, ok := request.Context().Deadline()
		if !ok {
			t.Errorf("handler context has no deadline")
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > 5*time.Second {
			t.Errorf("deadline is %v away, want a positive budget below the request timeout", remaining)
		}
		close(waiting)
		<-request.Context().Done()
		writer.WriteHeader(http.StatusOK)
	}}

	request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	request = request.WithContext(ctx)

	go func() {
		<-waiting
		cancel()
	}()

	recorder := httptest.NewRecorder()
	httplimits.Middleware(httplimits.Default, handler).ServeHTTP(recorder, request)

	if handler.called() != 1 {
		t.Fatalf("handler was called %d times, want 1", handler.called())
	}
	// The client cancelled its own request: the handler observed it, and the
	// response carries the cancellation instead of a fabricated success.
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want the safe problem for a request that ended after its deadline (body: %s)",
			recorder.Code, recorder.Body.String())
	}
	if code := problemCode(t, recorder); code != httplimits.CodeRequestTimeout {
		t.Errorf("code = %q, want %q", code, httplimits.CodeRequestTimeout)
	}
}

// TestLateWriteCannotBecomeASuccess is the property the gate exists for: a
// handler that ignores its deadline and answers afterwards cannot hand the
// client a success it did not earn.
func TestLateWriteCannotBecomeASuccess(t *testing.T) {
	t.Parallel()

	handler := &recorderHandler{answer: func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(120 * time.Millisecond) // past the 50ms budget the test composes
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"leased":"a stale success"}`))
	}}

	budget := func(method, _ string) httplimits.Limits {
		return httplimits.Limits{Class: "test", BodyBytes: 1 << 10, JSONDepth: 4, Timeout: 50 * time.Millisecond}
	}

	recorder := httptest.NewRecorder()
	httplimits.Middleware(budget, handler).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/me/arena-drafts", strings.NewReader(`{"a":1}`)))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a handler that overran its deadline (body: %s)", recorder.Code, recorder.Body.String())
	}
	if code := problemCode(t, recorder); code != httplimits.CodeRequestTimeout {
		t.Errorf("code = %q, want %q", code, httplimits.CodeRequestTimeout)
	}
	if body := recorder.Body.String(); strings.Contains(body, "stale success") {
		t.Errorf("the late handler body reached the client: %s", body)
	}
}

// TestHandlerInsideItsDeadlineKeepsItsResponse is the counterweight: the gate
// must not interfere with a handler that answers in time.
func TestHandlerInsideItsDeadlineKeepsItsResponse(t *testing.T) {
	t.Parallel()

	handler := &recorderHandler{}
	recorder := serve(t, httptest.NewRequest(http.MethodPost, "/api/v1/me/arena-drafts", strings.NewReader(`{"statement":"curta"}`)), handler)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Body.String(); got != `{"ok":true}` {
		t.Errorf("body = %q, want the handler's own document", got)
	}
	if recorder.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want the handler's", recorder.Header().Get("Content-Type"))
	}
}

// TestBudgetResolutionIsPerRoute pins the resolution itself: the same body is
// accepted by one route and refused by another, which is what "per route"
// means, and an unknown path is bounded too.
func TestBudgetResolutionIsPerRoute(t *testing.T) {
	t.Parallel()

	handler := &recorderHandler{}
	ownerSized := fmt.Sprintf(`{"context":%q}`, strings.Repeat("x", 32<<10))
	oversized := fmt.Sprintf(`{"context":%q}`, strings.Repeat("x", 96<<10))

	ownerBudget := httplimits.Default(http.MethodPost, "/api/v1/me/arena-drafts").BodyBytes
	if int64(len(ownerSized)) > ownerBudget {
		t.Fatalf("fixture is %d bytes, beyond the %d-byte owner budget", len(ownerSized), ownerBudget)
	}
	if int64(len(oversized)) <= httplimits.Default(http.MethodPost, "/api/v1/auth/login").BodyBytes {
		t.Fatalf("fixture is %d bytes, inside the auth budget, so the refusal would not be proven", len(oversized))
	}

	arena := serve(t, httptest.NewRequest(http.MethodPost, "/api/v1/me/arena-drafts", strings.NewReader(ownerSized)), handler)
	if arena.Code != http.StatusOK {
		t.Fatalf("owner route refused a %d-byte body: %d (body: %s)", len(ownerSized), arena.Code, arena.Body.String())
	}

	auth := serve(t, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(oversized)), &recorderHandler{})
	if code := problemCode(t, auth); code != httplimits.CodeBodyTooLarge {
		t.Errorf("auth route accepted a %d-byte body: code = %q", len(oversized), code)
	}

	unknown := serve(t, httptest.NewRequest(http.MethodPost, "/api/v1/not-a-route", strings.NewReader(ownerSized)), &recorderHandler{})
	if code := problemCode(t, unknown); code != httplimits.CodeBodyTooLarge {
		t.Errorf("unknown route accepted a %d-byte body: code = %q", len(ownerSized), code)
	}

	classes := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodGet, "/health/live", httplimits.ClassHealth},
		{http.MethodGet, "/api/v1/arenas", httplimits.ClassRead},
		{http.MethodGet, "/transparency", httplimits.ClassRead},
		{http.MethodPost, "/api/v1/auth/login", httplimits.ClassAuthWrite},
		{http.MethodPost, "/api/v1/me/arena-drafts", httplimits.ClassOwnerWrite},
		{http.MethodPost, "/api/v1/me/moderation/reports", httplimits.ClassModerationWrite},
		{http.MethodPost, "/api/v1/moderation/cases/{id}/claim", httplimits.ClassModerationWrite},
		{http.MethodPost, "/api/v1/admin/jobs/{id}/retry", httplimits.ClassAdminWrite},
		{http.MethodPost, "/api/v1/not-a-route", httplimits.ClassUnrouted},
		{http.MethodTrace, "/api/v1/arenas", httplimits.ClassUnrouted},
	}
	for _, scenario := range classes {
		if got := httplimits.Default(scenario.method, scenario.path).Class; got != scenario.want {
			t.Errorf("Default(%s, %s) = %s, want %s", scenario.method, scenario.path, got, scenario.want)
		}
	}
}

// TestEveryBudgetIsBounded is the arithmetic guard: no class may hand out an
// unbounded body, a missing deadline or a deadline beyond the transport's, and
// the shipped table must stay inside the largest adapter constant.
func TestEveryBudgetIsBounded(t *testing.T) {
	t.Parallel()

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, path := range []string{"/health/ready", "/api/v1/me/wallet", "/api/v1/auth/login", "/api/v1/me/deletion", "/api/v1/moderation/cases", "/api/v1/admin/jobs/health", "/", "/anything"} {
			limits := httplimits.Default(method, path)
			if limits.Class == "" {
				t.Errorf("%s %s has no class", method, path)
			}
			if limits.BodyBytes < 0 {
				t.Errorf("%s %s: BodyBytes = %d, want a non-negative bound", method, path, limits.BodyBytes)
			}
			if limits.Timeout <= 0 {
				t.Errorf("%s %s: Timeout = %v, want a deadline", method, path, limits.Timeout)
			}
			if limits.BodyBytes > 1<<20 {
				t.Errorf("%s %s: BodyBytes = %d, want at most the largest adapter bound", method, path, limits.BodyBytes)
			}
		}
	}

	if longest := httplimits.LongestTimeout(); longest < 2*time.Second || longest > 20*time.Second {
		t.Errorf("LongestTimeout() = %v, want a budget between the shortest and the transport's", longest)
	}
}

// TestRefusalNeverEchoesTheBody is the one thing a refusal must never do: a
// refusal that quotes the payload turns a size limit into a reflection bug.
func TestRefusalNeverEchoesTheBody(t *testing.T) {
	t.Parallel()

	marker := strings.Repeat("Zx9", 40)
	budget := httplimits.Default(http.MethodPost, "/api/v1/me/arena-drafts").BodyBytes
	body := fmt.Sprintf(`{"context":%q}`, strings.Repeat(marker, int(budget)/len(marker)+2))

	recorder := serve(t, httptest.NewRequest(http.MethodPost, "/api/v1/me/arena-drafts", strings.NewReader(body)), &recorderHandler{})
	if code := problemCode(t, recorder); code != httplimits.CodeBodyTooLarge {
		t.Fatalf("code = %q, want %q", code, httplimits.CodeBodyTooLarge)
	}
	if strings.Contains(recorder.Body.String(), marker) {
		t.Errorf("the refusal echoed the request body: %s", recorder.Body.String())
	}
}
