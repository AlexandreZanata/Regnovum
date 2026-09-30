// Package httpserver owns the transport lifecycle of Regnovum (P02-T05):
// a hardened net/http server with explicit timeouts, a bounded header size,
// graceful shutdown driven by a context, and the health endpoints required
// by the plan.
//
// Hardening policy (docs/DEPLOYMENT.md, SCALABILITY.md):
//   - ReadHeaderTimeout guards against slowloris-style header starvation;
//   - ReadTimeout, WriteTimeout and IdleTimeout bound every request phase;
//   - MaxHeaderBytes is tightened well below the permissive stdlib default;
//   - shutdown always releases the listener first and then waits, within a
//     fixed deadline, for in-flight requests to finish.
//
// The package stays transport-only: routing composition of the platform
// middleware (request ID correlation) and signal handling live in cmd/.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httplimits"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/requestid"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Default hardening values. They are constants, not configuration, because
// the plan does not expose them as tunables yet; operators get them by
// default on every environment.
const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 20 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 120 * time.Second

	// defaultMaxHeaderBytes tightens the stdlib default (1 MiB) to 64 KiB:
	// generous for session cookies and correlation headers, hostile to
	// header-abuse memory pressure.
	defaultMaxHeaderBytes = 64 << 10

	// shutdownTimeout bounds the graceful drain of in-flight requests after
	// the shutdown signal. Processes that exceed it are deliberately allowed
	// to be killed by the supervisor.
	shutdownTimeout = 10 * time.Second
)

// Options configures a Server. The timeout and header fields default to the
// hardened constants above when left zero; tests may tighten them further.
type Options struct {
	// Addr is the TCP listen address, for example 127.0.0.1:8080.
	Addr string

	// Handler is the root handler; composition (request ID, health routes)
	// is built with NewMux in cmd/.
	Handler http.Handler

	// Logger receives lifecycle records (listening, shutdown). Required.
	Logger *slog.Logger

	ReadTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
}

// Server wraps a hardened *http.Server with an explicit listen/shutdown
// lifecycle driven by a context.
type Server struct {
	// HTTP exposes the configured server for inspection by tests.
	HTTP *http.Server

	logger   *slog.Logger
	listener net.Listener
}

// New builds a hardened server, applying the package defaults to any unset
// timeout or header limit.
func New(options Options) (*Server, error) {
	if options.Addr == "" {
		return nil, errors.New("httpserver: listen address is required")
	}
	if options.Handler == nil {
		return nil, errors.New("httpserver: handler is required")
	}
	if options.Logger == nil {
		return nil, errors.New("httpserver: logger is required")
	}

	readHeaderTimeout := options.ReadHeaderTimeout
	if readHeaderTimeout == 0 {
		readHeaderTimeout = defaultReadHeaderTimeout
	}
	readTimeout := options.ReadTimeout
	if readTimeout == 0 {
		readTimeout = defaultReadTimeout
	}
	writeTimeout := options.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = defaultWriteTimeout
	}
	idleTimeout := options.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = defaultIdleTimeout
	}
	maxHeaderBytes := options.MaxHeaderBytes
	if maxHeaderBytes == 0 {
		maxHeaderBytes = defaultMaxHeaderBytes
	}

	return &Server{
		HTTP: &http.Server{
			Addr:              options.Addr,
			Handler:           options.Handler,
			ReadTimeout:       readTimeout,
			ReadHeaderTimeout: readHeaderTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
		},
		logger: options.Logger,
	}, nil
}

// Listen binds the TCP listener eagerly, so a bad address or a busy port
// fails fast before the serve loop starts.
func (server *Server) Listen() error {
	listener, err := net.Listen("tcp", server.HTTP.Addr)
	if err != nil {
		return err
	}
	server.listener = listener
	return nil
}

// Addr returns the real bound address after Listen (resolving port 0);
// before Listen it returns the configured address.
func (server *Server) Addr() string {
	if server.listener != nil {
		return server.listener.Addr().String()
	}
	return server.HTTP.Addr
}

