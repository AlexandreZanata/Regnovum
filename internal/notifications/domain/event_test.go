package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

func TestEventKeyIsStableAndEventScoped(t *testing.T) {
	first := domain.EventKey(domain.TemplateVerification, "ana@example.com", "AAAA-1111")
	if first != domain.EventKey(domain.TemplateVerification, "ana@example.com", "AAAA-1111") {
		t.Error("the same event produced two keys")
	}
	if first != domain.EventKey(domain.TemplateVerification, " ana@example.com ", "AAAA-1111") {
		t.Error("padding or case changed the key: the event is the same")
	}
	for _, other := range []string{
		domain.EventKey(domain.TemplateVerification, "ana@example.com", "BBBB-2222"),
		domain.EventKey(domain.TemplatePasswordReset, "ana@example.com", "AAAA-1111"),
		domain.EventKey(domain.TemplateVerification, "bea@example.com", "AAAA-1111"),
	} {
		if other == first {
			t.Errorf("a different event produced the same key: %q", other)
		}
	}
	if err := domain.ValidateEventKey(first); err != nil {
		t.Errorf("ValidateEventKey(%q) error = %v", first, err)
	}
	if !strings.HasPrefix(first, domain.EventKeyPrefix+domain.TemplateVerification.String()+":") {
		t.Errorf("key = %q, want the template namespace", first)
	}
	// The code is a one-time secret: only a digest of it may reach a column
	// that an operator can read.
	if strings.Contains(first, "AAAA-1111") {
		t.Errorf("key = %q, want no cleartext code", first)
	}
}

func TestValidateEventKeyRefusesValuesOutsideTheAlphabet(t *testing.T) {
	for _, testCase := range []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"missing namespace", "verification:1"},
		{"space", "email:verification:1 2"},
		{"line break", "email:verification:1\n2"},
		{"slash", "email:verification/1"},
		{"oversized", domain.EventKeyPrefix + strings.Repeat("a", 201)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := domain.ValidateEventKey(testCase.key); !errors.Is(err, domain.ErrInvalidIdempotencyKey) {
				t.Errorf("ValidateEventKey() error = %v, want ErrInvalidIdempotencyKey", err)
			}
		})
	}
}

func TestDeliveryKeyIsAnchoredOnTheJob(t *testing.T) {
	jobID := "0191f0e0-0000-7000-8000-000000000001"
	key, err := domain.DeliveryKey(jobID)
	if err != nil {
		t.Fatalf("DeliveryKey() error = %v", err)
	}
	if key != "job:"+jobID {
		t.Errorf("key = %q, want it anchored on the job", key)
	}
	again, err := domain.DeliveryKey(" " + jobID + " ")
	if err != nil {
		t.Fatalf("DeliveryKey() error = %v", err)
	}
	if again != key {
		t.Errorf("padded identifier changed the key: %q", again)
	}
	for _, testCase := range []struct {
		name  string
		jobID string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"control character", "job\n1"},
		{"outside the alphabet", "job/1"},
		{"oversized", strings.Repeat("a", 201)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := domain.DeliveryKey(testCase.jobID); !errors.Is(err, domain.ErrInvalidIdempotencyKey) {
				t.Errorf("DeliveryKey() error = %v, want ErrInvalidIdempotencyKey", err)
			}
		})
	}
}

func TestRecipientValidationIsExposedWithoutComposingAMessage(t *testing.T) {
	if err := domain.ValidateRecipient("ana@example.com"); err != nil {
		t.Errorf("ValidateRecipient() error = %v, want nil", err)
	}
	if err := domain.ValidateRecipient("nope"); !errors.Is(err, domain.ErrInvalidRecipient) {
		t.Errorf("ValidateRecipient() error = %v, want ErrInvalidRecipient", err)
	}
}
