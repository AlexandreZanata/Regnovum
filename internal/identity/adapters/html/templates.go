// Package html is the inbound HTML adapter of the identity module (P18-T05):
// it owns the browser journey of the account — registration, email
// confirmation, sign in, sign out and password recovery — as semantic,
// server-rendered documents.
//
// The surface exists because the JSON API under /api/v1 is not a journey: it
// answers documents to a program. These pages are the ones a person fills in,
// and they are built to work with JavaScript switched off:
//
//   - every transition is a plain form POST answered with a rendered document
//     or a 303 redirect (never a JSON body a browser would display raw);
//   - every rejection the platform middleware can produce — a missing CSRF
//     token, a spent rate-limit budget — is translated into the same kind of
//     page, localized by the stable kind of the failure (I18N_STANDARD §5);
//   - the client module only *adds* the busy state to a submission that was
//     already going to happen, so removing it changes nothing about what the
//     server accepts.
//
// Two rules shape the markup. There is no inline style, script or event
// handler anywhere, because the browser policy of this binary is
// `style-src 'self'; script-src 'self'` with no nonce and no 'unsafe-inline'
// (internal/platform/securityheaders); the sheets and the module are the
// hashed assets of the manifest, resolved through html/template. And every
// dynamic value travels through html/template escaping, never as trusted
// markup.
package html

import (
	"html/template"
	"io"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/websurface"
)

// Logical names of the assets the pages load, as produced by cmd/assetgen
// from web/generated, web/src and web/public. The manifest resolves them to
// their hashed address; nothing here guesses a filename.
const (
	assetScript   = "pages/auth.js"
	assetResetCSS = "styles/reset.css"
	assetTokenCSS = "styles/tokens.css"
	assetBaseCSS  = "styles/base.css"
	assetPrimCSS  = "styles/primitives.css"
	assetShellCSS = "styles/shell.css"
	assetAuthCSS  = "styles/auth.css"
	assetCrest    = "realm/brand/crest.svg"
)

// requiredAssets is the exact set the pages load, in cascade order for the
// sheets: the shell chrome (skip link, current-page mark, footer) loads after
// the primitives it marks and before the page layout it frames. The crest is
// the realm brand mark shared with the kingdom dashboard; like every other
// entry it fails the composition when the build cannot resolve it.
var requiredAssets = []string{assetScript, assetResetCSS, assetTokenCSS, assetBaseCSS, assetPrimCSS, assetShellCSS, assetAuthCSS, assetCrest}

// Field identifier suffixes. They are the contract between the server-rendered
// markup and the client primitives: web/src/components/primitives/model.ts
// derives the same ids (`<name>-control`, `<name>-hint`, `<name>-error`) and
// the same `#<name>-control` fragment for the error summary links. The
// duplication is asserted on both sides — the frontend suite proves the
// model's ids, the adapter suite proves the rendered ones — because the two
// toolchains share no generator.
const (
	controlIDSuffix = "-control"
	hintIDSuffix    = "-hint"
	errorIDSuffix   = "-error"
)

// ActionLink is one navigational link of a page.
type ActionLink struct {
	Label string
	Href  string
	// Current marks the link of the address the visitor is on. It is decided
	// by the server from the request path and rendered as `aria-current`, so
	// the enhancer and the document agree before any script runs.
	Current bool
}

// DocumentData is the chrome every page shares: the application shell.
type DocumentData struct {
	// Lang is the interface locale, also the document's `lang` attribute.
	Lang string
	// PageTitle is the localized browser title.
	PageTitle string
	// Brand is the localized product name rendered in the header.
	Brand string
	// NavLabel is the accessible name of the navigation landmark.
	NavLabel string
	// Nav lists the account links of the header.
	Nav []ActionLink
	// SkipLabel is the localized skip-link text, pointing at the main landmark.
	SkipLabel string
	// FooterNote is the localized line closing the chrome.
	FooterNote string
	// CurrentPath is the normalized request path of this document, the stable
	// address a reload, a deep link and the back button agree on.
	CurrentPath string
	// SignedIn reports whether the request carries a session token. It only
	// chooses which links the chrome emphasizes; the server still enforces
	// every transition, so hiding a link is never the authorization.
	SignedIn bool
}

// FieldData is one rendered form control with its label, hint and error. The
// markup it produces is exactly what `ga-field` adopts: the element finds the
// label, the control and both message paragraphs already in the document and
// only completes the wiring.
type FieldData struct {
	// Name is the control name, and the identifier base of the field.
	Name string
	// Type is the input type.
	Type string
	// Label is the visible, translated label.
	Label string
	// Hint is the optional translated help text.
	Hint string
	// Error is the optional translated error of this field alone.
	Error string
	// Value is the value the browser may keep after a failed submission. It
	// is never set for a password.
	Value string
	// Autocomplete is the autocomplete token of the control.
	Autocomplete string
	// Required marks the control as required, both natively and for the
	// accessibility tree.
	Required bool
	// ControlID, HintID, ErrorID and DescribedBy are the identifiers the
	// primitives and the error summary links address.
	ControlID   string
	HintID      string
	ErrorID     string
	DescribedBy string
}

