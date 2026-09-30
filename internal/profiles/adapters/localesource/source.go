// Package localesource bridges the persisted profile interface locale to the
// presentation-side locale negotiation (P05-T06, I18N_STANDARD.md §4,
// precedence 2: authenticated profile preference). It is a read-only
// adapter: resolution never mutates the profile, the visitor cookie or any
// Arena content language.
package localesource

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// NewSource builds the locale.ProfileSource hook for the platform resolver.
// It resolves the authenticated account's stored interface locale; anonymous
// requests, storage failures and non-allowlisted stored values all report
// "no signal" so the resolver falls through to the visitor cookie,
// Accept-Language or the configured default. A nil reader yields a nil
// source (the hook is absent).
func NewSource(preferences application.CommunicationPreferencesReader) locale.ProfileSource {
	if preferences == nil {
		return nil
	}

	return func(request *http.Request) (locale.Tag, bool) {
		identity, ok := security.FromContext(request.Context())
		if !ok {
			return "", false
		}

		resolved, err := preferences.PreferencesFor(request.Context(), domain.AccountID(identity.AccountID))
		if err != nil || resolved == nil {
			return "", false
		}

		tag, err := locale.ParseBCP47(resolved.InterfaceLocale.String())
		if err != nil || !locale.IsSupported(tag) {
			return "", false
		}
		return tag, true
	}
}
