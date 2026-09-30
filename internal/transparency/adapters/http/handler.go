// Package http publishes versioned platform metrics (P14-T03): the JSON
// document at /api/v1/public/transparency and the HTML document at
// /transparency. Both are public and cacheable with methodology version,
// UTC period bounds, timezone label and derivation instant. Bodies carry
// suppressed integer counts only: no email, no Stripe identifier, no IP
// and no account-level position ever serializes.
package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/application"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/domain"
)

// metricsCacheSeconds is the public cache lifetime of transparency
// documents; metrics move per period, not per request, and the strong
// ETag keeps revalidation cheap.
const metricsCacheSeconds = 3600

// defaultWindowEnd anchors the default window to the end of the current UTC
// day, which is the reporting day these metrics belong to.
//
// A window that ended at the instant of the request made the published numbers
// unreproducible: a caller that asked for the same period a second later
// received a different document, so the report could not be verified against a
// later read, and its validator moved with the clock — which is what made the
// ETag of these documents unable to confirm that anything was unchanged. The
// day is the natural boundary of these metrics, and the window still covers
// everything that happened today, so the default report keeps counting the day
// in progress while staying stable for as long as it lasts.
func defaultWindowEnd(now time.Time) time.Time {
	utc := now.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
}

// defaultMetricsWindow is the lookback served when the caller names no
// explicit period.
const defaultMetricsWindow = 30 * 24 * time.Hour

// HandlerConfig aggregates the collaborators required to publish metrics.
type HandlerConfig struct {
	Derive    *application.DeriveMetricsUseCase
	Templates *Templates
	Clock     ports.Clock
}

// Handler serves the public transparency documents.
type Handler struct {
	derive    *application.DeriveMetricsUseCase
	templates *Templates
	clock     ports.Clock
}

// NewHandler constructs a transparency HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		derive:    cfg.Derive,
		templates: cfg.Templates,
		clock:     cfg.Clock,
	}
}

// transparencyResponse is the versioned JSON document: methodology,
// UTC bounds, timezone label, derivation instant and suppressed counts.
type transparencyResponse struct {
	MethodologyVersion int              `json:"methodology_version"`
	PeriodStart        string           `json:"period_start"`
	PeriodEnd          string           `json:"period_end"`
	Timezone           string           `json:"timezone"`
	UpdatedAt          string           `json:"updated_at"`
	Metrics            map[string]int64 `json:"metrics"`
}

// resolveWindow parses the explicit period or defaults to the trailing
// thirty days as of now. Bounds normalize to UTC; the timezone parameter
// only labels the document and never shifts the derivation.
func resolveWindow(query map[string][]string, now time.Time) (domain.Period, string, error) {
	timezone := "UTC"
	if names, ok := query["timezone"]; ok && len(names) > 0 && strings.TrimSpace(names[0]) != "" {
		name := strings.TrimSpace(names[0])
		location, err := time.LoadLocation(name)
		if err != nil {
			return domain.Period{}, "", err
		}
		timezone = location.String()
	}

	starts, hasStart := query["period_start"]
	ends, hasEnd := query["period_end"]
	if !hasStart || !hasEnd || len(starts) == 0 || len(ends) == 0 ||
		strings.TrimSpace(starts[0]) == "" || strings.TrimSpace(ends[0]) == "" {
		end := defaultWindowEnd(now)
		return mustPeriod(end.Add(-defaultMetricsWindow), end)
	}

	start, err := time.Parse(time.RFC3339, strings.TrimSpace(starts[0]))
	if err != nil {
		return domain.Period{}, "", err
	}
	end, err := time.Parse(time.RFC3339, strings.TrimSpace(ends[0]))
	if err != nil {
		return domain.Period{}, "", err
	}
	period, err := domain.NewPeriod(start, end)
	if err != nil {
		return domain.Period{}, "", err
	}
	return period, timezone, nil
}

func mustPeriod(start, end time.Time) (domain.Period, string, error) {
	period, err := domain.NewPeriod(start, end)
	if err != nil {
		return domain.Period{}, "", err
	}
	return period, "UTC", nil
}

// snapshotMetrics renders the suppressed snapshot as stable code/count
// pairs in canonical order.
func snapshotMetrics(snapshot *application.Snapshot) map[string]int64 {
	return map[string]int64{
		"eligible_accounts":        snapshot.EligibleAccounts,
		"arenas_published":         snapshot.ArenasPublished,
		"arenas_closed":            snapshot.ArenasClosed,
		"arenas_restricted":        snapshot.ArenasRestricted,
		"arenas_removed":           snapshot.ArenasRemoved,
		"arguments_published":      snapshot.ArgumentsPublished,
		"arguments_withdrawn":      snapshot.ArgumentsWithdrawn,
		"position_changes":         snapshot.PositionChanges,
		"attributions_valid":       snapshot.AttributionsValid,
		"attributions_invalidated": snapshot.AttributionsInvalidated,
		"influenced_authors":       snapshot.InfluencedAuthors,
		"ink_free_granted":         snapshot.InkFreeGranted,
		"ink_free_expired":         snapshot.InkFreeExpired,
		"ink_free_consumed":        snapshot.InkFreeConsumed,
		"ink_purchased_granted":    snapshot.InkPurchasedGranted,
		"ink_purchased_consumed":   snapshot.InkPurchasedConsumed,
		"ink_refunded":             snapshot.InkRefunded,
		"ink_admin_adjusted":       snapshot.InkAdminAdjusted,
		"passes_purchase_granted":  snapshot.PassesPurchaseGranted,
		"passes_member_granted":    snapshot.PassesMemberGranted,
		"passes_consumed":          snapshot.PassesConsumed,
		"reports_filed":            snapshot.ReportsFiled,
		"actions_recorded":         snapshot.ActionsRecorded,
		"appeals_filed":            snapshot.AppealsFiled,
		"appeals_reversed":         snapshot.AppealsReversed,
	}
}

