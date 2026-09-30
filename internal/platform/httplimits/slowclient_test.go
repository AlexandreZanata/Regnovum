// Slow client on a real socket (P16-T02): the two ways a client can starve a
// connection without ever sending a request — dribbling the headers and
// stalling in the middle of the body — are cut off by the transport, and the
// handler is never reached.
//
// This test goes to the socket on purpose. The body budget is proven against
// an in-process reader in httplimits_test.go; what changes with a real client
// is that a half-sent request is indistinguishable from a live one until the
// server puts a clock on it, and no in-process fixture can demonstrate that.
// The transport timeouts here are tightened to keep the test fast; the shipped
// values are the defaults asserted in internal/platform/httpserver.
package httplimits_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/httplimits"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func TestSlowClientIsCutOffWithoutReachingTheHandler(t *testing.T) {
	t.Parallel()

	handler := &recorderHandler{}

	server, err := httpserver.New(httpserver.Options{
		Addr:              "127.0.0.1:0",
		Handler:           httplimits.Middleware(httplimits.Default, handler),
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		ReadHeaderTimeout: 250 * time.Millisecond,
		ReadTimeout:       400 * time.Millisecond,
		WriteTimeout:      2 * time.Second,
		IdleTimeout:       2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := server.Listen(); err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-serveResult; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})

	t.Run("dribbling the headers", func(t *testing.T) {
		connection := dial(t, server.Addr())
		defer connection.Close()

		// Three fragments, each one inside the header timeout, together well
		// past it: this is the shape of a slowloris connection, and the
		// deadline must not be renewable by dribbling.
		for _, fragment := range []string{
			"POST /api/v1/me/arena-drafts HTTP/1.1\r\n",
			"Host: arena.test\r\n",
			"X-Slow: y",
		} {
			if _, err := connection.Write([]byte(fragment)); err != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		assertCutOff(t, connection, "headers")
	})

	t.Run("stalling inside the body", func(t *testing.T) {
		connection := dial(t, server.Addr())
		defer connection.Close()

		// A complete, well-formed head that declares a body inside the budget
		// and then stops: the promise is not the fact, and the deadline has to
		// apply to the bytes, not to the declaration.
		head := "POST /api/v1/me/arena-drafts HTTP/1.1\r\n" +
			"Host: arena.test\r\n" +
			"Content-Type: application/json\r\n" +
			"Content-Length: 4096\r\n\r\n"
		if _, err := connection.Write([]byte(head + `{"statement":`)); err != nil {
			t.Fatalf("write error = %v", err)
		}

		assertCutOff(t, connection, "body")
	})

	if handler.called() != 0 {
		t.Errorf("handler was called %d times by a client that never finished a request, want 0", handler.called())
	}
}

// dial opens one connection to the test server.
func dial(t *testing.T, address string) net.Conn {
	t.Helper()

	connection, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", address, err)
	}
	return connection
}

// assertCutOff waits for the server to end the connection and reports the time
// it took. A connection still open after the wait, or answered with a success,
// means a slow client held a server slot and reached the handler.
func assertCutOff(t *testing.T, connection net.Conn, phase string) {
	t.Helper()

	const wait = 3 * time.Second

	if err := connection.SetReadDeadline(time.Now().Add(wait)); err != nil {
		t.Fatalf("%s: SetReadDeadline error = %v", phase, err)
	}

	var response strings.Builder
	buffer := make([]byte, 4096)
	timedOut := false
	for {
		read, err := connection.Read(buffer)
		if read > 0 {
			response.Write(buffer[:read])
		}
		if err != nil {
			timedOut = errors.Is(err, os.ErrDeadlineExceeded)
			break
		}
	}
	if timedOut {
		t.Fatalf("%s: the connection was still open after %v: a slow client holds a server slot indefinitely", phase, wait)
	}
	if strings.Contains(response.String(), " 200 ") {
		t.Errorf("%s: a request that was never completed was answered with success: %q", phase, response.String())
	}
}
