// Composition of the private account reads (P49-T02): the public profile,
// the owner's private profile, the wallet statement, the personal export
// request/download and the account deletion workflow, over the same pool and
// security boundary as the account journey.
//
// Why a second surface and not a wider account one: the pages and the auth
// API answer who the person is; this surface answers what belongs to them.
// It shares the repository pool, the session validator and the CSRF cookie
// of the origin — a session opened by the JSON login is the session these
// routes require, and no second manager ever verifies it. The export
// generation and the due-deletion execution stay worker work; what is
// composed here persists the durable request and serves it, idempotently:
// a replayed request resolves the existing record instead of duplicating
// its side effect.
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
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	profileshttp "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/http"
	profilespostgres "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	wallethttp "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/http"
	walletpostgres "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
)

// PrivacySurface is the composed private account surface, ready to be
// mounted on the platform mux beside the account journey.
type PrivacySurface struct {
	routes   []httpserver.Route
	register func(mux *http.ServeMux)

	mu      sync.Mutex
	mounted bool
}

// privacyHandlers are the four handlers of the surface, sharing one pool,
// one security boundary and one session validator.
type privacyHandlers struct {
	profiles *profileshttp.Handler
	export   *profileshttp.ExportHandler
	deletion *profileshttp.DeletionHandler
	wallet   *wallethttp.Handler
}

// privacyDeps are the shared edges the handlers are built from. One struct
// keeps the composers under the parameter budget.
type privacyDeps struct {
	profiles  *profilespostgres.Repository
	wallet    *walletpostgres.Repository
	manager   *security.Manager
	validator security.SessionValidator
}

// ComposeAccountPrivacy builds the private account surface: public profile,
// private profile, wallet balance and statement, personal export
// request/download and deletion request/status/cancel, over the real
// PostgreSQL repositories, the shared security manager and the shared
// session validator.
//
// It fails closed: without the cursor signing secret the statement cannot
// paginate honestly, so the refusal names it instead of mounting a surface
// whose cursor any client could forge.
func ComposeAccountPrivacy(options Options) (*PrivacySurface, error) {
	if err := validatePrivacy(options); err != nil {
		return nil, err
	}
	manager, err := options.securityManager()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account privacy: security manager: %w", err)
	}
	codec, err := walletapp.NewStatementCursorCodec(options.CursorSecret)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account privacy: statement cursor: %w", err)
	}
	validator, err := composePrivacyValidator(options)
	if err != nil {
		return nil, err
	}
	handlers, err := buildPrivacyHandlers(options, privacyDeps{
		profiles:  profilespostgres.NewRepository(options.Pool),
		wallet:    walletpostgres.NewRepository(options.Pool),
		manager:   manager,
		validator: validator,
	}, codec)
	if err != nil {
		return nil, err
	}
	routes := append(profileshttp.Routes(), wallethttp.Routes()...)
	options.Logger.Info("account privacy: composed",
		slog.String("env", string(options.Env)),
		slog.Int("routes", len(routes)),
	)
	return &PrivacySurface{
		routes: routes,
		register: func(mux *http.ServeMux) {
			handlers.profiles.RegisterRoutes(mux)
			handlers.export.RegisterRoutes(mux)
			handlers.deletion.RegisterRoutes(mux)
			handlers.wallet.RegisterRoutes(mux)
		},
	}, nil
}

