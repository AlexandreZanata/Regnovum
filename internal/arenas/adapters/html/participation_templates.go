package html

import (
	"html/template"
	"io"

	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/websurface"
)

// Logical names of the assets the participation page loads, as produced by
// cmd/assetgen from web/generated and web/src. The manifest resolves them to
// their address; nothing here guesses a filename.
const (
	participationScript   = "pages/arena.js"
	participationResetCSS = "styles/reset.css"
	participationTokenCSS = "styles/tokens.css"
	participationBaseCSS  = "styles/base.css"
	participationPrimCSS  = "styles/primitives.css"
	participationArenaCSS = "styles/arena.css"
)

// participationAssets is the exact set the page loads, in cascade order for the
// sheets.
var participationAssets = []string{
	participationScript,
	participationResetCSS,
	participationTokenCSS,
	participationBaseCSS,
	participationPrimCSS,
	participationArenaCSS,
}

// Field identifier suffixes. They are the contract between the server-rendered
// markup and the client primitives: web/src/components/primitives/model.ts
// derives the same ids (`<name>-control`, `<name>-hint`, `<name>-error`) and the
// same `#<name>-control` fragment for the error summary links. The duplication
// is asserted on both sides — the frontend suite proves the model's ids, the
// adapter suite proves the rendered ones — because the two toolchains share no
// generator.
const (
	controlIDSuffix = "-control"
	hintIDSuffix    = "-hint"
	errorIDSuffix   = "-error"
)

// ParticipationChrome is the head and header shared by the two documents of the
// Arena participation journey.
type ParticipationChrome struct {
	// Lang is the interface locale, also the document's `lang` attribute.
	Lang string
	// PageTitle is the localized browser title.
	PageTitle string
	// Brand is the localized product name rendered in the header.
	Brand string
	// NavLabel is the accessible name of the navigation landmark.
	NavLabel string
	// Nav lists the navigational links of the header.
	Nav []ParticipationLink
	// SignOut is the form that ends the session, present only for an
	// authenticated page. Signing out is a POST because it changes state:
	// the account journey renders the same form, and this page reuses the
	// route and the same double-submit token so a link here can never end a
	// session by accident.
	SignOut *SignOutData
}

// ParticipationLink is one navigational link of a page.
type ParticipationLink struct {
	Label string
	Href  string
}

// SignOutData is the one-button form that ends the session.
type SignOutData struct {
	Action string
	Field  string
	Token  string
	Label  string
}

// SummaryData is one link of the error summary. Target is the identifier the
// link moves to, without the `#`: the first control of the field that failed.
type SummaryData struct {
	Target  string
	Message string
}

// HiddenData is one hidden input of a form.
type HiddenData struct {
	Name  string
	Value string
}

// ChoiceData is one radio or checkbox of a form, with the identifiers the label
// and the primitives address.
type ChoiceData struct {
	Name      string
	Value     string
	Label     string
	ControlID string
	Checked   bool
}

// FormData is the part every submitted form of the page shares.
type FormData struct {
	Action       string
	Heading      string
	Intro        string
	Field        string
	Token        string
	SummaryTitle string
	Summary      []SummaryData
	// Error is the form-level message of a refusal that belongs to no single
	// field (an immutable initial position, a version conflict, a balance
	// that does not cover the publication).
	Error       string
	ErrorID     string
	Hidden      []HiddenData
	SubmitLabel string
	BusyLabel   string
}

// FieldGroupData is one radio or checkbox group: a fieldset with its legend, its
// optional hint and its optional error. Limit is the bound the group declares to
// the client module (the attribution selection); the application enforces the
// same bound, so the declaration is a courtesy and zero means none.
type FieldGroupData struct {
	Name        string
	Legend      string
	Hint        string
	Error       string
	ControlID   string
	HintID      string
	ErrorID     string
	DescribedBy string
	Limit       int
	Choices     []ChoiceData
}

