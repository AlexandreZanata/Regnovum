package http

import (
	"html/template"
	"io"
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/websurface"
)

// The landing pages of the transactional links (P04-T08) are documents a person
// opens from an email: the confirmation of a verification link and the reset of
// a password. Until P18-T10 they were five hardcoded Portuguese sources with a
// hardcoded `lang="pt-BR"`, which is exactly the defect the localization gate
// hunts: a page a person reads, in a language nobody chose, that no catalog
// knows about.
//
// They are now one document and one catalog section. The document carries the
// locale of the request in `lang` and its direction in `dir` (both from the
// same expression, see websurface.Funcs), and every word of prose comes from
// `auth.landing.<section>` — the same catalog the rest of the account journey
// reads, through the same fallback rule, so an English request never opens a
// Portuguese page again.
//
// The markup stays deliberately small and standalone: these documents are
// opened from an email client, may render without the application's sheets and
// must not depend on the browser build. Their CSS is inline for that reason,
// and it uses logical or symmetric properties only, so a future right-to-left
// locale does not need a second stylesheet.
const landingTemplateSrc = `<!DOCTYPE html>
<html lang="{{.Lang}}" dir="{{dir .Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body { font-family: system-ui, -apple-system, sans-serif; margin: 2rem auto; max-width: 600px; padding: 0 1rem; line-height: 1.5; color: #111; }
.card { border: 1px solid #e0e0e0; border-radius: 8px; padding: 2rem; }
.card--failure { border-color: #fee2e2; background-color: #fef2f2; }
h1 { color: #15803d; font-size: 1.5rem; margin-top: 0; }
.card--failure h1 { color: #b91c1c; }
a.button, button { display: inline-block; background-color: #111; color: #fff; text-decoration: none; border: none; padding: 0.6rem 1.2rem; border-radius: 4px; font-weight: bold; margin-top: 1rem; font-size: 1rem; cursor: pointer; }
button:hover, a.button:hover { background-color: #333; }
.form-group { margin-bottom: 1.2rem; }
label { display: block; font-weight: 600; margin-bottom: 0.3rem; }
input[type="password"] { width: 100%; padding: 0.6rem; border: 1px solid #ccc; border-radius: 4px; box-sizing: border-box; font-size: 1rem; }
</style>
</head>
<body>
<div class="card{{if .Failure}} card--failure{{end}}">
<h1>{{.Heading}}</h1>
<p>{{.Detail}}</p>
{{if .Note}}<p>{{.Note}}</p>{{end}}
{{if .Form}}<form method="POST" action="{{.Form.Action}}">
<input type="hidden" name="token" value="{{.Form.Token}}">
<input type="hidden" name="csrf_token" value="{{.Form.CSRFToken}}">
<div class="form-group">
<label for="password">{{.Form.Label}}</label>
<input type="password" id="password" name="password" minlength="8" required autofocus>
</div>
<button type="submit">{{.Form.Submit}}</button>
</form>{{else}}<a href="{{.ActionHref}}" class="button">{{.ActionLabel}}</a>{{end}}
</div>
</body>
</html>`

// landingNamespace is the catalog section of the landing pages.
const landingNamespace = "auth.landing."

// LandingForm is the interactive part of the password reset document. The
// action, the hidden fields and the two localized labels travel together,
// because a form rendered without one of them is a form that cannot be
// submitted or cannot be read.
type LandingForm struct {
	// Action is the endpoint the document submits to.
	Action string
	// Token is the recovery token the message carried.
	Token string
	// CSRFToken is the double-submit token of the platform.
	CSRFToken string
	// Label is the localized label of the new password field.
	Label string
	// Submit is the localized label of the submit button.
	Submit string
}

// LandingData is the localized data of one landing document. Every string is a
// catalog message: the template holds no prose of its own.
type LandingData struct {
	// Lang is the interface locale, also the document's `lang` attribute.
	Lang string
	// Title is the localized browser title.
	Title string
	// Heading is the localized heading of the card.
	Heading string
	// Detail is the localized explanation of what happened.
	Detail string
	// Note is the localized note, empty when the section declares none.
	Note string
	// Failure selects the failure presentation of the card.
	Failure bool
	// ActionLabel and ActionHref are the single way out of a page without a
	// form.
	ActionLabel string
	ActionHref  string
	// Form is the interactive part, absent on the pages that only report.
	Form *LandingForm
}

// HTMLTemplates owns the compiled document of the landing pages.
type HTMLTemplates struct {
	document *template.Template
}

// NewHTMLTemplates parses and compiles the landing document.
func NewHTMLTemplates() *HTMLTemplates {
	return &HTMLTemplates{
		document: template.Must(template.New("landing").Funcs(websurface.Funcs()).Parse(landingTemplateSrc)),
	}
}

// message resolves one catalog message of a section in the request locale,
// with the fallback rule of the browser surfaces (I18N_STANDARD §8).
func (t *HTMLTemplates) message(r *http.Request, section, key string) (string, error) {
	return websurface.Localized(r, landingNamespace+section+"."+key, nil)
}