// Run serves until ctx is cancelled (for example by SIGTERM at the process
// edge), then performs a graceful shutdown: the listener is released
// immediately and in-flight requests get shutdownTimeout to finish.
//
// A context-driven stop returns nil; http.ErrServerClosed is never treated
// as an error. Real listener failures are returned.
func (server *Server) Run(ctx context.Context) error {
	if server.listener == nil {
		return errors.New("httpserver: Run called before Listen")
	}

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.HTTP.Serve(server.listener)
	}()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		server.logger.Info("http server: shutting down gracefully")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		shutdownErr := server.HTTP.Shutdown(shutdownCtx)
		<-serveResult // Serve always returns ErrServerClosed after Shutdown.

		if shutdownErr != nil {
			server.logger.Error("http server: graceful shutdown failed", slog.String("error", shutdownErr.Error()))
			return shutdownErr
		}
		server.logger.Info("http server: graceful shutdown complete")
		return nil
	}
}

// mountSafely runs one mount, converting the panic net/http raises for a
// pattern that conflicts with one already registered into an error. The
// platform routes are registered first, so a surface that claims one of them
// is refused at composition time — with a message naming the surface — instead
// of taking the process down with a stack trace at boot.
func mountSafely(mount func(mux *http.ServeMux) error, mux *http.ServeMux) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("route registration panicked: %v", recovered)
		}
	}()
	return mount(mux)
}

// statusBody is the single-field JSON document of the health endpoints.
type statusBody struct {
	Status string `json:"status"`
}

// ReadyChecker checks whether a dependency (e.g. database pool) is ready to serve traffic.
type ReadyChecker interface {
	CheckReadiness(ctx context.Context) error
}

// ReadyCheckerFunc adapts a regular function to the ReadyChecker interface.
type ReadyCheckerFunc func(ctx context.Context) error

// CheckReadiness executes the underlying readiness check function.
func (f ReadyCheckerFunc) CheckReadiness(ctx context.Context) error {
	return f(ctx)
}

// defaultReadyTimeout bounds individual readiness checks to avoid hanging probes.
const defaultReadyTimeout = 2 * time.Second

// LiveHandler reports liveness: the process is running. It never inspects
// dependencies, so a wedged database cannot make the supervisor restart a
// healthy-but-busy process (readiness is the deployment gate instead).
func LiveHandler() http.Handler {
	return writeStatus("live")
}

// ReadyHandler reports readiness. It executes every registered ReadyChecker;
// if all succeed (or none are configured), it responds with 200 OK and {"status":"ready"}.
// If any checker fails, it responds with 503 Service Unavailable and {"status":"unavailable"}.
// It never leaks internal errors, connection strings or credentials to the client.
func ReadyHandler(checkers ...ReadyChecker) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		for _, checker := range checkers {
			if checker == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(request.Context(), defaultReadyTimeout)
			err := checker.CheckReadiness(ctx)
			cancel()
			if err != nil {
				writer.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(writer).Encode(statusBody{Status: "unavailable"})
				return
			}
		}

		writer.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(writer).Encode(statusBody{Status: "ready"})
	})
}

// writeStatus renders the fixed JSON health document.
func writeStatus(status string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(writer).Encode(statusBody{Status: status})
	})
}

// NewMux composes the platform router from the route registry (routes.go):
// the request bound around the health routes registered with explicit method
// patterns, so wrong methods answer 405 automatically, under request ID
// correlation, locale negotiation and the browser security policy of the
// environment. Registration failures (duplicate or malformed registry
// entries) return an error instead of panicking at boot.
//
// The order of the layers is part of the contract, not an accident:
//
//   - the security policy is outermost, so it covers responses that never
//     reach a module handler — 404 on an unknown path, 405 on a wrong method,
//     readiness failures, and every refusal written by the layers below;
//   - the request bound sits inside correlation and locale, because a refusal
//     is a problem document: it has to carry the request id and be titled in
//     the negotiated interface locale;
//   - the bound sits outside the mux, so a route cannot be served without it.
func NewMux(ids ports.IDGenerator, locales *locale.Resolver, security securityheaders.Config, readyCheckers ...ReadyChecker) (http.Handler, error) {
	return NewMuxWith(ids, locales, security, nil, readyCheckers...)
}

