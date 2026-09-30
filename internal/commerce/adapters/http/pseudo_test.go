//go:build pseudolocale

package http_test

// P37-T07 — the pseudo-locale carries the commerce catalog: every
// commerce key resolves bracketed in the tagged build, so layout
// expansion is proven for the staged surface like any other.
import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

func TestPseudoLocaleCarriesCommerceCatalog(t *testing.T) {
	if !i18n.PseudoEnabled {
		t.Fatal("PseudoEnabled is false in a build with the pseudolocale tag")
	}
	for _, key := range []string{
		"commerce.receipt.title",
		"commerce.receipt.gross",
		"commerce.statement.title",
		"commerce.failure.title",
	} {
		message, err := i18n.Message(i18n.PseudoLocale, key)
		if err != nil {
			t.Fatalf("Message(pseudo, %q): %v", key, err)
		}
		if !strings.HasPrefix(message, "⟦") || !strings.HasSuffix(message, "⟧") {
			t.Errorf("Message(pseudo, %q) = %q, want the pseudo markers", key, message)
		}
	}
	formatted, err := i18n.Format(i18n.PseudoLocale, "commerce.receipt.gross", map[string]string{"total": "20000"})
	if err != nil {
		t.Fatalf("Format(pseudo): %v", err)
	}
	if strings.Contains(formatted, "{total}") {
		t.Errorf("Format(pseudo) = %q, placeholders were not replaced", formatted)
	}
}