// ServeMetrics handles GET /api/v1/public/transparency.
func (h *Handler) ServeMetrics(w http.ResponseWriter, r *http.Request) {
	if h.derive == nil || h.clock == nil {
		_ = httperror.WriteProblem(w, r, errMetricsUnavailable())
		return
	}

	now := h.clock.Now().UTC()
	period, timezone, err := resolveWindow(r.URL.Query(), now)
	if err != nil {
		_ = httperror.WriteProblem(w, r, errInvalidPeriod())
		return
	}

	snapshot, err := h.derive.Execute(r.Context(), period)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	response := transparencyResponse{
		MethodologyVersion: snapshot.MethodologyVersion,
		PeriodStart:        snapshot.PeriodStart.UTC().Format(time.RFC3339),
		PeriodEnd:          snapshot.PeriodEnd.UTC().Format(time.RFC3339),
		Timezone:           timezone,
		Metrics:            snapshotMetrics(snapshot),
	}
	// The facts are marshaled without the instant of this derivation, which
	// is what the validator covers; the document that is served carries it.
	facts, err := json.Marshal(response)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}
	response.UpdatedAt = now.Format(time.RFC3339)
	document, err := json.Marshal(response)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	writeCacheableJSON(w, r, facts, document)
}

// ServeDocument handles GET /transparency. The interface locale
// negotiates Accept-Language against the allowlist with an explicit
// locale parameter taking precedence; unknown values fall back to the
// product default without being reflected.
func (h *Handler) ServeDocument(w http.ResponseWriter, r *http.Request) {
	if h.derive == nil || h.templates == nil || h.clock == nil {
		_ = httperror.WriteProblem(w, r, errMetricsUnavailable())
		return
	}

	now := h.clock.Now().UTC()
	period, timezone, err := resolveWindow(r.URL.Query(), now)
	if err != nil {
		_ = httperror.WriteProblem(w, r, errInvalidPeriod())
		return
	}

	snapshot, err := h.derive.Execute(r.Context(), period)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	locale := negotiateLocale(r)
	document, err := h.buildDocument(locale, timezone, now, snapshot)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	// The page states the instant of this rendering, so the validator covers
	// the same page with that instant left out: two renderings of the same
	// facts have to compare equal (writeCacheableHTML).
	factsDocument, err := h.buildDocument(locale, timezone, time.Time{}, snapshot)
	if err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}
	var facts bytes.Buffer
	if err := h.templates.RenderDocument(&facts, factsDocument); err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}

	var body bytes.Buffer
	if err := h.templates.RenderDocument(&body, document); err != nil {
		_ = httperror.WriteProblem(w, r, err)
		return
	}
	w.Header().Set("Vary", "Accept-Language")
	writeCacheableHTML(w, r, facts.Bytes(), body.Bytes())
}

