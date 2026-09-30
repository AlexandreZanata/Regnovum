#!/usr/bin/env bash
#
# verify.sh — the live verification of the disposable environment (P22-T01).
#
# The four validations of the phase are properties of a machine, not of a value:
# two namespaces that do not collide, an interruption that takes its resources
# with it, a port already taken that fails with a diagnosis, and a service that
# uses neither the internet nor a real credential. The package's own tests hold
# the decisions (which names, which order, what is torn down, what is refused)
# with a recorded daemon, because those are decisions and they have to be
# provable without one. This script runs the same orchestrator against a real
# daemon and measures the properties themselves.
#
# The measurement that matters most is the one that cannot be simulated: the
# services of the environment are attached to an internal network and to no
# other, an ordinary network carries only the environment's own forwarder, and a
# container inside the isolated plane cannot dial a public address. The positive
# control is what keeps that from being vacuous: the same probe *does* reach a
# service inside the plane, so "unreachable" is the network's answer and not the
# program's silence.
#
# Environment:
#   ARENA_TESTENV_ASSETS_DIR   the frontend build the application serves
#                              (default: web/dist; `make build-web` produces it)
#   ARENA_TESTENV_STATE_DIR    where the namespaces are recorded in this run
#   ARENA_TESTENV_KEEP         when set, the work directory is kept for inspection
set -euo pipefail

TOOL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$TOOL_DIR/../.." && pwd)"
cd "$ROOT"

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/arena-testenv-verify-XXXXXX")"
STATE_DIR="${ARENA_TESTENV_STATE_DIR:-$WORK_DIR/state}"
ASSETS_DIR="${ARENA_TESTENV_ASSETS_DIR:-$ROOT/web/dist}"
BIN="$WORK_DIR/arena-testenv"
RUN_ID="$$"
IMAGE="gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab"

# The namespaces this run creates, so that the teardown can find them even when
# the script dies in the middle: the state file is the record, and the record is
# what the command itself reads to take a namespace down.
NAMESPACES=()

log() { printf 'testenv-verify: %s\n' "$*" >&2; }
fail() { printf 'testenv-verify: %s\n' "$*" >&2; exit 1; }

require_command() {
	if ! command -v "$1" >/dev/null 2>&1; then
		fail "$1 is required and was not found in PATH"
	fi
}
require_command docker
require_command go
require_command curl
require_command python3

cleanup() {
	for namespace in "${NAMESPACES[@]:-}"; do
		[[ -n "$namespace" ]] || continue
		"$BIN" down -namespace "$namespace" -state "$STATE_DIR" >/dev/null 2>&1 || true
	done
	if [[ -n "${ARENA_TESTENV_KEEP:-}" ]]; then
		log "the work directory was kept at $WORK_DIR"
	else
		rm -rf "$WORK_DIR"
	fi
}
trap cleanup EXIT

# testenv runs the tool the way an operator does.
testenv() {
	"$BIN" "$@"
}

# namespace names one environment of this run, and remembers it for the teardown.
namespace() {
	printf 'verify-%s-%s' "$RUN_ID" "$1"
}

# created records a namespace so the teardown takes it down even if the script
# stops between `up` and the assertion that follows it.
created() {
	NAMESPACES+=("$1")
}

# containers_of names the containers of one namespace, by label, the way the
# command itself finds them after an interruption.
containers_of() {
	docker ps --all --format '{{.Names}}' --filter "label=arena.testenv.namespace=$1"
}

# networks_of reports the networks one container is attached to, space separated.
networks_of() {
	docker inspect --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}} {{end}}' "$1" | tr -s ' ' | sed 's/ $//'
}

# assert_empty proves that nothing of one namespace survives.
assert_empty() {
	local namespace="$1" detail="$2"
	local containers networks
	containers="$(containers_of "$namespace")"
	networks="$(docker network ls --format '{{.Name}}' | grep "^arena-testenv-$namespace" || true)"
	[[ -z "$containers" ]] || fail "$detail: the containers of $namespace are still there: $containers"
	[[ -z "$networks" ]] || fail "$detail: the networks of $namespace are still there: $networks"
	[[ ! -e "$STATE_DIR/$namespace.json" ]] || fail "$detail: the record of $namespace is still there"
	[[ ! -d "$STATE_DIR/$namespace" ]] || fail "$detail: the working directory of $namespace is still there"
}

