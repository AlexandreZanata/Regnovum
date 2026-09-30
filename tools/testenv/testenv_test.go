// Tests of the disposable environment (P22-T01).
//
// The orchestration is written against an interface for a reason that is not
// abstraction for its own sake: the decisions it makes have to be provable
// without a daemon. Below, a recorded client stands in for Docker, so the parts
// that decide — which names, which pins, which addresses, what a service
// receives, in what order things come up, what is refused and what is torn down
// — are held by tests that run everywhere. `tools/testenv/verify.sh` proves the
// same plan against a real daemon.
//
// The four validations the phase states are all here, each as a measurement:
//
//   - two namespaces do not collide, because every name, path and port of a plan
//     is derived from the namespace and nothing else;
//   - an interrupted run takes down what it created, asserted on the recorded
//     calls rather than on the intention — and the fake refuses to work with a
//     cancelled context, so the test holds that the teardown runs outside the
//     cancellation;
//   - a port that is taken is refused with the port and the way to look at it;
//   - no service reaches the internet and no service receives a credential,
//     asserted on the environment each container is given, on the network it is
//     put on, and on the guard that refuses a credential-shaped variable.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeContainer is one container the recorded client believes exists.
type fakeContainer struct {
	running bool
	health  string
	logs    string
	spec    ContainerSpec
}

// fakeDocker records every call and answers as the plan expects. It is
// deliberately dumb about names: the tests assert on the calls, and a fake that
// guessed would hide the very thing under test.
//
// It refuses to work with a cancelled context, like a real client would. That is
// what makes the teardown's use of context.WithoutCancel a property a test can
// hold instead of a stylistic detail.
type fakeDocker struct {
	calls       []string
	containers  map[string]*fakeContainer
	version     string
	startHealth string
	failList    error
	failRunOnce error
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		containers: map[string]*fakeContainer{},
		version:    "29.1.3",
		// A container that comes up is healthy unless a test says otherwise;
		// the environment waits for health, so the default has to be the good
		// case or every test would be a test of the deadline.
		startHealth: "healthy",
	}
}

func (f *fakeDocker) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

// called reports whether any recorded call contains the substring, which is how
// the tests assert on what the lifecycle asked the daemon to do.
func (f *fakeDocker) called(substring string) bool {
	for _, call := range f.calls {
		if strings.Contains(call, substring) {
			return true
		}
	}
	return false
}

func (f *fakeDocker) Version(context.Context) (string, error) {
	if f.version == "" {
		return "", fmt.Errorf("cannot connect to the Docker daemon")
	}
	f.record("version")
	return f.version, nil
}

func (f *fakeDocker) NetworksCreate(_ context.Context, plan Plan) error {
	f.record("network create internal=true %s", plan.Network)
	f.record("network create internal=false %s", plan.DoorNetwork)
	return nil
}

func (f *fakeDocker) NetworksRemove(_ context.Context, plan Plan) error {
	f.record("network rm %s", plan.Network)
	f.record("network rm %s", plan.DoorNetwork)
	return nil
}

func (f *fakeDocker) NetworkIsInternal(_ context.Context, network string) (bool, error) {
	f.record("network inspect %s", network)
	return true, nil
}

func (f *fakeDocker) Connect(_ context.Context, container, network string) error {
	f.record("network connect %s %s", network, container)
	return nil
}

func (f *fakeDocker) Start(_ context.Context, spec ContainerSpec) error {
	f.record("run --detach --name %s %s %s", spec.Name, spec.Image, strings.Join(spec.Args, " "))
	f.containers[spec.Name] = &fakeContainer{
		running: true,
		health:  f.startHealth,
		logs:    "arena worker: the workload registry is ready\n",
		spec:    spec,
	}
	return nil
}

func (f *fakeDocker) RunOnce(_ context.Context, spec ContainerSpec) error {
	f.record("run --rm --name %s %s %s", spec.Name, spec.Image, strings.Join(spec.Args, " "))
	return f.failRunOnce
}

func (f *fakeDocker) HealthStatus(_ context.Context, container string) (string, error) {
	if c, ok := f.containers[container]; ok {
		return c.health, nil
	}
	return "", fmt.Errorf("no such container: %s", container)
}

func (f *fakeDocker) IsRunning(_ context.Context, container string) (bool, error) {
	if c, ok := f.containers[container]; ok {
		return c.running, nil
	}
	return false, fmt.Errorf("no such container: %s", container)
}

func (f *fakeDocker) Logs(_ context.Context, container string, _ int) (string, error) {
	if c, ok := f.containers[container]; ok {
		return c.logs, nil
	}
	return "", fmt.Errorf("no such container: %s", container)
}

func (f *fakeDocker) Remove(ctx context.Context, container string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("the daemon was not reached with a cancelled context: %w", err)
	}
	f.record("rm --force %s", container)
	delete(f.containers, container)
	return nil
}

