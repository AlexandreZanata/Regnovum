package stripe_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	stripeadapter "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
)

// testClock is a deterministic clock for tests.
type testClock struct {
	now time.Time
}

func (c *testClock) Now() time.Time { return c.now }

func TestWebhookVerifierRejectsMissingSecret(t *testing.T) {
	t.Parallel()
	_, err := stripeadapter.NewWebhookVerifier("", 0, &testClock{now: time.Now()})
	if !errors.Is(err, stripeadapter.ErrMissingWebhookSecret) {
		t.Fatalf("error = %v, want ErrMissingWebhookSecret", err)
	}
}

func TestWebhookVerifierAcceptsValidSignature(t *testing.T) {
	t.Parallel()

	secret := "whsec_test_secret_key"
	verifier, err := stripeadapter.NewWebhookVerifier(secret, 5*time.Minute, &testClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewWebhookVerifier error = %v", err)
	}

	body := []byte(`{"id":"evt_test","type":"checkout.session.completed"}`)
	timestamp := time.Now().Unix()
	signedPayload := fmt.Sprintf("%d.%s", timestamp, body)

	// Compute the correct signature.
	mac := newHMAC(signedPayload, secret)
	signature := fmt.Sprintf("t=%d,v1=%s", timestamp, mac)

	if err := verifier.Verify(body, signature, strconv.FormatInt(timestamp, 10)); err != nil {
		t.Fatalf("Verify error = %v", err)
	}
}

func TestWebhookVerifierRejectsInvalidSignature(t *testing.T) {
	t.Parallel()

	secret := "whsec_test_secret_key"
	verifier, err := stripeadapter.NewWebhookVerifier(secret, 5*time.Minute, &testClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewWebhookVerifier error = %v", err)
	}

	body := []byte(`{"id":"evt_test","type":"checkout.session.completed"}`)
	timestamp := time.Now().Unix()
	wrongSignature := fmt.Sprintf("t=%d,v1=wrong_signature_value", timestamp)

	if err := verifier.Verify(body, wrongSignature, strconv.FormatInt(timestamp, 10)); err == nil {
		t.Fatal("a wrong signature must be rejected")
	}
}

func TestWebhookVerifierRejectsAlteredBody(t *testing.T) {
	t.Parallel()

	secret := "whsec_test_secret_key"
	verifier, err := stripeadapter.NewWebhookVerifier(secret, 5*time.Minute, &testClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewWebhookVerifier error = %v", err)
	}

	originalBody := []byte(`{"id":"evt_test","type":"checkout.session.completed"}`)
	alteredBody := []byte(`{"id":"evt_test","type":"checkout.session.completed","extra":"data"}`)
	timestamp := time.Now().Unix()

	// Sign the original body.
	signedPayload := fmt.Sprintf("%d.%s", timestamp, originalBody)
	mac := newHMAC(signedPayload, secret)
	signature := fmt.Sprintf("t=%d,v1=%s", timestamp, mac)

	// Verify with the altered body: must fail.
	if err := verifier.Verify(alteredBody, signature, strconv.FormatInt(timestamp, 10)); err == nil {
		t.Fatal("an altered body must be rejected")
	}
}

func TestWebhookVerifierRejectsExpiredTimestamp(t *testing.T) {
	t.Parallel()

	secret := "whsec_test_secret_key"
	verifier, err := stripeadapter.NewWebhookVerifier(secret, 5*time.Minute, &testClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewWebhookVerifier error = %v", err)
	}

	body := []byte(`{"id":"evt_test"}`)
	// Timestamp from 10 minutes ago: outside the 5-minute tolerance.
	oldTimestamp := time.Now().Add(-10 * time.Minute).Unix()
	signedPayload := fmt.Sprintf("%d.%s", oldTimestamp, body)
	mac := newHMAC(signedPayload, secret)
	signature := fmt.Sprintf("t=%d,v1=%s", oldTimestamp, mac)

	if err := verifier.Verify(body, signature, strconv.FormatInt(oldTimestamp, 10)); err == nil {
		t.Fatal("an expired timestamp must be rejected")
	}
}

