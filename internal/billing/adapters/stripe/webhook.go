package stripe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// defaultWebhookTolerance is the maximum age of a webhook event that the
// verifier accepts. Stripe recommends 5 minutes; this matches the default
// tolerance of the official Stripe SDK's webhook.ConstructEvent.
const defaultWebhookTolerance = 5 * time.Minute

// WebhookVerifier implements application.WebhookPayloadVerifier over the
// Stripe HMAC-SHA256 webhook signature scheme. The verifier is constructed
// once at boot with the provider's webhook signing secret and is immutable
// thereafter.
//
// The verification algorithm follows the Stripe documentation:
//
//  1. Parse the Stripe-Signature header to extract t= (timestamp) and
//     v1= (signature).
//  2. Reject if the timestamp is older than the tolerance window.
//  3. Compute the expected signature: HMAC-SHA256(secret, "t payload").
//  4. Compare the computed signature with the provided signature using
//     constant-time comparison.
type WebhookVerifier struct {
	secret    string
	tolerance time.Duration
	clock     ports.Clock
}

// NewWebhookVerifier builds the verifier. The secret is the webhook signing
// secret from the Stripe dashboard (whsec_...). It is required and never
// leaves this package.
func NewWebhookVerifier(secret string, tolerance time.Duration, clock ports.Clock) (*WebhookVerifier, error) {
	if secret == "" {
		return nil, ErrMissingWebhookSecret
	}
	// The clock is required, and it is required by the rule rather than by
	// tidiness: the tolerance window is what makes a replayed webhook fail, and
	// a verifier that could not say what "now" is could not apply it. It also
	// means the window is exercisable with a fixed instant instead of only in
	// real time (P22-T02).
	if clock == nil {
		return nil, ErrMissingClock
	}
	if tolerance <= 0 {
		tolerance = defaultWebhookTolerance
	}
	return &WebhookVerifier{
		secret:    secret,
		tolerance: tolerance,
		clock:     clock,
	}, nil
}

// Verify checks that the payload was signed by the provider's secret and was
// created within the tolerance window. It implements
// application.WebhookPayloadVerifier.
func (v *WebhookVerifier) Verify(payload []byte, signatureHeader string, timestampHeader string) error {
	if signatureHeader == "" {
		return fmt.Errorf("%w: the signature header is empty", application.ErrWebhookSignatureInvalid)
	}
	if timestampHeader == "" {
		return fmt.Errorf("%w: the timestamp header is empty", application.ErrWebhookSignatureInvalid)
	}

	// Parse the timestamp.
	timestamp, err := strconv.ParseInt(timestampHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: the timestamp header is not a valid integer", application.ErrWebhookSignatureInvalid)
	}

	// Reject if the timestamp is outside the tolerance window. The age is
	// measured against the injected clock and not against the wall clock: a
	// `time.Since` here would make the window a rule that agrees with its own
	// tests only while the tests run, which is how it was until P22-T02.
	eventTime := time.Unix(timestamp, 0)
	if v.clock.Now().Sub(eventTime) > v.tolerance {
		return fmt.Errorf("%w: the webhook event is older than the tolerance window", application.ErrWebhookSignatureInvalid)
	}
	if eventTime.After(v.clock.Now().Add(time.Minute)) {
		// Reject events from the future (clock skew protection).
		return fmt.Errorf("%w: the webhook event timestamp is in the future", application.ErrWebhookSignatureInvalid)
	}

	// Parse the signature header to extract the v1 signature.
	expectedSignature, err := parseSignatureHeader(signatureHeader)
	if err != nil {
		return fmt.Errorf("%w: %v", application.ErrWebhookSignatureInvalid, err)
	}

	// Compute the expected signature: HMAC-SHA256(secret, "t payload").
	signedPayload := fmt.Sprintf("%s.%s", timestampHeader, payload)
	mac := hmac.New(sha256.New, []byte(v.secret))
	mac.Write([]byte(signedPayload))
	computedSignature := hex.EncodeToString(mac.Sum(nil))

	// Constant-time comparison to prevent timing attacks.
	if !hmac.Equal([]byte(expectedSignature), []byte(computedSignature)) {
		return fmt.Errorf("%w: the computed signature does not match", application.ErrWebhookSignatureInvalid)
	}

	return nil
}

// parseSignatureHeader extracts the v1= signature from the Stripe-Signature
// header. The header format is: t=<timestamp>,v1=<signature>[,v1=<other>]
func parseSignatureHeader(header string) (string, error) {
	parts := strings.Split(header, ",")
	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		if kv[0] == "v1" {
			return kv[1], nil
		}
	}
	return "", fmt.Errorf("no v1 signature found in the header")
}

// Construction errors of the webhook verifier.
var (
	// ErrMissingClock indicates no clock was injected. The verifier cannot
	// judge the tolerance window without one, and a verifier that silently
	// read the wall clock would be a security rule that no test can replay.
	ErrMissingClock = errors.New("stripe: a clock is required to judge the webhook tolerance")

	// ErrMissingWebhookSecret indicates no webhook signing secret was
	// configured. Without it there is nothing to verify signatures with.
	ErrMissingWebhookSecret = errors.New("stripe: a webhook signing secret is required")
)
