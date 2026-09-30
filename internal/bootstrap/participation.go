// Composition of the Arena participation journey (P18-T07B).
//
// Why it is a second surface and not an extension of the account one: the two
// journeys share only the platform edges. The account journey answers forms
// that create an account; this one answers the page of an Arena and the four
// transitions a person submits from it, and it is the first surface that
// charges INK — publishing an argument debits the author's wallet inside the
// publication transaction.
//
// The composition is the only place where the modules that must not know each
// other meet: positions and arguments ask their own eligibility ports, and the
// adapters in this file's import list are what answer them. Nothing here
// decides a policy; the aggregate threshold, the reply depth, the attribution
// limit, the INK cost and the throttle of each action come from the modules
// that own them.
//
// The durable queue is deliberately absent: no step of this journey enqueues a
// job — the publication charges and writes in one transaction, and the
// notifications that follow belong to the worker (`cmd/arena worker`), which
// already composes the queue, the outbox and its handlers. Composing it here
// would be configuration without a consumer.
//
// The JSON API of the participating modules is absent for the same reason:
// these pages call the use cases directly, and the endpoints that expose them
// over HTTP belong to the phase that mounts that API.
package bootstrap

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	arenashtml "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/html"
	arenaspostgres "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	argumentseligibility "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/eligibility"
	argumentspostgres "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	argumentswalletdebit "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/walletdebit"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/sessionvalidator"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	persuasionpostgres "github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/postgres"
	persuasionapp "github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	persuasiondomain "github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	positionseligibility "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/eligibility"
	positionspostgres "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/postgres"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	walletpostgres "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
)

// ParticipationSurface is the composed browser journey of one Arena, ready to
// be mounted on the platform mux.
type ParticipationSurface struct {
	routes   []httpserver.Route
	register func(mux *http.ServeMux)

	mu      sync.Mutex
	mounted bool
}

// ComposeParticipation builds the participation journey of the browser: the
// page of a published Arena, the confirmation and the change of a position, the
// publication of an argument (charged in INK) and the attribution of influence,
// over the real PostgreSQL repositories, the real transaction manager, the
// real wallet, the real session of the account module and the real pages.
//
// It fails closed: a journey composed without the cursor signing secret, the
// asset manifest, the database or the security boundary answers some requests
// and silently drops others, so the refusal names what is missing instead of
// mounting a partial surface.
func ComposeParticipation(options Options) (*ParticipationSurface, error) {
	if err := options.validateParticipation(); err != nil {
		return nil, err
	}

	cursors, err := argumentsapp.NewArgumentCursorCodec(options.CursorSecret)
	if err != nil {
		return nil, fmt.Errorf("%w: participation journey: cursor signing secret: %w", ErrIncompleteComposition, err)
	}

	manager, err := options.securityManager()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: participation journey: %w", err)
	}

	templates, err := arenashtml.NewParticipationTemplates(options.Assets)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: participation journey: templates: %w", err)
	}

	// One pool, six repositories: the modules keep their own adapters and the
	// composition is what puts them together.
	identityRepository := identitypostgres.NewRepository(options.Pool)
	arenasRepository := arenaspostgres.NewRepository(options.Pool)
	positionsRepository := positionspostgres.NewRepository(options.Pool)
	argumentsRepository := argumentspostgres.NewRepository(options.Pool)
	persuasionRepository := persuasionpostgres.NewRepository(options.Pool)
	walletRepository := walletpostgres.NewRepository(options.Pool)
	unitOfWork := platformpg.NewTxManager(options.Pool)

	// The eligibility bridges answer the ports of the two consuming modules;
	// the Arena lifecycle rule stays in the arenas domain and the account
	// lifecycle rule in the identity domain.
	positionEligibility, err := positionseligibility.New(identityRepository, arenasRepository)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: participation journey: %w", err)
	}
	argumentEligibility, err := argumentseligibility.New(identityRepository, arenasRepository)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: participation journey: %w", err)
	}

	// The session validator asks the same question the account pages ask, and
	// the identity module is what answers it: one adapter, two surfaces.
	sessions, err := sessionvalidator.New(identityapp.NewAuthenticateSessionUseCase(
		identityRepository, identityRepository, options.Clock, identitydomain.DefaultSessionPolicy(), 0,
	))
	if err != nil {
		return nil, fmt.Errorf("bootstrap: participation journey: %w", err)
	}

	// The throttle is shared with the account journey by construction and not
	// by instance: the limits are per action and the actions of the two
	// surfaces are disjoint. Trusted proxies are still unconfigured (P16-T03),
	// so the peer address is what is counted.
	resolver := clientip.New(nil)
	throttle := ratelimit.New(ratelimit.NewLimiter(ratelimit.Options{Now: options.Clock.Now}), resolver)

	attributionPolicy := persuasiondomain.DefaultEligibilityPolicy()

	handler, err := arenashtml.NewParticipationHandler(arenashtml.ParticipationConfig{
		GetDocument: arenasapp.NewGetArenaDocumentUseCase(arenasRepository),
		MyPosition:  positionsapp.NewGetMyPositionUseCase(positionsRepository),
		ConfirmPosition: positionsapp.NewConfirmInitialPositionUseCase(
			positionsRepository, positionEligibility, positionEligibility, options.Clock,
		),
		ChangePosition: positionsapp.NewChangePositionUseCase(
			positionsRepository, positionEligibility, unitOfWork, options.Clock,
		),
		Aggregate: positionsapp.NewGetPositionAggregateUseCase(
			positionsRepository, positionsdomain.DefaultAggregatePolicy(), options.Clock,
		),
		PositionChanges: positionsapp.NewListPositionChangesUseCase(positionsRepository),
		Arguments:       argumentsapp.NewListArenaArgumentsUseCase(argumentsRepository, cursors),
		PublishArgument: argumentsapp.NewPublishArgumentUseCase(
			argumentsRepository,
			argumentEligibility,
			argumentEligibility,
			// The wallet charge is bridged in the consumer's adapters: the
			// arguments module asks for an INK debit and never imports the
			// wallet vocabulary.
			argumentswalletdebit.New(walletapp.NewDebitInkUseCase(walletRepository, options.Clock)),
			unitOfWork,
			// The grapheme counter is the approved UAX #29 capability of the
			// platform, handed to the domain as a plain function (ADR-013).
			text.GraphemeCount,
			argumentsdomain.DefaultReplyPolicy(),
			options.Clock,
		),
		RecordAttributions: persuasionapp.NewRecordAttributionsUseCase(
			persuasionRepository, attributionPolicy, unitOfWork,
		),
		Security:         manager,
		SessionValidator: sessions,
		Random:           options.Random,
		RateLimit:        throttle,
		Templates:        templates,
		MaxAttributions:  attributionPolicy.MaxAttributions,
		Analytics:        options.Analytics,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: participation journey: %w", err)
	}

	options.Logger.Info("participation journey: composed",
		slog.String("env", string(options.Env)),
		slog.Int("routes", len(arenashtml.ParticipationRoutes())),
		slog.Int("max_attributions", attributionPolicy.MaxAttributions),
	)

	return &ParticipationSurface{
		routes:   arenashtml.ParticipationRoutes(),
		register: handler.RegisterRoutes,
	}, nil
}