func (f *fakeDocker) List(ctx context.Context, namespace string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("the daemon was not reached with a cancelled context: %w", err)
	}
	if f.failList != nil {
		return nil, f.failList
	}
	names := []string{}
	for name := range f.containers {
		if strings.HasPrefix(name, "arena-testenv-"+namespace+"-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// fixtureOptions is one namespace in a temporary state directory, with nothing
// built and nothing started by the machine.
func fixtureOptions(t *testing.T, namespace string) Options {
	t.Helper()
	root := t.TempDir()
	assets := filepath.Join(root, "web", "dist")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatalf("the fixture assets cannot be created: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assets, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("the fixture manifest cannot be written: %v", err)
	}
	return Options{
		Root:         root,
		StateDir:     t.TempDir(),
		Namespace:    namespace,
		AssetsDir:    assets,
		ReadyTimeout: 2 * time.Second,
	}
}

// fixtureEnvironment composes the lifecycle of one namespace with the recorded
// client, and with the binary build replaced by nothing: the build is the Go
// toolchain's job, and a test that compiled the whole product would be a slow
// test of something else.
func fixtureEnvironment(t *testing.T, options Options, fake *fakeDocker) *Environment {
	t.Helper()
	plan, err := NewPlan(options)
	if err != nil {
		t.Fatalf("the fixture plan cannot be built: %v", err)
	}
	environment := NewEnvironment(plan, fake, func(string, ...any) {})
	environment.Build = func(context.Context, Plan) error {
		return os.MkdirAll(plan.BinDir, 0o755)
	}
	return environment
}

// serveOn answers on the port the plan published. The lifecycle probes the
// provider surface and the application over HTTP, and those probes are the real
// ones: a test that stubbed them would not hold the way a service is judged
// ready. The port comes from the plan, because the plan is what refuses a port
// that is taken.
func serveOn(t *testing.T, port int, handler http.Handler) {
	t.Helper()
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("the fixture service cannot take the published port %d: %v", port, err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

// freePort asks the kernel for an address nothing is serving, which is how the
// tests that need a listener on a known address get one.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("the test cannot ask for a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// readyApp answers the application's readiness probe.
func readyApp() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/ready" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

// TestThePlanPinsEverythingItCan states the reproducibility the phase asks for:
// the images the environment runs are pinned, and the namespace — not something
// ambient — decides every name and address.
func TestThePlanPinsEverythingItCan(t *testing.T) {
	plan, err := NewPlan(fixtureOptions(t, "plan-check"))
	if err != nil {
		t.Fatalf("the plan cannot be built: %v", err)
	}
	if !strings.HasPrefix(PostgresImage, "postgres:18") || !strings.HasSuffix(PostgresImage, ".4") {
		t.Fatalf("the database image %q is not pinned to a patch: the delivered Compose pins postgres:18.4, and an environment that ran another server would be verifying something else", PostgresImage)
	}
	if !strings.Contains(RuntimeImage, "@sha256:") {
		t.Fatalf("the runtime base %q is not pinned by digest: a tag can be repointed under a review, a digest cannot", RuntimeImage)
	}
	if want := "arena-testenv-plan-check"; plan.Network != want {
		t.Fatalf("the network is %q and the namespace says %q", plan.Network, want)
	}
	if want := "arena-testenv-plan-check-door"; plan.DoorNetwork != want {
		t.Fatalf("the door network is %q and the namespace says %q", plan.DoorNetwork, want)
	}
	if plan.DoorNetwork == plan.Network {
		t.Fatalf("the door network is the isolated one, which would put the door and the services on the same plane")
	}
	for _, role := range Roles {
		if want := "arena-testenv-plan-check-" + role; plan.Containers[role] != want {
			t.Errorf("the %s container is %q and the namespace says %q", role, plan.Containers[role], want)
		}
	}
	for _, address := range []string{plan.InternalDSN(), plan.HostDSN()} {
		if !strings.Contains(address, throwawayPassword) {
			t.Errorf("the DSN %q does not carry the throwaway credential of the environment", address)
		}
	}
	if plan.CursorSecret == "" || len(plan.CursorSecret) < 32 {
		t.Errorf("the cursor secret is %q: it is generated per run", plan.CursorSecret)
	}
}

// TestTheEnvironmentIsBuildableTwiceAndInParallel is the first validation of
// the phase: two namespaces on the same machine share no name, no path and no
// address, so two runs can exist at the same time without meeting.
func TestTheEnvironmentIsBuildableTwiceAndInParallel(t *testing.T) {
	first, err := NewPlan(fixtureOptions(t, "parallel-one"))
	if err != nil {
		t.Fatalf("the first plan cannot be built: %v", err)
	}
	second, err := NewPlan(fixtureOptions(t, "parallel-two"))
	if err != nil {
		t.Fatalf("the second plan cannot be built: %v", err)
	}
	if first.Network == second.Network {
		t.Errorf("two namespaces share the network %q", first.Network)
	}
	for _, role := range Roles {
		if first.Containers[role] == second.Containers[role] {
			t.Errorf("two namespaces share the %s container %q", role, first.Containers[role])
		}
	}
	if first.WorkDir == second.WorkDir || first.StatePath == second.StatePath {
		t.Errorf("two namespaces share the working directory or the state file: %q/%q", first.WorkDir, second.StatePath)
	}
	if first.CursorSecret == second.CursorSecret {
		t.Errorf("two namespaces share the cursor secret")
	}
	// The generated namespace is the case that matters in practice: nobody
	// passes one when they run two environments at once.
	generated := map[string]bool{}
	for index := 0; index < 16; index++ {
		plan, err := NewPlan(fixtureOptions(t, ""))
		if err != nil {
			t.Fatalf("a generated namespace cannot be built: %v", err)
		}
		if !NamespacePattern.MatchString(plan.Namespace) {
			t.Fatalf("the generated namespace %q does not match the convention", plan.Namespace)
		}
		if generated[plan.Namespace] {
			t.Fatalf("the generated namespace %q came out twice", plan.Namespace)
		}
		generated[plan.Namespace] = true
	}
}

// TestAPortThatIsTakenIsRefusedWithItsDiagnosis is the third validation: the
// refusal names the port and says how to look at it, and it happens before
// anything is created.
func TestAPortThatIsTakenIsRefusedWithItsDiagnosis(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("the test cannot occupy a port: %v", err)
	}
	defer listener.Close()
	taken := listener.Addr().(*net.TCPAddr).Port

	options := fixtureOptions(t, "port-clash")
	options.PostgresPort = taken
	_, err = NewPlan(options)
	if err == nil {
		t.Fatalf("the plan accepted the port %d, which is taken", taken)
	}
	message := err.Error()
	for _, expected := range []string{fmt.Sprint(taken), "already taken", "lsof", "docker ps"} {
		if !strings.Contains(message, expected) {
			t.Errorf("the diagnosis does not mention %q: %s", expected, message)
		}
	}

	invalid := fixtureOptions(t, "port-clash")
	invalid.AppPort = 70000
	if _, err := NewPlan(invalid); err == nil {
		t.Errorf("the plan accepted the port 70000")
	}
}

// TestNoServiceReceivesTheCallersEnvironment is half of the fourth validation:
// what a container receives is the plan's own list, never the shell that
// started the environment.
func TestNoServiceReceivesTheCallersEnvironment(t *testing.T) {
	t.Setenv("STRIPE_SECRET_KEY", "sk_live_this_is_not_a_real_key")
	t.Setenv("RESEND_API_KEY", "re_this_is_not_a_real_key")
	t.Setenv("ARENA_DATABASE_URL", "postgres://real:credential@db.example/production?sslmode=require")

	environment := fixtureEnvironment(t, fixtureOptions(t, "no-inherit"), newFakeDocker())
	specs := map[string]ContainerSpec{
		RoleApp:     environment.appSpec(),
		RoleWorker:  environment.workerSpec(),
		RoleMigrate: environment.migrateSpec(),
	}
	for role, spec := range specs {
		if err := assertNoCredentials(spec.Env, environment.Plan); err != nil {
			t.Errorf("the %s service receives a credential: %v", role, err)
		}
		for _, variable := range spec.Env {
			if strings.Contains(variable, "sk_live_") || strings.Contains(variable, "re_") || strings.Contains(variable, "@db.example") {
				t.Errorf("the %s service inherited %q from the caller", role, variable)
			}
		}
		// The DSN of the service points inside the network: a service that
		// reached the database over the host would be an environment whose
		// services depend on the machine that started them.
		if !contains(spec.Env, "ARENA_DATABASE_URL="+environment.Plan.InternalDSN()) {
			t.Errorf("the %s service does not address the database of the network: %v", role, spec.Env)
		}
		// And every name it does receive is one the delivered configuration
		// accepts. This list is not decoration: the loader refuses an unknown
		// `ARENA_*` variable to catch a typo instead of ignoring it, so a name
		// invented here would stop the service from booting at all — which is
		// what the live verification proves by booting it.
		for _, variable := range spec.Env {
			name, _, _ := strings.Cut(variable, "=")
			if !acceptedConfiguration[name] {
				t.Errorf("the %s service is given `%s`, which is not a variable the configuration accepts: the loader refuses unknown ARENA_* names, so the service would not boot", role, name)
			}
		}
	}
	// The application listens on the address of the network; the migration and
	// the worker have no listener at all.
	if !contains(specs[RoleApp].Env, "ARENA_ADDR=0.0.0.0:"+internalAppPort) {
		t.Errorf("the application does not listen on the port of the network: %v", specs[RoleApp].Env)
	}
	for role, spec := range specs {
		if role == RoleApp {
			continue
		}
		for _, variable := range spec.Env {
			if strings.HasPrefix(variable, "ARENA_ADDR=") {
				t.Errorf("the %s service is given a listening address: %v", role, spec.Env)
			}
		}
	}
}

// acceptedConfiguration is the list of `ARENA_*` variables the delivered loader
// knows (internal/platform/config, and the same list `.env.example` documents).
// It is repeated here on purpose: this is the test that fails the day somebody
// hands a service a name the product would refuse, and a test that asked the
// plan what it composes could not notice.
var acceptedConfiguration = map[string]bool{
	"ARENA_ENV":                   true,
	"ARENA_ADDR":                  true,
	"ARENA_ADMIN_ADDR":            true,
	"ARENA_ASSETS_DIR":            true,
	"ARENA_EMAIL_SINK_DIR":        true,
	"ARENA_CURSOR_SECRET":         true,
	"ARENA_DATABASE_URL":          true,
	"ARENA_LOG_LEVEL":             true,
	"ARENA_DB_MAX_CONNS":          true,
	"ARENA_DB_MIN_CONNS":          true,
	"ARENA_DB_MAX_CONN_LIFETIME":  true,
	"ARENA_DB_MAX_CONN_IDLE_TIME": true,
	"ARENA_DB_ACQUIRE_TIMEOUT":    true,
	"ARENA_BILLING_MARKETS":       true,
	"ARENA_BILLING_PRICE_IDS":     true,
	"ARENA_BILLING_SUCCESS_URL":   true,
	"ARENA_BILLING_CANCEL_URL":    true,
	"ARENA_STRIPE_SECRET_KEY":     true,
	"ARENA_STRIPE_TIMEOUT":        true,
	"ARENA_RESEND_API_KEY":        true,
	"ARENA_EMAIL_FROM":            true,
	"ARENA_SENTRY_DSN":            true,
	"ARENA_POSTHOG_API_KEY":       true,
	"ARENA_POSTHOG_HOST":          true,
	"ARENA_ANALYTICS_SAMPLE_RATE": true,
}

// TestTheEnvironmentNamesItsOwnVariablesOutsideTheProductNamespace states the
// rule that keeps a sourced environment file from poisoning a process: the
// product claims `ARENA_*`, so the environment's bookkeeping is published under
// its own prefix and can never be mistaken for configuration.
func TestTheEnvironmentNamesItsOwnVariablesOutsideTheProductNamespace(t *testing.T) {
	plan, err := NewPlan(fixtureOptions(t, "names"))
	if err != nil {
		t.Fatalf("the plan cannot be built: %v", err)
	}
	for _, line := range plan.EnvironmentVariables() {
		name, _, found := strings.Cut(line, "=")
		if !found {
			t.Fatalf("the environment file publishes %q, which is not a pair", line)
		}
		if strings.HasPrefix(name, "ARENA_") && !acceptedConfiguration[name] {
			t.Errorf("the environment file publishes `%s`, which the product's loader would refuse: the environment's own variables belong outside the `ARENA_` namespace", name)
		}
	}
	if !contains(plan.EnvironmentVariables(), "ARENA_DATABASE_URL="+plan.HostDSN()) {
		t.Errorf("the environment file does not publish the DSN a test on this machine needs: %v", plan.EnvironmentVariables())
	}
	for _, expected := range []string{
		TestEnvVariable + "=" + plan.Namespace,
		TestEnvAppURL + "=" + plan.AppURL(),
		TestEnvProviders + "=" + plan.ProvidersURL(),
		TestEnvSinkDir + "=" + plan.SinkDir,
		TestEnvHostDSN + "=" + plan.HostDSN(),
	} {
		if !contains(plan.EnvironmentVariables(), expected) {
			t.Errorf("the environment file does not publish %q: %v", expected, plan.EnvironmentVariables())
		}
	}
	// The environment file is what `testenv run` exports, and it is exactly what
	// it hands to a command: a file and a process cannot disagree.
	seen := map[string]bool{}
	for _, line := range plan.EnvironmentVariables() {
		name, value, _ := strings.Cut(line, "=")
		if seen[name] {
			t.Errorf("the environment publishes `%s` twice", name)
		}
		seen[name] = true
		if value == "" {
			t.Errorf("the environment publishes `%s` with no value", name)
		}
	}
	if len(seen) != 11 {
		t.Errorf("the environment publishes %d variables: the seven of its own and the four provider names", len(seen))
	}
}

// TestTheCredentialGuardRefusesCredentialShapedVariables states the guard as a
// behaviour: a plan that would hand a real credential to a test is refused, and
// the environment's own two generated values are admitted by name.
func TestTheCredentialGuardRefusesCredentialShapedVariables(t *testing.T) {
	plan := Plan{CursorSecret: "generated-for-the-test"}
	admitted := []string{
		"ARENA_ENV=test",
		"ARENA_DATABASE_URL=postgres://arena:arena-testenv@postgres:5432/arena?sslmode=disable",
		"ARENA_CURSOR_SECRET=generated-for-the-test",
	}
	if err := assertNoCredentials(admitted, plan); err != nil {
		t.Fatalf("the guard refuses the environment's own variables: %v", err)
	}
	for _, refused := range []string{
		"STRIPE_SECRET_KEY=sk_live_abc",
		"RESEND_API_KEY=re_abc",
		"ARENA_TURNSTILE_SECRET_KEY=1x0000",
		"ARENA_CURSOR_SECRET=somebody-elses-secret",
	} {
		if err := assertNoCredentials([]string{refused}, plan); err == nil {
			t.Errorf("the guard admitted %q", refused)
		}
	}
}

// TestEveryServiceIsOnTheIsolatedNetworkAndPublishesNothing is the shape the
// hermetic plane has to have to be worth the name: the services of the product
// are on the internal network and on no other, they publish nothing — a
// published port on an internal network is not even reachable from the host,
// which is why the door exists — and the door is the only thing on the ordinary
// network, forwarding exactly the ports it was given.
func TestEveryServiceIsOnTheIsolatedNetworkAndPublishesNothing(t *testing.T) {
	environment := fixtureEnvironment(t, fixtureOptions(t, "publish"), newFakeDocker())
	plan := environment.Plan

	services := map[string]ContainerSpec{
		RolePostgres:  environment.postgresSpec(),
		RoleApp:       environment.appSpec(),
		RoleProviders: environment.providersSpec(),
		RoleMigrate:   environment.migrateSpec(),
		RoleWorker:    environment.workerSpec(),
	}
	for role, spec := range services {
		if spec.Network != plan.Network {
			t.Errorf("the %s container is on %q and the isolated network is %q", role, spec.Network, plan.Network)
		}
		if len(spec.Ports) != 0 {
			t.Errorf("the %s container publishes %v: a service is reached through the door and lives on the isolated network only", role, spec.Ports)
		}
		for _, port := range []string{fmt.Sprint(plan.PostgresPort), fmt.Sprint(plan.AppPort), fmt.Sprint(plan.ProviderPort)} {
			if strings.Contains(strings.Join(spec.Args, " "), port) {
				t.Errorf("the %s container names the host port %s: it has no listening address of its own on the host", role, port)
			}
		}
		for _, mount := range spec.Mounts {
			if mount.Host == "" || !strings.Contains(strings.Join(spec.RunArgs(true), " "), mount.Host+":"+mount.Container) {
				t.Errorf("the %s container does not mount %q: %s", role, mount.Host, strings.Join(spec.RunArgs(true), " "))
			}
		}
	}

	// The migration is the delivered command, spelled as its own CLI spells it:
	// `migrate` alone answers "requires a subcommand" and leaves the environment
	// without a schema.
	migrate := services[RoleMigrate]
	if len(migrate.Args) != 2 || migrate.Args[0] != "migrate" || migrate.Args[1] != "up" {
		t.Errorf("the migration container runs %v and the delivered command requires `migrate up`", migrate.Args)
	}

	// The door: the ordinary network, every published address on the loopback
	// interface, and one forward per door of the plan and nothing else.
	door := environment.doorSpec()
	if door.Network != plan.DoorNetwork {
		t.Errorf("the door is on %q and its own network is %q", door.Network, plan.DoorNetwork)
	}
	if door.Name != plan.Containers[RoleDoor] {
		t.Errorf("the door is named %q", door.Name)
	}
	forwards := map[string]bool{}
	for index, arg := range door.Args {
		if arg == "-forward" && index+1 < len(door.Args) {
			forwards[door.Args[index+1]] = true
		}
	}
	if len(forwards) != len(plan.Doors()) {
		t.Errorf("the door forwards %d ports and the environment needs %d: %v", len(forwards), len(plan.Doors()), forwards)
	}
	for _, expected := range plan.Doors() {
		want := fmt.Sprintf("%d:%s:%s", expected.HostPort, expected.Container, expected.Port)
		if !forwards[want] {
			t.Errorf("the door does not forward %q: %v", want, forwards)
		}
	}
	doorArgs := door.RunArgs(true)
	published := 0
	for index, arg := range doorArgs {
		if arg != "--publish" {
			continue
		}
		published++
		if index+1 >= len(doorArgs) || !strings.HasPrefix(doorArgs[index+1], "127.0.0.1:") {
			t.Errorf("the door publishes %q: every published address is on the loopback interface", doorArgs[index+1])
		}
	}
	if published != len(plan.Doors()) {
		t.Errorf("the door publishes %d ports and the environment needs %d", published, len(plan.Doors()))
	}

	postgres := strings.Join(environment.postgresSpec().RunArgs(true), " ")
	if !strings.Contains(postgres, "pg_isready") {
		t.Errorf("the database carries no healthcheck of its own, so the wait for it would be a wait for a running process: %s", postgres)
	}
	if !strings.Contains(postgres, "--tmpfs /var/lib/postgresql") {
		t.Errorf("the database writes its data to the working directory of the run instead of a temporary filesystem: %s", postgres)
	}
	if !strings.Contains(postgres, throwawayPassword) {
		t.Errorf("the database is started without the throwaway credential of the environment: %s", postgres)
	}
}

// TestUpBringsTheServicesUpInOrderAndRecordsThem is the shape of a run: the
// network first, then the database, the migrations, the providers, the
// application and the worker — and only then the record, so that a state file
// means "this environment is complete".
func TestUpBringsTheServicesUpInOrderAndRecordsThem(t *testing.T) {
	fake := newFakeDocker()
	options := fixtureOptions(t, "up-order")
	// The ports are chosen by the plan, which is what refuses a port that is
	// taken; the fixture then serves on exactly those.
	options.PostgresPort = freePort(t)
	options.ProviderPort = freePort(t)
	options.AppPort = freePort(t)
	environment := fixtureEnvironment(t, options, fake)
	serveOn(t, environment.Plan.ProviderPort, &providerServer{})
	serveOn(t, environment.Plan.AppPort, readyApp())

	if err := environment.Up(context.Background()); err != nil {
		t.Fatalf("the fixture environment did not come up: %v", err)
	}

	order := []string{
		"network create internal=true",
		"run --detach --name " + environment.Plan.Containers[RolePostgres],
		"run --detach --name " + environment.Plan.Containers[RoleDoor],
		"network connect " + environment.Plan.Network + " " + environment.Plan.Containers[RoleDoor],
		"run --rm --name " + environment.Plan.Containers[RoleMigrate],
		"run --detach --name " + environment.Plan.Containers[RoleProviders],
		"run --detach --name " + environment.Plan.Containers[RoleApp],
		"run --detach --name " + environment.Plan.Containers[RoleWorker],
	}
	position := 0
	for _, call := range fake.calls {
		if position < len(order) && strings.Contains(call, order[position]) {
			position++
		}
	}
	if position != len(order) {
		t.Fatalf("the services did not come up in the order the phase asks for: %q is missing in\n%s", order[position], strings.Join(fake.calls, "\n"))
	}

	state, err := ReadState(options.StateDir, "up-order")
	if err != nil {
		t.Fatalf("the environment was not recorded: %v", err)
	}
	if state.PostgresPort != environment.Plan.PostgresPort || state.AppPort != environment.Plan.AppPort || state.ProviderPort != environment.Plan.ProviderPort {
		t.Errorf("the record does not carry the addresses: %+v", state)
	}
	if state.CreatedAt.IsZero() {
		t.Errorf("the record carries no creation instant: %+v", state)
	}
	raw, err := os.ReadFile(state.Environment)
	if err != nil {
		t.Fatalf("the environment file was not written: %v", err)
	}
	for _, expected := range []string{"ARENA_DATABASE_URL=", TestEnvAppURL + "=", TestEnvProviders + "=", TestEnvProviderVar + "STRIPE="} {
		if !strings.Contains(string(raw), expected) {
			t.Errorf("the environment file does not carry %q:\n%s", expected, raw)
		}
	}
	if strings.Contains(string(raw), environment.Plan.CursorSecret) {
		t.Errorf("the environment file carries the cursor secret, which is a value of the services and not of the shell")
	}
	// What the file publishes as the database is the address the host reaches,
	// not the one of the network: a Go test on this machine cannot resolve the
	// container's name.
	if !strings.Contains(string(raw), environment.Plan.HostDSN()) {
		t.Errorf("the environment file does not carry the host address of the database:\n%s", raw)
	}
}

// TestAnInterruptedRunLeavesNothingBehind is the second validation, and it holds
// two things at once: that a run cut short leaves no container, no network and no
// record — and that the teardown does not run inside the cancellation, which the
// fake makes observable by refusing to work with a cancelled context.
func TestAnInterruptedRunLeavesNothingBehind(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	fake := newFakeDocker()
	fake.startHealth = "starting" // the database never becomes ready
	options := fixtureOptions(t, "interrupted")
	options.ReadyTimeout = 10 * time.Second // the deadline, not readiness, is what ends this run
	environment := fixtureEnvironment(t, options, fake)

	var stdout, stderr bytes.Buffer
	status, err := environment.Run(ctx, []string{"sh", "-c", "echo the-command-ran"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("the environment came up on a context that was already expiring")
	}
	if status != exitViolation {
		t.Errorf("the interrupted run reported %d", status)
	}
	if !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("the diagnosis does not say the run was interrupted: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("the command ran despite the interruption: %q", stdout.String())
	}
	if len(fake.containers) != 0 {
		t.Errorf("the containers survived the interruption: %v", fake.containers)
	}
	if !fake.called("network rm " + environment.Plan.Network) {
		t.Errorf("the network survived the interruption:\n%s", strings.Join(fake.calls, "\n"))
	}
	if _, err := os.Stat(environment.Plan.WorkDir); !os.IsNotExist(err) {
		t.Errorf("the working directory survived the interruption: %v", err)
	}
	if _, err := os.Stat(environment.Plan.StatePath); !os.IsNotExist(err) {
		t.Errorf("the state file survived the interruption: %v", err)
	}
}

// TestRunTakesTheEnvironmentDownWhateverTheCommandDoes is the guarantee of the
// phase expressed as the two outcomes a command can have: a green test and a red
// one both leave the machine as it was.
func TestRunTakesTheEnvironmentDownWhateverTheCommandDoes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture drives a POSIX shell")
	}
	for _, testCase := range []struct {
		name       string
		argv       []string
		wantStatus int
		wantOut    string
	}{{name: "a command that succeeds", argv: []string{"sh", "-c", `printf %s "$TESTENV_NAMESPACE"`}, wantStatus: exitOK, wantOut: "run-outcome"},
		{name: "a command that fails", argv: []string{"sh", "-c", "exit 7"}, wantStatus: 7},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeDocker()
			options := fixtureOptions(t, "run-outcome")
			options.PostgresPort = freePort(t)
			options.ProviderPort = freePort(t)
			options.AppPort = freePort(t)
			environment := fixtureEnvironment(t, options, fake)
			serveOn(t, environment.Plan.ProviderPort, &providerServer{})
			serveOn(t, environment.Plan.AppPort, readyApp())

			var stdout, stderr bytes.Buffer
			status, err := environment.Run(context.Background(), testCase.argv, &stdout, &stderr)
			if err != nil {
				t.Fatalf("the run reported %v", err)
			}
			if status != testCase.wantStatus {
				t.Fatalf("the run returned %d and the command returned %d: %s%s", status, testCase.wantStatus, stdout.String(), stderr.String())
			}
			if testCase.wantOut != "" && !strings.Contains(stdout.String(), testCase.wantOut) {
				t.Errorf("the command did not receive the environment: stdout %q", stdout.String())
			}
			if len(fake.containers) != 0 {
				t.Errorf("the namespace survived the run: %v", fake.containers)
			}
			if _, err := os.Stat(environment.Plan.StatePath); !os.IsNotExist(err) {
				t.Errorf("the state file survived the run: %v", err)
			}
			if _, err := os.Stat(environment.Plan.WorkDir); !os.IsNotExist(err) {
				t.Errorf("the working directory survived the run: %v", err)
			}
		})
	}
}

