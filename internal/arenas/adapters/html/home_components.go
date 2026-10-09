package html

import (
	"html/template"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

// homeComponentFuncs supplies typed contexts to reusable server components.
// No component receives trusted HTML or interpolates markup from public data.
func homeComponentFuncs() template.FuncMap {
	return template.FuncMap{
		"category": func(locale, value string) string {
			label, err := i18n.Message(locale, "server-home.categories."+value)
			if err != nil {
				return value
			}
			return label
		},
		"dictArena": func(page homeData, arena homeArena) any {
			return struct {
				Locale string
				Arena  homeArena
			}{page.Locale, arena}
		},
		"dictRole": func(page homeData, role homeRole) any {
			return struct {
				Locale string
				Role   homeRole
			}{page.Locale, role}
		},
		"quick": func(locale, href, icon, title, note string) any {
			return struct{ Locale, Href, Icon, Title, Note string }{locale, href, icon, title, note}
		},
	}
}
