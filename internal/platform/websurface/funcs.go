package websurface

import (
	"html/template"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

// Funcs are the template functions every served document shares (P18-T10).
//
// `dir` is a function and not a field of the template data because the
// direction of a document is a property of the locale the document already
// declares: `lang="{{.Lang}}" dir="{{dir .Lang}}"` cannot disagree with itself,
// while two fields named Lang and Dir are two places to keep in step, and the
// second one is exactly the kind of value a new page forgets to set. The
// attribute is therefore computed from the same expression the head already
// carries.
//
// The map lives here rather than in internal/i18n so that the catalog package
// keeps knowing nothing about html/template, and here rather than in each
// adapter so that a document served by email and a document served to a
// browser answer the direction question the same way.
func Funcs() template.FuncMap {
	return template.FuncMap{
		"dir": i18n.Direction,
	}
}

// WithFuncs composes the shared template functions with the ones a surface
// already declares. The functions of the surface win on a name collision: a
// document that needs a different `dir` says so in the set it parses, where
// the exception is visible, instead of being silently overridden.
func WithFuncs(declared template.FuncMap) template.FuncMap {
	merged := make(template.FuncMap, len(declared)+len(Funcs()))
	for name, function := range Funcs() {
		merged[name] = function
	}
	for name, function := range declared {
		merged[name] = function
	}
	return merged
}
