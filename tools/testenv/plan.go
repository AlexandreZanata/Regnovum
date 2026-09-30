package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The pinned references of the environment (P22-T01).
//
// Both are pinned the way the delivered artifacts pin theirs: the database by
// patch version — the same `postgres:18.4` the development Compose declares, so
// an environment and a workstation run the same server — and the runtime base by
// manifest list digest, which is the third stage of the production `Dockerfile`.
// A floating tag is what makes an environment pass today and fail tomorrow
// without a commit, which is precisely the failure a hermetic environment exists
// to remove.
const (
	PostgresImage = "postgres:18.4"
	RuntimeImage  = "gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab"
)

// NamespacePattern is what a namespace may look like. It becomes a Docker
// network name, five container names and a file name, so the vocabulary is
// narrow enough to be a file name everywhere and still readable enough to name
// the run that left something behind.
var NamespacePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,24}$`)

// Roles of the environment, in the order it brings them up. Each one is a label
// and a container name suffix.
const (
	RolePostgres  = "postgres"
	RoleProviders = "providers"
	RoleMigrate   = "migrate"
	RoleApp       = "app"
	RoleWorker    = "worker"
	RoleDoor      = "door"
)

// Roles is the whole vocabulary. The order is the order of the container names
// (they are sorted by role when they are rendered), not the order in which the
// environment starts them: that order is a decision of the lifecycle and it is
// stated there, next to the reasons for it.
var Roles = []string{RolePostgres, RoleProviders, RoleMigrate, RoleApp, RoleWorker, RoleDoor}

// The addresses inside the isolated network. They are not published and not
// configurable: a service of the environment reaches another by name, which is
// what the network is for, and the host reaches only what the environment
// publishes on the loopback interface.
const (
	internalPostgresPort  = "5432"
	internalAppPort       = "8080"
	internalProvidersPort = "9090"
)

// throwawayPassword is the credential of the database of an environment. It is
// a constant on purpose: it is not a secret, it is not shared with anything, and
// a test environment whose password changed per run would make the DSN
// unreproducible without making the database any safer — it is reachable on the
// loopback interface of one machine and holds synthetic data.
const throwawayPassword = "arena-testenv"

// databaseName is the database of the run. The environment owns the whole
// server, so there is no need for a database per run beside another one.
const databaseName = "arena"

// Options is what a caller may choose about one environment.
type Options struct {
	Root         string
	StateDir     string
	Namespace    string
	PostgresPort int
	AppPort      int
	ProviderPort int
	AssetsDir    string
	ReadyTimeout time.Duration
	Now          func() time.Time
	Log          func(format string, args ...any)
}

// Plan is one environment as data: every name, address and variable decided
// before anything is created. It is a value because that is what makes the
// orchestration testable without a daemon — the tests hold the plan, and the
// lifecycle only carries it out.
type Plan struct {
	Root         string
	StateDir     string
	Namespace    string
	Network      string
	DoorNetwork  string
	Containers   map[string]string
	PostgresPort int
	AppPort      int
	ProviderPort int
	AssetsDir    string
	WorkDir      string
	SinkDir      string
	BinDir       string
	CursorSecret string
	StatePath    string
	CreatedAt    time.Time
	ReadyTimeout time.Duration
}

// Door is one forwarded port: the address the host reaches, and the service of
// the isolated network it leads to.
type Door struct {
	HostPort  int
	Container string
	Port      string
}

// Doors are the ports the host reaches. They are the whole reason the door
// container exists, and the reason is a measurement rather than a preference:
// Docker does not create the host-side mapping of a published port on an
// internal network, so a service that published its own port on the hermetic
// network would be unreachable — and one that published on an ordinary network
// would have a route out. The door is dual-homed: it is reachable from the host
// on an ordinary network and it reaches the services on the hermetic one, and
// nothing else of the environment is on the ordinary network at all.
func (plan Plan) Doors() []Door {
	return []Door{
		{HostPort: plan.PostgresPort, Container: plan.Containers[RolePostgres], Port: internalPostgresPort},
		{HostPort: plan.AppPort, Container: plan.Containers[RoleApp], Port: internalAppPort},
		{HostPort: plan.ProviderPort, Container: plan.Containers[RoleProviders], Port: internalProvidersPort},
	}
}

// NetworkInternal is the flag that makes the network hermetic: Docker gives a
// container on an internal network no route out, so a service that tried to
// reach the internet fails instead of quietly depending on it. It is a
// constant because the audit of the environment asserts it by name.
const networkInternalFlag = "--internal"

// Labels of everything the environment creates. They are what lets the teardown
// find the environment again from a fresh process — after an interruption, the
// process that created the containers is gone, and only the labels remember
// which namespace they belonged to.
func labelsFor(namespace string) map[string]string {
	return map[string]string{
		"arena.testenv.namespace": namespace,
		"arena.testenv.part-of":   "phase-22-test-platform",
	}
}

// labelFilters renders the labels as `--filter label=key=value`.
func labelFilters(namespace string) []string {
	labels := labelsFor(namespace)
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	filters := make([]string, 0, len(keys))
	for _, key := range keys {
		filters = append(filters, "--filter", "label="+key+"="+labels[key])
	}
	return filters
}

// NewPlan decides everything about one environment. It refuses a namespace that
// is not a namespace, a state directory it cannot write, and a port that is
// already taken — the last one with the diagnosis the phase asks for, because a
// port collision discovered as a Docker error three steps later is a diagnosis
// nobody can act on.
func NewPlan(options Options) (Plan, error) {
	if options.Root == "" {
		return Plan{}, fmt.Errorf("the repository root is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReadyTimeout == 0 {
		options.ReadyTimeout = 90 * time.Second
	}
	if options.Namespace == "" {
		generated, err := newNamespace()
		if err != nil {
			return Plan{}, err
		}
		options.Namespace = generated
	}
	if !NamespacePattern.MatchString(options.Namespace) {
		return Plan{}, fmt.Errorf("`%s` is not a namespace: the convention is lower case letters, digits and dashes, starting with a letter or a digit", options.Namespace)
	}

	stateDir := options.StateDir
	if stateDir == "" {
		stateDir = filepath.Join(os.TempDir(), "arena-testenv")
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return Plan{}, fmt.Errorf("the state directory %s is not writable: %w", stateDir, err)
	}

	prefix := "arena-testenv-" + options.Namespace
	workDir := filepath.Join(stateDir, options.Namespace)
	plan := Plan{
		Root:         options.Root,
		StateDir:     stateDir,
		Namespace:    options.Namespace,
		Network:      prefix,
		DoorNetwork:  prefix + "-door",
		Containers:   map[string]string{},
		AssetsDir:    options.AssetsDir,
		WorkDir:      workDir,
		SinkDir:      filepath.Join(workDir, "email-sink"),
		BinDir:       filepath.Join(workDir, "bin"),
		StatePath:    filepath.Join(stateDir, options.Namespace+".json"),
		CreatedAt:    options.Now().UTC(),
		ReadyTimeout: options.ReadyTimeout,
	}
	for _, role := range Roles {
		plan.Containers[role] = prefix + "-" + role
	}

	var err error
	if plan.PostgresPort, err = resolvePort(options.PostgresPort, "the database"); err != nil {
		return Plan{}, err
	}
	if plan.AppPort, err = resolvePort(options.AppPort, "the application"); err != nil {
		return Plan{}, err
	}
	if plan.ProviderPort, err = resolvePort(options.ProviderPort, "the fake providers"); err != nil {
		return Plan{}, err
	}
	if plan.CursorSecret, err = newSecret(); err != nil {
		return Plan{}, err
	}
	// The sink is written by a process that runs as an unprivileged user of the
	// runtime base, so the directory of one throwaway environment is opened to
	// it. It holds synthetic messages of a test and nothing else.
	if err := os.MkdirAll(plan.SinkDir, 0o777); err != nil {
		return Plan{}, fmt.Errorf("the email sink directory is not creatable: %w", err)
	}
	return plan, nil
}

// resolvePort answers with a port that is free right now, either the one that
// was asked for or one the kernel hands out. A port that is taken is refused
// here, with the port and the way to look at it, rather than left to fail as a
// Docker error.
func resolvePort(wanted int, subject string) (int, error) {
	if wanted == 0 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, fmt.Errorf("no free port can be found for %s: %w", subject, err)
		}
		defer listener.Close()
		return listener.Addr().(*net.TCPAddr).Port, nil
	}
	if wanted < 1 || wanted > 65535 {
		return 0, fmt.Errorf("%d is not a port for %s", wanted, subject)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", wanted))
	if err != nil {
		return 0, fmt.Errorf("the port %d asked for %s is already taken: %w\n  who holds it: `lsof -nP -iTCP:%d -sTCP:LISTEN`, or `docker ps --filter publish=%d` if it is a container", wanted, subject, err, wanted, wanted)
	}
	defer listener.Close()
	return wanted, nil
}

// newNamespace is a short random name for the run. It is random because two
// environments started at the same moment must not collide, and it is short
// because it ends up in five container names a person has to read.
func newNamespace() (string, error) {
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("a namespace cannot be generated: %w", err)
	}
	return "run-" + hex.EncodeToString(raw), nil
}

// newSecret is the pagination cursor signing key of the environment, generated
// per run and never committed: the harness that drives the delivered process
// already does exactly this, and a shared secret in a test environment is the
// first half of a shared secret in production.
func newSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("a cursor secret cannot be generated: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// InternalDSN is the address a service of the network uses for the database.
func (plan Plan) InternalDSN() string {
	return fmt.Sprintf("postgres://arena:%s@%s:%s/%s?sslmode=disable", throwawayPassword, plan.Containers[RolePostgres], internalPostgresPort, databaseName)
}

// HostDSN is the same database seen from the machine, through the loopback
// address the environment publishes. Tests that run as processes on the host —
// every Go test of this repository — use this one.
func (plan Plan) HostDSN() string {
	return fmt.Sprintf("postgres://arena:%s@127.0.0.1:%d/%s?sslmode=disable", throwawayPassword, plan.PostgresPort, databaseName)
}

// AppURL is the application's address as the host reaches it.
func (plan Plan) AppURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", plan.AppPort)
}

// ProvidersURL is the fake provider surface as the host reaches it.
func (plan Plan) ProvidersURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", plan.ProviderPort)
}

// InternalProvidersURL is the fake provider surface as a service of the network
// reaches it. The product's adapters are configured with this one: a provider
// address that pointed at the host would be an environment whose services
// depend on the machine that started them.
func (plan Plan) InternalProvidersURL() string {
	return fmt.Sprintf("http://%s:%s", plan.Containers[RoleProviders], internalProvidersPort)
}

// ServiceEnvironment is the environment the application, the worker and the
// migration receive. It is an explicit list, never the caller's environment: a
// test environment that inherited the shell would depend on whoever started it,
// and would carry whatever credentials that shell happened to hold.
//
// The list is exactly what the delivered configuration accepts in
// `ARENA_ENV=test`, read from the loader itself (internal/platform/config) and
// from the harness that already drives the process (tools/e2e/harness.sh)
// rather than guessed: the DSN, the frontend build the pages reference, the
// local email sink, a per-run cursor secret and the log level. No provider
// credential appears, because a test environment has none to give.
//
// Nothing beyond that list may be added here, and the reason is not tidiness:
// the loader refuses an unknown `ARENA_*` variable to catch a typo instead of
// ignoring it, so one invented name would stop the application and the worker
// from booting at all. The environment's own bookkeeping therefore lives under
// its own prefix (TestEnvVariable) and never inside the product's namespace.
func (plan Plan) ServiceEnvironment(role string) []string {
	variables := []string{
		"ARENA_ENV=test",
		"ARENA_DATABASE_URL=" + plan.InternalDSN(),
		"ARENA_ASSETS_DIR=/web/dist",
		"ARENA_EMAIL_SINK_DIR=/sink",
		"ARENA_CURSOR_SECRET=" + plan.CursorSecret,
		"ARENA_LOG_LEVEL=info",
	}
	if role == RoleApp {
		variables = append(variables, "ARENA_ADDR=0.0.0.0:"+internalAppPort)
	}
	sort.Strings(variables)
	return variables
}

// The variables the environment publishes about itself. They are deliberately
// outside the `ARENA_` namespace the product claims: an environment that
// published `ARENA_TESTENV_*` would be handing the process a variable its own
// loader refuses, and the harness that already drives the delivered binary
// solved the same problem the same way (it scrubs `ARENA_E2E_*` before starting
// the process). A test that sources the environment file and then boots the
// application is therefore not a trap.
const (
	TestEnvVariable    = "TESTENV_NAMESPACE"
	TestEnvState       = "TESTENV_STATE"
	TestEnvHostDSN     = "TESTENV_HOST_DSN"
	TestEnvAppURL      = "TESTENV_APP_URL"
	TestEnvProviders   = "TESTENV_PROVIDERS_URL"
	TestEnvSinkDir     = "TESTENV_SINK_DIR"
	TestEnvProviderVar = "TESTENV_PROVIDER_"
)

// EnvironmentFile is what a test sources to reach the environment: the DSN as
// the host sees it, the two published addresses, the sink directory and the
// provider surface under the four names the product uses. It is rendered, never
// hand-written, so it cannot disagree with what was created.
func (plan Plan) EnvironmentFile() string {
	variables := append([]string{
		TestEnvVariable + "=" + plan.Namespace,
		TestEnvState + "=" + plan.StatePath,
		TestEnvHostDSN + "=" + plan.HostDSN(),
		// ARENA_DATABASE_URL is published because it is a name the product
		// already accepts, and it is the one a test on this machine needs: the
		// database of the environment, as the host reaches it. Anything the
		// environment adds about itself stays outside that namespace.
		"ARENA_DATABASE_URL=" + plan.HostDSN(),
		TestEnvAppURL + "=" + plan.AppURL(),
		TestEnvProviders + "=" + plan.ProvidersURL(),
		TestEnvSinkDir + "=" + plan.SinkDir,
	}, providerVariables(plan.ProvidersURL())...)
	sort.Strings(variables)

	var out strings.Builder
	out.WriteString("# Environment of the run `" + plan.Namespace + "`, written by tools/testenv (P22-T01).\n")
	out.WriteString("# Generated, never edited: `source` it, or use `testenv run -- <command>`,\n")
	out.WriteString("# which exports exactly these variables to the command it drives.\n")
	for _, variable := range variables {
		out.WriteString(variable + "\n")
	}
	return out.String()
}

// credential is the shape of a variable name that must never appear in a test
// environment. The guard below refuses the plan that carries one, which is how
// "no real credential" stops being a promise in a document and becomes a
// behaviour of the command.
var credential = regexp.MustCompile(`(?i)(_KEY|_SECRET|_TOKEN|_PASSWORD|_CREDENTIAL|_WEBHOOK_SECRET|API_KEY)$`)

// assertNoCredentials refuses an environment whose variables carry a credential.
// The two names that are allowed are the throwaway ones of the local database
// and the per-run cursor secret, and both are asserted to hold the value the
// plan generated rather than a value that came from anywhere else.
func assertNoCredentials(variables []string, plan Plan) error {
	for _, variable := range variables {
		name, value, _ := strings.Cut(variable, "=")
		if !credential.MatchString(name) {
			continue
		}
		switch name {
		case "ARENA_CURSOR_SECRET":
			if value != plan.CursorSecret {
				return fmt.Errorf("`%s` carries a value the environment did not generate", name)
			}
		default:
			if strings.Contains(value, "@") || !strings.HasPrefix(value, "postgres://") {
				return fmt.Errorf("`%s` carries a credential: a test environment has none to give", name)
			}
		}
	}
	return nil
}

// State is what the environment records about itself, so that a teardown from a
// fresh process can find everything an interrupted run created.
type State struct {
	Namespace    string            `json:"namespace"`
	Network      string            `json:"network"`
	DoorNetwork  string            `json:"door_network"`
	Containers   map[string]string `json:"containers"`
	PostgresPort int               `json:"postgres_port"`
	AppPort      int               `json:"app_port"`
	ProviderPort int               `json:"provider_port"`
	HostDSN      string            `json:"host_dsn"`
	AppURL       string            `json:"app_url"`
	ProvidersURL string            `json:"providers_url"`
	SinkDir      string            `json:"sink_dir"`
	Environment  string            `json:"environment_file"`
	CreatedAt    time.Time         `json:"created_at"`
}

// StateOf reduces the plan to the state a later teardown needs.
func (plan Plan) StateOf() State {
	return State{
		Namespace:    plan.Namespace,
		Network:      plan.Network,
		DoorNetwork:  plan.DoorNetwork,
		Containers:   plan.Containers,
		PostgresPort: plan.PostgresPort,
		AppPort:      plan.AppPort,
		ProviderPort: plan.ProviderPort,
		HostDSN:      plan.HostDSN(),
		AppURL:       plan.AppURL(),
		ProvidersURL: plan.ProvidersURL(),
		SinkDir:      plan.SinkDir,
		Environment:  filepath.Join(plan.WorkDir, "environment"),
		CreatedAt:    plan.CreatedAt,
	}
}

// Recorded answers the state of this namespace, and whether there is one. The
// commands that act on a namespace that already exists — status, and the guard
// that refuses to build the same namespace twice — read it here rather than
// deriving a plan again: a plan derived a second time has new ports, and
// reporting those would be reporting an environment that does not exist.
func (plan Plan) Recorded() (State, bool) {
	state, err := ReadState(plan.StateDir, plan.Namespace)
	if err != nil {
		return State{}, false
	}
	return state, true
}

// WriteState records the environment, and writes the environment file beside
// it. Both are written after everything is up, so a state file means "this
// environment is complete" rather than "somebody started creating it".
func (plan Plan) WriteState() error {
	state := plan.StateOf()
	if err := os.MkdirAll(plan.WorkDir, 0o755); err != nil {
		return fmt.Errorf("the environment directory is not creatable: %w", err)
	}
	if err := os.WriteFile(state.Environment, []byte(plan.EnvironmentFile()), 0o644); err != nil {
		return fmt.Errorf("the environment file is not writable: %w", err)
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("the state cannot be encoded: %w", err)
	}
	if err := os.WriteFile(plan.StatePath, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("the state file is not writable: %w", err)
	}
	return nil
}

// ReadState reads the record of a namespace from the state directory.
func ReadState(stateDir, namespace string) (State, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, namespace+".json"))
	if err != nil {
		return State{}, fmt.Errorf("the namespace `%s` is not recorded in %s: %w", namespace, stateDir, err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("the state of `%s` does not decode: %w", namespace, err)
	}
	if state.Namespace != namespace {
		return State{}, fmt.Errorf("the state of `%s` records the namespace `%s`", namespace, state.Namespace)
	}
	return state, nil
}

// ListState names the environments the state directory remembers, oldest first:
// what a person needs in order to find the run that was interrupted.
func ListState(stateDir string) ([]State, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("the state directory %s is unreadable: %w", stateDir, err)
	}
	states := []State{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		namespace := strings.TrimSuffix(entry.Name(), ".json")
		state, err := ReadState(stateDir, namespace)
		if err != nil {
			continue
		}
		states = append(states, state)
	}
	sort.Slice(states, func(one, other int) bool { return states[one].CreatedAt.Before(states[other].CreatedAt) })
	return states, nil
}
