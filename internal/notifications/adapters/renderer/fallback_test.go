package renderer_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/renderer"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// testCatalog resolves through the real generated catalog and can be told to
// miss specific strings. Injecting it is what lets the production fallback and
// the parity gate be exercised without shipping a catalog that is actually
// incomplete.
type testCatalog struct {
	broken map[string]bool
}

func newTestCatalog(broken ...string) *testCatalog {
	catalog := &testCatalog{broken: map[string]bool{}}
	for _, entry := range broken {
		catalog.broken[entry] = true
	}
	return catalog
}

func (c *testCatalog) resolve(locale, key string) (string, error) {
	if c.broken[locale+"|"+key] {
		return "", fmt.Errorf("i18n: unknown message key %q for locale %q", key, locale)
	}
	return i18n.Format(locale, key, nil)
}

func (c *testCatalog) breakKey(locale, key string) { c.broken[locale+"|"+key] = true }

// recordingReporter collects the fallback events of one test.
type recordingReporter struct {
	events []renderer.FallbackEvent
}

func (r *recordingReporter) ReportFallback(event renderer.FallbackEvent) {
	r.events = append(r.events, event)
}

// TestCatalogDefaultLocalesAgree keeps the two packages from drifting apart:
// the renderer falls back to the domain's default, and that label has to be a
// locale the catalog actually ships.
func TestCatalogDefaultLocalesAgree(t *testing.T) {
	if i18n.DefaultLocale != domain.LocaleDefault.String() {
		t.Errorf("i18n default = %q, notifications default = %q, want one default",
			i18n.DefaultLocale, domain.LocaleDefault)
	}
	shipped := false
	for _, locale := range domain.Locales() {
		if locale == domain.LocaleDefault {
			shipped = true
		}
	}
	if !shipped {
		t.Errorf("default locale %q has no catalog", domain.LocaleDefault)
	}
}

// catalogFieldsOf lists the catalog fields one template asks for. It restates
// the contract instead of importing the renderer's own list, because a test
// that derives its expectations from the code under test cannot catch the code
// forgetting a key. A template that carries no code has no code label to
// miss (P16-T06), and demanding one would fail on a message whose catalog is
// complete.
func catalogFieldsOf(templateID domain.TemplateID) []string {
	if templateID.CarriesCode() {
		return []string{"subject", "lead", "code_label"}
	}
	return []string{"subject", "lead"}
}

// TestRendererVerifiesCatalogParityAtWiring is the "CI fails on a missing key"
// rule: with fallback disabled, a string missing from any shipped locale is a
// wiring failure, and every template is checked rather than the one a test
// happens to render.
func TestRendererVerifiesCatalogParityAtWiring(t *testing.T) {
	for _, locale := range domain.Locales() {
		for _, templateID := range domain.TemplateIDs() {
			for _, field := range catalogFieldsOf(templateID) {
				key := "email." + templateID.String() + "." + field
				t.Run(locale.String()+"."+key, func(t *testing.T) {
					catalog := newTestCatalog(locale.String() + "|" + key)
					if _, err := renderer.NewRenderer(renderer.WithCatalog(catalog.resolve)); err == nil {
						t.Fatalf("NewRenderer() error = nil, want %q in %q refused", key, locale)
					}
				})
			}
		}
	}
	for _, key := range []string{"email.greeting", "email.signature"} {
		t.Run("shared."+key, func(t *testing.T) {
			if _, err := renderer.NewRenderer(renderer.WithCatalog(newTestCatalog("en-US|" + key).resolve)); err == nil {
				t.Fatalf("NewRenderer() error = nil, want %q refused", key)
			}
		})
	}
	if _, err := renderer.NewRenderer(renderer.WithCatalog(newTestCatalog().resolve)); err != nil {
		t.Fatalf("NewRenderer() error = %v, want a complete catalog accepted", err)
	}
}

// TestProductionModeRequiresTheFallbackSource: the fallback hands out the
// default locale's string, so that string has to exist — a locale that cannot
// answer for itself cannot answer for anybody else either.
func TestProductionModeRequiresTheFallbackSource(t *testing.T) {
	const missing = "pt-BR|email.verification.lead"
	if _, err := renderer.NewRenderer(
		renderer.WithCatalog(newTestCatalog(missing).resolve),
		renderer.WithFallback(&recordingReporter{}),
	); err == nil {
		t.Error("NewRenderer() error = nil, want the missing default-locale string refused")
	}

	// A string missing from another locale does wire: that is the state the
	// report exists for, and the message is still composed.
	if _, err := renderer.NewRenderer(
		renderer.WithCatalog(newTestCatalog("en-US|email.verification.lead").resolve),
		renderer.WithFallback(&recordingReporter{}),
	); err != nil {
		t.Errorf("NewRenderer() error = %v, want the fallback to cover a non-default locale", err)
	}
}