// buildPrivacyHandlers wires the four handlers over the shared edges: the
// same pool the account journey wrote to, the same security boundary, and
// one session validator for every private route.
func buildPrivacyHandlers(options Options, deps privacyDeps, codec *walletapp.StatementCursorCodec) (*privacyHandlers, error) {
	profilesHandler := profileshttp.NewHandler(profileshttp.HandlerConfig{
		GetPublicProfileUseCase:  profilesapp.NewGetPublicProfileUseCase(deps.profiles),
		GetPrivateProfileUseCase: profilesapp.NewGetPrivateProfileUseCase(deps.profiles),
		SecurityManager:          deps.manager,
		SessionValidator:         deps.validator,
	})
	requestExport, err := profilesapp.NewRequestPersonalExportUseCase(deps.profiles, options.Random, options.Clock)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account privacy: export request: %w", err)
	}
	downloadExport, err := profilesapp.NewDownloadPersonalExportUseCase(deps.profiles, options.Clock)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account privacy: export download: %w", err)
	}
	exportHandler := profileshttp.NewExportHandler(profileshttp.ExportHandlerConfig{
		RequestUseCase:   requestExport,
		DownloadUseCase:  downloadExport,
		Sessions:         deps.profiles,
		SecurityManager:  deps.manager,
		Clock:            options.Clock,
		SessionValidator: deps.validator,
	})
	uow := platformpg.NewTxManager(options.Pool)
	audit := auditpostgres.NewRepository(options.Pool)
	requestDeletion, err := profilesapp.NewRequestDeletionUseCase(deps.profiles, audit, uow, options.Clock)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account privacy: deletion request: %w", err)
	}
	deletionStatus, err := profilesapp.NewGetDeletionStatusUseCase(deps.profiles)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account privacy: deletion status: %w", err)
	}
	cancelDeletion, err := profilesapp.NewCancelDeletionUseCase(deps.profiles, audit, uow, options.Clock)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account privacy: deletion cancel: %w", err)
	}
	deletionHandler := profileshttp.NewDeletionHandler(profileshttp.DeletionHandlerConfig{
		RequestUseCase:   requestDeletion,
		StatusUseCase:    deletionStatus,
		CancelUseCase:    cancelDeletion,
		SecurityManager:  deps.manager,
		SessionValidator: deps.validator,
	})
	walletHandler := wallethttp.NewHandler(wallethttp.HandlerConfig{
		GetWalletBalanceUseCase:   walletapp.NewGetWalletBalanceUseCase(deps.wallet),
		GetWalletStatementUseCase: walletapp.NewGetWalletStatementUseCase(deps.wallet, codec),
		SecurityManager:           deps.manager,
		SessionValidator:          deps.validator,
	})
	return &privacyHandlers{
		profiles: profilesHandler,
		export:   exportHandler,
		deletion: deletionHandler,
		wallet:   walletHandler,
	}, nil
}

// composePrivacyValidator resolves the session cookie through the identity
// module, like participation does: one validator for every private route of
// the surface, over the same pool the login wrote to.
func composePrivacyValidator(options Options) (security.SessionValidator, error) {
	identityRepository := identitypostgres.NewRepository(options.Pool)
	authenticate := identityapp.NewAuthenticateSessionUseCase(
		identityRepository, identityRepository, options.Clock, identitydomain.DefaultSessionPolicy(), 0,
	)
	validator, err := sessionvalidator.New(authenticate)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account privacy: session validator: %w", err)
	}
	return validator, nil
}

// Routes is the canonical route list of the surface.
func (surface *PrivacySurface) Routes() []httpserver.Route {
	return append([]httpserver.Route(nil), surface.routes...)
}

// Surface is what the platform router mounts.
func (surface *PrivacySurface) Surface() httpserver.Surface {
	return httpserver.Surface{Routes: surface.Routes(), Register: surface.Mount}
}

// Mount registers the surface on the mux, refusing a second mount instead
// of panicking on duplicate patterns.
func (surface *PrivacySurface) Mount(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("%w: account privacy: mux is required", ErrIncompleteComposition)
	}
	surface.mu.Lock()
	defer surface.mu.Unlock()
	if surface.mounted {
		return fmt.Errorf("%w: account privacy is already mounted; mounting it again would register duplicate patterns", ErrIncompleteComposition)
	}
	surface.register(mux)
	surface.mounted = true
	return nil
}

// validatePrivacy reports every missing edge at once, sorted, because an
// operator fixing a boot failure should not discover them one per attempt.
// Unlike the page journeys this surface serves no documents, so it needs no
// asset manifest — but without the cursor secret the statement cursor would
// be forgeable, so the secret is required here and not merely preferred.
func validatePrivacy(options Options) error {
	missing := make([]string, 0, 5)
	if options.Logger == nil {
		missing = append(missing, "logger")
	}
	if options.Clock == nil {
		missing = append(missing, "clock")
	}
	if options.Random == nil {
		missing = append(missing, "entropy source")
	}
	if options.Pool == nil {
		missing = append(missing, "postgres pool")
	}
	if len(options.CursorSecret) == 0 {
		missing = append(missing, "cursor signing secret")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: account privacy: missing %s", ErrIncompleteComposition, strings.Join(missing, ", "))
	}
	switch options.Env {
	case config.EnvDevelopment, config.EnvTest, config.EnvProduction:
		return nil
	default:
		return fmt.Errorf("%w: account privacy: environment %q is not one of development, test, production", ErrIncompleteComposition, options.Env)
	}
}
