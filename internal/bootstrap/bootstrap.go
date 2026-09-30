// Package bootstrap is the composition root of the process: it turns the
// configuration and the process edges into the instances the server mounts. It
// owns no business rule and answers no request — docs/ARCHITECTURE.md names
// this layer and forbids it from carrying rules of its own.
//
// Why it exists as a package (P18-T07A): until now `arena server` mounted the
// health routes and nothing else, so the browser journeys of the account
// (P18-T05) and of the Arena (P18-T06) were reachable only from the tests of
// their adapters — every module route existed in the registry and nowhere in
// the process. Composing them belongs to one place, and the first surface to
// land here is the account journey.
//
// Two rules shape it:
//
//   - it fails closed. A surface is mounted whole or not at all: every
//     dependency is validated before anything is constructed, and the refusal
//     names each missing piece, so the process never serves a partial
//     application while looking healthy;
//   - it composes, it does not decide. Policies, thresholds and messages come
//     from the packages that own them; nothing here invents a default that
//     would be invisible to the module that enforces it.
package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/emailsink"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/fakeemail"
	identityhtml "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/html"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobspostgres "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/outbox"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/renderer"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/resend"
	notificationsapp "github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
	profilespostgres "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
)

// ErrIncompleteComposition marks a refusal to build a surface from a
// composition that cannot serve it. Callers can test for it instead of
// matching message text; the message names every missing piece.
var ErrIncompleteComposition = errors.New("bootstrap: incomplete composition")

// Options are the process edges and the configuration one surface is composed
// from. Every field is required unless its documentation says otherwise, and a
// missing one is a refusal, never a silent default.
type Options struct {
	// Env selects the environment rules: the development replacements and the
	// cookie policy.
	Env config.Env
	// Logger records what the composition decided (which surface was mounted,
	// which replacement was installed).
	Logger *slog.Logger
	// Pool is the PostgreSQL pool the module repositories run on.
	Pool *pgxpool.Pool
	// Clock is the process clock the use cases receive.
	Clock ports.Clock
	// Random is the process entropy source the use cases receive.
	Random ports.Random
	// Assets is the manifest of the frontend build the pages load. The pages
	// reference the hashed files through it, so a surface mounted without one
	// would render links to files that do not exist.
	Assets assets.Manifest
	// CursorSecret signs the pagination cursors of the public lists the
	// participation journey renders. It is required by the journeys that
	// paginate and ignored by the ones that do not.
	CursorSecret []byte
	// EmailFrom is the verified sender address of the transactional email
	// provider, as the configuration validated it. It is required by the
	// environments that deliver through a provider (P19-T02A).
	EmailFrom string
	// EmailAPIKey is the credential of that provider. It is never logged: it
	// travels from the configuration to the adapter through Unredacted and
	// nowhere else.
	EmailAPIKey config.Secret
	// SinkDir is the directory the local email sink writes to. When it is
	// set, development and test deliver the identity messages there instead
	// of keeping them in memory, so a journey driven by another process can
	// read the code the message carries (P18-T07). It is refused in
	// production, where no account code may be written to disk.
	SinkDir string
	// Security is the security boundary shared by every surface of the process
	// (cookies, CSRF, identity in the request context). When nil, a surface
	// composes its own: correct for a process that serves one journey, and the
	// reason `arena server` hands the same one to all of them.
	Security *security.Manager
	// Analytics is the allowlisted event sink the surfaces record their
	// product events through. It is composed by ComposeTelemetry; nil means
	// the surface emits nothing, which is the case of a test that drives the
	// journey without a process.
	Analytics observability.EventSink
	// SentryDSN is the error reporter credential. Empty disables reporting.
	SentryDSN config.Secret
	// PostHogAPIKey is the product analytics write key. Empty disables
	// analytics.
	PostHogAPIKey config.Secret
	// PostHogHost overrides the analytics API origin; empty selects the
	// provider default.
	PostHogHost string
	// AnalyticsSampleRate is the deterministic sampling percentage of
	// analytics, 0..100.
	AnalyticsSampleRate int
}

// AccountSurface is the composed browser journey of the account, ready to be
// mounted on the platform mux.
type AccountSurface struct {
	routes   []httpserver.Route
	register func(mux *http.ServeMux)
	sink     *fakeemail.Sender
	sinkDir  string

	mu      sync.Mutex
	mounted bool
}

