// Package resend delivers transactional email through the Resend HTTP API
// (P15-T03).
//
// The adapter is written against net/http rather than the provider SDK for
// one reason: the behaviour this task demands — a bounded timeout, an
// idempotency key on every attempt, a failure taxonomy that separates a
// retryable outage from a permanent rejection, and a response body that is
// never quoted into a log or an error — is exactly the behaviour that has to
// be visible and testable here. It is exercised against a fake server in the
// package tests, so nothing in this file depends on network access.
package resend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
)

// The provider endpoint path and the transport defaults.
const (
	// defaultBaseURL is the production API root.
	defaultBaseURL = "https://api.resend.com"
	// emailsPath is the transactional send endpoint.
	emailsPath = "/emails"
	// defaultTimeout bounds one attempt end to end.
	defaultTimeout = 10 * time.Second
	// maxTimeout caps what a caller may configure: a delivery that outlives
	// its job lease would be recorded twice.
	maxTimeout = 30 * time.Second
	// maxResponseBytes bounds how much of an answer is read. The body is
	// only mined for the receipt identifier and a stable error code.
	maxResponseBytes = 8 * 1024
	// maxAPITokenLength bounds the credential.
	maxAPITokenLength = 512
	// maxFromLength is the RFC 5322 address length bound.
	maxFromLength = 320
)

// Provider code shapes. A rejection answer may name a stable machine code;
// anything outside this shape (free-form prose, which can echo the rejected
// address) is dropped rather than propagated.
const (
	maxProviderCodeLength = 64
	providerCodeAlphabet  = "abcdefghijklmnopqrstuvwxyz0123456789_"
)

// Config is the adapter configuration. Every field is explicit: there is no
// fallback to the process environment here, the composition root owns that.
type Config struct {
	// APIToken is the bearer credential. It is never logged.
	APIToken string
	// From is the verified sender, optionally with a display name.
	From string
	// BaseURL overrides the API root. Empty selects production. Plain HTTP
	// is accepted only for a loopback host, so a test fake is the only way
	// to reach the adapter unencrypted.
	BaseURL string
	// Timeout bounds one attempt. Zero selects the default.
	Timeout time.Duration
	// Logger receives delivery outcomes. Nil discards them.
	Logger *slog.Logger
}

// Sender is the Resend implementation of the email port.
type Sender struct {
	client   *http.Client
	endpoint string
	from     string
	token    string
	logger   *slog.Logger
}

