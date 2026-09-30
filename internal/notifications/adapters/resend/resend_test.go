package resend_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/resend"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/contract"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// The fixture credential. It is distinctive so a test can assert it never
// reaches a log line.
const apiToken = "re_test_token_8f14e45f"

// harnessTimeout is the deadline every harness configures, small enough to
// keep the timeout case fast and large enough for a local fake server.
const harnessTimeout = 100 * time.Millisecond

// payload mirrors the provider request body.
type payload struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html"`
}

// requestRecord is one request the fake provider received.
type requestRecord struct {
	header  http.Header
	payload payload
	raw     string
}

// harness serves a fake provider and adapts the adapter to the contract.
type harness struct {
	t       *testing.T
	server  *httptest.Server
	sender  *resend.Sender
	logs    *syncBuffer
	timeout time.Duration

	mu       sync.Mutex
	class    contract.OutcomeClass
	status   int
	answer   string
	requests []requestRecord
}

// syncBuffer collects log output for assertions under concurrent writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	built := &harness{t: t, logs: &syncBuffer{}, timeout: harnessTimeout, class: contract.ClassSuccess}
	built.server = httptest.NewServer(http.HandlerFunc(built.handle))
	t.Cleanup(built.server.Close)
	sender, err := resend.NewSender(resend.Config{
		APIToken: apiToken,
		From:     "Regnovum <no-reply@arena.example>",
		BaseURL:  built.server.URL,
		Timeout:  built.timeout,
		Logger:   slog.New(slog.NewJSONHandler(built.logs, nil)),
	})
	if err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}
	built.sender = sender
	return built
}

func (h *harness) Sender() application.Sender { return h.sender }

func (h *harness) Timeout() time.Duration { return h.timeout }

func (h *harness) Program(outcome contract.Outcome) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.class = outcome.Class
	h.status = 0
	h.answer = ""
}

// programStatus answers with a raw status and body, for the cases the
// semantic classes do not cover.
func (h *harness) programStatus(status int, answer string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status = status
	h.answer = answer
}

func (h *harness) Attempts() []contract.Attempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	attempts := make([]contract.Attempt, 0, len(h.requests))
	for _, record := range h.requests {
		recipient := ""
		if len(record.payload.To) > 0 {
			recipient = record.payload.To[0]
		}
		attempts = append(attempts, contract.Attempt{
			Recipient:      recipient,
			Subject:        record.payload.Subject,
			Text:           record.payload.Text,
			HTML:           record.payload.HTML,
			IdempotencyKey: record.header.Get("Idempotency-Key"),
		})
	}
	return attempts
}

func (h *harness) requestsSnapshot() []requestRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]requestRecord(nil), h.requests...)
}

