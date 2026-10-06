// Composition of the Arena lifecycle JSON API (P49-T03): draft CRUD,
// publication spending one Arena Pass, closing, the public feed and
// document, and the public full-text search, over the same pool, cursor
// secret, session validator and security boundary as the other journeys.
//
// Why a third surface and not a wider account one: each family mounts only
// what it answers, whole or not at all. The publication reuses the
// existing pass bridge and transaction manager — the debit and the
// transition commit together or the draft stays a draft — and the HTML
// participation journey is preserved untouched. No rule is reimplemented
// here; eligibility, cost and cursor honesty stay in the modules that own
// them.
package bootstrap

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/billingpass"
	arenashtml "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/html"
	arenashttp "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/http"
	arenaspostgres "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	billingpostgres "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/sessionvalidator"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
	searchhttp "github.com/AlexandreZanata/Regnovum/internal/search/adapters/http"
	searchpostgres "github.com/AlexandreZanata/Regnovum/internal/search/adapters/postgres"
	searchapp "github.com/AlexandreZanata/Regnovum/internal/search/application"
)

// ArenaSurface is the composed Arena lifecycle surface, ready to be mounted
// on the platform mux beside the other journeys.
type ArenaSurface struct {
	routes   []httpserver.Route
	register func(mux *http.ServeMux)

	mu      sync.Mutex
	mounted bool
}

// arenaHandlers are the three handlers of the surface, sharing one pool,
// one security boundary and one session validator.
type arenaHandlers struct {
	arenas   *arenashttp.Handler
	search   *searchhttp.Handler
	document *arenashtml.Handler
}

// arenaDeps are the shared edges the handlers are built from. One struct
// keeps the composers under the parameter budget.
type arenaDeps struct {
	arenas    *arenaspostgres.Repository
	billing   *billingpostgres.Repository
	search    *searchpostgres.Repository
	manager   *security.Manager
	validator security.SessionValidator
	risk      turnstile.Challenger
}

// documentRoute is the cacheable public Arena document the HTML read
// handler answers. It lives in the HTML routes next to the participation
// journey, and this surface is what mounts it.
var documentRoute = httpserver.Route{Method: http.MethodGet, Path: "/d/{slug}"}

// ComposeArenaLifecycle builds the Arena lifecycle surface: drafts,
// publication, closing, feed, public document and search, over the real
// repositories, the shared security manager and the shared session
// validator.
//
// It fails closed: without the cursor signing secret neither the feed nor
// the search can paginate honestly, so the refusal names it instead of
// mounting a surface whose cursor any client could forge.
func ComposeArenaLifecycle(options Options) (*ArenaSurface, error) {
	if err := validateArena(options); err != nil {
		return nil, err
	}
	manager, err := options.securityManager()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: arena lifecycle: security manager: %w", err)
	}
	validator, err := composeArenaValidator(options)
	if err != nil {
		return nil, err
	}
	handlers, err := buildArenaHandlers(options, arenaDeps{
		arenas:    arenaspostgres.NewRepository(options.Pool),
		billing:   billingpostgres.NewRepository(options.Pool),
		search:    searchpostgres.NewRepository(options.Pool),
		manager:   manager,
		validator: validator,
		risk:      composeArenaRisk(options),
	})
	if err != nil {
		return nil, err
	}
	routes := append(arenashttp.Routes(), searchhttp.Routes()...)
	routes = append(routes, documentRoute)
	options.Logger.Info("arena lifecycle: composed",
		slog.String("env", string(options.Env)),
		slog.Int("routes", len(routes)),
	)
	return &ArenaSurface{
		routes: routes,
		register: func(mux *http.ServeMux) {
			handlers.arenas.RegisterRoutes(mux)
			handlers.search.RegisterRoutes(mux)
			handlers.document.RegisterRoutes(mux)
		},
	}, nil
}

// composeArenaValidator resolves the session cookie through the identity
// module: one validator for every private route of the surface, over the
// same pool the login wrote to.
func composeArenaValidator(options Options) (security.SessionValidator, error) {
	identityRepository := identitypostgres.NewRepository(options.Pool)
	authenticate := identityapp.NewAuthenticateSessionUseCase(
		identityRepository, identityRepository, options.Clock, identitydomain.DefaultSessionPolicy(), 0,
	)
	validator, err := sessionvalidator.New(authenticate)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: arena lifecycle: session validator: %w", err)
	}
	return validator, nil
}

// composeArenaRisk observes publication outcomes for the risk signal. Its
// verifier is deliberately nil: the JSON API never presents a challenge
// widget, and a verifier installed here would be a dependency nothing can
// reach — the same contract the account journey composes.
func composeArenaRisk(options Options) turnstile.Challenger {
	resolver := clientip.New(nil)
	return turnstile.NewEnforcer(
		turnstile.Config{},
		nil,
		turnstile.NewFailureTracker(turnstile.FailureTrackerOptions{Now: options.Clock.Now}),
		resolver,
	)
}

