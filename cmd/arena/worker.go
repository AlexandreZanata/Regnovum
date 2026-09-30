package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	identityjobs "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/jobs"
	identityrepo "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobsrepo "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbpool"
	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
)

const workerUsage = `run the durable job worker of Regnovum.

Usage:

  arena worker    consume queued jobs until stopped (SIGTERM or SIGINT)

The worker reads ARENA_* configuration from the environment
(ARENA_DATABASE_URL is required) and consumes app.jobs with a bounded pool:
each worker leases one job at a time, runs its versioned handler under a
deadline shorter than the lease, records success or a redacted failure, and
retries with exponential backoff until the attempt budget is spent.

A shutdown signal stops new claims and lets in-flight jobs finish and be
recorded, so a stop never abandons a lease.`

// runWorker implements `arena worker` (P15-T02). It is a process edge: it owns
// the signal handling, the real clock and the real entropy source, and it
// composes the queue adapters with the runtime.
func runWorker(args []string, stdout *os.File) error {
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
			fmt.Fprintln(stdout, workerUsage)
			return nil
		}
		return fmt.Errorf("worker takes no arguments (got %q)\n\n%s", args[0], workerUsage)
	}

	cfg := config.MustLoad()
	dsn := string(cfg.DatabaseURL().Unredacted())
	if dsn == "" {
		return fmt.Errorf("ARENA_DATABASE_URL is required for arena worker (set it to the PostgreSQL DSN)")
	}

	logger := logging.New(stdout, cfg.LogLevel())

	clock := clockseed.NewClock()
	pool, err := dbpool.New(context.Background(), dsn, dbpool.FromConfig(cfg), logger, clock)
	if err != nil {
		return fmt.Errorf("initialize database pool: %w", err)
	}
	defer pool.Close()

	// The worker is a telemetry producer too: it reports handler failures to
	// the error reporter and exposes its own metrics, so a dead queue is
	// visible in the process that drains it (P19-T05).
	telemetry, err := startTelemetry(cfg, logger, clock)
	if err != nil {
		return err
	}
	defer telemetry.Close()
	if err := registerDatabaseMetrics(telemetry, pool, clock); err != nil {
		return err
	}

	repo := jobsrepo.NewRepository(pool.Pool())
	workerCfg := jobsapp.DefaultWorkerConfig()

	enqueue, err := jobsapp.NewEnqueueUseCase(repo, clock)
	if err != nil {
		return err
	}
	lease, err := jobsapp.NewLeaseUseCase(repo, clock)
	if err != nil {
		return err
	}
	complete, err := jobsapp.NewCompleteUseCase(repo, clock)
	if err != nil {
		return err
	}
	fail, err := jobsapp.NewFailUseCase(repo, clock)
	if err != nil {
		return err
	}
	recoverLeases, err := jobsapp.NewRecoverExpiredLeasesUseCase(repo, clock)
	if err != nil {
		return err
	}

	// Handlers are registered by the phases that own each workload. The
	// scheduled session cleanup is the first one to land here (P16-T06): its
	// cadence has been queued by the scheduler since P15-T05, and a queued
	// workload with no handler is not idle, it is a dead job per period. The
	// remaining workloads (retention and the rest of the scheduled
	// maintenance) are still recorded as JOB_UNKNOWN_TYPE rather than guessed
	// at, and the started record below counts what is wired.
	registry := jobsapp.NewHandlerMap()
	_ = enqueue // Enqueue is composed for producers wired in later phases.

	// Transactional email (P19-T02A): production queues every identity message
	// as an `email_delivery` job — the composition of the account journey hands
	// the message to the outbox instead of recording it — and this is the
	// process that delivers it. Development and test deliver through the local
	// sink, so nothing is queued there and the composition returns no handler:
	// a handler installed anyway would consume work no flow produced.
	delivery, err := bootstrap.ComposeEmailDelivery(bootstrap.Options{
		Env:         cfg.Env(),
		Logger:      logger,
		Pool:        pool.Pool(),
		Clock:       clock,
		EmailFrom:   cfg.EmailFrom(),
		EmailAPIKey: cfg.ResendAPIKey(),
	})
	if err != nil {
		return err
	}
	if delivery.Handler != nil {
		if err := delivery.Handler.Register(registry); err != nil {
			return err
		}
		logger.Info("job worker: transactional email handler registered")
	} else {
		logger.Info("job worker: transactional email is delivered by the local sink; no email handler to register")
	}

	sessionCleanup, err := identityjobs.NewCleanupHandler(
		identityapp.NewCleanupSessionsUseCase(
			identityrepo.NewRepository(pool.Pool()),
			clock,
			identitydomain.DefaultSessionPolicy(),
		),
	)
	if err != nil {
		return err
	}
	if err := sessionCleanup.Register(registry); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if adminAddr := cfg.AdminAddr(); adminAddr != "" {
		if err := startAdminListener(ctx, adminAddr, adminHandler(telemetry.MetricsHandler()), logger); err != nil {
			return err
		}
	}

	worker, err := jobsapp.NewWorker(jobsapp.WorkerDeps{
		Lease:    lease,
		Complete: complete,
		Fail:     fail,
		Recover:  recoverLeases,
		Registry: &observedRegistry{
			inner:    registry,
			metrics:  telemetry.Metrics,
			reporter: telemetry.Errors,
			clock:    clock,
		},
		Clock:  clock,
		Random: clockseed.NewRandom(),
		Logger: logger,
		Config: workerCfg,
	})
	if err != nil {
		return err
	}

	logger.Info("job worker: started",
		slog.Int("concurrency", workerCfg.Concurrency),
		slog.Duration("lease", workerCfg.LeaseDuration),
		slog.Duration("handler_timeout", workerCfg.HandlerTimeout),
		slog.Int("handlers", registry.Len()),
	)
	if registry.Len() == 0 {
		logger.Warn("job worker: no handlers registered yet; unregistered workloads fail as JOB_UNKNOWN_TYPE")
	}

	runErr := worker.Run(ctx)

	stats := worker.Stats()
	logger.Info("job worker: stopped",
		slog.Int64("leased", stats.Leased),
		slog.Int64("succeeded", stats.Succeeded),
		slog.Int64("failed", stats.Failed),
		slog.Int64("dead", stats.Dead),
		slog.Int64("reclaimed", stats.Reclaimed),
	)
	return runErr
}
