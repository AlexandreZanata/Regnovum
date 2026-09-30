package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

func validBody() domain.Body {
	return domain.Body{Subject: "Confirme seu email", Text: "texto", HTML: "<p>texto</p>"}
}

func TestNewMessageValidatesTheRecipient(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		recipient string
		wantOK    bool
	}{
		{"simple", "ana@example.com", true},
		{"tag and plus", "ana.silva+arena@mail.example.co.uk", true},
		{"hyphenated host", "ana@mail-1.example.com", true},
		{"padded", "  ana@example.com  ", true},
		{"empty", "", false},
		{"no at", "ana.example.com", false},
		{"two at", "ana@@example.com", false},
		{"empty local", "@example.com", false},
		{"empty host", "ana@", false},
		{"host without a dot", "ana@localhost", false},
		{"host with a trailing dot", "ana@example.com.", false},
		{"leading dot in local", ".ana@example.com", false},
		{"double dot in local", "an..a@example.com", false},
		{"display name form", "Ana <ana@example.com>", false},
		{"space inside", "ana silva@example.com", false},
		{"line break", "ana@example.com\nBcc: x@example.com", false},
		{"nul byte", "ana\x00@example.com", false},
		{"oversized", strings.Repeat("a", 250) + "@example.com", false},
		{"host label with an underscore", "ana@exa_mple.com", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			message, err := domain.NewMessage(testCase.recipient, domain.LocaleDefault, domain.TemplateVerification, validBody(), "email:1")
			if testCase.wantOK {
				if err != nil {
					t.Fatalf("NewMessage() error = %v, want nil", err)
				}
				if message.Recipient() != strings.TrimSpace(testCase.recipient) {
					t.Errorf("Recipient() = %q, want %q", message.Recipient(), strings.TrimSpace(testCase.recipient))
				}
				return
			}
			if !errors.Is(err, domain.ErrInvalidRecipient) {
				t.Errorf("NewMessage() error = %v, want ErrInvalidRecipient", err)
			}
		})
	}
}

func TestMessageAccessorsRoundTrip(t *testing.T) {
	message, err := domain.NewMessage("ana@example.com", domain.LocaleAmericanEnglish, domain.TemplatePasswordReset, validBody(), "email:reset:1")
	if err != nil {
		t.Fatalf("NewMessage() error = %v", err)
	}
	if message.Locale() != domain.LocaleAmericanEnglish {
		t.Errorf("Locale() = %q", message.Locale())
	}
	if message.Template() != domain.TemplatePasswordReset {
		t.Errorf("Template() = %q", message.Template())
	}
	if message.IdempotencyKey() != "email:reset:1" {
		t.Errorf("IdempotencyKey() = %q", message.IdempotencyKey())
	}
	if message.Body() != validBody() {
		t.Errorf("Body() = %+v", message.Body())
	}
	if err := message.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}
}

func TestZeroMessageIsInvalid(t *testing.T) {
	if err := (domain.Message{}).Validate(); err == nil {
		t.Error("the zero Message must not validate")
	}
}

