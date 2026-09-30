package application_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/contract"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// fakeSender is the in-memory contract fake of the email port: it produces
// the programmed outcomes and records what it was asked to send, so the same
// observable behaviour can be asserted here and against the provider
// adapter (see contract.RunSenderContract).
type fakeSender struct {
	mu       sync.Mutex
	class    contract.OutcomeClass
	attempts []contract.Attempt
	accepted int
}

func newFakeSender() *fakeSender {
	return &fakeSender{class: contract.ClassSuccess}
}

func (f *fakeSender) Send(_ context.Context, message domain.Message) (application.Receipt, error) {
	// Validation of the message is part of the port contract: a message
	// that never became valid must not count as an attempt.
	if err := message.Validate(); err != nil {
		return application.Receipt{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, contract.Attempt{
		Recipient:      message.Recipient(),
		Subject:        message.Body().Subject,
		Text:           message.Body().Text,
		HTML:           message.Body().HTML,
		IdempotencyKey: message.IdempotencyKey(),
	})
	switch f.class {
	case contract.ClassSuccess:
		f.accepted++
		return application.Receipt{ProviderID: fmt.Sprintf("fake-%d", f.accepted)}, nil
	case contract.ClassReceiptless:
		return application.Receipt{}, application.ErrProviderUnavailable
	case contract.ClassRateLimited:
		return application.Receipt{}, application.ErrProviderRateLimited
	case contract.ClassUnavailable:
		return application.Receipt{}, application.ErrProviderUnavailable
	case contract.ClassRejected:
		return application.Receipt{}, application.ErrProviderRejected
	case contract.ClassTimeout:
		return application.Receipt{}, application.ErrProviderTimeout
	default:
		return application.Receipt{}, errors.New("fake: unknown outcome class")
	}
}

// fakeHarness adapts the fake to the shared contract suite.
type fakeHarness struct {
	t      *testing.T
	sender *fakeSender
}

func newFakeHarness(t *testing.T) contract.Harness {
	t.Helper()
	return &fakeHarness{t: t, sender: newFakeSender()}
}

func (h *fakeHarness) Sender() application.Sender { return h.sender }

func (h *fakeHarness) Timeout() time.Duration { return time.Second }

func (h *fakeHarness) Program(outcome contract.Outcome) {
	h.sender.mu.Lock()
	defer h.sender.mu.Unlock()
	h.sender.class = outcome.Class
}

func (h *fakeHarness) Attempts() []contract.Attempt {
	h.sender.mu.Lock()
	defer h.sender.mu.Unlock()
	return append([]contract.Attempt(nil), h.sender.attempts...)
}

// TestFakeSenderSatisfiesThePortContract runs the shared suite against the
// fake, so the behaviour the application tests rely on is the behaviour the
// provider adapter is held to.
func TestFakeSenderSatisfiesThePortContract(t *testing.T) {
	contract.RunSenderContract(t, newFakeHarness)
}

// fakeRenderer is the contract fake of the renderer port.
type fakeRenderer struct {
	body domain.Body
	err  error
}

func (f fakeRenderer) Render(domain.TemplateID, domain.Locale, domain.TemplateValues) (domain.Body, error) {
	if f.err != nil {
		return domain.Body{}, f.err
	}
	return f.body, nil
}

func validRenderer() fakeRenderer {
	return fakeRenderer{body: domain.Body{
		Subject: contract.Subject,
		Text:    contract.Text,
		HTML:    contract.HTML,
	}}
}

func deliverRequest() application.DeliverRequest {
	return application.DeliverRequest{
		Recipient:      contract.Recipient,
		Locale:         domain.LocaleBrazilianPortuguese,
		Template:       domain.TemplateVerification,
		Values:         domain.TemplateValues{Name: "Ana", Code: contract.Code},
		IdempotencyKey: contract.IdempotencyKey,
	}
}

func TestNewDelivererRequiresBothPorts(t *testing.T) {
	if _, err := application.NewDeliverer(nil, newFakeSender()); !errors.Is(err, domain.ErrMissingDependency) {
		t.Errorf("NewDeliverer(nil, sender) error = %v, want ErrMissingDependency", err)
	}
	if _, err := application.NewDeliverer(validRenderer(), nil); !errors.Is(err, domain.ErrMissingDependency) {
		t.Errorf("NewDeliverer(renderer, nil) error = %v, want ErrMissingDependency", err)
	}
}

func TestDeliverComposesAndSendsTheRenderedBody(t *testing.T) {
	sender := newFakeSender()
	deliverer, err := application.NewDeliverer(validRenderer(), sender)
	if err != nil {
		t.Fatalf("NewDeliverer() error = %v", err)
	}
	receipt, err := deliverer.Deliver(context.Background(), deliverRequest())
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if receipt.ProviderID != "fake-1" {
		t.Errorf("receipt = %+v, want the provider identifier", receipt)
	}
	attempts := sender.attempts
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Recipient != contract.Recipient || attempts[0].Subject != contract.Subject {
		t.Errorf("attempt = %+v, want the rendered message", attempts[0])
	}
	if attempts[0].IdempotencyKey != contract.IdempotencyKey {
		t.Errorf("key = %q, want %q", attempts[0].IdempotencyKey, contract.IdempotencyKey)
	}
}

func TestDeliverRefusesRequestsBeforeRendering(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		mutate  func(*application.DeliverRequest)
		wantErr error
	}{
		{"unknown template", func(r *application.DeliverRequest) { r.Template = "marketing" }, domain.ErrUnsupportedTemplate},
		{"unknown locale", func(r *application.DeliverRequest) { r.Locale = "fr-FR" }, domain.ErrUnsupportedLocale},
		{"empty template", func(r *application.DeliverRequest) { r.Template = "" }, domain.ErrUnsupportedTemplate},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := deliverRequest()
			testCase.mutate(&request)
			sender := newFakeSender()
			deliverer, err := application.NewDeliverer(validRenderer(), sender)
			if err != nil {
				t.Fatalf("NewDeliverer() error = %v", err)
			}
			if _, err := deliverer.Deliver(context.Background(), request); !errors.Is(err, testCase.wantErr) {
				t.Errorf("Deliver() error = %v, want %v", err, testCase.wantErr)
			}
			if len(sender.attempts) != 0 {
				t.Errorf("attempts = %d, want 0", len(sender.attempts))
			}
		})
	}
}