// ComposeAccount builds the account journey of the browser: registration,
// email confirmation, sign in, sign out and password recovery, over the real
// PostgreSQL repositories, the real security manager, the real throttle and
// the real pages.
//
// It returns ErrIncompleteComposition — naming what is missing — rather than a
// surface that would answer some requests and fail others.
func ComposeAccount(options Options) (*AccountSurface, error) {
	if err := options.validate("account journey"); err != nil {
		return nil, err
	}

	delivery, err := ComposeEmailDelivery(options)
	if err != nil {
		return nil, err
	}
	emails, sink := delivery.Sender, delivery.Sink
	repository := identitypostgres.NewRepository(options.Pool)

	hasher, err := argon2id.NewDefault(options.Random)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account journey: password hasher: %w", err)
	}

	manager, err := options.securityManager()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account journey: security manager: %w", err)
	}

	// The throttle is the only thing bounding credential guessing on this
	// surface: the browser policy (`script-src 'self'`) refuses the challenge
	// script of the provider, so no widget is presented here. Trusted proxies
	// are still unconfigured — the criterion P16-T03 registered — which means
	// forwarding headers are evidence of nothing and the peer address is what
	// is counted.
	resolver := clientip.New(nil)
	throttle := ratelimit.New(ratelimit.NewLimiter(ratelimit.Options{Now: options.Clock.Now}), resolver)

	// The risk signal observes a run of failed sign-ins, and `NewEnforcer`
	// answers the surface's Observe port. Its verifier is deliberately nil:
	// this surface never presents a challenge, and a verifier installed here
	// would be a dependency nothing can reach.
	risk := turnstile.NewEnforcer(
		turnstile.Config{},
		nil,
		turnstile.NewFailureTracker(turnstile.FailureTrackerOptions{Now: options.Clock.Now}),
		resolver,
	)

	templates, err := identityhtml.NewTemplates(options.Assets)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account journey: templates: %w", err)
	}

	handler, err := identityhtml.NewHandler(identityhtml.HandlerConfig{
		Register: identityapp.NewRegisterAccountUseCase(
			repository, repository, hasher, emails, options.Clock, options.Random, identitydomain.DefaultVerificationPolicy(),
		),
		Verify: identityapp.NewVerifyEmailUseCase(repository, repository, options.Clock),
		Login: identityapp.NewLoginUseCase(
			repository, repository, repository, hasher, options.Clock, options.Random, identitydomain.DefaultSessionPolicy(),
		),
		Logout: identityapp.NewLogoutUseCase(repository),
		RequestPasswordReset: identityapp.NewRequestPasswordResetUseCase(
			repository, repository, emails, options.Clock, options.Random, identitydomain.DefaultPasswordResetPolicy(),
		),
		CompletePasswordReset: identityapp.NewCompletePasswordResetUseCase(
			repository, repository, repository, repository, repository, hasher, emails, options.Clock,
		),
		Security:   manager,
		RateLimit:  throttle,
		Templates:  templates,
		RiskSignal: risk,
		Analytics:  options.Analytics,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: account journey: %w", err)
	}

	options.Logger.Info("account journey: composed",
		slog.String("env", string(options.Env)),
		slog.Int("routes", len(identityhtml.Routes())),
	)

	return &AccountSurface{
		routes:   identityhtml.Routes(),
		register: handler.RegisterRoutes,
		sink:     sink,
		sinkDir:  options.SinkDir,
	}, nil
}

// Routes is the canonical route list of the surface, in the same vocabulary
// the registry and the contract use.
func (surface *AccountSurface) Routes() []httpserver.Route {
	return append([]httpserver.Route(nil), surface.routes...)
}

// Surface is what the platform router mounts: the routes the journey answers
// and the registration that installs them.
func (surface *AccountSurface) Surface() httpserver.Surface {
	return httpserver.Surface{Routes: surface.Routes(), Register: surface.Mount}
}

// Mount registers the surface on the mux the platform router is built from, so
// its routes are served inside the request id, locale and security layers
// instead of beside them.
//
// Mounting one surface twice is refused instead of attempted: net/http panics
// on a duplicate pattern, and a boot that panics is not a boot that fails
// closed.
func (surface *AccountSurface) Mount(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("%w: account journey: mux is required", ErrIncompleteComposition)
	}

	surface.mu.Lock()
	defer surface.mu.Unlock()
	if surface.mounted {
		return fmt.Errorf("%w: account journey is already mounted; mounting it again would register duplicate patterns", ErrIncompleteComposition)
	}

	surface.register(mux)
	surface.mounted = true
	return nil
}