# ---------------------------------------------------------------------------
# 0. The prerequisites, stated as refusals
# ---------------------------------------------------------------------------
log "building the tool and checking the daemon"
go build -o "$BIN" ./tools/testenv
docker version --format '{{.Server.Version}}' >/dev/null 2>&1 ||
	fail "the Docker daemon is required and was not reachable"
[[ -f "$ASSETS_DIR/manifest.json" ]] ||
	fail "no frontend build at $ASSETS_DIR (manifest.json is missing): run 'make build-web' or point ARENA_TESTENV_ASSETS_DIR at an existing build"
mkdir -p "$STATE_DIR"

# The credential the environment must never carry. It is shaped like a real one
# and it is exported in the shell that starts the environment, which is the only
# way a real credential could reach a container.
FAKE_CREDENTIAL="sk_live_this_is_not_a_real_key_$RUN_ID"
export ARENA_STRIPE_SECRET_KEY="$FAKE_CREDENTIAL"
export ARENA_DATABASE_URL="postgres://real:credential@db.invalid/production?sslmode=require"

# ---------------------------------------------------------------------------
# 1. The environment comes up, and the host reaches it through the door
# ---------------------------------------------------------------------------
PRIMARY="$(namespace main)"
created "$PRIMARY"
log "bringing up $PRIMARY"
testenv up -namespace "$PRIMARY" -state "$STATE_DIR" -assets "$ASSETS_DIR" -timeout 120s >/dev/null

STATE_FILE="$STATE_DIR/$PRIMARY.json"
[[ -f "$STATE_FILE" ]] || fail "the environment did not record itself in $STATE_FILE"
APP_URL="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["app_url"])' "$STATE_FILE")"
HOST_DSN="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["host_dsn"])' "$STATE_FILE")"
PROVIDERS_URL="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["providers_url"])' "$STATE_FILE")"
NETWORK="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["network"])' "$STATE_FILE")"
DOOR_NETWORK="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["door_network"])' "$STATE_FILE")"

curl -fsS --max-time 10 "$APP_URL/health/ready" >/dev/null ||
	fail "the application does not answer its readiness probe on $APP_URL through the door"
curl -fsS --max-time 10 "$PROVIDERS_URL/health/live" >/dev/null ||
	fail "the fake provider surface does not answer on $PROVIDERS_URL through the door"
log "the application and the provider surface answer on the host through the door"

# The environment file is what a test sources, and it must describe this
# environment: the same database, the same addresses, and nothing else.
ENVIRONMENT_FILE="$STATE_DIR/$PRIMARY/environment"
[[ -f "$ENVIRONMENT_FILE" ]] || fail "the environment file was not written at $ENVIRONMENT_FILE"
grep -q "^ARENA_DATABASE_URL=$HOST_DSN$" "$ENVIRONMENT_FILE" ||
	fail "the environment file does not publish the database of this run"
grep -q "^TESTENV_APP_URL=$APP_URL$" "$ENVIRONMENT_FILE" ||
	fail "the environment file does not publish the application of this run"
log "the environment file describes this run and not another one"

# ---------------------------------------------------------------------------
# 2. The plane: the services are isolated, and only the door is on both networks
# ---------------------------------------------------------------------------
for role in postgres app worker providers; do
	container="arena-testenv-$PRIMARY-$role"
	networks="$(networks_of "$container")"
	[[ "$networks" == "$NETWORK" ]] ||
		fail "the $role container is on '$networks' and the isolated network is '$NETWORK': a service of the environment is on the isolated network and nowhere else"
done
[[ "$(networks_of "arena-testenv-$PRIMARY-door")" == "$NETWORK $DOOR_NETWORK" ]] ||
	fail "the door is not on both networks, so nothing published reaches the isolated plane"
[[ "$(docker network inspect "$NETWORK" --format '{{.Internal}}')" == "true" ]] ||
	fail "the network $NETWORK is not internal: a container on it would have a route out"
[[ "$(docker network inspect "$DOOR_NETWORK" --format '{{.Internal}}')" == "false" ]] ||
	fail "the door network is internal, and Docker does not publish a port of a container on an internal network — the host could not reach the environment at all"
log "the four services are on $NETWORK alone; only the door is on $DOOR_NETWORK"

# ---------------------------------------------------------------------------
# 3. Isolation, with its positive control
# ---------------------------------------------------------------------------
testenv isolated -namespace "$PRIMARY" -state "$STATE_DIR" -address 1.1.1.1:443 >/dev/null ||
	fail "a container inside $NETWORK reached a public address: the environment is not isolated"
