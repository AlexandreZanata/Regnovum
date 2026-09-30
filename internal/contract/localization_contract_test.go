package contract_test

// P25-T10 — contratos internacionais de API e email.
//
// O teste prova o I18N_STANDARD no backend servido: negociação de locale
// por HTTP com eco canônico (nunca reflete o bruto), códigos de Problem
// Details estáveis entre locales, isolamento de cache por idioma,
// templates de email nos dois locales com escaping e retry idêntico,
// paridade total de chaves/placeholders pt-BR×en-US, pseudo-locale
// derivado sem perda, moeda em minor units + ISO e datas RFC 3339 UTC, e
// reason codes de moderação estáveis. A troca de locale de interface nunca
// altera conteúdo persistido nem o idioma do conteúdo (content_language).

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/renderer"
	notifydomain "github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
)

var placeholderPattern = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// flattenKeys achata um catálogo aninhado em chaves pontilhadas → mensagem.
func flattenKeys(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	flattened := map[string]string{}
	var walk func(prefix string, value any)
	walk = func(prefix string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				name := key
				if prefix != "" {
					name = prefix + "." + key
				}
				walk(name, typed[key])
			}
		case string:
			flattened[prefix] = typed
		default:
			t.Fatalf("%s: key %q carries a non-string", path, prefix)
		}
	}
	walk("", document)
	return flattened
}

