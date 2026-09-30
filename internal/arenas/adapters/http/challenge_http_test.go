// Wiring tests for the publication challenge (P16-T04).
//
// The refusal shape itself is asserted once in internal/platform/turnstile;
// what is asserted here is where the challenge sits. Two facts matter and they
// are different:
//
//   - the challenge is *inside* the session requirement, so an unauthenticated
//     caller is answered about its session (401) and is never sent to solve a
//     challenge for a request it cannot make;
//   - the challenge is applied to the publish route at all, so an
//     authenticated caller without a token is refused by it (403).
//
// The use cases of this handler are left nil on purpose, exactly as the rate
// limit wiring test does: a challenge runs in front of the handler, so a
// request that is refused must never reach the code that would dereference
// them, and a wiring regression fails loudly instead of quietly passing.
package http_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

// acceptingVerifier accepts every non-empty token. The token's validity is not
// what these tests are about.
type acceptingVerifier struct{}

func (acceptingVerifier) Verify(context.Context, turnstile.Verification) error { return nil }

// arenasChallengeMux builds the arena surface with a session validator that
// knows one token and refuses everything else, which is the cheapest honest
// stand-in for the authentication the routes require.
func arenasChallengeMux(t *testing.T, authenticatedFor string) http.Handler {
	t.Helper()

	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		Clock:          clockseed.NewClock(),
		Random:         clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("setup security manager: %v", err)
	}

	enforcer := turnstile.NewEnforcer(turnstile.Config{}, acceptingVerifier{}, nil, clientip.New(nil))
	handler := adapterhttp.NewHandler(adapterhttp.HandlerConfig{
		SecurityManager: secMgr,
		Challenge:       enforcer,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		if rawToken == authenticatedFor {
			return security.AuthIdentity{AccountID: "11111111-1111-1111-1111-111111111111", SessionID: "session"}, nil
		}
		return security.AuthIdentity{}, errors.New("unknown session")
	})
	return secMgr.AuthenticateMiddleware(validator)(mux)
}

func publishRequest(token string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/me/arena-drafts/22222222-2222-2222-2222-222222222222/publish", nil)
	if token != "" {
		request.Header.Set(turnstile.DefaultChallengeHeaderName, token)
	}
	return request
}

// TestPublicationChallengedPlacement pins the two orders down.
func TestPublicationChallengedPlacement(t *testing.T) {
	t.Parallel()

	const sessionToken = "known"
	mux := arenasChallengeMux(t, sessionToken)

	t.Run("an unauthenticated caller meets the session first", func(t *testing.T) {
		t.Parallel()

		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, publishRequest("solved"))

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401: the challenge must not be reached before the session is resolved", recorder.Code)
		}
	})

	t.Run("an authenticated caller must solve the challenge", func(t *testing.T) {
		t.Parallel()

		request := publishRequest("")
		request.AddCookie(&http.Cookie{Name: security.DefaultSessionCookieName, Value: "known"})
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body.String())
		}
		if contentType := recorder.Header().Get("Content-Type"); contentType == "" {
			t.Error("the refusal carried no content type")
		}
	})
}
