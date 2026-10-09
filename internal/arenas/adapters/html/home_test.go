package html

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	searchapp "github.com/AlexandreZanata/Regnovum/internal/search/application"
)

type homeFeedStub struct {
	command application.GetArenaFeedCommand
	page    application.ArenaFeedPage
	err     error
}

func (stub *homeFeedStub) Execute(_ context.Context, command application.GetArenaFeedCommand) (*application.ArenaFeedPage, error) {
	stub.command = command
	return &stub.page, stub.err
}

type homeSearchStub struct {
	query, language, cursor string
	page                    searchapp.ArenaPage
	err                     error
}

func (stub *homeSearchStub) Execute(_ context.Context, query, language, cursor string, _ int) (*searchapp.ArenaPage, error) {
	stub.query, stub.language, stub.cursor = query, language, cursor
	return &stub.page, stub.err
}

func homeTestManifest(t *testing.T) assets.Manifest {
	t.Helper()
	manifest := assets.Manifest{Version: 1, Assets: map[string]assets.Record{}}
	root := "../../../.."
	err := filepath.WalkDir(filepath.Join(root, "web/public"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel(filepath.Join(root, "web/public"), path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		manifest.Assets[name] = assets.Record{Path: "/assets/" + name, SHA256: "fixture"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"styles/realm.css", "components/realm/navigation.js"} {
		manifest.Assets[name] = assets.Record{Path: "/assets/" + name, SHA256: "fixture"}
	}
	return manifest
}

func homeResponse(t *testing.T, feed HomeFeed, search HomeSearch, target string) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := NewHomeHandler(feed, search, homeTestManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	return response
}

func TestHomeSearchRendersEscapedCardsAndCursor(t *testing.T) {
	search := &homeSearchStub{page: searchapp.ArenaPage{Items: []searchapp.ArenaResult{{Slug: "public-debate", Statement: `<script>alert("x")</script>`, Category: "technology", Language: "pt-BR"}}, NextCursor: "signed+cursor/="}}
	response := homeResponse(t, &homeFeedStub{}, search, "/?q=energia&cursor=previous")
	body := response.Body.String()
	if response.Code != http.StatusOK || search.query != "energia" || search.cursor != "previous" {
		t.Fatalf("search response = %d, query=%q cursor=%q, body %.400s", response.Code, search.query, search.cursor, body)
	}
	for _, want := range []string{"&lt;script&gt;", "/arenas/public-debate", "/d/public-debate", "cursor=signed%2Bcursor%2F%3D", "q=energia", "ga-arena-card"} {
		if !strings.Contains(body, want) {
			t.Errorf("search document missing %q", want)
		}
	}
	for _, forbidden := range []string{`<script>alert`, "2.450", "52%", "1.240 INK", "aggregate"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("home disclosed or injected %q", forbidden)
		}
	}
	if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("search document must not be stored")
	}
}

func TestHomeFeedFiltersAndNativeEmptyState(t *testing.T) {
	feed := &homeFeedStub{}
	response := homeResponse(t, feed, &homeSearchStub{}, "/?category=science&locale=en-US&language=en-US&cursor=opaque")
	if response.Code != http.StatusOK || feed.command.Category != "science" || feed.command.Language != "en-US" || feed.command.Cursor != "opaque" || feed.command.Limit != 8 {
		t.Fatalf("unexpected feed request: status=%d command=%+v body=%.300s", response.Code, feed.command, response.Body.String())
	}
	for _, want := range []string{`lang="en-US"`, "No arenas found.", `role="search"`, "Kingdom dashboard", `href="/transparency"`, "Not available yet"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("native home missing %q", want)
		}
	}
}

func TestHomeInterfaceLocaleDoesNotFilterContent(t *testing.T) {
	feed := &homeFeedStub{}
	response := homeResponse(t, feed, &homeSearchStub{}, "/?locale=en-US")
	if response.Code != 200 || feed.command.Language != "" || !strings.Contains(response.Body.String(), `lang="en-US"`) {
		t.Fatalf("interface locale leaked into content filter: status=%d language=%q", response.Code, feed.command.Language)
	}
}

func TestHomeFeedRendersActualPublishedArena(t *testing.T) {
	statement, err := domain.ParseStatement("Contratos voluntários melhoram a cooperação entre cidadãos.", domain.DefaultStatementPolicy())
	if err != nil {
		t.Fatal(err)
	}
	category, err := domain.ParseCategory("economics")
	if err != nil {
		t.Fatal(err)
	}
	language, err := domain.ParseLanguage("pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	slug, err := domain.ParseSlug("contratos-voluntarios")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	arena, err := domain.ReconstituteArena("arena-one", "owner-one", statement, domain.Context{}, category, language, domain.ArenaStatusPublished, slug, 1, at, &at, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := homeResponse(t, &homeFeedStub{page: application.ArenaFeedPage{Arenas: []domain.Arena{*arena}}}, &homeSearchStub{}, "/")
	if response.Code != 200 || !strings.Contains(response.Body.String(), statement.String()) || !strings.Contains(response.Body.String(), "/arenas/contratos-voluntarios") {
		t.Fatalf("published feed was not rendered: status %d, body %.400s", response.Code, response.Body.String())
	}
}

func TestHomeFailureIsHonestAndUnknownPathIs404(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{{"storage", errors.New("private storage detail"), 503}, {"cursor", application.ErrInvalidCursor, 400}} {
		t.Run(test.name, func(t *testing.T) {
			response := homeResponse(t, &homeFeedStub{err: test.err}, &homeSearchStub{}, "/")
			if response.Code != test.status || strings.Contains(response.Body.String(), "private storage detail") || strings.Contains(response.Body.String(), "ga-arena-card\"") {
				t.Fatalf("failure = %d, body %.300s", response.Code, response.Body.String())
			}
		})
	}
	response := homeResponse(t, &homeFeedStub{}, &homeSearchStub{}, "/unknown")
	if response.Code != 404 {
		t.Fatalf("unknown address = %d, want 404", response.Code)
	}
}

func TestHomeRefusesMissingDependenciesAndBrokenAssetBuild(t *testing.T) {
	if _, err := NewHomeHandler((*homeFeedStub)(nil), &homeSearchStub{}, assets.Manifest{}); err == nil {
		t.Fatal("typed nil feed was accepted")
	}
	handler, err := NewHomeHandler(&homeFeedStub{}, &homeSearchStub{}, assets.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHome(response, httptest.NewRequest("GET", "/", nil))
	if response.Code != 500 || strings.Contains(response.Body.String(), "<!doctype") {
		t.Fatal("broken asset build leaked a partial document")
	}
}
