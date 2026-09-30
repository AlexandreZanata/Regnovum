package html_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/billingpass"
	adapterhtml "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/html"
	arenaspg "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	billingpg "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// --- Harness -------------------------------------------------------------

type seoHarness struct {
	mux       *http.ServeMux
	pool      *pgxpool.Pool
	create    *arenasapp.CreateArenaDraftUseCase
	publish   *arenasapp.PublishArenaUseCase
	close     *arenasapp.CloseArenaUseCase
	remove    *arenasapp.RemoveArenaUseCase
	restrict  *arenasapp.RestrictArenaUseCase
	ownerID   string
	moderator string
	grants    int
}

type allowModerator struct{}

func (allowModerator) EnsureModerator(context.Context, domain.ModeratorID) error { return nil }

type recordingAudit struct{ events []arenasapp.ModerationEvent }

func (a *recordingAudit) RecordArenaModeration(_ context.Context, event arenasapp.ModerationEvent) error {
	a.events = append(a.events, event)
	return nil
}

func seoUUID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func mustSEOAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) string {
	t.Helper()
	account, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return seoUUID(account.ID)
}

func setupSEOHarness(t *testing.T) *seoHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	repo := arenaspg.NewRepository(pool)
	billingRepo := billingpg.NewRepository(pool)
	clock := clockseed.NewClock()

	publish := arenasapp.NewPublishArenaUseCase(repo, billingpass.New(billingRepo, clock), platformpg.NewTxManager(pool), clock)
	create := arenasapp.NewCreateArenaDraftUseCase(repo, domain.DefaultStatementPolicy())
	remove := arenasapp.NewRemoveArenaUseCase(repo, allowModerator{}, &recordingAudit{}, clock)
	restrict := arenasapp.NewRestrictArenaUseCase(repo, allowModerator{}, &recordingAudit{}, clock)

	handler := adapterhtml.NewHandler(adapterhtml.HandlerConfig{
		GetDocumentUseCase: arenasapp.NewGetArenaDocumentUseCase(repo),
		Templates:          adapterhtml.NewTemplates(),
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	return &seoHarness{
		mux:       mux,
		pool:      pool,
		create:    create,
		publish:   publish,
		close:     arenasapp.NewCloseArenaUseCase(repo),
		remove:    remove,
		restrict:  restrict,
		ownerID:   mustSEOAccount(t, ctx, q, "seo-owner@arena.example.com"),
		moderator: mustSEOAccount(t, ctx, q, "seo-moderator@arena.example.com"),
	}
}

func (h *seoHarness) grantPass(t *testing.T, ctx context.Context, repo *billingpg.Repository, accountID string) {
	t.Helper()
	quantity, err := billingdomain.NewQuantity(1)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	h.grants++
	reference, err := billingdomain.ParseReference(fmt.Sprintf("stripe:evt_seo_%d", h.grants))
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if _, err := repo.GrantPassLot(ctx, billingapp.GrantPassLotRequest{
		AccountID: billingdomain.AccountID(accountID),
		Origin:    billingdomain.OriginPurchase,
		Quantity:  quantity,
		Reference: reference,
		GrantedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("GrantPassLot: %v", err)
	}
}

func (h *seoHarness) publishArena(t *testing.T, statement, contextText, language, category string) *domain.Arena {
	t.Helper()
	ctx := context.Background()
	repo := billingpg.NewRepository(h.pool)
	h.grantPass(t, ctx, repo, h.ownerID)

	draft, err := h.create.Execute(ctx, arenasapp.CreateArenaDraftCommand{
		AccountID: h.ownerID,
		Statement: statement,
		Context:   contextText,
		Category:  category,
		Language:  language,
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	result, err := h.publish.Execute(ctx, arenasapp.PublishArenaCommand{
		AccountID: h.ownerID,
		ArenaID:   draft.ID().String(),
	})
	if err != nil {
		t.Fatalf("publish draft: %v", err)
	}
	return &result.Arena
}

func getDocument(t *testing.T, mux *http.ServeMux, path string, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if ifNoneMatch != "" {
		request.Header.Set("If-None-Match", ifNoneMatch)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

// --- Parsed document model (standard library XML parser) -----------------

type parsedDocument struct {
	XMLName xml.Name   `xml:"html"`
	Lang    string     `xml:"lang,attr"`
	Dir     string     `xml:"dir,attr"`
	Head    parsedHead `xml:"head"`
	Body    parsedBody `xml:"body"`
}

type parsedHead struct {
	Title   string         `xml:"title"`
	Metas   []parsedMeta   `xml:"meta"`
	Links   []parsedLink   `xml:"link"`
	Scripts []parsedScript `xml:"script"`
}

type parsedMeta struct {
	Name     string `xml:"name,attr"`
	Property string `xml:"property,attr"`
	Content  string `xml:"content,attr"`
}

type parsedLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

type parsedScript struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

type parsedBody struct {
	Main parsedMain `xml:"main"`
}

type parsedMain struct {
	Heading    string            `xml:"h1"`
	Paragraphs []parsedParagraph `xml:"p"`
}

type parsedParagraph struct {
	Text string      `xml:",chardata"`
	Time *parsedTime `xml:"time"`
}

type parsedTime struct {
	Datetime string `xml:"datetime,attr"`
	Text     string `xml:",chardata"`
}

func parseDocument(t *testing.T, body string) parsedDocument {
	t.Helper()
	var document parsedDocument
	if err := xml.Unmarshal([]byte(body), &document); err != nil {
		t.Fatalf("document is not parseable: %v\nbody: %s", err, body)
	}
	return document
}

func metaContent(t *testing.T, head parsedHead, name, property string) string {
	t.Helper()
	for _, meta := range head.Metas {
		if name != "" && meta.Name == name {
			return meta.Content
		}
		if property != "" && meta.Property == property {
			return meta.Content
		}
	}
	t.Fatalf("meta name=%q property=%q not found in %v", name, property, head.Metas)
	return ""
}

func structuredDataOf(t *testing.T, head parsedHead) map[string]any {
	t.Helper()
	for _, script := range head.Scripts {
		if script.Type != "application/ld+json" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(script.Text), &payload); err != nil {
			t.Fatalf("JSON-LD is not valid JSON: %v\ntext: %s", err, script.Text)
		}
		return payload
	}
	t.Fatal("document has no application/ld+json script")
	return nil
}

// --- Tests ---------------------------------------------------------------

func TestArenaDocumentRendersCacheableHTML(t *testing.T) {
	harness := setupSEOHarness(t)
	arena := harness.publishArena(t,
		"A AGI existirá até 2040",
		"Debate sobre prazos e evidências da inteligência geral.",
		"pt-BR", "technology")

	recorder := getDocument(t, harness.mux, "/d/"+arena.Slug().String(), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/html; charset=utf-8", contentType)
	}
	cacheControl := recorder.Header().Get("Cache-Control")
	if !strings.Contains(cacheControl, "public") || !strings.Contains(cacheControl, "max-age=60") {
		t.Fatalf("Cache-Control = %q, want public max-age=60", cacheControl)
	}
	if vary := recorder.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", vary)
	}
	etag := recorder.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("ETag = %q, want a strong quoted validator", etag)
	}

	body := recorder.Body.String()
	document := parseDocument(t, body)
	if document.Lang != "pt-BR" {
		t.Fatalf("html lang = %q, want the arena content language", document.Lang)
	}
	// The direction is declared from the same locale the document names
	// (P18-T10): a cacheable document served to search engines and to link
	// previews is read by clients that lay it out, and one without a
	// direction would break the first time a right-to-left Arena exists.
	if document.Dir != "ltr" {
		t.Fatalf("html dir = %q, want ltr for the content language", document.Dir)
	}

	statement := arena.Statement().String()
	contextText := arena.Context().String()
	canonical := "/d/" + arena.Slug().String()
	publishedAt := arena.PublishedAt().UTC().Format(time.RFC3339)

	if want := statement + " — Regnovum"; document.Head.Title != want {
		t.Fatalf("title = %q, want %q", document.Head.Title, want)
	}
	if got := metaContent(t, document.Head, "description", ""); got != contextText {
		t.Fatalf("description = %q, want the arena context", got)
	}
	if got := metaContent(t, document.Head, "", "og:type"); got != "article" {
		t.Fatalf("og:type = %q, want article", got)
	}
	if got := metaContent(t, document.Head, "", "og:title"); got != statement {
		t.Fatalf("og:title = %q, want the statement", got)
	}
	if got := metaContent(t, document.Head, "", "og:description"); got != contextText {
		t.Fatalf("og:description = %q, want the arena context", got)
	}
	if got := metaContent(t, document.Head, "", "og:url"); got != canonical {
		t.Fatalf("og:url = %q, want %q", got, canonical)
	}
	if got := metaContent(t, document.Head, "", "og:locale"); got != "pt_BR" {
		t.Fatalf("og:locale = %q, want pt_BR", got)
	}
	if got := metaContent(t, document.Head, "", "og:site_name"); got != "Regnovum" {
		t.Fatalf("og:site_name = %q, want Regnovum", got)
	}

	var canonicalHref string
	for _, link := range document.Head.Links {
		if link.Rel == "canonical" {
			canonicalHref = link.Href
		}
	}
	if canonicalHref != canonical {
		t.Fatalf("canonical = %q, want the stable slug address %q", canonicalHref, canonical)
	}

	// JSON-LD: exactly the Arena's own facts, no aggregated results.
	payload := structuredDataOf(t, document.Head)
	if len(payload) != 7 {
		t.Fatalf("JSON-LD keys = %v, want exactly the seven declared fields", payload)
	}
	if payload["@context"] != "https://schema.org" || payload["@type"] != "Article" {
		t.Fatalf("JSON-LD context/type = %v/%v", payload["@context"], payload["@type"])
	}
	if payload["headline"] != statement || payload["description"] != contextText {
		t.Fatalf("JSON-LD content = %v/%v, want the arena content", payload["headline"], payload["description"])
	}
	if payload["inLanguage"] != "pt-BR" || payload["datePublished"] != publishedAt || payload["url"] != canonical {
		t.Fatalf("JSON-LD facts = %v, want inLanguage/datePublished/url of the arena", payload)
	}
	for _, forbidden := range []string{"result", "aggregate", "score", "count", "participants"} {
		if _, ok := payload[forbidden]; ok {
			t.Fatalf("SECURITY/SEO VIOLATION: JSON-LD carries %q", forbidden)
		}
	}

	// Visible document.
	if document.Body.Main.Heading != statement {
		t.Fatalf("h1 = %q, want the statement", document.Body.Main.Heading)
	}
	if len(document.Body.Main.Paragraphs) != 3 {
		t.Fatalf("paragraphs = %v, want status, time and context", document.Body.Main.Paragraphs)
	}
	if got := strings.TrimSpace(document.Body.Main.Paragraphs[0].Text); got != "Publicada" {
		t.Fatalf("status label = %q, want Publicada", got)
	}
	timestamp := document.Body.Main.Paragraphs[1].Time
	if timestamp == nil || timestamp.Datetime != publishedAt {
		t.Fatalf("time = %v, want datetime %q", timestamp, publishedAt)
	}
	if got := strings.TrimSpace(document.Body.Main.Paragraphs[2].Text); got != contextText {
		t.Fatalf("context paragraph = %q, want the arena context", got)
	}

	// Deterministic rendering: same bytes, same validator.
	second := getDocument(t, harness.mux, "/d/"+arena.Slug().String(), "")
	if second.Body.String() != body {
		t.Fatal("document rendering is not deterministic")
	}
	if second.Header().Get("ETag") != etag {
		t.Fatalf("second ETag = %q, want %q", second.Header().Get("ETag"), etag)
	}

	// Conditional revalidation answers 304 without a body.
	conditional := getDocument(t, harness.mux, "/d/"+arena.Slug().String(), etag)
	if conditional.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304 (body: %s)", conditional.Code, conditional.Body.String())
	}
	if conditional.Body.Len() != 0 {
		t.Fatalf("conditional body = %q, want empty", conditional.Body.String())
	}
	if conditional.Header().Get("ETag") != etag {
		t.Fatalf("conditional ETag = %q, want %q", conditional.Header().Get("ETag"), etag)
	}
	if got := conditional.Header().Get("Cache-Control"); !strings.Contains(got, "public") {
		t.Fatalf("conditional Cache-Control = %q, want public", got)
	}
}

func TestArenaDocumentUsesContentLanguage(t *testing.T) {
	harness := setupSEOHarness(t)
	arena := harness.publishArena(t,
		"Fusion energy will be commercial by 2035",
		"",
		"en-US", "science")

	recorder := getDocument(t, harness.mux, "/d/"+arena.Slug().String(), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	document := parseDocument(t, recorder.Body.String())
	if document.Lang != "en-US" {
		t.Fatalf("html lang = %q, want en-US", document.Lang)
	}
	if document.Dir != "ltr" {
		t.Fatalf("html dir = %q, want ltr for the content language", document.Dir)
	}
	if got := metaContent(t, document.Head, "", "og:locale"); got != "en_US" {
		t.Fatalf("og:locale = %q, want en_US", got)
	}
	if got := strings.TrimSpace(document.Body.Main.Paragraphs[0].Text); got != "Published" {
		t.Fatalf("status label = %q, want Published", got)
	}
	payload := structuredDataOf(t, document.Head)
	if payload["inLanguage"] != "en-US" {
		t.Fatalf("JSON-LD inLanguage = %v, want en-US", payload["inLanguage"])
	}
	// Without context the statement is the description.
	if got := metaContent(t, document.Head, "description", ""); got != arena.Statement().String() {
		t.Fatalf("description = %q, want the statement fallback", got)
	}
}

// TestCacheableDocumentDoesNotVaryByRequestedLocale is the cache half of the
// internationalization audit (P20-T09).
//
// I18N_STANDARD.md §4 requires a public cache to vary only by a controlled
// locale dimension and never by the raw `Accept-Language` header. The only
// public surface of this adapter is the Arena document, and it is rendered in
// its *content* language: the header cannot reach it. That is a claim about
// bytes, so it is asserted in bytes — two requests for the same Arena, one
// asking for each interface locale, must produce the same body, the same
// validator and the same cache policy.
//
// The control is the other half, and it is what keeps the assertion from being
// a statement that the page never changes: the same request against an Arena
// whose content language is `en-US` renders different bytes, so the
// representation is a function of what the content is written in — which is
// exactly why `Vary: Accept-Encoding` alone is the right policy here, and why a
// surface that did negotiate the interface locale would have to declare a
// locale dimension of its own before it could be cached.
func TestCacheableDocumentDoesNotVaryByRequestedLocale(t *testing.T) {
	harness := setupSEOHarness(t)
	portuguese := harness.publishArena(t,
		"A AGI existirá até 2040",
		"Debate sobre prazos e evidências da inteligência geral.",
		"pt-BR", "technology")
	english := harness.publishArena(t,
		"AGI will exist by 2040",
		"A debate about the deadlines and the evidence of general intelligence.",
		"en-US", "technology")

	request := func(path, acceptLanguage string) *httptest.ResponseRecorder {
		recorded := httptest.NewRequest(http.MethodGet, path, nil)
		recorded.Header.Set("Accept-Language", acceptLanguage)
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, recorded)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s with Accept-Language %s: status = %d (body: %s)",
				path, acceptLanguage, recorder.Code, recorder.Body.String())
		}
		return recorder
	}

	document := "/d/" + portuguese.Slug().String()
	defaultLocale := request(document, "pt-BR")
	otherLocale := request(document, "en-US")

	if defaultLocale.Body.String() != otherLocale.Body.String() {
		t.Fatalf("the cacheable document changed with Accept-Language: a shared cache would answer pt-BR to an en-US reader")
	}
	if defaultLocale.Header().Get("ETag") != otherLocale.Header().Get("ETag") {
		t.Fatalf("ETag = %q and %q: the validator of a cacheable document may not depend on the header it does not vary by",
			defaultLocale.Header().Get("ETag"), otherLocale.Header().Get("ETag"))
	}
	for _, recorder := range []*httptest.ResponseRecorder{defaultLocale, otherLocale} {
		vary := recorder.Header().Get("Vary")
		if !strings.Contains(vary, "Accept-Encoding") {
			t.Fatalf("Vary = %q, want Accept-Encoding", vary)
		}
		if strings.Contains(vary, "Accept-Language") {
			t.Fatalf("Vary = %q: the raw Accept-Language header must never select a cached representation", vary)
		}
	}

	// The control: the document does change with the language of its content.
	otherContent := request("/d/"+english.Slug().String(), "pt-BR")
	if otherContent.Body.String() == defaultLocale.Body.String() {
		t.Fatalf("two Arenas of different content languages rendered the same document: the equality above would prove nothing")
	}
	if !strings.Contains(otherContent.Body.String(), `lang="en-US"`) {
		t.Fatalf("the en-US Arena does not declare its content language")
	}
}

