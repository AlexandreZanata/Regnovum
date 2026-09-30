//go:build pseudolocale

package http_test

// P39-T08 — the pseudo-locale carries the disputes catalog: every
// disputes key resolves bracketed in the tagged build, so layout
// expansion is proven for the staged surface like any other.
import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

func TestPseudoLocaleCarriesDisputesCatalog(t *testing.T) {
	if !i18n.PseudoEnabled {
		t.Fatal("PseudoEnabled is false in a build with the pseudolocale tag")
	}
	for _, key := range []string{
		"disputes.case.title",
		"disputes.case.proposal",
		"disputes.notice.ruling",
		"disputes.ruling.title",
		"disputes.failure.title",
	} {
		message, err := i18n.Message(i18n.PseudoLocale, key)
		if err != nil {
			t.Fatalf("Message(pseudo, %q): %v", key, err)
		}
		if !strings.HasPrefix(message, "⟦") || !strings.HasSuffix(message, "⟧") {
			t.Errorf("Message(pseudo, %q) = %q, want the pseudo markers", key, message)
		}
	}
	formatted, err := i18n.Format(i18n.PseudoLocale, "disputes.case.proposal", map[string]string{"key": "caso-alpha", "version": "1"})
	if err != nil {
		t.Fatalf("Format(pseudo): %v", err)
	}
	if strings.Contains(formatted, "{key}") || strings.Contains(formatted, "{version}") {
		t.Errorf("Format(pseudo) = %q, placeholders were not replaced", formatted)
	}
}
