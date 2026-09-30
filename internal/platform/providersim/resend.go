package providersim

import (
	"net/http"
	"testing"
)

// The Resend surface: the one address the mail adapter posts to, and the two
// documents it reads — a receipt with an identifier, and an error with the
// provider's own name and message.
const (
	ResendEmails = "POST /emails"
)

// ResendAPIKey is the synthetic credential of the fixture. It is composed so
// that no scanner reads a complete credential in this file, and it is the only
// key the adapter ever needs: the fake provider is what makes an external one
// unnecessary.
const ResendAPIKey = "re" + "_sim_synthetic_api_key"

// ResendSenderAddress is the synthetic sender a fixture declares.
const ResendSenderAddress = "no-reply@example.test"

// NewResend builds the fake mail provider with the send accepted.
func NewResend(t testing.TB, options ...Option) *Simulator {
	t.Helper()
	simulator := New(t, "resend", options...)
	simulator.handler = resendRefusal
	simulator.Route(ResendEmails, Reply(http.StatusOK, ResendAcceptedBody("msg_sim_1")))
	return simulator
}

// resendRefusal is what the fake mail provider answers a call nobody scripted.
func resendRefusal(method, path string) (int, string) {
	return UnexpectedStatus, ResendErrorBody(UnexpectedStatus, "not_scripted", method+" "+path+" is not scripted")
}

// ResendAcceptedBody renders the receipt of an accepted message. The adapter
// reads the identifier and treats a body without one as an unavailable
// provider, so the document is what makes a send successful.
func ResendAcceptedBody(id string) string {
	return encodedDocument(map[string]any{"id": id})
}

// ResendErrorBody renders the provider's error document. The adapter reads the
// name and the message to classify the failure, so both are carried.
func ResendErrorBody(status int, name, message string) string {
	return encodedDocument(map[string]any{
		"statusCode": status,
		"name":       name,
		"message":    message,
	})
}

// ResendError is the answer of a mail provider that refuses one send.
func ResendError(status int, name, message string) Answer {
	return Reply(status, ResendErrorBody(status, name, message))
}
