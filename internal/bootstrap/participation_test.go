// Tests of the participation composition (P18-T07B) that do not need
// PostgreSQL: every refusal is decided before anything is constructed, so it is
// decided before anything connects.
package bootstrap_test

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	arenashtml "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/html"
	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
)

// cursorSecret is the key every participation composition needs. It is a
// fixture, never a credential: 32 printable bytes, the minimum the cursor
// codecs accept.
const cursorSecret = "participation-cursor-secret-fixture"

// participationOptions is a composition that lacks nothing but the field each
// case removes.
func participationOptions(t *testing.T) bootstrap.Options {
	t.Helper()

	options := completeOptions(t)
	options.CursorSecret = []byte(cursorSecret)
	return options
}

func TestComposeParticipationRefusesAnIncompleteComposition(t *testing.T) {
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
		{
			name:    "without the cursor signing secret",
			remove:  func(options *bootstrap.Options) { options.CursorSecret = nil },
			dropped: "cursor signing secret",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			options := participationOptions(t)
			testCase.remove(&options)

			surface, err := bootstrap.ComposeParticipation(options)
			if err == nil {
				t.Fatalf("ComposeParticipation() built a surface from an incomplete composition (%d routes)", len(surface.Routes()))
			}
			if !errors.Is(err, bootstrap.ErrIncompleteComposition) {
				t.Errorf("ComposeParticipation() error = %v, want it to wrap ErrIncompleteComposition", err)
			}
			if !strings.Contains(err.Error(), testCase.dropped) {
				t.Errorf("ComposeParticipation() error = %q, want it to name the missing %q", err, testCase.dropped)
			}
		})
	}
}

// TestComposeParticipationRefusesAWeakCursorSecret: the key size is the rule of
// the cursor codec, and the refusal must reach the operator at boot instead of
// inside a use case constructor. The composition does not shorten the
// requirement to get a surface built.
func TestComposeParticipationRefusesAWeakCursorSecret(t *testing.T) {
	t.Parallel()

	options := participationOptions(t)
	options.CursorSecret = []byte(strings.Repeat("k", 31))

	surface, err := bootstrap.ComposeParticipation(options)
	if err == nil {
		t.Fatalf("ComposeParticipation() accepted a 31-byte secret (%d routes)", len(surface.Routes()))
	}
	if !errors.Is(err, bootstrap.ErrIncompleteComposition) {
		t.Errorf("ComposeParticipation() error = %v, want it to wrap ErrIncompleteComposition", err)
	}
	if !strings.Contains(err.Error(), "cursor signing secret") {
		t.Errorf("ComposeParticipation() error = %q, want it to name the cursor signing secret", err)
	}
}

func TestComposeParticipationRefusesAnUnknownEnvironment(t *testing.T) {
	t.Parallel()

	options := participationOptions(t)
	options.Env = config.Env("staging")

	if _, err := bootstrap.ComposeParticipation(options); err == nil {
		t.Fatal("ComposeParticipation() accepted an environment outside development, test and production")
	}
}

// TestTheParticipationSurfaceDeclaresWhatTheJourneyAnswers is the difference
// between the journey and the module: the arenas module also serves the public
// document of an Arena, and this surface must not claim a route it never
// serves — the router would leave the real handler without a mux pattern and
// the contract test would compare a reachable endpoint with nothing behind it.
func TestTheParticipationSurfaceDeclaresWhatTheJourneyAnswers(t *testing.T) {
	t.Parallel()

	surface, err := bootstrap.ComposeParticipation(participationOptions(t))
	if err != nil {
		t.Fatalf("ComposeParticipation() error = %v", err)
	}

	want := arenashtml.ParticipationRoutes()
	if !reflect.DeepEqual(surface.Routes(), want) {
		t.Fatalf("the surface declares %v, want the journey routes %v", surface.Routes(), want)
	}
	for _, route := range surface.Routes() {
		if route.Path == "/d/{slug}" {
			t.Errorf("the surface claims %s, which the read handler serves", route.String())
		}
	}
	if len(surface.Routes()) != 5 {
		t.Errorf("the journey declares %d routes, want the page and its four transitions", len(surface.Routes()))
	}

	second, err := bootstrap.ComposeParticipation(participationOptions(t))
	if err != nil {
		t.Fatalf("second ComposeParticipation() error = %v", err)
	}
	if !reflect.DeepEqual(surface.Routes(), second.Routes()) {
		t.Errorf("two compositions declare different routes:\n%v\n%v", surface.Routes(), second.Routes())
	}
}

func TestTheParticipationSurfaceRefusesASecondMount(t *testing.T) {
	t.Parallel()

	surface, err := bootstrap.ComposeParticipation(participationOptions(t))
	if err != nil {
		t.Fatalf("ComposeParticipation() error = %v", err)
	}

	mux := http.NewServeMux()
	if err := surface.Mount(mux); err != nil {
		t.Fatalf("first Mount() error = %v", err)
	}
	if err := surface.Mount(mux); err == nil {
		t.Fatal("second Mount() registered the surface again")
	} else if !strings.Contains(err.Error(), "already mounted") {
		t.Errorf("second Mount() error = %q, want it to report the duplicate mount", err)
	}

	unmounted, err := bootstrap.ComposeParticipation(participationOptions(t))
	if err != nil {
		t.Fatalf("ComposeParticipation() error = %v", err)
	}
	if err := unmounted.Mount(nil); err == nil {
		t.Error("Mount(nil) accepted a nil mux")
	}
}
