//go:build pseudolocale

// Tagged half of the P29-T01 matrix: the build that carries the
// pseudo-locale negotiates it like any other allowlisted tag, through
// every signal, while the default build (negotiation_matrix_test.go)
// refuses it at every level. Both directions are asserted so a build
// that lost the tag — or gained it by accident — fails instead of
// shipping a locale nobody asked for.
package locale

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

func TestNegotiationMatrixPseudoLocaleResolvesWhenCarried(t *testing.T) {
	t.Parallel()

	if !i18n.PseudoEnabled {
		t.Fatal("PseudoEnabled is false in a build with the pseudolocale tag")
	}
	pseudo := Tag(i18n.PseudoLocale)
	if !IsSupported(pseudo) {
		t.Fatalf("%s must be supported in the tagged build", pseudo)
	}

	t.Run("header", func(t *testing.T) {
		t.Parallel()

		if tag, ok := Negotiate("es-ES, " + i18n.PseudoLocale); !ok || tag != pseudo {
			t.Errorf("Negotiate(pseudo header) = (%q, %v), want (%q, true)", tag, ok, pseudo)
		}
	})

	t.Run("cookie", func(t *testing.T) {
		t.Parallel()

		resolver := NewResolver()
		request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		request.AddCookie(&http.Cookie{Name: CookieName, Value: i18n.PseudoLocale})
		if got := resolver.Resolve(request); got != pseudo {
			t.Errorf("cookie pseudo resolved to %q, want %q", got, pseudo)
		}
	})

	t.Run("profile", func(t *testing.T) {
		t.Parallel()

		resolver := NewResolver(WithProfileSource(func(*http.Request) (Tag, bool) {
			return pseudo, true
		}))
		request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		if got := resolver.Resolve(request); got != pseudo {
			t.Errorf("profile pseudo resolved to %q, want %q", got, pseudo)
		}
	})

	t.Run("echo", func(t *testing.T) {
		t.Parallel()

		resolver := NewResolver()
		handler := SetHandler(resolver, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusOK)
		}))
		request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		request.Header.Set("Accept-Language", i18n.PseudoLocale)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if got := recorder.Header().Get(InterfaceLocaleHeader); got != i18n.PseudoLocale {
			t.Errorf("%s = %q, want %q", InterfaceLocaleHeader, got, i18n.PseudoLocale)
		}
	})
}
