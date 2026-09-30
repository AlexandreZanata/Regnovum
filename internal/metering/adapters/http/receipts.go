package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
)

// legDocument is one journal leg of a receipt: direction with its
// custody and exact amount.
type legDocument struct {
	Direction string `json:"direction"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Amount    int64  `json:"amount_milli"`
}

// refundDocument is the compensation behind a receipt, when the
// publication was compensated.
type refundDocument struct {
	RefundID   string `json:"refund_id"`
	Amount     int64  `json:"amount_milli"`
	TransferID string `json:"transfer_id"`
	PostedAt   string `json:"posted_at"`
}

// receiptDocument is the receipt: the sealed settlement with its
// legs and its compensation, if any.
type receiptDocument struct {
	Title         string          `json:"title"`
	PublicationID string          `json:"publication_id"`
	Service       string          `json:"service"`
	Version       int             `json:"version"`
	Units         int             `json:"units"`
	TotalMilli    int64           `json:"total_milli"`
	ContentHash   string          `json:"content_hash"`
	TransferID    string          `json:"transfer_id"`
	PostedAt      string          `json:"posted_at"`
	Legs          []legDocument   `json:"legs"`
	Refund        *refundDocument `json:"refund,omitempty"`
}

// getReceipt resolves one owned receipt.
func (h *Handler) getReceipt(w http.ResponseWriter, r *http.Request) {
	account, ok := h.identity(r)
	if !ok {
		deny(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication is required"))
		return
	}
	receipt, err := h.reads.GetReceipt(r.Context(), account, r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	legs := make([]legDocument, 0, len(receipt.Legs))
	for _, leg := range receipt.Legs {
		legs = append(legs, legDocument{
			Direction: leg.Direction, Kind: leg.Kind, Label: leg.Label, Amount: leg.Amount,
		})
	}
	document := receiptDocument{
		Title: titles(r).Title, PublicationID: receipt.Publication.ID,
		Service: receipt.Publication.Service, Version: receipt.Publication.Version,
		Units: receipt.Publication.Units, TotalMilli: receipt.Publication.AmountMilli,
		ContentHash: receipt.Publication.ContentHash, TransferID: receipt.Publication.TransferID,
		PostedAt: iso(receipt.Publication.PostedAt), Legs: legs,
	}
	if receipt.Refund != nil {
		document.Refund = &refundDocument{
			RefundID: receipt.Refund.ID, Amount: receipt.Refund.Amount,
			TransferID: receipt.Refund.TransferID, PostedAt: iso(receipt.Refund.PostedAt),
		}
	}
	writeJSON(w, http.StatusOK, document)
}

// statementEntryDocument is one extract line with its compensation
// flag.
type statementEntryDocument struct {
	PublicationID string `json:"publication_id"`
	Service       string `json:"service"`
	TotalMilli    int64  `json:"total_milli"`
	PostedAt      string `json:"posted_at"`
	Refunded      bool   `json:"refunded"`
}

// statementDocument is the owner extract: lines in reverse posting
// order with the journal-derived balance.
type statementDocument struct {
	Title        string                   `json:"title"`
	Entries      []statementEntryDocument `json:"entries"`
	BalanceMilli int64                    `json:"balance_milli"`
}

// getStatement resolves the owner extract with the current balance.
func (h *Handler) getStatement(w http.ResponseWriter, r *http.Request) {
	account, ok := h.identity(r)
	if !ok {
		deny(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication is required"))
		return
	}
	statement, err := h.reads.Statement(r.Context(), account, 50)
	if err != nil {
		fail(w, r, err)
		return
	}
	entries := make([]statementEntryDocument, 0, len(statement.Entries))
	for _, entry := range statement.Entries {
		entries = append(entries, statementEntryDocument{
			PublicationID: entry.Publication.ID, Service: entry.Publication.Service,
			TotalMilli: entry.Publication.AmountMilli,
			PostedAt:   iso(entry.Publication.PostedAt), Refunded: entry.Refunded,
		})
	}
	writeJSON(w, http.StatusOK, statementDocument{
		Title: titles(r).Title, Entries: entries, BalanceMilli: statement.BalanceMilli,
	})
}