// SummaryItem is one link of the error summary.
type SummaryItem struct {
	// Target is the control identifier the link moves to, without the `#`.
	Target  string
	Message string
}

// FormPageData is one form document: a heading, an intro, the fields and, when
// the submission failed, the summary of what to fix.
type FormPageData struct {
	DocumentData
	Heading       string
	Intro         string
	Action        string
	Method        string
	CSRFName      string
	CSRFToken     string
	SummaryTitle  string
	Summary       []SummaryItem
	KeyField      *FieldData
	KeySubmit     string
	KeyHeading    string
	LegacyHeading string
	Fields        []FieldData
	SubmitLabel   string
	// BusyLabel is the status text announced while a submission is in flight
	// and the client module is present. It is never the only feedback: the
	// server always answers a document.
	BusyLabel string
	// After is the optional secondary link of a form (for example "forgot my
	// password" on the sign-in page).
	After *ActionLink
}

// NoticePageData is one document that reports an outcome: the confirmation
// after a registration, the confirmation of a confirmed email, the notice that
// a recovery code was sent, and the pages that report a refusal.
type NoticePageData struct {
	DocumentData
	Heading        string
	Detail         string
	AccountKey     string
	KeyInstruction string
	KeyWarning     string
	CopyLabel      string
	Actions        []ActionLink
}

// Templates owns the compiled documents of the browser journey.
type Templates struct {
	form   *template.Template
	notice *template.Template
}

// NewTemplates compiles the documents with the manifest's asset resolver. It
// fails when the manifest cannot resolve an asset the pages load, so a broken
// build fails at composition instead of rendering a page whose stylesheet or
// module 404s.
func NewTemplates(manifest assets.Manifest) (*Templates, error) {
	resolved := make(map[string]string, len(requiredAssets))
	for _, name := range requiredAssets {
		url, err := manifest.URL(name)
		if err != nil {
			return nil, err
		}
		resolved[name] = url
	}

	set, err := template.New("auth").Funcs(websurface.WithFuncs(manifest.TemplateFuncs())).Parse(templateSources)
	if err != nil {
		return nil, err
	}

	form, err := set.Clone()
	if err != nil {
		return nil, err
	}
	notice, err := set.Clone()
	if err != nil {
		return nil, err
	}
	return &Templates{
		form:   form.Lookup("form_page"),
		notice: notice.Lookup("notice_page"),
	}, nil
}

// RenderForm renders one form document.
func (t *Templates) RenderForm(writer io.Writer, data FormPageData) error {
	return t.form.Execute(writer, data)
}

// RenderNotice renders one outcome document.
func (t *Templates) RenderNotice(writer io.Writer, data NoticePageData) error {
	return t.notice.Execute(writer, data)
}

// newField builds a field and its identifiers.
func newField(name, inputType, label, hint, value, autocomplete string, required bool, fieldError string) FieldData {
	control := name + controlIDSuffix
	describes := make([]string, 0, 2)
	if hint != "" {
		describes = append(describes, name+hintIDSuffix)
	}
	if fieldError != "" {
		describes = append(describes, name+errorIDSuffix)
	}
	return FieldData{
		Name:         name,
		Type:         inputType,
		Label:        label,
		Hint:         hint,
		Error:        fieldError,
		Value:        value,
		Autocomplete: autocomplete,
		Required:     required,
		ControlID:    control,
		HintID:       name + hintIDSuffix,
		ErrorID:      name + errorIDSuffix,
		DescribedBy:  strings.Join(describes, " "),
	}
}

