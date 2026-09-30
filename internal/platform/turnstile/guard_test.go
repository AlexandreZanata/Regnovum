package turnstile_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

// recordingVerifier is a Verifier that answers whatever the test told it to and
// remembers what it was asked. Using the real enforcer over it keeps the
// policy, the extraction and the refusal shape under test.
type recordingVerifier struct {
	mu   sync.Mutex
	seen []turnstile.Verification
	err  error
}

func (verifier *recordingVerifier) Verify(_ context.Context, verification turnstile.Verification) error {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	verifier.seen = append(verifier.seen, verification)

	if verifier.err != nil {
		return verifier.err
	}
	// The stub answers like a real verifier for the one case that is not about
	// verification at all: no token was presented. A stub that accepted an
	// empty token would let a test assert that a refusal happened while the
	// refusal never had a reason to.
	if verification.Token == "" {
		return apperr.New(apperr.KindForbidden, turnstile.CodeChallengeRequired, "a challenge token is required for this action")
	}
	return nil
}

func (verifier *recordingVerifier) calls() []turnstile.Verification {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	return append([]turnstile.Verification(nil), verifier.seen...)
}

// enforcerFor builds the real enforcer of a test.
func enforcerFor(verifier turnstile.Verifier, tracker *turnstile.FailureTracker, configuration turnstile.Config) *turnstile.Enforcer {
	return turnstile.NewEnforcer(configuration, verifier, tracker, clientip.New(nil))
}

// guarded builds a one-route mux whose handler records that it ran.
func guarded(t *testing.T, action turnstile.Action, enforcer turnstile.Challenger) (*http.ServeMux, *bool) {
	t.Helper()

	ran := false
	mux := http.NewServeMux()
	mux.Handle("POST /guarded", enforcer.Challenge(action, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ran = true
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("handled"))
	})))
	return mux, &ran
}

// refusalCode decodes the stable code of a problem response.
func refusalCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("refusal body is not a problem document: %v (%s)", err, recorder.Body.String())
	}
	return problem.Code
}

// TestTheChallengeHeaderCarriesTheTokenAndTheClientIsResolved is the happy
// path: the header token reaches the verifier with the action and the caller's
// address, and the handler runs when it is solved.
func TestTheChallengeHeaderCarriesTheTokenAndTheClientIsResolved(t *testing.T) {
	t.Parallel()

	verifier := &recordingVerifier{}
	mux, ran := guarded(t, turnstile.ActionSignup, enforcerFor(verifier, nil, turnstile.Config{}))

	request := httptest.NewRequest(http.MethodPost, "/guarded", nil)
	request.Header.Set(turnstile.DefaultChallengeHeaderName, "solved")
	request.RemoteAddr = "203.0.113.7:41234"
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if !*ran {
		t.Fatalf("the handler did not run (status %d, body %s)", recorder.Code, recorder.Body.String())
	}
	seen := verifier.calls()
	if len(seen) != 1 {
		t.Fatalf("the verifier was called %d time(s), want 1", len(seen))
	}
	if seen[0].Token != "solved" || seen[0].Action != turnstile.ActionSignup {
		t.Errorf("the verifier saw %+v, want the header token and the route's action", seen[0])
	}
	if seen[0].Client != "203.0.113.7" {
		t.Errorf("the verifier saw client %q, want the resolved peer address", seen[0].Client)
	}
}

