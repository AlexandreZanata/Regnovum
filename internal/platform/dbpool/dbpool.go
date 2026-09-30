// Package dbpool implements the bounded PostgreSQL connection pool adapter
// of Regnovum using pgx/v5 (P03-T04).
package dbpool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config defines the tuning parameters and bounds for the PostgreSQL pool.
type Config struct {
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
	AcquireTimeout    time.Duration
	PingTimeout       time.Duration
}

// DefaultConfig returns safe development and operational pool defaults.
func DefaultConfig() Config {
	return Config{
		MaxConns:          10,
		MinConns:          2,
		MaxConnLifetime:   1 * time.Hour,
		MaxConnIdleTime:   30 * time.Minute,
		HealthCheckPeriod: 1 * time.Minute,
		AcquireTimeout:    5 * time.Second,
		PingTimeout:       2 * time.Second,
	}
}

// FromConfig derives a pool Config from the application runtime configuration.
func FromConfig(appCfg config.Config) Config {
	c := DefaultConfig()
	if m := appCfg.DBMaxConns(); m > 0 {
		c.MaxConns = m
	}
	if m := appCfg.DBMinConns(); m >= 0 {
		c.MinConns = m
	}
	if d := appCfg.DBMaxConnLifetime(); d > 0 {
		c.MaxConnLifetime = d
	}
	if d := appCfg.DBMaxConnIdleTime(); d > 0 {
		c.MaxConnIdleTime = d
	}
	if d := appCfg.DBAcquireTimeout(); d > 0 {
		c.AcquireTimeout = d
	}
	return c
}

// Validate ensures pool bounds and timeouts are strictly valid.
func (c Config) Validate() error {
	if c.MaxConns < 1 {
		return errors.New("dbpool: MaxConns must be at least 1")
	}
	if c.MinConns < 0 {
		return errors.New("dbpool: MinConns must be non-negative")
	}
	if c.MinConns > c.MaxConns {
		return fmt.Errorf("dbpool: MinConns (%d) cannot exceed MaxConns (%d)", c.MinConns, c.MaxConns)
	}
	if c.MaxConnLifetime <= 0 {
		return errors.New("dbpool: MaxConnLifetime must be positive")
	}
	if c.MaxConnIdleTime <= 0 {
		return errors.New("dbpool: MaxConnIdleTime must be positive")
	}
	if c.HealthCheckPeriod <= 0 {
		return errors.New("dbpool: HealthCheckPeriod must be positive")
	}
	if c.AcquireTimeout <= 0 {
		return errors.New("dbpool: AcquireTimeout must be positive")
	}
	if c.PingTimeout <= 0 {
		return errors.New("dbpool: PingTimeout must be positive")
	}
	return nil
}

// Pool wraps pgxpool.Pool with lifecycle control, health checks and redacted tracing.
type Pool struct {
	pool   *pgxpool.Pool
	cfg    Config
	logger *slog.Logger
}

// New establishes a bounded PostgreSQL connection pool. DSN is parsed and
// connection limits and query tracers are configured.
func New(ctx context.Context, dsn string, cfg Config, logger *slog.Logger, clock ports.Clock) (*Pool, error) {
	if dsn == "" {
		return nil, errors.New("dbpool: database DSN is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	pgxCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("dbpool: parse DSN: %s", logging.RedactValue(err.Error()))
	}

	pgxCfg.MaxConns = cfg.MaxConns
	pgxCfg.MinConns = cfg.MinConns
	pgxCfg.MaxConnLifetime = cfg.MaxConnLifetime
	pgxCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	pgxCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	pgxCfg.ConnConfig.Tracer = newTracer(logger, clock)

	rawPool, err := pgxpool.NewWithConfig(ctx, pgxCfg)
	if err != nil {
		return nil, fmt.Errorf("dbpool: create pool: %s", logging.RedactValue(err.Error()))
	}

	return &Pool{
		pool:   rawPool,
		cfg:    cfg,
		logger: logger,
	}, nil
}

// Ping verifies connectivity to the PostgreSQL cluster with a bounded timeout.
func (p *Pool) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, p.cfg.PingTimeout)
	defer cancel()
	return p.pool.Ping(pingCtx)
}

// CheckReadiness implements httpserver.ReadyChecker.
func (p *Pool) CheckReadiness(ctx context.Context) error {
	return p.Ping(ctx)
}

// Close gracefully closes all open connections and releases resources.
func (p *Pool) Close() {
	if p.pool != nil {
		p.pool.Close()
	}
}

// Pool returns the underlying *pgxpool.Pool for use by repository adapters.
func (p *Pool) Pool() *pgxpool.Pool {
	return p.pool
}

// Acquire returns an active connection from the pool, bounded by AcquireTimeout.
func (p *Pool) Acquire(ctx context.Context) (*pgxpool.Conn, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && p.cfg.AcquireTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.cfg.AcquireTimeout)
		defer cancel()
	}
	return p.pool.Acquire(ctx)
}

// Exec executes a SQL command on the pool.
func (p *Pool) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return p.pool.Exec(ctx, sql, arguments...)
}

// Query executes a query returning multiple rows.
func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return p.pool.Query(ctx, sql, args...)
}

// QueryRow executes a query expected to return at most one row.
func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return p.pool.QueryRow(ctx, sql, args...)
}

// Stat returns pool performance and connection statistics.
func (p *Pool) Stat() *pgxpool.Stat {
	return p.pool.Stat()
}
