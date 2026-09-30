// Tests of the landing pages of the transactional links (P18-T10). The five
// documents used to be hardcoded Portuguese sources; what these tests hold is
// the property that replaced them: every document declares the locale it was
// rendered in, and every document is rendered from the catalog in both
// locales.
package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
)

// landingDocument renders one landing page in one locale.
func landingDocument(t *testing.T, render func(w *strings.Builder, r *http.Request) error, tag locale.Tag) string {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	request = request.WithContext(locale.WithLocale(request.Context(), tag))

	var body strings.Builder
	if err := render(&body, request); err != nil {
		t.Fatalf("rendering in %s: %v", tag, err)
	}
	return body.String()
}

func TestLandingPagesRenderInBothLocales(t *testing.T) {
	t.Parallel()

	templates := NewHTMLTemplates()
	form := PasswordResetFormData{Token: "tok", CSRFToken: "csrf"}

	pages := []struct {
		name     string
		render   func(w *strings.Builder, r *http.Request) error
		portugue string
		english  string
	}{
		{
			name:     "verify_success",
			render:   func(w *strings.Builder, r *http.Request) error { return templates.RenderVerifySuccess(w, r) },
			portugue: "Email verificado com sucesso",
			english:  "Email verified successfully",
		},
		{
			name:     "verify_error",
			render:   func(w *strings.Builder, r *http.Request) error { return templates.RenderVerifyError(w, r) },
			portugue: "Link de verificação inválido ou expirado",
			english:  "Verification link invalid or expired",
		},
		{
			name:     "reset_form",
			render:   func(w *strings.Builder, r *http.Request) error { return templates.RenderPasswordResetForm(w, r, form) },
			portugue: "Nova Senha (mínimo 8 caracteres):",
			english:  "New password (at least 8 characters):",
		},
		{
			name:     "reset_success",
			render:   func(w *strings.Builder, r *http.Request) error { return templates.RenderPasswordResetSuccess(w, r) },
			portugue: "Senha redefinida com sucesso",
			english:  "Password reset successfully",
		},
		{
			name:     "reset_error",
			render:   func(w *strings.Builder, r *http.Request) error { return templates.RenderPasswordResetError(w, r) },
			portugue: "Não foi possível redefinir sua senha",
			english:  "Your password could not be reset",
		},
	}

	for _, page := range pages {
		page := page
		t.Run(page.name, func(t *testing.T) {
			t.Parallel()

			// The locale and its direction are declared by the document
			// itself, from the same expression: a page cannot claim one
			// language and paint it in another direction.
			for _, expected := range []struct {
				tag   locale.Tag
				dir   string
				mark  string
				other string
			}{
				{tag: locale.BrazilianPortuguese, dir: "ltr", mark: page.portugue, other: page.english},
				{tag: locale.AmericanEnglish, dir: "ltr", mark: page.english, other: page.portugue},
			} {
				document := landingDocument(t, page.render, expected.tag)
				if marker := `<html lang="` + expected.tag.String() + `" dir="` + expected.dir + `">`; !strings.Contains(document, marker) {
					t.Errorf("the %s document does not declare %q:\n%s", expected.tag, marker, document)
				}
				if !strings.Contains(document, expected.mark) {
					t.Errorf("the %s document does not render %q:\n%s", expected.tag, expected.mark, document)
				}
				if strings.Contains(document, expected.other) {
					t.Errorf("the %s document renders the other locale's copy:\n%s", expected.tag, document)
				}
				if strings.Contains(document, landingNamespace) {
					t.Errorf("the %s document leaked a raw catalog key:\n%s", expected.tag, document)
				}
			}
		})
	}
}

// TestPasswordResetFormCarriesTheSubmittedMaterial keeps the interactive half
// of the migration honest: localizing the labels must not drop the token, the
// CSRF double submit or the endpoint the form posts to.
func TestPasswordResetFormCarriesTheSubmittedMaterial(t *testing.T) {
	t.Parallel()

	document := landingDocument(t, func(w *strings.Builder, r *http.Request) error {
		return NewHTMLTemplates().RenderPasswordResetForm(w, r, PasswordResetFormData{Token: "tok-123", CSRFToken: "csrf-456"})
	}, locale.BrazilianPortuguese)

	for _, marker := range []string{
		`action="` + landingConfirmReset + `"`,
		`name="token" value="tok-123"`,
		`name="csrf_token" value="csrf-456"`,
		"minlength=\"8\"",
	} {
		if !strings.Contains(document, marker) {
			t.Errorf("the reset form does not carry %q:\n%s", marker, document)
		}
	}
}

// TestLandingPagesAreNotHardcoded is the counterpart of the catalog gate: the
// template the adapter compiles holds no prose of its own, so a sentence that
// never reached locales/ cannot be served by this surface.
func TestLandingPagesAreNotHardcoded(t *testing.T) {
	t.Parallel()

	body := landingTemplateSrc
	body = strings.TrimPrefix(body, `<!DOCTYPE html>`)

	// Every letter outside the head, the CSS and the actions would be a
	// hardcoded string; the only letters this template may contain are the
	// ones of its own attribute values and element names.
	for _, word := range []string{"Senha", "Email verificado", "password reset", "Reset", "verification"} {
		if strings.Contains(body, word) {
			t.Errorf("the landing template carries the hardcoded string %q", word)
		}
	}
}