// Surface is one module surface a composition mounts: the routes it answers and
// the registration that installs them on the mux.
//
// The routes are declared here, and not left implicit in the registration,
// because the platform router has to know them before it builds the mux. The
// registry (routes.go) knows every route the process *declares*, including the
// ones whose module is not composed yet; without the declaration, those routes
// would be registered as placeholders and then collide with the real surface
// mounting on top of them.
type Surface struct {
	// Routes is the surface route list, in the same vocabulary the registry
	// and the contract use. A static surface declares none: its addresses are
	// published by the asset manifest, not by the registry.
	Routes []Route
	// Register installs the surface on the mux. A registration that fails
	// aborts the composition.
	Register func(mux *http.ServeMux) error
	// Static marks a surface that serves content a build declares — today the
	// hashed frontend build (P18-T07C) — instead of application endpoints. It
	// is mounted inside the same middleware stack, but it takes no part in the
	// registry/contract comparison, because a stylesheet is not an operation of
	// the API. Declaring a route beside Static is contradictory and refused.
	Static bool
}

// NewMuxWith composes the same router as NewMux and additionally mounts the
// given module surfaces, inside the platform middleware stack (request id,
// locale and security policy cover them) and in the declared order.
//
// The distinction the router makes is between a route that is *declared* and a
// route that is *served*: a declared route no surface claims keeps the
// placeholder answer of a module that is not composed yet, and a surface that
// claims a route the registry does not declare is refused, because the
// contract would not know the endpoint a person can reach.
func NewMuxWith(ids ports.IDGenerator, locales *locale.Resolver, security securityheaders.Config, surfaces []Surface, readyCheckers ...ReadyChecker) (http.Handler, error) {
	mux := http.NewServeMux()

	declared := make(map[string]bool)
	for _, route := range RegisteredRoutes() {
		declared[route.String()] = true
	}

	// The platform routes are answered by the platform. A module surface that
	// claimed one of them would not conflict — it would silently replace it,
	// because the mux is built from whatever is left unclaimed.
	platform := make(map[string]bool)
	for _, route := range HealthRoutes() {
		platform[route.String()] = true
	}

	claimed := make(map[string]bool)
	for index, surface := range surfaces {
		if surface.Register == nil {
			return nil, fmt.Errorf("httpserver: surface %d registers nothing", index)
		}
		if surface.Static {
			if len(surface.Routes) != 0 {
				return nil, fmt.Errorf("httpserver: surface %d is static and declares %d routes; static content is published by its build, not by the registry", index, len(surface.Routes))
			}
			continue
		}
		if len(surface.Routes) == 0 {
			return nil, fmt.Errorf("httpserver: surface %d declares no routes", index)
		}
		for _, route := range surface.Routes {
			if platform[route.String()] {
				return nil, fmt.Errorf("httpserver: surface %d claims %s, which is a platform route", index, route.String())
			}
			if !declared[route.String()] {
				return nil, fmt.Errorf("httpserver: surface %d serves %s, which the route registry does not declare", index, route.String())
			}
			if claimed[route.String()] {
				return nil, fmt.Errorf("httpserver: %s is claimed by two surfaces", route.String())
			}
			claimed[route.String()] = true
		}
	}

	remaining := make([]Route, 0, len(declared))
	for _, route := range RegisteredRoutes() {
		if !claimed[route.String()] {
			remaining = append(remaining, route)
		}
	}
	if err := RegisterAll(mux, remaining, readyCheckers...); err != nil {
		return nil, err
	}

	for index, surface := range surfaces {
		if err := mountSafely(surface.Register, mux); err != nil {
			return nil, fmt.Errorf("httpserver: surface %d: %w", index, err)
		}
	}

	handler := httplimits.Middleware(httplimits.Default, mux)
	if locales != nil {
		handler = locale.SetHandler(locales, handler)
	}
	handler = requestid.Middleware(ids, handler)

	return securityheaders.Middleware(security)(handler), nil
}
