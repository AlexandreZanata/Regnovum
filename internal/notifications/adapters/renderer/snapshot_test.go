package renderer_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// snapshotName returns the committed artifact of one rendered email.
func snapshotName(templateID domain.TemplateID, locale domain.Locale) string {
	return filepath.Join("testdata", templateID.String()+"."+locale.String()+".golden")
}

// snapshot is the committed representation of one rendered email: the subject
// and both representations, so a change to any of the three is a reviewable
// diff instead of a silent edit to what a recipient reads.
func snapshot(body domain.Body) string {
	var builder strings.Builder
	builder.WriteString("subject: " + body.Subject + "\n")
	builder.WriteString("--- text\n")
	builder.WriteString(body.Text + "\n")
	builder.WriteString("--- html\n")
	builder.WriteString(body.HTML)
	return builder.String()
}

// TestRenderedEmailsMatchTheCommittedSnapshots is the guard the standard asks
// for: one committed artifact per template and locale, compared byte for byte.
//
// The comparison never rewrites the artifact. A mismatch means the email a
// recipient reads has changed, and the fix is a deliberate edit to the file in
// the same commit — reviewed as the change in wording or markup that it is.
func TestRenderedEmailsMatchTheCommittedSnapshots(t *testing.T) {
	engine := newRenderer(t)
	const name, code = "Ana", "K7QP-2M4Z-9RTX"
	for _, templateID := range domain.TemplateIDs() {
		for _, locale := range domain.Locales() {
			t.Run(templateID.String()+"."+locale.String(), func(t *testing.T) {
				body, err := engine.Render(templateID, locale, valuesFor(t, templateID, name, code))
				if err != nil {
					t.Fatalf("Render() error = %v", err)
				}
				// Subject, text and html exist for every template in every
				// shipped locale: the snapshot is incomplete without all
				// three.
				if body.Subject == "" || body.Text == "" || body.HTML == "" {
					t.Fatalf("rendered email is incomplete: %+v", body)
				}
				path := snapshotName(templateID, locale)
				committed, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read %s: %v", path, err)
				}
				if rendered := snapshot(body); rendered != string(committed) {
					t.Errorf("rendered email differs from %s.\n--- committed\n%s\n--- rendered\n%s\n"+
						"Update the artifact deliberately if the change is intended.", path, committed, rendered)
				}
			})
		}
	}
}

// TestEveryTemplateIsReallyLocalized closes the other half: the artifacts
// existing is not the same as being localized, and a hardcoded string would
// pass the snapshot check for exactly one locale.
func TestEveryTemplateIsReallyLocalized(t *testing.T) {
	engine := newRenderer(t)
	for _, templateID := range domain.TemplateIDs() {
		rendered := make(map[domain.Locale]domain.Body, len(domain.Locales()))
		for _, locale := range domain.Locales() {
			body, err := engine.Render(templateID, locale, valuesFor(t, templateID, "Ana", "K7QP-2M4Z-9RTX"))
			if err != nil {
				t.Fatalf("Render(%s, %s) error = %v", templateID, locale, err)
			}
			rendered[locale] = body
		}
		left, right := rendered[domain.LocaleBrazilianPortuguese], rendered[domain.LocaleAmericanEnglish]
		for _, part := range []struct {
			name string
			pt   string
			en   string
		}{
			{"subject", left.Subject, right.Subject},
			{"text", left.Text, right.Text},
			{"html", left.HTML, right.HTML},
		} {
			if part.pt == "" || part.en == "" {
				t.Errorf("%s: %s is empty in one of the locales", templateID, part.name)
				continue
			}
			if part.pt == part.en {
				t.Errorf("%s: %s is identical in both locales, so it is not localized", templateID, part.name)
			}
		}
	}
}

// tagPattern matches one markup tag, attributes included.
var tagPattern = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)

// codeBlockTags is the markup the code block contributes to the shared
// document, in order. A template that carries no code renders everything else
// and nothing of this.
var codeBlockTags = []string{"<p>", "<strong>", "</strong>", "</p>", "<p>", "</p>"}

