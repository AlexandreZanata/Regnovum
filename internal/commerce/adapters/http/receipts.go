package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
)

// refundDocument is one proportional compensation behind a trade
// receipt, when the liquidated payment was refunded in whole or in
// part.
type refundDocument struct {
	RefundKey      string `json:"refund_key"`
	AmountMilli    int64  `json:"amount_milli"`
	TitheReversal  int64  `json:"tithe_reversal_milli"`
	ProviderShare  int64  `json:"provider_share_milli"`
	ObligationMill int64  `json:"obligation_milli"`
	PostedAt       string `json:"posted_at"`
}

// receiptDocument is the private trade receipt: the sealed gross
// with its tithe split, the derived escrow status, the instants, the
// identifiers of the participant side and the compensations, if any.
// Buyer and provider read identical amounts through their own role;
// nothing of third parties ever appears.
type receiptDocument struct {
	Title                string           `json:"title"`
	ContractID           string           `json:"contract_id"`
	ContractKey          string           `json:"contract_key"`
	Role                 string           `json:"role"`
	Object               string           `json:"object"`
	GrossMilli           int64            `json:"gross_milli"`
	TitheMilli           int64            `json:"tithe_milli"`
	NetMilli             int64            `json:"net_milli"`
	Status               string           `json:"status"`
	ExpiresAt            string           `json:"expires_at"`
	PostedAt             string           `json:"posted_at"`
	SettledAt            *string          `json:"settled_at,omitempty"`
	EscrowTransferID     string           `json:"escrow_transfer_id"`
	SettlementTransferID *string          `json:"settlement_transfer_id,omitempty"`
	Refunds              []refundDocument `json:"refunds"`
	RefundedMilli        int64            `json:"refunded_milli"`
}

// getReceipt resolves one owned trade receipt.
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
	refunds := make([]refundDocument, 0, len(receipt.Refunds))
	for _, line := range receipt.Refunds {
		refunds = append(refunds, refundDocument{
			RefundKey: line.RefundKey, AmountMilli: line.AmountMill,
			TitheReversal: line.TitheReversal, ProviderShare: line.ProviderShare,
			ObligationMill: line.ObligationMill, PostedAt: iso(line.PostedAt),
		})
	}
	document := receiptDocument{
		Title: titles(r).Title, ContractID: receipt.ContractID,
		ContractKey: receipt.ContractKey, Role: receipt.Role, Object: receipt.Object,
		GrossMilli: receipt.GrossMilli, TitheMilli: receipt.TitheMilli, NetMilli: receipt.NetMilli,
		Status: string(receipt.Status), ExpiresAt: iso(receipt.ExpiresAt), PostedAt: iso(receipt.PostedAt),
		EscrowTransferID: receipt.EscrowTransferID, Refunds: refunds, RefundedMilli: receipt.RefundedMilli,
	}
	if receipt.SettledAt != nil {
		settled := iso(*receipt.SettledAt)
		document.SettledAt = &settled
	}
	if receipt.SettlementTransferID != nil {
		settlement := *receipt.SettlementTransferID
		document.SettlementTransferID = &settlement
	}
	writeJSON(w, http.StatusOK, document)
}

// statementEntryDocument is one extract line of the participant.
type statementEntryDocument struct {
	ContractID  string `json:"contract_id"`
	ContractKey string `json:"contract_key"`
	Role        string `json:"role"`
	GrossMilli  int64  `json:"gross_milli"`
	Status      string `json:"status"`
	PostedAt    string `json:"posted_at"`
}

// statementDocument is the participant extract: owned lines in
// reverse posting order.
type statementDocument struct {
	Title   string                   `json:"title"`
	Entries []statementEntryDocument `json:"entries"`
}

// getStatement resolves the participant extract.
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
			ContractID: entry.ContractID, ContractKey: entry.ContractKey,
			Role: entry.Role, GrossMilli: entry.GrossMilli,
			Status: string(entry.Status), PostedAt: iso(entry.PostedAt),
		})
	}
	writeJSON(w, http.StatusOK, statementDocument{Title: titles(r).Title, Entries: entries})
}
