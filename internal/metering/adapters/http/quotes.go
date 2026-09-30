package http

import (
	"net/http"

	meteringapp "github.com/AlexandreZanata/Regnovum/internal/metering/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
)

// quoteRequest is the priced preview input: the candidate text and
// the service table pricing it. Drafts never settle here.
type quoteRequest struct {
	Content string `json:"content"`
	Service string `json:"service"`
}

// quoteResponse is the priced preview document: canonical integer
// amounts with the rule version, the content hash and the
// acceptance window a confirmation must meet.
type quoteResponse struct {
	Title       string `json:"title"`
	Units       int    `json:"units"`
	PriceMilli  int64  `json:"price_milli"`
	TotalMilli  int64  `json:"total_milli"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
	QuoteHash   string `json:"quote_hash"`
	AcceptedAt  string `json:"accepted_at"`
	ExpiresAt   string `json:"expires_at"`
}

// previewQuote prices one candidate without storing or charging
// anything.
func (h *Handler) previewQuote(w http.ResponseWriter, r *http.Request) {
	account, ok := h.identity(r)
	if !ok {
		deny(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication is required"))
		return
	}
	var input quoteRequest
	if !decodeBody(w, r, &input) {
		return
	}
	now := h.clock.Now().UTC()
	preview, err := h.preview.Execute(meteringapp.PreviewCommand{
		Account: account, Content: input.Content, Service: input.Service,
		Now: now, MaxUnits: h.maxUnits, TTL: h.ttl,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, quoteResponse{
		Title: titles(r).Title, Units: preview.Units,
		PriceMilli: preview.PriceMilli, TotalMilli: preview.TotalMilli,
		Version: preview.Version, ContentHash: preview.ContentHash,
		QuoteHash:  preview.QuoteHash,
		AcceptedAt: iso(preview.AcceptedAt), ExpiresAt: iso(preview.ExpiresAt),
	})
}

// publicationRequest is the confirmation input: the idempotency key
// with the exact candidate the preview priced. Edited bytes price
// anew instead of reusing the preview.
type publicationRequest struct {
	IntentionKey string `json:"intention_key"`
	Content      string `json:"content"`
	Service      string `json:"service"`
}

// publicationResponse is the settlement document: the stored row,
// the journal transfer, the exact total and whether the call
// replayed the original settlement.
type publicationResponse struct {
	Title         string `json:"title"`
	PublicationID string `json:"publication_id"`
	TransferID    string `json:"transfer_id"`
	TotalMilli    int64  `json:"total_milli"`
	PostedAt      string `json:"posted_at"`
	Replayed      bool   `json:"replayed"`
}

// confirmPublication settles one candidate with its exact quoted
// charge. The preview recomputes server-side at the confirmation
// instant from the candidate bytes: a stale client expectation
// never binds the settlement, and edited bytes price anew.
func (h *Handler) confirmPublication(w http.ResponseWriter, r *http.Request) {
	account, ok := h.identity(r)
	if !ok {
		deny(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication is required"))
		return
	}
	var input publicationRequest
	if !decodeBody(w, r, &input) {
		return
	}
	now := h.clock.Now().UTC()
	preview, err := h.preview.Execute(meteringapp.PreviewCommand{
		Account: account, Content: input.Content, Service: input.Service,
		Now: now, MaxUnits: h.maxUnits, TTL: h.ttl,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	result, err := h.publish.Execute(r.Context(), meteringapp.PublishCommand{
		Key: input.IntentionKey, Account: account, Content: preview.Content,
		Price: preview.Price, Quote: preview.Quote,
		FromKind: h.fromKind, FromLabel: account,
		ToKind: h.toKind, ToLabel: h.toLabel, Now: now,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, publicationResponse{
		Title: titles(r).Title, PublicationID: result.PublicationID,
		TransferID: result.TransferID, TotalMilli: result.TotalMilli,
		PostedAt: iso(result.PostedAt), Replayed: result.Replayed,
	})
}
