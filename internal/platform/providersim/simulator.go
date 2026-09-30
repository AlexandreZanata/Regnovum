// Package providersim provides the deterministic protocol simulators of the
// test platform (P22-T05): fake Stripe, Resend, Turnstile, Sentry and PostHog
// servers that the product's own adapters talk to over HTTP.
//
// Why a simulator and not a stub per test: the adapters are where a provider's
// protocol is read — a signature header, a form field, a JSON document, an
// error shape — and every one of them already had a private fake inside its own
// test file. Four private fakes are four places where "what the provider
// really answers" can drift, and none of them can produce the states a suite
// needs on purpose: a provider that is slow, one that answers a status the
// adapter must classify, one that answers a document the parser must refuse,
// one that is not there at all. This package declares those states once.
//
// Four rules shape it, and each one is proved by a fixture rather than promised
// in this comment:
//
//   - **Scripted, not imagined.** A route answers the queue of answers it was
//     given, and the last answer of a queue repeats: a fixture scripts success
//     followed by a 503 to exercise a retry, or one answer to keep a call
//     constant. Every answer is explicit, including the successful one.
//   - **Unexpected calls fail the test.** A call to a route nobody declared is
//     recorded, answered with the protocol's own refusal document, and — with a
//     reporter attached, which is the default — reported through
//     testing.TB.Errorf. An adapter that starts calling something new is a
//     fixture that goes red, not a fixture that quietly stops covering it.
//   - **Deterministic.** No simulator reads the wall clock or the environment:
//     the instant of a recorded call comes from an injected clock
//     (testsource's, stopped at a committed epoch), identifiers are counters,
//     and no credential is read, generated or required — the keys a fixture
//     passes are synthetic literals of the fixture.
//   - **Bodies are captured on request.** A call is recorded with its headers
//     always and with its body only when the fixture asks (CapturingBodies),
//     because the two fixtures that need bodies are the redaction ones and
//     everything else should not hold a payload it has no question about.
//
// Nothing the product ships may import this package: it is listed in the
// test-only gate of internal/architecture_test.go exactly as the scenario
// builders are, and for the same reason — a fake that reached production would
// answer a call the product believes it made to a provider.
package providersim

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// epoch is the instant every recorded call is stamped with. A simulator that
// stamped the wall clock would make a fixture's record depend on the hour it
// ran, which is the failure the deterministic sources of P22-T02 exist to make
// impossible.
var epoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// UnexpectedStatus is the status a call nobody scripted is answered with. It is
// 501 and not 404 on purpose: the route exists in the provider's protocol, the
// fixture simply did not declare it, and a 404 would read as "the provider has
// no such endpoint".
const UnexpectedStatus = http.StatusNotImplemented

// Reporter is the part of testing.TB this package needs: the unexpected-call
// rule reports through it. It is an interface and not *testing.T so that a
// fixture can hold a recorder and prove the rule fires without failing itself.
type Reporter interface {
	Errorf(format string, args ...any)
}

// Answer is what the next call to a route receives: the status, the document
// and how long the provider takes to answer at all. It carries no headers of
// its own because no fixture asks about one — the headers this surface is here
// to hold are the ones the adapters *send*, which every recorded call keeps —
// and a knob nobody turns is a knob nobody knows is broken.
type Answer struct {
	Status int
	Body   string
	Delay  time.Duration
}

// Reply builds one answer. The status is explicit even when it is 200: a route
// table where success is implicit is a route table nobody reads.
func Reply(status int, body string) Answer {
	return Answer{Status: status, Body: body}
}

// Slow makes an answer arrive after a delay, which is how a fixture crosses the
// client's timeout without touching the client's configuration.
func Slow(answer Answer, delay time.Duration) Answer {
	answer.Delay = delay
	return answer
}

// Call is one request the simulator received.
type Call struct {
	Method  string
	Path    string
	Query   string
	Headers http.Header
	Body    []byte
	At      time.Time
}

// Header answers one request header, empty when it was not sent.
func (call Call) Header(name string) string {
	return call.Headers.Get(name)
}

// Decode reads the captured body into a value. It fails when the body was not
// captured, which is the mistake the message names instead of answering an
// empty document.
func (call Call) Decode(into any) error {
	if call.Body == nil {
		return fmt.Errorf("providersim: the body of %s %s was not captured (build the simulator with CapturingBodies)", call.Method, call.Path)
	}
	return json.Unmarshal(call.Body, into)
}

