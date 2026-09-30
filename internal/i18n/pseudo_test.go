// Tests of the pseudo-locale derivation (P18-T10). They run in every build:
// the transform is a pure function of one catalog, and what it must guarantee
// (placeholders intact, the message longer, the result reproducible) does not
// depend on whether the derivation is registered.
package i18n

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// placeholderPattern finds the named placeholders of a catalog message, in
// order, so a comparison of two messages is a comparison of what the format
// step will look for.
var placeholderPattern = regexp.MustCompile(`\{[a-z_]+\}`)

func placeholdersOf(t *testing.T, message string) []string {
	t.Helper()

	return placeholderPattern.FindAllString(message, -1)
}

func TestPseudoMessagePreservesPlaceholders(t *testing.T) {
	t.Parallel()

	source := "{subject} — Regnovum ({count} replies, up to {max})"
	derived := PseudoMessage(source)

	if got, want := placeholdersOf(t, derived), placeholdersOf(t, source); !equalStrings(got, want) {
		t.Fatalf("placeholders of the pseudo message = %v, want %v", got, want)
	}
	if !strings.Contains(derived, "{count}") || !strings.Contains(derived, "{max}") {
		t.Errorf("pseudo message %q renamed a placeholder", derived)
	}
}

// TestPseudoDerivationCoversEveryMessage is the gate that a new translation is
// covered in the same commit that adds it: the derivation is applied to the
// whole default catalog, never to a hand-picked list.
func TestPseudoDerivationCoversEveryMessage(t *testing.T) {
	t.Parallel()

	source := catalog[DefaultLocale]
	if len(source) == 0 {
		t.Fatal("the default locale has no catalog to derive from")
	}

	derived := DerivePseudo(source)
	if len(derived) != len(source) {
		t.Fatalf("derived %d messages for %d source messages", len(derived), len(source))
	}

	for key, message := range source {
		pseudo, ok := derived[key]
		if !ok {
			t.Errorf("key %q is missing from the pseudo catalog", key)
			continue
		}
		if !strings.HasPrefix(pseudo, pseudoOpen) || !strings.HasSuffix(pseudo, pseudoClose) {
			t.Errorf("key %q = %q, want the pseudo markers around it", key, pseudo)
		}
		growth := float64(len(pseudo)-len(message)) / float64(len(message))
		if growth < pseudoGrowth {
			t.Errorf("key %q grew by %.2f, want at least %.2f", key, growth, pseudoGrowth)
		}
		if got, want := placeholdersOf(t, pseudo), placeholdersOf(t, message); !equalStrings(got, want) {
			t.Errorf("key %q placeholders = %v, want %v", key, got, want)
		}
		if !strings.Contains(pseudo, "•") {
			t.Errorf("key %q = %q, want the padding marker", key, pseudo)
		}
	}
}

func TestPseudoDerivationIsDeterministicAndLeavesTheSourceAlone(t *testing.T) {
	t.Parallel()

	source := map[string]string{
		"arenas.document.page_title": "{subject} — Regnovum",
		"auth.brand":                 "Regnovum",
	}
	before := map[string]string{}
	for key, message := range source {
		before[key] = message
	}

	first := DerivePseudo(source)
	second := DerivePseudo(source)

	if len(first) != len(second) {
		t.Fatalf("two derivations disagree on size: %d and %d", len(first), len(second))
	}
	for key := range first {
		if first[key] != second[key] {
			t.Errorf("key %q is not reproducible: %q then %q", key, first[key], second[key])
		}
	}
	for key, message := range source {
		if message != before[key] {
			t.Errorf("the source message %q was modified by the derivation", key)
		}
	}
}

// TestPseudoMessageAccentsLettersAndKeepsTheRest prints one message so a
// failure shows what the gate would render, and asserts the two properties a
// reader of the pseudo text relies on: letters are accented and the markup the
// template escapes stays byte-identical.
func TestPseudoMessageAccentsLettersAndKeepsTheRest(t *testing.T) {
	t.Parallel()

	derived := PseudoMessage("Senha (mínimo 8) {min}")
	t.Logf("pseudo message: %s", derived)

	if strings.Contains(derived, "Senha") {
		t.Errorf("pseudo message %q kept an unaccented word", derived)
	}
	if !strings.Contains(derived, "8") || !strings.Contains(derived, "{min}") {
		t.Errorf("pseudo message %q dropped the digits or the placeholder", derived)
	}
}

func TestIsPseudoLocale(t *testing.T) {
	t.Parallel()

	if !IsPseudoLocale(PseudoLocale) {
		t.Errorf("IsPseudoLocale(%q) = false, want true", PseudoLocale)
	}
	for _, locale := range []string{"pt-BR", "en-US", "", "qps", "QPS-PLOC"} {
		if IsPseudoLocale(locale) {
			t.Errorf("IsPseudoLocale(%q) = true, want false", locale)
		}
	}
}

func equalStrings(left, right []string) bool {
	leftCopy := append([]string(nil), left...)
	rightCopy := append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)

	if len(leftCopy) != len(rightCopy) {
		return false
	}
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}