func TestWebhookVerifierRejectsFutureTimestamp(t *testing.T) {
	t.Parallel()

	secret := "whsec_test_secret_key"
	verifier, err := stripeadapter.NewWebhookVerifier(secret, 5*time.Minute, &testClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewWebhookVerifier error = %v", err)
	}

	body := []byte(`{"id":"evt_test"}`)
	// Timestamp 2 minutes in the future.
	futureTimestamp := time.Now().Add(2 * time.Minute).Unix()
	signedPayload := fmt.Sprintf("%d.%s", futureTimestamp, body)
	mac := newHMAC(signedPayload, secret)
	signature := fmt.Sprintf("t=%d,v1=%s", futureTimestamp, mac)

	if err := verifier.Verify(body, signature, strconv.FormatInt(futureTimestamp, 10)); err == nil {
		t.Fatal("a future timestamp must be rejected")
	}
}

func TestWebhookVerifierRejectsEmptySignatureHeader(t *testing.T) {
	t.Parallel()

	secret := "whsec_test_secret_key"
	verifier, err := stripeadapter.NewWebhookVerifier(secret, 5*time.Minute, &testClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewWebhookVerifier error = %v", err)
	}

	body := []byte(`{"id":"evt_test"}`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	if err := verifier.Verify(body, "", timestamp); err == nil {
		t.Fatal("an empty signature header must be rejected")
	}
}

func TestWebhookVerifierRejectsEmptyTimestampHeader(t *testing.T) {
	t.Parallel()

	secret := "whsec_test_secret_key"
	verifier, err := stripeadapter.NewWebhookVerifier(secret, 5*time.Minute, &testClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewWebhookVerifier error = %v", err)
	}

	body := []byte(`{"id":"evt_test"}`)
	signature := fmt.Sprintf("t=%d,v1=abc", time.Now().Unix())

	if err := verifier.Verify(body, signature, ""); err == nil {
		t.Fatal("an empty timestamp header must be rejected")
	}
}

func TestWebhookVerifierRejectsNonIntegerTimestamp(t *testing.T) {
	t.Parallel()

	secret := "whsec_test_secret_key"
	verifier, err := stripeadapter.NewWebhookVerifier(secret, 5*time.Minute, &testClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewWebhookVerifier error = %v", err)
	}

	body := []byte(`{"id":"evt_test"}`)
	signature := fmt.Sprintf("t=%d,v1=abc", time.Now().Unix())

	if err := verifier.Verify(body, signature, "not_a_number"); err == nil {
		t.Fatal("a non-integer timestamp must be rejected")
	}
}

func TestWebhookVerifierRejectsMissingV1Signature(t *testing.T) {
	t.Parallel()

	secret := "whsec_test_secret_key"
	verifier, err := stripeadapter.NewWebhookVerifier(secret, 5*time.Minute, &testClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewWebhookVerifier error = %v", err)
	}

	body := []byte(`{"id":"evt_test"}`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	// Header with no v1= signature.
	signatureHeader := fmt.Sprintf("t=%s,v0=old_version", timestamp)

	if err := verifier.Verify(body, signatureHeader, timestamp); err == nil {
		t.Fatal("a header without v1 signature must be rejected")
	}
}

func TestWebhookVerifierUsesConstantTimeComparison(t *testing.T) {
	// This test verifies that the verifier uses hmac.Equal (constant-time
	// comparison) instead of == (which leaks timing information). The test
	// constructs a signature that is very close to the correct one (only
	// the last character differs) and verifies it is still rejected.
	t.Parallel()

	secret := "whsec_test_secret_key"
	verifier, err := stripeadapter.NewWebhookVerifier(secret, 5*time.Minute, &testClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewWebhookVerifier error = %v", err)
	}

	body := []byte(`{"id":"evt_test"}`)
	timestamp := time.Now().Unix()
	signedPayload := fmt.Sprintf("%d.%s", timestamp, body)
	mac := newHMAC(signedPayload, secret)

	// Flip the last character of the signature.
	if len(mac) > 0 {
		lastChar := mac[len(mac)-1]
		if lastChar == 'a' {
			mac = mac[:len(mac)-1] + "b"
		} else {
			mac = mac[:len(mac)-1] + "a"
		}
	}
	signature := fmt.Sprintf("t=%d,v1=%s", timestamp, mac)

	// Must be rejected despite being very close.
	if err := verifier.Verify(body, signature, strconv.FormatInt(timestamp, 10)); err == nil {
		t.Fatal("a nearly-correct signature must be rejected")
	}
}

// TestWebhookVerifierRefusesAMissingClock states the requirement the verifier
// gained with P22-T02: the tolerance window is a security rule, and a verifier
// that cannot say what "now" is cannot apply it. Refusing at construction is
// fail-closed — the alternative is a rule that silently reads the machine clock
// on the first webhook of a release.
func TestWebhookVerifierRefusesAMissingClock(t *testing.T) {
	t.Parallel()

	_, err := stripeadapter.NewWebhookVerifier("whsec_test_secret_key", 5*time.Minute, nil)
	if !errors.Is(err, stripeadapter.ErrMissingClock) {
		t.Fatalf("error = %v, want ErrMissingClock", err)
	}
}

// TestTheToleranceIsDecidedByTheInjectedClock is the expiry scenario of the
// webhook window, and it is the test that could not exist before: with a fully
// fixed instant on both sides of the comparison, the two edges of the window
// are decidable without the wall clock, so a failure of the rule is a failure
// somebody can replay.
func TestTheToleranceIsDecidedByTheInjectedClock(t *testing.T) {
	t.Parallel()

	const secret = "whsec_test_secret_key"
	// A fixed instant: every timestamp below is an offset from it, and no part
	// of this test reads the machine clock.
	instant := time.Unix(1_700_000_000, 0)
	verifier, err := stripeadapter.NewWebhookVerifier(secret, 5*time.Minute, &testClock{now: instant})
	if err != nil {
		t.Fatalf("NewWebhookVerifier error = %v", err)
	}
	body := []byte(`{"id":"evt_test"}`)

	cases := []struct {
		name     string
		offset   time.Duration
		accepted bool
		because  string
	}{
		{name: "an event four minutes old", offset: -4 * time.Minute, accepted: true, because: "it is inside the five minute window"},
		{name: "an event exactly at the edge", offset: -5 * time.Minute, accepted: true, because: "the window is inclusive"},
		{name: "an event one second past the window", offset: -5*time.Minute - time.Second, accepted: false, because: "the replay window is what the tolerance is for"},
		{name: "an event thirty seconds ahead", offset: 30 * time.Second, accepted: true, because: "the skew allowance is one minute"},
		{name: "an event ten minutes ahead", offset: 10 * time.Minute, accepted: false, because: "a timestamp from the future is not a skew, it is a mistake or an attack"},
	}
	for _, testCase := range cases {
		timestamp := instant.Add(testCase.offset).Unix()
		signedPayload := fmt.Sprintf("%d.%s", timestamp, body)
		signature := fmt.Sprintf("t=%d,v1=%s", timestamp, newHMAC(signedPayload, secret))

		err := verifier.Verify(body, signature, strconv.FormatInt(timestamp, 10))
		switch {
		case testCase.accepted && err != nil:
			t.Errorf("%s was refused (%v), and it is accepted %s", testCase.name, err, testCase.because)
		case !testCase.accepted && err == nil:
			t.Errorf("%s was accepted, and it must not be: %s", testCase.name, testCase.because)
		}
	}
}

// newHMAC computes the HMAC-SHA256 of a message with a secret.
func newHMAC(message, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
