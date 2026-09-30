// Package renderer composes the localized transactional emails of Goyim
// Arena from the shared catalog (P15-T03, P15-T07).
//
// Three rules shape the package. First, every localized string comes from
// internal/i18n, so an email and the interface it mirrors never drift apart.
// Second, the markup is produced by html/template from one reviewed document
// per message type — never by concatenating localized strings with user data:
// the catalog string is substituted verbatim (i18n documents that escaping
// belongs to the rendering context), so a user-supplied display name reaches
// the document only as an html/template action, where the correct escaping for
// that position is applied by construction. Third, a missing string is a
// build-time failure, never a broken email: the catalog parity of every
// template is verified when the renderer is wired, per I18N_STANDARD.md
// section 8.
package renderer

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/websurface"
)

// Catalog keys, one block per message type, shared by the plain-text and
// HTML representations. The message-type segment is the template
// identifier, so a new template means a new catalog block and nothing else.
const (
	keyGreeting  = "email.greeting"
	keySignature = "email.signature"
	keySubject   = "email.%s.subject"
	keyLead      = "email.%s.lead"
	keyCodeLabel = "email.%s.code_label"
)

// htmlDocument is the transactional email document.
//
// Every template renders this one document because their difference is
// entirely in the catalog: the subject, the lead and the code label are looked
// up per template, and the markup that carries them is identical. Two
// byte-identical sources would be two places to keep in step, which is exactly
// the drift the standard closes ("a change of meaning updates every locale in
// the same commit" applies to the document too). The registry below still
// requires an explicit entry per template, so a template that needs different
// markup is a visible addition rather than a silent reuse.
const htmlDocument = `<!doctype html>
<html lang="{{.Language}}" dir="{{dir .Language}}">
<head>
<meta charset="utf-8">
<title>{{.Subject}}</title>
</head>
<body>
<p>{{.Greeting}} {{.Name}}</p>
<p>{{.Lead}}</p>
{{if .Code}}<p><strong>{{.CodeLabel}}</strong></p>
<p>{{.Code}}</p>
{{end}}<p>{{.Signature}}</p>
</body>
</html>
`

// templateSources is the registry of documents, one entry per template of the
// closed set. Absence is a wiring failure: a template with no document cannot
// be sent.
func templateSources() map[domain.TemplateID]string {
	return map[domain.TemplateID]string{
		domain.TemplateVerification:  htmlDocument,
		domain.TemplatePasswordReset: htmlDocument,
		// The notice renders the same document, and its code block is the only
		// part that falls away: a second document would be a second place to
		// keep the shared markup in step for a message that differs by one
		// paragraph.
		domain.TemplatePasswordChanged: htmlDocument,
	}
}

// htmlData is the data handed to the template. Greeting, Lead, CodeLabel,
// Signature and Subject are catalog strings (no caller input); Name and Code
// are raw values that html/template escapes for the position they occupy.
type htmlData struct {
	Language  string
	Subject   string
	Greeting  string
	Name      string
	Lead      string
	CodeLabel string
	Code      string
	Signature string
}

// CatalogFunc resolves one catalog message of one locale. Production uses the
// generated catalog; tests inject one to drive the states a generated catalog
// cannot show (a string missing in a single locale), which is how the
// production fallback stays exercised instead of becoming untested code.
type CatalogFunc func(locale, key string) (string, error)

// FallbackEvent is one message the locale of a job could not resolve, and the
// locale that answered instead.
//
// The event has four fields and no more: the template, the locale that was
// asked for, the key and the locale that answered. A recipient, a display name
// or a one-time code has no field to travel in, so the metric this becomes
// cannot carry personal data — the same closing by type the domain uses for
// template values.
type FallbackEvent struct {
	Template  domain.TemplateID
	Requested domain.Locale
	Key       string
	Source    domain.Locale
}

// FallbackReporter receives every fallback. Production passes the one below;
// a nil reporter means "no fallback allowed", which is the development and CI
// behavior.
type FallbackReporter interface {
	// ReportFallback records one fallback. It must not block: it runs inside
	// a delivery.
	ReportFallback(event FallbackEvent)
}

// catalogFromI18n resolves a key through the generated catalog.
func catalogFromI18n(locale, key string) (string, error) {
	return i18n.Format(locale, key, nil)
}