// TestRenderFallsBackToTheDefaultLocaleAndReports is the monitored fallback:
// the recipient reads a complete email in a language we ship, and the operator
// gets a metric naming the defect.
func TestRenderFallsBackToTheDefaultLocaleAndReports(t *testing.T) {
	const missingKey = "email.verification.lead"
	catalog := newTestCatalog("en-US|" + missingKey)
	reporter := &recordingReporter{}
	engine, err := renderer.NewRenderer(renderer.WithCatalog(catalog.resolve), renderer.WithFallback(reporter))
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}
	body, err := engine.Render(domain.TemplateVerification, domain.LocaleAmericanEnglish, values(t, "Ana", "K7QP-2M4Z-9RTX"))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	// The string that resolved stays in the requested language.
	wantSubject, err := i18n.Format("en-US", "email.verification.subject", nil)
	if err != nil {
		t.Fatalf("catalog lookup error = %v", err)
	}
	if body.Subject != wantSubject {
		t.Errorf("subject = %q, want the requested locale", body.Subject)
	}
	// The string that did not resolve is the default locale's, in both
	// representations — the message is complete, not half-rendered.
	fallbackLead, err := i18n.Format("pt-BR", "email.verification.lead", nil)
	if err != nil {
		t.Fatalf("catalog lookup error = %v", err)
	}
	if !strings.Contains(body.Text, fallbackLead) || !strings.Contains(body.HTML, fallbackLead) {
		t.Errorf("body does not carry the fallback string:\n%s", body.Text)
	}

	if len(reporter.events) != 1 {
		t.Fatalf("fallback events = %d, want exactly one", len(reporter.events))
	}
	event := reporter.events[0]
	if event.Template != domain.TemplateVerification ||
		event.Requested != domain.LocaleAmericanEnglish ||
		event.Source != domain.LocaleDefault ||
		event.Key != missingKey {
		t.Errorf("event = %+v, want the defect named", event)
	}

	// A locale that resolves everything reports nothing: the metric measures
	// the defect, not the rendering.
	if _, err := engine.Render(domain.TemplateVerification, domain.LocaleBrazilianPortuguese, values(t, "Ana", "K7QP-2M4Z-9RTX")); err != nil {
		t.Fatalf("Render(pt-BR) error = %v", err)
	}
	if len(reporter.events) != 1 {
		t.Errorf("fallback events = %d, want the first locale to stay silent", len(reporter.events))
	}
}

// TestStrictModeRefusesAMissingStringInsteadOfFallingBack: without a reporter
// there is no metric, so there is no fallback — a string that cannot be
// resolved fails the render instead of being silently replaced.
func TestStrictModeRefusesAMissingStringInsteadOfFallingBack(t *testing.T) {
	catalog := newTestCatalog()
	engine, err := renderer.NewRenderer(renderer.WithCatalog(catalog.resolve))
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}
	catalog.breakKey("en-US", "email.verification.lead")
	if _, err := engine.Render(domain.TemplateVerification, domain.LocaleAmericanEnglish, values(t, "Ana", "K7QP-2M4Z-9RTX")); err == nil {
		t.Error("Render() error = nil, want a strict render to refuse a missing string")
	}
}

// TestRenderNeverHandsOutARawKey is the rule that survives every mode: a
// recipient never reads a catalog key. When no locale can supply the string,
// the render fails and the queue records a dead job.
func TestRenderNeverHandsOutARawKey(t *testing.T) {
	const key = "email.verification.lead"
	catalog := newTestCatalog()
	reporter := &recordingReporter{}
	engine, err := renderer.NewRenderer(renderer.WithCatalog(catalog.resolve), renderer.WithFallback(reporter))
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}
	// The catalog was complete at wiring; the string disappears from both
	// locales afterwards, which is the deployment skew no fallback can cover.
	catalog.breakKey("en-US", key)
	catalog.breakKey("pt-BR", key)
	body, err := engine.Render(domain.TemplateVerification, domain.LocaleAmericanEnglish, values(t, "Ana", "K7QP-2M4Z-9RTX"))
	if err == nil {
		t.Fatal("Render() error = nil, want a failure when no locale can supply the string")
	}
	if body != (domain.Body{}) {
		t.Errorf("body = %+v, want nothing renderable", body)
	}
	if len(reporter.events) != 0 {
		t.Errorf("fallback events = %d, want no event for a fallback that did not happen", len(reporter.events))
	}
}