// TestDownIsIdempotent states the property the interruption path depends on:
// taking down an environment that is already gone is not a failure.
func TestDownIsIdempotent(t *testing.T) {
	fake := newFakeDocker()
	environment := fixtureEnvironment(t, fixtureOptions(t, "idempotent"), fake)
	if err := environment.Down(context.Background()); err != nil {
		t.Fatalf("taking down an environment that was never built failed: %v", err)
	}
	// A second teardown over a namespace whose containers were already removed
	// by somebody else: the daemon has nothing left to say about it.
	fake.containers["arena-testenv-idempotent-app"] = &fakeContainer{running: false, health: "none"}
	if err := environment.Down(context.Background()); err != nil {
		t.Fatalf("the second teardown failed: %v", err)
	}
	if len(fake.containers) != 0 {
		t.Errorf("the containers survived the second teardown: %v", fake.containers)
	}
}

// TestStatusShowsWhatTheDaemonHasAndWhatWasRecorded is the surface a person uses
// when something went wrong: the addresses of the namespace and the containers
// that are actually there.
func TestStatusShowsWhatTheDaemonHasAndWhatWasRecorded(t *testing.T) {
	fake := newFakeDocker()
	environment := fixtureEnvironment(t, fixtureOptions(t, "status-check"), fake)
	if err := fake.Start(context.Background(), environment.appSpec()); err != nil {
		t.Fatalf("the fixture container cannot be started: %v", err)
	}
	// The addresses of a namespace are the ones it recorded when it was
	// created. A second command derives its own plan, with ports of its own,
	// and reporting those would report an environment that does not exist — so
	// the record is written here and the status must read it.
	if err := environment.Plan.WriteState(); err != nil {
		t.Fatalf("the fixture environment cannot be recorded: %v", err)
	}
	var out bytes.Buffer
	if err := environment.Status(context.Background(), &out); err != nil {
		t.Fatalf("the status cannot be read: %v", err)
	}
	for _, expected := range []string{"namespace status-check", environment.Plan.Network, environment.Plan.DoorNetwork, environment.Plan.AppURL(), environment.Plan.Containers[RoleApp], "recorded "} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("the status does not mention %q:\n%s", expected, out.String())
		}
	}

	// A namespace with no record and no container: what an interrupted run
	// leaves behind, and the status has to say both things.
	empty := fixtureEnvironment(t, fixtureOptions(t, "status-empty"), newFakeDocker())
	out.Reset()
	if err := empty.Status(context.Background(), &out); err != nil {
		t.Fatalf("the status of an empty namespace cannot be read: %v", err)
	}
	for _, expected := range []string{"no record", "no container of this namespace"} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("the status of a namespace with nothing in it does not say %q:\n%s", expected, out.String())
		}
	}
}