// TestTheWidgetFormFieldCarriesTheTokenWithoutABodyBeingConsumed covers the
// two halves of extraction: a form body's own field is read, and a JSON body
// is left for the handler that has to decode it.
func TestTheWidgetFormFieldCarriesTheTokenWithoutABodyBeingConsumed(t *testing.T) {
	t.Parallel()

	t.Run("form", func(t *testing.T) {
		t.Parallel()

		verifier := &recordingVerifier{}
		mux, ran := guarded(t, turnstile.ActionSignup, enforcerFor(verifier, nil, turnstile.Config{}))

		body := strings.NewReader(turnstile.DefaultChallengeFormField + "=form-token")
		request := httptest.NewRequest(http.MethodPost, "/guarded", body)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if !*ran {
			t.Fatalf("the handler did not run (status %d, body %s)", recorder.Code, recorder.Body.String())
		}
		seen := verifier.calls()
		if len(seen) != 1 || seen[0].Token != "form-token" {
			t.Fatalf("the verifier saw %+v, want the form field's token", seen)
		}
	})

	t.Run("json body is not touched", func(t *testing.T) {
		t.Parallel()

		// The guard runs before the handler, so a guard that parsed the body
		// to look for a form field would leave the handler an empty document.
		// This is the regression the extraction rule exists for.
		verifier := &recordingVerifier{}
		enforcer := enforcerFor(verifier, nil, turnstile.Config{})

		var decoded map[string]string
		mux := http.NewServeMux()
		mux.Handle("POST /guarded", enforcer.Challenge(turnstile.ActionSignup, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			payload, err := io.ReadAll(request.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
			}
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Errorf("decode body: %v (payload %q)", err, payload)
			}
			writer.WriteHeader(http.StatusOK)
		})))

		request := httptest.NewRequest(http.MethodPost, "/guarded", strings.NewReader(`{"email":"a@b.c"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(turnstile.DefaultChallengeHeaderName, "solved")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		if decoded["email"] != "a@b.c" {
			t.Errorf("the handler decoded %v, want the JSON body untouched", decoded)
		}
	})
}

// TestAMissingTokenIsRefusedBeforeTheHandlerRuns asserts the two things a
// refusal must guarantee: the handler never runs, and the answer is a problem
// document carrying the stable code.
func TestAMissingTokenIsRefusedBeforeTheHandlerRuns(t *testing.T) {
	t.Parallel()

	verifier := &recordingVerifier{}
	var configuration turnstile.Config // the zero value, which must behave closed
	mux, ran := guarded(t, turnstile.ActionSignup, enforcerFor(verifier, nil, configuration))

	request := httptest.NewRequest(http.MethodPost, "/guarded", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if *ran {
		t.Error("the handler ran without a challenge token")
	}
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", recorder.Code)
	}
	if code := refusalCode(t, recorder); code != turnstile.CodeChallengeRequired {
		t.Errorf("code = %q, want %q", code, turnstile.CodeChallengeRequired)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
		t.Errorf("Content-Type = %q, want a problem document", contentType)
	}
	// A missing precondition is not a verification: asking the verifier would
	// make the answer depend on the provider's reachability, which is exactly
	// what the open fail policy must not be able to influence.
	if calls := verifier.calls(); len(calls) != 0 {
		t.Errorf("the verifier was called %d time(s) without a token, want 0", len(calls))
	}
}

// TestRiskGatedActionsChallengeOnlyOnElevatedRisk is the elevated-risk item:
// the same action is free for a caller with no failures, gated after a run of
// failures, and free again after a success — and a composition without a risk
// signal challenges everyone rather than nobody.
func TestRiskGatedActionsChallengeOnlyOnElevatedRisk(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	tracker := turnstile.NewFailureTracker(turnstile.FailureTrackerOptions{
		Threshold: 3,
		Window:    10 * time.Minute,
		Now:       func() time.Time { return now },
	})
	verifier := &recordingVerifier{}
	enforcer := enforcerFor(verifier, tracker, turnstile.Config{})
	mux, ran := guarded(t, turnstile.ActionLoginElevated, enforcer)

	// The same caller for the requests and for the observed outcomes: the
	// signal is keyed on the peer address, so a helper that forgot it would
	// count the failures of somebody else.
	fromCaller := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/guarded", nil)
		request.RemoteAddr = "198.51.100.9:5555"
		return request
	}
	request := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, fromCaller())
		return recorder
	}

	*ran = false
	if recorder := request(); !*ran {
		t.Fatalf("a caller with no failures was challenged (status %d, body %s)", recorder.Code, recorder.Body.String())
	}
	if calls := verifier.calls(); len(calls) != 0 {
		t.Fatalf("the verifier was called %d time(s) for an unelevated caller, want 0", len(calls))
	}

	// Two failures are below the threshold; the third makes the caller
	// elevated, and the next request meets the challenge.
	enforcer.Observe(fromCaller(), true)
	enforcer.Observe(fromCaller(), true)
	*ran = false
	if recorder := request(); !*ran {
		t.Fatalf("two failures challenged the caller (status %d)", recorder.Code)
	}

	enforcer.Observe(fromCaller(), true)
	*ran = false
	recorder := request()
	if *ran {
		t.Error("an elevated caller was not challenged")
	}
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", recorder.Code)
	}

	// A success clears the run: the caller is a person again.
	enforcer.Observe(fromCaller(), false)
	*ran = false
	if recorder := request(); !*ran {
		t.Fatalf("a caller whose run was cleared was challenged (status %d)", recorder.Code)
	}

	// Without a risk signal the requirement cannot be decided, so the safe
	// reading is applied: everyone is challenged.
	blind, blindRan := guarded(t, turnstile.ActionLoginElevated, enforcerFor(&recordingVerifier{}, nil, turnstile.Config{}))
	blindRecorder := httptest.NewRecorder()
	blind.ServeHTTP(blindRecorder, httptest.NewRequest(http.MethodPost, "/guarded", nil))
	if *blindRan {
		t.Error("a risk-gated action with no risk signal let a caller through unchallenged")
	}
	if blindRecorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", blindRecorder.Code)
	}
}

// TestTheFailPolicyAppliesToAnUnreachableProviderOnly is the fail policy item.
// The policy is about one answer (nobody told us whether the token is good) and
// about nothing else: a replayed token, an invalid token and our own
// misconfiguration are refused under both policies.
func TestTheFailPolicyAppliesToAnUnreachableProviderOnly(t *testing.T) {
	t.Parallel()

	unavailable := apperr.New(apperr.KindInternal, turnstile.CodeChallengeUnavailable, "the challenge could not be verified")

	t.Run("closed by default", func(t *testing.T) {
		t.Parallel()

		verifier := &recordingVerifier{err: unavailable}
		mux, ran := guarded(t, turnstile.ActionSignup, enforcerFor(verifier, nil, turnstile.Config{}))

		request := httptest.NewRequest(http.MethodPost, "/guarded", nil)
		request.Header.Set(turnstile.DefaultChallengeHeaderName, "solved")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if *ran {
			t.Error("the action proceeded while the challenge could not be verified")
		}
		if recorder.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500: the server failed, and saying 403 would blame the caller", recorder.Code)
		}
		if code := refusalCode(t, recorder); code != turnstile.CodeChallengeUnavailable {
			t.Errorf("code = %q, want %q", code, turnstile.CodeChallengeUnavailable)
		}
	})

	t.Run("open when asked for by name", func(t *testing.T) {
		t.Parallel()

		verifier := &recordingVerifier{err: unavailable}
		mux, ran := guarded(t, turnstile.ActionSignup, enforcerFor(verifier, nil, turnstile.Config{FailPolicy: turnstile.FailOpen}))

		request := httptest.NewRequest(http.MethodPost, "/guarded", nil)
		request.Header.Set(turnstile.DefaultChallengeHeaderName, "solved")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if !*ran {
			t.Fatalf("the action did not proceed under the open policy (status %d)", recorder.Code)
		}
	})

	t.Run("open does not serve through a missing token", func(t *testing.T) {
		t.Parallel()

		// The policy is about an answer we did not get, never about an input we
		// never had: with no token there is nothing to verify and nothing to
		// forgive, so the refusal stands under both policies.
		verifier := &recordingVerifier{}
		mux, ran := guarded(t, turnstile.ActionSignup, enforcerFor(verifier, nil, turnstile.Config{FailPolicy: turnstile.FailOpen}))

		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/guarded", nil))

		if *ran {
			t.Error("the action proceeded without a challenge token under the open policy")
		}
		if code := refusalCode(t, recorder); code != turnstile.CodeChallengeRequired {
			t.Errorf("code = %q, want %q", code, turnstile.CodeChallengeRequired)
		}
	})

	t.Run("a refusal is never served through", func(t *testing.T) {
		t.Parallel()

		for _, testCase := range []struct {
			name string
			err  *apperr.Error
			code string
		}{
			{"invalid", apperr.New(apperr.KindForbidden, turnstile.CodeChallengeInvalid, "invalid"), turnstile.CodeChallengeInvalid},
			{"replayed", apperr.New(apperr.KindForbidden, turnstile.CodeChallengeReplayed, "replayed"), turnstile.CodeChallengeReplayed},
			{"misconfigured", apperr.New(apperr.KindInternal, turnstile.CodeChallengeMisconfigured, "misconfigured"), turnstile.CodeChallengeMisconfigured},
		} {
			verifier := &recordingVerifier{err: testCase.err}
			mux, ran := guarded(t, turnstile.ActionSignup, enforcerFor(verifier, nil, turnstile.Config{FailPolicy: turnstile.FailOpen}))

			request := httptest.NewRequest(http.MethodPost, "/guarded", nil)
			request.Header.Set(turnstile.DefaultChallengeHeaderName, "some-token")
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)

			if *ran {
				t.Errorf("%s: the action proceeded under the open policy", testCase.name)
			}
			if code := refusalCode(t, recorder); code != testCase.code {
				t.Errorf("%s: code = %q, want %q", testCase.name, code, testCase.code)
			}
		}
	})

	t.Run("no verifier at all is the documented disabled state", func(t *testing.T) {
		t.Parallel()

		mux, ran := guarded(t, turnstile.ActionSignup, enforcerFor(nil, nil, turnstile.Config{}))
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/guarded", nil))

		if !*ran {
			t.Errorf("a composition without a verifier refused the request (status %d)", recorder.Code)
		}
	})
}

// TestNoResponseEverCarriesTheSecret drives the real enforcer over the real
// provider verifier and fails if the secret appears anywhere the caller can
// see. It is the "nenhuma secret no browser" item at the layer that owns the
// value: whatever the outcome, the only place the secret went was the
// provider.
func TestNoResponseEverCarriesTheSecret(t *testing.T) {
	t.Parallel()

	stub, endpoint := newProviderStub(t, func(forms []url.Values) (int, string) {
		// The first call is a solved challenge; the second is a provider that
		// rejects our secret, which is the answer most likely to be echoed by
		// a careless implementation.
		if len(forms) == 1 {
			return http.StatusOK, successBody(testHostname, turnstile.ActionSignup)
		}
		return http.StatusOK, failureBody("invalid-input-secret")
	})

	verifier := newVerifier(t, endpoint, testTimeout)
	enforcer := enforcerFor(verifier, nil, turnstile.Config{})

	mux := http.NewServeMux()
	mux.Handle("POST /guarded", enforcer.Challenge(turnstile.ActionSignup, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"status":"handled"}`))
	})))

	tokens := []string{"solved-token", "second-token", ""}
	for index, token := range tokens {
		request := httptest.NewRequest(http.MethodPost, "/guarded", nil)
		if token != "" {
			request.Header.Set(turnstile.DefaultChallengeHeaderName, token)
		}
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		rendered := recorder.Body.String()
		for name, value := range recorder.Header() {
			rendered += name + ": " + strings.Join(value, ", ") + "\n"
		}
		if strings.Contains(rendered, testSecret) {
			t.Fatalf("request %d: the response carried the secret: %s", index, rendered)
		}
	}

	if calls := stub.callCount(); calls == 0 {
		t.Fatal("the provider was never called; the assertion above would pass without the secret being used at all")
	}
}

// TestObserveIsKeyedOnTheResolvedAddress keeps the risk signal honest: the run
// is counted against the address clientip decides, so a spoofed forwarding
// header cannot move a caller's failures onto somebody else.
func TestObserveIsKeyedOnTheResolvedAddress(t *testing.T) {
	t.Parallel()

	tracker := turnstile.NewFailureTracker(turnstile.FailureTrackerOptions{Threshold: 1, Window: time.Minute})
	enforcer := turnstile.NewEnforcer(turnstile.Config{}, nil, tracker, clientip.New(nil))

	failing := httptest.NewRequest(http.MethodPost, "/guarded", nil)
	failing.RemoteAddr = "198.51.100.1:1000"
	failing.Header.Set("X-Forwarded-For", "203.0.113.99")
	enforcer.Observe(failing, true)

	if !tracker.Elevated("198.51.100.1") {
		t.Error("the peer address that failed is not elevated")
	}
	if tracker.Elevated("203.0.113.99") {
		t.Error("a spoofed forwarding header was counted as the caller")
	}
}