// PositionFormData renders the confirm form and the change form: the same shape,
// one radio per position.
type PositionFormData struct {
	FormData
	Group FieldGroupData
}

// PublishFormData renders the argument form: a relation group and the content.
type PublishFormData struct {
	FormData
	Relations        FieldGroupData
	ContentName      string
	ContentLabel     string
	ContentHint      string
	ContentError     string
	ContentValue     string
	ContentID        string
	ContentHintID    string
	ContentErrorID   string
	ContentDescribed string
}

// AttributionFormData renders the attribution form of one position change.
type AttributionFormData struct {
	FormData
	Options FieldGroupData
}

// DistributionRow is one counted choice of the public aggregate.
type DistributionRow struct {
	Label string
	Count int64
}

// AggregateData is the revealed public aggregate. The counts are the ones the
// application derived: the view formats nothing and invents nothing, and a
// suppressed aggregate carries the note instead of the numbers.
type AggregateData struct {
	Heading        string
	TotalLabel     string
	CurrentHeading string
	InitialHeading string
	CheckedLabel   string
	SuppressedNote string
	Current        []DistributionRow
	Initial        []DistributionRow
}

// ArgumentData is one published argument of the list. ID is the identifier the
// attribution form sends back; it is not rendered in the list itself.
type ArgumentData struct {
	ID            string
	RelationLabel string
	Content       string
	CreatedAt     string
	RepliesLabel  string
	// OptionLabel is the localized label of the argument as an attribution
	// option (its relation and a bounded excerpt of its content).
	OptionLabel string
}

// RelationGroupData is the list of one relation.
type RelationGroupData struct {
	Heading   string
	Empty     string
	Arguments []ArgumentData
}

// AnonymousPositionData is the block a visitor without a session sees: the local
// choice, what it means and the two ways into the account journey.
type AnonymousPositionData struct {
	Text    string
	Heading string
	Hint    string
	Choices []ChoiceData
	Links   []ParticipationLink
}

// PositionStateData reports the stored position of the authenticated person.
type PositionStateData struct {
	Heading string
	Initial string
	Current string
}

// ParticipationPageData is everything the Arena participation page renders.
type ParticipationPageData struct {
	ParticipationChrome
	// ArenaID is the identifier the client module scopes the local choice to,
	// so two Arenas never share one stored position.
	ArenaID       string
	Statement     string
	Context       string
	StatusLabel   string
	CategoryLabel string
	LanguageLabel string
	// PublishedAt is the RFC 3339 instant the Arena was published, and
	// PublishedLabel the same instant inside its localized sentence.
	PublishedAt    string
	PublishedLabel string
	// Notices are the localized outcomes of the transitions that just
	// happened, resolved from a closed set of codes so nothing the query
	// string carries is ever rendered.
	Notices          []string
	AggregateHeading string
	RevealLabel      string
	RevealHref       string
	Aggregate        *AggregateData
	PositionHeading  string
	Anonymous        *AnonymousPositionData
	State            *PositionStateData
	Confirm          *PositionFormData
	Change           *PositionFormData
	Arguments        []RelationGroupData
	Publish          *PublishFormData
	Attribution      *AttributionFormData
}

// NoticeData is the outcome or refusal document of the journey.
type NoticeData struct {
	ParticipationChrome
	Heading string
	Detail  string
	Actions []ParticipationLink
}

// ParticipationTemplates owns the compiled documents of the Arena participation
// journey. It is a separate set from the public document templates because the
// two surfaces answer different questions: /d/{slug} is a cacheable document for
// search engines, this one is a page a person acts on.
type ParticipationTemplates struct {
	page   *template.Template
	notice *template.Template
}

