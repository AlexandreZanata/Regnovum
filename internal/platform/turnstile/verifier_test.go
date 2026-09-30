package turnstile_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

// testTimeout is the deadline every test verifier runs with. It is short so a
// timeout test stays fast, and long enough that the stub answering instantly
// never trips it.
const testTimeout = 2 * time.Second

// testHostname is the hostname the stub reports as the one the challenge was
// solved for.
const testHostname = "arena.example"

// providerStub stands in for Cloudflare's verification endpoint. It records
// every form it was sent, which is how the tests prove two things the design
// rests on: that the secret's only destination is the provider, and that a
// refusal decided locally never reaches the provider at all.
type providerStub struct {
	mu     sync.Mutex
	forms  []url.Values
	answer func(forms []url.Values) (status int, body string)
}

func newProviderStub(t *testing.T, answer func(forms []url.Values) (int, string)) (*providerStub, string) {
	t.Helper()

	stub := &providerStub{answer: answer}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		stub.mu.Lock()
		stub.forms = append(stub.forms, request.PostForm)
		forms := append([]url.Values(nil), stub.forms...)
		stub.mu.Unlock()

		status, body := stub.answer(forms)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return stub, server.URL
}

func (stub *providerStub) callCount() int {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return len(stub.forms)
}

func (stub *providerStub) lastForm(t *testing.T) url.Values {
	t.Helper()
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.forms) == 0 {
		t.Fatal("the provider was never called")
	}
	return stub.forms[len(stub.forms)-1]
}

// successBody is the answer of a solved challenge for one hostname and action.
func successBody(hostname string, action turnstile.Action) string {
	return fmt.Sprintf(`{"success":true,"hostname":%q,"action":%q,"challenge_ts":"2026-09-18T00:00:00Z"}`, hostname, action)
}

// failureBody is the answer of an unsolved challenge with the provider's codes.
func failureBody(codes ...string) string {
	quoted := make([]string, 0, len(codes))
	for _, code := range codes {
		quoted = append(quoted, fmt.Sprintf("%q", code))
	}
	return fmt.Sprintf(`{"success":false,"error-codes":[%s]}`, strings.Join(quoted, ","))
}

// newVerifier builds the provider-backed verifier of a test.
func newVerifier(t *testing.T, endpoint string, timeout time.Duration) turnstile.Verifier {
	t.Helper()

	verifier, err := turnstile.New(turnstile.Config{
		SecretKey: testSecret,
		Hostname:  testHostname,
		Endpoint:  endpoint,
		Timeout:   timeout,
		Client:    &http.Client{},
	}, "test")
	if err != nil {
		t.Fatalf("build verifier: %v", err)
	}
	return verifier
}

// TestVerificationSendsTheSecretOnlyToTheProvider is the reachability half of
// "no secret no browser": the secret goes out in exactly one direction, in the
// body of the provider request, together with the token and the caller's
// address.
func TestVerificationSendsTheSecretOnlyToTheProvider(t *testing.T) {
	t.Parallel()

	stub, endpoint := newProviderStub(t, func([]url.Values) (int, string) {
		return http.StatusOK, successBody(testHostname, turnstile.ActionSignup)
	})
	verifier := newVerifier(t, endpoint, testTimeout)

	if err := verifier.Verify(context.Background(), turnstile.Verification{
		Token:  "solved-token",
		Action: turnstile.ActionSignup,
		Client: "203.0.113.7",
	}); err != nil {
		t.Fatalf("a solved challenge was refused: %v", err)
	}

	form := stub.lastForm(t)
	if form.Get("secret") != testSecret {
		t.Errorf("the provider received secret %q, want the configured secret", form.Get("secret"))
	}
	if form.Get("response") != "solved-token" {
		t.Errorf("the provider received response %q, want the token", form.Get("response"))
	}
	if form.Get("remoteip") != "203.0.113.7" {
		t.Errorf("the provider received remoteip %q, want the caller address", form.Get("remoteip"))
	}
}