// TestANamespaceIsNotBuiltTwice states the collision guard: a namespace is the
// name of one environment, and building the same name twice would put the second
// run's containers on the first one's network and ports.
func TestANamespaceIsNotBuiltTwice(t *testing.T) {
	fake := newFakeDocker()
	environment := fixtureEnvironment(t, fixtureOptions(t, "twice"), fake)
	if err := environment.Plan.WriteState(); err != nil {
		t.Fatalf("the fixture record cannot be written: %v", err)
	}
	err := environment.Up(context.Background())
	if err == nil {
		t.Fatal("the environment was built over a namespace that is already recorded")
	}
	for _, expected := range []string{"already recorded", "testenv down -namespace twice"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("the refusal does not say %q: %v", expected, err)
		}
	}
	if len(fake.calls) != 0 {
		t.Errorf("the refusal created something anyway: %v", fake.calls)
	}
}

// TestTheProviderSurfaceRecordsAndRefuses states what the fake providers do
// today: they answer a liveness probe, they keep every call, and they refuse
// everything else — because a service that answered 200 to a request nobody
// implemented would let a test pass over its absence.
func TestTheProviderSurfaceRecordsAndRefuses(t *testing.T) {
	server := httptest.NewServer(&providerServer{})
	defer server.Close()

	response, err := http.Get(server.URL + "/health/live")
	if err != nil {
		t.Fatalf("the liveness probe failed: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the liveness probe answered %d", response.StatusCode)
	}

	response, err = http.Post(server.URL+"/v1/checkout/sessions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("the recorded call failed: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNotImplemented {
		t.Fatalf("an unimplemented provider route answered %d: the surface must refuse, not pretend", response.StatusCode)
	}
	if !strings.Contains(string(body), "testenv_provider_not_implemented") {
		t.Errorf("the refusal does not name itself: %s", body)
	}

	response, err = http.Get(server.URL + "/__testenv/calls")
	if err != nil {
		t.Fatalf("the call log cannot be read: %v", err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload struct {
		Calls []recordedCall `json:"calls"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("the call log does not decode: %v", err)
	}
	if len(payload.Calls) != 1 || payload.Calls[0].Path != "/v1/checkout/sessions" || payload.Calls[0].Method != http.MethodPost {
		t.Fatalf("the call log carries %+v", payload.Calls)
	}
	if payload.Calls[0].At.IsZero() {
		t.Errorf("the recorded call carries no instant: %+v", payload.Calls[0])
	}
}

// TestTheProviderServerRunInsideTheNetworkListensWhereItIsTold holds the
// subcommand the environment runs as a container: it listens on the address it
// is given and it stops when it cannot.
func TestTheProviderServerRunInsideTheNetworkListensWhereItIsTold(t *testing.T) {
	port := freePort(t)
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- runProviders([]string{"-addr", fmt.Sprintf("127.0.0.1:%d", port)}, &stdout, &stderr) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health/live", port))
		if err == nil {
			response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake provider surface never answered on the address it was given: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(stdout.String(), "fake providers listening on") {
		t.Errorf("the service does not report where it listens: %s", stdout.String())
	}
	// Nothing else can take the same address, which is what the refusal is for.
	var refused, refusedErr bytes.Buffer
	if status := runProviders([]string{"-addr", fmt.Sprintf("127.0.0.1:%d", port)}, &refused, &refusedErr); status == exitOK {
		t.Errorf("a second surface took an address that is in use")
	}
}

// TestTheProbeIsTheProofThatTheNetworkIsClosed is the fourth validation's other
// half: from inside the network a public address is unreachable — and the same
// command refuses when it is not.
func TestTheProbeIsTheProofThatTheNetworkIsClosed(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// 192.0.2.0/24 is reserved for documentation and carried nowhere: the dial
	// has no answer to wait for, which is the same answer the internet gives a
	// container on an internal network.
	if status := runProbe([]string{"-address", "192.0.2.1:9", "-timeout", "500ms"}, &stdout, &stderr); status != exitOK {
		t.Fatalf("the probe failed on an address that is carried nowhere: %s%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "unreachable") {
		t.Errorf("the probe does not report what it proved: %s", stdout.String())
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("the test cannot serve an address: %v", err)
	}
	defer listener.Close()
	stdout.Reset()
	stderr.Reset()
	if status := runProbe([]string{"-address", listener.Addr().String(), "-timeout", "1s"}, &stdout, &stderr); status == exitOK {
		t.Fatalf("the probe reported an address it could reach as unreachable: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "not isolated") {
		t.Errorf("the refusal does not say what it means: %s", stderr.String())
	}
}

// TestTheForwarderOnlyForwardsWhatItWasTold states the door's contract: the
// mapping is validated when it is read, the command refuses to run without one,
// and it actually carries bytes to the fixed target — the last one because a
// forwarder that never forwards would pass every validation of the environment
// except the readiness of everything behind it.
func TestTheForwarderOnlyForwardsWhatItWasTold(t *testing.T) {
	for _, refused := range []string{"", "9090", "9090:app", "notaport:app:8080", "9090:app:notaport", "9090::8080", "9090:app:8080:extra", ":app:8080"} {
		if _, err := parseForward(refused); err == nil {
			t.Errorf("the door accepted %q as a mapping", refused)
		}
	}
	mapping, err := parseForward("9090:arena-testenv-x-app:8080")
	if err != nil {
		t.Fatalf("the door refuses a mapping of its own: %v", err)
	}
	if mapping.listenPort != "9090" || mapping.target != "arena-testenv-x-app:8080" {
		t.Fatalf("the mapping reads %+v", mapping)
	}

	// The command: no mapping is a refusal, and a malformed one is refused when
	// it is given rather than when a connection arrives.
	var stdout, stderr bytes.Buffer
	if status := runForward(nil, &stdout, &stderr); status == exitOK {
		t.Errorf("the door ran without a mapping")
	}
	if status := runForward([]string{"-forward", "9090:app"}, &stdout, &stderr); status == exitOK {
		t.Errorf("the door ran with a malformed mapping")
	}

	// A service to reach, and a listener to forward from.
	service, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("the fixture service cannot listen: %v", err)
	}
	defer service.Close()
	go func() {
		for {
			connection, err := service.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("the door cannot listen: %v", err)
	}
	defer listener.Close()
	go forwardLoop(listener, service.Addr().String())

	connection, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("the door does not accept connections: %v", err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("ping\n")); err != nil {
		t.Fatalf("the door cannot be written to: %v", err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	echoed := make([]byte, len("ping\n"))
	if _, err := io.ReadFull(connection, echoed); err != nil {
		t.Fatalf("the door did not carry the answer back: %v", err)
	}
	if string(echoed) != "ping\n" {
		t.Errorf("the door carried %q", echoed)
	}
}

// TestTheIsolationProbeIsAContainerOfTheEnvironment states the fourth
// validation as the command that carries it: the probe runs inside the isolated
// network, publishes nothing, and the environment reports a failure of the probe
// as a failure of isolation rather than as a success.
func TestTheIsolationProbeIsAContainerOfTheEnvironment(t *testing.T) {
	fake := newFakeDocker()
	environment := fixtureEnvironment(t, fixtureOptions(t, "isolation-probe"), fake)
	spec := environment.ProbeSpec("1.1.1.1:443", 3*time.Second)
	if spec.Network != environment.Plan.Network {
		t.Errorf("the probe runs on %q and the isolated network is %q", spec.Network, environment.Plan.Network)
	}
	if len(spec.Ports) != 0 {
		t.Errorf("the probe publishes %v", spec.Ports)
	}
	if !strings.Contains(strings.Join(spec.Args, " "), "probe -address 1.1.1.1:443") {
		t.Errorf("the probe runs %v", spec.Args)
	}

	var stdout bytes.Buffer
	if err := environment.Isolated(context.Background(), "1.1.1.1:443", &stdout); err != nil {
		t.Fatalf("the fixture environment cannot probe itself: %v", err)
	}
	if !strings.Contains(stdout.String(), "unreachable") {
		t.Errorf("the probe does not report what it proved: %s", stdout.String())
	}

	// A probe that reached the address is the failure the command exists for.
	fake.failRunOnce = fmt.Errorf("the probe reached 1.1.1.1:443 from inside the network: the environment is not isolated")
	stdout.Reset()
	err := environment.Isolated(context.Background(), "1.1.1.1:443", &stdout)
	if err == nil {
		t.Fatal("the environment reported itself isolated when the probe reached the address")
	}
	if !strings.Contains(err.Error(), "not isolated") {
		t.Errorf("the refusal does not say what it means: %v", err)
	}
}

// TestAPlanWithoutAFrontendBuildIsRefused states the diagnosis a caller needs:
// the application composes its pages from the build, so a backend environment
// without one cannot boot — and the refusal says what to run instead.
func TestAPlanWithoutAFrontendBuildIsRefused(t *testing.T) {
	options := fixtureOptions(t, "no-assets")
	options.AssetsDir = filepath.Join(t.TempDir(), "nothing-here")
	environment := fixtureEnvironment(t, options, newFakeDocker())
	err := environment.Up(context.Background())
	if err == nil {
		t.Fatal("the environment came up without a frontend build")
	}
	if !strings.Contains(err.Error(), "make build-web") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}

	options = fixtureOptions(t, "no-assets")
	options.AssetsDir = ""
	if err := fixtureEnvironment(t, options, newFakeDocker()).Up(context.Background()); err == nil {
		t.Fatal("the environment came up with no frontend build named at all")
	}
}

// TestNoDaemonIsADiagnosisAndNotAFailure states the first thing the command
// does: a machine without a daemon gets one sentence that says so.
func TestNoDaemonIsADiagnosisAndNotAFailure(t *testing.T) {
	fake := newFakeDocker()
	fake.version = ""
	environment := fixtureEnvironment(t, fixtureOptions(t, "no-daemon"), fake)
	err := environment.Up(context.Background())
	if err == nil {
		t.Fatal("the environment came up without a daemon")
	}
	if !strings.Contains(err.Error(), "Docker daemon") {
		t.Errorf("the refusal does not name what is missing: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("the command did something before asking the daemon: %v", fake.calls)
	}
}

// TestTheCommandRefusesWhatItCannotDo is the wiring of the command surface: a
// namespace that is not one, an unknown subcommand and a run without a command
// are refusals, not silent successes.
func TestTheCommandRefusesWhatItCannotDo(t *testing.T) {
	for _, args := range [][]string{{"nonsense"}, {}, {"status", "-namespace", "Not A Namespace"}, {"run"}, {"run", "--"}} {
		var stdout, stderr bytes.Buffer
		if status := runCLI(args, &stdout, &stderr); status == exitOK {
			t.Errorf("`testenv %s` was accepted", strings.Join(args, " "))
		}
	}
	var stdout, stderr bytes.Buffer
	if status := runCLI([]string{"help"}, &stdout, &stderr); status != exitOK {
		t.Errorf("help is not a success: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "testenv run") {
		t.Errorf("the usage does not name the shapes of the command: %s", stdout.String())
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