// route is one scripted address: the answers it still owes, in order.
type route struct {
	key     string
	prefix  bool
	answers []Answer
	served  int
}

// next answers the call at hand and advances the queue. The last answer
// repeats, so a route scripted with one answer is a constant and no fixture has
// to guess how many calls the adapter makes.
func (r *route) next() Answer {
	index := r.served
	if index >= len(r.answers) {
		index = len(r.answers) - 1
	}
	r.served++
	return r.answers[index]
}

// Simulator is one scripted provider: a route table, the calls it received and
// the answers it owes.
type Simulator struct {
	name    string
	clock   ports.Clock
	handler func(method, path string) (int, string)

	mu            sync.Mutex
	routes        []*route
	calls         []Call
	unexpected    []Call
	captureBodies bool
	strict        bool
	reporter      Reporter
	down          bool

	server *httptest.Server
}

// Option configures a simulator.
type Option func(*Simulator)

// WithClock injects the clock the recorded calls are stamped with.
func WithClock(clock ports.Clock) Option {
	return func(simulator *Simulator) {
		if clock != nil {
			simulator.clock = clock
		}
	}
}

// CapturingBodies records the body of every call. It is off by default: a body
// can hold a secret, and only the redaction fixtures have a question about one.
func CapturingBodies() Option {
	return func(simulator *Simulator) { simulator.captureBodies = true }
}

// WithReporter attaches the reporter an unexpected call is announced to. It is
// what a fixture that wants to observe the rule without failing uses.
func WithReporter(reporter Reporter) Option {
	return func(simulator *Simulator) { simulator.reporter = reporter }
}

// WithoutStrictness answers unexpected calls without reporting them. It exists
// for the fixture that observes the refusal itself; a fixture that scripts what
// its adapter calls has no reason to reach for it.
func WithoutStrictness() Option {
	return func(simulator *Simulator) { simulator.strict = false }
}

// New builds a simulator for one provider and starts the server a fixture
// points its adapter at. The server is closed when the test ends.
func New(t testing.TB, name string, options ...Option) *Simulator {
	t.Helper()
	simulator := newSimulator(name, append([]Option{WithReporter(t)}, options...)...)
	simulator.server = httptest.NewServer(simulator)
	t.Cleanup(simulator.Stop)
	return simulator
}

// NewStandalone builds a simulator without a server of its own: the caller
// mounts ServeHTTP wherever it serves, which is what the fake provider service
// of the hermetic environment does. A standalone simulator has no URL.
func NewStandalone(name string, options ...Option) *Simulator {
	return newSimulator(name, options...)
}

// newSimulator is the shared constructor. The strictness is on unless an option
// turns it off, so the default is the loud one.
func newSimulator(name string, options ...Option) *Simulator {
	simulator := &Simulator{
		name:    name,
		clock:   testsource.NewClock(epoch),
		strict:  true,
		handler: genericRefusal,
	}
	for _, option := range options {
		option(simulator)
	}
	return simulator
}

// genericRefusal is the refusal of a simulator whose protocol did not declare
// its own. Every protocol in this package declares one, and this is the answer
// of a simulator built by hand.
func genericRefusal(method, path string) (int, string) {
	document, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"type":    "unexpected_call",
			"message": method + " " + path + " is not scripted",
		},
	})
	return UnexpectedStatus, string(document)
}