func TestParseLocaleIsExact(t *testing.T) {
	for _, testCase := range []struct {
		raw     string
		want    domain.Locale
		wantErr error
	}{
		{"pt-BR", domain.LocaleBrazilianPortuguese, nil},
		{"en-US", domain.LocaleAmericanEnglish, nil},
		{" en-US ", domain.LocaleAmericanEnglish, nil},
		{"pt-br", "", domain.ErrUnsupportedLocale},
		{"pt_BR", "", domain.ErrUnsupportedLocale},
		{"en", "", domain.ErrUnsupportedLocale},
		{"", "", domain.ErrUnsupportedLocale},
		{"fr-FR", "", domain.ErrUnsupportedLocale},
	} {
		got, err := domain.ParseLocale(testCase.raw)
		if !errors.Is(err, testCase.wantErr) {
			t.Errorf("ParseLocale(%q) error = %v, want %v", testCase.raw, err, testCase.wantErr)
			continue
		}
		if got != testCase.want {
			t.Errorf("ParseLocale(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
	if len(domain.Locales()) != 2 {
		t.Errorf("Locales() = %v, want the two shipped locales", domain.Locales())
	}
}

func TestTemplateSetIsClosed(t *testing.T) {
	if !domain.TemplateVerification.Valid() || !domain.TemplatePasswordReset.Valid() || !domain.TemplatePasswordChanged.Valid() {
		t.Error("a shipped template is not valid")
	}
	for _, unknown := range []domain.TemplateID{"marketing", "", "VERIFICATION", "verification "} {
		if unknown.Valid() {
			t.Errorf("TemplateID(%q).Valid() = true, want false", unknown)
		}
	}
	if len(domain.TemplateIDs()) != 3 {
		t.Errorf("TemplateIDs() = %v, want the closed set", domain.TemplateIDs())
	}
}

// TestCarriesCodeIsTheContractOfThePair pins which templates deliver a code.
// The list is stated here and not derived from CarriesCode, so the two have to
// agree by construction: a template that changes kind without changing this
// table fails, which is the review the change deserves.
func TestCarriesCodeIsTheContractOfThePair(t *testing.T) {
	for templateID, carries := range map[domain.TemplateID]bool{
		domain.TemplateVerification:    true,
		domain.TemplatePasswordReset:   true,
		domain.TemplatePasswordChanged: false,
	} {
		if got := templateID.CarriesCode(); got != carries {
			t.Errorf("%s.CarriesCode() = %v, want %v", templateID, got, carries)
		}
	}
	if domain.TemplateID("marketing").CarriesCode() {
		t.Error("an unknown template must not claim to carry a code")
	}
}

// TestValidateTemplateValuesRefusesTheWrongPair is the rule that keeps a secret
// from being dropped: a code-carrying template without a code and a notice that
// was handed one are both refused, and the notice's own values are what a
// notice accepts.
func TestValidateTemplateValuesRefusesTheWrongPair(t *testing.T) {
	if _, err := domain.ValidateTemplateValues(domain.TemplateVerification, "Ana", ""); !errors.Is(err, domain.ErrInvalidTemplateValue) {
		t.Errorf("a code-carrying template without a code: error = %v, want ErrInvalidTemplateValue", err)
	}
	if _, err := domain.ValidateTemplateValues(domain.TemplatePasswordChanged, "Ana", "K7QP-2M4Z-9RTX"); !errors.Is(err, domain.ErrInvalidTemplateValue) {
		t.Errorf("a notice handed a code: error = %v, want ErrInvalidTemplateValue", err)
	}
	values, err := domain.ValidateTemplateValues(domain.TemplatePasswordChanged, " Ana ", "")
	if err != nil {
		t.Fatalf("a notice with a name: error = %v, want nil", err)
	}
	if values.Name != "Ana" || values.Code != "" {
		t.Errorf("values = %+v, want the trimmed name and no code", values)
	}
	if _, err := domain.ValidateTemplateValues("marketing", "Ana", ""); !errors.Is(err, domain.ErrUnsupportedTemplate) {
		t.Errorf("an unknown template: error = %v, want ErrUnsupportedTemplate", err)
	}
	if _, err := domain.ValidateTemplateValues(domain.TemplatePasswordChanged, strings.Repeat("a", 81), ""); !errors.Is(err, domain.ErrInvalidTemplateValue) {
		t.Errorf("an oversized name: error = %v, want ErrInvalidTemplateValue", err)
	}
	if _, err := domain.ValidateTemplateValues(domain.TemplatePasswordChanged, "Ana\nBea", ""); !errors.Is(err, domain.ErrInvalidTemplateValue) {
		t.Errorf("a name with a control character: error = %v, want ErrInvalidTemplateValue", err)
	}
}

func TestNewBodyRefusesHeaderUnsafeContent(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		subject string
		text    string
		html    string
		wantErr bool
	}{
		{"valid", "Assunto", "texto", "<p>texto</p>", false},
		{"empty subject", "", "texto", "<p>texto</p>", true},
		{"subject with a line break", "Assunto\r\nBcc: x@example.com", "texto", "<p>texto</p>", true},
		{"subject with a bare newline", "Assunto\nBcc: x@example.com", "texto", "<p>texto</p>", true},
		{"subject with a control character", "Assunto\x07", "texto", "<p>texto</p>", true},
		{"oversized subject", strings.Repeat("a", 201), "texto", "<p>texto</p>", true},
		{"empty text", "Assunto", "", "<p>texto</p>", true},
		{"empty html", "Assunto", "texto", "", true},
		{"oversized text", "Assunto", strings.Repeat("a", 64*1024+1), "<p>texto</p>", true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := domain.NewBody(testCase.subject, testCase.text, testCase.html)
			if testCase.wantErr {
				if !errors.Is(err, domain.ErrInvalidBody) {
					t.Errorf("NewBody() error = %v, want ErrInvalidBody", err)
				}
				return
			}
			if err != nil {
				t.Errorf("NewBody() error = %v, want nil", err)
			}
		})
	}
}

func TestNewTemplateValuesBoundsTheValues(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		value   string
		code    string
		wantErr error
	}{
		{"valid", "Ana", "K7QP-2M4Z", nil},
		{"no display name", "", "K7QP-2M4Z", nil},
		{"code with a space", "Ana", "K7 QP", domain.ErrInvalidTemplateValue},
		{"code with a newline", "Ana", "K7QP\n2M4Z", domain.ErrInvalidTemplateValue},
		{"code with a tab", "Ana", "K7QP\t2M4Z", domain.ErrInvalidTemplateValue},
		{"empty code", "Ana", "", domain.ErrInvalidTemplateValue},
		{"oversized code", "Ana", strings.Repeat("a", 513), domain.ErrInvalidTemplateValue},
		{"name with a control character", "A\x1bna", "K7QP", domain.ErrInvalidTemplateValue},
		{"oversized name", strings.Repeat("a", 81), "K7QP", domain.ErrInvalidTemplateValue},
		{"markup is allowed and escaped downstream", "<script>x</script>", "K7QP", nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := domain.NewTemplateValues(testCase.value, testCase.code)
			if !errors.Is(err, testCase.wantErr) {
				t.Errorf("NewTemplateValues() error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

func TestNewMessageValidatesTheIdempotencyKey(t *testing.T) {
	for _, testCase := range []struct {
		name string
		key  string
		want bool
	}{
		{"uuid shape", "0f14e45f-ceea-467f-a3a9-1f2b3c4d5e6f", true},
		{"prefixed", "email:verification:8f14e45f", true},
		{"dot and underscore", "email.verification_1", true},
		{"empty", "", false},
		{"space", "email verification", false},
		{"slash", "email/verification", false},
		{"line break", "email\n:1", false},
		{"oversized", strings.Repeat("a", 201), false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := domain.NewMessage("ana@example.com", domain.LocaleDefault, domain.TemplateVerification, validBody(), testCase.key)
			if testCase.want && err != nil {
				t.Errorf("NewMessage() error = %v, want nil", err)
			}
			if !testCase.want && !errors.Is(err, domain.ErrInvalidIdempotencyKey) {
				t.Errorf("NewMessage() error = %v, want ErrInvalidIdempotencyKey", err)
			}
		})
	}
}

func TestNewMessageRefusesUnknownLocaleAndTemplate(t *testing.T) {
	if _, err := domain.NewMessage("ana@example.com", "fr-FR", domain.TemplateVerification, validBody(), "k"); !errors.Is(err, domain.ErrUnsupportedLocale) {
		t.Errorf("error = %v, want ErrUnsupportedLocale", err)
	}
	if _, err := domain.NewMessage("ana@example.com", domain.LocaleDefault, "marketing", validBody(), "k"); !errors.Is(err, domain.ErrUnsupportedTemplate) {
		t.Errorf("error = %v, want ErrUnsupportedTemplate", err)
	}
}