func TestArenaDocumentEscapesMaliciousContent(t *testing.T) {
	harness := setupSEOHarness(t)
	statement := `"><script>alert(1)</script><img src=x onerror=alert(2)>`
	contextText := `</script><svg onload=alert(3)>&"<>`
	arena := harness.publishArena(t, statement, contextText, "pt-BR", "technology")

	recorder := getDocument(t, harness.mux, "/d/"+arena.Slug().String(), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()

	for _, rawPayload := range []string{"<script>alert(1)", "<img src=x", "<svg onload"} {
		if strings.Contains(body, rawPayload) {
			t.Fatalf("SECURITY VIOLATION: unescaped payload %q in body: %s", rawPayload, body)
		}
	}
	if strings.Count(body, "</script>") != 1 {
		t.Fatalf("SECURITY VIOLATION: script element was not contained: %s", body)
	}

	document := parseDocument(t, body)
	if want := statement + " — Regnovum"; document.Head.Title != want {
		t.Fatalf("title = %q, want the escaped statement round-tripped", document.Head.Title)
	}
	if document.Body.Main.Heading != statement {
		t.Fatalf("h1 = %q, want the escaped statement round-tripped", document.Body.Main.Heading)
	}
	if got := metaContent(t, document.Head, "description", ""); got != contextText {
		t.Fatalf("description = %q, want the escaped context round-tripped", got)
	}

	payload := structuredDataOf(t, document.Head)
	if payload["headline"] != statement || payload["description"] != contextText {
		t.Fatalf("JSON-LD payload = %v/%v, want the raw content", payload["headline"], payload["description"])
	}
	for _, script := range document.Head.Scripts {
		if strings.Contains(script.Text, "</script") {
			t.Fatalf("SECURITY VIOLATION: JSON-LD can close its script element: %s", script.Text)
		}
	}
}

func TestArenaDocumentStatuses(t *testing.T) {
	harness := setupSEOHarness(t)

	// Removed Arenas answer 410 and never leak their content.
	removed := harness.publishArena(t, "A AGI existirá até 2040", "Contexto removido.", "pt-BR", "technology")
	if _, err := harness.remove.Execute(context.Background(), arenasapp.ModerateArenaCommand{
		ActorAccountID: harness.moderator,
		ArenaID:        removed.ID().String(),
		Reason:         "remoção por violação das regras do debate",
	}); err != nil {
		t.Fatalf("remove arena: %v", err)
	}

	recorder := getDocument(t, harness.mux, "/d/"+removed.Slug().String(), "")
	if recorder.Code != http.StatusGone {
		t.Fatalf("removed status = %d, want 410 (body: %s)", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "Arena removida") {
		t.Fatalf("removed body = %q, want the gone message", body)
	}
	if strings.Contains(body, removed.Statement().String()) || strings.Contains(body, "Contexto removido") {
		t.Fatalf("SECURITY VIOLATION: removed document leaked its content: %s", body)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
		t.Fatalf("removed Cache-Control = %q, want no-store", cacheControl)
	}
	if etag := recorder.Header().Get("ETag"); etag != "" {
		t.Fatalf("removed ETag = %q, want none on error documents", etag)
	}

	// Unknown and invalid addresses are 404.
	for _, path := range []string{"/d/this-arena-never-existed", "/d/UPPERCASE", "/d/x"} {
		recorder := getDocument(t, harness.mux, path, "")
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "Arena não encontrada") {
			t.Fatalf("GET %s body = %q, want the not-found message", path, recorder.Body.String())
		}
		if cacheControl := recorder.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
			t.Fatalf("GET %s Cache-Control = %q, want no-store", path, cacheControl)
		}
	}

	// Closed and restricted Arenas stay publicly readable with their label.
	closed := harness.publishArena(t, "O Brasil sediará a próxima Copa do Mundo", "", "pt-BR", "society")
	if _, err := harness.close.Execute(context.Background(), arenasapp.CloseArenaCommand{
		AccountID: harness.ownerID,
		ArenaID:   closed.ID().String(),
	}); err != nil {
		t.Fatalf("close arena: %v", err)
	}
	recorder = getDocument(t, harness.mux, "/d/"+closed.Slug().String(), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("closed status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "Fechada") {
		t.Fatalf("closed body = %q, want the closed label", recorder.Body.String())
	}

	restricted := harness.publishArena(t, "A energia de fusão será comercial em 2035", "", "pt-BR", "science")
	if _, err := harness.restrict.Execute(context.Background(), arenasapp.ModerateArenaCommand{
		ActorAccountID: harness.moderator,
		ArenaID:        restricted.ID().String(),
		Reason:         "restrição temporária durante a revisão",
	}); err != nil {
		t.Fatalf("restrict arena: %v", err)
	}
	recorder = getDocument(t, harness.mux, "/d/"+restricted.Slug().String(), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("restricted status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "Restrita") {
		t.Fatalf("restricted body = %q, want the restricted label", recorder.Body.String())
	}
}

// TestArenaDocumentNeverReflectsRequestedSlug is the anti-reflection proof:
// error documents render catalog text only.
func TestArenaDocumentNeverReflectsRequestedSlug(t *testing.T) {
	harness := setupSEOHarness(t)
	probe := `"><img src=x onerror=alert(9)>`

	recorder := getDocument(t, harness.mux, "/d/"+url.PathEscape(probe), "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("probe status = %d, want 404 (body: %s)", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "<img") || strings.Contains(body, "alert(9)") {
		t.Fatalf("SECURITY VIOLATION: error document reflected the probe: %s", body)
	}
}
