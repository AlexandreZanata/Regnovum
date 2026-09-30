package localesource_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/localesource"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

const accountID = "018f6b2a-0000-7000-8000-000000000070"

type stubReader struct {
	locales map[domain.AccountID]domain.Locale
	err     error
	calls   int
}

func (s *stubReader) PreferencesFor(_ context.Context, id domain.AccountID) (*application.CommunicationPreferences, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	found, ok := s.locales[id]
	if !ok {
		return nil, application.ErrProfileNotFound
	}
	return &application.CommunicationPreferences{
		AccountID:       id,
		InterfaceLocale: found,
	}, nil
}

func mustLocale(t *testing.T, raw string) domain.Locale {
	t.Helper()
	parsed, err := domain.ParseLocale(raw)
	if err != nil {
		t.Fatalf("parse locale %q: %v", raw, err)
	}
	return parsed
}

func authenticatedRequest(account domain.AccountID) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	identity := security.AuthIdentity{AccountID: account.String(), SessionID: "session"}
	return request.WithContext(security.WithAuth(request.Context(), identity))
}

func TestSourceResolvesAuthenticatedProfileLocale(t *testing.T) {
	reader := &stubReader{locales: map[domain.AccountID]domain.Locale{
		accountID: mustLocale(t, "en-US"),
	}}
	source := localesource.NewSource(reader)
	if source == nil {
		t.Fatal("NewSource() returned nil for a non-nil reader")
	}

	tag, ok := source(authenticatedRequest(accountID))
	if !ok {
		t.Fatal("source did not resolve the authenticated profile locale")
	}
	if string(tag) != domain.LocaleAmericanEnglish {
		t.Errorf("tag = %q, want en-US", tag)
	}
	if reader.calls != 1 {
		t.Errorf("reader calls = %d, want 1", reader.calls)
	}
}

func TestSourceIsSilentWithoutUsableSignal(t *testing.T) {
	t.Parallel()

	reader := &stubReader{locales: map[domain.AccountID]domain.Locale{}}
	source := localesource.NewSource(reader)

	t.Run("anonymous request", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		if _, ok := source(request); ok {
			t.Fatal("anonymous request must not resolve a profile locale")
		}
	})

	t.Run("missing profile", func(t *testing.T) {
		if _, ok := source(authenticatedRequest("018f6b2a-0000-7000-8000-000000000071")); ok {
			t.Fatal("missing profile must not resolve a locale")
		}
	})

	t.Run("storage failure", func(t *testing.T) {
		failing := &stubReader{err: errors.New("storage unavailable")}
		failingSource := localesource.NewSource(failing)
		if _, ok := failingSource(authenticatedRequest(accountID)); ok {
			t.Fatal("storage failure must not resolve a locale")
		}
	})

	t.Run("unset stored locale", func(t *testing.T) {
		empty := &stubReader{locales: map[domain.AccountID]domain.Locale{
			accountID: domain.Locale{},
		}}
		emptySource := localesource.NewSource(empty)
		if _, ok := emptySource(authenticatedRequest(accountID)); ok {
			t.Fatal("an unset stored locale must not resolve")
		}
	})

	t.Run("nil reader", func(t *testing.T) {
		if localesource.NewSource(nil) != nil {
			t.Fatal("NewSource(nil) must yield a nil source")
		}
	})
}

// TestResolverPrecedenceWithProfileSource drives the full platform
// precedence chain (I18N_STANDARD.md §4) with the real resolver: the
// authenticated profile preference beats the visitor cookie, malformed or
// unknown cookies are ignored instead of reflected, and switching the stored
// locale between pt-BR and en-US changes the resolution.
func TestResolverPrecedenceWithProfileSource(t *testing.T) {
	t.Parallel()

	reader := &stubReader{locales: map[domain.AccountID]domain.Locale{
		accountID: mustLocale(t, "en-US"),
	}}
	resolver := locale.NewResolver(locale.WithProfileSource(localesource.NewSource(reader)))

	t.Run("profile beats cookie and Accept-Language", func(t *testing.T) {
		request := authenticatedRequest(accountID)
		request.AddCookie(&http.Cookie{Name: locale.CookieName, Value: "pt-BR"})
		request.Header.Set("Accept-Language", "pt-BR")
		if got := resolver.Resolve(request); string(got) != "en-US" {
			t.Errorf("Resolve() = %q, want en-US (profile beats cookie)", got)
		}
	})

	t.Run("profile preference switch changes resolution pt/en", func(t *testing.T) {
		reader.locales[accountID] = mustLocale(t, "pt-BR")
		if got := resolver.Resolve(authenticatedRequest(accountID)); string(got) != "pt-BR" {
			t.Errorf("Resolve() = %q, want pt-BR", got)
		}
		reader.locales[accountID] = mustLocale(t, "en-US")
		if got := resolver.Resolve(authenticatedRequest(accountID)); string(got) != "en-US" {
			t.Errorf("Resolve() = %q, want en-US", got)
		}
	})

	t.Run("anonymous visitor cookie still resolves", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(&http.Cookie{Name: locale.CookieName, Value: "en-US"})
		if got := resolver.Resolve(request); string(got) != "en-US" {
			t.Errorf("Resolve() = %q, want en-US from cookie", got)
		}
	})

	t.Run("tampered cookie falls through and is never reflected", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(&http.Cookie{Name: locale.CookieName, Value: "<script>alert(1)</script>"})
		request.Header.Set("Accept-Language", "en-US")
		if got := resolver.Resolve(request); string(got) != "en-US" {
			t.Errorf("Resolve() = %q, want en-US (tampered cookie ignored)", got)
		}
	})

	t.Run("unknown cookie locale falls through to the default", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(&http.Cookie{Name: locale.CookieName, Value: "fr-FR"})
		if got := resolver.Resolve(request); string(got) != "pt-BR" {
			t.Errorf("Resolve() = %q, want the pt-BR default (unknown never reflected)", got)
		}
	})

	t.Run("misbehaving profile source never blocks the chain", func(t *testing.T) {
		failing := &stubReader{err: errors.New("storage unavailable")}
		failingResolver := locale.NewResolver(locale.WithProfileSource(localesource.NewSource(failing)))
		request := authenticatedRequest(accountID)
		request.AddCookie(&http.Cookie{Name: locale.CookieName, Value: "en-US"})
		if got := failingResolver.Resolve(request); string(got) != "en-US" {
			t.Errorf("Resolve() = %q, want en-US from cookie after profile failure", got)
		}
	})
}
