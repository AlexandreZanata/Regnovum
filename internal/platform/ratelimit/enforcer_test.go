// Tests of the enforcement half of internal/platform/ratelimit (P16-T03):
// which subjects reach the decision, what the client is told, and what happens
// when the limiter itself is broken.
//
// The subjects are the interesting part. A throttle is only as good as what it
// keys on, so these tests name the peer, the forwarding header and the
// authenticated identity and assert which key the guard saw.
package ratelimit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// subjectRecorder is a guard that records what it was asked and answers with a
// decision of the test's choosing.
type subjectRecorder struct {
	seen     []ratelimit.Subject
	action   ratelimit.Action
	decision ratelimit.Decision
	err      error
	calls    int
}

func (recorder *subjectRecorder) Allow(_ context.Context, action ratelimit.Action, subjects ...ratelimit.Subject) (ratelimit.Decision, error) {
	recorder.calls++
	recorder.action = action
	recorder.seen = subjects
	return recorder.decision, recorder.err
}

// countingHandler records whether the wrapped handler ran.
type countingHandler struct {
	calls int
}

func (handler *countingHandler) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	handler.calls++
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(`{"ok":true}`))
}

// protectedRequest builds a request with a peer address.
func protectedRequest(remoteAddr string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	request.RemoteAddr = remoteAddr
	return request
}

