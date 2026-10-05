package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/seasons/adapters/http"
	seasonapp "github.com/AlexandreZanata/Regnovum/internal/seasons/application"
	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

type stubReads struct{}

func (stubReads) GetCurrent(context.Context, string) (seasonapp.SeasonView, error) {
	return seasonapp.SeasonView{}, seasondomain.ErrSeasonClosed
}

func (stubReads) GetSeason(context.Context, string, string) (seasonapp.SeasonView, error) {
	return seasonapp.SeasonView{}, seasondomain.ErrSeasonUnknown
}

func (stubReads) ListHistory(context.Context, string) ([]seasonapp.SeasonView, error) {
	return nil, nil
}

type stubChampions struct{}

func (stubChampions) GetChampions(_ context.Context, _, seasonKey, locale string, _, _ bool) (seasonapp.ChampionsView, error) {
	if seasonKey != "temporada-1" {
		return seasonapp.ChampionsView{}, seasondomain.ErrSeasonUnknown
	}
	titles := seasondomain.ChampionTitlesFor(seasondomain.SeasonLocale(locale))
	if locale != "pt" && locale != "en" {
		titles = seasondomain.ChampionTitlesFor(seasondomain.SeasonLocaleEnglish)
	}
	return seasonapp.ChampionsView{
		Season: "temporada-1", CutoffRevision: 42, Hash: "ab12",
		Version: 1, LastKing: "alias-reservado",
		Leaders: []seasonapp.ExportedLeader{
			{Subject: "ana", Display: "coruja-azul"},
			{Subject: "bruno", Display: "lobo-cinza"},
		},
		RichestTitle: titles.Richest, LastKingTitle: titles.LastKing, HistoryTitle: titles.History,
	}, nil
}

func championsMux(t *testing.T) http.Handler {
	t.Helper()
	handler, err := adapterhttp.NewHandler(adapterhttp.HandlerConfig{Reads: stubReads{}, Champions: stubChampions{}})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return mux
}

func championsRequest(t *testing.T, mux http.Handler, target, locale string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if locale != "" {
		req.Header.Set("Accept-Language", locale)
	}
	req = req.WithContext(security.WithAuth(req.Context(), security.AuthIdentity{AccountID: "conta-1", SessionID: "sessao-1"}))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)
	return recorder
}

func TestChampions_RedactedNoStorePtEn(t *testing.T) {
	mux := championsMux(t)
	for _, locale := range []string{"pt-BR", "en-US"} {
		recorder := championsRequest(t, mux, "/api/v1/me/seasons/temporada-1/champions", locale)
		if recorder.Code != http.StatusOK {
			t.Fatalf("champions %s status = %d", locale, recorder.Code)
		}
		if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
			t.Fatalf("champions %s Cache-Control = %q, want no-store", locale, got)
		}
		var document map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
			t.Fatalf("decode champions %s: %v", locale, err)
		}
		raw, _ := json.Marshal(document)
		if strings.Contains(string(raw), "800") || strings.Contains(string(raw), "\"wealth\"") {
			t.Fatalf("champions %s leaked exact wealth: %s", locale, raw)
		}
		if !strings.Contains(string(raw), "coruja-azul") {
			t.Fatalf("champions %s missing pseudonym: %s", locale, raw)
		}
	}
	pt := championsRequest(t, mux, "/api/v1/me/seasons/temporada-1/champions", "pt-BR")
	en := championsRequest(t, mux, "/api/v1/me/seasons/temporada-1/champions", "en-US")
	if pt.Body.String() == en.Body.String() {
		t.Fatal("pt/en champions identical: cache não mistura locale")
	}
}
