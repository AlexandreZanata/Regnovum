package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The lifecycle of one environment (P22-T01): create it, wait until it is
// genuinely ready, record it, and take it down again — on every path, including
// the one where somebody interrupts the command.
//
// The order matters and it is not arbitrary. The network comes first because it
// is what makes the environment hermetic; the database before the migration,
// the migration before the application, the application before the worker;
// and the record is written last, so that a state file means "this environment
// is complete" rather than "somebody started creating it".
type Environment struct {
	Plan   Plan
	Client dockerClient
	HTML   *http.Client
	Log    func(format string, args ...any)
	Build  func(ctx context.Context, plan Plan) error
}

// NewEnvironment composes the lifecycle around a plan.
func NewEnvironment(plan Plan, client dockerClient, log func(string, ...any)) *Environment {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Environment{
		Plan:   plan,
		Client: client,
		HTML:   &http.Client{Timeout: 3 * time.Second},
		Log:    log,
		Build:  buildBinaries,
	}
}

// Up creates the environment and leaves it running.
func (env *Environment) Up(ctx context.Context) error {
	// A namespace is a name for one environment. Building the same name twice
	// would leave the second run's containers on the same network and the same
	// published ports as the first one's, which is exactly the collision
	// namespaces exist to prevent — so it is refused, before anything is
	// created and before the daemon is even asked, with what to do about it.
	if _, recorded := env.Plan.Recorded(); recorded {
		return fmt.Errorf("the namespace `%s` is already recorded in %s: take it down with `testenv down -namespace %s`, or use another namespace", env.Plan.Namespace, env.Plan.StateDir, env.Plan.Namespace)
	}

	version, err := env.Client.Version(ctx)
	if err != nil {
		return fmt.Errorf("the Docker daemon is required and not reachable: %w\n  the environment runs its services in an isolated network, which is what makes 'no service reaches the internet' a property instead of a promise", err)
	}
	env.Log("testenv: docker %s, namespace %s", version, env.Plan.Namespace)

	if env.Plan.AssetsDir == "" {
		return fmt.Errorf("no frontend build was named: the application composes its pages from the build at ARENA_ASSETS_DIR, so a backend environment without one cannot boot — run `make build-web` or point ARENA_TESTENV_ASSETS_DIR at an existing build")
	}
	if manifest := filepath.Join(env.Plan.AssetsDir, "manifest.json"); !isFile(manifest) {
		return fmt.Errorf("%s is missing: the application reads the manifest the frontend build publishes — run `make build-web` or point ARENA_TESTENV_ASSETS_DIR at an existing build", manifest)
	}

	env.Log("testenv: building the delivered binary and the environment's own")
	if err := env.Build(ctx, env.Plan); err != nil {
		return err
	}

	env.Log("testenv: creating the isolated network %s and the door network %s", env.Plan.Network, env.Plan.DoorNetwork)
	if err := env.Client.NetworksCreate(ctx, env.Plan); err != nil {
		return fmt.Errorf("the networks of the environment cannot be created: %w", err)
	}
	internal, err := env.Client.NetworkIsInternal(ctx, env.Plan.Network)
	if err != nil {
		return fmt.Errorf("the network %s cannot be inspected: %w", env.Plan.Network, err)
	}
	if !internal {
		return fmt.Errorf("the network %s is not internal: a container on it would reach the internet, and an environment that can reach the internet is not one", env.Plan.Network)
	}

	env.Log("testenv: starting the database on the isolated network")
	if err := env.Client.Start(ctx, env.postgresSpec()); err != nil {
		return fmt.Errorf("the database cannot be started: %w", err)
	}
	if err := env.wait(ctx, "the database", healthIs(env.Client, env.Plan.Containers[RolePostgres])); err != nil {
		return err
	}

	// The door comes after the database because the database is the first
	// service the host needs to reach, and before the migration because the
	// migration is the first thing the environment runs after it.
	env.Log("testenv: starting the door on 127.0.0.1:%d, 127.0.0.1:%d and 127.0.0.1:%d", env.Plan.PostgresPort, env.Plan.AppPort, env.Plan.ProviderPort)
	if err := env.Client.Start(ctx, env.doorSpec()); err != nil {
		return fmt.Errorf("the door cannot be started: %w", err)
	}
	// Connected after it is running, which is the only way a container is on
	// two networks: the services reach the internet only through this one, and
	// this one is the only thing of the environment on an ordinary network.
	if err := env.Client.Connect(ctx, env.Plan.Containers[RoleDoor], env.Plan.Network); err != nil {
		return fmt.Errorf("the door cannot be attached to the isolated network: %w", err)
	}
	if err := env.wait(ctx, "the door", runningIs(env.Client, env.Plan.Containers[RoleDoor])); err != nil {
		return err
	}

	env.Log("testenv: applying the embedded migrations")
	if err := env.Client.RunOnce(ctx, env.migrateSpec()); err != nil {
		return fmt.Errorf("the migrations cannot be applied: %w", err)
	}

	env.Log("testenv: starting the fake provider surface on 127.0.0.1:%d", env.Plan.ProviderPort)
	if err := env.Client.Start(ctx, env.providersSpec()); err != nil {
		return fmt.Errorf("the fake provider surface cannot be started: %w", err)
	}
	// Reached through the door, from the host: the same address a test uses, so
	// a readiness that passes here is a readiness a test can rely on — and a
	// door that does not forward cannot pass this check.
	if err := env.wait(ctx, "the fake provider surface, reached through the door", reachable(env.HTML, env.Plan.ProvidersURL()+"/health/live")); err != nil {
		return err
	}

	env.Log("testenv: starting the application on %s", env.Plan.AppURL())
	if err := env.Client.Start(ctx, env.appSpec()); err != nil {
		return fmt.Errorf("the application cannot be started: %w", err)
	}
	if err := env.wait(ctx, "the application's readiness probe, reached through the door", reachable(env.HTML, env.Plan.AppURL()+"/health/ready")); err != nil {
		return err
	}

	env.Log("testenv: starting the worker")
	if err := env.Client.Start(ctx, env.workerSpec()); err != nil {
		return fmt.Errorf("the worker cannot be started: %w", err)
	}
	if err := env.wait(ctx, "the worker", runningIs(env.Client, env.Plan.Containers[RoleWorker])); err != nil {
		return err
	}
	if err := env.wait(ctx, "the worker's own report of itself", logsContain(env.Client, env.Plan.Containers[RoleWorker])); err != nil {
		return err
	}

	if err := env.Plan.WriteState(); err != nil {
		return err
	}
	env.Log("testenv: namespace %s is up; environment file at %s", env.Plan.Namespace, env.Plan.StateOf().Environment)
	return nil
}