// TestTemplatesShareOneDocumentStructure is the parity of the markup: the
// templates differ in the catalog block they read, never in the document they
// render. A template that silently gained or lost markup — or that reached for
// a second document — fails here, and the values it fills are proven to be the
// only difference.
//
// The one structural difference the set is allowed to have is the code block
// (P16-T06): the notice renders the same document without it, so its tag
// sequence must equal a code-carrying template's sequence with exactly that run
// of tags removed. Stating the exception makes it testable instead of tolerated
// — a notice that quietly dropped or gained a paragraph fails here.
func TestTemplatesShareOneDocumentStructure(t *testing.T) {
	engine := newRenderer(t)
	ids := domain.TemplateIDs()
	if len(ids) < 3 {
		t.Fatalf("templates = %d, want at least three to compare", len(ids))
	}
	for _, locale := range domain.Locales() {
		t.Run(locale.String(), func(t *testing.T) {
			markup := make(map[domain.TemplateID]string, len(ids))
			for _, templateID := range ids {
				body, err := engine.Render(templateID, locale, valuesFor(t, templateID, "Ana", "K7QP-2M4Z-9RTX"))
				if err != nil {
					t.Fatalf("Render(%s, %s) error = %v", templateID, locale, err)
				}
				markup[templateID] = body.HTML
			}

			var codeTemplates []domain.TemplateID
			var noticeTemplates []domain.TemplateID
			for _, templateID := range ids {
				if templateID.CarriesCode() {
					codeTemplates = append(codeTemplates, templateID)
					continue
				}
				noticeTemplates = append(noticeTemplates, templateID)
			}
			if len(codeTemplates) < 2 || len(noticeTemplates) == 0 {
				t.Fatalf("the set must hold both kinds, got %d code-carrying and %d notices", len(codeTemplates), len(noticeTemplates))
			}

			// Every code-carrying template renders the document with the block.
			wantTags := strings.Join(tagPattern.FindAllString(markup[codeTemplates[0]], -1), "|")
			wantValues := strings.Join(tagPattern.Split(markup[codeTemplates[0]], -1), "|")
			for _, templateID := range codeTemplates[1:] {
				if got := strings.Join(tagPattern.FindAllString(markup[templateID], -1), "|"); got != wantTags {
					t.Errorf("markup of %s differs from %s:\n%s\n%s", templateID, codeTemplates[0], wantTags, got)
				}
				if got := strings.Join(tagPattern.Split(markup[templateID], -1), "|"); got == wantValues {
					t.Errorf("%s and %s filled the document with the same values", templateID, codeTemplates[0])
				}
			}

			// A notice is the same document without the code block, and no
			// other change is allowed.
			withoutBlock, err := removeFirst(tagPattern.FindAllString(markup[codeTemplates[0]], -1), codeBlockTags)
			if err != nil {
				t.Fatalf("the code-carrying document does not carry the code block: %v", err)
			}
			for _, templateID := range noticeTemplates {
				got := tagPattern.FindAllString(markup[templateID], -1)
				if strings.Join(got, "|") != strings.Join(withoutBlock, "|") {
					t.Errorf("markup of the code-free %s is not the document minus the code block:\n%v\n%v", templateID, withoutBlock, got)
				}
				if strings.Join(tagPattern.Split(markup[templateID], -1), "|") == wantValues {
					t.Errorf("%s filled the document with the same values as %s", templateID, codeTemplates[0])
				}
			}
		})
	}
}

// removeFirst deletes the first occurrence of a contiguous subsequence and
// returns the rest. An absent subsequence is an error rather than a silent
// no-op: the whole point of the comparison above is that the block exists
// exactly once.
func removeFirst(tags []string, block []string) ([]string, error) {
	for start := 0; start+len(block) <= len(tags); start++ {
		matched := true
		for offset, tag := range block {
			if tags[start+offset] != tag {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		out := make([]string, 0, len(tags)-len(block))
		out = append(out, tags[:start]...)
		out = append(out, tags[start+len(block):]...)
		return out, nil
	}
	return nil, fmt.Errorf("subsequence %v not found in %v", block, tags)
}
