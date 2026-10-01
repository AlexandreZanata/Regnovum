//go:build pseudolocale

package http_test

// P46-T11 — the pseudo-locale carries the seasons catalog: every
// seasons key resolves bracketed in the tagged build, so layout
// expansion is proven for the staged surface like any other.
import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

func TestPseudoLocaleCarriesSeasonsCatalog(t *testing.T) {
	if !i18n.PseudoEnabled {
		t.Fatal("PseudoEnabled is false in a build with the pseudolocale tag")
	}
	for _, key := range []string{
		"seasons.current.title",
		"seasons.history.title",
		"seasons.detail.title",
		"seasons.failure.title",
	} {
		message, err := i18n.Message(i18n.PseudoLocale, key)
		if err != nil {
			t.Fatalf("Message(pseudo, %q): %v", key, err)
		}
		if !strings.HasPrefix(message, "⟦") || !strings.HasSuffix(message, "⟧") {
			t.Errorf("Message(pseudo, %q) = %q, want the pseudo markers", key, message)
		}
	}
	formatted, err := i18n.Format(i18n.PseudoLocale, "seasons.history.empty", map[string]string{})
	if err != nil {
		t.Fatalf("Format(pseudo): %v", err)
	}
	if formatted == "" {
		t.Error("Format(pseudo) is empty: the catalog must render")
	}
}
