package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Denying egress and *observing* it are two different things, and the offline
// mode needs both. Denied alone, an unexpected download is a failure nobody can
// explain: a command stops with "connection refused" and the reader does not
// know what it wanted. Observed, the same failure becomes a refusal that names
// the host and the path a process asked for — which is the difference between a
// run that is hermetic and a run that was lucky.
//
// The sensor is that observer: a loopback HTTP proxy that answers every request
// with a refusal and records it. It sits on 127.0.0.1 and resolves nothing: the
// client sends it the absolute address it wanted, so the sensor learns the name
// without the machine ever looking it up.

// Attempt is one request the sensor saw and refused.
type Attempt struct {
	Method string `json:"method"`
	Host   string `json:"host"`
	Path   string `json:"path"`
}

// String renders an attempt the way a refusal reads.
func (a Attempt) String() string {
	return fmt.Sprintf("%s %s%s", a.Method, a.Host, a.Path)
}

// Sensor is the egress deny-and-observe proxy of one run.
type Sensor struct {
	listener net.Listener
	server   *http.Server

	mutex    sync.Mutex
	attempts []Attempt
	closed   bool
}

// StartSensor puts the sensor on a loopback port of its own and answers its
// address. The kernel chooses the port: two runs on the same machine must not
// collide, and a fixed port would be a gate that fails when a laptop runs two
// gates at once.
func StartSensor() (*Sensor, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("cannot listen on loopback: %w", err)
	}
	sensor := &Sensor{listener: listener}
	sensor.server = &http.Server{
		Handler:           http.HandlerFunc(sensor.handle),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		// The handler never fails on its own: the refusal is the answer.
		_ = sensor.server.Serve(listener)
	}()
	return sensor, nil
}

// URL answers the sensor's address, which is what the environment points at.
func (s *Sensor) URL() string {
	return "http://" + s.listener.Addr().String()
}

// Attempts answers what the sensor saw, in the order it saw it, deduplicated:
// a retry loop that reaches the same place ten times is one thing a reader has
// to act on, counted.
func (s *Sensor) Attempts() []Attempt {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	seen := map[string]bool{}
	attempts := make([]Attempt, 0, len(s.attempts))
	for _, attempt := range s.attempts {
		key := attempt.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		attempts = append(attempts, attempt)
	}
	sort.SliceStable(attempts, func(left, right int) bool { return attempts[left].String() < attempts[right].String() })
	return attempts
}

// Count answers how many requests the sensor refused, retries included.
func (s *Sensor) Count() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return len(s.attempts)
}

// Close stops the sensor and waits for it to let go of the port, so the next
// run of the same gate can start its own.
func (s *Sensor) Close() error {
	s.mutex.Lock()
	s.closed = true
	s.mutex.Unlock()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.server.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return s.listener.Close()
}

// handle is the whole policy of the sensor: record the attempt, refuse it, and
// tell the caller what this address is. It never dials anything.
func (s *Sensor) handle(writer http.ResponseWriter, request *http.Request) {
	host := request.Host
	if request.Method == http.MethodConnect {
		// The CONNECT line of an HTTPS proxy names the address in the request
		// target, and that is the interesting one: it is the host the client
		// wanted and could not have.
		host = request.URL.Host
		if host == "" {
			host = request.Host
		}
	}
	attempt := Attempt{Method: request.Method, Host: host, Path: request.URL.RequestURI()}

	s.mutex.Lock()
	s.attempts = append(s.attempts, attempt)
	s.mutex.Unlock()

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Connection", "close")
	writer.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(writer, "%s: %s is not reachable from an offline run; install from the approved lockfiles first (make offline-preload)\n", RuleEgressAttempt, attempt.String())
}

// deniedVariables are the variables the offline mode owns. Every one of them is
// *replaced*, never appended: an ambient NO_PROXY=* would undo the whole mode,
// and a build that inherited it would look hermetic while reaching anywhere.
var deniedVariables = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "all_proxy", "no_proxy",
	"GOPROXY", "GOFLAGS",
	"npm_config_registry", "npm_config_proxy", "npm_config_https_proxy",
	"npm_config_offline", "npm_config_audit", "npm_config_fund",
}

// DeniedEnvironment answers the environment of an offline run: the one it
// inherited, with every variable the mode owns replaced by what the mode needs.
//
// The loopback addresses stay out of the proxy: the gates of this repository
// talk to a PostgreSQL, a fake provider and an application on 127.0.0.1, and a
// mode that sent those through the sensor would be measuring the sensor instead
// of the gate. What the sensor sees is what leaves the machine.
func DeniedEnvironment(base []string, sensorURL string) []string {
	owned := map[string]bool{}
	for _, name := range deniedVariables {
		owned[name] = true
	}
	environment := make([]string, 0, len(base)+len(deniedVariables))
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if !found || owned[name] {
			continue
		}
		environment = append(environment, entry)
	}
	for _, entry := range DeniedValues(sensorURL) {
		environment = append(environment, entry)
	}
	return environment
}

// DeniedValues answers the values the offline mode sets, as NAME=value pairs in
// a fixed order. It is separate from the environment so that a test can read
// the policy instead of the ambient machine.
func DeniedValues(sensorURL string) []string {
	values := map[string]string{
		"HTTP_PROXY":             sensorURL,
		"HTTPS_PROXY":            sensorURL,
		"ALL_PROXY":              sensorURL,
		"http_proxy":             sensorURL,
		"https_proxy":            sensorURL,
		"all_proxy":              sensorURL,
		"NO_PROXY":               "127.0.0.1,localhost,::1",
		"no_proxy":               "127.0.0.1,localhost,::1",
		"GOPROXY":                sensorURL,
		"GOFLAGS":                "-mod=readonly",
		"npm_config_registry":    sensorURL,
		"npm_config_proxy":       sensorURL,
		"npm_config_https_proxy": sensorURL,
		"npm_config_offline":     "true",
		"npm_config_audit":       "false",
		"npm_config_fund":        "false",
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]string, 0, len(names))
	for _, name := range names {
		entries = append(entries, name+"="+values[name])
	}
	return entries
}

// ValuesOf reads an environment into a map, which is how a reader looks up what
// a run was given.
func ValuesOf(environment []string) map[string]string {
	values := map[string]string{}
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		values[name] = value
	}
	return values
}

// probeTimeout bounds the attempt of the control: a client that hangs would
// make the gate wait for the network it is proving absent.
const probeTimeout = 10 * time.Second

// Probe is a client that tries to reach an address with the environment it was
// given. It exists as a subcommand so that the exercise has a control which is
// not the tool judging itself: a normal HTTP client, in the offline
// environment, asking for something outside the machine.
//
// Three answers are possible and two of them mean the mode works: the request
// fails (nothing answered), or the sensor answers the refusal it answers
// everything (403). Only a real answer means the run reached out. The probe
// does not decide that: the sensor's own record is what the run is judged by,
// and a client cannot tell a refusal from a broken network.
func Probe(target string, stdout, stderr io.Writer) error {
	client := &http.Client{
		Timeout: probeTimeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}
	response, err := client.Get(target)
	if err != nil {
		fmt.Fprintf(stderr, "probe: the request to %s failed: %v\n", target, err)
		return nil
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode == http.StatusForbidden {
		fmt.Fprintf(stdout, "probe: the sensor refused the request to %s\n", target)
		return nil
	}
	fmt.Fprintf(stdout, "probe: the request to %s answered %d, which means the environment is not denying egress\n", target, response.StatusCode)
	return refuse(RuleEgressAttempt, "the probe reached %s with status %d", target, response.StatusCode)
}
