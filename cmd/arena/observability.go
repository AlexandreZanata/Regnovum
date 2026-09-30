package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	jobsrepo "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbpool"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
	"github.com/AlexandreZanata/Regnovum/internal/platform/profiling"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// startTelemetry composes the process telemetry from the typed configuration:
// the error reporter, the analytics sink and the metrics registry. An empty
// credential disables that provider; the metrics registry is always built.
func startTelemetry(cfg config.Config, logger *slog.Logger, clock ports.Clock) (*observability.Telemetry, error) {
	return bootstrap.ComposeTelemetry(bootstrap.Options{
		Env:                 cfg.Env(),
		Logger:              logger,
		Clock:               clock,
		SentryDSN:           cfg.SentryDSN(),
		PostHogAPIKey:       cfg.PostHogAPIKey(),
		PostHogHost:         cfg.PostHogHost(),
		AnalyticsSampleRate: cfg.AnalyticsSampleRate(),
	})
}

// registerDatabaseMetrics installs the USE gauges of the database pool and of
// the durable queue. Both are read at scrape time; the queue reading is
// bounded and cached inside the registry, so a scrape costs one query.
func registerDatabaseMetrics(telemetry *observability.Telemetry, pool *dbpool.Pool, clock ports.Clock) error {
	if telemetry == nil || telemetry.Metrics == nil {
		return nil
	}

	telemetry.Metrics.RegisterPool(func() observability.PoolStats {
		stats := pool.Stat()
		return observability.PoolStats{
			Total:      stats.TotalConns(),
			Acquired:   stats.AcquiredConns(),
			Idle:       stats.IdleConns(),
			Max:        stats.MaxConns(),
			Acquires:   stats.AcquireCount(),
			Empty:      stats.EmptyAcquireCount(),
			Canceled:   stats.CanceledAcquireCount(),
			AcquireSum: stats.AcquireDuration().Seconds(),
		}
	})

	health, err := jobsapp.NewGetQueueHealthUseCase(jobsrepo.NewRepository(pool.Pool()), clock)
	if err != nil {
		return fmt.Errorf("compose the queue health query: %w", err)
	}
	telemetry.Metrics.RegisterQueue(func(ctx context.Context) (observability.QueueStats, error) {
		report, err := health.Execute(ctx)
		if err != nil {
			return observability.QueueStats{}, err
		}
		return observability.QueueStats{
			Queued:         report.Queue.Queued,
			Leased:         report.Queue.Leased,
			DueNow:         report.Queue.DueNow,
			Dead:           report.Queue.Dead,
			LagSeconds:     report.Queue.LagSeconds,
			OldestDeadSecs: report.Queue.OldestDeadSeconds,
		}, nil
	})
	return nil
}

// adminHandler composes the loopback administrative surface: the runtime
// profiles and one metrics scrape. It is never mounted on the public address.
func adminHandler(metrics http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/debug/pprof/", profiling.Handler())
	if metrics != nil {
		mux.Handle("GET /metrics", metrics)
	}
	return mux
}

// startAdminListener binds the administrative listener eagerly and serves it
// until the process context is cancelled, so a bad address or a busy port
// fails the boot instead of being discovered later.
func startAdminListener(ctx context.Context, addr string, handler http.Handler, logger *slog.Logger) error {
	server, err := httpserver.New(httpserver.Options{
		Addr:    addr,
		Handler: handler,
		Logger:  logger,
	})
	if err != nil {
		return fmt.Errorf("initialize administrative server: %w", err)
	}
	if err := server.Listen(); err != nil {
		return fmt.Errorf("listen administrative server: %w", err)
	}
	go func() {
		if err := server.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("administrative server stopped", slog.String("error", err.Error()))
		}
	}()
	logger.Info("administrative server: listening",
		slog.String("addr", server.Addr()),
		slog.Bool("metrics", handler != nil),
	)
	return nil
}

// observedRegistry decorates the job handler registry with the worker
// instrumentation: each finished job is counted and timed by type, and a
// handler failure is reported to the error reporter. The wrapped error is
// returned untouched, so the worker keeps classifying and retrying it.
type observedRegistry struct {
	inner    jobsapp.HandlerRegistry
	metrics  *observability.Metrics
	reporter observability.ErrorReporter
	clock    ports.Clock
}

// Resolve returns the instrumented handler of one job type.
func (registry *observedRegistry) Resolve(jobType jobsdomain.JobType, version int) (jobsapp.Handler, error) {
	handler, err := registry.inner.Resolve(jobType, version)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, job *jobsdomain.Job) error {
		started := registry.clock.Now()
		err := handler(ctx, job)
		outcome := "succeeded"
		if err != nil {
			outcome = "failed"
			registry.reporter.Report(observability.ErrorReport{
				Message:   "job handler failed",
				Kind:      "handler",
				Operation: string(jobType),
			})
		}
		registry.metrics.ObserveJob(string(jobType), outcome, registry.clock.Now().Sub(started))
		return err
	}, nil
}
