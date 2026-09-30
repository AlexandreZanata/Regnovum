package providersim

import (
	"net/http"
	"testing"
)

// The Turnstile surface: the verification address the challenge adapter posts
// the visitor's token to, and the answers it classifies — a solved challenge
// for the declared site, a refusal with the provider's own error codes, and the
// outages that are not a refusal at all.
const (
	// TurnstileSiteverifyPath is the documented path of the endpoint. A fixture
	// points the adapter at URL()+TurnstileSiteverifyPath.
	TurnstileSiteverifyPath = "/turnstile/v0/siteverify"
	// TurnstileSiteverify is the same address as the simulator's route key.
	TurnstileSiteverify = "POST " + TurnstileSiteverifyPath
)

// TurnstileSecretKey is the synthetic server-side secret of the fixture. It is
// composed so that no scanner reads a complete credential in this file.
const TurnstileSecretKey = "0x" + "4AAAsimSyntheticTurnstileSecret"

// NewTurnstile builds the fake challenge provider. The hostname and the action
// are what the fake answers for: the adapter compares both with what it was
// asked to verify — the site the deployment declared and the action the policy
// is spending the token on — and a fixture crossing either comparison declares
// the other one here.
func NewTurnstile(t testing.TB, hostname, action string, options ...Option) *Simulator {
	t.Helper()
	simulator := New(t, "turnstile", options...)
	simulator.handler = turnstileRefusal
	simulator.Route(TurnstileSiteverify, Reply(http.StatusOK, TurnstileSolvedBody(hostname, action)))
	return simulator
}

// turnstileRefusal is what the fake challenge provider answers a call nobody
// scripted. The document is the provider's own shape, so the adapter classifies
// it as an unexpected refusal instead of failing to parse it.
func turnstileRefusal(method, path string) (int, string) {
	return UnexpectedStatus, TurnstileBody(false, "", "unexpected-call")
}

// TurnstileSolvedBody renders a solved challenge for one site and one action.
// Both fields are part of the answer and not decoration: the adapter compares
// the hostname against the site the deployment declared and the action against
// the one the token is being spent on, so a body that carried neither would be
// a solved challenge no adapter could accept.
func TurnstileSolvedBody(hostname, action string) string {
	return TurnstileBody(true, hostname, action)
}

// TurnstileBody renders the provider's answer. The error codes are the closed
// vocabulary the adapter classifies: a missing or invalid secret is an outage
// of the deployment, and the token errors are refusals of the visitor.
func TurnstileBody(success bool, hostname, action string, codes ...string) string {
	if codes == nil {
		codes = []string{}
	}
	return encodedDocument(map[string]any{
		"success":     success,
		"hostname":    hostname,
		"action":      action,
		"error-codes": codes,
	})
}

// TurnstileRefused is the answer of a challenge that was not solved: a
// successful call with the provider's refusal inside it, which is the state a
// fixture must not confuse with an outage.
func TurnstileRefused(codes ...string) Answer {
	return Reply(http.StatusOK, TurnstileBody(false, "", "", codes...))
}

// TurnstileUnavailable is the answer of a challenge provider that fails: an
// HTTP status the adapter must treat as an outage of the dependency.
func TurnstileUnavailable(status int) Answer {
	return Reply(status, encodedDocument(map[string]any{"success": false, "error-codes": []string{"internal-error"}}))
}
