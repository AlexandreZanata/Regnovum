// Package contract holds the executable contract of the email sender port
// (P15-T03). One suite, run against every implementation, is what keeps the
// in-memory fake used by the application tests and the provider adapter from
// drifting apart: a change in the agreed behaviour fails here once, for both.
//
// The package ships no production code and is imported only from tests, but
// it is a regular package on purpose — an external test package may import
// it, and a test file cannot.
package contract

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// The fixture values. Recipient and code are distinctive strings so a test
// can assert they never surface in an error or a log line.
const (
	// Recipient is the fixture mailbox.
	Recipient = "ana.silva@example.com"
	// Code is the fixture one-time code.
	Code = "K7QP-2M4Z-9RTX"
	// Subject, Text and HTML stand in for a rendered body.
	Subject = "Confirme seu email no Regnovum"
	Text    = "Olá, Ana\n\nUse o código abaixo.\n\nCódigo: " + Code
	HTML    = "<p>Olá, Ana</p><p>" + Code + "</p>"
	// IdempotencyKey is the fixture delivery key.
	IdempotencyKey = "email:verification:8f14e45f"
)

// OutcomeClass is the semantic answer a harness is asked to produce. It is
// a class rather than a bare status code because the contract is about the
// behaviour the caller observes, not about how a provider encodes it.
type OutcomeClass int

const (
	// ClassSuccess accepts the message and returns a receipt.
	ClassSuccess OutcomeClass = iota
	// ClassRateLimited throttles the request.
	ClassRateLimited
	// ClassUnavailable fails without processing the message.
	ClassUnavailable
	// ClassRejected refuses the request permanently.
	ClassRejected
	// ClassTimeout answers slower than the configured deadline.
	ClassTimeout
	// ClassReceiptless accepts the request but returns no receipt.
	ClassReceiptless
)

// Outcome programs the next attempt.
type Outcome struct {
	Class OutcomeClass
}

// Attempt is what an implementation actually sent, recorded for assertions.
type Attempt struct {
	Recipient      string
	Subject        string
	Text           string
	HTML           string
	IdempotencyKey string
}

// Harness adapts one implementation to the suite.
type Harness interface {
	// Sender returns the implementation under test.
	Sender() application.Sender
	// Timeout reports the deadline the implementation was configured with,
	// so a timeout outcome can be programmed against it.
	Timeout() time.Duration
	// Program makes the next attempt answer with the outcome.
	Program(outcome Outcome)
	// Attempts returns everything sent so far, in order.
	Attempts() []Attempt
}

// Message returns the fixture message the suite sends.
func Message(t *testing.T) domain.Message {
	t.Helper()
	message, err := domain.NewMessage(
		Recipient,
		domain.LocaleBrazilianPortuguese,
		domain.TemplateVerification,
		domain.Body{Subject: Subject, Text: Text, HTML: HTML},
		IdempotencyKey,
	)
	if err != nil {
		t.Fatalf("contract: build message: %v", err)
	}
	return message
}

// RunSenderContract exercises the port contract. Every implementation of the
// sender port must pass it unchanged.
func RunSenderContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	t.Helper()

	t.Run("delivers once and reports a receipt", func(t *testing.T) {
		harness := newHarness(t)
		harness.Program(Outcome{Class: ClassSuccess})
		receipt, err := harness.Sender().Send(context.Background(), Message(t))
		if err != nil {
			t.Fatalf("Send() error = %v, want nil", err)
		}
		if receipt.ProviderID == "" {
			t.Error("receipt has no provider id")
		}
		attempts := harness.Attempts()
		if len(attempts) != 1 {
			t.Fatalf("attempts = %d, want 1", len(attempts))
		}
		sent := attempts[0]
		if sent.Recipient != Recipient || sent.Subject != Subject || sent.Text != Text || sent.HTML != HTML {
			t.Errorf("attempt = %+v, want the message forwarded verbatim", sent)
		}
	})

	t.Run("carries the idempotency key on every attempt", func(t *testing.T) {
		harness := newHarness(t)
		harness.Program(Outcome{Class: ClassUnavailable})
		message := Message(t)
		if _, err := harness.Sender().Send(context.Background(), message); err == nil {
			t.Fatal("Send() error = nil, want the programmed failure")
		}
		harness.Program(Outcome{Class: ClassSuccess})
		if _, err := harness.Sender().Send(context.Background(), message); err != nil {
			t.Fatalf("retry Send() error = %v, want nil", err)
		}
		attempts := harness.Attempts()
		if len(attempts) != 2 {
			t.Fatalf("attempts = %d, want 2", len(attempts))
		}
		for index, attempt := range attempts {
			if attempt.IdempotencyKey != IdempotencyKey {
				t.Errorf("attempt %d key = %q, want %q", index, attempt.IdempotencyKey, IdempotencyKey)
			}
		}
	})

	t.Run("classifies retryable and permanent failures", func(t *testing.T) {
		for _, testCase := range []struct {
			name         string
			class        OutcomeClass
			wantRetry    bool
			wantSentinel error
		}{
			{"rate limited", ClassRateLimited, true, application.ErrProviderRateLimited},
			{"unavailable", ClassUnavailable, true, application.ErrProviderUnavailable},
			{"timeout", ClassTimeout, true, application.ErrProviderTimeout},
			{"rejected", ClassRejected, false, application.ErrProviderRejected},
			{"receiptless", ClassReceiptless, true, application.ErrProviderUnavailable},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				harness := newHarness(t)
				harness.Program(Outcome{Class: testCase.class})
				_, err := harness.Sender().Send(context.Background(), Message(t))
				if err == nil {
					t.Fatalf("Send() error = nil, want %v", testCase.wantSentinel)
				}
				if !errors.Is(err, testCase.wantSentinel) {
					t.Errorf("Send() error = %v, want it to wrap %v", err, testCase.wantSentinel)
				}
				if got := application.IsRetryable(err); got != testCase.wantRetry {
					t.Errorf("IsRetryable(%v) = %v, want %v", err, got, testCase.wantRetry)
				}
				if !strings.Contains(err.Error(), "notifications:") {
					t.Errorf("Send() error = %q, want a namespaced failure", err)
				}
			})
		}
	})

	t.Run("never quotes the recipient or the code in an error", func(t *testing.T) {
		for _, class := range []OutcomeClass{ClassRateLimited, ClassUnavailable, ClassRejected, ClassTimeout, ClassReceiptless} {
			harness := newHarness(t)
			harness.Program(Outcome{Class: class})
			_, err := harness.Sender().Send(context.Background(), Message(t))
			if err == nil {
				t.Fatalf("class %d: Send() error = nil", class)
			}
			text := err.Error()
			if strings.Contains(text, Recipient) {
				t.Errorf("class %d: error quotes the recipient: %q", class, text)
			}
			if strings.Contains(text, Code) {
				t.Errorf("class %d: error quotes the code: %q", class, text)
			}
			if strings.Contains(text, IdempotencyKey) {
				t.Errorf("class %d: error quotes the idempotency key: %q", class, text)
			}
		}
	})

	t.Run("refuses an unwired message", func(t *testing.T) {
		harness := newHarness(t)
		harness.Program(Outcome{Class: ClassSuccess})
		if _, err := harness.Sender().Send(context.Background(), domain.Message{}); err == nil {
			t.Error("Send() error = nil, want the zero message refused")
		}
		if attempts := harness.Attempts(); len(attempts) != 0 {
			t.Errorf("attempts = %d, want 0: an invalid message must not reach the provider", len(attempts))
		}
	})
}
