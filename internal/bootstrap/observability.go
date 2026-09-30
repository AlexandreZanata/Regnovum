// Composition of the process telemetry (P19-T05).
//
// It lives beside the email pipeline and for the same reason: what a provider
// needs is configuration and one hand-off point, and the composition root is
// where a credential is unredacted exactly once. The surfaces receive the
// analytics sink the composition built; nothing else reads the credentials.
package bootstrap

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
)

// ComposeTelemetry builds the error reporter, the analytics sink and the
// metrics registry of the process.
//
// It reads only the logger, the clock and the observability fields of
// Options; the journey-only fields are ignored, which is why both `arena
// server` and `arena worker` call this same function with the fields they
// have. An empty credential disables that provider instead of refusing the
// boot: telemetry is optional and the application is not.
func ComposeTelemetry(options Options) (*observability.Telemetry, error) {
	missing := make([]string, 0, 2)
	if options.Logger == nil {
		missing = append(missing, "logger")
	}
	if options.Clock == nil {
		missing = append(missing, "clock")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("%w: telemetry: missing %s", ErrIncompleteComposition, strings.Join(missing, ", "))
	}

	telemetry, err := observability.New(observability.Config{
		Logger:            options.Logger,
		Clock:             options.Clock,
		Environment:       string(options.Env),
		SentryDSN:         string(options.SentryDSN.Unredacted()),
		PostHogAPIKey:     string(options.PostHogAPIKey.Unredacted()),
		PostHogHost:       options.PostHogHost,
		SampleRatePercent: options.AnalyticsSampleRate,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: telemetry: %w", ErrIncompleteComposition, err)
	}

	options.Logger.Info("observability: telemetry composed",
		slog.Bool("error_reporting", options.SentryDSN.IsSet()),
		slog.Bool("analytics", options.PostHogAPIKey.IsSet()),
		slog.Int("analytics_sample_rate_percent", options.AnalyticsSampleRate),
	)
	return telemetry, nil
}