// TestVerificationClassifiesTheProviderAnswers walks the answers the provider
// actually gives and asserts the code each one becomes. The validation items
// token inválido / replay / action errada are three rows here, and the rows
// that must never be confused are separated: an outage is not a wrong secret.
func TestVerificationClassifiesTheProviderAnswers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		action     turnstile.Action
		answer     func([]url.Values) (int, string)
		wantCode   string
		wantUnavai bool
	}{
		{
			name:   "solved for the right action and hostname",
			action: turnstile.ActionSignup,
			answer: func([]url.Values) (int, string) {
				return http.StatusOK, successBody(testHostname, turnstile.ActionSignup)
			},
		},
		{
			name:   "invalid token",
			action: turnstile.ActionSignup,
			answer: func([]url.Values) (int, string) {
				return http.StatusOK, failureBody("invalid-input-response")
			},
			wantCode: turnstile.CodeChallengeInvalid,
		},
		{
			name:   "provider reports the token as already used or expired",
			action: turnstile.ActionSignup,
			answer: func([]url.Values) (int, string) {
				return http.StatusOK, failureBody("timeout-or-duplicate")
			},
			wantCode: turnstile.CodeChallengeReplayed,
		},
		{
			name:   "token minted for another action",
			action: turnstile.ActionArenaPublish,
			answer: func([]url.Values) (int, string) {
				return http.StatusOK, successBody(testHostname, turnstile.ActionSignup)
			},
			wantCode: turnstile.CodeChallengeActionMismatch,
		},
		{
			name:   "token minted for another hostname",
			action: turnstile.ActionSignup,
			answer: func([]url.Values) (int, string) {
				return http.StatusOK, successBody("other.example", turnstile.ActionSignup)
			},
			wantCode: turnstile.CodeChallengeHostnameMismatch,
		},
		{
			name:   "provider internal error",
			action: turnstile.ActionSignup,
			answer: func([]url.Values) (int, string) {
				return http.StatusOK, failureBody("internal-error")
			},
			wantCode:   turnstile.CodeChallengeUnavailable,
			wantUnavai: true,
		},
		{
			name:   "provider rejects our secret",
			action: turnstile.ActionSignup,
			answer: func([]url.Values) (int, string) {
				return http.StatusOK, failureBody("invalid-input-secret")
			},
			wantCode: turnstile.CodeChallengeMisconfigured,
		},
		{
			name:   "provider rejects our request",
			action: turnstile.ActionSignup,
			answer: func([]url.Values) (int, string) {
				return http.StatusOK, failureBody("bad-request")
			},
			wantCode: turnstile.CodeChallengeMisconfigured,
		},
		{
			name:   "provider answers a status that is not 200",
			action: turnstile.ActionSignup,
			answer: func([]url.Values) (int, string) {
				return http.StatusBadGateway, "gateway"
			},
			wantCode:   turnstile.CodeChallengeUnavailable,
			wantUnavai: true,
		},
		{
			name:   "provider answers something that is not JSON",
			action: turnstile.ActionSignup,
			answer: func([]url.Values) (int, string) {
				return http.StatusOK, "<html>we are down</html>"
			},
			wantCode:   turnstile.CodeChallengeUnavailable,
			wantUnavai: true,
		},
		{
			name:   "provider answers success with no action at all",
			action: turnstile.ActionSignup,
			answer: func([]url.Values) (int, string) {
				return http.StatusOK, `{"success":true,"hostname":"` + testHostname + `"}`
			},
			wantCode: turnstile.CodeChallengeActionMismatch,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, endpoint := newProviderStub(t, testCase.answer)
			verifier := newVerifier(t, endpoint, testTimeout)

			// A distinct token per case: the replay memory is per verifier and
			// each case builds its own, but a distinct token keeps the intent
			// obvious.
			err := verifier.Verify(context.Background(), turnstile.Verification{
				Token:  "token-" + strings.ReplaceAll(testCase.name, " ", "-"),
				Action: testCase.action,
			})

			if testCase.wantCode == "" {
				if err != nil {
					t.Fatalf("verification failed: %v", err)
				}
				return
			}
			if got := codeOf(t, err); got != testCase.wantCode {
				t.Fatalf("code = %q, want %q (error: %v)", got, testCase.wantCode, err)
			}
			if got := turnstile.IsUnavailable(err); got != testCase.wantUnavai {
				t.Errorf("IsUnavailable = %v, want %v — the fail policy applies to exactly one of these", got, testCase.wantUnavai)
			}
		})
	}
}

