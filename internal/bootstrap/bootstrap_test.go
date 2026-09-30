// Tests of the composition root (P18-T07A) that do not need PostgreSQL: the
// refusals are decided before anything is constructed, so they are decided
// before anything connects.
package bootstrap_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
)

// manifestFixture is the asset build the composition needs. It is loaded from
// disk — not built in memory — because reading the manifest back is part of
// what the composition does at boot.
func manifestFixture(t *testing.T) assets.Manifest {
	t.Helper()
	manifest, err := assets.LoadFile("testdata/assets")
	if err != nil {
		t.Fatalf("assets.LoadFile(testdata/assets) error = %v", err)
	}
	return manifest
}

// lazyPool is a pool handle that never connects: pgxpool dials on demand, and
// every test in this file is refused before the first query. The address is
// deliberately unreachable so a test that started querying would fail loudly
// instead of silently passing against a development database.
func lazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://arena:arena-local-dev@127.0.0.1:1/arena?sslmode=disable")
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// completeOptions is a composition that lacks nothing but the field each case
// removes.
func completeOptions(t *testing.T) bootstrap.Options {
	t.Helper()
	return bootstrap.Options{
		Env:    config.EnvDevelopment,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool:   lazyPool(t),
		Clock:  clockseed.NewClock(),
		Random: clockseed.NewRandom(),
		Assets: manifestFixture(t),
	}
}

func TestComposeAccountRefusesAnIncompleteComposition(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		remove  func(options *bootstrap.Options)
		dropped string
	}{
		{
			name:    "without a logger",
			remove:  func(options *bootstrap.Options) { options.Logger = nil },
			dropped: "logger",
		},
		{
			name:    "without a clock",
			remove:  func(options *bootstrap.Options) { options.Clock = nil },
			dropped: "clock",
		},
		{
			name:    "without entropy",
			remove:  func(options *bootstrap.Options) { options.Random = nil },
			dropped: "entropy source",
		},
		{
			name:    "without a database pool",
			remove:  func(options *bootstrap.Options) { options.Pool = nil },
			dropped: "postgres pool",
		},
		{
			name:    "without the frontend build",
			remove:  func(options *bootstrap.Options) { options.Assets = assets.Manifest{} },
			dropped: "asset manifest",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			options := completeOptions(t)
			testCase.remove(&options)

			surface, err := bootstrap.ComposeAccount(options)
			if err == nil {
				t.Fatalf("ComposeAccount() built a surface from an incomplete composition (%d routes)", len(surface.Routes()))
			}
			if !errors.Is(err, bootstrap.ErrIncompleteComposition) {
				t.Errorf("ComposeAccount() error = %v, want it to wrap ErrIncompleteComposition", err)
			}
			if !strings.Contains(err.Error(), testCase.dropped) {
				t.Errorf("ComposeAccount() error = %q, want it to name the missing %q", err, testCase.dropped)
			}
		})
	}
}

// TestComposeAccountNamesEveryMissingDependencyAtOnce is the difference between
// one boot failure and four: an operator fixing the environment sees the whole
// list, not the first entry.
func TestComposeAccountNamesEveryMissingDependencyAtOnce(t *testing.T) {
	t.Parallel()

	options := completeOptions(t)
	options.Pool = nil
	options.Clock = nil
	options.Assets = assets.Manifest{}

	_, err := bootstrap.ComposeAccount(options)
	if err == nil {
		t.Fatal("ComposeAccount() built a surface without pool, clock and assets")
	}
	for _, missing := range []string{"clock", "postgres pool", "asset manifest"} {
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("ComposeAccount() error = %q, want it to name %q", err, missing)
		}
	}
	if strings.Contains(err.Error(), "logger") {
		t.Errorf("ComposeAccount() error = %q, but the logger was provided", err)
	}
}

func TestComposeAccountRefusesAnUnknownEnvironment(t *testing.T) {
	t.Parallel()

	options := completeOptions(t)
	options.Env = config.Env("staging")

	_, err := bootstrap.ComposeAccount(options)
	if err == nil {
		t.Fatal("ComposeAccount() accepted an environment outside development, test and production")
	}
	if !errors.Is(err, bootstrap.ErrIncompleteComposition) || !strings.Contains(err.Error(), "staging") {
		t.Errorf("ComposeAccount() error = %v, want a refusal naming the environment", err)
	}
}

// TestComposeAccountRefusesProductionWithoutAnEmailProvider is the fail-closed
// half of the transactional email decision: production serves registrations,
// and a registration is a link that has to arrive, so an environment that
// cannot deliver refuses to build the journey instead of serving forms whose
// links go nowhere. The refusal names the two variables the operator has to
// set: "no email provider" without a variable name is a puzzle, not an error
// message.
func TestComposeAccountRefusesProductionWithoutAnEmailProvider(t *testing.T) {
	t.Parallel()

	options := completeOptions(t)
	options.Env = config.EnvProduction

	surface, err := bootstrap.ComposeAccount(options)
	if err == nil {
		t.Fatalf("ComposeAccount() served production without an email provider (%d routes)", len(surface.Routes()))
	}
	if !errors.Is(err, bootstrap.ErrIncompleteComposition) {
		t.Errorf("ComposeAccount() error = %v, want it to wrap ErrIncompleteComposition", err)
	}
	for _, variable := range []string{config.ResendAPIKeyVariable, config.EmailFromVariable} {
		if !strings.Contains(err.Error(), variable) {
			t.Errorf("ComposeAccount() error = %q, want it to name %s", err, variable)
		}
	}
}

// TestComposeAccountServesProductionWithAnEmailProvider is the other half, and
// the reason P19-T02A exists: with the credential and the sender address
// configured, production composes — the durable queue behind the flow and the
// delivery handler in front of it — instead of refusing the boot.
//
// Nothing here reaches PostgreSQL. The refusals are decided before anything is
// constructed, and building handles is not querying: the pool in these options
// points at an unreachable address, so a composition that dialed would fail
// loudly rather than pass quietly.
func TestComposeAccountServesProductionWithAnEmailProvider(t *testing.T) {
	t.Parallel()

	options := completeOptions(t)
	options.Env = config.EnvProduction
	options.EmailFrom = "Arena <no-reply@arena.invalid>"
	options.EmailAPIKey = config.NewSecret("re_live_provider_key")

	surface, err := bootstrap.ComposeAccount(options)
	if err != nil {
		t.Fatalf("ComposeAccount() refused a complete production composition: %v", err)
	}
	if len(surface.Routes()) == 0 {
		t.Fatal("production composed a surface without routes")
	}
	if surface.LocalSink() != nil {
		t.Error("production installed the in-process email sink")
	}
	if surface.SinkDirectory() != "" {
		t.Error("production installed a directory of account codes")
	}
}