// NewParticipationTemplates compiles the documents with the manifest's asset
// resolver. It fails when the manifest cannot resolve an asset the page loads,
// so a broken build fails at composition instead of rendering a page whose
// stylesheet or module 404s.
func NewParticipationTemplates(manifest assets.Manifest) (*ParticipationTemplates, error) {
	for _, name := range participationAssets {
		if _, err := manifest.URL(name); err != nil {
			return nil, err
		}
	}

	set, err := template.New("participation").Funcs(websurface.WithFuncs(manifest.TemplateFuncs())).Parse(participationTemplateSources)
	if err != nil {
		return nil, err
	}

	page, err := set.Clone()
	if err != nil {
		return nil, err
	}
	notice, err := set.Clone()
	if err != nil {
		return nil, err
	}
	return &ParticipationTemplates{
		page:   page.Lookup("participation_page"),
		notice: notice.Lookup("notice_page"),
	}, nil
}

// RenderPage renders the participation page.
func (t *ParticipationTemplates) RenderPage(writer io.Writer, data ParticipationPageData) error {
	return t.page.Execute(writer, data)
}

// RenderNotice renders the outcome or refusal document.
func (t *ParticipationTemplates) RenderNotice(writer io.Writer, data NoticeData) error {
	return t.notice.Execute(writer, data)
}

