package html

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/websurface"
	searchapp "github.com/AlexandreZanata/Regnovum/internal/search/application"
)

//go:embed home/*.gohtml
var homeFiles embed.FS

// HomeFeed and HomeSearch keep the browser adapter on application ports.
type HomeFeed interface {
	Execute(context.Context, application.GetArenaFeedCommand) (*application.ArenaFeedPage, error)
}

type HomeSearch interface {
	Execute(context.Context, string, string, string, int) (*searchapp.ArenaPage, error)
}

// HomeHandler renders the functional entry point, including without JavaScript.
// It never reads position aggregates, wallet balances or role entitlements.
type HomeHandler struct {
	feed      HomeFeed
	search    HomeSearch
	templates *template.Template
}

type homeArena struct {
	Slug, Statement, Category, Language, Status, Scene string
}

type homeLink struct {
	Key, Href, Icon string
	Current         bool
}

type homeRole struct {
	ID, Icon string
}

type homeData struct {
	Locale, Language, Query, Category, Next, Error string
	Arenas                                         []homeArena
	Navigation, Categories                         []homeLink
	Roles, MoreRoles                               []homeRole
}

// NewHomeHandler composes templates without resolving an asset until rendering.
// Missing build assets therefore fail as a complete response, never half HTML.
func NewHomeHandler(feed HomeFeed, search HomeSearch, manifest assets.Manifest) (*HomeHandler, error) {
	if websurface.Missing(feed) || websurface.Missing(search) {
		return nil, fmt.Errorf("realm home: feed and search are required")
	}
	funcs := manifest.TemplateFuncs()
	for key, fn := range homeComponentFuncs() {
		funcs[key] = fn
	}
	funcs["t"] = func(locale, key string) (string, error) { return i18n.Message(locale, "server-home."+key) }
	funcs["icon"] = func(name string) (string, error) {
		path, err := manifest.URL("realm/icons/sprite.svg")
		return path + "#" + name, err
	}
	parsed, err := template.New("home").Funcs(funcs).ParseFS(homeFiles, "home/*.gohtml")
	if err != nil {
		return nil, fmt.Errorf("realm home: templates: %w", err)
	}
	return &HomeHandler{feed: feed, search: search, templates: parsed}, nil
}

func (h *HomeHandler) RegisterRoutes(mux *http.ServeMux) {
	// Exact root: an unknown path must not silently render the dashboard.
	mux.HandleFunc("GET /{$}", h.ServeHome)
}

func (h *HomeHandler) ServeHome(w http.ResponseWriter, r *http.Request) {
	data, status := newHomeData(r), http.StatusOK
	if err := h.load(r.Context(), &data, r.URL.Query().Get("cursor")); err != nil {
		data.Error, status = "load_error", http.StatusServiceUnavailable
		if errors.Is(err, application.ErrInvalidCursor) || errors.Is(err, searchapp.ErrInvalidCursor) || errors.Is(err, searchapp.ErrInvalidQuery) || errors.Is(err, domain.ErrInvalidCategory) || errors.Is(err, domain.ErrInvalidLanguage) || errors.Is(err, domain.ErrInvalidStatus) {
			status = http.StatusBadRequest
			data.Error = "invalid_query"
		}
	}
	body, err := websurface.Document(func(writer io.Writer) error {
		return h.templates.ExecuteTemplate(writer, "home", data)
	})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	websurface.WritePrivate(w, status, body)
}

func newHomeData(r *http.Request) homeData {
	locale := websurface.Locale(r)
	if lang := r.URL.Query().Get("locale"); lang == "pt-BR" || lang == "en-US" {
		locale = lang
	}
	data := homeData{Locale: locale, Language: r.URL.Query().Get("language"), Query: strings.TrimSpace(r.URL.Query().Get("q")), Category: r.URL.Query().Get("category")}
	data.Navigation = []homeLink{
		{Key: "nav.home", Href: "/", Icon: "home", Current: true},
		{Key: "nav.arenas", Href: "#arenas", Icon: "swords"},
		{Key: "nav.disputes", Href: "#disputes", Icon: "scales"},
		{Key: "nav.contracts", Href: "#contracts", Icon: "scroll"},
		{Key: "nav.roles", Href: "#roles", Icon: "crown"},
		{Key: "nav.transparency", Href: "/transparency", Icon: "audit"},
	}
	for _, key := range []string{"", "politics", "economics", "society", "technology", "science", "philosophy", "health", "culture"} {
		label := key
		if label == "" {
			label = "all"
		}
		query := url.Values{"locale": {locale}, "language": {data.Language}, "category": {key}}
		data.Categories = append(data.Categories, homeLink{Key: "categories." + label, Href: "/?" + query.Encode() + "#arenas", Current: data.Category == key})
	}
	data.Roles = []homeRole{{"inquisidor", "investigate"}, {"arbitro", "scales"}, {"conselheiro", "scroll"}, {"carrasco", "axe"}, {"campeao", "laurel"}}
	data.MoreRoles = []homeRole{{"rei", "crown"}, {"justiceiro", "lantern"}, {"defensor", "shield"}, {"testemunha", "eye"}, {"jurado", "jury"}, {"fiador-segurador", "handshake"}, {"oraculo", "compass"}, {"escrivao", "quill"}, {"auditor", "audit"}, {"cidadao", "citizen"}, {"campones", "wheat"}, {"senhor-feudo", "castle"}}
	return data
}

func (h *HomeHandler) load(ctx context.Context, data *homeData, cursor string) error {
	if data.Query != "" {
		return h.loadSearch(ctx, data, cursor)
	}
	page, err := h.feed.Execute(ctx, application.GetArenaFeedCommand{Language: data.Language, Category: data.Category, Cursor: cursor, Limit: 8})
	if err != nil {
		return err
	}
	for _, arena := range page.Arenas {
		data.Arenas = append(data.Arenas, homeArena{Slug: arena.Slug().String(), Statement: arena.Statement().String(), Category: arena.Category().String(), Language: arena.Language().String(), Status: arena.Status().String(), Scene: homeScene(arena.Category().String())})
	}
	data.Next = homeNext(data, page.NextCursor)
	return nil
}

func (h *HomeHandler) loadSearch(ctx context.Context, data *homeData, cursor string) error {
	page, err := h.search.Execute(ctx, data.Query, data.Language, cursor, 8)
	if err != nil {
		return err
	}
	for _, arena := range page.Items {
		data.Arenas = append(data.Arenas, homeArena{Slug: arena.Slug, Statement: arena.Statement, Category: arena.Category, Language: arena.Language, Status: "published", Scene: homeScene(arena.Category)})
	}
	data.Next = homeNext(data, page.NextCursor)
	return nil
}

func homeNext(data *homeData, cursor string) string {
	if cursor == "" {
		return ""
	}
	return "/?" + url.Values{"locale": {data.Locale}, "language": {data.Language}, "category": {data.Category}, "q": {data.Query}, "cursor": {cursor}}.Encode() + "#arenas"
}

func homeScene(category string) string {
	scenes := map[string]string{"economics": "mercado-contratos", "science": "arquivo-real", "philosophy": "sala-arbitragem", "technology": "arquivo-real", "society": "sala-arbitragem"}
	if scene := scenes[category]; scene != "" {
		return "realm/scenes/" + scene + "-768.webp"
	}
	return "realm/scenes/arena-debate-768.webp"
}