log "a container inside $NETWORK cannot reach 1.1.1.1:443"

# The positive control: the same probe, the same network, an address that is
# genuinely reachable from there. Without it, "unreachable" could just as well be
# a probe that never ran.
APP_CONTAINER="arena-testenv-$PRIMARY-app"
if docker run --rm --network "$NETWORK" --entrypoint /arena-testenv \
	--volume "$STATE_DIR/$PRIMARY/bin/arena-testenv:/arena-testenv:ro" "$IMAGE" \
	probe -address "$APP_CONTAINER:8080" -timeout 3s >/dev/null 2>&1; then
	fail "the probe reports $APP_CONTAINER:8080 as unreachable and the application answers there: the probe proves nothing"
fi
log "the same probe reaches $APP_CONTAINER:8080 inside $NETWORK, so the previous result is the network's answer"

# ---------------------------------------------------------------------------
# 4. No service carries a real credential, and none carries a name the product refuses
# ---------------------------------------------------------------------------
for role in postgres app worker providers migrate; do
	environment="$(docker inspect --format '{{json .Config.Env}}' "arena-testenv-$PRIMARY-$role" 2>/dev/null || echo '[]')"
	[[ "$environment" != *"$FAKE_CREDENTIAL"* ]] ||
		fail "the $role container received the credential exported in the shell that started the environment"
	[[ "$environment" != *"db.invalid"* ]] ||
		fail "the $role container received the database address exported in the shell"
done
log "no container received the credential or the database address of the shell"

# The application refuses an unknown ARENA_* variable to catch a typo instead of
# ignoring it, so the environment's own names live outside that namespace. This
# is the check that keeps a service from being handed a name it would refuse.
UNKNOWN="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "arena-testenv-$PRIMARY-app" |
	grep -E '^ARENA_' |
	cut -d= -f1 |
	grep -Ev '^(ARENA_ENV|ARENA_ADDR|ARENA_ASSETS_DIR|ARENA_CURSOR_SECRET|ARENA_DATABASE_URL|ARENA_LOG_LEVEL|ARENA_EMAIL_SINK_DIR)$' || true)"
[[ -z "$UNKNOWN" ]] ||
	fail "the application was given configuration the product does not accept, so it would refuse to boot: $UNKNOWN"
log "every ARENA_* variable the application received is one the loader accepts"

# The positive control of that check: the delivered binary really does refuse an
# unknown name, so the list above is a measurement and not a copy of a document.
if docker run --rm --entrypoint /arena \
	--volume "$STATE_DIR/$PRIMARY/bin/arena:/arena:ro" \
	--env ARENA_ENV=test \
	--env ARENA_DATABASE_URL="$HOST_DSN" \
	--env ARENA_TYPO_HERE=1 \
	"$IMAGE" server >/dev/null 2>&1; then
	fail "the application started with an unknown ARENA_* variable: the check above would accept anything"
fi
log "the application refuses an unknown ARENA_* variable, as the check assumes"

# ---------------------------------------------------------------------------
# 5. Two namespaces at the same time
# ---------------------------------------------------------------------------
SECOND="$(namespace second)"
created "$SECOND"
log "bringing up $SECOND beside $PRIMARY"
testenv up -namespace "$SECOND" -state "$STATE_DIR" -assets "$ASSETS_DIR" -timeout 120s >/dev/null

SECOND_STATE="$STATE_DIR/$SECOND.json"
for field in network app_url postgres_port; do
	first="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$STATE_FILE" "$field")"
	next="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$SECOND_STATE" "$field")"
	[[ "$first" != "$next" ]] || fail "the two namespaces share their $field: $first"
done
curl -fsS --max-time 10 "$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["app_url"])' "$SECOND_STATE")/health/ready" >/dev/null ||
	fail "the second environment does not answer while the first one is up"
log "two environments run at the same time without sharing a name, a port or a network"

# ---------------------------------------------------------------------------
# 6. A command is driven with the environment, and the teardown happens anyway
# ---------------------------------------------------------------------------
THIRD="$(namespace run)"
created "$THIRD"
log "driving a green command in $THIRD"
OUTPUT="$(testenv run -namespace "$THIRD" -state "$STATE_DIR" -assets "$ASSETS_DIR" -timeout 120s -- \
	sh -c 'printf "%s\n" "$TESTENV_NAMESPACE"; curl -fsS --max-time 10 "$TESTENV_APP_URL/health/ready"')"