// participationTemplateSources is the whole set of documents of the journey.
//
// The markup keeps the rules the browser policy of this binary requires: no
// inline style, no inline script, no event handler attribute, and every external
// asset resolved through the manifest. Void elements are self-closed and every
// attribute carries a value, so the rendered pages are well-formed markup as
// well as valid HTML5 — which is what lets the browser-policy test parse and
// scan them with the standard library.
//
// The local choice is a group of buttons and not a form: it is not submitted
// anywhere. What it does is decided by web/src/pages/arena.ts, which keeps the
// pick in this browser only, and the two account links sit in the same block so
// a visitor without scripts still has a working way forward.
const participationTemplateSources = `{{define "document_head"}}<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width, initial-scale=1" />
	<title>{{.PageTitle}}</title>
	<meta name="robots" content="noindex" />
	<link rel="stylesheet" href="{{asset "styles/reset.css"}}" />
	<link rel="stylesheet" href="{{asset "styles/tokens.css"}}" />
	<link rel="stylesheet" href="{{asset "styles/base.css"}}" />
	<link rel="stylesheet" href="{{asset "styles/primitives.css"}}" />
	<link rel="stylesheet" href="{{asset "styles/arena.css"}}" />
	<script type="module" src="{{asset "pages/arena.js"}}"></script>
</head>{{end}}

{{define "document_header"}}<header class="ga-arena__header">
	<a class="ga-arena__brand" href="/">{{.Brand}}</a>
	<nav aria-label="{{.NavLabel}}">
		<ul class="ga-arena__nav">
			{{range .Nav}}<li><a href="{{.Href}}">{{.Label}}</a></li>
			{{end}}
		</ul>
	</nav>
	{{if .SignOut}}<form class="ga-arena__signout" method="post" action="{{.SignOut.Action}}">
		<input type="hidden" name="{{.SignOut.Field}}" value="{{.SignOut.Token}}" />
		<button type="submit">{{.SignOut.Label}}</button>
	</form>{{end}}
</header>{{end}}

{{define "error_summary"}}<ga-error-summary{{if not .Summary}} hidden="hidden"{{end}}>
	<h2>{{.SummaryTitle}}</h2>
	<ul>
		{{range .Summary}}<li><a href="#{{.Target}}">{{.Message}}</a></li>
		{{end}}
	</ul>
</ga-error-summary>{{end}}

{{define "hidden_fields"}}{{range .Hidden}}<input type="hidden" name="{{.Name}}" value="{{.Value}}" />
			{{end}}{{end}}

{{define "form_head"}}<h2>{{.Heading}}</h2>
		<p>{{.Intro}}</p>
		{{template "error_summary" .}}
		{{if .Error}}<p id="{{.ErrorID}}" role="alert">{{.Error}}</p>
		{{end}}<form method="post" action="{{.Action}}">
			<input type="hidden" name="{{.Field}}" value="{{.Token}}" />
			{{template "hidden_fields" .}}{{end}}

{{define "form_foot"}}<ga-busy label="{{.BusyLabel}}">
				<span data-ga-indicator="true" hidden="hidden" aria-hidden="true"></span>
				<span data-ga-status="true" hidden="hidden" role="status" aria-live="polite" aria-atomic="true"></span>
				<button type="submit">{{.SubmitLabel}}</button>
			</ga-busy>
		</form>{{end}}

{{define "choice_group"}}<fieldset id="{{.ControlID}}"{{if .DescribedBy}} aria-describedby="{{.DescribedBy}}"{{end}}{{if .Error}} aria-invalid="true"{{end}}>
			<legend>{{.Legend}}</legend>
			{{if .Hint}}<p id="{{.HintID}}">{{.Hint}}</p>
			{{end}}{{range .Choices}}<div class="ga-arena__choice">
				<input type="radio" id="{{.ControlID}}" name="{{.Name}}" value="{{.Value}}"{{if .Checked}} checked="checked"{{end}} />
				<label for="{{.ControlID}}">{{.Label}}</label>
			</div>
			{{end}}{{if .Error}}<p id="{{.ErrorID}}" role="alert">{{.Error}}</p>
			{{end}}</fieldset>{{end}}

{{define "checkbox_group"}}<fieldset id="{{.ControlID}}"{{if .DescribedBy}} aria-describedby="{{.DescribedBy}}"{{end}}{{if .Error}} aria-invalid="true"{{end}}{{if .Limit}} data-ga-attribution-group="true" data-ga-attribution-limit="{{.Limit}}"{{end}}>
			<legend>{{.Legend}}</legend>
			{{if .Hint}}<p id="{{.HintID}}">{{.Hint}}</p>
			{{end}}{{range .Choices}}<div class="ga-arena__choice">
				<input type="checkbox" id="{{.ControlID}}" name="{{.Name}}" value="{{.Value}}"{{if .Checked}} checked="checked"{{end}} />
				<label for="{{.ControlID}}">{{.Label}}</label>
			</div>
			{{end}}{{if .Error}}<p id="{{.ErrorID}}" role="alert">{{.Error}}</p>
			{{end}}</fieldset>{{end}}

{{define "position_form"}}<section class="ga-arena__section">
		{{template "form_head" .FormData}}
			{{template "choice_group" .Group}}
			{{template "form_foot" .FormData}}
	</section>{{end}}

{{define "aggregate"}}<section class="ga-arena__section">
		<h2>{{.Heading}}</h2>
		{{if .SuppressedNote}}<p>{{.SuppressedNote}}</p>
		{{end}}<p>{{.TotalLabel}}</p>
		<h3>{{.CurrentHeading}}</h3>
		<ul>
			{{range .Current}}<li>{{.Label}}: {{.Count}}</li>
			{{end}}
		</ul>
		<h3>{{.InitialHeading}}</h3>
		<ul>
			{{range .Initial}}<li>{{.Label}}: {{.Count}}</li>
			{{end}}
		</ul>
		<p>{{.CheckedLabel}}</p>
	</section>{{end}}

{{define "participation_page"}}<!DOCTYPE html>
<html lang="{{.Lang}}" dir="{{dir .Lang}}">
{{template "document_head" .}}
<body>
	{{template "document_header" .}}
	<main id="main" class="ga-arena">
		{{range .Notices}}<ga-toast severity="info"><p>{{.}}</p></ga-toast>
		{{end}}<h1>{{.Statement}}</h1>
		{{if .Context}}<p>{{.Context}}</p>
		{{end}}<p>{{.StatusLabel}}</p>
		<p>{{.CategoryLabel}}</p>
		<p>{{.LanguageLabel}}</p>
		{{if .PublishedAt}}<p><time datetime="{{.PublishedAt}}">{{.PublishedLabel}}</time></p>
		{{end}}

		<section class="ga-arena__section">
			<h2>{{.AggregateHeading}}</h2>
			{{if .Aggregate}}{{template "aggregate" .Aggregate}}
			{{else}}<p><a href="{{.RevealHref}}">{{.RevealLabel}}</a></p>
			{{end}}
		</section>

		<section class="ga-arena__section">
			<h2>{{.PositionHeading}}</h2>
			{{if .Anonymous}}<p>{{.Anonymous.Text}}</p>
				<h3>{{.Anonymous.Heading}}</h3>
				<p>{{.Anonymous.Hint}}</p>
				<div class="ga-arena__choices" data-ga-choice-group="true" data-ga-arena="{{.ArenaID}}">
					{{range .Anonymous.Choices}}<button type="button" data-ga-choice="{{.Value}}" aria-pressed="false">{{.Label}}</button>
					{{end}}
				</div>
				<ul>
					{{range .Anonymous.Links}}<li><a href="{{.Href}}">{{.Label}}</a></li>
					{{end}}
				</ul>
			{{end}}
			{{if .State}}<p>{{.State.Initial}}</p>
				<p>{{.State.Current}}</p>
			{{end}}
			{{if .Confirm}}{{template "position_form" .Confirm}}
			{{end}}
			{{if .Change}}{{template "position_form" .Change}}
			{{end}}
		</section>

		{{if .Attribution}}<section class="ga-arena__section">
			{{template "form_head" .Attribution.FormData}}
				{{template "checkbox_group" .Attribution.Options}}
				{{template "form_foot" .Attribution.FormData}}
		</section>
		{{end}}

		{{range .Arguments}}<section class="ga-arena__section">
			<h2>{{.Heading}}</h2>
			{{if .Arguments}}<ul class="ga-arena__arguments">
				{{range .Arguments}}<li>
					<p>{{.Content}}</p>
					<p>{{.RelationLabel}}</p>
					<p><time datetime="{{.CreatedAt}}">{{.CreatedAt}}</time></p>
					<p>{{.RepliesLabel}}</p>
				</li>
				{{end}}
			</ul>
			{{else}}<p>{{.Empty}}</p>
			{{end}}
		</section>
		{{end}}

		{{if .Publish}}<section class="ga-arena__section">
			{{template "form_head" .Publish.FormData}}
			{{template "choice_group" .Publish.Relations}}
			<ga-field name="{{.Publish.ContentName}}" label="{{.Publish.ContentLabel}}" hint="{{.Publish.ContentHint}}"{{if .Publish.ContentError}} error="{{.Publish.ContentError}}"{{end}} required="required">
				<label for="{{.Publish.ContentID}}">{{.Publish.ContentLabel}}</label>
				<textarea id="{{.Publish.ContentID}}" name="{{.Publish.ContentName}}" required="required" aria-required="true"{{if .Publish.ContentDescribed}} aria-describedby="{{.Publish.ContentDescribed}}"{{end}}{{if .Publish.ContentError}} aria-invalid="true"{{end}}>{{.Publish.ContentValue}}</textarea>
				<p id="{{.Publish.ContentHintID}}">{{.Publish.ContentHint}}</p>
				{{if .Publish.ContentError}}<p id="{{.Publish.ContentErrorID}}" role="alert">{{.Publish.ContentError}}</p>
				{{end}}
			</ga-field>
			{{template "form_foot" .Publish.FormData}}
		</section>
		{{end}}
	</main>
</body>
</html>{{end}}

{{define "notice_page"}}<!DOCTYPE html>
<html lang="{{.Lang}}" dir="{{dir .Lang}}">
{{template "document_head" .}}
<body>
	{{template "document_header" .}}
	<main id="main" class="ga-arena">
		<h1>{{.Heading}}</h1>
		<p>{{.Detail}}</p>
		<ul>
			{{range .Actions}}<li><a href="{{.Href}}">{{.Label}}</a></li>
			{{end}}
		</ul>
	</main>
</body>
</html>{{end}}
`