// Option configures the renderer.
type Option func(*Renderer)

// WithCatalog replaces the catalog the renderer reads. It exists for tests;
// production uses the generated catalog.
func WithCatalog(catalog CatalogFunc) Option {
	return func(r *Renderer) {
		if catalog != nil {
			r.catalog = catalog
		}
	}
}

// WithFallback enables the production fallback: a string missing in the locale
// a job froze is rendered from the product default and reported, instead of
// failing the message. Without it the renderer is strict, which is what
// development and CI must be (I18N_STANDARD.md section 8).
func WithFallback(reporter FallbackReporter) Option {
	return func(r *Renderer) {
		r.reporter = reporter
	}
}

// Renderer composes bodies from the catalog and the fixed HTML templates.
// It is immutable after construction and safe for concurrent use.
type Renderer struct {
	documents map[domain.TemplateID]*template.Template
	catalog   CatalogFunc
	reporter  FallbackReporter
}

// NewRenderer builds the renderer. It fails when a template has no document,
// when a document does not parse — programming errors that must surface at
// wiring time, never as a broken email — or when the catalog cannot satisfy
// every template (see checkCatalog).
func NewRenderer(options ...Option) (*Renderer, error) {
	built := &Renderer{catalog: catalogFromI18n}
	for _, option := range options {
		if option != nil {
			option(built)
		}
	}
	if built.catalog == nil {
		return nil, fmt.Errorf("renderer: no catalog")
	}

	sources := templateSources()
	documents := make(map[domain.TemplateID]*template.Template, len(sources))
	for _, id := range domain.TemplateIDs() {
		source, ok := sources[id]
		if !ok {
			return nil, fmt.Errorf("renderer: no html source for template %q", id)
		}
		parsed, err := template.New(id.String()).Funcs(websurface.Funcs()).Parse(source)
		if err != nil {
			return nil, fmt.Errorf("renderer: parse template %q: %w", id, err)
		}
		documents[id] = parsed
	}
	built.documents = documents

	if err := built.checkCatalog(); err != nil {
		return nil, err
	}
	return built, nil
}

// checkCatalog is the parity gate of this adapter, and it runs at wiring time
// because a missing string must fail the build, never a user request
// (I18N_STANDARD.md section 8).
//
// Strict mode verifies every shipped locale: development and CI fail on a
// missing key, missing namespace or divergent placeholder — the generator
// already enforces that over the JSON, and this proves the templates ask for
// exactly the keys the catalog declares. Production mode verifies the default
// locale instead, because that is the string the fallback hands out: without
// it there would be nothing to fall back to. A non-default locale missing a
// key then renders the default one and reports it, instead of failing the
// message of a user who happens to prefer that language.
func (r *Renderer) checkCatalog() error {
	locales := domain.Locales()
	if r.reporter != nil {
		locales = []domain.Locale{domain.LocaleDefault}
	}
	for _, id := range domain.TemplateIDs() {
		for _, key := range keysOf(id) {
			for _, locale := range locales {
				if _, err := r.catalog(locale.String(), key); err != nil {
					return fmt.Errorf("renderer: template %q needs %q in %q: %w", id, key, locale, err)
				}
			}
		}
	}
	return nil
}

// keysOf returns the catalog keys one template needs, in a deterministic
// order. It is the template's contract with the catalog, and the parity gate
// above is what keeps the two in step.
//
// A template that carries no code does not ask for a code label: requiring a
// label for a message with no code would put a string in the catalog that no
// recipient can ever read, and the parity gate would then protect a phantom.
func keysOf(id domain.TemplateID) []string {
	namespace := id.String()
	keys := []string{
		keyGreeting,
		keySignature,
		fmt.Sprintf(keySubject, namespace),
		fmt.Sprintf(keyLead, namespace),
	}
	if id.CarriesCode() {
		keys = append(keys, fmt.Sprintf(keyCodeLabel, namespace))
	}
	return keys
}