// Down takes the environment down and leaves nothing behind. It is idempotent:
// running it twice is not an error, because the path that interrupts a run and
// then tears it down must not fail on the half it already removed.
func (env *Environment) Down(ctx context.Context) error {
	names, err := env.Client.List(ctx, env.Plan.Namespace)
	if err != nil {
		return fmt.Errorf("the containers of `%s` cannot be listed: %w", env.Plan.Namespace, err)
	}
	for _, name := range names {
		env.Log("testenv: removing container %s", name)
		if err := env.Client.Remove(ctx, name); err != nil {
			return fmt.Errorf("the container %s cannot be removed: %w", name, err)
		}
	}
	if err := env.Client.NetworksRemove(ctx, env.Plan); err != nil {
		env.Log("testenv: a network of `%s` is already gone (%v)", env.Plan.Namespace, err)
	}
	if err := os.RemoveAll(env.Plan.WorkDir); err != nil {
		return fmt.Errorf("the working directory of `%s` cannot be removed: %w", env.Plan.Namespace, err)
	}
	if err := os.Remove(env.Plan.StatePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("the state of `%s` cannot be removed: %w", env.Plan.Namespace, err)
	}
	env.Log("testenv: namespace %s is down", env.Plan.Namespace)
	return nil
}

// Status reports what the environment is, by asking the daemon and by reading
// what was recorded: a namespace whose state file is gone but whose containers
// are alive is exactly what an interrupted run leaves, and the report has to
// show both sides.
func (env *Environment) Status(ctx context.Context, stdout io.Writer) error {
	names, err := env.Client.List(ctx, env.Plan.Namespace)
	if err != nil {
		return fmt.Errorf("the containers of `%s` cannot be listed: %w", env.Plan.Namespace, err)
	}
	fmt.Fprintf(stdout, "namespace %s\n", env.Plan.Namespace)
	fmt.Fprintf(stdout, "  network  %s (isolated; the door is also on %s)\n", env.Plan.Network, env.Plan.DoorNetwork)
	// The addresses come from the record, never from a plan derived now: the
	// ports of this namespace were decided when it was created, and a plan
	// built by a second command has ports of its own.
	if state, recorded := env.Plan.Recorded(); recorded {
		fmt.Fprintf(stdout, "  database 127.0.0.1:%d\n", state.PostgresPort)
		fmt.Fprintf(stdout, "  app      %s\n", state.AppURL)
		fmt.Fprintf(stdout, "  providers %s\n", state.ProvidersURL)
		fmt.Fprintf(stdout, "  recorded %s\n", state.CreatedAt.Format(time.RFC3339))
	} else {
		fmt.Fprintf(stdout, "  no record: nothing is written in %s, so this namespace was never brought up completely — an interrupted run leaves exactly this\n", env.Plan.StateDir)
	}
	for _, name := range names {
		health, _ := env.Client.HealthStatus(ctx, name)
		running, _ := env.Client.IsRunning(ctx, name)
		fmt.Fprintf(stdout, "  container %-40s running=%t health=%s\n", name, running, health)
	}
	if len(names) == 0 {
		fmt.Fprintf(stdout, "  no container of this namespace is on the daemon\n")
	}
	return nil
}

