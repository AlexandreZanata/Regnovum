package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func TestParseIntentionTriple(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"sale-1", "ophelia", "checkout", "a"} {
		if _, err := domain.ParseIntentionKey(raw); err != nil {
			t.Errorf("ParseIntentionKey(%q): %v", raw, err)
		}
		if _, err := domain.ParseIntentionActor(raw); err != nil {
			t.Errorf("ParseIntentionActor(%q): %v", raw, err)
		}
		if _, err := domain.ParseIntentionOperation(raw); err != nil {
			t.Errorf("ParseIntentionOperation(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", "   ", strings.Repeat("k", 129), "key\x00", "op\teration"} {
		if _, err := domain.ParseIntentionKey(raw); !errors.Is(err, domain.ErrInvalidIntention) {
			t.Errorf("ParseIntentionKey(%q) = %v, want ErrInvalidIntention", raw, err)
		}
		if _, err := domain.ParseIntentionActor(raw); !errors.Is(err, domain.ErrInvalidIntention) {
			t.Errorf("ParseIntentionActor(%q) = %v, want ErrInvalidIntention", raw, err)
		}
		if _, err := domain.ParseIntentionOperation(raw); !errors.Is(err, domain.ErrInvalidIntention) {
			t.Errorf("ParseIntentionOperation(%q) = %v, want ErrInvalidIntention", raw, err)
		}
	}
}
