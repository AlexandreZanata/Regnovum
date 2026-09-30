package renderer_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/renderer"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

func newRenderer(t *testing.T) *renderer.Renderer {
	t.Helper()
	built, err := renderer.NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}
	return built
}

func values(t *testing.T, name, code string) domain.TemplateValues {
	t.Helper()
	rendered, err := domain.NewTemplateValues(name, code)
	if err != nil {
		t.Fatalf("NewTemplateValues() error = %v", err)
	}
	return rendered
}

// valuesFor builds the values of one template, including the templates that
// carry no code (P16-T06): the code argument is ignored for those, because a
// notice has no field to render one in and the domain refuses it outright. A
// test that loops over the whole set therefore asks each template for the pair
// it actually declares instead of forcing one shape onto all of them.
func valuesFor(t *testing.T, templateID domain.TemplateID, name, code string) domain.TemplateValues {
	t.Helper()
	if !templateID.CarriesCode() {
		code = ""
	}
	built, err := domain.ValidateTemplateValues(templateID, name, code)
	if err != nil {
		t.Fatalf("ValidateTemplateValues(%s) error = %v", templateID, err)
	}
	return built
}

// TestRenderIsLocalized proves the body comes from the catalog: each locale
// matches its own catalog entries, and the two locales do not produce the
// same subject (a hardcoded string would pass the first check and fail this
// one).
func TestRenderIsLocalized(t *testing.T) {
	engine := newRenderer(t)
	subjects := make(map[domain.Locale]string, len(domain.Locales()))
	for _, locale := range domain.Locales() {
		body, err := engine.Render(domain.TemplateVerification, locale, values(t, "Ana", "K7QP-2M4Z-9RTX"))
		if err != nil {
			t.Fatalf("Render(%s) error = %v", locale, err)
		}
		wantSubject, err := i18n.Format(locale.String(), "email.verification.subject", nil)
		if err != nil {
			t.Fatalf("catalog lookup error = %v", err)
		}
		if body.Subject != wantSubject {
			t.Errorf("Render(%s) subject = %q, want %q", locale, body.Subject, wantSubject)
		}
		wantSignature, err := i18n.Format(locale.String(), "email.signature", nil)
		if err != nil {
			t.Fatalf("catalog lookup error = %v", err)
		}
		if !strings.Contains(body.Text, wantSignature) || !strings.Contains(body.HTML, wantSignature) {
			t.Errorf("Render(%s) body does not carry the localized signature", locale)
		}
		if !strings.Contains(body.HTML, `lang="`+locale.String()+`"`) {
			t.Errorf("Render(%s) html does not declare the document language", locale)
		}
		subjects[locale] = body.Subject
	}
	if subjects[domain.LocaleBrazilianPortuguese] == subjects[domain.LocaleAmericanEnglish] {
		t.Error("both locales produced the same subject: the body is not localized")
	}
}

// TestRenderEscapesUntrustedValues is the core security property of the
// package: a display name and a code are data, never markup — in every locale
// and every template, because escaping is a property of the position and not
// of the language.
//
// The notice has no code, so what is proven for it is the half of the property
// that exists: its display name is escaped, and its body carries no code at
// all. The code position is covered by the templates that have one — the
// document is shared, so the escaping of that position is exercised there.
func TestRenderEscapesUntrustedValues(t *testing.T) {
	engine := newRenderer(t)
	hostileName := `<script>alert("x")</script>&'"><`
	hostileCode := `<b>code</b>&"<`
	for _, locale := range domain.Locales() {
		for _, templateID := range domain.TemplateIDs() {
			t.Run(locale.String()+"."+templateID.String(), func(t *testing.T) {
				body, err := engine.Render(templateID, locale, valuesFor(t, templateID, hostileName, hostileCode))
				if err != nil {
					t.Fatalf("Render() error = %v", err)
				}
				for _, raw := range []string{"<script>", "</script>", "<b>code</b>"} {
					if strings.Contains(body.HTML, raw) {
						t.Errorf("html contains unescaped %q: %s", raw, body.HTML)
					}
				}
				for _, escaped := range []string{"&lt;script&gt;", "&amp;", "&#34;"} {
					if !strings.Contains(body.HTML, escaped) {
						t.Errorf("html does not contain %q", escaped)
					}
				}
				// The plain-text alternative carries the same values verbatim: it
				// has no markup context to escape for.
				if templateID.CarriesCode() {
					if !strings.Contains(body.HTML, "&lt;b&gt;code&lt;/b&gt;") {
						t.Error("html does not escape the code value")
					}
					if !strings.Contains(body.Text, hostileCode) {
						t.Error("text body does not carry the code verbatim")
					}
				} else if strings.Contains(body.Text, "<b>") || strings.Contains(body.HTML, "<b>code</b>") {
					t.Error("a template that carries no code rendered a code value")
				}
				if strings.Contains(body.Subject, "&lt;") {
					t.Error("subject was html-escaped: it is a plain-text header value")
				}
			})
		}
	}
}

