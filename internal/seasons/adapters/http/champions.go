package http

import (
	"context"
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	seasonapp "github.com/AlexandreZanata/Regnovum/internal/seasons/application"
)

// ChampionsReader serves redacted champion history for one account:
// co-leaders at cutoff, last King and locale titles. Exact wealth
// never leaves this reader: the HTTP surface is third-party safe by
// construction (omitNames, hidden wealth).
type ChampionsReader interface {
	GetChampions(ctx context.Context, accountID, seasonKey, locale string, omitNames, canSeeExact bool) (seasonapp.ChampionsView, error)
}

// championLeaderDocument is one redacted co-leader: pseudonym display
// with hidden wealth. The íntegra stays with titular/auditor paths.
type championLeaderDocument struct {
	Subject string `json:"subject"`
	Display string `json:"display"`
}

// championsDocument is the privacy-safe history answer of one book:
// cutoff identity, redacted co-leaders, last King pseudonym, hash
// and locale titles. No exact wealth, no real names.
type championsDocument struct {
	Title          string                   `json:"title"`
	SeasonKey      string                   `json:"season_key"`
	CutoffRevision int64                    `json:"cutoff_revision"`
	Hash           string                   `json:"hash"`
	Richest        string                   `json:"richest_title"`
	LastKing       string                   `json:"last_king_title"`
	LastKingHolder string                   `json:"last_king"`
	Leaders        []championLeaderDocument `json:"leaders"`
}

// localeTag selects the pt/en tag from Accept-Language for champion
// titles: dates and ranks travel untouched either way.
func localeTag(r *http.Request) string {
	for _, t := range championLocales(r) {
		return t
	}
	return "en"
}

func championLocales(r *http.Request) []string {
	accept := r.Header.Get("Accept-Language")
	if len(accept) >= 2 && (accept[0] == 'p' || accept[0] == 'P') {
		return []string{"pt"}
	}
	return []string{"en"}
}

// getChampions resolves the redacted champions of one allowlisted
// book. Third-party safe: pseudonyms only, wealth hidden, private
// no-store like every seasons answer.
func (h *Handler) getChampions(w http.ResponseWriter, r *http.Request) {
	account, ok := h.identity(r)
	if !ok {
		deny(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication is required"))
		return
	}
	if h.champions == nil {
		fail(w, r, apperr.New(apperr.KindNotFound, "season_unknown", "no such season for this account"))
		return
	}
	view, err := h.champions.GetChampions(r.Context(), account, r.PathValue("season_key"), localeTag(r), true, false)
	if err != nil {
		fail(w, r, err)
		return
	}
	leaders := make([]championLeaderDocument, 0, len(view.Leaders))
	for _, l := range view.Leaders {
		leaders = append(leaders, championLeaderDocument{Subject: l.Subject, Display: l.Display})
	}
	writeJSON(w, http.StatusOK, championsDocument{
		Title: view.HistoryTitle, SeasonKey: view.Season,
		CutoffRevision: view.CutoffRevision, Hash: view.Hash,
		Richest: view.RichestTitle, LastKing: view.LastKingTitle,
		LastKingHolder: view.LastKing, Leaders: leaders,
	})
}
