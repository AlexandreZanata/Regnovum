// Tests of internal/platform/httpserver (P02-T05): hardening defaults,
// health endpoints, mux composition and a real listen/graceful-shutdown
// lifecycle on an ephemeral port.
package httpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httplimits"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// newTestLogger returns a logger that only prints its captured records when
// the test fails.
func newTestLogger(t *testing.T) *slog.Logger {
	t.Helper()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("captured lifecycle logs:\n%s", buffer.String())
		}
	})
	return logger
}

func TestNewAppliesHardeningDefaults(t *testing.T) {
	t.Parallel()

	server, err := httpserver.New(httpserver.Options{
		Addr:    "127.0.0.1:0",
		Handler: http.NotFoundHandler(),
		Logger:  newTestLogger(t),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if got := server.HTTP.ReadHeaderTimeout; got != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s (slowloris guard)", got)
	}
	if got := server.HTTP.ReadTimeout; got != 20*time.Second {
		t.Errorf("ReadTimeout = %v, want 20s", got)
	}
	if got := server.HTTP.WriteTimeout; got != 30*time.Second {
		t.Errorf("WriteTimeout = %v, want 30s", got)
	}
	if got := server.HTTP.IdleTimeout; got != 2*time.Minute {
		t.Errorf("IdleTimeout = %v, want 2m", got)
	}
	if got := server.HTTP.MaxHeaderBytes; got != 64<<10 {
		t.Errorf("MaxHeaderBytes = %d, want 65536 (below the 1MiB default)", got)
	}

	// The per-route deadlines of P16-T02 have to fit inside the transport
	// budget: a route that aims to finish after the server stops writing is a
	// deadline that can never be honored, so the two are asserted together
	// here, where both numbers are visible.
	if longest := httplimits.LongestTimeout(); longest >= server.HTTP.WriteTimeout {
		t.Errorf("longest route deadline = %v, want below the %v write timeout", longest, server.HTTP.WriteTimeout)
	}
	if longest := httplimits.LongestTimeout(); longest <= 0 {
		t.Errorf("longest route deadline = %v, want a positive budget", longest)
	}
}

func TestNewRequiresEssentialOptions(t *testing.T) {
	t.Parallel()

	logger := newTestLogger(t)

	if _, err := httpserver.New(httpserver.Options{Handler: http.NotFoundHandler(), Logger: logger}); err == nil {
		t.Error("New() without Addr should fail")
	}
	if _, err := httpserver.New(httpserver.Options{Addr: "127.0.0.1:0", Logger: logger}); err == nil {
		t.Error("New() without Handler should fail")
	}
	if _, err := httpserver.New(httpserver.Options{Addr: "127.0.0.1:0", Handler: http.NotFoundHandler()}); err == nil {
		t.Error("New() without Logger should fail")
	}
}

func TestNewHonorsExplicitOptions(t *testing.T) {
	t.Parallel()

	server, err := httpserver.New(httpserver.Options{
		Addr:              "127.0.0.1:0",
		Handler:           http.NotFoundHandler(),
		Logger:            newTestLogger(t),
		ReadTimeout:       time.Second,
		ReadHeaderTimeout: 500 * time.Millisecond,
		WriteTimeout:      2 * time.Second,
		IdleTimeout:       3 * time.Second,
		MaxHeaderBytes:    1024,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if got := server.HTTP.ReadTimeout; got != time.Second {
		t.Errorf("ReadTimeout = %v, want explicit 1s", got)
	}
	if got := server.HTTP.MaxHeaderBytes; got != 1024 {
		t.Errorf("MaxHeaderBytes = %d, want explicit 1024", got)
	}
}

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("GET /health/live", httpserver.LiveHandler())
	mux.Handle("GET /health/ready", httpserver.ReadyHandler())

	tests := []struct {
		path       string
		wantStatus string
	}{
		{"/health/live", "live"},
		{"/health/ready", "ready"},
	}

	for _, test := range tests {
		recorder := do(t, mux, test.path)
		if recorder.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", test.path, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("%s Content-Type = %q, want application/json", test.path, got)
		}

		var body struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s body is not valid JSON: %v", test.path, err)
		}
		if body.Status != test.wantStatus {
			t.Errorf("%s status field = %q, want %q", test.path, body.Status, test.wantStatus)
		}
	}
}