// TestVerificationTimesOutWithoutHangingTheRequest is the timeout item: the
// provider that never answers must not hold the request, and the answer must
// be "we could not check", not "you were refused".
func TestVerificationTimesOutWithoutHangingTheRequest(t *testing.T) {
	t.Parallel()

	// The provider holds the answer well past the deadline and then answers, so
	// the assertion is about the deadline rather than about a connection that
	// could also have been refused. The delay is bounded on purpose: an
	// unbounded handler would keep httptest.Server.Close waiting and turn a
	// failure into a hung test.
	const answerDelay = 600 * time.Millisecond
	const deadline = 100 * time.Millisecond

	_, endpoint := newProviderStub(t, func([]url.Values) (int, string) {
		time.Sleep(answerDelay)
		return http.StatusOK, successBody(testHostname, turnstile.ActionSignup)
	})
	verifier := newVerifier(t, endpoint, deadline)

	start := time.Now()
	err := verifier.Verify(context.Background(), turnstile.Verification{Token: "slow-token", Action: turnstile.ActionSignup})
	elapsed := time.Since(start)

	if codeOf(t, err) != turnstile.CodeChallengeUnavailable {
		t.Fatalf("code = %q, want %q (error: %v)", codeOf(t, err), turnstile.CodeChallengeUnavailable, err)
	}
	if !turnstile.IsUnavailable(err) {
		t.Error("IsUnavailable = false for a timeout; the fail policy would not apply to it")
	}
	if elapsed >= answerDelay {
		t.Errorf("verification waited %v for a %v deadline: the deadline did not cut the call", elapsed, deadline)
	}
}

// TestVerificationRefusesAnOversizedTokenWithoutCallingTheProvider bounds what
// a caller can make the server carry outbound: a token beyond the accepted
// size is refused before any request is made.
func TestVerificationRefusesAnOversizedTokenWithoutCallingTheProvider(t *testing.T) {
	t.Parallel()

	stub, endpoint := newProviderStub(t, func([]url.Values) (int, string) {
		return http.StatusOK, successBody(testHostname, turnstile.ActionSignup)
	})
	verifier := newVerifier(t, endpoint, testTimeout)

	err := verifier.Verify(context.Background(), turnstile.Verification{
		Token:  strings.Repeat("a", turnstile.MaxTokenBytes+1),
		Action: turnstile.ActionSignup,
	})
	if codeOf(t, err) != turnstile.CodeChallengeInvalid {
		t.Errorf("code = %q, want %q", codeOf(t, err), turnstile.CodeChallengeInvalid)
	}
	if calls := stub.callCount(); calls != 0 {
		t.Errorf("the provider was called %d time(s) for an oversized token, want 0", calls)
	}
}

// TestASpentTokenIsRefusedLocallyWithoutAskingTheProvider is the replay item
// at the layer that owns it: the second presentation of a token never leaves
// the process.
func TestASpentTokenIsRefusedLocallyWithoutAskingTheProvider(t *testing.T) {
	t.Parallel()

	stub, endpoint := newProviderStub(t, func([]url.Values) (int, string) {
		return http.StatusOK, successBody(testHostname, turnstile.ActionSignup)
	})
	verifier := newVerifier(t, endpoint, testTimeout)
	verification := turnstile.Verification{Token: "one-shot", Action: turnstile.ActionSignup}

	if err := verifier.Verify(context.Background(), verification); err != nil {
		t.Fatalf("the first use was refused: %v", err)
	}
	if err := verifier.Verify(context.Background(), verification); codeOf(t, err) != turnstile.CodeChallengeReplayed {
		t.Errorf("the second use: code = %q, want %q", codeOf(t, err), turnstile.CodeChallengeReplayed)
	}
	if calls := stub.callCount(); calls != 1 {
		t.Errorf("the provider was called %d time(s), want exactly 1: a spent token must be refused here", calls)
	}
}

