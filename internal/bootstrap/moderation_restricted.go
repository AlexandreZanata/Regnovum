// Composition of the restricted moderation API (P49-T07): user
// report/appeal filing plus the case queue, claim and decision routes, over
// the same pool, cursor secret, session validator and security boundary as
// the account journey.
//
// Why a surface of its own: moderation answers two audiences with different
// gates. Filing is open to any authenticated account; the triage routes
// additionally require an active assignment and a recently stepped-up
// session, and those facts are read from the role store and the session at
// request time — never granted here. No promotion route exists on this
// surface: the first-administrator bootstrap stays a host-only command
// (ComposeAdministration), and the browser cannot promote itself or anyone.
// The attribution signals the debate surface serves keep resolving against
// this same role store, so competence is one fact in both places.
package bootstrap

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/sessionvalidator"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	moderationhttp "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/http"
	moderationpostgres "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// ModerationSurface is the composed restricted moderation surface, ready to
// be mounted on the platform mux beside the other journeys.
type ModerationSurface struct {
	routes   []httpserver.Route
	register func(mux *http.ServeMux)

	mu      sync.Mutex
	mounted bool
}

// ComposeModeration builds the moderation surface: report/appeal filing and
// the restricted triage routes, over the real PostgreSQL repository, the
// shared security manager and the shared session validator.
//
// It fails closed: without the cursor signing secret the queue cannot
// paginate honestly, so the refusal names it instead of mounting a surface
// whose cursor any client could forge.
func ComposeModeration(options Options) (*ModerationSurface, error) {
	if err := validateModeration(options); err != nil {
		return nil, err
	}
	manager, err := options.securityManager()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: moderation: security manager: %w", err)
	}
	validator, err := composeModerationValidator(options)
	if err != nil {
		return nil, err
	}
	handler, err := buildModerationHandler(options, moderationpostgres.NewRepository(options.Pool), manager, validator)
	if err != nil {
		return nil, err
	}
	routes := append([]httpserver.Route(nil), moderationhttp.Routes()...)
	options.Logger.Info("moderation: composed",
		slog.String("env", string(options.Env)),
		slog.Int("routes", len(routes)),
	)
	return &ModerationSurface{
		routes: routes,
		register: func(mux *http.ServeMux) {
			handler.RegisterRoutes(mux)
		},
	}, nil
}

// buildModerationHandler wires the moderation handler over the shared edges:
// the same pool the account journey wrote to, the same security boundary,
// and one session validator for every route. The role store answers both
// the triage gate here and the attribution signals the debate surface
// serves, so competence cannot disagree between them.
func buildModerationHandler(options Options, repository *moderationpostgres.Repository, manager *security.Manager, validator security.SessionValidator) (*moderationhttp.Handler, error) {
	codec, err := moderationapp.NewQueueCursorCodec(options.CursorSecret)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: moderation: queue cursor: %w", err)
	}
	authorizer, err := moderationapp.NewAuthorizer(repository, options.Clock)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: moderation: authorizer: %w", err)
	}
	fileReport, err := moderationapp.NewFileReportUseCase(moderationapp.FileReportDependencies{
		Targets: repository, Reports: repository, Clock: options.Clock,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: moderation: report filing: %w", err)
	}
	fileAppeal, err := moderationapp.NewFileAppealUseCase(moderationapp.AppealDependencies{
		Appeals: repository, Clock: options.Clock,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: moderation: appeal filing: %w", err)
	}
	getQueue, err := moderationapp.NewGetCaseQueueUseCase(repository, repository)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: moderation: case queue: %w", err)
	}
	claimCase, err := moderationapp.NewClaimCaseUseCase(moderationapp.ReviewDependencies{
		Cases: repository, Authorizer: authorizer, Clock: options.Clock,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: moderation: case claim: %w", err)
	}
	decideCase, err := moderationapp.NewDecideCaseUseCase(moderationapp.ReviewDependencies{
		Cases: repository, Authorizer: authorizer, Clock: options.Clock,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: moderation: case decision: %w", err)
	}
	resolver := clientip.New(nil)
	return moderationhttp.NewHandler(moderationhttp.HandlerConfig{
		FileReport:       fileReport,
		FileAppeal:       fileAppeal,
		GetQueue:         getQueue,
		ClaimCase:        claimCase,
		DecideCase:       decideCase,
		Roles:            repository,
		Sessions:         repository,
		QueueCodec:       codec,
		SecurityManager:  manager,
		Clock:            options.Clock,
		RateLimit:        ratelimit.New(ratelimit.NewLimiter(ratelimit.Options{Now: options.Clock.Now}), resolver),
		SessionValidator: validator,
	}), nil
}

// composeModerationValidator resolves the session cookie through the
// identity module: one validator for every route of the surface, over the
// same pool the login wrote to.
func composeModerationValidator(options Options) (security.SessionValidator, error) {
	identityRepository := identitypostgres.NewRepository(options.Pool)
	authenticate := identityapp.NewAuthenticateSessionUseCase(
		identityRepository, identityRepository, options.Clock, identitydomain.DefaultSessionPolicy(), 0,
	)
	validator, err := sessionvalidator.New(authenticate)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: moderation: session validator: %w", err)
	}
	return validator, nil
}

// Routes is the canonical route list of the surface.
func (surface *ModerationSurface) Routes() []httpserver.Route {
	return append([]httpserver.Route(nil), surface.routes...)
}

// Surface is what the platform router mounts.
func (surface *ModerationSurface) Surface() httpserver.Surface {
	return httpserver.Surface{Routes: surface.Routes(), Register: surface.Mount}
}

// Mount registers the surface on the mux, refusing a second mount instead of
// panicking on duplicate patterns.
func (surface *ModerationSurface) Mount(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("%w: moderation: mux is required", ErrIncompleteComposition)
	}
	surface.mu.Lock()
	defer surface.mu.Unlock()
	if surface.mounted {
		return fmt.Errorf("%w: moderation is already mounted; mounting it again would register duplicate patterns", ErrIncompleteComposition)
	}
	surface.register(mux)
	surface.mounted = true
	return nil
}

// validateModeration reports every missing edge at once, sorted, because an
// operator fixing a boot failure should not discover them one per attempt.
// Like the other JSON-only surfaces this one serves no documents, so it needs
// no asset manifest — but without the cursor secret the queue cursor would
// be forgeable, so the secret is required here and not merely preferred.
func validateModeration(options Options) error {
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
		return fmt.Errorf("%w: moderation: missing %s", ErrIncompleteComposition, strings.Join(missing, ", "))
	}
	switch options.Env {
	case config.EnvDevelopment, config.EnvTest, config.EnvProduction:
		return nil
	default:
		return fmt.Errorf("%w: moderation: environment %q is not one of development, test, production", ErrIncompleteComposition, options.Env)
	}
}
