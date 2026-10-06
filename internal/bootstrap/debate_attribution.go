// Composition of the debate JSON API (P49-T04): positions, arguments and
// persuasion over the same pool, cursor secret, session validator and
// security boundary as the other journeys.
//
// Why a fourth surface and not a wider lifecycle one: each family mounts
// only what it answers, whole or not at all. The publication reuses the
// existing INK debit bridge and transaction manager — the charge and the
// write commit together or the argument is never published — and the HTML
// participation journey is preserved untouched. No rule is reimplemented
// here; eligibility, cost, cursor honesty and the moderator-only signals
// gate stay in the modules that own them. The signals use case asks its
// own authorizer port, answered here from the moderation role store; case
// competence, claims and decisions stay with P49-T07.
package bootstrap

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	arenaspostgres "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	argumentseligibility "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/eligibility"
	argumentshttp "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/http"
	argumentspostgres "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	argumentswalletdebit "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/walletdebit"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/sessionvalidator"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	moderationpostgres "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	persuasionhttp "github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/moderationpass"
	persuasionpostgres "github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/postgres"
	persuasionapp "github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	persuasiondomain "github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	positionseligibility "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/eligibility"
	positionshttp "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/http"
	positionspostgres "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/postgres"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	walletpostgres "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
)

// DebateSurface is the composed debate surface, ready to be mounted on the
// platform mux beside the other journeys.
type DebateSurface struct {
	routes   []httpserver.Route
	register func(mux *http.ServeMux)

	mu      sync.Mutex
	mounted bool
}

// debateHandlers are the three handlers of the surface, sharing one pool,
// one security boundary and one session validator.
type debateHandlers struct {
	positions  *positionshttp.Handler
	arguments  *argumentshttp.Handler
	persuasion *persuasionhttp.Handler
}

// debateDeps are the shared edges the handlers are built from. One struct
// keeps the composers under the parameter budget.
type debateDeps struct {
	arenas     *arenaspostgres.Repository
	identity   *identitypostgres.Repository
	positions  *positionspostgres.Repository
	arguments  *argumentspostgres.Repository
	persuasion *persuasionpostgres.Repository
	wallet     *walletpostgres.Repository
	moderation *moderationpostgres.Repository
	manager    *security.Manager
	validator  security.SessionValidator
	throttle   ratelimit.Protector
}

// ComposeDebateAttribution builds the debate surface: positions, arguments
// and persuasion over the real repositories, the shared security manager
// and the shared session validator.
//
// It fails closed: without the cursor signing secret neither argument list
// can paginate honestly, so the refusal names it instead of mounting a
// surface whose cursor any client could forge.
func ComposeDebateAttribution(options Options) (*DebateSurface, error) {
	if err := validateDebate(options); err != nil {
		return nil, err
	}
	manager, err := options.securityManager()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: debate: security manager: %w", err)
	}
	validator, err := composeDebateValidator(options)
	if err != nil {
		return nil, err
	}
	handlers, err := buildDebateHandlers(options, debateDeps{
		arenas:     arenaspostgres.NewRepository(options.Pool),
		identity:   identitypostgres.NewRepository(options.Pool),
		positions:  positionspostgres.NewRepository(options.Pool),
		arguments:  argumentspostgres.NewRepository(options.Pool),
		persuasion: persuasionpostgres.NewRepository(options.Pool),
		wallet:     walletpostgres.NewRepository(options.Pool),
		moderation: moderationpostgres.NewRepository(options.Pool),
		manager:    manager,
		validator:  validator,
		throttle:   composeDebateThrottle(options),
	})
	if err != nil {
		return nil, err
	}
	routes := append(positionshttp.Routes(), argumentshttp.Routes()...)
	routes = append(routes, persuasionhttp.Routes()...)
	options.Logger.Info("debate: composed",
		slog.String("env", string(options.Env)),
		slog.Int("routes", len(routes)),
	)
	return &DebateSurface{
		routes: routes,
		register: func(mux *http.ServeMux) {
			handlers.positions.RegisterRoutes(mux)
			handlers.arguments.RegisterRoutes(mux)
			handlers.persuasion.RegisterRoutes(mux)
		},
	}, nil
}

// composeDebateValidator resolves the session cookie through the identity
// module: one validator for every private route of the surface, over the
// same pool the login wrote to.
func composeDebateValidator(options Options) (security.SessionValidator, error) {
	identityRepository := identitypostgres.NewRepository(options.Pool)
	authenticate := identityapp.NewAuthenticateSessionUseCase(
		identityRepository, identityRepository, options.Clock, identitydomain.DefaultSessionPolicy(), 0,
	)
	validator, err := sessionvalidator.New(authenticate)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: debate: session validator: %w", err)
	}
	return validator, nil
}

// composeDebateThrottle bounds the debate writes per account and address,
// over the same action vocabulary the participation journey throttles.
func composeDebateThrottle(options Options) ratelimit.Protector {
	resolver := clientip.New(nil)
	return ratelimit.New(ratelimit.NewLimiter(ratelimit.Options{Now: options.Clock.Now}), resolver)
}

