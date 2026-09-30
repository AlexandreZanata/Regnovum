package http

import (
	"net/http"

	disputesapp "github.com/AlexandreZanata/Regnovum/internal/disputes/application"
	disputesdomain "github.com/AlexandreZanata/Regnovum/internal/disputes/domain"
)

// noticeEntry is one localized lifecycle notice: the stable event
// code beside its translated title. Versions and instants stay on
// the case file; this list only names what happened.
type noticeEntry struct {
	Event string `json:"event"`
	Title string `json:"title"`
}

// notices renders the five lifecycle notices in the request locale.
func notices(titles disputesdomain.NoticeTitles) []noticeEntry {
	events := []struct {
		event disputesdomain.DisputeEvent
		title string
	}{
		{disputesdomain.EventProposal, titles.Proposal},
		{disputesdomain.EventAccept, titles.Accept},
		{disputesdomain.EventDefense, titles.Defense},
		{disputesdomain.EventRuling, titles.Ruling},
		{disputesdomain.EventAppeal, titles.Appeal},
	}
	entries := make([]noticeEntry, 0, len(events))
	for _, item := range events {
		entries = append(entries, noticeEntry{Event: item.event.String(), Title: item.title})
	}
	return entries
}

// caseFileDocument is the private case file: the sealed proposal
// with its version, the two named parties through the reader's
// role, the acceptance and defense counts, the stable ruling when
// reasoned, the appeal state, the escrow reference with the
// declared value, the instants and the localized notices. Proof
// digests never appear here: counts prove the defenses, digests
// stay in the record.
type caseFileDocument struct {
	Title       string        `json:"title"`
	Key         string        `json:"key"`
	Version     int           `json:"version"`
	Kind        string        `json:"kind"`
	Status      string        `json:"status"`
	Role        string        `json:"role"`
	Object      string        `json:"object"`
	ValueMilli  int64         `json:"value_milli"`
	EscrowRef   string        `json:"escrow_ref"`
	Accepts     int           `json:"accepts"`
	Defenses    int           `json:"defenses"`
	Verdict     *string       `json:"verdict,omitempty"`
	AwardMilli  *int64        `json:"award_milli,omitempty"`
	DecidedAt   *string       `json:"decided_at,omitempty"`
	AppealDueAt *string       `json:"appeal_due_at,omitempty"`
	Appealed    bool          `json:"appealed"`
	ExpiresAt   string        `json:"expires_at"`
	OpenedAt    *string       `json:"opened_at,omitempty"`
	Notices     []noticeEntry `json:"notices"`
}

// describe renders one owned case file. The reader's role is their
// party side; both sides read identical versions, amounts and
// instants.
func describe(record disputesapp.CaseRecord, account string, titles disputesdomain.NoticeTitles) caseFileDocument {
	proposal := record.Proposal
	role := "respondent"
	if account == proposal.Claimant {
		role = "claimant"
	}
	accepts := 0
	for _, accepted := range proposal.Accepted {
		if accepted {
			accepts++
		}
	}
	defenses := 0
	if record.Hearing != nil {
		defenses = len(record.Hearing.Evidence)
	}
	document := caseFileDocument{
		Title: titles.Case, Key: proposal.Key, Version: proposal.Version,
		Role: role, Object: proposal.Object, ValueMilli: proposal.ValueMilli,
		EscrowRef: proposal.EscrowRef, Accepts: accepts, Defenses: defenses,
		ExpiresAt: iso(proposal.ExpiresAt), Notices: notices(titles),
	}
	if record.Entry.Status != "" {
		document.Kind = record.Entry.Kind.String()
		document.Status = string(record.Entry.Status)
		opened := iso(record.Entry.OpenedAt)
		document.OpenedAt = &opened
	} else {
		document.Kind = disputesdomain.CaseArbitration.String()
		document.Status = "proposed"
	}
	if record.Decision != nil {
		verdict := record.Decision.Verdict.String()
		decided := iso(record.Decision.DecidedAt)
		due := iso(record.Decision.AppealDueAt)
		award := record.Decision.AwardMilli
		document.Verdict = &verdict
		document.AwardMilli = &award
		document.DecidedAt = &decided
		document.AppealDueAt = &due
		document.Appealed = record.Decision.Appealed
	}
	return document
}

// getCaseFile resolves one owned case file.
func (h *Handler) getCaseFile(w http.ResponseWriter, r *http.Request) {
	account, ok := h.account(w, r)
	if !ok {
		h.log(r.Method, r.URL.Path, http.StatusUnauthorized, r.PathValue("key"))
		return
	}
	record, err := h.read.Execute(r.PathValue("key"), account)
	if err != nil {
		h.log(r.Method, r.URL.Path, http.StatusNotFound, r.PathValue("key"))
		conflict(w, r, err, http.StatusNotFound)
		return
	}
	h.log(r.Method, r.URL.Path, http.StatusOK, record.Proposal.Key)
	writeJSON(w, http.StatusOK, describe(record, account, titles(r)))
}