// LocalSink returns the development email sink when the composition installed
// the in-memory one, and nil otherwise. It is the only way a development or
// test process can follow the link that a real provider would deliver, and it
// exists so the browser journey can be completed without a provider: the
// tokens live in the messages this sink recorded, in this process, for this
// environment.
//
// When the composition installed the directory sink instead, this returns nil
// and SinkDirectory names where the messages are: a process that reads the
// tokens from outside does not need an in-process handle, and handing out a
// half-configured one would hide the difference.
func (surface *AccountSurface) LocalSink() *fakeemail.Sender {
	return surface.sink
}

// SinkDirectory returns the directory the local email sink writes to, and the
// empty string when the composition installed the in-memory sink.
func (surface *AccountSurface) SinkDirectory() string { return surface.sinkDir }

// validate reports every missing dependency at once, sorted by name, because
// an operator fixing a boot failure should not discover them one per attempt.
// The journey name is a parameter because the same edges compose more than one
// surface, and a refusal that named the wrong one would send the operator to
// the wrong part of the configuration.
func (options Options) validate(journey string) error {
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
	if len(options.Assets.Assets) == 0 {
		missing = append(missing, "asset manifest")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: %s: missing %s", ErrIncompleteComposition, journey, strings.Join(missing, ", "))
	}

	switch options.Env {
	case config.EnvDevelopment, config.EnvTest, config.EnvProduction:
		// The sink directory is a development replacement, not a deployment
		// option: it is refused here as well as at the configuration edge, so
		// no caller of this composition can install it in production.
		if options.Env == config.EnvProduction && options.SinkDir != "" {
			return fmt.Errorf("%w: %s: the local email sink is refused in production", ErrIncompleteComposition, journey)
		}
		return nil
	default:
		return fmt.Errorf("%w: %s: environment %q is not one of development, test, production", ErrIncompleteComposition, journey, options.Env)
	}
}

// EmailDelivery is the transactional email pipeline of the process. It answers
// two questions with one composition because the two halves have to agree on
// the same decision: how an identity flow hands a message over, and how a
// queued delivery is executed (P19-T02A).
type EmailDelivery struct {
	// Sender is what the identity flows receive. In production it is the
	// outbox bridge, which persists the message as durable work; in development
	// and test it is the local sink, which records the message and never
	// delivers it.
	Sender identityapp.EmailSender
	// Sink is the in-memory sink of development and test, exposed so a test
	// that owns the process reads the tokens it recorded. It is nil in
	// production, where nothing keeps a message in memory.
	Sink *fakeemail.Sender
	// Handler executes the queued deliveries in the worker. It is nil when
	// nothing is queued, which is exactly the development and test case: there,
	// a handler would consume work no flow ever produced.
	Handler *outbox.Handler
}

// ComposeEmailDelivery composes the transactional email pipeline of the
// environment.
//
// Development and test install the local sink the identity module already
// documents for non-production environments, and the composition says so out
// loud: without a provider the message is recorded and never delivered, which
// is exactly what a person debugging a journey needs to know. Two flavours
// exist because two kinds of reader exist (P18-T07): the in-memory sink is
// enough for a test that owns the process, and the directory sink is how a
// reader in another process — the browser harness — sees the same delivery.
//
// Production composes the delivery the previous phases built and left unwired:
// the flow hands the message to the outbox, which persists the frozen facts of
// the notification as an `email_delivery` job, and the worker renders and
// delivers it through the provider. The chain is composed whole or not at all —
// a registration whose confirmation link cannot be delivered is not a journey
// worth serving — so a missing credential is a refusal that names the variable,
// never a message dropped in silence.
//
// It reads the environment, the logger, the pool, the clock, the sender address
// and the credential; the journey-only fields of Options are ignored, which is
// why `arena worker` — a process that mounts no page — calls this same function
// with the fields it has.
func ComposeEmailDelivery(options Options) (*EmailDelivery, error) {
	switch options.Env {
	case config.EnvDevelopment, config.EnvTest:
		if options.SinkDir != "" {
			sink, err := emailsink.NewSender(emailsink.Options{
				Directory: options.SinkDir,
				Clock:     options.Clock,
				Logger:    options.Logger,
			})
			if err != nil {
				return nil, fmt.Errorf("%w: account journey: local email sink: %w", ErrIncompleteComposition, err)
			}
			options.Logger.Warn(
				"account journey: local email sink writes to a directory; verification and recovery messages are recorded there and never delivered",
				slog.String("env", string(options.Env)),
				slog.String("directory", sink.Directory()),
			)
			return &EmailDelivery{Sender: sink}, nil
		}

		sink := fakeemail.NewSender()
		options.Logger.Warn(
			"account journey: local email sink installed; verification and recovery messages are recorded in this process and never delivered",
			slog.String("env", string(options.Env)),
		)
		return &EmailDelivery{Sender: sink, Sink: sink}, nil
	case config.EnvProduction:
		return composeQueuedEmailDelivery(options)
	default:
		return nil, fmt.Errorf(
			"%w: transactional email: environment %q is not one of development, test, production",
			ErrIncompleteComposition, options.Env,
		)
	}
}