func TestDeliverRejectsAnUnsafeRenderedBody(t *testing.T) {
	// A renderer is an adapter: it may be replaced, so the composed message
	// is validated here too. A subject with a line break would let content
	// inject headers downstream.
	renderer := fakeRenderer{body: domain.Body{Subject: "hi\r\nBcc: ana@example.com", Text: "text", HTML: "<p>html</p>"}}
	sender := newFakeSender()
	deliverer, err := application.NewDeliverer(renderer, sender)
	if err != nil {
		t.Fatalf("NewDeliverer() error = %v", err)
	}
	if _, err := deliverer.Deliver(context.Background(), deliverRequest()); !errors.Is(err, domain.ErrInvalidBody) {
		t.Errorf("Deliver() error = %v, want ErrInvalidBody", err)
	}
	if len(sender.attempts) != 0 {
		t.Error("an invalid body reached the sender")
	}
}

func TestDeliverPropagatesRendererAndSenderFailures(t *testing.T) {
	rendererFailure := errors.New("renderer: catalog missing")
	deliverer, err := application.NewDeliverer(fakeRenderer{err: rendererFailure}, newFakeSender())
	if err != nil {
		t.Fatalf("NewDeliverer() error = %v", err)
	}
	if _, err := deliverer.Deliver(context.Background(), deliverRequest()); !errors.Is(err, rendererFailure) {
		t.Errorf("Deliver() error = %v, want the renderer failure", err)
	}

	sender := newFakeSender()
	sender.class = contract.ClassRejected
	deliverer, err = application.NewDeliverer(validRenderer(), sender)
	if err != nil {
		t.Fatalf("NewDeliverer() error = %v", err)
	}
	_, err = deliverer.Deliver(context.Background(), deliverRequest())
	if !errors.Is(err, application.ErrProviderRejected) {
		t.Fatalf("Deliver() error = %v, want ErrProviderRejected", err)
	}
	if application.IsRetryable(err) {
		t.Error("a provider rejection was classified as retryable")
	}
	for _, secret := range []string{contract.Recipient, contract.Code, contract.IdempotencyKey} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("delivery error quotes %q: %v", secret, err)
		}
	}
}

func TestDeliverRejectsANilContext(t *testing.T) {
	deliverer, err := application.NewDeliverer(validRenderer(), newFakeSender())
	if err != nil {
		t.Fatalf("NewDeliverer() error = %v", err)
	}
	//lint:ignore SA1012 the explicit nil context is the failure under test
	if _, err := deliverer.Deliver(nil, deliverRequest()); err == nil {
		t.Error("Deliver(nil) error = nil, want a refusal")
	}
}

// TestDelivererIsUsableThroughThePorts keeps the wiring honest: the use case
// is consumed as a concrete type here, and the ports it needs are the two
// interfaces above.
func TestDelivererIsUsableThroughThePorts(t *testing.T) {
	var _ application.Renderer = validRenderer()
	var _ application.Sender = newFakeSender()
}