// postAccept records the session account's acceptance of the filed
// terms. Replays answer unchanged.
func (h *Handler) postAccept(w http.ResponseWriter, r *http.Request) {
	account, ok := h.account(w, r)
	if !ok {
		h.log(r.Method, r.URL.Path, http.StatusUnauthorized, r.PathValue("key"))
		return
	}
	record, err := h.accept.Execute(r.PathValue("key"), account, h.clock.Now())
	if err != nil {
		h.log(r.Method, r.URL.Path, http.StatusForbidden, r.PathValue("key"))
		conflict(w, r, err, http.StatusForbidden)
		return
	}
	h.log(r.Method, r.URL.Path, http.StatusOK, record.Proposal.Key)
	writeJSON(w, http.StatusOK, describe(record, account, titles(r)))
}

// defenseInput carries one defense exhibit digest. The digest points
// to the filed content; the content itself never travels here.
type defenseInput struct {
	Digest string `json:"digest"`
}

// postDefense files one proportional-access exhibit of the session
// account.
func (h *Handler) postDefense(w http.ResponseWriter, r *http.Request) {
	account, ok := h.account(w, r)
	if !ok {
		h.log(r.Method, r.URL.Path, http.StatusUnauthorized, r.PathValue("key"))
		return
	}
	var input defenseInput
	if !decodeInput(w, r, &input) {
		h.log(r.Method, r.URL.Path, http.StatusBadRequest, r.PathValue("key"))
		return
	}
	record, err := h.defend.Execute(r.PathValue("key"), account, input.Digest, h.clock.Now())
	if err != nil {
		h.log(r.Method, r.URL.Path, http.StatusForbidden, r.PathValue("key"))
		conflict(w, r, err, http.StatusForbidden)
		return
	}
	h.log(r.Method, r.URL.Path, http.StatusOK, record.Proposal.Key)
	writeJSON(w, http.StatusOK, describe(record, account, titles(r)))
}

// rulingDocument is the stable ruling read: the decision code, the
// sealed terms hash it applies, the verdict with the capped award,
// the instants and the appeal state. Proof digests and grounds stay
// out: the code decides, the record keeps the proof.
type rulingDocument struct {
	Title        string `json:"title"`
	Key          string `json:"key"`
	DecisionCode string `json:"decision_code"`
	TermsHash    string `json:"terms_hash"`
	Verdict      string `json:"verdict"`
	AwardMilli   int64  `json:"award_milli"`
	DecidedAt    string `json:"decided_at"`
	AppealDueAt  string `json:"appeal_due_at"`
	Appealed     bool   `json:"appealed"`
}

// getRuling resolves the stable ruling of one owned case. Without a
// reasoned decision there is nothing to read yet.
func (h *Handler) getRuling(w http.ResponseWriter, r *http.Request) {
	account, ok := h.account(w, r)
	if !ok {
		h.log(r.Method, r.URL.Path, http.StatusUnauthorized, r.PathValue("key"))
		return
	}
	record, err := h.read.Execute(r.PathValue("key"), account)
	if err != nil {
		h.log(r.Method, r.URL.Path, http.StatusNotFound, r.PathValue("key"))
		conflict(w, r, err, http.StatusNotFound)
		return
	}
	if record.Decision == nil {
		h.log(r.Method, r.URL.Path, http.StatusNotFound, record.Proposal.Key)
		conflict(w, r, disputesdomain.ErrUnknownCase, http.StatusNotFound)
		return
	}
	decision := record.Decision
	h.log(r.Method, r.URL.Path, http.StatusOK, record.Proposal.Key)
	writeJSON(w, http.StatusOK, rulingDocument{
		Title: titles(r).Ruling, Key: record.Proposal.Key,
		DecisionCode: decision.Verdict.String() + "/" + decision.TermsHash,
		TermsHash:    decision.TermsHash, Verdict: decision.Verdict.String(),
		AwardMilli: decision.AwardMilli, DecidedAt: iso(decision.DecidedAt),
		AppealDueAt: iso(decision.AppealDueAt), Appealed: decision.Appealed,
	})
}

// appealInput carries one appeal reason in the reader's words.
type appealInput struct {
	Reason string `json:"reason"`
}

// postAppeal contests the filed ruling once. A second appeal over
// the same ruling conflicts.
func (h *Handler) postAppeal(w http.ResponseWriter, r *http.Request) {
	account, ok := h.account(w, r)
	if !ok {
		h.log(r.Method, r.URL.Path, http.StatusUnauthorized, r.PathValue("key"))
		return
	}
	var input appealInput
	if !decodeInput(w, r, &input) {
		h.log(r.Method, r.URL.Path, http.StatusBadRequest, r.PathValue("key"))
		return
	}
	record, err := h.appeal.Execute(r.PathValue("key"), account, input.Reason, h.clock.Now())
	if err != nil {
		h.log(r.Method, r.URL.Path, http.StatusForbidden, r.PathValue("key"))
		conflict(w, r, err, http.StatusForbidden)
		return
	}
	h.log(r.Method, r.URL.Path, http.StatusOK, record.Proposal.Key)
	writeJSON(w, http.StatusOK, describe(record, account, titles(r)))
}
