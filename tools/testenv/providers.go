package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// The fake provider surface of the environment (P22-T01).
//
// The environment runs one service that stands in for the providers the product
// talks to over HTTP. It exists from the first task of the phase because the
// alternative — an environment whose services are pointed at the real
// endpoints — is not a hermetic environment: it depends on the internet, on
// credentials, and on somebody else's state at the moment the test runs.
//
// What this task delivers is the surface and the discipline, not the protocol
// scenarios: the routes answer a recorded call with a refusal, and every call is
// kept so that a test can assert what was asked. `P22-T05` replaces the refusal
// with the protocol behaviours (success, timeout, 4xx/5xx, invalid response,
// duplication, reordering, unavailability) and its own fixtures. Keeping the
// recording and the refusal here means the environment is honest today: a
// service that silently answered 200 would let a test pass over a call nobody
// implemented.
//
// No credential is read, written or required by anything in this file.
type providerServer struct {
	mu    sync.Mutex
	calls []recordedCall
}

// recordedCall is one request the environment received, reduced to what a test
// asserts on. It carries no body: a body could hold a secret, and this surface
// is read by whoever runs the environment.
type recordedCall struct {
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Query  string    `json:"query,omitempty"`
	At     time.Time `json:"at"`
}

// ServeHTTP answers the three surfaces of the fake provider service:
//
//	GET  /health/live    the liveness of the service itself;
//	GET  /__testenv/calls what has been asked since the environment started;
//	*    anything else    a recorded call and a refusal that says so.
func (s *providerServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/health/live":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"live"}`)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/__testenv/calls":
		s.mu.Lock()
		calls := append([]recordedCall(nil), s.calls...)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if calls == nil {
			calls = []recordedCall{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"calls": calls})
		return
	}

	s.mu.Lock()
	s.calls = append(s.calls, recordedCall{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		At:     time.Now().UTC(),
	})
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	fmt.Fprintf(w, `{"error":"testenv_provider_not_implemented","detail":%q,"hint":"P22-T05 gives this provider its protocol scenarios; the call was recorded"}`, r.Method+" "+r.URL.Path)
}

// runProviders is the subcommand the environment runs inside the isolated
// network: `arena-testenv providers -addr 0.0.0.0:9090`. It serves until the
// container is stopped, and it never reads the process environment.
func runProviders(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("testenv providers", flag.ContinueOnError)
	flags.SetOutput(stderr)
	addr := flags.String("addr", "0.0.0.0:9090", "address the fake provider surface listens on")
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}
	if *addr == "" {
		fmt.Fprintln(stderr, "testenv: the fake provider surface needs an address")
		return exitViolation
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           &providerServer{},
		ReadHeaderTimeout: 5 * time.Second,
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(stderr, "testenv: the fake provider surface cannot listen on %s: %v\n", *addr, err)
		return exitViolation
	}
	fmt.Fprintf(stdout, "testenv: fake providers listening on %s\n", listener.Addr().String())
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(stderr, "testenv: the fake provider surface stopped: %v\n", err)
		return exitViolation
	}
	return exitOK
}

// providerNames are the providers the environment stands in for, in the fixed
// order the environment file publishes them. The names are the product's own
// vocabulary — the ones the adapters and the configuration use — so that
// pointing an adapter at the environment is naming one of these.
var providerNames = []string{"stripe", "resend", "turnstile", "observability"}

// providerVariables renders the environment file entries that point the product
// at the fake surface. Each provider gets the same address — one service, four
// names — because what a test needs is that nothing reaches the internet, and
// one address is one thing to keep honest.
//
// The names are the environment's own (`TESTENV_PROVIDER_*`) and not the
// product's: the loader refuses an unknown `ARENA_*` variable, so a variable the
// product does not yet accept cannot be published as if it did. Which
// configuration name points an adapter at this surface is a decision of P22-T05,
// and until then the environment states the address instead of guessing the name.
func providerVariables(baseURL string) []string {
	variables := make([]string, 0, len(providerNames))
	for _, name := range providerNames {
		variables = append(variables, TestEnvProviderVar+strings.ToUpper(name)+"="+baseURL)
	}
	sort.Strings(variables)
	return variables
}
