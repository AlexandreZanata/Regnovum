package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The door of the environment (P22-T01).
//
// It exists because of a measurement, not a preference: Docker does not create
// the host-side mapping of a published port on an internal network. A service of
// the environment therefore cannot publish the address the host needs while also
// having no route out — so the environment runs one container that is on both
// networks and forwards exactly three addresses from the ordinary one to the
// isolated one.
//
// What it forwards is fixed at start and nothing else: one listener per mapping,
// one dial target per listener, no name taken from the environment, no port
// opened on demand. A forwarder that could be told what to dial at run time
// would be a route out of the environment for anything that could talk to it.
//
// It is the environment's own binary rather than a proxy image pulled from
// somewhere, which is what makes the door a thing this repository tests.

// forwardMapping is one forwarded address: the port the host reaches and the
// service of the isolated network it leads to.
type forwardMapping struct {
	listenPort string
	target     string
}

// forwardFlag collects the repeated `-forward` flag. `flag` has no repeatable
// value of its own, and a door with one port would be a door for one service.
type forwardFlag []string

func (f *forwardFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *forwardFlag) Set(value string) error {
	if _, err := parseForward(value); err != nil {
		return err
	}
	*f = append(*f, value)
	return nil
}

// parseForward reads one `listenPort:host:port` mapping. The port the door
// listens on is the port the host publishes, so the mapping is one number
// written once, and the pair is checked here rather than discovered when a
// connection arrives.
func parseForward(value string) (forwardMapping, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return forwardMapping{}, fmt.Errorf("`%s` is not a `listenPort:host:port` mapping", value)
	}
	if _, err := strconv.ParseUint(parts[0], 10, 16); err != nil {
		return forwardMapping{}, fmt.Errorf("`%s` does not name a port to listen on", value)
	}
	if parts[1] == "" {
		return forwardMapping{}, fmt.Errorf("`%s` names no host to forward to", value)
	}
	if _, err := strconv.ParseUint(parts[2], 10, 16); err != nil {
		return forwardMapping{}, fmt.Errorf("`%s` does not name a port to forward to", value)
	}
	return forwardMapping{listenPort: parts[0], target: net.JoinHostPort(parts[1], parts[2])}, nil
}

// runForward is the subcommand the door container runs. It listens on every
// mapping it was given, forwards each connection to its fixed target, and
// returns when the container is stopped.
func runForward(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("testenv forward", stderr)
	var forwards forwardFlag
	flags.Var(&forwards, "forward", "one `listenPort:host:port` mapping; repeat the flag for each port")
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}
	if len(forwards) == 0 {
		fmt.Fprint(stderr, "testenv: the door needs at least one `-forward listenPort:host:port`\n")
		return exitViolation
	}

	mappings := make([]forwardMapping, 0, len(forwards))
	for _, raw := range forwards {
		mapping, err := parseForward(raw)
		if err != nil {
			fmt.Fprintf(stderr, "testenv: %v\n", err)
			return exitViolation
		}
		mappings = append(mappings, mapping)
	}
	sort.Slice(mappings, func(one, other int) bool { return mappings[one].listenPort < mappings[other].listenPort })

	var wait sync.WaitGroup
	listeners := make([]net.Listener, 0, len(mappings))
	fail := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "testenv: "+format+"\n", args...)
		for _, listener := range listeners {
			listener.Close()
		}
		wait.Wait()
		return exitViolation
	}
	for _, mapping := range mappings {
		listener, err := net.Listen("tcp", "0.0.0.0:"+mapping.listenPort)
		if err != nil {
			return fail("the door cannot listen on %s: %v", mapping.listenPort, err)
		}
		listeners = append(listeners, listener)
		wait.Add(1)
		go func() {
			defer wait.Done()
			forwardLoop(listener, mapping.target)
		}()
	}
	for _, mapping := range mappings {
		fmt.Fprintf(stdout, "testenv: forwarding 0.0.0.0:%s to %s\n", mapping.listenPort, mapping.target)
	}

	// The door is stopped by the environment, and a door that ignored the stop
	// would make the teardown wait for a timeout on every run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	for _, listener := range listeners {
		listener.Close()
	}
	wait.Wait()
	fmt.Fprintln(stdout, "testenv: the door is closed")
	return exitOK
}

// forwardLoop accepts connections until the listener is closed and gives each
// one its own pair of copies. A connection that fails is logged and dropped:
// the door forwards ports, it does not manage the services behind them.
func forwardLoop(listener net.Listener, target string) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go copyBothWays(connection, target)
	}
}

func copyBothWays(connection net.Conn, target string) {
	defer connection.Close()
	outbound, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return
	}
	defer outbound.Close()

	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, _ = io.Copy(outbound, connection)
		// Half close: what the host finished saying is finished, without
		// dropping the answer that is still coming back.
		if closer, ok := outbound.(*net.TCPConn); ok {
			_ = closer.CloseWrite()
		}
	}()
	go func() {
		defer wait.Done()
		_, _ = io.Copy(connection, outbound)
		if closer, ok := connection.(*net.TCPConn); ok {
			_ = closer.CloseWrite()
		}
	}()
	wait.Wait()
}