// buildDebateHandlers wires the three handlers over the shared edges,
// reusing the eligibility bridges, the INK debit and the transaction
// manager the participation journey already composes.
func buildDebateHandlers(options Options, deps debateDeps) (*debateHandlers, error) {
	cursors, err := argumentsapp.NewArgumentCursorCodec(options.CursorSecret)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: debate: argument cursor: %w", err)
	}
	positionEligibility, err := positionseligibility.New(deps.identity, deps.arenas)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: debate: position eligibility: %w", err)
	}
	argumentEligibility, err := argumentseligibility.New(deps.identity, deps.arenas)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: debate: argument eligibility: %w", err)
	}
	uow := platformpg.NewTxManager(options.Pool)
	attributionPolicy := persuasiondomain.DefaultEligibilityPolicy()
	positionsHandler := positionshttp.NewHandler(positionshttp.HandlerConfig{
		ConfirmUseCase: positionsapp.NewConfirmInitialPositionUseCase(
			deps.positions, positionEligibility, positionEligibility, options.Clock,
		),
		ChangeUseCase: positionsapp.NewChangePositionUseCase(
			deps.positions, positionEligibility, uow, options.Clock,
		),
		GetMineUseCase:  positionsapp.NewGetMyPositionUseCase(deps.positions),
		ListMineUseCase: positionsapp.NewListPositionChangesUseCase(deps.positions),
		AggregateUseCase: positionsapp.NewGetPositionAggregateUseCase(
			deps.positions, positionsdomain.DefaultAggregatePolicy(), options.Clock,
		),
		SecurityManager:  deps.manager,
		RateLimit:        deps.throttle,
		SessionValidator: deps.validator,
	})
	argumentsHandler := argumentshttp.NewHandler(argumentshttp.HandlerConfig{
		PublishUseCase: argumentsapp.NewPublishArgumentUseCase(
			deps.arguments,
			argumentEligibility,
			argumentEligibility,
			argumentswalletdebit.New(walletapp.NewDebitInkUseCase(deps.wallet, options.Clock)),
			uow,
			text.GraphemeCount,
			argumentsdomain.DefaultReplyPolicy(),
			options.Clock,
		),
		WithdrawUseCase:  argumentsapp.NewWithdrawArgumentUseCase(deps.arguments, options.Clock),
		ListArenaUseCase: argumentsapp.NewListArenaArgumentsUseCase(deps.arguments, cursors),
		ListRepliesCase:  argumentsapp.NewListRepliesUseCase(deps.arguments, cursors),
		GetPublicUseCase: argumentsapp.NewGetPublicArgumentUseCase(deps.arguments),
		SecurityManager:  deps.manager,
		RateLimit:        deps.throttle,
		SessionValidator: deps.validator,
	})
	persuasionHandler := persuasionhttp.NewHandler(persuasionhttp.HandlerConfig{
		RecordUseCase: persuasionapp.NewRecordAttributionsUseCase(
			deps.persuasion, attributionPolicy, uow,
		),
		ArgumentMetricsUseCase: persuasionapp.NewGetArgumentMetricsUseCase(deps.persuasion, options.Clock),
		ProfileReputationCase:  persuasionapp.NewGetProfileReputationUseCase(deps.persuasion, deps.persuasion, options.Clock),
		SignalsUseCase: persuasionapp.NewGetAttributionSignalsUseCase(
			deps.persuasion, moderationpass.New(deps.moderation), persuasiondomain.DefaultSignalPolicy(), options.Clock,
		),
		SecurityManager:  deps.manager,
		SessionValidator: deps.validator,
	})
	return &debateHandlers{
		positions:  positionsHandler,
		arguments:  argumentsHandler,
		persuasion: persuasionHandler,
	}, nil
}

// Routes is the canonical route list of the surface.
func (surface *DebateSurface) Routes() []httpserver.Route {
	return append([]httpserver.Route(nil), surface.routes...)
}

// Surface is what the platform router mounts.
func (surface *DebateSurface) Surface() httpserver.Surface {
	return httpserver.Surface{Routes: surface.Routes(), Register: surface.Mount}
}

// Mount registers the surface on the mux, refusing a second mount instead
// of panicking on duplicate patterns.
func (surface *DebateSurface) Mount(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("%w: debate: mux is required", ErrIncompleteComposition)
	}
	surface.mu.Lock()
	defer surface.mu.Unlock()
	if surface.mounted {
		return fmt.Errorf("%w: debate is already mounted; mounting it again would register duplicate patterns", ErrIncompleteComposition)
	}
	surface.register(mux)
	surface.mounted = true
	return nil
}

// validateDebate reports every missing edge at once, sorted. Like the
// lifecycle surface this one serves JSON only, so it needs no asset
// manifest — but without the cursor secret neither argument list could
// paginate honestly.
func validateDebate(options Options) error {
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
		return fmt.Errorf("%w: debate: missing %s", ErrIncompleteComposition, strings.Join(missing, ", "))
	}
	switch options.Env {
	case config.EnvDevelopment, config.EnvTest, config.EnvProduction:
		return nil
	default:
		return fmt.Errorf("%w: debate: environment %q is not one of development, test, production", ErrIncompleteComposition, options.Env)
	}
}