// PasswordResetFormData is the submitted material the reset document carries
// into the form.
type PasswordResetFormData struct {
	Token     string
	CSRFToken string
}

// RenderVerifySuccess renders the confirmation of a verified email.
func (t *HTMLTemplates) RenderVerifySuccess(w io.Writer, r *http.Request) error {
	title, err := t.message(r, "verify_success", "page_title")
	if err != nil {
		return err
	}
	heading, err := t.message(r, "verify_success", "heading")
	if err != nil {
		return err
	}
	detail, err := t.message(r, "verify_success", "detail")
	if err != nil {
		return err
	}
	note, err := t.message(r, "verify_success", "note")
	if err != nil {
		return err
	}
	action, err := t.message(r, "verify_success", "action")
	if err != nil {
		return err
	}
	return t.document.Execute(w, LandingData{
		Lang:        websurface.Locale(r),
		Title:       title,
		Heading:     heading,
		Detail:      detail,
		Note:        note,
		ActionLabel: action,
		ActionHref:  landingSignIn,
	})
}

// RenderVerifyError renders the refusal of an invalid or spent verification
// link.
func (t *HTMLTemplates) RenderVerifyError(w io.Writer, r *http.Request) error {
	title, err := t.message(r, "verify_error", "page_title")
	if err != nil {
		return err
	}
	heading, err := t.message(r, "verify_error", "heading")
	if err != nil {
		return err
	}
	detail, err := t.message(r, "verify_error", "detail")
	if err != nil {
		return err
	}
	note, err := t.message(r, "verify_error", "note")
	if err != nil {
		return err
	}
	action, err := t.message(r, "verify_error", "action")
	if err != nil {
		return err
	}
	return t.document.Execute(w, LandingData{
		Lang:        websurface.Locale(r),
		Title:       title,
		Heading:     heading,
		Detail:      detail,
		Note:        note,
		Failure:     true,
		ActionLabel: action,
		ActionHref:  landingSignIn,
	})
}

// RenderPasswordResetForm renders the interactive form that completes a
// recovery.
func (t *HTMLTemplates) RenderPasswordResetForm(w io.Writer, r *http.Request, data PasswordResetFormData) error {
	title, err := t.message(r, "reset_form", "page_title")
	if err != nil {
		return err
	}
	heading, err := t.message(r, "reset_form", "heading")
	if err != nil {
		return err
	}
	detail, err := t.message(r, "reset_form", "detail")
	if err != nil {
		return err
	}
	label, err := t.message(r, "reset_form", "field_label")
	if err != nil {
		return err
	}
	submit, err := t.message(r, "reset_form", "submit")
	if err != nil {
		return err
	}
	return t.document.Execute(w, LandingData{
		Lang:    websurface.Locale(r),
		Title:   title,
		Heading: heading,
		Detail:  detail,
		Form: &LandingForm{
			Action:    landingConfirmReset,
			Token:     data.Token,
			CSRFToken: data.CSRFToken,
			Label:     label,
			Submit:    submit,
		},
	})
}

// RenderPasswordResetSuccess renders the confirmation of a completed
// recovery.
func (t *HTMLTemplates) RenderPasswordResetSuccess(w io.Writer, r *http.Request) error {
	title, err := t.message(r, "reset_success", "page_title")
	if err != nil {
		return err
	}
	heading, err := t.message(r, "reset_success", "heading")
	if err != nil {
		return err
	}
	detail, err := t.message(r, "reset_success", "detail")
	if err != nil {
		return err
	}
	note, err := t.message(r, "reset_success", "note")
	if err != nil {
		return err
	}
	action, err := t.message(r, "reset_success", "action")
	if err != nil {
		return err
	}
	return t.document.Execute(w, LandingData{
		Lang:        websurface.Locale(r),
		Title:       title,
		Heading:     heading,
		Detail:      detail,
		Note:        note,
		ActionLabel: action,
		ActionHref:  landingSignIn,
	})
}

// RenderPasswordResetError renders the refusal of a recovery that could not be
// completed.
func (t *HTMLTemplates) RenderPasswordResetError(w io.Writer, r *http.Request) error {
	title, err := t.message(r, "reset_error", "page_title")
	if err != nil {
		return err
	}
	heading, err := t.message(r, "reset_error", "heading")
	if err != nil {
		return err
	}
	detail, err := t.message(r, "reset_error", "detail")
	if err != nil {
		return err
	}
	note, err := t.message(r, "reset_error", "note")
	if err != nil {
		return err
	}
	action, err := t.message(r, "reset_error", "action")
	if err != nil {
		return err
	}
	return t.document.Execute(w, LandingData{
		Lang:        websurface.Locale(r),
		Title:       title,
		Heading:     heading,
		Detail:      detail,
		Note:        note,
		Failure:     true,
		ActionLabel: action,
		ActionHref:  landingSignIn,
	})
}

// The two addresses the landing pages link to: the account journey, never the
// JSON API, so the way out of a document is a page a person can use.
const (
	landingSignIn       = "/login"
	landingConfirmReset = "/api/v1/auth/password-reset/confirm"
)
