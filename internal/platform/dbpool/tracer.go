// Package dbpool implements the bounded PostgreSQL connection pool adapter
// of Regnovum using pgx/v5 (P03-T04).
package dbpool

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbbudget"
	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
	"github.com/jackc/pgx/v5"
)

type queryTraceStateKey struct{}

type queryTraceState struct {
	start     time.Time
	sql       string
	argsCount int
}

// tracer implements pgx.QueryTracer with clock injection and automatic
// redaction of query SQL and error details.
type tracer struct {
	logger *slog.Logger
	clock  ports.Clock
}

func newTracer(logger *slog.Logger, clock ports.Clock) *tracer {
	return &tracer{
		logger: logger,
		clock:  clock,
	}
}

// TraceQueryStart records the query metadata and start instant in the context.
func (t *tracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if tracker := dbbudget.FromContext(ctx); tracker != nil {
		tracker.Start()
	}
	state := queryTraceState{
		sql:       data.SQL,
		argsCount: len(data.Args),
	}
	if t.clock != nil {
		state.start = t.clock.Now()
	}
	return context.WithValue(ctx, queryTraceStateKey{}, state)
}

func sanitizeSQL(sql string) string {
	lower := strings.ToLower(sql)
	if strings.Contains(lower, "password") {
		return "[REDACTED]"
	}
	return logging.RedactValue(sql)
}

// TraceQueryEnd logs query execution duration and sanitized SQL without
// leaking raw parameter values, secrets, or unredacted DSNs.
func (t *tracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if t.logger == nil {
		return
	}

	state, ok := ctx.Value(queryTraceStateKey{}).(queryTraceState)
	if !ok {
		return
	}

	var duration time.Duration
	if t.clock != nil && !state.start.IsZero() {
		duration = t.clock.Now().Sub(state.start)
	}
	if tracker := dbbudget.FromContext(ctx); tracker != nil {
		tracker.Observe(duration)
	}

	redactedSQL := sanitizeSQL(state.sql)

	if data.Err != nil && !errors.Is(data.Err, context.Canceled) {
		redactedErr := logging.RedactValue(data.Err.Error())
		t.logger.WarnContext(ctx, "db query error",
			slog.String("sql", redactedSQL),
			slog.Duration("duration", duration),
			slog.Int("args_count", state.argsCount),
			slog.String("error", redactedErr),
		)
		return
	}

	t.logger.DebugContext(ctx, "db query",
		slog.String("sql", redactedSQL),
		slog.Duration("duration", duration),
		slog.Int("args_count", state.argsCount),
	)
}