// buildDocument localizes the document shell around stable metric codes.
// Codes never translate: they are identifiers shared with the JSON
// document, while headings and notes resolve through the catalog.
func (h *Handler) buildDocument(locale, timezone string, now time.Time, snapshot *application.Snapshot) (TransparencyDocument, error) {
	format := func(key string, values map[string]string) (string, error) {
		message, err := i18n.Format(locale, key, values)
		if err != nil {
			return i18n.Format(i18n.DefaultLocale, key, values)
		}
		return message, nil
	}

	pageTitle, err := format("transparency.document.page_title", nil)
	if err != nil {
		return TransparencyDocument{}, err
	}
	heading, err := format("transparency.document.heading", nil)
	if err != nil {
		return TransparencyDocument{}, err
	}
	periodLine, err := format("transparency.document.period", map[string]string{
		"start":    snapshot.PeriodStart.UTC().Format(time.RFC3339),
		"end":      snapshot.PeriodEnd.UTC().Format(time.RFC3339),
		"timezone": timezone,
	})
	if err != nil {
		return TransparencyDocument{}, err
	}
	updatedLine, err := format("transparency.document.updated", map[string]string{
		"at":      now.Format(time.RFC3339),
		"version": strconv.Itoa(snapshot.MethodologyVersion),
	})
	if err != nil {
		return TransparencyDocument{}, err
	}
	methodology, err := format("transparency.document.methodology", nil)
	if err != nil {
		return TransparencyDocument{}, err
	}
	metricHeader, err := format("transparency.document.metric", nil)
	if err != nil {
		return TransparencyDocument{}, err
	}
	valueHeader, err := format("transparency.document.value", nil)
	if err != nil {
		return TransparencyDocument{}, err
	}

	metrics := snapshotMetrics(snapshot)
	codes := []string{
		"eligible_accounts",
		"arenas_published", "arenas_closed", "arenas_restricted", "arenas_removed",
		"arguments_published", "arguments_withdrawn",
		"position_changes",
		"attributions_valid", "attributions_invalidated", "influenced_authors",
		"ink_free_granted", "ink_free_expired", "ink_free_consumed",
		"ink_purchased_granted", "ink_purchased_consumed", "ink_refunded", "ink_admin_adjusted",
		"passes_purchase_granted", "passes_member_granted", "passes_consumed",
		"reports_filed", "actions_recorded", "appeals_filed", "appeals_reversed",
	}
	rows := make([]TransparencyRow, 0, len(codes))
	for _, code := range codes {
		rows = append(rows, TransparencyRow{Code: code, Value: metrics[code]})
	}

	return TransparencyDocument{
		Lang:         locale,
		PageTitle:    pageTitle,
		Heading:      heading,
		Period:       periodLine,
		Updated:      updatedLine,
		Methodology:  methodology,
		MetricHeader: metricHeader,
		ValueHeader:  valueHeader,
		Rows:         rows,
	}, nil
}

// negotiateLocale resolves the interface locale: an explicit valid locale
// parameter wins, then Accept-Language negotiated against the allowlist,
// then the product default. Unknown values never reflect.
func negotiateLocale(r *http.Request) string {
	if raw := strings.TrimSpace(r.URL.Query().Get("locale")); raw != "" {
		for _, supported := range i18n.SupportedLocales() {
			if raw == supported {
				return raw
			}
		}
	}
	header := r.Header.Get("Accept-Language")
	for _, part := range strings.Split(header, ",") {
		candidate := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		for _, supported := range i18n.SupportedLocales() {
			if strings.EqualFold(candidate, supported) {
				return supported
			}
			if len(candidate) >= 2 && strings.EqualFold(candidate[:2], supported[:2]) {
				return supported
			}
		}
	}
	return i18n.DefaultLocale
}

// writeCacheableJSON serves a public JSON document whose body states the
// instant it was derived at.
//
// The validator covers the document without that instant, and it is weak.
// A strong validator over the whole body was wrong: the instant moves on every
// request, so two reads of the same metrics never compared equal and a client
// revalidating after the cache window was sent the whole document again — the
// ETag saved nothing. RFC 9110 section 8.8.1 requires a strong validator to be
// unique across every representation, which no validator can be while the
// annotation is part of the body; section 8.8.2 is for exactly this case, and
// If-None-Match performs the weak comparison for GET.
//
// facts is the document with the annotation left out; body is what is served.
func writeCacheableJSON(w http.ResponseWriter, r *http.Request, facts, body []byte) {
	sum := sha256.Sum256(facts)
	etag := `W/"` + hex.EncodeToString(sum[:]) + `"`

	httpcache.Public(w, metricsCacheSeconds)
	w.Header().Set("ETag", etag)

	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// writeCacheableHTML serves a public HTML document whose body states the
// instant it was rendered at.
//
// The validator covers the page without that instant, and it is weak, for the
// same reason as the JSON document above: an annotation that moves on every
// request cannot be part of a validator that is supposed to confirm that the
// content did not move.
//
// facts is the page rendered without its annotation; body is what is served.
func writeCacheableHTML(w http.ResponseWriter, r *http.Request, facts, body []byte) {
	sum := sha256.Sum256(facts)
	etag := `W/"` + hex.EncodeToString(sum[:]) + `"`

	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(metricsCacheSeconds))
	w.Header().Set("ETag", etag)

	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// etagMatches implements the weak comparison of RFC 9110 for If-None-Match:
// the opaque tags are compared, and the weakness prefix of either side is not
// part of the identity of the representation.
func etagMatches(header, etag string) bool {
	etag = strings.TrimPrefix(etag, "W/")
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		candidate = strings.Trim(candidate, `"`)
		if `"`+candidate+`"` == etag {
			return true
		}
	}
	return false
}

// errMetricsUnavailable reports a wiring gap without leaking internals.
func errMetricsUnavailable() error {
	return apperr.New(apperr.KindInternal, "server_error", "transparency metrics unavailable")
}

// errInvalidPeriod reports an unparsable or incoherent period window.
func errInvalidPeriod() error {
	return apperr.New(apperr.KindValidation, "invalid_period", "period window is invalid")
}

// RegisterRoutes wires the public transparency documents into the
// provided ServeMux. Both stay outside /api/v1 when they serve HTML;
// the JSON document lives under the versioned public API.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/public/transparency", h.ServeMetrics)
	mux.HandleFunc("GET /transparency", h.ServeDocument)
}