// Routes is the canonical route list of the surface, in the same vocabulary
// the registry and the contract use.
func (surface *ParticipationSurface) Routes() []httpserver.Route {
	return append([]httpserver.Route(nil), surface.routes...)
}

// Surface is what the platform router mounts.
func (surface *ParticipationSurface) Surface() httpserver.Surface {
	return httpserver.Surface{Routes: surface.Routes(), Register: surface.Mount}
}

// Mount registers the surface on the mux the platform router is built from, so
// its routes are served inside the request id, locale and security layers
// instead of beside them. Mounting it twice is refused: net/http panics on a
// duplicate pattern, and a boot that panics is not a boot that fails closed.
func (surface *ParticipationSurface) Mount(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("%w: participation journey: mux is required", ErrIncompleteComposition)
	}

	surface.mu.Lock()
	defer surface.mu.Unlock()
	if surface.mounted {
		return fmt.Errorf("%w: participation journey is already mounted; mounting it again would register duplicate patterns", ErrIncompleteComposition)
	}

	surface.register(mux)
	surface.mounted = true
	return nil
}

// validateParticipation reports the shared edges plus what only this journey
// needs.
func (options Options) validateParticipation() error {
	if err := options.validate("participation journey"); err != nil {
		return err
	}
	if len(options.CursorSecret) == 0 {
		return fmt.Errorf("%w: participation journey: missing cursor signing secret", ErrIncompleteComposition)
	}
	return nil
}

// securityManager returns the security boundary of the surface: the one the
// process shares, when the composition was given one, and a private one
// otherwise.
//
// Sharing matters because the CSRF cookie is one per origin: two managers with
// two secrets would each answer for the cookie the other one rendered, and a
// person moving between the account pages and an Arena page would be refused by
// a middleware that cannot know why.
func (options Options) securityManager() (*security.Manager, error) {
	if options.Security != nil {
		return options.Security, nil
	}
	return security.New(security.Options{
		Env:    options.Env,
		Clock:  options.Clock,
		Random: options.Random,
	})
}
