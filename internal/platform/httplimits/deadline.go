package httplimits

import (
	"context"
	"errors"
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
)

// gateState is the single lifetime of one response.
type gateState uint8

const (
	// gateOpen means nothing has been sent yet.
	gateOpen gateState = iota
	// gatePassedThrough means the handler answered inside its deadline and
	// owns the response from here on.
	gatePassedThrough
	// gateRefused means the deadline was already spent when the handler tried
	// to write, so the refusal is the response and anything the handler writes
	// afterwards is discarded.
	gateRefused
)

// errDeadlineExceeded is returned to a handler that writes after its route
// deadline. It is deliberately not an http error: the response has already
// been decided by the gate.
var errDeadlineExceeded = errors.New("httplimits: the route deadline was exceeded")

// deadlineGate closes the one gap a context deadline cannot close by itself.
//
// A deadline in the request context stops the work that honours a context, but
// a handler that ignores it — or that is simply slower than its budget — still
// returns and writes a success the middleware would then pass through, which
// would tell the client the work finished inside a budget it did not. The gate
// turns that late write into the safe problem instead, and swallows whatever
// the handler writes afterwards so a late success can never be delivered.
//
// What the gate deliberately does not do: it cannot rescue a response that was
// already sent before the deadline (rewriting a half-written body is worse
// than letting the transport fail it), and it does not interrupt a handler
// that never writes and never returns — that case is bounded by the transport
// timeouts (httpserver ReadTimeout and WriteTimeout), which is where the
// process-level bound belongs.
type deadlineGate struct {
	http.ResponseWriter
	request  *http.Request
	deadline context.Context
	state    gateState
}

// WriteHeader implements http.ResponseWriter.
func (gate *deadlineGate) WriteHeader(status int) {
	if gate.state != gateOpen {
		return
	}
	if gate.expired() {
		gate.refuse()
		return
	}
	gate.state = gatePassedThrough
	gate.ResponseWriter.WriteHeader(status)
}

// Write implements http.ResponseWriter.
func (gate *deadlineGate) Write(body []byte) (int, error) {
	if gate.state == gateRefused {
		return 0, errDeadlineExceeded
	}
	if gate.state == gateOpen {
		if gate.expired() {
			gate.refuse()
			return 0, errDeadlineExceeded
		}
		gate.state = gatePassedThrough
	}
	return gate.ResponseWriter.Write(body)
}

// Unwrap exposes the underlying writer to http.ResponseController, so a
// handler that really needs to flush or hijack can still reach the transport.
// It is the documented way to keep optional interfaces reachable through a
// wrapper; nothing in the arena handlers uses it today.
func (gate *deadlineGate) Unwrap() http.ResponseWriter {
	return gate.ResponseWriter
}

// Refused reports whether the gate replaced the handler's response with the
// deadline problem. It exists for tests and for the middleware's own
// diagnostics.
func (gate *deadlineGate) Refused() bool {
	return gate.state == gateRefused
}

// expired reports whether the route deadline has already passed.
func (gate *deadlineGate) expired() bool {
	return gate.deadline.Err() != nil
}

// refuse writes the safe problem for an overrun.
func (gate *deadlineGate) refuse() {
	gate.state = gateRefused
	// A handler that overran its own budget is a server-side failure: the
	// client sent something the route could not finish, and the honest answer
	// is the generic internal problem with a code that names the deadline, not
	// a validation error blaming the request.
	_ = httperror.WriteProblem(gate.ResponseWriter, gate.request,
		apperr.New(apperr.KindInternal, CodeRequestTimeout, "the request exceeded its processing deadline"))
}
