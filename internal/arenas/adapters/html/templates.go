// Package html is the inbound HTML adapter of the arenas module (P08-T08):
// it renders the cacheable public Arena document served at /d/{slug} for
// search engines and link previews. Metadata never includes aggregated
// results (none exist in the MVP) and every dynamic value flows through
// html/template escaping or through JSON produced by encoding/json with
// HTML escaping enabled.
package html

import (
	"html/template"
	"io"

	"github.com/AlexandreZanata/Regnovum/internal/platform/websurface"
)

// documentTemplateSrc renders the public Arena document. Void elements are
// self-closed so the output is well-formed XML as well as valid HTML5,
// which lets the tests parse it with the standard library. The JSON-LD
// payload arrives as template.JS produced by encoding/json, never as raw
// user content.
const documentTemplateSrc = `<!DOCTYPE html>
<html lang="{{.Lang}}" dir="{{dir .Lang}}">
<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width, initial-scale=1" />
	<title>{{.PageTitle}}</title>
	<meta name="description" content="{{.Description}}" />
	<link rel="canonical" href="{{.Canonical}}" />
	<meta property="og:type" content="article" />
	<meta property="og:title" content="{{.Statement}}" />
	<meta property="og:description" content="{{.Description}}" />
	<meta property="og:url" content="{{.Canonical}}" />
	<meta property="og:locale" content="{{.OGLocale}}" />
	<meta property="og:site_name" content="Regnovum" />
	<script type="application/ld+json">{{.JSONLD}}</script>
</head>
<body>
	<main>
		<h1>{{.Statement}}</h1>
		<p>{{.StatusLabel}}</p>
		<p><time datetime="{{.PublishedAt}}">{{.PublishedAt}}</time></p>
		{{if .Context}}<p>{{.Context}}</p>{{end}}
	</main>
</body>
</html>`

// errorTemplateSrc renders the not-found and gone documents. Error pages
// never echo the requested slug and are never cacheable.
const errorTemplateSrc = `<!DOCTYPE html>
<html lang="{{.Lang}}" dir="{{dir .Lang}}">
<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width, initial-scale=1" />
	<title>{{.PageTitle}}</title>
	<meta name="robots" content="noindex" />
</head>
<body>
	<main>
		<h1>{{.Title}}</h1>
		<p>{{.Detail}}</p>
	</main>
</body>
</html>`

// DocumentData is the presentation data of one public Arena document.
type DocumentData struct {
	// Lang is the Arena content language, which is also the interface
	// locale of the document (I18N standard: the Arena uses its
	// content_language in metadata and in the main content).
	Lang string
	// PageTitle is the localized "{subject} — Regnovum" pattern.
	PageTitle string
	// Statement and Context are user content, escaped by html/template.
	Statement string
	Context   string
	// Description is the context when present, otherwise the statement.
	Description string
	// Canonical is the stable slug-based address of the document.
	Canonical string
	// OGLocale is the OpenGraph form of Lang (pt_BR, en_US).
	OGLocale string
	// StatusLabel is the localized public status.
	StatusLabel string
	// PublishedAt is the RFC 3339 UTC publication instant.
	PublishedAt string
	// JSONLD is the structured data document, already JSON-encoded with
	// HTML escaping by encoding/json.
	JSONLD template.JS
}

// ErrorData is the presentation data of the not-found and gone documents.
type ErrorData struct {
	Lang      string
	PageTitle string
	Title     string
	Detail    string
}

// Templates owns the compiled documents of the public Arena surface.
type Templates struct {
	document *template.Template
	errorDoc *template.Template
}

// NewTemplates parses and compiles the arena documents.
func NewTemplates() *Templates {
	return &Templates{
		document: template.Must(template.New("arenaDocument").Funcs(websurface.Funcs()).Parse(documentTemplateSrc)),
		errorDoc: template.Must(template.New("arenaError").Funcs(websurface.Funcs()).Parse(errorTemplateSrc)),
	}
}

// RenderDocument renders the public Arena document.
func (t *Templates) RenderDocument(w io.Writer, data DocumentData) error {
	return t.document.Execute(w, data)
}

// RenderError renders the not-found or gone document.
func (t *Templates) RenderError(w io.Writer, data ErrorData) error {
	return t.errorDoc.Execute(w, data)
}
