package http_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/seasons/adapters/http"
	seasonpg "github.com/AlexandreZanata/Regnovum/internal/seasons/adapters/postgres"
)

func TestSeasonJourneyCurrentHistoryDetail(t *testing.T) {
	h, _ := newSeasonHarness(t)

	current := seasonRequest(t, h, http.MethodGet, "/api/v1/me/seasons/current", seasonOwnerToken, "pt-BR")
	if current.Code != http.StatusOK {
		t.Fatalf("current status = %d, body %s", current.Code, current.Body.String())
	}
	assertSeasonNoStore(t, current)
	document := seasonDecode(t, current.Body.Bytes())
	if document["season_key"] != h.current || document["state"] != "active" {
		t.Fatalf("current = %v, want active %q", document, h.current)
	}
	for _, key := range []string{"title", "season_key", "ordinal", "starts_at", "ends_at", "state"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("current missing key %q: %v", key, document)
		}
	}

	history := seasonRequest(t, h, http.MethodGet, "/api/v1/me/seasons/history", seasonOwnerToken, "pt-BR")
	if history.Code != http.StatusOK {
		t.Fatalf("history status = %d, body %s", history.Code, history.Body.String())
	}
	assertSeasonNoStore(t, history)
	extract := seasonDecode(t, history.Body.Bytes())
	entries, _ := extract["seasons"].([]any)
	if len(entries) != 2 {
		t.Fatalf("seasons = %v, want archived plus current", extract["seasons"])
	}

	detail := seasonRequest(t, h, http.MethodGet, "/api/v1/me/seasons/"+h.archived, seasonOwnerToken, "pt-BR")
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body %s", detail.Code, detail.Body.String())
	}
	assertSeasonNoStore(t, detail)
	archived := seasonDecode(t, detail.Body.Bytes())
	if archived["season_key"] != h.archived || archived["state"] != "archived" {
		t.Fatalf("detail = %v, want archived %q", archived, h.archived)
	}
}

func TestSeasonAPIRequiresAuthentication(t *testing.T) {
	h, _ := newSeasonHarness(t)
	paths := []string{
		"/api/v1/me/seasons/current",
		"/api/v1/me/seasons/history",
		"/api/v1/me/seasons/" + h.archived,
	}
	for _, path := range paths {
		recorder := seasonRequest(t, h, http.MethodGet, path, "", "")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s = %d, want 401", path, recorder.Code)
		}
	}
}

func TestSeasonInactiveNamespaceIsForbidden(t *testing.T) {
	h, _ := newSeasonHarness(t)
	recorder := seasonRequest(t, h, http.MethodGet, "/api/v1/me/seasons/compat-legacy", seasonOwnerToken, "")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("compat-legacy = %d, want 403", recorder.Code)
	}
	assertSeasonNoStore(t, recorder)
	body := seasonDecode(t, recorder.Body.Bytes())
	if body["code"] != "season_mismatch" {
		t.Fatalf("code = %v, want season_mismatch", body["code"])
	}
}

func TestSeasonForeignBookIsNotFound(t *testing.T) {
	h, _ := newSeasonHarness(t)
	alien := seasonRequest(t, h, http.MethodGet, "/api/v1/me/seasons/temporada-de-outra-conta", seasonOtherToken, "")
	if alien.Code != http.StatusNotFound {
		t.Fatalf("unknown season = %d, want 404", alien.Code)
	}
	assertSeasonNoStore(t, alien)
	other := seasonRequest(t, h, http.MethodGet, "/api/v1/me/seasons/"+h.current, seasonOtherToken, "pt-BR")
	if other.Code != http.StatusOK {
		t.Fatalf("other current = %d, want 200 for the global allowlist", other.Code)
	}
	owned := seasonDecode(t, seasonRequest(t, h, http.MethodGet, "/api/v1/me/seasons/"+h.current, seasonOwnerToken, "pt-BR").Body.Bytes())
	foreign := seasonDecode(t, other.Body.Bytes())
	for _, key := range []string{"season_key", "ordinal", "starts_at", "ends_at", "state"} {
		if owned[key] != foreign[key] {
			t.Fatalf("sides diverge on %q: owner=%v other=%v; allowlist reads the same book", key, owned[key], foreign[key])
		}
	}
}

