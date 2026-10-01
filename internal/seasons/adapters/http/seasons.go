package http

import (
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
)

// seasonDocument is one allowlisted season: the book identity, the
// ordinal, the exact UTC window and the lifecycle state. Balances,
// holders, accounts and personal data never appear here: history is
// allowlist, never a ledger extract.
type seasonDocument struct {
	Title     string `json:"title"`
	SeasonKey string `json:"season_key"`
	Ordinal   int    `json:"ordinal"`
	StartsAt  string `json:"starts_at"`
	EndsAt    string `json:"ends_at"`
	State     string `json:"state"`
}

// historyDocument is the allowlisted history in ordinal order.
type historyDocument struct {
	Title   string           `json:"title"`
	Seasons []seasonDocument `json:"seasons"`
}

// getCurrent resolves the current ACTIVE season for the caller.
func (h *Handler) getCurrent(w http.ResponseWriter, r *http.Request) {
	account, ok := h.identity(r)
	if !ok {
		deny(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication is required"))
		return
	}
	view, err := h.reads.GetCurrent(r.Context(), account)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, seasonDocument{
		Title: titles(r).Current, SeasonKey: view.SeasonKey, Ordinal: view.Ordinal,
		StartsAt: iso(view.StartsAt), EndsAt: iso(view.EndsAt), State: view.State,
	})
}

// getHistory resolves the allowlisted history for the caller.
func (h *Handler) getHistory(w http.ResponseWriter, r *http.Request) {
	account, ok := h.identity(r)
	if !ok {
		deny(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication is required"))
		return
	}
	views, err := h.reads.ListHistory(r.Context(), account)
	if err != nil {
		fail(w, r, err)
		return
	}
	seasons := make([]seasonDocument, 0, len(views))
	for _, view := range views {
		seasons = append(seasons, seasonDocument{
			Title: titles(r).History, SeasonKey: view.SeasonKey, Ordinal: view.Ordinal,
			StartsAt: iso(view.StartsAt), EndsAt: iso(view.EndsAt), State: view.State,
		})
	}
	writeJSON(w, http.StatusOK, historyDocument{Title: titles(r).History, Seasons: seasons})
}

// getSeason resolves one allowlisted season for the caller.
func (h *Handler) getSeason(w http.ResponseWriter, r *http.Request) {
	account, ok := h.identity(r)
	if !ok {
		deny(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication is required"))
		return
	}
	view, err := h.reads.GetSeason(r.Context(), account, r.PathValue("season_key"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, seasonDocument{
		Title: titles(r).Title, SeasonKey: view.SeasonKey, Ordinal: view.Ordinal,
		StartsAt: iso(view.StartsAt), EndsAt: iso(view.EndsAt), State: view.State,
	})
}