func TestReadyHandlerWithCheckers(t *testing.T) {
	t.Parallel()

	// 1. Healthy checker
	healthyChecker := httpserver.ReadyCheckerFunc(func(ctx context.Context) error {
		return nil
	})
	handler := httpserver.ReadyHandler(healthyChecker)
	recorder := do(t, handler, "/health/ready")
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Status != "ready" {
		t.Errorf("status = %q, want ready", body.Status)
	}

	// 2. Failing checker with sensitive error
	sensitiveErr := errors.New("dial tcp 127.0.0.1:5432: connection refused (dsn: postgres://user:secret@host/db)")
	failingChecker := httpserver.ReadyCheckerFunc(func(ctx context.Context) error {
		return sensitiveErr
	})
	failingHandler := httpserver.ReadyHandler(failingChecker)
	failingRecorder := do(t, failingHandler, "/health/ready")
	if failingRecorder.Code != http.StatusServiceUnavailable {
		t.Errorf("failing status = %d, want 503", failingRecorder.Code)
	}
	if err := json.Unmarshal(failingRecorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Status != "unavailable" {
		t.Errorf("failing status = %q, want unavailable", body.Status)
	}

	// Proves no error text or credentials leaked to the client
	responseBody := failingRecorder.Body.String()
	if strings.Contains(responseBody, "connection refused") ||
		strings.Contains(responseBody, "secret") ||
		strings.Contains(responseBody, "postgres://") {
		t.Fatalf("response leaked sensitive driver error: %s", responseBody)
	}

	// 3. Multiple checkers where one fails
	multiHandler := httpserver.ReadyHandler(healthyChecker, failingChecker)
	multiRecorder := do(t, multiHandler, "/health/ready")
	if multiRecorder.Code != http.StatusServiceUnavailable {
		t.Errorf("multi status = %d, want 503", multiRecorder.Code)
	}

	// 4. NewMux wires checkers
	mux, err := httpserver.NewMux(stubIDs{value: "test-id"}, nil, securityheaders.Config{}, failingChecker)
	if err != nil {
		t.Fatalf("NewMux error: %v", err)
	}
	muxRecorder := do(t, mux, "/health/ready")
	if muxRecorder.Code != http.StatusServiceUnavailable {
		t.Errorf("NewMux with failing checker status = %d, want 503", muxRecorder.Code)
	}
}

func TestNewMuxRoutesAndCorrelates(t *testing.T) {
	t.Parallel()

	handler, err := httpserver.NewMux(stubIDs{value: "test-id-123"}, nil, securityheaders.Config{})
	if err != nil {
		t.Fatalf("NewMux() error = %v", err)
	}

	recorder := do(t, handler, "/health/live")
	if recorder.Code != http.StatusOK {
		t.Errorf("/health/live status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "test-id-123" {
		t.Errorf("X-Request-Id = %q, want injected stub value", got)
	}

	if recorder := do(t, handler, "/unknown"); recorder.Code != http.StatusNotFound {
		t.Errorf("unknown route status = %d, want 404", recorder.Code)
	}
}

// TestNewMuxWithServesASurfaceInsideTheStack covers P18-T07A: a route the
// registry declares is served by the surface that claims it, inside the
// platform middleware (the request id header is what proves the position), and
// not by the placeholder a module that is not composed leaves behind.
// surfacePath is the route the composition tests mount. The registry is a
// process-wide list, so the provider is installed once for the whole test
// binary: a provider registered per test would declare the route twice, which
// the registry itself refuses.
const surfacePath = "/composed-surface"

func init() {
	httpserver.RegisterRouteProvider(func() []httpserver.Route {
		return []httpserver.Route{{Method: http.MethodGet, Path: surfacePath}}
	})
}

func TestNewMuxWithServesASurfaceInsideTheStack(t *testing.T) {
	t.Parallel()

	path := surfacePath

	served := 0
	handler, err := httpserver.NewMuxWith(
		stubIDs{value: "composed-id"}, nil, securityheaders.Config{},
		[]httpserver.Surface{{
			Routes: []httpserver.Route{{Method: http.MethodGet, Path: path}},
			Register: func(mux *http.ServeMux) error {
				mux.HandleFunc("GET "+path, func(writer http.ResponseWriter, request *http.Request) {
					served++
					writer.WriteHeader(http.StatusOK)
				})
				return nil
			},
		}},
	)
	if err != nil {
		t.Fatalf("NewMuxWith() error = %v", err)
	}

	recorder := do(t, handler, path)
	if recorder.Code != http.StatusOK || served != 1 {
		t.Errorf("GET %s status = %d (handler calls = %d), want the mounted surface to answer", path, recorder.Code, served)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "composed-id" {
		t.Errorf("X-Request-Id = %q, want the platform middleware to cover the mounted route", got)
	}

	// Without the surface the same declared route is a placeholder: that is the
	// difference between a route the process declares and one it serves.
	placeholder, err := httpserver.NewMuxWith(stubIDs{value: "composed-id"}, nil, securityheaders.Config{}, nil)
	if err != nil {
		t.Fatalf("NewMuxWith() without surfaces error = %v", err)
	}
	if recorder := do(t, placeholder, path); recorder.Code != http.StatusNotFound {
		t.Errorf("GET %s without a surface = %d, want the placeholder 404", path, recorder.Code)
	}
}

// TestNewMuxWithRefusesSurfacesItCannotServe is the fail-closed half: a surface
// that is incomplete, undeclared, duplicated or in conflict with a platform
// route fails the composition instead of booting a mux that answers wrongly.
func TestNewMuxWithRefusesSurfacesItCannotServe(t *testing.T) {
	t.Parallel()

	undeclared := []httpserver.Route{{Method: http.MethodGet, Path: "/never-declared"}}
	health := []httpserver.Route{{Method: http.MethodGet, Path: "/health/live"}}
	surface := []httpserver.Route{{Method: http.MethodGet, Path: surfacePath}}
	ok := func(mux *http.ServeMux) error { return nil }

	cases := []struct {
		name     string
		surfaces []httpserver.Surface
		want     string
	}{
		{
			name:     "a surface that registers nothing",
			surfaces: []httpserver.Surface{{Routes: health}},
			want:     "registers nothing",
		},
		{
			name:     "a surface that declares no routes",
			surfaces: []httpserver.Surface{{Register: ok}},
			want:     "declares no routes",
		},
		{
			name:     "a static surface that claims a route of the registry",
			surfaces: []httpserver.Surface{{Routes: surface, Register: ok, Static: true}},
			want:     "is static and declares",
		},
		{
			name:     "a route the registry does not declare",
			surfaces: []httpserver.Surface{{Routes: undeclared, Register: ok}},
			want:     "does not declare",
		},
		{
			name: "two surfaces claiming one route",
			surfaces: []httpserver.Surface{
				{Routes: surface, Register: ok},
				{Routes: surface, Register: ok},
			},
			want: "claimed by two surfaces",
		},
		{
			name:     "a surface claiming a platform route",
			surfaces: []httpserver.Surface{{Routes: health, Register: ok}},
			want:     "which is a platform route",
		},
		{
			name: "a surface that panics while registering",
			surfaces: []httpserver.Surface{{
				Routes:   surface,
				Register: func(*http.ServeMux) error { panic("duplicate pattern") },
			}},
			want: "route registration panicked",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			handler, err := httpserver.NewMuxWith(stubIDs{value: "id"}, nil, securityheaders.Config{}, testCase.surfaces)
			if err == nil {
				t.Fatalf("NewMuxWith() accepted the composition and returned %T", handler)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("NewMuxWith() error = %q, want it to report %q", err, testCase.want)
			}
		})
	}
}

// TestNewMuxWithServesAStaticSurface covers P18-T07C: the frontend build is
// content, not an operation, so it is mounted inside the platform stack without
// entering the registry the contract is compared with — the router knows the
// address space because the surface registers it, not because a route declares
// it.
func TestNewMuxWithServesAStaticSurface(t *testing.T) {
	t.Parallel()

	const prefix = "/assets"
	handler, err := httpserver.NewMuxWith(
		stubIDs{value: "static-id"}, nil, securityheaders.Config{},
		[]httpserver.Surface{{
			Static: true,
			Register: func(mux *http.ServeMux) error {
				mux.HandleFunc("GET "+prefix+"/", func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					_, _ = writer.Write([]byte("/* build */"))
				})
				return nil
			},
		}},
	)
	if err != nil {
		t.Fatalf("NewMuxWith() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, prefix+"/styles/arena-abc123.css", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET an address of the static surface = %d, want 200", recorder.Code)
	}
	if recorder.Header().Get("X-Request-Id") == "" {
		t.Error("the static surface answered outside the platform middleware: no request id")
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want the policy the surface set", got)
	}

	// A route the registry declares is still answered by the placeholder when
	// no module surface claims it: the static surface declares nothing.
	placeholder := httptest.NewRecorder()
	handler.ServeHTTP(placeholder, httptest.NewRequest(http.MethodGet, surfacePath, nil))
	if placeholder.Code != http.StatusNotFound {
		t.Errorf("GET %s = %d, want the placeholder of an uncomposed route", surfacePath, placeholder.Code)
	}
}

func TestListenFailsFastOnBusyPort(t *testing.T) {
	t.Parallel()

	// Occupy a real port, then demand it: the second Listen must fail
	// eagerly, before any serve loop starts.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	busy, err := httpserver.New(httpserver.Options{
		Addr:    listener.Addr().String(),
		Handler: http.NotFoundHandler(),
		Logger:  newTestLogger(t),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := busy.Listen(); err == nil {
		t.Fatal("Listen() on an occupied port should fail fast")
	}
}

// TestGracefulShutdownLifecycle serves on an ephemeral port, cancels the
// context (the process-edge signal stand-in) and proves the serve loop
// returns nil and the listener is released for immediate rebinding.
func TestGracefulShutdownLifecycle(t *testing.T) {
	t.Parallel()

	handler, err := httpserver.NewMux(stubIDs{value: "lifecycle-id"}, nil, securityheaders.Config{})
	if err != nil {
		t.Fatalf("NewMux() error = %v", err)
	}
	server, err := httpserver.New(httpserver.Options{
		Addr:    "127.0.0.1:0",
		Handler: handler,
		Logger:  newTestLogger(t),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := server.Listen(); err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- server.Run(ctx) }()

	// Serve loop is up: probe the live endpoint.
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + server.Addr() + "/health/live")
	if err != nil {
		t.Fatalf("GET /health/live error = %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /health/live status = %d, want 200", response.StatusCode)
	}

	cancel()

	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("Run() after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s of cancellation")
	}

	// The port is released: a fresh listener must bind the same address.
	fresh, err := net.Listen("tcp", server.Addr())
	if err != nil {
		t.Fatalf("listener not released after graceful shutdown: %v", err)
	}
	_ = fresh.Close()
}

// TestRunReturnsListenerErrors proves Run surfaces real listener failures
// instead of swallowing them as lifecycle noise.
func TestRunReturnsListenerErrors(t *testing.T) {
	t.Parallel()

	server, err := httpserver.New(httpserver.Options{
		Addr:    "127.0.0.1:0",
		Handler: http.NotFoundHandler(),
		Logger:  newTestLogger(t),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Run without Listen: Serve(nil) must fail immediately.
	err = server.Run(context.Background())
	if err == nil {
		t.Fatal("Run() without Listen() should return the listener error")
	}
}

type stubIDs struct{ value string }

func (stub stubIDs) NewID() string { return stub.value }

func do(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://internal"+path, nil)
	if err != nil {
		t.Fatalf("http.NewRequest(%s) error = %v", path, err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