func TestSeasonSuspendedReadsConflict(t *testing.T) {
	testDB := dbtest.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	base := time.Date(2027, time.January, 2, 12, 0, 0, 0, time.UTC)
	seedLifecycleSeason(t, ctx, testDB, "temporada-suspensa", 321, base, [][2]string{{"", "prepared"}})
	reads, err := seasonpg.NewSeasonReader(testDB.Pool.Pool())
	if err != nil {
		t.Fatalf("NewSeasonReader: %v", err)
	}
	secMgr := mustSeasonSecurity(t)
	handler, err := adapterhttp.NewHandler(adapterhttp.HandlerConfig{Reads: reads, Security: secMgr})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	_ = handler
	if _, err := reads.GetCurrent(ctx, "conta-suspensa"); err == nil {
		t.Fatal("suspended current passed: sem ACTIVE a leitura viva conflita")
	}
}

func TestSeasonArchivedPredecessorConflictsLive(t *testing.T) {
	testDB := dbtest.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	base := time.Date(2027, time.April, 2, 12, 0, 0, 0, time.UTC)
	seedLifecycleSeason(t, ctx, testDB, "temporada-arquivada", 322, base,
		[][2]string{{"", "prepared"}, {"prepared", "active"}, {"active", "closing"}, {"closing", "sealed"}, {"sealed", "archived"}})
	reads, err := seasonpg.NewSeasonReader(testDB.Pool.Pool())
	if err != nil {
		t.Fatalf("NewSeasonReader: %v", err)
	}
	if _, err := reads.GetCurrent(ctx, "conta-arquivada"); err == nil {
		t.Fatal("archived-only current passed: livro arquivado aguarda a sucessora")
	}
	history, err := reads.ListHistory(ctx, "conta-arquivada")
	if err != nil || len(history) != 1 || history[0].State != "archived" {
		t.Fatalf("history = %+v/%v, want the archived book allowlisted", history, err)
	}
}

func TestSeasonDocumentsCarryNoBalancesOrPII(t *testing.T) {
	h, _ := newSeasonHarness(t)
	for _, path := range []string{"/api/v1/me/seasons/current", "/api/v1/me/seasons/history", "/api/v1/me/seasons/" + h.archived} {
		raw := seasonRequest(t, h, http.MethodGet, path, seasonOwnerToken, "pt-BR").Body.String()
		lowered := strings.ToLower(raw)
		for _, banned := range []string{"milliink", "treasury", "balance", "holder", "monarch", "regent", "email", "account", "custody", "genesis"} {
			if strings.Contains(lowered, banned) {
				t.Fatalf("GET %s leaks %q: %s", path, banned, raw)
			}
		}
	}
}

func TestSeasonReadsAreLocaleIndependent(t *testing.T) {
	h, _ := newSeasonHarness(t)
	pt := seasonDecode(t, seasonRequest(t, h, http.MethodGet, "/api/v1/me/seasons/current", seasonOwnerToken, "pt-BR").Body.Bytes())
	en := seasonDecode(t, seasonRequest(t, h, http.MethodGet, "/api/v1/me/seasons/current", seasonOwnerToken, "en-US").Body.Bytes())
	for _, key := range []string{"season_key", "ordinal", "starts_at", "ends_at", "state"} {
		if pt[key] != en[key] {
			t.Fatalf("locale changed %q: pt=%v en=%v; translated text must never enter the computation", key, pt[key], en[key])
		}
	}
	if pt["title"] == en["title"] {
		t.Fatalf("titles do not differ: pt=%v en=%v", pt["title"], en["title"])
	}
	if !strings.Contains(pt["title"].(string), "Temporada") || !strings.Contains(en["title"].(string), "eason") {
		t.Fatalf("titles lost the season: pt=%v en=%v", pt["title"], en["title"])
	}
}
