// Command testenv is the disposable environment of the test platform (P22-T01).
//
// One command provisions PostgreSQL 18, the delivered application, its worker
// and the fake provider surface on an isolated Docker network, waits until each
// one is genuinely ready, records what it created, and takes it all down again
// — including when somebody interrupts it. The namespaces are what let two runs
// exist at the same time without meeting.
//
// The command has three shapes and they answer three different needs:
//
//	testenv up / down / status    the environment as a thing a person manages,
//	                              which is how a failure is looked at instead
//	                              of guessed at;
//	testenv run -- <command>      the environment as a fixture: nothing outlives
//	                              the command, whatever the command does;
//	testenv providers / forward / probe
//	                              the environment as a service, inside its own
//	                              network, which is what makes "no service
//	                              reaches the internet" a property of the
//	                              network rather than a promise in a document.
//
// It is fail-closed everywhere it can be: no daemon, no frontend build, a
// namespace that is not one, a port already taken, a network that is not
// internal, a check that never passes, an environment file that would carry a
// credential — each one is a refusal with a diagnosis, and a refusal never
// leaves a half-built environment behind.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// Exit statuses, as a vocabulary the tests can name.
const (
	exitOK        = 0
	exitViolation = 1
)

const usage = `testenv — ambiente descartável da plataforma de testes (P22-T01)

  testenv up      [flags]                     cria o ambiente e o deixa de pé
  testenv down    [flags]                     derruba o ambiente e não deixa nada
  testenv status  [flags]                     diz o que o namespace tem agora
  testenv run     [flags] -- <cmd> [args...]  sobe, roda o comando e derruba
  testenv isolated [flags]                    prova, de dentro da rede, que ela não alcança a internet
  testenv providers [-addr 0.0.0.0:9090]      serve os providers fake (dentro da rede)
  testenv forward  -forward P:host:porta      encaminha P para um serviço da rede (a porta do ambiente)
  testenv probe    [-address 1.1.1.1:443]     o dial que precisa falhar, dentro do contêiner

Flags comuns: -namespace, -state, -port, -app-port, -provider-port, -assets, -timeout
`

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

// runCLI is the whole command, with its status as a value: the contract of a
// command is about exit statuses, and a contract that only lives inside main is
// one no test can hold.
func runCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitViolation
	}

	// The interrupt path: a run that is interrupted has to take the environment
	// down before it goes, and it is the context — not a deferred call somebody
	// may forget — that carries the cancellation to both the child process and
	// the teardown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	interrupted := false
	go func() {
		<-ctx.Done()
		interrupted = true
	}()

	status := dispatch(ctx, args, stdout, stderr)
	if interrupted {
		fmt.Fprintln(stderr, "testenv: interrupted; the namespace was taken down")
		return 130
	}
	return status
}

func dispatch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	switch args[0] {
	case "providers":
		return runProviders(args[1:], stdout, stderr)
	case "forward":
		return runForward(args[1:], stdout, stderr)
	case "probe":
		return runProbe(args[1:], stdout, stderr)
	case "up", "down", "status", "run", "isolated":
		return runEnvironment(ctx, args[0], args[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "testenv: unknown subcommand %q\n\n%s", args[0], usage)
		return exitViolation
	}
}

// commonFlags are the flags every environment subcommand accepts: they name the
// root, where the record lives, and the addresses the environment publishes.
type commonFlags struct {
	root         *string
	state        *string
	namespace    *string
	postgresPort *int
	appPort      *int
	providerPort *int
	assets       *string
	timeout      *time.Duration
	keep         *bool
	address      *string
}

func newCommonFlags(name string, flags *flag.FlagSet, stderr io.Writer) *commonFlags {
	common := &commonFlags{
		root:         flags.String("root", ".", "repository root the binary is built from"),
		state:        flags.String("state", os.Getenv("ARENA_TESTENV_STATE_DIR"), "directory the environments are recorded in (default: the temporary directory)"),
		namespace:    flags.String("namespace", os.Getenv("ARENA_TESTENV_NAMESPACE"), "namespace of the environment (default: one generated per run)"),
		postgresPort: flags.Int("port", 0, "port the database is published on (default: one the kernel hands out)"),
		appPort:      flags.Int("app-port", 0, "port the application is published on (default: one the kernel hands out)"),
		providerPort: flags.Int("provider-port", 0, "port the fake provider surface is published on (default: one the kernel hands out)"),
		assets:       flags.String("assets", os.Getenv("ARENA_TESTENV_ASSETS_DIR"), "frontend build the application serves (default: web/dist)"),
		timeout:      flags.Duration("timeout", 90*time.Second, "how long a service may take to become ready"),
		keep:         flags.Bool("keep", false, "leave the environment up when the command of `run` finishes"),
		address:      flags.String("address", "1.1.1.1:443", "address `isolated` proves unreachable from inside the network"),
	}
	flags.SetOutput(stderr)
	return common
}