// composeQueuedEmailDelivery is the production pipeline: the identity port is
// answered by the outbox bridge, and the worker handler renders and delivers
// what the queue holds.
//
// The pieces are the ones P15 left behind, wired in the order that makes each
// of them honest: the directory answers "which account and which locale" from
// identity and profiles, the enqueuer persists the frozen facts inside the
// caller's transaction when there is one, the notifier freezes the locale, and
// the bridge is what identity sees. Nothing here re-implements a decision those
// packages already make.
func composeQueuedEmailDelivery(options Options) (*EmailDelivery, error) {
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
		return nil, fmt.Errorf("%w: transactional email: missing %s", ErrIncompleteComposition, strings.Join(missing, ", "))
	}
	if !options.EmailAPIKey.IsSet() || options.EmailFrom == "" {
		return nil, fmt.Errorf(
			"%w: transactional email: %s and %s are required: every registration sends a confirmation link, and without a provider adapter that message could not be delivered",
			ErrIncompleteComposition, config.ResendAPIKeyVariable, config.EmailFromVariable,
		)
	}

	accounts := identitypostgres.NewRepository(options.Pool)
	preferences := profilespostgres.NewRepository(options.Pool)
	directory, err := outbox.NewDirectory(accounts, preferences)
	if err != nil {
		return nil, fmt.Errorf("%w: transactional email: account directory: %w", ErrIncompleteComposition, err)
	}

	enqueue, err := jobsapp.NewEnqueueUseCase(jobspostgres.NewRepository(options.Pool), options.Clock)
	if err != nil {
		return nil, fmt.Errorf("%w: transactional email: enqueue use case: %w", ErrIncompleteComposition, err)
	}
	enqueuer, err := outbox.NewEnqueuer(enqueue)
	if err != nil {
		return nil, fmt.Errorf("%w: transactional email: outbox enqueuer: %w", ErrIncompleteComposition, err)
	}
	notifier, err := notificationsapp.NewNotifier(directory, enqueuer)
	if err != nil {
		return nil, fmt.Errorf("%w: transactional email: notifier: %w", ErrIncompleteComposition, err)
	}
	bridge, err := outbox.NewSender(notifier)
	if err != nil {
		return nil, fmt.Errorf("%w: transactional email: outbox bridge: %w", ErrIncompleteComposition, err)
	}

	handler, err := composeEmailHandler(options)
	if err != nil {
		return nil, err
	}
	options.Logger.Info(
		"transactional email: messages are queued as durable jobs and delivered by the worker through the provider",
		slog.String("env", string(options.Env)),
	)
	return &EmailDelivery{Sender: bridge, Handler: handler}, nil
}

// composeEmailHandler builds the worker half: the renderer, the provider
// adapter and the use case that joins them.
//
// The provider sender is constructed with the credential, which is unredacted
// here and nowhere else, and the composition never logs it: the adapter's own
// redaction is what keeps a provider answer out of a log.
func composeEmailHandler(options Options) (*outbox.Handler, error) {
	render, err := renderer.NewRenderer()
	if err != nil {
		return nil, fmt.Errorf("%w: transactional email: message renderer: %w", ErrIncompleteComposition, err)
	}

	provider, err := resend.NewSender(resend.Config{
		APIToken: string(options.EmailAPIKey.Unredacted()),
		From:     options.EmailFrom,
		Logger:   options.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: transactional email: provider sender: %w", ErrIncompleteComposition, err)
	}

	deliverer, err := notificationsapp.NewDeliverer(render, provider)
	if err != nil {
		return nil, fmt.Errorf("%w: transactional email: deliverer: %w", ErrIncompleteComposition, err)
	}
	handler, err := outbox.NewHandler(deliverer)
	if err != nil {
		return nil, fmt.Errorf("%w: transactional email: delivery handler: %w", ErrIncompleteComposition, err)
	}
	return handler, nil
}