// Run brings the environment up, drives one command with its environment, and
// takes everything down again whatever the command did — a red test, an
// interrupt, a panic. It is the shape the phase asks for: the teardown is not a
// second step somebody has to remember.
func (env *Environment) Run(ctx context.Context, argv []string, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return exitViolation, fmt.Errorf("run needs a command: `testenv run -- <command> [args...]`")
	}
	if err := env.Up(ctx); err != nil {
		// The environment may be half built; taking it down is what makes the
		// failed attempt invisible to the next one.
		_ = env.Down(context.WithoutCancel(ctx))
		return exitViolation, err
	}

	violations := []error{}
	status := exitOK
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = append(os.Environ(), env.Plan.EnvironmentVariables()...)
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			status = exit.ExitCode()
		} else {
			violations = append(violations, fmt.Errorf("the command could not be driven: %w", err))
			status = exitViolation
		}
	}
	env.Log("testenv: tearing the namespace down")
	if err := env.Down(context.WithoutCancel(ctx)); err != nil {
		violations = append(violations, err)
	}
	if len(violations) > 0 {
		return status, errors.Join(violations...)
	}
	return status, nil
}

// EnvironmentVariables exports the environment file to a process: the same
// values, as pairs, so that `run` and a sourced file cannot disagree.
func (plan Plan) EnvironmentVariables() []string {
	variables := []string{}
	for _, line := range strings.Split(plan.EnvironmentFile(), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		variables = append(variables, line)
	}
	sort.Strings(variables)
	return variables
}

// The container specifications. Each one mounts the delivered binary read-only
// from the working directory of the environment and runs it on the pinned
// runtime base: the environment runs the code this checkout produces, on a base
// whose digest is the production image's own, and it builds no image of its own
// — an image build per run would be the slowest and least reproducible part of
// starting a test environment.
func (env *Environment) postgresSpec() ContainerSpec {
	return ContainerSpec{
		Name:    env.Plan.Containers[RolePostgres],
		Image:   PostgresImage,
		Network: env.Plan.Network,
		Env: []string{
			"POSTGRES_USER=arena",
			"POSTGRES_PASSWORD=" + throwawayPassword,
			"POSTGRES_DB=" + databaseName,
			"POSTGRES_INITDB_ARGS=--encoding=UTF8 --locale=C",
			"POSTGRES_HOST_AUTH_METHOD=scram-sha-256",
		},
		// Nothing is published: the host reaches this database through the door.
		Tmpfs:       []string{"/var/lib/postgresql"},
		Labels:      labelsFor(env.Plan.Namespace),
		Healthcheck: []string{"pg_isready", "-U", "arena", "-d", databaseName},
	}
}

// migrateSpec applies every pending migration of the delivered binary. The
// subcommand is spelled out — `arena migrate up` — because the command refuses
// to guess: `migrate` alone answers "requires a subcommand" and leaves the
// environment without a schema, which the live verification found.
func (env *Environment) migrateSpec() ContainerSpec {
	return env.serviceSpec(RoleMigrate, []string{"migrate", "up"})
}

func (env *Environment) appSpec() ContainerSpec {
	spec := env.serviceSpec(RoleApp, []string{"server"})
	spec.Mounts = append(spec.Mounts, Mount{Host: env.Plan.AssetsDir, Container: "/web/dist", ReadOnly: true})
	return spec
}