// Route declares the answers of one address, written the way the provider's
// own route table writes it: "POST /v1/checkout/sessions". The path is exact
// unless it ends in "*", which matches by prefix — the shape a provider uses
// for an object read (`GET /v1/checkout/sessions/*`).
//
// Declaring an address a second time replaces the first: a fixture scripts a
// state by re-declaring what it cares about, and no fixture has to know which
// addresses the protocol constructor already answered.
func (s *Simulator) Route(address string, answers ...Answer) *Simulator {
	if len(answers) == 0 {
		panic("providersim: a route without an answer would answer nothing")
	}
	key := routeKey(address)
	declared := &route{
		key:     key,
		prefix:  strings.HasSuffix(key, "*"),
		answers: answers,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Replacement, not accumulation: the protocol constructor declares the
	// route table a provider really has, and a fixture that scripts a state
	// must be able to override one of those entries by naming it again. An
	// append would leave the constructor's success answer in front of the
	// fixture's failure answer, which is how a fixture silently stops testing
	// the state it scripted.
	for index, candidate := range s.routes {
		if candidate.key == declared.key && candidate.prefix == declared.prefix {
			s.routes[index] = declared
			return s
		}
	}
	s.routes = append(s.routes, declared)
	return s
}

// Name answers the provider this simulator stands in for.
func (s *Simulator) Name() string { return s.name }

// URL answers the address the fake provider listens on, empty for a standalone
// simulator (which is mounted by its caller instead of listening).
func (s *Simulator) URL() string {
	if s.server == nil {
		return ""
	}
	return s.server.URL
}

// Stop closes the server, which is how a fixture makes the provider
// unavailable in the only way a provider really is: nothing answers.
func (s *Simulator) Stop() {
	if s.server != nil {
		s.server.Close()
		s.server = nil
	}
}

// Down makes the simulator answer every call with 503 while its listener is
// still there. It is the unavailability a fixture needs when the address has to
// stay valid — a downstream that is up but not serving.
func (s *Simulator) Down(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// Calls answers every call received, in order.
func (s *Simulator) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// CallsTo answers the calls received by one address, in order.
func (s *Simulator) CallsTo(method, path string) []Call {
	key := strings.ToUpper(method) + " " + path
	matched := make([]Call, 0, 1)
	for _, call := range s.Calls() {
		if strings.ToUpper(call.Method)+" "+call.Path == key {
			matched = append(matched, call)
		}
	}
	return matched
}

// Unexpected answers the calls nobody declared.
func (s *Simulator) Unexpected() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.unexpected...)
}

// ServeHTTP is the provider. It matches the request against the declared
// routes, records it, answers it, and refuses whatever nobody declared.
func (s *Simulator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := s.readBody(r)

	s.mu.Lock()
	call := Call{
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: r.Header.Clone(),
		Body:    body,
		At:      s.clock.Now(),
	}
	s.calls = append(s.calls, call)

	if s.down {
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":{"type":"provider_unavailable","message":"the %s simulator is down"}}`, s.name)
		return
	}

	matched := s.matchLocked(call)
	if matched == nil {
		status, document := s.handler(call.Method, call.Path)
		s.unexpected = append(s.unexpected, call)
		reporter, strict := s.reporter, s.strict
		s.mu.Unlock()

		if strict && reporter != nil {
			reporter.Errorf("providersim: %s received an unexpected call: %s %s", s.name, call.Method, call.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, document)
		return
	}
	answer := matched.next()
	s.mu.Unlock()

	if answer.Delay > 0 {
		// Waiting is not reading: the delay is what a fixture uses to cross the
		// client's timeout, and it decides nothing on its own.
		time.Sleep(answer.Delay)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(answer.Status)
	fmt.Fprint(w, answer.Body)
}

// readBody reads the request body when the fixture asked for it. A body that
// cannot be read is not a scenario: the caller sees no body and the adapter
// sees an empty one, which is what a truncated upload is.
func (s *Simulator) readBody(r *http.Request) []byte {
	if !s.captureBodies || r.Body == nil {
		return nil
	}
	captured, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	return captured
}

// routeKey renders an address the way the matching compares it: the method
// upper-cased and the path exactly as the provider spells it, because a
// provider's path is case sensitive and its method is not.
func routeKey(address string) string {
	trimmed := strings.TrimSpace(address)
	method, path, found := strings.Cut(trimmed, " ")
	if !found {
		return strings.ToUpper(trimmed)
	}
	return strings.ToUpper(method) + " " + path
}

// matchLocked answers the route of a call: the exact address first, then the
// prefix ones in declaration order.
func (s *Simulator) matchLocked(call Call) *route {
	key := routeKey(call.Method + " " + call.Path)
	for _, candidate := range s.routes {
		if !candidate.prefix && candidate.key == key {
			return candidate
		}
	}
	for _, candidate := range s.routes {
		if candidate.prefix && strings.HasPrefix(key, strings.TrimSuffix(candidate.key, "*")) {
			return candidate
		}
	}
	return nil
}