// buildArenaHandlers wires the three handlers over the shared edges.
func buildArenaHandlers(options Options, deps arenaDeps) (*arenaHandlers, error) {
	feedCodec, err := arenasapp.NewFeedCursorCodec(options.CursorSecret)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: arena lifecycle: feed cursor: %w", err)
	}
	searchCodec, err := searchapp.NewCursorCodec(options.CursorSecret)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: arena lifecycle: search cursor: %w", err)
	}
	policy := arenasdomain.DefaultStatementPolicy()
	uow := platformpg.NewTxManager(options.Pool)
	arenaSearch, err := searchapp.NewArenaSearchUseCase(deps.search, searchCodec)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: arena lifecycle: arena search: %w", err)
	}
	argumentSearch, err := searchapp.NewArgumentSearchUseCase(deps.search, searchCodec)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: arena lifecycle: argument search: %w", err)
	}
	arenasHandler := arenashttp.NewHandler(arenashttp.HandlerConfig{
		CreateDraftUseCase: arenasapp.NewCreateArenaDraftUseCase(deps.arenas, policy),
		GetDraftUseCase:    arenasapp.NewGetArenaDraftUseCase(deps.arenas),
		ListDraftsUseCase:  arenasapp.NewListArenaDraftsUseCase(deps.arenas),
		UpdateDraftUseCase: arenasapp.NewUpdateArenaDraftUseCase(deps.arenas, policy),
		DeleteDraftUseCase: arenasapp.NewDeleteArenaDraftUseCase(deps.arenas),
		PublishUseCase: arenasapp.NewPublishArenaUseCase(
			deps.arenas, billingpass.New(deps.billing, options.Clock), uow, options.Clock,
		),
		CloseUseCase:     arenasapp.NewCloseArenaUseCase(deps.arenas),
		FeedUseCase:      arenasapp.NewGetArenaFeedUseCase(deps.arenas, feedCodec),
		GetPublicUseCase: arenasapp.NewGetPublicArenaUseCase(deps.arenas),
		SecurityManager:  deps.manager,
		Challenge:        deps.risk,
		SessionValidator: deps.validator,
	})
	searchHandler := searchhttp.NewHandler(searchhttp.HandlerConfig{
		Arenas:    arenaSearch,
		Arguments: argumentSearch,
	})
	documentHandler := arenashtml.NewHandler(arenashtml.HandlerConfig{
		GetDocumentUseCase: arenasapp.NewGetArenaDocumentUseCase(deps.arenas),
		Templates:          arenashtml.NewTemplates(),
	})
	return &arenaHandlers{
		arenas:   arenasHandler,
		search:   searchHandler,
		document: documentHandler,
	}, nil
}

// Routes is the canonical route list of the surface.
func (surface *ArenaSurface) Routes() []httpserver.Route {
	return append([]httpserver.Route(nil), surface.routes...)
}

// Surface is what the platform router mounts.
func (surface *ArenaSurface) Surface() httpserver.Surface {
	return httpserver.Surface{Routes: surface.Routes(), Register: surface.Mount}
}

// Mount registers the surface on the mux, refusing a second mount instead
// of panicking on duplicate patterns.
func (surface *ArenaSurface) Mount(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("%w: arena lifecycle: mux is required", ErrIncompleteComposition)
	}
	surface.mu.Lock()
	defer surface.mu.Unlock()
	if surface.mounted {
		return fmt.Errorf("%w: arena lifecycle is already mounted; mounting it again would register duplicate patterns", ErrIncompleteComposition)
	}
	surface.register(mux)
	surface.mounted = true
	return nil
}

// validateArena reports every missing edge at once, sorted. Like the
// privacy surface this one serves JSON plus one cacheable document, so it
// needs no asset manifest — but without the cursor secret neither the feed
// nor the search could paginate honestly.
func validateArena(options Options) error {
	missing := make([]string, 0, 4)
	if options.Logger == nil {
		missing = append(missing, "logger")
	}
	if options.Clock == nil {
		missing = append(missing, "clock")
	}
	if options.Pool == nil {
		missing = append(missing, "postgres pool")
	}
	if len(options.CursorSecret) == 0 {
		missing = append(missing, "cursor signing secret")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: arena lifecycle: missing %s", ErrIncompleteComposition, strings.Join(missing, ", "))
	}
	switch options.Env {
	case config.EnvDevelopment, config.EnvTest, config.EnvProduction:
		return nil
	default:
		return fmt.Errorf("%w: arena lifecycle: environment %q is not one of development, test, production", ErrIncompleteComposition, options.Env)
	}
}
