//go:build pseudolocale

package http_test

// P36-T08 — the pseudo-locale carries the metering catalog: every
// metering key resolves bracketed in the tagged build, so layout
// expansion is proven for the staged surface like any other.
import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

func TestPseudoLocaleCarriesMeteringCatalog(t *testing.T) {
	if !i18n.PseudoEnabled {
		t.Fatal("PseudoEnabled is false in a build with the pseudolocale tag")
	}
	for _, key := range []string{
		"metering.quote.title",
		"metering.quote.total",
		"metering.receipt.title",
		"metering.statement.balance",
		"metering.failure.title",
	} {
		message, err := i18n.Message(i18n.PseudoLocale, key)
		if err != nil {
			t.Fatalf("Message(pseudo, %q): %v", key, err)
		}
		if !strings.HasPrefix(message, "⟦") || !strings.HasSuffix(message, "⟧") {
			t.Errorf("Message(pseudo, %q) = %q, want the pseudo markers", key, message)
		}
	}
	formatted, err := i18n.Format(i18n.PseudoLocale, "metering.receipt.total", map[string]string{"total": "2750", "units": "11"})
	if err != nil {
		t.Fatalf("Format(pseudo): %v", err)
	}
	if strings.Contains(formatted, "{total}") || strings.Contains(formatted, "{units}") {
		t.Errorf("Format(pseudo) = %q, placeholders were not replaced", formatted)
	}
}
