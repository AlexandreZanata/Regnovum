// Command arena is the single binary of Regnovum. Per the master plan,
// subcommands include server, worker, migrate and explicitly approved
// operations; server, worker, migrate, version and help exist.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	// The embedded IANA timezone database keeps the optional profile
	// timezone validation (P05-T06) working in minimal containers that
	// ship no system tzdata.
	_ "time/tzdata"

	billingcatalog "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/catalog"
	billingstripe "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	"github.com/AlexandreZanata/Regnovum/internal/buildinfo"
	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbpool"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

const usage = `arena is the command-line entrypoint of Regnovum.

Usage:

  arena <command> [arguments]

The commands are:

  server     run the HTTP server (ARENA_* configuration from the environment)
  worker     consume durable jobs until stopped (SIGTERM or SIGINT)
  migrate    apply or inspect database migrations (status, up)
  admin      bootstrap or revoke an administrator from the host (never over HTTP)
  projections rebuild derived public statistics projections
  version    show the arena version; use --json for machine-readable output
  help       show this help

Run "arena <command> -h" for details about a command.`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "arena:", err)
		os.Exit(1)
	}
}

// errUsage is the sentinel of a bad invocation. The usage text is printed where
// usage belongs — on the output — and never travels inside the error string,
// where it would be one message ending in punctuation read by nobody.
var errUsage = errors.New("invalid invocation")

func run(args []string, stdout *os.File) error {
	if len(args) == 0 {
		fmt.Fprintln(stdout, usage)
		return nil
	}

	switch args[0] {
	case "server":
		return runServer(args[1:], stdout)
	case "worker":
		return runWorker(args[1:], stdout)
	case "migrate":
		return runMigrate(args[1:], stdout)
	case "admin":
		return runAdmin(args[1:], stdout)
	case "projections":
		return runProjections(args[1:], stdout)
	case "version":
		return runVersion(args[1:], stdout)
	case "help", "-h", "-help", "--help":
		if len(args) > 1 {
			return fmt.Errorf("help takes no arguments (got %q)", args[1])
		}
		fmt.Fprintln(stdout, usage)
	default:
		fmt.Fprintln(stdout, usage)
		return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
	}
	return nil
}

