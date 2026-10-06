// Composition of the public transparency surface (P49-T08): the versioned
// JSON metrics document, the localized HTML transparency document and the
// versioned public Arena export, over the same pool and clock the account
// journey wrote to.
//
// Why a surface of its own: transparency answers the public, not a person.
// Every route is unauthenticated and cacheable, so this surface takes no
// session validator and no security boundary — there is no identity to
// resolve and no cookie to verify. What it does take is the cursor signing
// secret: the export page cursor is signed, and without the secret the
// export pagination could be forged. The metrics and the document do not
// paginate, but the family mounts whole or not at all, so the secret gates
// the surface together with the pool.
//
// What is deliberately not here: metrics/pprof stay off this surface — a
// public listing is no reason to expose the process internals — and no
// private fact ever serializes. The projection carries suppressed integer
// counts only (domain.LowCountThreshold), the export withholds withdrawn
// content and sources, and the HTML locale negotiates against the
// allowlist with an explicit parameter winning: unknown values fall back
// to the product default without being reflected.
package bootstrap

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	transparencyhttp "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/http"
	transparencypostgres "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/postgres"
	transparencyapp "github.com/AlexandreZanata/Regnovum/internal/transparency/application"
)

// TransparencySurface is the composed public transparency surface, ready to
// be mounted on the platform mux beside the other journeys.
type TransparencySurface struct {
	routes   []httpserver.Route
	register func(mux *http.ServeMux)

	mu      sync.Mutex
	mounted bool
}

// ComposeTransparency builds the transparency surface: the JSON metrics
// document, the HTML document and the public Arena export, over the real
// PostgreSQL projection, the embedded document templates and the process
// clock.
//
// It fails closed: without the pool the projection cannot be derived, and
// without the cursor signing secret the export cursor would be forgeable,
// so either refusal names what is missing instead of mounting a surface
// that answers some documents and forges others.
func ComposeTransparency(options Options) (*TransparencySurface, error) {
	if err := validateTransparency(options); err != nil {
		return nil, err
	}
	repository := transparencypostgres.NewRepository(options.Pool)
	handlers, err := buildTransparencyHandlers(options, repository)
	if err != nil {
		return nil, err
	}
	routes := append([]httpserver.Route(nil), transparencyhttp.Routes()...)
	options.Logger.Info("transparency: composed",
		slog.String("env", string(options.Env)),
		slog.Int("routes", len(routes)),
	)
	return &TransparencySurface{
		routes: routes,
		register: func(mux *http.ServeMux) {
			handlers.documents.RegisterRoutes(mux)
			handlers.export.RegisterRoutes(mux)
		},
	}, nil
}

// transparencyHandlers are the two handlers of the surface: the metrics
// documents (JSON and HTML over the same derivation) and the public Arena
// export, sharing one pool, one projection and one clock.
type transparencyHandlers struct {
	documents *transparencyhttp.Handler
	export    *transparencyhttp.ExportHandler
}

// buildTransparencyHandlers wires the two handlers over the shared edges:
// the same pool the account journey wrote to and the same clock the
// derivation windows are anchored to. Nothing here invents a projection:
// the counts come from the source tables through the use cases the
// transparency module already owns.
func buildTransparencyHandlers(options Options, repository *transparencypostgres.Repository) (*transparencyHandlers, error) {
	derive, err := transparencyapp.NewDeriveMetricsUseCase(repository)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: transparency: metrics derivation: %w", err)
	}
	templates, err := transparencyhttp.NewTemplates()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: transparency: document templates: %w", err)
	}
	cursors, err := transparencyapp.NewExportCursorCodec(options.CursorSecret)
	if err != nil {
		return nil, fmt.Errorf("%w: transparency: cursor signing secret: %w", ErrIncompleteComposition, err)
	}
	getExport, err := transparencyapp.NewGetArenaExportUseCase(repository, cursors)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: transparency: arena export: %w", err)
	}
	return &transparencyHandlers{
		documents: transparencyhttp.NewHandler(transparencyhttp.HandlerConfig{
			Derive:    derive,
			Templates: templates,
			Clock:     options.Clock,
		}),
		export: transparencyhttp.NewExportHandler(transparencyhttp.ExportHandlerConfig{
			UseCase: getExport,
		}),
	}, nil
}

// Routes is the canonical route list of the surface.
func (surface *TransparencySurface) Routes() []httpserver.Route {
	return append([]httpserver.Route(nil), surface.routes...)
}

// Surface is what the platform router mounts.
func (surface *TransparencySurface) Surface() httpserver.Surface {
	return httpserver.Surface{Routes: surface.Routes(), Register: surface.Mount}
}

// Mount registers the surface on the mux, refusing a second mount instead of
// panicking on duplicate patterns.
func (surface *TransparencySurface) Mount(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("%w: transparency: mux is required", ErrIncompleteComposition)
	}
	surface.mu.Lock()
	defer surface.mu.Unlock()
	if surface.mounted {
		return fmt.Errorf("%w: transparency is already mounted; mounting it again would register duplicate patterns", ErrIncompleteComposition)
	}
	surface.register(mux)
	surface.mounted = true
	return nil
}

// validateTransparency reports every missing edge at once, sorted, because an
// operator fixing a boot failure should not discover them one per attempt.
// This surface serves no authenticated route and no document from the asset
// build, so it needs neither a session validator, nor a security boundary,
// nor an asset manifest — but without the cursor secret the export cursor
// would be forgeable, so the secret is required here and not merely
// preferred.
func validateTransparency(options Options) error {
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
		return fmt.Errorf("%w: transparency: missing %s", ErrIncompleteComposition, strings.Join(missing, ", "))
	}
	switch options.Env {
	case config.EnvDevelopment, config.EnvTest, config.EnvProduction:
		return nil
	default:
		return fmt.Errorf("%w: transparency: environment %q is not one of development, test, production", ErrIncompleteComposition, options.Env)
	}
}
