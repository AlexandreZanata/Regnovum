package http

import (
	"html/template"
	"io"

	"github.com/AlexandreZanata/Regnovum/internal/platform/websurface"
)

// transparencyTemplate renders the public transparency document. Metric
// rows carry stable snake_case codes with integer counts only; all prose
// arrives pre-localized through the document fields, so the template
// itself holds no interface strings.
const transparencyTemplate = `<!DOCTYPE html>
<html lang="{{.Lang}}" dir="{{dir .Lang}}">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>{{.PageTitle}}</title>
</head>
<body>
<main>
<h1>{{.Heading}}</h1>
<p>{{.Period}}</p>
<p>{{.Updated}}</p>
<p>{{.Methodology}}</p>
<table>
<thead><tr><th>{{.MetricHeader}}</th><th>{{.ValueHeader}}</th></tr></thead>
<tbody>
{{range .Rows}}<tr><td>{{.Code}}</td><td>{{.Value}}</td></tr>
{{end}}</tbody>
</table>
</main>
</body>
</html>
`

// TransparencyDocument is the localized public transparency document.
type TransparencyDocument struct {
	Lang         string
	PageTitle    string
	Heading      string
	Period       string
	Updated      string
	Methodology  string
	MetricHeader string
	ValueHeader  string
	Rows         []TransparencyRow
}

// TransparencyRow is one stable metric code with its suppressed integer.
type TransparencyRow struct {
	Code  string
	Value int64
}

// Templates renders transparency documents.
type Templates struct {
	document *template.Template
}

// NewTemplates parses the transparency templates.
func NewTemplates() (*Templates, error) {
	document, err := template.New("transparency").Funcs(websurface.Funcs()).Parse(transparencyTemplate)
	if err != nil {
		return nil, err
	}
	return &Templates{document: document}, nil
}

// RenderDocument renders the transparency document.
func (t *Templates) RenderDocument(writer io.Writer, document TransparencyDocument) error {
	return t.document.Execute(writer, document)
}