// problemOf decodes the problem document of a response.
func problemOf(t *testing.T, recorder *httptest.ResponseRecorder) (code string, status int) {
	t.Helper()

	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (body: %s)", contentType, recorder.Body.String())
	}
	var document struct {
		Code   string `json:"code"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("response is not a problem document: %v (body: %s)", err, recorder.Body.String())
	}
	return document.Code, document.Status
}

func TestRefusalIsTheStandardProblemWithRetryAfter(t *testing.T) {
	t.Parallel()

	guard := &subjectRecorder{decision: ratelimit.Decision{Allowed: false, Refused: ratelimit.SubjectAddress, RetryAfter: 1400 * time.Millisecond, Limit: 10}}
	handler := &countingHandler{}

	recorder := httptest.NewRecorder()
	ratelimit.New(guard, clientip.New(nil)).Protect(ratelimit.ActionAuthLogin, handler).ServeHTTP(recorder, protectedRequest("198.51.100.7:1234"))

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	code, status := problemOf(t, recorder)
	if code != ratelimit.CodeRateLimited {
		t.Errorf("code = %q, want %q", code, ratelimit.CodeRateLimited)
	}
	if status != recorder.Code {
		t.Errorf("problem status = %d, want the response status %d", status, recorder.Code)
	}
	// Delta-seconds, rounded up: a client told to retry in 0 seconds after
	// 1.4 seconds of waiting would retry into another refusal.
	if retryAfter := recorder.Header().Get("Retry-After"); retryAfter != "2" {
		t.Errorf("Retry-After = %q, want \"2\"", retryAfter)
	}
	if handler.calls != 0 {
		t.Errorf("handler ran %d times, want 0: a refused request must not reach a use case", handler.calls)
	}
	// The refusal must not name what it was keyed on.
	if body := recorder.Body.String(); body == "" || strings.Contains(body, "198.51.100.7") {
		t.Errorf("the refusal reflects the caller: %s", body)
	}
}

func TestRefusalWithoutAWaitOmitsRetryAfter(t *testing.T) {
	t.Parallel()

	// A refusal with no wait is not a throttle: it is an unusable key. Promising
	// "retry in 0 seconds" would be a lie that also invites a tight loop.
	guard := &subjectRecorder{decision: ratelimit.Decision{Allowed: false, Refused: ratelimit.SubjectAddress}}
	recorder := httptest.NewRecorder()
	ratelimit.New(guard, clientip.New(nil)).Protect(ratelimit.ActionAuthLogin, &countingHandler{}).ServeHTTP(recorder, protectedRequest("198.51.100.7:1234"))

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	if _, present := recorder.Header()["Retry-After"]; present {
		t.Errorf("Retry-After = %q, want the header absent", recorder.Header().Get("Retry-After"))
	}
}

func TestSubjectExtractionIsWhatTheGuardKeysOn(t *testing.T) {
	t.Parallel()

	trusted := clientip.New(mustTrusted(t, "10.0.0.0/8"))

	scenarios := []struct {
		name        string
		remoteAddr  string
		forwarded   string
		identity    *security.AuthIdentity
		wantAddress string
		wantAccount string
	}{
		{
			name:        "public route, untrusted peer: no forwarding header is evidence",
			remoteAddr:  "198.51.100.7:1234",
			forwarded:   "6.6.6.6",
			wantAddress: "198.51.100.7",
		},
		{
			name:        "public route, trusted peer: the chain names the client",
			remoteAddr:  "10.0.0.5:1234",
			forwarded:   "198.51.100.7, 10.0.0.9",
			wantAddress: "198.51.100.7",
		},
		{
			name:        "authenticated route carries the account",
			remoteAddr:  "198.51.100.7:1234",
			identity:    &security.AuthIdentity{AccountID: "acc-1", SessionID: "sess-1"},
			wantAddress: "198.51.100.7",
			wantAccount: "acc-1",
		},
		{
			name:        "ipv6 peer, forward-free",
			remoteAddr:  "[2001:db8::7]:1234",
			wantAddress: "2001:db8::7",
		},
		{
			name:        "an unreadable peer address still keys one shared bucket",
			remoteAddr:  "not-an-address",
			wantAddress: "unknown",
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			guard := &subjectRecorder{decision: ratelimit.Decision{Allowed: true, Limit: 10, Remaining: 9}}
			request := protectedRequest(scenario.remoteAddr)
			if scenario.forwarded != "" {
				request.Header.Set("X-Forwarded-For", scenario.forwarded)
			}
			if scenario.identity != nil {
				request = request.WithContext(security.WithAuth(request.Context(), *scenario.identity))
			}

			recorder := httptest.NewRecorder()
			ratelimit.New(guard, trusted).Protect(ratelimit.ActionAuthLogin, &countingHandler{}).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
			}
			if guard.action != ratelimit.ActionAuthLogin {
				t.Errorf("action = %q, want %q", guard.action, ratelimit.ActionAuthLogin)
			}
			if got := subjectOf(guard.seen, ratelimit.SubjectAddress); got != scenario.wantAddress {
				t.Errorf("address subject = %q, want %q", got, scenario.wantAddress)
			}
			if got := subjectOf(guard.seen, ratelimit.SubjectAccount); got != scenario.wantAccount {
				t.Errorf("account subject = %q, want %q", got, scenario.wantAccount)
			}
		})
	}
}

func TestGuardFailureFailsClosedWithoutLying(t *testing.T) {
	t.Parallel()

	// A throttle that cannot decide must not let the action through. It also
	// must not claim the client is throttled: the client is told the server
	// failed, and no Retry-After invites a loop.
	guard := &subjectRecorder{err: errors.New("limiter unavailable")}
	handler := &countingHandler{}

	recorder := httptest.NewRecorder()
	ratelimit.New(guard, clientip.New(nil)).Protect(ratelimit.ActionAuthRegister, handler).ServeHTTP(recorder, protectedRequest("198.51.100.7:1234"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	code, _ := problemOf(t, recorder)
	if code != ratelimit.CodeRateLimitUnavailable {
		t.Errorf("code = %q, want %q", code, ratelimit.CodeRateLimitUnavailable)
	}
	if body := recorder.Body.String(); strings.Contains(body, "limiter unavailable") {
		t.Errorf("the internal cause leaked into the response: %s", body)
	}
	if _, present := recorder.Header()["Retry-After"]; present {
		t.Errorf("Retry-After = %q, want the header absent", recorder.Header().Get("Retry-After"))
	}
	if handler.calls != 0 {
		t.Errorf("handler ran %d times, want 0", handler.calls)
	}
}

func TestCompositionWithoutAGuardDoesNotDecide(t *testing.T) {
	t.Parallel()

	// A composition that has not installed a limiter passes through instead of
	// panicking. It is the only place where the package tolerates a missing
	// bound, and it is visible here so that the production wiring is a
	// deliberate act rather than an accident.
	handler := &countingHandler{}

	var absent *ratelimit.Enforcer
	for name, enforcer := range map[string]*ratelimit.Enforcer{"nil enforcer": absent, "no guard": ratelimit.New(nil, clientip.New(nil))} {
		recorder := httptest.NewRecorder()
		enforcer.Protect(ratelimit.ActionAuthLogin, handler).ServeHTTP(recorder, protectedRequest("198.51.100.7:1234"))
		if recorder.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want the handler's 200", name, recorder.Code)
		}
	}
}

func TestRetryAfterRoundsUpToAtLeastOneSecond(t *testing.T) {
	t.Parallel()

	// Every wait between 0 and 1 second must be advertised as one second.
	for _, wait := range []time.Duration{time.Millisecond, 5 * time.Millisecond, 400 * time.Millisecond, 999 * time.Millisecond, time.Second} {
		guard := &subjectRecorder{decision: ratelimit.Decision{Allowed: false, Refused: ratelimit.SubjectAddress, RetryAfter: wait}}
		recorder := httptest.NewRecorder()
		ratelimit.New(guard, clientip.New(nil)).Protect(ratelimit.ActionAuthLogin, &countingHandler{}).ServeHTTP(recorder, protectedRequest("198.51.100.7:1234"))

		if got := recorder.Header().Get("Retry-After"); got != "1" {
			t.Errorf("RetryAfter = %v advertised %q, want \"1\"", wait, got)
		}
	}

	guard := &subjectRecorder{decision: ratelimit.Decision{Allowed: false, Refused: ratelimit.SubjectAddress, RetryAfter: 61 * time.Second}}
	recorder := httptest.NewRecorder()
	ratelimit.New(guard, clientip.New(nil)).Protect(ratelimit.ActionAuthLogin, &countingHandler{}).ServeHTTP(recorder, protectedRequest("198.51.100.7:1234"))
	if got := recorder.Header().Get("Retry-After"); got != "61" {
		t.Errorf("Retry-After = %q, want \"61\"", got)
	}
}

func TestEnforcerSatisfiesTheProtectorPort(t *testing.T) {
	t.Parallel()

	// The adapters depend on the port; the enforcer is the implementation. The
	// assertion keeps the seam substitutable at composition.
	var _ ratelimit.Protector = ratelimit.New(nil, clientip.New(nil))
	var _ ratelimit.Protector = &ratelimit.Enforcer{}
}

// subjectOf returns the value of one subject kind, or the empty string.
func subjectOf(subjects []ratelimit.Subject, kind ratelimit.SubjectKind) string {
	for _, subject := range subjects {
		if subject.Kind == kind {
			return subject.Value
		}
	}
	return ""
}

func mustTrusted(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()

	prefixes, err := clientip.ParseTrusted(values)
	if err != nil {
		t.Fatalf("ParseTrusted(%v) error = %v", values, err)
	}
	return prefixes
}