// NewSender validates the configuration and builds the adapter. The
// credential, the sender and the endpoint are checked here so a
// misconfiguration fails the composition root instead of every delivery.
func NewSender(config Config) (*Sender, error) {
	token := strings.TrimSpace(config.APIToken)
	if token == "" || len(token) > maxAPITokenLength || hasControl(token) {
		return nil, errors.New("resend: invalid api token")
	}
	from := strings.TrimSpace(config.From)
	if from == "" || len(from) > maxFromLength || hasControl(from) {
		return nil, errors.New("resend: invalid sender address")
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout < 0 || timeout > maxTimeout {
		return nil, fmt.Errorf("resend: timeout must be positive and at most %s", maxTimeout)
	}
	base := strings.TrimSpace(config.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	endpoint, err := endpointURL(base)
	if err != nil {
		return nil, err
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return &Sender{
		client:   &http.Client{Timeout: timeout},
		endpoint: endpoint,
		from:     from,
		token:    token,
		logger:   logger,
	}, nil
}

// endpointURL validates the API root and appends the send path. Plain HTTP
// is refused everywhere except loopback: sending a bearer token in the clear
// is a configuration bug, not a preference.
func endpointURL(base string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", errors.New("resend: invalid base url")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", errors.New("resend: base url must use http or https")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("resend: plain http base url must be loopback")
	}
	if parsed.Host == "" {
		return "", errors.New("resend: base url must include a host")
	}
	return strings.TrimSuffix(parsed.String(), "/") + emailsPath, nil
}

func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// sendPayload is the provider request body. It holds the composed, already
// validated representations of the message.
type sendPayload struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html"`
}

// sendResponse carries the provider receipt.
type sendResponse struct {
	ID string `json:"id"`
}

// errorResponse is mined only for the stable code; the human message is
// deliberately ignored, because a provider rejection may quote the rejected
// address back at us and that must not reach a log or an error.
type errorResponse struct {
	Name string `json:"name"`
}

// Send delivers one message. The idempotency key travels as a header on
// every attempt, so a retry of the same durable job is the same logical
// send at the provider and cannot produce a duplicate email.
func (s *Sender) Send(ctx context.Context, message domain.Message) (application.Receipt, error) {
	if s == nil || s.client == nil {
		return application.Receipt{}, domain.ErrMissingDependency
	}
	if ctx == nil {
		return application.Receipt{}, errors.New("resend: nil context")
	}
	if err := message.Validate(); err != nil {
		return application.Receipt{}, err
	}
	payload, err := json.Marshal(sendPayload{
		From:    s.from,
		To:      []string{message.Recipient()},
		Subject: message.Body().Subject,
		Text:    message.Body().Text,
		HTML:    message.Body().HTML,
	})
	if err != nil {
		return application.Receipt{}, fmt.Errorf("resend: encode payload: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return application.Receipt{}, fmt.Errorf("resend: build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set("Idempotency-Key", message.IdempotencyKey())

	response, err := s.client.Do(request)
	if err != nil {
		return application.Receipt{}, s.transportError(ctx, message, err)
	}
	defer func() { _ = response.Body.Close() }()

	// Read a bounded slice and drain: the body is never logged, echoed or
	// retained, and draining keeps the connection reusable.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return application.Receipt{}, s.logFailure(ctx, message, 0, "", fmt.Errorf("resend: read response: %w", wrapUnavailable(err)))
	}

	switch class := classify(response.StatusCode); class {
	case classSuccess:
		var decoded sendResponse
		if err := json.Unmarshal(body, &decoded); err != nil || decoded.ID == "" {
			// The provider accepted the request but we cannot confirm the
			// receipt. The outcome is unknown, not lost: the same
			// idempotency key makes a retry safe.
			return application.Receipt{}, s.logFailure(ctx, message, response.StatusCode, "", fmt.Errorf("resend: missing receipt: %w", application.ErrProviderUnavailable))
		}
		s.logger.InfoContext(ctx, "email delivered",
			slog.String("template", message.Template().String()),
			slog.String("recipient", logging.RedactEmail(message.Recipient())),
			slog.String("provider_id", decoded.ID),
		)
		return application.Receipt{ProviderID: decoded.ID}, nil
	case classRateLimited:
		return application.Receipt{}, s.logFailure(ctx, message, response.StatusCode, providerCode(body), application.ErrProviderRateLimited)
	case classRejected:
		return application.Receipt{}, s.logFailure(ctx, message, response.StatusCode, providerCode(body), application.ErrProviderRejected)
	default:
		return application.Receipt{}, s.logFailure(ctx, message, response.StatusCode, providerCode(body), application.ErrProviderUnavailable)
	}
}

// statusClass is the failure taxonomy of one answer.
type statusClass int

const (
	classSuccess statusClass = iota
	classRateLimited
	classRejected
	classUnavailable
)

func classify(status int) statusClass {
	switch {
	case status >= 200 && status < 300:
		return classSuccess
	case status == http.StatusTooManyRequests:
		return classRateLimited
	case status >= 500:
		return classUnavailable
	case status >= 400:
		return classRejected
	default:
		// 1xx/3xx never reach this point with the default client policy
		// (redirects are followed); anything else is treated as an outage.
		return classUnavailable
	}
}

// transportError separates our deadline from a provider outage. A cancelled
// context means the caller gave up — the message never reached the provider,
// so it stays retryable either way.
func (s *Sender) transportError(ctx context.Context, message domain.Message, err error) error {
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return s.logFailure(ctx, message, 0, "", errors.Join(application.ErrProviderTimeout, context.DeadlineExceeded))
	}
	return s.logFailure(ctx, message, 0, "", fmt.Errorf("resend: transport: %w", wrapUnavailable(err)))
}

// wrapUnavailable keeps the class retryable while dropping the transport
// detail that could quote the endpoint or the credential.
func wrapUnavailable(err error) error {
	if errors.Is(err, context.Canceled) {
		return errors.Join(application.ErrProviderUnavailable, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(application.ErrProviderTimeout, context.DeadlineExceeded)
	}
	return application.ErrProviderUnavailable
}

// logFailure records one failure with the safe fields only: redacted
// recipient, template, status and stable provider code. The code, the
// subject, the message body and the idempotency key never reach the log.
func (s *Sender) logFailure(ctx context.Context, message domain.Message, status int, code string, err error) error {
	s.logger.WarnContext(ctx, "email delivery failed",
		slog.String("template", message.Template().String()),
		slog.String("recipient", logging.RedactEmail(message.Recipient())),
		slog.Int("status", status),
		slog.String("provider_code", code),
		slog.Bool("retryable", application.IsRetryable(err)),
		slog.String("error", logging.RedactValue(err.Error())),
	)
	return fmt.Errorf("resend: deliver: %w", err)
}

// providerCode extracts a stable machine code from an error answer, or the
// empty string. Only the constrained alphabet and length survive: a
// free-form provider message may quote the rejected address.
func providerCode(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var decoded errorResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return ""
	}
	name := strings.TrimSpace(decoded.Name)
	if name == "" || len(name) > maxProviderCodeLength {
		return ""
	}
	for _, char := range name {
		if !strings.ContainsRune(providerCodeAlphabet, char) {
			return ""
		}
	}
	return name
}

// hasControl reports whether the value carries a control character: inside a
// header value it would let configuration inject headers of its own.
func hasControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}