// doorSpec is the forwarder. It is the only container of the environment on an
// ordinary network, it publishes the three addresses the host uses, and it
// reaches the services on the isolated one — which is the pairing that makes a
// published port and a hermetic service plane possible at the same time.
//
// It runs the environment's own binary, so the forwarder is code this
// repository tests rather than a shell image pulled from somewhere.
func (env *Environment) doorSpec() ContainerSpec {
	args := []string{"forward"}
	ports := []PortMap{}
	for _, door := range env.Plan.Doors() {
		port := fmt.Sprint(door.HostPort)
		args = append(args, "-forward", port+":"+door.Container+":"+door.Port)
		// The same number inside and outside, so that the mapping is read off
		// the door's own arguments instead of being recomputed.
		ports = append(ports, PortMap{Host: door.HostPort, Container: port})
	}
	return ContainerSpec{
		Name:       env.Plan.Containers[RoleDoor],
		Image:      RuntimeImage,
		Entrypoint: "/arena-testenv",
		Args:       args,
		Network:    env.Plan.DoorNetwork,
		Mounts: []Mount{
			{Host: filepath.Join(env.Plan.BinDir, "arena-testenv"), Container: "/arena-testenv", ReadOnly: true},
		},
		Ports:  ports,
		Labels: labelsFor(env.Plan.Namespace),
	}
}

func (env *Environment) workerSpec() ContainerSpec {
	return env.serviceSpec(RoleWorker, []string{"worker"})
}

// serviceSpec is the shape every service of the product shares: the delivered
// binary mounted read-only, the environment of a test process, the email sink,
// and the pinned runtime base.
func (env *Environment) serviceSpec(role string, args []string) ContainerSpec {
	return ContainerSpec{
		Name:       env.Plan.Containers[role],
		Image:      RuntimeImage,
		Entrypoint: "/arena",
		Args:       args,
		Network:    env.Plan.Network,
		Env:        env.Plan.ServiceEnvironment(role),
		Mounts: []Mount{
			{Host: filepath.Join(env.Plan.BinDir, "arena"), Container: "/arena", ReadOnly: true},
			{Host: env.Plan.SinkDir, Container: "/sink"},
		},
		Labels: labelsFor(env.Plan.Namespace),
	}
}

// providersSpec runs the environment's own binary as the fake provider surface:
// one command, one binary, and the surface is inside the isolated network where
// the product's adapters will find it.
func (env *Environment) providersSpec() ContainerSpec {
	return ContainerSpec{
		Name:       env.Plan.Containers[RoleProviders],
		Image:      RuntimeImage,
		Entrypoint: "/arena-testenv",
		Args:       []string{"providers", "-addr", "0.0.0.0:" + internalProvidersPort},
		Network:    env.Plan.Network,
		Mounts: []Mount{
			{Host: filepath.Join(env.Plan.BinDir, "arena-testenv"), Container: "/arena-testenv", ReadOnly: true},
		},
		Labels: labelsFor(env.Plan.Namespace),
	}
}

// Isolated proves the fourth validation from inside the environment: it runs the
// probe in the same network as the services, and the environment is hermetic
// exactly when that dial fails. A probe that answered nothing because the
// container could not start is not a proof, so a failure of the container itself
// is reported as a failure of the command.
func (env *Environment) Isolated(ctx context.Context, address string, stdout io.Writer) error {
	fmt.Fprintf(stdout, "testenv: probing %s from inside %s\n", address, env.Plan.Network)
	if err := env.Client.RunOnce(ctx, env.ProbeSpec(address, 3*time.Second)); err != nil {
		return fmt.Errorf("the environment is not isolated: %w", err)
	}
	fmt.Fprintf(stdout, "testenv: %s is unreachable from inside %s\n", address, env.Plan.Network)
	return nil
}

// ProbeSpec is the container that proves the network is closed: the same
// binary, the same network, and a dial that has to fail.
func (env *Environment) ProbeSpec(address string, timeout time.Duration) ContainerSpec {
	return ContainerSpec{
		Name:       env.Plan.Containers[RolePostgres] + "-probe",
		Image:      RuntimeImage,
		Entrypoint: "/arena-testenv",
		Args:       []string{"probe", "-address", address, "-timeout", timeout.String()},
		Network:    env.Plan.Network,
		Mounts: []Mount{
			{Host: filepath.Join(env.Plan.BinDir, "arena-testenv"), Container: "/arena-testenv", ReadOnly: true},
		},
		Labels: labelsFor(env.Plan.Namespace),
	}
}

