package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// The daemon adapter of the environment (P22-T01).
//
// Everything the environment does to the machine goes through this interface,
// and it exists for one reason that is not abstraction for its own sake: the
// orchestration has to be provable without a daemon. The tests of this package
// drive the same lifecycle against a recorded client, so the parts that decide —
// which names, which addresses, which order, what is torn down and what is
// refused — are held by tests that run everywhere, while the live verification
// (tools/testenv/verify.sh) proves the same plan against a real Docker.
type dockerClient interface {
	Version(ctx context.Context) (string, error)
	NetworksCreate(ctx context.Context, plan Plan) error
	NetworksRemove(ctx context.Context, plan Plan) error
	NetworkIsInternal(ctx context.Context, network string) (bool, error)
	Connect(ctx context.Context, container, network string) error
	Start(ctx context.Context, spec ContainerSpec) error
	RunOnce(ctx context.Context, spec ContainerSpec) error
	HealthStatus(ctx context.Context, container string) (string, error)
	IsRunning(ctx context.Context, container string) (bool, error)
	Logs(ctx context.Context, container string, lines int) (string, error)
	Remove(ctx context.Context, container string) error
	List(ctx context.Context, namespace string) ([]string, error)
}

// Mount is one host path made readable inside a container.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

// PortMap publishes one container port on the loopback interface of the host.
// The host address is not configurable: an environment that published on
// 0.0.0.0 would expose a test database and a test application to the network
// the machine happens to be on.
type PortMap struct {
	Host      int
	Container string
}

// ContainerSpec is one container of the environment, as data.
type ContainerSpec struct {
	Name        string
	Image       string
	Entrypoint  string
	Args        []string
	Env         []string
	Mounts      []Mount
	Ports       []PortMap
	Tmpfs       []string
	Labels      map[string]string
	Network     string
	Healthcheck []string
}

// RunArgs renders the `docker run` invocation of one container. It is a
// function of the spec alone — no ambient state, no environment of the caller —
// because this is the place where a missing pin, a published-on-every-interface
// port or an inherited variable would go unnoticed.
func (spec ContainerSpec) RunArgs(detached bool) []string {
	args := []string{"run"}
	if detached {
		args = append(args, "--detach")
	} else {
		args = append(args, "--rm")
	}
	args = append(args, "--name", spec.Name)
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	labels := make([]string, 0, len(spec.Labels))
	for key := range spec.Labels {
		labels = append(labels, key+"="+spec.Labels[key])
	}
	sort.Strings(labels)
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	environment := append([]string(nil), spec.Env...)
	sort.Strings(environment)
	for _, variable := range environment {
		args = append(args, "--env", variable)
	}
	for _, mount := range spec.Mounts {
		value := mount.Host + ":" + mount.Container
		if mount.ReadOnly {
			value += ":ro"
		}
		args = append(args, "--volume", value)
	}
	for _, port := range spec.Ports {
		args = append(args, "--publish", fmt.Sprintf("127.0.0.1:%d:%s", port.Host, port.Container))
	}
	for _, path := range spec.Tmpfs {
		args = append(args, "--tmpfs", path)
	}
	if len(spec.Healthcheck) > 0 {
		args = append(args,
			"--health-cmd", strings.Join(spec.Healthcheck, " "),
			"--health-interval", "2s",
			"--health-retries", "30",
			"--health-start-period", "3s",
		)
	}
	if spec.Entrypoint != "" {
		args = append(args, "--entrypoint", spec.Entrypoint)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Args...)
	return args
}

// cliDocker runs the real client. Nothing here reads the caller's environment:
// the commands it builds carry what a container needs, and a `docker` that took
// the shell's configuration would be the first place a real credential could
// enter the environment.
type cliDocker struct {
	binary string
	stdout io.Writer
	stderr io.Writer
}

func newCLIDocker(stdout, stderr io.Writer) *cliDocker {
	return &cliDocker{binary: "docker", stdout: stdout, stderr: stderr}
}

// execute runs one docker command and returns its standard output.
func (d *cliDocker) execute(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, d.binary, args...)
	var out, errOut bytes.Buffer
	command.Stdout = &out
	command.Stderr = &errOut
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(errOut.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("docker %s: %s", strings.Join(args, " "), detail)
	}
	return strings.TrimSpace(out.String()), nil
}

// Version asks the daemon for its version. It is the first thing the command
// does: a machine without a daemon gets one sentence that says so instead of
// five failures that say something else.
func (d *cliDocker) Version(ctx context.Context) (string, error) {
	return d.execute(ctx, "version", "--format", "{{.Server.Version}}")
}

// NetworksCreate creates the two networks of the environment: the isolated one
// the services live on, and the ordinary one the door is published on. The
// second exists for one reason — Docker maps a published port to the host only
// on a network that is not internal — and only the door is ever attached to it.
func (d *cliDocker) NetworksCreate(ctx context.Context, plan Plan) error {
	if _, err := d.execute(ctx, "network", "create", networkInternalFlag,
		"--label", "arena.testenv.namespace="+plan.Namespace, plan.Network); err != nil {
		return err
	}
	_, err := d.execute(ctx, "network", "create",
		"--label", "arena.testenv.namespace="+plan.Namespace, plan.DoorNetwork)
	return err
}

// NetworksRemove takes both networks down, reporting the first refusal. A
// network that is already gone is the ordinary case of a second teardown, and
// the caller decides what to say about it.
func (d *cliDocker) NetworksRemove(ctx context.Context, plan Plan) error {
	var first error
	for _, network := range []string{plan.Network, plan.DoorNetwork} {
		if _, err := d.execute(ctx, "network", "rm", network); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Connect attaches a running container to a second network, which is how the
// door reaches the services of the isolated network.
func (d *cliDocker) Connect(ctx context.Context, container, network string) error {
	_, err := d.execute(ctx, "network", "connect", network, container)
	return err
}

func (d *cliDocker) NetworkIsInternal(ctx context.Context, network string) (bool, error) {
	value, err := d.execute(ctx, "network", "inspect", "--format", "{{.Internal}}", network)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

func (d *cliDocker) Start(ctx context.Context, spec ContainerSpec) error {
	_, err := d.execute(ctx, spec.RunArgs(true)...)
	return err
}

func (d *cliDocker) RunOnce(ctx context.Context, spec ContainerSpec) error {
	_, err := d.execute(ctx, spec.RunArgs(false)...)
	return err
}

func (d *cliDocker) HealthStatus(ctx context.Context, container string) (string, error) {
	return d.execute(ctx, "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", container)
}

func (d *cliDocker) IsRunning(ctx context.Context, container string) (bool, error) {
	value, err := d.execute(ctx, "inspect", "--format", "{{.State.Running}}", container)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

func (d *cliDocker) Logs(ctx context.Context, container string, lines int) (string, error) {
	return d.execute(ctx, "logs", "--tail", fmt.Sprint(lines), container)
}

func (d *cliDocker) Remove(ctx context.Context, container string) error {
	_, err := d.execute(ctx, "rm", "--force", "--volumes", container)
	return err
}

// List names the containers of one namespace. It asks the daemon by label, not
// by name: after an interruption the names are the only thing that was never
// written down anywhere else.
func (d *cliDocker) List(ctx context.Context, namespace string) ([]string, error) {
	args := append([]string{"ps", "--all", "--format", "{{.Names}}"}, labelFilters(namespace)...)
	output, err := d.execute(ctx, args...)
	if err != nil {
		return nil, err
	}
	if output == "" {
		return nil, nil
	}
	names := strings.Split(output, "\n")
	sort.Strings(names)
	return names, nil
}