func (common *commonFlags) options() (Options, error) {
	root, err := filepath.Abs(*common.root)
	if err != nil {
		return Options{}, fmt.Errorf("the repository root cannot be resolved: %w", err)
	}
	assets := *common.assets
	if assets == "" {
		assets = filepath.Join(root, "web", "dist")
	}
	return Options{
		Root:         root,
		StateDir:     *common.state,
		Namespace:    *common.namespace,
		PostgresPort: *common.postgresPort,
		AppPort:      *common.appPort,
		ProviderPort: *common.providerPort,
		AssetsDir:    assets,
		ReadyTimeout: *common.timeout,
	}, nil
}

// runEnvironment carries out one of the four subcommands that own a namespace.
func runEnvironment(ctx context.Context, subcommand string, args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("testenv "+subcommand, stderr)
	common := newCommonFlags(subcommand, flags, stderr)
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}

	options, err := common.options()
	if err != nil {
		fmt.Fprintf(stderr, "testenv: %v\n", err)
		return exitViolation
	}
	plan, err := NewPlan(options)
	if err != nil {
		fmt.Fprintf(stderr, "testenv: %v\n", err)
		return exitViolation
	}
	log := func(format string, args ...any) {
		fmt.Fprintf(stderr, format+"\n", args...)
	}
	environment := NewEnvironment(plan, newCLIDocker(stdout, stderr), log)

	switch subcommand {
	case "up":
		if err := environment.Up(ctx); err != nil {
			reportFailure(ctx, environment, err, stderr)
			return exitViolation
		}
		return exitOK
	case "down":
		if err := environment.Down(ctx); err != nil {
			fmt.Fprintf(stderr, "testenv: %v\n", err)
			return exitViolation
		}
		return exitOK
	case "status":
		if err := environment.Status(ctx, stdout); err != nil {
			fmt.Fprintf(stderr, "testenv: %v\n", err)
			return exitViolation
		}
		return exitOK
	case "isolated":
		if err := environment.Isolated(ctx, *common.address, stdout); err != nil {
			fmt.Fprintf(stderr, "testenv: %v\n", err)
			return exitViolation
		}
		return exitOK
	case "run":
		argv := flags.Args()
		if len(argv) > 0 && argv[0] == "--" {
			argv = argv[1:]
		}
		status, err := environment.Run(ctx, argv, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "testenv: %v\n", err)
			// A run that could not be carried out, or one whose teardown failed,
			// is a failure of the environment even when the command itself
			// succeeded: nothing about it may look green.
			if status == exitOK {
				return exitViolation
			}
		}
		// Otherwise the status of the run is the status of the command, which is
		// what makes `testenv run -- go test ./...` a thing a pipeline can call:
		// collapsing every red test to 1 would throw away the only number the
		// caller can act on.
		return status
	}
	fmt.Fprintf(stderr, "testenv: unknown subcommand %q\n", subcommand)
	return exitViolation
}

// reportFailure takes a failed `up` down before it reports: the environment
// that could not be built is the one nobody should have to clean up by hand,
// and a namespace left half built is the thing that makes the next run fail for
// a reason that has nothing to do with the next run.
func reportFailure(ctx context.Context, environment *Environment, failure error, stderr io.Writer) {
	fmt.Fprintf(stderr, "testenv: %v\n", failure)
	log := func(format string, args ...any) {
		fmt.Fprintf(stderr, format+"\n", args...)
	}
	cleanup := NewEnvironment(environment.Plan, environment.Client, log)
	if err := cleanup.Down(context.WithoutCancel(ctx)); err != nil {
		fmt.Fprintf(stderr, "testenv: the failed environment could not be taken down: %v\n  run `testenv down -namespace %s`\n", err, environment.Plan.Namespace)
	}
}

// newFlagSet is the flag set every subcommand uses: it writes its own errors to
// the caller's stream instead of the process's, so a test can read them.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}
