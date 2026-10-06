// Composition of the private entitlement reads (P49-T05): the Arena Pass
// summary, the paginated consumption history and the Member subscription
// projection, over the same pool, cursor secret, session validator and
// security boundary as the account journey.
//
// Why a fifth surface and not a wider one: each family mounts only what it
// answers, whole or not at all. This surface mounts reads and only reads —
// checkout and the hosted portal are guarded writes and land in P49-T06, so
// no write path is reachable from here. Nothing grants a benefit on a read:
// both projections are derived from rows already written by the audited use
// cases, the webhook mirror and the worker, and a reload of any of these
// routes returns the same stored state instead of extending a pass.
package bootstrap

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	billinghttp "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	billingpostgres "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/sessionvalidator"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// EntitlementSurface is the composed read-only entitlement surface, ready to
// be mounted on the platform mux beside the other journeys.
type EntitlementSurface struct {
	routes   []httpserver.Route
	register func(mux *http.ServeMux)

	mu      sync.Mutex
	mounted bool
}

// entitlementHandlers are the two handlers of the surface: the pass reads and
// the subscription read, sharing one pool, one security boundary and one
// session validator.
type entitlementHandlers struct {
	passes     *billinghttp.Handler
	subscribes *billinghttp.BillingHandler
}

// entitlementReadRoutes are the three reads this surface mounts. They repeat
// the canonical billing declarations (no second source of truth is scanned:
// the P48 inventory collects route literals from the adapter routes files
// only), so the surface answers exactly the reads and never a write.
var entitlementReadRoutes = []httpserver.Route{
	{Method: http.MethodGet, Path: "/api/v1/me/passes"},
	{Method: http.MethodGet, Path: "/api/v1/me/passes/history"},
	{Method: http.MethodGet, Path: "/api/v1/me/billing/subscription"},
}

// ComposeEntitlementReads builds the entitlement read surface: the pass
// summary, the paginated history and the subscription projection, over the
// real PostgreSQL repository, the shared security manager and the shared
// session validator.
//
// It fails closed: without the cursor signing secret the history cannot
// paginate honestly, so the refusal names it instead of mounting a surface
// whose cursor any client could forge.
func ComposeEntitlementReads(options Options) (*EntitlementSurface, error) {
	if err := validateEntitlement(options); err != nil {
		return nil, err
	}
	manager, err := options.securityManager()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: entitlement reads: security manager: %w", err)
	}
	validator, err := composeEntitlementValidator(options)
	if err != nil {
		return nil, err
	}
	handlers, err := buildEntitlementHandlers(options, billingpostgres.NewRepository(options.Pool), manager, validator)
	if err != nil {
		return nil, err
	}
	routes := append([]httpserver.Route(nil), entitlementReadRoutes...)
	options.Logger.Info("entitlement reads: composed",
		slog.String("env", string(options.Env)),
		slog.Int("routes", len(routes)),
	)
	return &EntitlementSurface{
		routes: routes,
		register: func(mux *http.ServeMux) {
			handlers.passes.RegisterRoutes(mux)
			handlers.subscribes.RegisterSubscriptionReadRoutes(mux)
		},
	}, nil
}

// buildEntitlementHandlers wires the two handlers over the shared edges: the
// same pool the account journey wrote to, the same security boundary, and one
// session validator for every private route.
func buildEntitlementHandlers(options Options, repository *billingpostgres.Repository, manager *security.Manager, validator security.SessionValidator) (*entitlementHandlers, error) {
	cursors, err := billingapp.NewHistoryCursorCodec(options.CursorSecret)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: entitlement reads: history cursor: %w", err)
	}
	passes := billinghttp.NewHandler(billinghttp.HandlerConfig{
		GetArenaPassSummaryUseCase: billingapp.NewGetArenaPassSummaryUseCase(repository, options.Clock),
		GetArenaPassHistoryUseCase: billingapp.NewGetArenaPassHistoryUseCase(repository, cursors),
		SecurityManager:            manager,
		SessionValidator:           validator,
	})
	subscribes := billinghttp.NewBillingHandler(billinghttp.BillingHandlerConfig{
		GetSubscriptionStatus: billingapp.NewGetSubscriptionStatusUseCase(repository),
		SecurityManager:       manager,
		SessionValidator:      validator,
	})
	return &entitlementHandlers{passes: passes, subscribes: subscribes}, nil
}

// composeEntitlementValidator resolves the session cookie through the
// identity module: one validator for every private route of the surface,
// over the same pool the login wrote to.
func composeEntitlementValidator(options Options) (security.SessionValidator, error) {
	identityRepository := identitypostgres.NewRepository(options.Pool)
	authenticate := identityapp.NewAuthenticateSessionUseCase(
		identityRepository, identityRepository, options.Clock, identitydomain.DefaultSessionPolicy(), 0,
	)
	validator, err := sessionvalidator.New(authenticate)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: entitlement reads: session validator: %w", err)
	}
	return validator, nil
}

// Routes is the canonical route list of the surface.
func (surface *EntitlementSurface) Routes() []httpserver.Route {
	return append([]httpserver.Route(nil), surface.routes...)
}

// Surface is what the platform router mounts.
func (surface *EntitlementSurface) Surface() httpserver.Surface {
	return httpserver.Surface{Routes: surface.Routes(), Register: surface.Mount}
}

// Mount registers the surface on the mux, refusing a second mount instead of
// panicking on duplicate patterns.
func (surface *EntitlementSurface) Mount(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("%w: entitlement reads: mux is required", ErrIncompleteComposition)
	}
	surface.mu.Lock()
	defer surface.mu.Unlock()
	if surface.mounted {
		return fmt.Errorf("%w: entitlement reads are already mounted; mounting them again would register duplicate patterns", ErrIncompleteComposition)
	}
	surface.register(mux)
	surface.mounted = true
	return nil
}

// validateEntitlement reports every missing edge at once, sorted, because an
// operator fixing a boot failure should not discover them one per attempt.
// Like the other JSON-only surfaces this one serves no documents, so it needs
// no asset manifest — but without the cursor secret the history cursor would
// be forgeable, so the secret is required here and not merely preferred.
func validateEntitlement(options Options) error {
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
		return fmt.Errorf("%w: entitlement reads: missing %s", ErrIncompleteComposition, strings.Join(missing, ", "))
	}
	switch options.Env {
	case config.EnvDevelopment, config.EnvTest, config.EnvProduction:
		return nil
	default:
		return fmt.Errorf("%w: entitlement reads: environment %q is not one of development, test, production", ErrIncompleteComposition, options.Env)
	}
}