// placeholderSet extrai os placeholders {nome} de uma mensagem, ordenados.
func placeholderSet(message string) []string {
	found := map[string]bool{}
	for _, match := range placeholderPattern.FindAllStringSubmatch(message, -1) {
		found[match[1]] = true
	}
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// catalogLocales lê os dois catálogos de um arquivo (pt-BR, en-US).
func catalogLocales(t *testing.T, name string) (map[string]string, map[string]string) {
	t.Helper()
	root := repoRoot(t)
	return flattenKeys(t, filepath.Join(root, "locales", "pt-BR", name)),
		flattenKeys(t, filepath.Join(root, "locales", "en-US", name))
}

// interfaceLocaleOf lê o eco canônico de locale de uma resposta.
func interfaceLocaleOf(t *testing.T, header http.Header) string {
	t.Helper()
	tag := header.Get(locale.InterfaceLocaleHeader)
	if tag != "pt-BR" && tag != "en-US" {
		t.Fatalf("X-Interface-Locale = %q, want an allowlisted tag", tag)
	}
	return tag
}

// problemCodeOf decodifica o código estável de um Problem Details.
func problemCodeOf(t *testing.T, context string, status int, body []byte) string {
	t.Helper()
	var document struct {
		Code   string `json:"code"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("%s: problem body is not JSON: %v (%s)", context, err, string(body))
	}
	if document.Code == "" {
		t.Fatalf("%s: problem carries no stable code (status %d): %s", context, status, string(body))
	}
	return document.Code
}

// TestLocalizationNegotiation prova a precedência observável: tag suportada
// ecoa canônica, desconhecida/ausente cai no default sem refletir o bruto.
func TestLocalizationNegotiation(t *testing.T) {
	t.Parallel()
	h := conformanceServer(t)

	cases := []struct {
		name   string
		accept string
		want   string
	}{
		{name: "brazilian portuguese", accept: "pt-BR", want: "pt-BR"},
		{name: "american english", accept: "en-US", want: "en-US"},
		{name: "english with quality", accept: "en-US, pt-BR;q=0.5", want: "en-US"},
		{name: "unsupported falls back", accept: "fr-FR", want: "pt-BR"},
		{name: "garbage falls back", accept: "xx-!!!-garbage", want: "pt-BR"},
		{name: "missing falls back", accept: "", want: "pt-BR"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var headers map[string]string
			if testCase.accept != "" {
				headers = map[string]string{"Accept-Language": testCase.accept}
			}
			status, header, _ := conformanceRequest(t, h, "GET", "/api/v1/arenas", "", "", headers)
			if status != 200 {
				t.Fatalf("GET /api/v1/arenas = %d, want 200", status)
			}
			if got := interfaceLocaleOf(t, header); got != testCase.want {
				t.Fatalf("locale = %q, want %q for Accept-Language %q", got, testCase.want, testCase.accept)
			}
		})
	}
}

// TestLocalizationProblemCodesStable prova que o cliente decide pelo código:
// a mesma falha sob pt-BR e en-US responde o mesmo code em problem+json,
// mesmo com título/detalhe localizados.
func TestLocalizationProblemCodesStable(t *testing.T) {
	t.Parallel()
	h := conformanceServer(t)

	failures := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "unauthenticated", method: "GET", path: "/api/v1/me/profile", body: ""},
		{name: "unknown arena", method: "GET", path: "/api/v1/arenas/no-such-arena", body: ""},
		{name: "invalid limit", method: "GET", path: "/api/v1/arenas?limit=abc", body: ""},
		{name: "invalid draft", method: "POST", path: "/api/v1/me/arena-drafts", body: `{}`},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			t.Parallel()
			codes := map[string]string{}
			for _, tag := range []string{"pt-BR", "en-US"} {
				status, header, body := conformanceRequest(t, h, failure.method, failure.path, conformanceOwnerToken, failure.body,
					map[string]string{"Accept-Language": tag})
				if status < 400 {
					t.Fatalf("%s %s = %d, want a failure", failure.method, failure.path, status)
				}
				if contentType := header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
					t.Fatalf("%s: Content-Type = %q, want application/problem+json", failure.name, contentType)
				}
				if got := interfaceLocaleOf(t, header); got != tag {
					t.Fatalf("%s: locale echo = %q, want %q", failure.name, got, tag)
				}
				codes[tag] = problemCodeOf(t, failure.name+"/"+tag, status, body)
			}
			if codes["pt-BR"] != codes["en-US"] {
				t.Fatalf("codes diverge by locale: pt-BR=%q en-US=%q (clients branch on code)", codes["pt-BR"], codes["en-US"])
			}
		})
	}
}

// TestLocalizationCacheIsolation prova que o cache não mistura idiomas: cada
// locale recebe seu documento com o eco correspondente e o documento
// público cacheável viaja com Vary, para que um intermediário particione.
// (A canonicalização da chave de vary — canônica em vez do header bruto —
// é a decisão aberta I18N-02; aqui vale a presença que impede a mistura.)
func TestLocalizationCacheIsolation(t *testing.T) {
	t.Parallel()
	h := conformanceServer(t)

	for _, path := range []string{"/api/v1/public/transparency", "/api/v1/arenas"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			bodies := map[string]string{}
			for _, tag := range []string{"pt-BR", "en-US"} {
				status, header, body := conformanceRequest(t, h, "GET", path, "", "",
					map[string]string{"Accept-Language": tag})
				if status != 200 {
					t.Fatalf("GET %s = %d, want 200", path, status)
				}
				if got := interfaceLocaleOf(t, header); got != tag {
					t.Fatalf("GET %s: locale echo = %q, want %q", path, got, tag)
				}
				if vary := header.Get("Vary"); path == "/api/v1/public/transparency" && vary == "" {
					t.Fatalf("GET %s: no Vary on a cacheable localized document", path)
				}
				bodies[tag] = string(body)
			}
			for tag, body := range bodies {
				assertNoSecrets(t, "localized "+path, []byte(body))
				_ = tag
			}
		})
	}
}

// TestLocalizationEmailTemplates renderiza todo template nos dois locales
// contra o catálogo real: zero chave ausente, lang correto, escaping de
// valores hostis e retry byte-idêntico (o locale congelado da mensagem).
func TestLocalizationEmailTemplates(t *testing.T) {
	t.Parallel()
	render, err := renderer.NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	fixtures := []struct {
		template notifydomain.TemplateID
		name     string
		code     string
	}{
		{notifydomain.TemplateVerification, "Ana", "K7QP-2M4Z-9RTX"},
		{notifydomain.TemplatePasswordReset, "Ana", "K7QP-2M4Z-9RTX"},
		{notifydomain.TemplatePasswordChanged, "Ana", ""},
	}
	for _, fixture := range fixtures {
		t.Run(string(fixture.template), func(t *testing.T) {
			t.Parallel()
			for _, tag := range []notifydomain.Locale{notifydomain.LocaleBrazilianPortuguese, notifydomain.LocaleAmericanEnglish} {
				values, err := notifydomain.ValidateTemplateValues(fixture.template, fixture.name, fixture.code)
				if err != nil {
					t.Fatalf("ValidateTemplateValues: %v", err)
				}
				first, err := render.Render(fixture.template, tag, values)
				if err != nil {
					t.Fatalf("render %s/%s: %v", fixture.template, tag, err)
				}
				if first.Subject == "" || first.Text == "" || first.HTML == "" {
					t.Fatalf("render %s/%s has an empty representation", fixture.template, tag)
				}
				if !strings.Contains(first.HTML, `<html lang="`+tag.String()+`"`) {
					t.Fatalf("render %s/%s does not declare its lang", fixture.template, tag)
				}
				second, err := render.Render(fixture.template, tag, values)
				if err != nil {
					t.Fatalf("render retry %s/%s: %v", fixture.template, tag, err)
				}
				if first.Subject != second.Subject || first.Text != second.Text || first.HTML != second.HTML {
					t.Fatal("retry rendered different bytes with the frozen locale")
				}
			}
		})
	}

	t.Run("hostile values are escaped", func(t *testing.T) {
		t.Parallel()
		values, err := notifydomain.NewTemplateValues("<script>alert(1)</script>", "K7QP-2M4Z-9RTX")
		if err != nil {
			t.Fatalf("NewTemplateValues: %v", err)
		}
		rendered, err := render.Render(notifydomain.TemplateVerification, notifydomain.LocaleBrazilianPortuguese, values)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if strings.Contains(rendered.HTML, "<script>") {
			t.Fatalf("HTML quotes the hostile name raw: %.200s", rendered.HTML)
		}
		if !strings.Contains(rendered.HTML, "&lt;script&gt;") {
			t.Fatalf("HTML lost the escaped name: %.200s", rendered.HTML)
		}
	})

	t.Run("unknown locale and template are refused", func(t *testing.T) {
		t.Parallel()
		values, err := notifydomain.NewTemplateValues("Ana", "K7QP-2M4Z-9RTX")
		if err != nil {
			t.Fatalf("NewTemplateValues: %v", err)
		}
		if _, err := render.Render(notifydomain.TemplateVerification, notifydomain.Locale("fr-FR"), values); err == nil {
			t.Fatal("render with unsupported locale was accepted")
		}
		if _, err := render.Render(notifydomain.TemplateID("unknown"), notifydomain.LocaleBrazilianPortuguese, values); err == nil {
			t.Fatal("render with unknown template was accepted")
		}
	})
}

// TestLocalizationCatalogParity prova o §9 no dado: todo arquivo tem o
// mesmo conjunto de chaves nos dois locales e os mesmos placeholders
// tipados por chave — zero ausente, extra ou divergente.
func TestLocalizationCatalogParity(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"arenas.json", "auth.json", "email.json", "errors.json", "transparency.json"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			portuguese, english := catalogLocales(t, name)
			for key := range portuguese {
				translation, ok := english[key]
				if !ok {
					t.Errorf("%s: key %q missing in en-US", name, key)
					continue
				}
				want, got := placeholderSet(portuguese[key]), placeholderSet(translation)
				if strings.Join(want, ",") != strings.Join(got, ",") {
					t.Errorf("%s: key %q placeholders diverge pt=%v en=%v", name, key, want, got)
				}
			}
			for key := range english {
				if _, ok := portuguese[key]; !ok {
					t.Errorf("%s: key %q missing in pt-BR", name, key)
				}
			}
		})
	}
}

// TestLocalizationPseudoLocale deriva o pseudo-locale do catálogo real e
// prova a cobertura: toda mensagem sai bracketada, maior que a origem e
// com os placeholders intactos (texto hardcoded não passa pela derivação).
func TestLocalizationPseudoLocale(t *testing.T) {
	t.Parallel()
	if !i18n.IsPseudoLocale(i18n.PseudoLocale) {
		t.Fatalf("IsPseudoLocale(%q) = false", i18n.PseudoLocale)
	}
	messages := flattenKeys(t, filepath.Join(repoRoot(t), "locales", "pt-BR", "email.json"))
	derived := i18n.DerivePseudo(messages)
	if len(derived) != len(messages) {
		t.Fatalf("derived %d messages from %d", len(derived), len(messages))
	}
	for key, source := range messages {
		pseudo, ok := derived[key]
		if !ok {
			t.Errorf("key %q has no pseudo message", key)
			continue
		}
		if !strings.HasPrefix(pseudo, "⟦") || !strings.HasSuffix(pseudo, "⟧") {
			t.Errorf("key %q is not bracketed: %q", key, pseudo)
		}
		if len(pseudo) <= len(source) {
			t.Errorf("key %q did not grow: %d vs %d", key, len(pseudo), len(source))
		}
		want, got := placeholderSet(source), placeholderSet(pseudo)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Errorf("key %q lost placeholders: %v vs %v", key, want, got)
		}
	}
}

// TestLocalizationMoneyAndDates prova o §5 no fio: minor units inteiras +
// ISO sem pré-formatação, e datas RFC 3339 em UTC.
func TestLocalizationMoneyAndDates(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	const email = "j10-money@arena.example.com"
	const password = "j10-correct-horse-1"
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	address, _ := identitydomain.ParseEmail(email)
	verifyToken, found := world.sender.LastTokenForEmail(address)
	if !found {
		t.Fatal("registration issued no verification email")
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/auth/verify?token="+verifyToken, "", "", nil)
	if status != 200 {
		t.Fatalf("verify = %d, want 200", status)
	}
	status, header, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	cookie := journeyCookie(t, header)

	status, _, raw := journeyCall(t, server, "POST", "/api/v1/me/billing/checkout", cookie, `{"market":"BR","product":"ink_10000","idempotency_key":"j10-buy-1"}`, nil)
	if status != 200 {
		t.Fatalf("checkout = %d, want 200 (%s)", status, string(raw))
	}
	var order map[string]any
	if err := json.Unmarshal(raw, &order); err != nil {
		t.Fatalf("decode checkout: %v", err)
	}
	amount, ok := order["amount_minor"].(float64)
	if !ok || amount != float64(int64(amount)) || int64(amount) != 2490 {
		t.Fatalf("amount_minor = %v, want the integer 2490", order["amount_minor"])
	}
	if order["currency"] != "BRL" {
		t.Fatalf("currency = %v, want the ISO code BRL", order["currency"])
	}

	arenaID := journeyArena(t, world.pool, email, "A arena j10 debate a tese com clareza", "j10-money-arena")
	for _, tag := range []string{"pt-BR", "en-US"} {
		status, _, exportRaw := journeyCall(t, server, "GET", "/api/v1/arenas/"+arenaID+"/export", "", "", map[string]string{"Accept-Language": tag})
		if status != 200 {
			t.Fatalf("export = %d, want 200", status)
		}
		var exported struct {
			Arena struct {
				Statement string `json:"statement"`
				Language  string `json:"language"`
			} `json:"arena"`
		}
		if err := json.Unmarshal(exportRaw, &exported); err != nil {
			t.Fatalf("decode export: %v", err)
		}
		if exported.Arena.Language != "pt-BR" || exported.Arena.Statement != "A arena j10 debate a tese com clareza" {
			t.Fatalf("locale %s changed the content: %+v", tag, exported.Arena)
		}
	}

	status, _, feedRaw := journeyCall(t, server, "GET", "/api/v1/arenas", "", "", map[string]string{"Accept-Language": "en-US"})
	if status != 200 {
		t.Fatalf("feed = %d, want 200", status)
	}
	var feed struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(feedRaw, &feed); err != nil {
		t.Fatalf("decode feed: %v", err)
	}
	seen := false
	for _, item := range feed.Items {
		published, _ := item["published_at"].(string)
		if published == "" {
			continue
		}
		instant, err := time.Parse(time.RFC3339, published)
		if err != nil {
			t.Fatalf("published_at = %q, want RFC 3339", published)
		}
		if instant.Location() != time.UTC {
			t.Fatalf("published_at = %q, want UTC", published)
		}
		seen = true
	}
	if !seen {
		t.Fatal("feed carries no timestamp to judge")
	}
}

// TestLocalizationModerationReasons prova reason codes estáveis: o motivo
// válido é aceito nos dois locales com o mesmo comportamento e o inválido
// recusa com o mesmo código — texto nunca decide.
func TestLocalizationModerationReasons(t *testing.T) {
	t.Parallel()
	h := conformanceServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var arenaID string
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
		SELECT id, 'A arena j10 sob revisão', 'technology', 'pt-BR', 'published', 'j10-moderation-arena', now()
		FROM app.accounts WHERE email = 't06-owner@arena.example.com'
		RETURNING id::text`).Scan(&arenaID); err != nil {
		t.Fatalf("seed arena: %v", err)
	}

	codes := map[string]string{}
	for _, tag := range []string{"pt-BR", "en-US"} {
		status, _, raw := conformanceRequest(t, h, "POST", "/api/v1/me/moderation/reports", conformanceOwnerToken,
			`{"target_type":"arena","target_id":"`+arenaID+`","reason":"spam"}`,
			map[string]string{"Accept-Language": tag})
		if status != 200 && status != 201 {
			t.Fatalf("report = %d, want 200 or 201 (%s)", status, string(raw))
		}
		status, _, raw = conformanceRequest(t, h, "POST", "/api/v1/me/moderation/reports", conformanceOwnerToken,
			`{"target_type":"arena","target_id":"`+arenaID+`","reason":"bogus"}`,
			map[string]string{"Accept-Language": tag})
		if status != 400 {
			t.Fatalf("report bogus reason = %d, want 400", status)
		}
		var document struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(raw, &document); err != nil || document.Code == "" {
			t.Fatalf("refusal carries no stable code: %s", string(raw))
		}
		codes[tag] = document.Code
	}
	if codes["pt-BR"] != codes["en-US"] {
		t.Fatalf("reason refusal codes diverge: pt-BR=%q en-US=%q", codes["pt-BR"], codes["en-US"])
	}
}
