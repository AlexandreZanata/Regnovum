package html

import (
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
)

// documentCacheSeconds is the public cache lifetime of the Arena document;
// the strong ETag keeps revalidation cheap.
const documentCacheSeconds = 60

// HandlerConfig aggregates the use case and templates required to render
// the public Arena document.
type HandlerConfig struct {
	GetDocumentUseCase *application.GetArenaDocumentUseCase
	Templates          *Templates
}

// Handler serves the cacheable public Arena document.
type Handler struct {
	getDocument *application.GetArenaDocumentUseCase
	templates   *Templates
}

// NewHandler constructs the arenas HTML handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		getDocument: cfg.GetDocumentUseCase,
		templates:   cfg.Templates,
	}
}

// RegisterRoutes wires the Arena document route into the provided ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /d/{slug}", h.ServeDocument)
}

// ServeDocument handles GET /d/{slug}. Public Arenas render a cacheable
// document; removed Arenas answer 410 Gone, everything else answers 404.
func (h *Handler) ServeDocument(w http.ResponseWriter, r *http.Request) {
	arena, err := h.getDocument.Execute(r.Context(), r.PathValue("slug"))
	switch {
	case errors.Is(err, application.ErrArenaNotFound):
		h.renderErrorPage(w, r, http.StatusNotFound, "arenas.document.not_found")
		return
	case errors.Is(err, application.ErrArenaGone):
		h.renderErrorPage(w, r, http.StatusGone, "arenas.document.gone")
		return
	case err != nil:
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	document, err := h.buildDocument(*arena)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	var body bytes.Buffer
	if err := h.templates.RenderDocument(&body, document); err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}
	writeCacheableHTML(w, r, body.Bytes())
}

// buildDocument assembles the presentation data of the public document. The
// content language drives the interface locale: both supported content
// languages are interface locales (pt-BR, en-US).
func (h *Handler) buildDocument(arena domain.Arena) (DocumentData, error) {
	lang := arena.Language().String()
	statement := arena.Statement().String()
	context := arena.Context().String()
	description := context
	if description == "" {
		description = statement
	}

	statusLabel, err := h.localized(lang, "arenas.document.status."+arena.Status().String(), nil)
	if err != nil {
		return DocumentData{}, err
	}
	pageTitle, err := h.localized(lang, "arenas.document.page_title", map[string]string{"subject": statement})
	if err != nil {
		return DocumentData{}, err
	}

	publishedAt := arena.PublishedAt()
	if publishedAt == nil {
		return DocumentData{}, errors.New("public arena is missing published_at")
	}
	canonical := "/d/" + arena.Slug().String()

	structured, err := marshalStructuredData(structuredData{
		Context:       "https://schema.org",
		Type:          "Article",
		Headline:      statement,
		Description:   description,
		InLanguage:    lang,
		DatePublished: publishedAt.UTC().Format(time.RFC3339),
		URL:           canonical,
	})
	if err != nil {
		return DocumentData{}, err
	}

	return DocumentData{
		Lang:        lang,
		PageTitle:   pageTitle,
		Statement:   statement,
		Context:     context,
		Description: description,
		Canonical:   canonical,
		OGLocale:    strings.ReplaceAll(lang, "-", "_"),
		StatusLabel: statusLabel,
		PublishedAt: publishedAt.UTC().Format(time.RFC3339),
		JSONLD:      structured,
	}, nil
}

// structuredData is the JSON-LD payload of one public Arena document. The
// MVP exposes no aggregated results, so metadata carries only the Arena's
// own content and publication facts.
type structuredData struct {
	Context       string `json:"@context"`
	Type          string `json:"@type"`
	Headline      string `json:"headline"`
	Description   string `json:"description"`
	InLanguage    string `json:"inLanguage"`
	DatePublished string `json:"datePublished"`
	URL           string `json:"url"`
}

// marshalStructuredData encodes the JSON-LD document. encoding/json escapes
// <, > and & by default, so the payload can never close the script element
// or inject markup; the result is therefore safe to hand to html/template as
// template.JS.
func marshalStructuredData(data structuredData) (template.JS, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return template.JS(encoded), nil
}

// localized resolves the message in the given locale, falling back to the
// default locale for messages missing from it. The I18N standard requires a
// metric for that fallback; observability arrives in a later phase.
func (h *Handler) localized(locale, key string, values map[string]string) (string, error) {
	message, err := i18n.Format(locale, key, values)
	if err == nil {
		return message, nil
	}
	return i18n.Format(i18n.DefaultLocale, key, values)
}

// renderErrorPage renders the not-found or gone document in the default
// locale. Error documents are never cacheable and never echo the requested
// slug.
func (h *Handler) renderErrorPage(w http.ResponseWriter, r *http.Request, status int, keyPrefix string) {
	locale := i18n.DefaultLocale

	title, err := h.localized(locale, keyPrefix+".title", nil)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}
	detail, err := h.localized(locale, keyPrefix+".detail", nil)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}
	pageTitle, err := h.localized(locale, "arenas.document.page_title", map[string]string{"subject": title})
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	var body bytes.Buffer
	if err := h.templates.RenderError(&body, ErrorData{
		Lang:      locale,
		PageTitle: pageTitle,
		Title:     title,
		Detail:    detail,
	}); err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	httpcache.NoStore(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

// writeCacheableHTML writes a public HTML document with a strong ETag and
// honors If-None-Match with 304.
func writeCacheableHTML(w http.ResponseWriter, r *http.Request, body []byte) {
	etag := httpcache.Validator(body)

	httpcache.Public(w, documentCacheSeconds)
	w.Header().Set("ETag", etag)

	if httpcache.Matches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