// templateSources is the whole set of documents of the journey. Void elements
// are self-closed and every attribute carries a value, so the rendered pages
// are well-formed markup as well as valid HTML5 — which is what lets the
// browser-policy test parse and scan them with the standard library.
const templateSources = `{{define "document_head"}}<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width, initial-scale=1" />
	<meta name="robots" content="noindex" />
	<title>{{.PageTitle}}</title>
	<link rel="stylesheet" href="{{asset "styles/reset.css"}}" />
	<link rel="stylesheet" href="{{asset "styles/tokens.css"}}" />
	<link rel="stylesheet" href="{{asset "styles/base.css"}}" />
	<link rel="stylesheet" href="{{asset "styles/primitives.css"}}" />
	<link rel="stylesheet" href="{{asset "styles/shell.css"}}" />
	<link rel="stylesheet" href="{{asset "styles/auth.css"}}" />
	<script type="module" src="{{asset "pages/auth.js"}}"></script>
</head>{{end}}

 {{define "document_nav"}}<header class="ga-auth__header">
	<a class="ga-auth__brand" href="/"><img src="{{asset "realm/brand/crest.svg"}}" alt="" width="30" height="34" />{{.Brand}}</a>
	<nav aria-label="{{.NavLabel}}">
		<ul class="ga-auth__nav">
			{{range .Nav}}<li><a href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}</a></li>
			{{end}}
		</ul>
	</nav>
</header>{{end}}

{{define "document_footer"}}<footer class="ga-shell__footer">
	<p>{{.FooterNote}}</p>
</footer>{{end}}

{{define "field"}}<ga-field name="{{.Name}}" label="{{.Label}}"{{if .Hint}} hint="{{.Hint}}"{{end}}{{if .Error}} error="{{.Error}}"{{end}}{{if .Required}} required="required"{{end}}>
	<label for="{{.ControlID}}">{{.Label}}</label>
	<input type="{{.Type}}" id="{{.ControlID}}" name="{{.Name}}"{{if .Autocomplete}} autocomplete="{{.Autocomplete}}"{{end}}{{if .Required}} required="required" aria-required="true"{{end}}{{if .Value}} value="{{.Value}}"{{end}}{{if .DescribedBy}} aria-describedby="{{.DescribedBy}}"{{end}}{{if .Error}} aria-invalid="true"{{end}} />
	{{if .Hint}}<p id="{{.HintID}}">{{.Hint}}</p>{{end}}
	{{if .Error}}<p id="{{.ErrorID}}" role="alert">{{.Error}}</p>{{end}}
</ga-field>{{end}}

{{define "form_page"}}<!DOCTYPE html>
<html lang="{{.Lang}}" dir="{{dir .Lang}}">
{{template "document_head" .}}
<body>
	<a class="ga-shell__skip" href="#main">{{.SkipLabel}}</a>
	{{template "document_nav" .}}
	<main id="main" class="ga-auth" tabindex="-1">
		<h1>{{.Heading}}</h1>
		<p>{{.Intro}}</p>
		<ga-error-summary{{if not .Summary}} hidden="hidden"{{end}}>
			<h2>{{.SummaryTitle}}</h2>
			<ul>
				{{range .Summary}}<li><a href="#{{.Target}}">{{.Message}}</a></li>
				{{end}}
			</ul>
		</ga-error-summary>
		{{if .KeyField}}<div class="ga-auth__key-section">
			{{if .KeyHeading}}<h2>{{.KeyHeading}}</h2>{{end}}
			<form class="ga-auth__form" method="{{.Method}}" action="{{.Action}}">
				<input type="hidden" name="{{.CSRFName}}" value="{{.CSRFToken}}" />
				{{template "field" .KeyField}}
				<ga-busy label="{{.BusyLabel}}">
					<span data-ga-indicator="true" hidden="hidden" aria-hidden="true"></span>
					<span data-ga-status="true" hidden="hidden" role="status" aria-live="polite" aria-atomic="true"></span>
					<button type="submit">{{if .KeySubmit}}{{.KeySubmit}}{{else}}{{.SubmitLabel}}{{end}}</button>
				</ga-busy>
			</form>
		</div>
		{{if .LegacyHeading}}<div class="ga-auth__divider"><span>{{.LegacyHeading}}</span></div>{{end}}{{end}}
		<form class="ga-auth__form" method="{{.Method}}" action="{{.Action}}">
			<input type="hidden" name="{{.CSRFName}}" value="{{.CSRFToken}}" />
			{{range .Fields}}{{template "field" .}}
			{{end}}<ga-busy label="{{.BusyLabel}}">
				<span data-ga-indicator="true" hidden="hidden" aria-hidden="true"></span>
				<span data-ga-status="true" hidden="hidden" role="status" aria-live="polite" aria-atomic="true"></span>
				<button type="submit">{{.SubmitLabel}}</button>
			</ga-busy>
		</form>
		{{if .After}}<p><a href="{{.After.Href}}">{{.After.Label}}</a></p>
		{{end}}
	</main>
	{{template "document_footer" .}}
</body>
</html>{{end}}

{{define "notice_page"}}<!DOCTYPE html>
<html lang="{{.Lang}}" dir="{{dir .Lang}}">
{{template "document_head" .}}
<body>
	<a class="ga-shell__skip" href="#main">{{.SkipLabel}}</a>
	{{template "document_nav" .}}
	<main id="main" class="ga-auth" tabindex="-1">
		<h1>{{.Heading}}</h1>
		<p>{{.Detail}}</p>
		{{if .AccountKey}}<div class="ga-auth__key-card" role="region">
			{{if .KeyInstruction}}<p class="ga-auth__key-instruction">{{.KeyInstruction}}</p>{{end}}
			<div class="ga-auth__key-row">
				<code class="ga-auth__key-code" id="ga-account-key">{{.AccountKey}}</code>
				<button type="button" class="ga-auth__key-copy" data-ga-copy-key="{{.AccountKey}}">{{.CopyLabel}}</button>
			</div>
			{{if .KeyWarning}}<p class="ga-auth__key-warning" role="note">{{.KeyWarning}}</p>{{end}}
		</div>{{end}}
		{{range .Actions}}<p><a href="{{.Href}}">{{.Label}}</a></p>
		{{end}}
	</main>
	{{template "document_footer" .}}
</body>
</html>{{end}}
`