// buildBinaries compiles the delivered binary and the environment's own for the
// container's platform. CGO is off because the runtime base has no libc, and
// the toolchain cache makes the second run of an environment free.
func buildBinaries(ctx context.Context, plan Plan) error {
	if err := os.MkdirAll(plan.BinDir, 0o755); err != nil {
		return fmt.Errorf("the binary directory is not creatable: %w", err)
	}
	builds := []struct {
		name   string
		target string
	}{
		{"arena", "./cmd/arena"},
		{"arena-testenv", "./tools/testenv"},
	}
	for _, build := range builds {
		output := filepath.Join(plan.BinDir, build.name)
		command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", output, build.target)
		command.Dir = plan.Root
		command.Env = append(os.Environ(),
			"CGO_ENABLED=0",
			"GOOS=linux",
			"GOARCH="+runtime.GOARCH,
		)
		if combined, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("the binary %s cannot be built for the container: %w\n%s", build.target, err, strings.TrimSpace(string(combined)))
		}
	}
	return nil
}

// The waits. Each one is a poll with a deadline and a diagnosis: a service that
// never becomes ready has to say which service, how long it waited and where to
// read why, because "the environment did not come up" is not a result anybody
// can act on.
type check func(ctx context.Context) (bool, string, error)

func healthIs(client dockerClient, container string) check {
	return func(ctx context.Context) (bool, string, error) {
		status, err := client.HealthStatus(ctx, container)
		if err != nil {
			return false, err.Error(), nil
		}
		return status == "healthy", "health is " + status, nil
	}
}

func runningIs(client dockerClient, container string) check {
	return func(ctx context.Context) (bool, string, error) {
		running, err := client.IsRunning(ctx, container)
		if err != nil {
			return false, err.Error(), nil
		}
		return running, "the container is not running", nil
	}
}

// logsContain waits for the worker to report itself. It is liveness rather than
// readiness, and the distinction is stated where it is used: the worker has no
// HTTP surface to probe, so what the environment can honestly wait for is the
// process saying that it is up.
func logsContain(client dockerClient, container string) check {
	return func(ctx context.Context) (bool, string, error) {
		logs, err := client.Logs(ctx, container, 200)
		if err != nil {
			return false, err.Error(), nil
		}
		return strings.Contains(logs, "worker"), "no report of itself in the logs yet", nil
	}
}

func reachable(client *http.Client, url string) check {
	return func(ctx context.Context) (bool, string, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err.Error(), nil
		}
		response, err := client.Do(request)
		if err != nil {
			return false, err.Error(), nil
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK, fmt.Sprintf("%s answered %d", url, response.StatusCode), nil
	}
}

// wait polls one check until it passes or the deadline expires.
func (env *Environment) wait(ctx context.Context, subject string, probe check) error {
	deadline := time.Now().Add(env.Plan.ReadyTimeout)
	last := ""
	for {
		ready, reason, err := probe(ctx)
		if err != nil {
			return fmt.Errorf("%s cannot be judged: %w", subject, err)
		}
		if ready {
			return nil
		}
		last = reason
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not become ready within %s: %s\n  look at it with: docker logs %s", subject, env.Plan.ReadyTimeout, last, env.Plan.Containers[RoleApp])
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s was still not ready when the environment was interrupted: %s", subject, last)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// probeCommand is the proof that the network is hermetic, run from inside it:
// the dial that must fail. It is a subcommand of the same binary so that the
// proof needs no image, no shell and no tool the base image does not carry.
func runProbe(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("testenv probe", stderr)
	address := flags.String("address", "1.1.1.1:443", "address the probe tries to reach")
	timeout := flags.Duration("timeout", 3*time.Second, "how long the probe waits for an answer")
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}
	connection, err := net.DialTimeout("tcp", *address, *timeout)
	if err == nil {
		connection.Close()
		fmt.Fprintf(stderr, "testenv: the probe reached %s from inside the network: the environment is not isolated\n", *address)
		return exitViolation
	}
	fmt.Fprintf(stdout, "testenv: %s is unreachable from inside the network (%v)\n", *address, err)
	return exitOK
}

// ProbeReport is the JSON the probe prints when asked for it, so that the live
// verification can assert on a value instead of on prose.
func ProbeReport(address string, err error) string {
	report := map[string]any{"address": address, "reachable": err == nil}
	if err != nil {
		report["error"] = err.Error()
	}
	encoded, _ := json.Marshal(report)
	return string(encoded)
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