func (h *harness) handle(writer http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	var decoded payload
	_ = json.Unmarshal(body, &decoded)
	h.mu.Lock()
	h.requests = append(h.requests, requestRecord{header: request.Header.Clone(), payload: decoded, raw: string(body)})
	class, status, answer := h.class, h.status, h.answer
	h.mu.Unlock()

	writer.Header().Set("Content-Type", "application/json")
	switch {
	case status != 0:
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(answer))
	case class == contract.ClassTimeout:
		// Answer well after the configured deadline, so the client gives up.
		time.Sleep(4 * h.timeout)
		_, _ = writer.Write([]byte(`{"id":"late"}`))
	case class == contract.ClassSuccess:
		_, _ = writer.Write([]byte(`{"id":"msg_2f9a1c"}`))
	case class == contract.ClassReceiptless:
		_, _ = writer.Write([]byte(`{}`))
	case class == contract.ClassRateLimited:
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"name":"rate_limited","message":"slow down"}`))
	case class == contract.ClassUnavailable:
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(`{"name":"internal_error","message":"upstream"}`))
	case class == contract.ClassRejected:
		writer.WriteHeader(http.StatusUnprocessableEntity)
		// The human message quotes the address on purpose: the adapter must
		// not propagate it.
		_, _ = writer.Write([]byte(`{"name":"validation_error","message":"invalid to: ` + contract.Recipient + `"}`))
	default:
		writer.WriteHeader(http.StatusInternalServerError)
	}
}

// TestResendSatisfiesThePortContract runs the shared suite against the real
// adapter, behind the fake provider.
func TestResendSatisfiesThePortContract(t *testing.T) {
	contract.RunSenderContract(t, func(t *testing.T) contract.Harness { return newHarness(t) })
}

func TestSendCarriesTheContractHeadersAndPayload(t *testing.T) {
	built := newHarness(t)
	built.Program(contract.Outcome{Class: contract.ClassSuccess})
	if _, err := built.sender.Send(context.Background(), contract.Message(t)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	requests := built.requestsSnapshot()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	record := requests[0]
	if got := record.header.Get("Authorization"); got != "Bearer "+apiToken {
		t.Errorf("Authorization = %q, want the bearer credential", got)
	}
	if got := record.header.Get("Idempotency-Key"); got != contract.IdempotencyKey {
		t.Errorf("Idempotency-Key = %q, want %q", got, contract.IdempotencyKey)
	}
	if got := record.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if record.payload.From != "Regnovum <no-reply@arena.example>" {
		t.Errorf("From = %q", record.payload.From)
	}
	if len(record.payload.To) != 1 || record.payload.To[0] != contract.Recipient {
		t.Errorf("To = %v, want exactly the one recipient", record.payload.To)
	}
	if record.payload.Subject != contract.Subject || record.payload.Text != contract.Text || record.payload.HTML != contract.HTML {
		t.Errorf("payload = %+v, want the message forwarded verbatim", record.payload)
	}
	if strings.Contains(record.raw, apiToken) {
		t.Error("the credential was serialized into the request body")
	}
}

// TestProviderStatusesMapToTheFailureTaxonomy pins the mapping the durable
// job depends on: only an outage is retried, a rejection is not.
func TestProviderStatusesMapToTheFailureTaxonomy(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		status      int
		answer      string
		wantErr     error
		wantRetry   bool
		wantCodeLog string
	}{
		{"accepted", http.StatusOK, `{"id":"msg_1"}`, nil, false, ""},
		{"rate limited", http.StatusTooManyRequests, `{"name":"rate_limited"}`, application.ErrProviderRateLimited, true, "rate_limited"},
		{"server error", http.StatusInternalServerError, `{"name":"internal_error"}`, application.ErrProviderUnavailable, true, "internal_error"},
		{"bad gateway", http.StatusBadGateway, `{"name":"unknown_error"}`, application.ErrProviderUnavailable, true, "unknown_error"},
		{"service unavailable", http.StatusServiceUnavailable, `{}`, application.ErrProviderUnavailable, true, ""},
		{"bad request", http.StatusBadRequest, `{"name":"invalid_request"}`, application.ErrProviderRejected, false, "invalid_request"},
		{"unprocessable", http.StatusUnprocessableEntity, `{"name":"validation_error"}`, application.ErrProviderRejected, false, "validation_error"},
		{"malformed success body", http.StatusOK, `not json`, application.ErrProviderUnavailable, true, ""},
		{"success without a receipt", http.StatusOK, `{"id":""}`, application.ErrProviderUnavailable, true, ""},
		{"rejection with prose body", http.StatusUnprocessableEntity, `ana.silva@example.com`, application.ErrProviderRejected, false, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			built := newHarness(t)
			built.programStatus(testCase.status, testCase.answer)
			_, err := built.sender.Send(context.Background(), contract.Message(t))
			if testCase.wantErr == nil {
				if err != nil {
					t.Fatalf("Send() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("Send() error = %v, want %v", err, testCase.wantErr)
			}
			if got := application.IsRetryable(err); got != testCase.wantRetry {
				t.Errorf("IsRetryable() = %v, want %v", got, testCase.wantRetry)
			}
			if testCase.wantCodeLog != "" && !strings.Contains(built.logs.String(), `"provider_code":"`+testCase.wantCodeLog+`"`) {
				t.Errorf("log = %s, want provider_code %q", built.logs.String(), testCase.wantCodeLog)
			}
			if testCase.wantCodeLog == "" && testCase.wantErr != nil && !strings.Contains(built.logs.String(), `"provider_code":""`) {
				t.Errorf("log = %s, want an empty provider code", built.logs.String())
			}
		})
	}
}

func TestSendTimesOutAndStaysRetryable(t *testing.T) {
	built := newHarness(t)
	built.Program(contract.Outcome{Class: contract.ClassTimeout})
	_, err := built.sender.Send(context.Background(), contract.Message(t))
	if !errors.Is(err, application.ErrProviderTimeout) {
		t.Fatalf("Send() error = %v, want ErrProviderTimeout", err)
	}
	if !application.IsRetryable(err) {
		t.Error("a timeout was classified as permanent")
	}
}

func TestSendHonoursContextCancellation(t *testing.T) {
	built := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := built.sender.Send(ctx, contract.Message(t))
	if err == nil {
		t.Fatal("Send() error = nil, want a refusal")
	}
	if !application.IsRetryable(err) {
		t.Errorf("Send() error = %v, want a retryable failure: the message never left", err)
	}
}

// TestLogsNeverCarryTheRecipientOrTheSecrets is the redaction gate: the
// code, the idempotency key and the credential stay out of the log stream,
// and the recipient appears masked at most.
func TestLogsNeverCarryTheRecipientOrTheSecrets(t *testing.T) {
	for _, class := range []contract.OutcomeClass{contract.ClassSuccess, contract.ClassRejected, contract.ClassUnavailable, contract.ClassRateLimited} {
		built := newHarness(t)
		built.Program(contract.Outcome{Class: class})
		_, err := built.sender.Send(context.Background(), contract.Message(t))
		if class == contract.ClassSuccess && err != nil {
			t.Fatalf("class %d: Send() error = %v, want nil", class, err)
		}
		if class != contract.ClassSuccess && err == nil {
			t.Fatalf("class %d: Send() error = nil, want a failure", class)
		}
		logs := built.logs.String()
		if logs == "" {
			t.Fatalf("class %d: no log record was written", class)
		}
		for _, secret := range []string{
			contract.Recipient,
			contract.Code,
			contract.IdempotencyKey,
			apiToken,
			contract.HTML,
			contract.Text,
		} {
			if strings.Contains(logs, secret) {
				t.Errorf("class %d: log quotes %q: %s", class, secret, logs)
			}
		}
		if class == contract.ClassSuccess {
			if !strings.Contains(logs, "a***@example.com") {
				t.Errorf("class %d: log does not carry the masked recipient: %s", class, logs)
			}
			if !strings.Contains(logs, "msg_2f9a1c") {
				t.Errorf("class %d: log does not carry the provider id: %s", class, logs)
			}
			continue
		}
		if !strings.Contains(logs, "a***@example.com") {
			t.Errorf("class %d: failure log does not carry the masked recipient: %s", class, logs)
		}
		if !strings.Contains(logs, `"retryable":`) {
			t.Errorf("class %d: failure log does not classify the failure: %s", class, logs)
		}
	}
}

func TestSendRefusesAnUnwiredMessageWithoutCallingTheProvider(t *testing.T) {
	built := newHarness(t)
	if _, err := built.sender.Send(context.Background(), domain.Message{}); err == nil {
		t.Error("Send() error = nil, want the zero message refused")
	}
	if requests := built.requestsSnapshot(); len(requests) != 0 {
		t.Errorf("requests = %d, want 0", len(requests))
	}
}

func TestNilSenderAndNilContextAreRefused(t *testing.T) {
	var sender *resend.Sender
	if _, err := sender.Send(context.Background(), contract.Message(t)); !errors.Is(err, domain.ErrMissingDependency) {
		t.Errorf("nil sender error = %v, want ErrMissingDependency", err)
	}
	built := newHarness(t)
	//lint:ignore SA1012 the explicit nil context is the failure under test
	if _, err := built.sender.Send(nil, contract.Message(t)); err == nil {
		t.Error("Send(nil) error = nil, want a refusal")
	}
}

func TestNewSenderValidatesItsConfiguration(t *testing.T) {
	valid := resend.Config{
		APIToken: apiToken,
		From:     "no-reply@arena.example",
		BaseURL:  "https://api.resend.com",
		Timeout:  time.Second,
	}
	if _, err := resend.NewSender(valid); err != nil {
		t.Fatalf("NewSender(valid) error = %v", err)
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*resend.Config)
	}{
		{"empty token", func(c *resend.Config) { c.APIToken = "" }},
		{"token with a line break", func(c *resend.Config) { c.APIToken = "re_x\r\nInjected: 1" }},
		{"oversized token", func(c *resend.Config) { c.APIToken = strings.Repeat("a", 513) }},
		{"empty sender", func(c *resend.Config) { c.From = "  " }},
		{"sender with a line break", func(c *resend.Config) { c.From = "a@b.co\nBcc: c@d.co" }},
		{"oversized sender", func(c *resend.Config) { c.From = strings.Repeat("a", 321) }},
		{"negative timeout", func(c *resend.Config) { c.Timeout = -time.Second }},
		{"timeout above the cap", func(c *resend.Config) { c.Timeout = 31 * time.Second }},
		{"plain http to a public host", func(c *resend.Config) { c.BaseURL = "http://api.resend.com" }},
		{"unsupported scheme", func(c *resend.Config) { c.BaseURL = "ftp://api.resend.com" }},
		{"url without a scheme", func(c *resend.Config) { c.BaseURL = "api.resend.com" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			config := valid
			testCase.mutate(&config)
			if _, err := resend.NewSender(config); err == nil {
				t.Error("NewSender() error = nil, want a refusal")
			}
		})
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*resend.Config)
	}{
		{"loopback http", func(c *resend.Config) { c.BaseURL = "http://127.0.0.1:8080" }},
		{"empty base url selects production", func(c *resend.Config) { c.BaseURL = "" }},
		{"zero timeout selects the default", func(c *resend.Config) { c.Timeout = 0 }},
		{"trailing slash", func(c *resend.Config) { c.BaseURL = "https://api.resend.com/" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			config := valid
			testCase.mutate(&config)
			if _, err := resend.NewSender(config); err != nil {
				t.Errorf("NewSender() error = %v, want nil", err)
			}
		})
	}
}

// TestProviderCodeIsBoundedAndSanitized keeps a free-form provider answer
// from becoming an unredacted log field.
func TestProviderCodeIsBoundedAndSanitized(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		answer   string
		wantCode string
	}{
		{"stable code", `{"name":"validation_error"}`, "validation_error"},
		{"code with a space", `{"name":"invalid to: ana@example.com"}`, ""},
		{"code with an uppercase letter", `{"name":"ValidationError"}`, ""},
		{"oversized code", `{"name":"` + strings.Repeat("a", 65) + `"}`, ""},
		{"no name", `{"message":"boom"}`, ""},
		{"not an object", `[1,2,3]`, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			built := newHarness(t)
			built.programStatus(http.StatusUnprocessableEntity, testCase.answer)
			if _, err := built.sender.Send(context.Background(), contract.Message(t)); !errors.Is(err, application.ErrProviderRejected) {
				t.Fatalf("Send() error = %v, want ErrProviderRejected", err)
			}
			logs := built.logs.String()
			if testCase.wantCode != "" && !strings.Contains(logs, `"provider_code":"`+testCase.wantCode+`"`) {
				t.Errorf("log = %s, want provider_code %q", logs, testCase.wantCode)
			}
			if testCase.wantCode == "" && !strings.Contains(logs, `"provider_code":""`) {
				t.Errorf("log = %s, want an empty provider_code", logs)
			}
			if strings.Contains(logs, "ana@example.com") {
				t.Errorf("log quotes the provider answer: %s", logs)
			}
		})
	}
}