// TestRenderTreatsTemplateSyntaxAsData proves the values are never fed back
// into the template engine: a value that looks like an action must surface
// literally, and the document must keep exactly one signature.
func TestRenderTreatsTemplateSyntaxAsData(t *testing.T) {
	engine := newRenderer(t)
	body, err := engine.Render(domain.TemplateVerification, domain.LocaleAmericanEnglish, values(t, "{{.Subject}}", "{{.Code}}"))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, literal := range []string{"{{.Subject}}", "{{.Code}}"} {
		if !strings.Contains(body.HTML, literal) {
			t.Errorf("html lost the literal value %q: a template action inside data was executed", literal)
		}
	}
	signature, err := i18n.Format(domain.LocaleAmericanEnglish.String(), "email.signature", nil)
	if err != nil {
		t.Fatalf("catalog lookup error = %v", err)
	}
	if count := strings.Count(body.HTML, signature); count != 1 {
		t.Errorf("html carries %d signatures, want 1", count)
	}
}

func TestRenderOmitsAnEmptyName(t *testing.T) {
	engine := newRenderer(t)
	body, err := engine.Render(domain.TemplateVerification, domain.LocaleBrazilianPortuguese, values(t, "", "K7QP-2M4Z-9RTX"))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	greeting, err := i18n.Format(domain.LocaleBrazilianPortuguese.String(), "email.greeting", nil)
	if err != nil {
		t.Fatalf("catalog lookup error = %v", err)
	}
	if !strings.Contains(body.HTML, "<p>"+greeting+" </p>") {
		t.Errorf("html greeting = %q, want the bare greeting", body.HTML)
	}
	if !strings.Contains(body.Text, greeting+"\n") {
		t.Error("text body does not start with the bare greeting")
	}
}

func TestRenderRefusesUnknownInputs(t *testing.T) {
	engine := newRenderer(t)
	valid := values(t, "Ana", "K7QP-2M4Z-9RTX")
	for _, testCase := range []struct {
		name     string
		template domain.TemplateID
		locale   domain.Locale
		wantErr  error
	}{
		{"unknown template", "marketing", domain.LocaleBrazilianPortuguese, domain.ErrUnsupportedTemplate},
		{"empty template", "", domain.LocaleBrazilianPortuguese, domain.ErrUnsupportedTemplate},
		{"unknown locale", domain.TemplateVerification, "fr-FR", domain.ErrUnsupportedLocale},
		{"empty locale", domain.TemplateVerification, "", domain.ErrUnsupportedLocale},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := engine.Render(testCase.template, testCase.locale, valid); !errors.Is(err, testCase.wantErr) {
				t.Errorf("Render() error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

func TestRenderRefusesValuesThatMayNotTravel(t *testing.T) {
	engine := newRenderer(t)
	// The values are re-validated inside the adapter, so a caller that
	// bypassed the constructor still cannot inject a control character.
	for _, testCase := range []struct {
		name  string
		value domain.TemplateValues
	}{
		{"code with a line break", domain.TemplateValues{Code: "abc\ndef"}},
		{"code with a space", domain.TemplateValues{Code: "abc def"}},
		{"code empty", domain.TemplateValues{Code: ""}},
		{"name with a control character", domain.TemplateValues{Name: "a\x00b", Code: "code"}},
		{"name too long", domain.TemplateValues{Name: strings.Repeat("a", 81), Code: "code"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := engine.Render(domain.TemplateVerification, domain.LocaleBrazilianPortuguese, testCase.value); !errors.Is(err, domain.ErrInvalidTemplateValue) {
				t.Errorf("Render() error = %v, want ErrInvalidTemplateValue", err)
			}
		})
	}
}

func TestRenderIsStableUnderConcurrency(t *testing.T) {
	engine := newRenderer(t)
	values := values(t, "Ana", "K7QP-2M4Z-9RTX")
	want, err := engine.Render(domain.TemplateVerification, domain.LocaleAmericanEnglish, values)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	var group sync.WaitGroup
	failures := make(chan domain.Body, 16)
	for index := 0; index < 16; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			got, err := engine.Render(domain.TemplateVerification, domain.LocaleAmericanEnglish, values)
			if err != nil {
				failures <- domain.Body{}
				return
			}
			failures <- got
		}()
	}
	group.Wait()
	close(failures)
	for got := range failures {
		if got != want {
			t.Errorf("concurrent Render() = %+v, want %+v", got, want)
		}
	}
}
