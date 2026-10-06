// Composition of the guarded job operation surface (P49-T09): queue
// health, dead-job listing and single retry over the same pool, clock and
// security boundary as the account journey.
//
// Why a surface of its own: jobs answer operators, not people. Reading the
// queue requires an active administrative assignment; retrying one dead job
// additionally requires a recently stepped-up session (server-observed age,
// never a client claim), and the retry and its audit record commit in one
// transaction. No retry is public, no payload ever serializes, and no DSN
// or provider secret travels here — the composition never logs the pool
// address and the statements never select the parameters column.
//
// What is deliberately not here: the worker rule is untouched (no new
// workload becomes retryable to obtain success), staged modules stay
// unmounted, and there is no cursor secret — the dead listing is a bounded
// limit page, not a signed cursor walk.
package bootstrap

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	auditpostgres "github.com/AlexandreZanata/Regnovum/internal/audit/adapters/postgres"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/sessionvalidator"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/auditbridge"
	jobshttp "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/operatorbridge"
	jobspostgres "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	moderationpostgres "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// JobsSurface is the composed guarded job operation surface, ready to be
// mounted on the platform mux beside the other journeys.
type JobsSurface struct {
	routes   []httpserver.Route
	register func(mux *http.ServeMux)

	mu      sync.Mutex
	mounted bool
}

// ComposeJobs builds the job operation surface: queue health, dead listing
// and single retry over the real PostgreSQL queue, the role store that
// answers the operator question and the audit trail that records the retry.
//
// It fails closed: without the pool the queue cannot be read, without the
// clock the waits cannot be measured, and without the security boundary the
// operator cannot be resolved — so the refusal names what is missing
// instead of mounting a surface that answers some operations and forges
// others.
func ComposeJobs(options Options) (*JobsSurface, error) {
	if err := validateJobs(options); err != nil {
		return nil, err
	}
	manager, err := options.securityManager()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: jobs: security manager: %w", err)
	}
	validator, err := composeJobsValidator(options)
	if err != nil {
		return nil, err
	}
	handler, err := buildJobsHandler(options, manager)
	if err != nil {
		return nil, err
	}
	routes := append([]httpserver.Route(nil), jobshttp.Routes()...)
	options.Logger.Info("jobs: composed",
		slog.String("env", string(options.Env)),
		slog.Int("routes", len(routes)),
	)
	return &JobsSurface{
		routes: routes,
		register: func(mux *http.ServeMux) {
			// The jobs handler resolves the operator from the request
			// context and requires it, but it carries no session
			// validator of its own: the surface resolves the session
			// cookie around exactly its subtree with the same validator
			// every other surface uses, over the same pool the login
			// wrote to. Without this wrap the requirement would refuse
			// every call on a mux no outer wrapper authenticates.
			inner := http.NewServeMux()
			handler.RegisterRoutes(inner)
			mux.Handle("/api/v1/admin/jobs/", manager.AuthenticateMiddleware(validator)(inner))
		},
	}, nil
}

// composeJobsValidator resolves the session cookie through the identity
// module: one validator for every route of the surface, over the same pool
// the login wrote to.
func composeJobsValidator(options Options) (security.SessionValidator, error) {
	identityRepository := identitypostgres.NewRepository(options.Pool)
	authenticate := identityapp.NewAuthenticateSessionUseCase(
		identityRepository, identityRepository, options.Clock, identitydomain.DefaultSessionPolicy(), 0,
	)
	validator, err := sessionvalidator.New(authenticate)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: jobs: session validator: %w", err)
	}
	return validator, nil
}

// buildJobsHandler wires the operational handler over the shared edges: the
// same pool the account journey wrote to, the same security boundary, and
// the bridges that keep the modules from importing each other. Nothing here
// invents a queue rule: health, listing and retry come from the use cases
// the jobs module already owns, and the retry allowlist stays the domain's.
func buildJobsHandler(options Options, manager *security.Manager) (*jobshttp.Handler, error) {
	repository := jobspostgres.NewRepository(options.Pool)
	health, err := jobsapp.NewGetQueueHealthUseCase(repository, options.Clock)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: jobs: queue health: %w", err)
	}
	list, err := jobsapp.NewListDeadJobsUseCase(repository, options.Clock)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: jobs: dead listing: %w", err)
	}
	operators, err := operatorbridge.NewDirectory(moderationpostgres.NewRepository(options.Pool))
	if err != nil {
		return nil, fmt.Errorf("bootstrap: jobs: operator directory: %w", err)
	}
	audit, err := auditbridge.NewRecorder(auditpostgres.NewRepository(options.Pool))
	if err != nil {
		return nil, fmt.Errorf("bootstrap: jobs: audit trail: %w", err)
	}
	retry, err := jobsapp.NewRetryJobUseCase(repository, repository, audit, platformpg.NewTxManager(options.Pool), options.Clock)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: jobs: job retry: %w", err)
	}
	return jobshttp.NewHandler(jobshttp.HandlerConfig{
		Health:    health,
		ListDead:  list,
		Retry:     retry,
		Operators: operators,
		Sessions:  repository,
		Security:  manager,
		Clock:     options.Clock,
	}), nil
}

// Routes is the canonical route list of the surface.
func (surface *JobsSurface) Routes() []httpserver.Route {
	return append([]httpserver.Route(nil), surface.routes...)
}

// Surface is what the platform router mounts.
func (surface *JobsSurface) Surface() httpserver.Surface {
	return httpserver.Surface{Routes: surface.Routes(), Register: surface.Mount}
}

// Mount registers the surface on the mux, refusing a second mount instead
// of panicking on duplicate patterns.
func (surface *JobsSurface) Mount(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("%w: jobs: mux is required", ErrIncompleteComposition)
	}
	surface.mu.Lock()
	defer surface.mu.Unlock()
	if surface.mounted {
		return fmt.Errorf("%w: jobs are already mounted; mounting them again would register duplicate patterns", ErrIncompleteComposition)
	}
	surface.register(mux)
	surface.mounted = true
	return nil
}

// validateJobs reports every missing edge at once, sorted, because an
// operator fixing a boot failure should not discover them one per attempt.
// Like the other JSON-only surfaces this one serves no documents, so it
// needs no asset manifest and no cursor secret — the dead listing is a
// bounded limit page, not a signed cursor walk.
func validateJobs(options Options) error {
	missing := make([]string, 0, 3)
	if options.Logger == nil {
		missing = append(missing, "logger")
	}
	if options.Clock == nil {
		missing = append(missing, "clock")
	}
	if options.Pool == nil {
		missing = append(missing, "postgres pool")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: jobs: missing %s", ErrIncompleteComposition, strings.Join(missing, ", "))
	}
	switch options.Env {
	case config.EnvDevelopment, config.EnvTest, config.EnvProduction:
		return nil
	default:
		return fmt.Errorf("%w: jobs: environment %q is not one of development, test, production", ErrIncompleteComposition, options.Env)
	}
}