// Render composes the body for one message. Unknown templates and locales
// are refused before any lookup, and the values are re-validated here so the
// adapter does not depend on the caller having done it.
func (r *Renderer) Render(templateID domain.TemplateID, locale domain.Locale, values domain.TemplateValues) (domain.Body, error) {
	if r == nil {
		return domain.Body{}, domain.ErrMissingDependency
	}
	if !templateID.Valid() {
		return domain.Body{}, domain.ErrUnsupportedTemplate
	}
	if !locale.Valid() {
		return domain.Body{}, domain.ErrUnsupportedLocale
	}
	if _, err := domain.ValidateTemplateValues(templateID, values.Name, values.Code); err != nil {
		return domain.Body{}, err
	}
	document, ok := r.documents[templateID]
	if !ok {
		return domain.Body{}, domain.ErrUnsupportedTemplate
	}

	data := htmlData{
		Language: locale.String(),
		Name:     values.Name,
		Code:     values.Code,
	}
	// Every localized string of the message is resolved through the same
	// path, so a fallback cannot apply to the body but not the subject.
	namespace := templateID.String()
	type catalogField struct {
		target *string
		key    string
	}
	fields := []catalogField{
		{&data.Subject, fmt.Sprintf(keySubject, namespace)},
		{&data.Greeting, keyGreeting},
		{&data.Lead, fmt.Sprintf(keyLead, namespace)},
		{&data.Signature, keySignature},
	}
	// The label of the code is resolved only for the templates that have one:
	// asking the catalog for a key a notice does not declare would turn a
	// correct message into a failed delivery.
	if templateID.CarriesCode() {
		fields = append(fields, catalogField{&data.CodeLabel, fmt.Sprintf(keyCodeLabel, namespace)})
	}
	for _, field := range fields {
		message, err := r.resolve(templateID, locale, field.key)
		if err != nil {
			return domain.Body{}, err
		}
		*field.target = message
	}

	var html bytes.Buffer
	// html/template applies the escaping of the surrounding context, which
	// is why the user-supplied fields are passed as data rather than
	// interpolated into the localized strings first.
	if err := document.Execute(&html, data); err != nil {
		return domain.Body{}, fmt.Errorf("renderer: execute template %q: %w", templateID, err)
	}

	return domain.NewBody(
		data.Subject,
		plainText(data.Greeting, data.Name, data.Lead, data.CodeLabel, data.Code, data.Signature),
		html.String(),
	)
}

// resolve returns one localized string, falling back to the product default
// when the locale of the job cannot supply it.
//
// The fallback is a production concession, not a license: it is reported, so a
// string missing from a shipped locale becomes an alert instead of a shrug.
// The raw key never reaches a recipient — when neither locale can supply the
// string, the render fails and the queue records a dead job.
func (r *Renderer) resolve(templateID domain.TemplateID, locale domain.Locale, key string) (string, error) {
	message, err := r.catalog(locale.String(), key)
	if err == nil {
		return message, nil
	}
	// Nothing to fall back to when the default locale is the one that is
	// missing the string, or when fallback is disabled (development and CI).
	if r.reporter == nil || locale == domain.LocaleDefault {
		return "", fmt.Errorf("renderer: resolve %q in %q: %w", key, locale, err)
	}
	fallback, fallbackErr := r.catalog(domain.LocaleDefault.String(), key)
	if fallbackErr != nil {
		return "", fmt.Errorf("renderer: resolve %q in %q: %w", key, locale, err)
	}
	r.reporter.ReportFallback(FallbackEvent{
		Template:  templateID,
		Requested: locale,
		Key:       key,
		Source:    domain.LocaleDefault,
	})
	return fallback, nil
}

// plainText builds the text/plain alternative. Substitution is verbatim
// here on purpose: this representation carries no markup, so there is no
// context to escape for.
//
// The code block is written only when the message has one, which is the same
// condition the HTML document applies, so both representations of a notice
// omit it together instead of one of them leaving a dangling label.
func plainText(greeting, name, lead, codeLabel, code, signature string) string {
	var builder strings.Builder
	builder.WriteString(greeting)
	if name != "" {
		builder.WriteString(" ")
		builder.WriteString(name)
	}
	builder.WriteString("\n\n")
	builder.WriteString(lead)
	builder.WriteString("\n\n")
	if code != "" {
		builder.WriteString(codeLabel)
		builder.WriteString(": ")
		builder.WriteString(code)
		builder.WriteString("\n\n")
	}
	builder.WriteString(signature)
	return builder.String()
}