// TestConcurrentReplaysOfOneTokenProduceExactlyOneVerification is what makes
// the single-use claim mean something under concurrency: the claim is atomic,
// so a burst of replays of one token cannot be served, and the provider is
// asked once.
func TestConcurrentReplaysOfOneTokenProduceExactlyOneVerification(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	_, endpoint := newProviderStub(t, func([]url.Values) (int, string) {
		calls.Add(1)
		return http.StatusOK, successBody(testHostname, turnstile.ActionSignup)
	})
	verifier := newVerifier(t, endpoint, testTimeout)

	const attempts = 16
	verification := turnstile.Verification{Token: "raced-token", Action: turnstile.ActionSignup}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		codes = make([]string, 0, attempts)
	)
	start := make(chan struct{})
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// The outcome is collected rather than asserted inside the
			// goroutine: a test helper that fails has to run on the test's own
			// goroutine.
			err := verifier.Verify(context.Background(), verification)
			code := ""
			if err != nil {
				code = "error:" + err.Error()
				var appError *apperr.Error
				if errors.As(err, &appError) {
					code = appError.Code()
				}
			}
			mu.Lock()
			codes = append(codes, code)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	allowed, replays := 0, 0
	for _, code := range codes {
		switch code {
		case "":
			allowed++
		case turnstile.CodeChallengeReplayed:
			replays++
		default:
			t.Errorf("unexpected outcome: %s", code)
		}
	}

	if allowed != 1 {
		t.Errorf("%d attempts were allowed, want exactly 1", allowed)
	}
	if replays != attempts-1 {
		t.Errorf("%d attempts were reported as replays, want %d", replays, attempts-1)
	}
	if calls.Load() != 1 {
		t.Errorf("the provider was called %d time(s), want 1", calls.Load())
	}
}

// TestReplayMemoryIsBoundedInTimeAndInSize asserts the two bounds, because an
// unbounded memory of redeemed tokens is a denial of service that grows with
// traffic.
func TestReplayMemoryIsBoundedInTimeAndInSize(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }

	memory := turnstile.NewReplayMemory(2, time.Minute, clock)

	if !memory.Redeem("a") || !memory.Redeem("b") {
		t.Fatal("the first two tokens were not redeemable")
	}
	if memory.Redeem("a") {
		t.Error("a token was redeemable twice")
	}

	// Capacity: redeeming a third token evicts the least recently *touched*
	// one. The replay of "a" above is a touch on purpose, so the token that is
	// forgotten here is "b": a token an attacker is hammering stays remembered
	// until it could not have lived any longer, which is what makes the replay
	// refusal reliable.
	if !memory.Redeem("c") {
		t.Fatal("the third token was not redeemable")
	}
	if got := memory.Len(); got != 2 {
		t.Errorf("Len = %d, want the capacity 2", got)
	}
	if memory.Redeem("a") {
		t.Error("the most recently touched token was evicted at capacity 2")
	}
	if !memory.Redeem("b") {
		t.Error("the least recently touched token was still remembered at capacity 2")
	}

	// Expiry: a token is forgotten once it could not have lived any longer.
	now = now.Add(2 * time.Minute)
	if !memory.Redeem("a") {
		t.Error("a token older than its lifetime was still remembered")
	}
}

// TestIsUnavailableIsFalseForErrorsThatAreNotOurs keeps the fail policy from
// swallowing errors it has no business deciding on.
func TestIsUnavailableIsFalseForErrorsThatAreNotOurs(t *testing.T) {
	t.Parallel()

	if turnstile.IsUnavailable(nil) {
		t.Error("IsUnavailable(nil) = true")
	}
	if turnstile.IsUnavailable(errors.New("plain")) {
		t.Error("IsUnavailable(plain error) = true")
	}
}