[[ "${OUTPUT%%$'\n'*}" == "$THIRD" ]] ||
	fail "the command did not receive the environment of the run: it printed '$OUTPUT'"
[[ "$OUTPUT" == *ready* ]] ||
	fail "the command could not reach the application through the door: it printed '$OUTPUT'"
assert_empty "$THIRD" "after a green command"
log "the command saw TESTENV_NAMESPACE=$THIRD and reached the application; nothing was left behind"

FOURTH="$(namespace red)"
created "$FOURTH"
log "driving a red command in $FOURTH"
set +e
testenv run -namespace "$FOURTH" -state "$STATE_DIR" -assets "$ASSETS_DIR" -timeout 120s -- sh -c 'exit 7' >/dev/null
red_status=$?
set -e
[[ "$red_status" == "7" ]] ||
	fail "a command that exits 7 was reported as $red_status: the status of the command is the status of the run"
assert_empty "$FOURTH" "after a red command"
log "the red command kept its status and left nothing behind"

FIFTH="$(namespace interrupt)"
created "$FIFTH"
log "interrupting a run in $FIFTH"
testenv run -namespace "$FIFTH" -state "$STATE_DIR" -assets "$ASSETS_DIR" -timeout 120s -- sh -c 'sleep 300' >/dev/null 2>&1 &
runner=$!
# Wait for the environment to be up before interrupting it: the point is to
# interrupt a run that has resources, not one that has not started.
for _ in $(seq 1 60); do
	[[ -n "$(containers_of "$FIFTH")" ]] && break
	sleep 1
done
[[ -n "$(containers_of "$FIFTH")" ]] ||
	fail "the interrupted run never created a container, so there is nothing to prove"
kill -INT "$runner" 2>/dev/null || true
wait "$runner" 2>/dev/null || true
assert_empty "$FIFTH" "after an interrupt"
log "an interrupted run took its containers, its networks, its files and its record with it"

# ---------------------------------------------------------------------------
# 7. A port already taken fails with a diagnosis
# ---------------------------------------------------------------------------
log "asking for a port that is already taken"
python3 - "$WORK_DIR" <<'PY' &
import socket, sys, time
sock = socket.socket()
sock.bind(("127.0.0.1", 0))
sock.listen(1)
open(sys.argv[1] + "/taken-port", "w").write(str(sock.getsockname()[1]))
time.sleep(30)
PY
holder=$!
for _ in $(seq 1 30); do
	[[ -s "$WORK_DIR/taken-port" ]] && break
	sleep 0.2
done
TAKEN="$(cat "$WORK_DIR/taken-port" 2>/dev/null || true)"
[[ -n "$TAKEN" ]] || fail "the test could not occupy a port"
set +e
diagnosis="$(testenv up -namespace "$(namespace taken)" -state "$STATE_DIR" -assets "$ASSETS_DIR" -port "$TAKEN" 2>&1)"
status=$?
set -e
kill "$holder" 2>/dev/null || true
wait "$holder" 2>/dev/null || true
[[ "$status" != "0" ]] || fail "the environment came up on the port $TAKEN, which is taken"
for expected in "$TAKEN" "already taken" "lsof"; do
	[[ "$diagnosis" == *"$expected"* ]] || fail "the refusal does not mention '$expected': $diagnosis"
done
log "a taken port is refused with the port and the way to look at it"

# ---------------------------------------------------------------------------
# 8. The teardown leaves nothing at all
# ---------------------------------------------------------------------------
log "taking the namespaces down"
for namespace_name in "$PRIMARY" "$SECOND"; do
	testenv down -namespace "$namespace_name" -state "$STATE_DIR" >/dev/null
	assert_empty "$namespace_name" "after the teardown"
done
# The state directory itself, not only the namespaces this script remembered:
# what is left on the machine is what the record still names. The check is
# scoped to the directory of this run so that a second environment running
# beside it is not mistaken for a leftover.
remaining="$(ls -A "$STATE_DIR" 2>/dev/null || true)"
[[ -z "$remaining" ]] || fail "the state directory of this run still holds: $remaining"
gone="$(docker ps --all --format '{{.Names}}' | grep '^arena-testenv-verify-' || true)"
[[ -z "$gone" ]] || fail "containers of this run survived: $gone"
log "no container, no network and no file of this run survives"

log "PASS — the environment is hermetic, namespaced, reachable through the door and gone when it is done"