// runServer boots the hardened HTTP server (P02-T05) and composes the surfaces
// it serves (P18-T07A, P18-T07B, P18-T07C): typed configuration from the
// environment, the structured JSON logger, request ID correlation, the account
// and participation browser journeys, the frontend build they reference, all
// mounted on the platform mux, and a graceful shutdown on SIGTERM/SIGINT. It is
// the process edge — the only place allowed to own signals and the real
// clock/randomness sources.
//
// With ARENA_DATABASE_URL set, the account and participation journeys are
// composed and served, and so is the frontend build named by ARENA_ASSETS_DIR:
// the pages reference hashed addresses and the process publishes exactly the
// ones its manifest declares (P18-T07C). A page mounted without its manifest
// would render links to files that do not exist, and a build without the
// process that serves it is a page that loads nothing. Without the DSN the
// process serves the health routes only and says so in the log: an application
// that answers 404 on every page while reporting itself ready is worse than a
// probe that declares what it is.
func runServer(args []string, stdout *os.File) error {
	if len(args) > 0 {
		return fmt.Errorf("server takes no arguments\n\nUsage: arena server")
	}

	cfg := config.MustLoad()
	logger := logging.New(stdout, cfg.LogLevel())

	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clockseed.NewClock())
	locResolver := locale.NewResolver()

	// One clock and one entropy source for the whole process: a request
	// observed by two layers must carry the same instant, and a token minted by
	// a use case must come from the same source the composition was validated
	// against.
	clock := clockseed.NewClock()
	random := clockseed.NewRandom()

	// Telemetry is composed before any surface: the account and participation
	// journeys receive the analytics sink, and the metrics registry the
	// transport writes to is the one the administrative listener renders
	// (P19-T05).
	telemetry, err := startTelemetry(cfg, logger, clock)
	if err != nil {
		return err
	}
	defer telemetry.Close()

	var readyCheckers []httpserver.ReadyChecker
	var surfaces []httpserver.Surface
	if cfg.DatabaseURL().IsSet() {
		dsn := string(cfg.DatabaseURL().Unredacted())
		poolCfg := dbpool.FromConfig(cfg)
		pool, err := dbpool.New(context.Background(), dsn, poolCfg, logger, clock)
		if err != nil {
			return fmt.Errorf("initialize database pool: %w", err)
		}
		defer pool.Close()
		readyCheckers = append(readyCheckers, pool)
		if err := registerDatabaseMetrics(telemetry, pool, clock); err != nil {
			return err
		}

		// The account journey is composed whole or not at all: the database
		// and the frontend build are both required, and the boot is the only
		// place where noticing a missing one is cheap.
		manifest, err := assets.LoadFile(cfg.AssetsDir())
		if err != nil {
			return fmt.Errorf("compose the account journey: %w (run 'make build-web' or point ARENA_ASSETS_DIR at an existing build)", err)
		}

		// The build the pages reference is served by this same process, from
		// the manifest that was just read: a page whose stylesheet and module
		// answer 404 is a broken page, and no deployment step should have to
		// guess which addresses the build published (P18-T07C).
		frontend, err := assets.NewServer(assets.Config{
			Directory: cfg.AssetsDir(),
			Manifest:  manifest,
			Logger:    logger,
		})
		if err != nil {
			return fmt.Errorf("compose the frontend build: %w", err)
		}
		surfaces = append(surfaces, httpserver.Surface{Static: true, Register: frontend.Mount})
		logger.Info("http server: frontend build served", slog.String("prefix", frontend.Prefix()))

		// One security boundary for every surface of the process: the CSRF
		// cookie belongs to the origin, not to a journey, so a person moving
		// between the account pages and an Arena page must not be refused by
		// two managers that cannot verify each other's tokens.
		manager, err := security.New(security.Options{Env: cfg.Env(), Clock: clock, Random: random})
		if err != nil {
			return fmt.Errorf("compose the security boundary: %w", err)
		}

		account, err := bootstrap.ComposeAccount(bootstrap.Options{
			Env:      cfg.Env(),
			Logger:   logger,
			Pool:     pool.Pool(),
			Clock:    clock,
			Random:   random,
			Assets:   manifest,
			Security: manager,
			// Development and test may name a directory for the local email
			// sink, so a journey driven by another process reads the same
			// delivery the person would (P18-T07). Production refuses the
			// variable before the boot reaches here.
			SinkDir: cfg.EmailSinkDir(),
			// Production delivers through the provider: the flows queue the
			// message as durable work and the worker runs the handler
			// (P19-T02A). A missing credential is a refusal at boot, never a
			// registration whose link goes nowhere.
			EmailFrom:   cfg.EmailFrom(),
			EmailAPIKey: cfg.ResendAPIKey(),
			// The account events are allowlisted product facts; the sink
			// records them without ever blocking the request (P19-T05).
			Analytics: telemetry.Events,
		})
		if err != nil {
			return err
		}
		surfaces = append(surfaces, account.Surface())
		logger.Info("http server: account journey mounted", slog.Int("routes", len(account.Routes())))

		// The journeys that paginate — participation and account privacy —
		// share one option set over the same pool and boundary as the
		// account journey. The helper owns the cursor gate so the boot
		// refuses a gap silently in no environment.
		journeyBase := bootstrap.Options{
			Env:       cfg.Env(),
			Logger:    logger,
			Pool:      pool.Pool(),
			Clock:     clock,
			Random:    random,
			Assets:    manifest,
			Security:  manager,
			Analytics: telemetry.Events,
		}
		cursorSurfaces, err := mountCursorJourneys(journeyBase, cfg, logger)
		if err != nil {
			return err
		}
		surfaces = append(surfaces, cursorSurfaces...)
	} else {
		logger.Warn("http server: account journey not mounted (ARENA_DATABASE_URL is not set); only the health routes are served")
	}

	handler, err := httpserver.NewMuxWith(ids, locResolver, securityheaders.Config{Production: cfg.IsProduction()}, surfaces, readyCheckers...)
	if err != nil {
		return err
	}
	// The observation layer wraps the composed router without writing
	// anything itself: the security policy stays the outermost writer of the
	// response, and every request is counted, timed and checked for a panic
	// on the way out.
	handler = telemetry.HTTPMiddleware(handler)

	server, err := httpserver.New(httpserver.Options{
		Addr:    cfg.Addr(),
		Handler: handler,
		Logger:  logger,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if adminAddr := cfg.AdminAddr(); adminAddr != "" {
		if err := startAdminListener(ctx, adminAddr, adminHandler(telemetry.MetricsHandler()), logger); err != nil {
			return err
		}
	}

	if err := server.Listen(); err != nil {
		return err
	}
	logger.Info("http server: listening",
		slog.String("addr", server.Addr()),
		slog.String("env", string(cfg.Env())),
	)

	return server.Run(ctx)
}

// mountCursorJourneys composes the journeys that paginate — participation
// (Arena feed), account privacy (wallet statement), the Arena lifecycle
// (feed, search and drafts), the debate (argument lists), the entitlement
// reads (pass history), the moderation triage queue and the public Arena
// export — over the shared edges the caller hands over. Without the cursor
// signing secret none can paginate its lists honestly, so all stay
// unmounted; production, which is expected to serve them, refuses the boot
// instead of shipping the gap silently.
func mountCursorJourneys(base bootstrap.Options, cfg config.Config, logger *slog.Logger) ([]httpserver.Surface, error) {
	if !cfg.CursorSecret().IsSet() {
		if cfg.IsProduction() {
			return nil, fmt.Errorf(
				"compose the cursor journeys: %s is not set, and without a cursor signing secret neither the Arena pages, the wallet statement, the Arena feed, the argument lists, the pass history nor the triage queue can paginate their lists",
				config.CursorSecretVariable,
			)
		}
		logger.Warn(
			"http server: participation and account privacy not mounted (ARENA_CURSOR_SECRET is not set); the account pages and the health routes are served",
			slog.String("variable", config.CursorSecretVariable),
		)
		return nil, nil
	}
	options := base
	options.CursorSecret = []byte(cfg.CursorSecret().Unredacted())
	participation, err := bootstrap.ComposeParticipation(options)
	if err != nil {
		return nil, err
	}
	privacy, err := bootstrap.ComposeAccountPrivacy(options)
	if err != nil {
		return nil, err
	}
	lifecycle, err := bootstrap.ComposeArenaLifecycle(options)
	if err != nil {
		return nil, err
	}
	debate, err := bootstrap.ComposeDebateAttribution(options)
	if err != nil {
		return nil, err
	}
	entitlements, err := bootstrap.ComposeEntitlementReads(options)
	if err != nil {
		return nil, err
	}
	moderation, err := bootstrap.ComposeModeration(options)
	if err != nil {
		return nil, err
	}
	transparency, err := bootstrap.ComposeTransparency(options)
	if err != nil {
		return nil, err
	}
	logger.Info("http server: participation journey mounted", slog.Int("routes", len(participation.Routes())))
	logger.Info("http server: account privacy mounted", slog.Int("routes", len(privacy.Routes())))
	logger.Info("http server: arena lifecycle mounted", slog.Int("routes", len(lifecycle.Routes())))
	logger.Info("http server: debate mounted", slog.Int("routes", len(debate.Routes())))
	logger.Info("http server: entitlement reads mounted", slog.Int("routes", len(entitlements.Routes())))
	logger.Info("http server: moderation mounted", slog.Int("routes", len(moderation.Routes())))
	logger.Info("http server: transparency mounted", slog.Int("routes", len(transparency.Routes())))
	surfaces := []httpserver.Surface{participation.Surface(), privacy.Surface(), lifecycle.Surface(), debate.Surface(), entitlements.Surface(), moderation.Surface(), transparency.Surface()}
	// The guarded billing writes mount beside the cursor journeys: no second
	// auth, no second cookie, the same pool and boundary as every surface
	// above. An incomplete payment composition stays unmounted instead of
	// taking money it cannot settle.
	billing, err := mountBillingWrites(options, cfg, logger)
	if err != nil {
		return nil, err
	}
	return append(surfaces, billing...), nil
}

// mountBillingWrites composes the guarded checkout and portal writes over
// the shared edges the caller hands over. The writes mount only beside the
// verified settlement pipeline, which needs the provider webhook secret:
// no such variable exists in the configuration yet, so the composition is
// refused and the writes stay unmounted — production serves the reads and
// refuses the incomplete payment composition instead of faking a payment.
// The refusal is a skip of the additive surface, never a boot failure: the
// rest of the process serves normally without payment writes.
func mountBillingWrites(base bootstrap.Options, cfg config.Config, logger *slog.Logger) ([]httpserver.Surface, error) {
	if !cfg.StripeSecretKey().IsSet() && len(cfg.BillingMarkets()) == 0 && cfg.BillingSuccessURL() == "" && cfg.BillingCancelURL() == "" {
		logger.Info("http server: billing writes not mounted (no billing configuration); the entitlement reads are served")
		return nil, nil
	}
	markets := make([]billingcatalog.EnabledMarket, 0, len(cfg.BillingMarkets()))
	for _, market := range cfg.BillingMarkets() {
		markets = append(markets, billingcatalog.EnabledMarket{Market: market.Market, Currency: market.Currency})
	}
	prices := make([]billingcatalog.ConfiguredPrice, 0, len(cfg.BillingPrices()))
	for _, price := range cfg.BillingPrices() {
		prices = append(prices, billingcatalog.ConfiguredPrice{Market: price.Market, Product: price.Product, PriceID: price.PriceID})
	}
	catalog, err := billingcatalog.Load(billingcatalog.Spec{
		Production: cfg.IsProduction(),
		Markets:    markets,
		Prices:     prices,
	})
	if err != nil {
		logger.Error("http server: billing writes refused: incomplete payment composition, checkout and portal stay unmounted",
			slog.String("reason", err.Error()))
		return nil, nil
	}
	gateway, err := billingstripe.NewGateway(billingstripe.Config{
		SecretKey: string(cfg.StripeSecretKey().Unredacted()),
		Timeout:   cfg.StripeTimeout(),
	})
	if err != nil {
		logger.Error("http server: billing writes refused: incomplete payment composition, checkout and portal stay unmounted",
			slog.String("reason", err.Error()))
		return nil, nil
	}
	surface, err := bootstrap.ComposeBillingWrites(base, bootstrap.BillingConfig{
		Gateway:         gateway,
		Catalog:         catalog,
		SuccessURL:      cfg.BillingSuccessURL(),
		CancelURL:       cfg.BillingCancelURL(),
		PortalReturnURL: cfg.BillingSuccessURL(),
		// No provider webhook secret exists in the configuration: the
		// server-to-server ingress stays a declared provider-only contract
		// and the writes stay unmounted until one is configured.
		WebhookSecret: "",
	})
	if err != nil {
		logger.Error("http server: billing writes refused: incomplete payment composition, checkout and portal stay unmounted",
			slog.String("reason", err.Error()))
		return nil, nil
	}
	logger.Info("http server: billing writes mounted", slog.Int("routes", len(surface.Routes())))
	return []httpserver.Surface{surface.Surface()}, nil
}

// runVersion prints the reproducible build metadata (P01-T05). Without
// flags it renders one human-readable line; with --json it renders a single
// RFC 8259 object, so scripts can parse the output safely.
func runVersion(args []string, stdout *os.File) error {
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		default:
			return fmt.Errorf("unknown flag %q\n\nUsage: arena version [--json]", arg)
		}
	}

	info := buildinfo.Current(os.Environ())
	if asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(info)
	}

	fmt.Fprintf(stdout, "arena version %s\n", info.Version)
	return nil
}
