#!/usr/bin/env bash
#
# verify.sh — the disaster-and-load drill of the release (P20-T05).
#
# The phase asks for a specific set of things to have *happened*, not to have been
# reasoned about: a backup restored in an isolated environment, the application
# brought up on what came back, the financial integrity checked, RPO and RTO
# measured, a load baseline registered without a critical error, and the email and
# payment providers simulated as unavailable. This script makes them happen and
# measures them; `drillaudit` judges what was measured and refuses the numbers
# that exceed the declared ceilings. The operator entry point is
# `make disaster-drill`.
#
# What it deliberately reuses: the deployment's own Compose file for the server's
# arguments, the operator scripts (`deploy/backup/base-backup.sh`,
# `deploy/backup/restore.sh`) for the backup and the point-in-time recovery, the
# application's own migration runner and seeder, the versioned k6 workload and the
# versioned browser harness. Nothing here has a private path that an incident
# would not have.
#
# Environment:
#   ARENA_IMAGE             unused by the drill (it runs the binaries it builds)
#   ARENA_DRILL_REPORT      where the report is written (default docs/DISASTER_DRILL.md)
#   ARENA_DRILL_KEEP        when set, the work directory is kept for inspection
#   ARENA_BACKUP_S3_IMAGE   the S3-compatible store to run (default: the MinIO
#                           digest, pulled from quay.io, where MinIO publishes it)
set -euo pipefail

TOOL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$TOOL_DIR/../.." && pwd)"
cd "$ROOT"

COMPOSE_FILE="compose.production.yaml"
PROJECT="arena-drill-$$"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/arena-drill-XXXXXX")"
NETWORK="${PROJECT}-net"
S3_IMAGE="${ARENA_BACKUP_S3_IMAGE:-quay.io/minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e}"
BUCKET="arena-backups"
PREFIX="arena"
S3_USER="arena-backup"
S3_PASSWORD="drill-store-secret"
DB_USER="arena"
DB_PASSWORD="drill-db-password"
DB_NAME="arena"
REPORT="${ARENA_DRILL_REPORT:-docs/DISASTER_DRILL.md}"
ASSETS_DIR="${ARENA_ASSETS_DIR:-$ROOT/web/dist}"
RPO_TARGET_SECONDS=900
RTO_TARGET_SECONDS=14400

log() { printf 'disaster-drill: %s\n' "$*" >&2; }
fail() { printf 'disaster-drill: %s\n' "$*" >&2; exit 1; }

cleanup() {
	# Every path out of this script removes what it started, and none of these
	# steps may change the status the script is leaving with: by the time the
	# trap runs the verdict is already decided.
	for name in "$PROJECT-primary" "$PROJECT-restored" "$PROJECT-s3"; do
		docker rm --force "$name" >/dev/null 2>&1 || true
	done
	docker network rm "$NETWORK" >/dev/null 2>&1 || true
	if [[ -n "${SERVER_PID:-}" ]]; then kill "$SERVER_PID" >/dev/null 2>&1 || true; fi
	if [[ -n "${PROD_SERVER_PID:-}" ]]; then kill "$PROD_SERVER_PID" >/dev/null 2>&1 || true; fi
	if [[ -n "${VENDOR_PID:-}" ]]; then kill "$VENDOR_PID" >/dev/null 2>&1 || true; fi
	# The data directories are written by root inside the containers, so the host
	# user cannot unlink what is inside them; one last container with the image
	# that is already local removes them. That wipe is skipped when the exercise
	# is asked to keep its work directory: an inspection that found nothing would
	# not be an inspection.
	if [[ -n "${ARENA_DRILL_KEEP:-}" ]]; then
		printf 'disaster-drill: the work directory is %s\n' "$WORK_DIR" >&2
	elif [[ -n "${POSTGRES_IMAGE:-}" ]]; then
		docker run --rm --mount "type=bind,src=${WORK_DIR},dst=/cleanup" "$POSTGRES_IMAGE" \
			sh -c 'rm -rf /cleanup/* /cleanup/.[!.]* /cleanup/..?*' >/dev/null 2>&1 || true
		rm -rf "$WORK_DIR" >/dev/null 2>&1 || true
	fi
	true
}
trap cleanup EXIT

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required and was not found in PATH"
}
require_command docker
require_command go
require_command curl
require_command jq
require_command k6
require_command node

# ---------------------------------------------------------------------------
# 1. The binaries, and the topology's own judgement of itself
# ---------------------------------------------------------------------------
log "building the application, the seeder, the backup tool, the drill and the provider stand-in"
mkdir -p "$WORK_DIR/bin"
(cd "$ROOT" && go build -o "$WORK_DIR/bin/arena" ./cmd/arena) || fail "the application did not build"
(cd "$ROOT" && go build -o "$WORK_DIR/bin/e2e-seed" ./tools/e2e/seed) || fail "the seeder did not build"
(cd "$ROOT" && go build -o "$WORK_DIR/bin/backupctl" ./tools/backupctl) || fail "the backup tool did not build"
(cd "$ROOT" && go build -o "$WORK_DIR/bin/drillaudit" ./tools/drillaudit) || fail "the drill tool did not build"
(cd "$ROOT" && go build -o "$WORK_DIR/bin/vendorsim" ./tools/drillaudit/vendorsim) || fail "the provider stand-in did not build"

[[ -f "$ASSETS_DIR/manifest.json" ]] ||
	fail "no frontend build was found at $ASSETS_DIR (manifest.json is missing): run 'make build-web' first, because the application serves the assets it references"

# The server the exercise starts is the server the deployment declares: its
# archive settings come from the committed Compose file and are checked first,
# because a primary that recycles WAL unwritten makes every assertion below green
# while the store quietly stops receiving anything.
if ! "$WORK_DIR/bin/backupctl" check-compose "$COMPOSE_FILE" 2>"$WORK_DIR/compose.err"; then
	tail -10 "$WORK_DIR/compose.err" >&2
	fail "the committed Compose file would not archive"
fi
mapfile -t DB_COMMAND < <("$WORK_DIR/bin/backupctl" print-compose-command "$COMPOSE_FILE")
[[ "${#DB_COMMAND[@]}" -gt 1 ]] || fail "the Compose file declares no command for the db service"
POSTGRES_IMAGE="$(sed -nE 's/^[[:space:]]*image:[[:space:]]+(postgres:[^[:space:]]+).*/\1/p' "$COMPOSE_FILE" | head -1)"
[[ -n "$POSTGRES_IMAGE" ]] || fail "compose.production.yaml pins no PostgreSQL image"
log "running ${POSTGRES_IMAGE} with the ${#DB_COMMAND[@]} arguments the deployment declares"

# ---------------------------------------------------------------------------
# 2. A store, a key, and the environment the backup scripts read
# ---------------------------------------------------------------------------
docker network create "$NETWORK" >/dev/null || fail "the throwaway network was not created"

mkdir -p "$WORK_DIR/s3-data/$BUCKET"
chmod -R 0777 "$WORK_DIR/s3-data"
docker run --detach --name "${PROJECT}-s3" --network "$NETWORK" --network-alias minio \
	--publish 127.0.0.1::9000 \
	--env "MINIO_ROOT_USER=${S3_USER}" --env "MINIO_ROOT_PASSWORD=${S3_PASSWORD}" \
	--mount "type=bind,src=${WORK_DIR}/s3-data,dst=/data" \
	"$S3_IMAGE" server /data >/dev/null || fail "the store did not start"

S3_PORT=""
deadline=$((SECONDS + 60))
while (( SECONDS < deadline )); do
	S3_PORT="$(docker port "${PROJECT}-s3" 9000/tcp 2>/dev/null | head -1 | sed 's/.*://' || true)"
	if [[ -n "$S3_PORT" ]] && curl --silent --output /dev/null --max-time 2 "http://127.0.0.1:${S3_PORT}/minio/health/live"; then
		break
	fi
	S3_PORT=""
	sleep 1
done
[[ -n "$S3_PORT" ]] || fail "the store never answered on its health endpoint"
log "the store answers on 127.0.0.1:${S3_PORT}"

"$WORK_DIR/bin/backupctl" keygen "$WORK_DIR/backup.key" >/dev/null || fail "the sealing key was not generated"
chmod 0600 "$WORK_DIR/backup.key"
# The key the *server* reads is its own file with the same bytes: the
# archive_command runs as the postgres user inside the container, and that user
# cannot read a file the operator owns. Widening the mode would make the key
# readable by every process, which is not a way to share it.
mkdir -p "$WORK_DIR/server-key"
docker run --rm --mount "type=bind,src=${WORK_DIR},dst=/work" "$POSTGRES_IMAGE" \
	sh -c 'install -o postgres -g postgres -m 0600 /work/backup.key /work/server-key/backup.key' ||
	fail "the postgres user's copy of the sealing key was not prepared"

cat >"$WORK_DIR/backup.env" <<EOF
BACKUP_S3_ENDPOINT=http://minio:9000
BACKUP_S3_BUCKET=${BUCKET}
BACKUP_S3_ACCESS_KEY=${S3_USER}
BACKUP_S3_SECRET_KEY=${S3_PASSWORD}
BACKUP_S3_PREFIX=${PREFIX}
BACKUP_ENCRYPTION_KEY_FILE=/run/secrets/backup_key
BACKUP_BIN=/opt/backup-tool/backupctl
BACKUP_LOCK_DIR=/tmp
PGUSER=${DB_USER}
EOF
chmod 0600 "$WORK_DIR/backup.env"

mounts=(
	--mount "type=bind,src=${ROOT}/deploy/backup,dst=/opt/backup,readonly"
	--mount "type=bind,src=${WORK_DIR}/bin/backupctl,dst=/opt/backup-tool/backupctl,readonly"
	--mount "type=bind,src=${WORK_DIR}/server-key/backup.key,dst=/run/secrets/backup_key,readonly"
)
container_env=(--env-file "$WORK_DIR/backup.env")

host_ctl() {
	env BACKUP_S3_ENDPOINT="http://127.0.0.1:${S3_PORT}" BACKUP_S3_BUCKET="$BUCKET" \
		BACKUP_S3_ACCESS_KEY="$S3_USER" BACKUP_S3_SECRET_KEY="$S3_PASSWORD" BACKUP_S3_PREFIX="$PREFIX" \
		BACKUP_ENCRYPTION_KEY_FILE="$WORK_DIR/backup.key" "$WORK_DIR/bin/backupctl" "$@"
}

sql() {
	docker exec "$1" psql --no-psqlrc --tuples-only --no-align --username "$DB_USER" --dbname "$DB_NAME" -c "$2"
}

psql_script() {
	docker exec -i "$1" psql --no-psqlrc --quiet --username "$DB_USER" --dbname "$DB_NAME"
}

wait_ready() {
	local name="$1"
	local deadline=$((SECONDS + 180))
	while (( SECONDS < deadline )); do
		if docker exec "$name" pg_isready --quiet --username "$DB_USER" --dbname "$DB_NAME" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	docker logs "$name" 2>&1 | tail -20 >&2 || true
	fail "$name never became ready"
}

# scrubbed runs one application command with an environment this script composed:
# the configuration is strict about unknown ARENA_* variables, and inheriting the
# caller's shell would make the drill depend on who started it.
scrubbed() {
	env -i PATH="$PATH" HOME="${HOME:-$WORK_DIR}" TMPDIR="${TMPDIR:-/tmp}" "$@"
}

# ---------------------------------------------------------------------------
# 3. A primary that archives, migrated, holding a synthetic dataset
# ---------------------------------------------------------------------------
start_primary() {
	mkdir -p "$WORK_DIR/primary-data"
	chmod 0777 "$WORK_DIR/primary-data"
	docker run --detach --name "${PROJECT}-primary" --network "$NETWORK" \
		--publish 127.0.0.1::5432 \
		--env "POSTGRES_PASSWORD=${DB_PASSWORD}" --env "POSTGRES_USER=${DB_USER}" --env "POSTGRES_DB=${DB_NAME}" \
		--env "POSTGRES_INITDB_ARGS=--encoding=UTF8 --locale=C" \
		"${container_env[@]}" "${mounts[@]}" \
		--mount "type=bind,src=${WORK_DIR}/primary-data,dst=/var/lib/postgresql" \
		"$POSTGRES_IMAGE" "${DB_COMMAND[@]}" >/dev/null
}

start_primary
wait_ready "${PROJECT}-primary"
PG_PORT="$(docker port "${PROJECT}-primary" 5432/tcp | head -1 | sed 's/.*://')"
DSN="postgres://${DB_USER}:${DB_PASSWORD}@127.0.0.1:${PG_PORT}/${DB_NAME}?sslmode=disable"
log "the primary is up on 127.0.0.1:${PG_PORT} with the deployment's archive settings"

if ! scrubbed ARENA_ENV=test ARENA_DATABASE_URL="$DSN" "$WORK_DIR/bin/arena" migrate up >"$WORK_DIR/migrate.out" 2>"$WORK_DIR/migrate.err"; then
	tail -20 "$WORK_DIR/migrate.err" >&2 || true
	fail "the migrations did not apply through the application's own runner"
fi
log "the migrations applied through the application's own runner"

# The dataset is synthetic and it is created through the product's own use cases:
# the seeder registers an account, confirms it and credits INK through the wallet
# case, so the ledger the drill compares is a ledger the product wrote.
SEED="drill-$$"
PARTICIPANT_EMAIL="drill-participant-${SEED}@example.test"
PARTNER_EMAIL="drill-partner-${SEED}@example.test"
LOAD_EMAIL="drill-load-${SEED}@example.test"
PASSWORD="correct horse battery staple"
ARENA_SLUG="drill-arena-${SEED}"
DATASET_INK=1000

seed_account() {
	scrubbed ARENA_ENV=test ARENA_DATABASE_URL="$DSN" "$WORK_DIR/bin/e2e-seed" account \
		--email "$1" --password "$PASSWORD" --ink "$2"
}
seed_account "$PARTICIPANT_EMAIL" "$DATASET_INK" >/dev/null
seed_account "$PARTNER_EMAIL" "$DATASET_INK" >/dev/null
seed_account "$LOAD_EMAIL" "$DATASET_INK" >/dev/null
ARENA_DOCUMENT="$(scrubbed ARENA_ENV=test ARENA_DATABASE_URL="$DSN" "$WORK_DIR/bin/e2e-seed" arena \
	--slug "$ARENA_SLUG" --creator-email "$PARTICIPANT_EMAIL")"
ARENA_ID="$(printf '%s' "$ARENA_DOCUMENT" | jq -r '.arena_id // .id // empty')"
[[ -n "$ARENA_ID" ]] || fail "the seeder named no arena id, so the load workload has nothing to read"
log "the dataset is seeded: 3 accounts with ${DATASET_INK} INK each and one published Arena"

# ---------------------------------------------------------------------------
# 4. Continuous archiving, and a base backup sealed into the store
# ---------------------------------------------------------------------------
archived=""
deadline=$((SECONDS + 180))
while (( SECONDS < deadline )); do
	failed="$(sql "${PROJECT}-primary" 'SELECT failed_count FROM pg_stat_archiver')"
	if [[ "$failed" != "0" ]]; then
		sql "${PROJECT}-primary" 'SELECT last_failed_wal, last_failed_time FROM pg_stat_archiver' >&2 || true
		fail "the server failed to archive a segment ${failed} time(s): a broken archive is the failure that hides behind a running database"
	fi
	archived="$(sql "${PROJECT}-primary" 'SELECT archived_count FROM pg_stat_archiver')"
	if [[ -n "$archived" && "$archived" -ge 1 ]]; then break; fi
	sql "${PROJECT}-primary" 'SELECT pg_switch_wal()' >/dev/null
	sleep 2
done
[[ "${archived:-0}" -ge 1 ]] || fail "no WAL segment reached the archive: archive_mode is on and nothing is being shipped"
log "the archive received ${archived} segment(s) with no failure"

BASE_LABEL="drill-$(date -u +%Y-%m-%dT%H-%M-%SZ)"
docker exec "${PROJECT}-primary" /opt/backup/base-backup.sh --label "$BASE_LABEL" \
	>"$WORK_DIR/base.out" 2>"$WORK_DIR/base.err" ||
	{ tail -20 "$WORK_DIR/base.err" >&2; fail "the base backup failed"; }
host_ctl list base/ | grep -q "${BASE_LABEL}.tar.gz.enc" || fail "the base backup tar is not in the store"
log "the base backup ${BASE_LABEL} is sealed in the store under the operator script"

# The financial state before the loss. It is read *after* the base backup so that
# the newest commit that has to come back is as close to the target as the
# exercise can make it: the observed RPO is then the recovery point of the
# archive and not the length of the setup.
"$WORK_DIR/bin/drillaudit" snapshot -dsn "$DSN" -out "$WORK_DIR/baseline.json" >"$WORK_DIR/baseline.out" 2>&1 ||
	{ cat "$WORK_DIR/baseline.out" >&2; fail "the baseline reading could not be taken"; }
log "$(tail -1 "$WORK_DIR/baseline.out")"
BASELINE_AT="$(jq -r '.ledger.captured_at' "$WORK_DIR/baseline.json")"

# ---------------------------------------------------------------------------
# 5. The target, and the rows the restore must not bring back
# ---------------------------------------------------------------------------
sleep 1
TARGET_TIME="$(sql "${PROJECT}-primary" "SELECT to_char(now(), 'YYYY-MM-DD HH24:MI:SS.US')")"
sleep 1
# Two kinds of writing happen after the target, and both are things a recovery
# would be wrong to invent: a queue row, and one whole account with INK in it.
for sequence in 1 2; do
	psql_script "${PROJECT}-primary" <<SQL >/dev/null
INSERT INTO app.jobs (type, version, parameters, idempotency_key, available_at)
VALUES ('retention_run', 1, jsonb_build_object('marker', 'drill-after-target', 'sequence', ${sequence}), 'drill-after-${sequence}', now());
SQL
done
AFTER_TARGET_EMAIL="drill-after-target-${SEED}@example.test"
seed_account "$AFTER_TARGET_EMAIL" 500 >/dev/null
sql "${PROJECT}-primary" 'SELECT pg_switch_wal()' >/dev/null
jobs_after_target="$(sql "${PROJECT}-primary" "SELECT count(*) FROM app.jobs WHERE created_at > '${TARGET_TIME}'")"
accounts_after_target="$(sql "${PROJECT}-primary" "SELECT count(*) FROM app.accounts WHERE email = '${AFTER_TARGET_EMAIL}'")"
ink_after_target="$(sql "${PROJECT}-primary" "SELECT COALESCE(sum(t.amount), 0) FROM app.wallet_transactions t JOIN app.wallet_operations o ON o.id = t.operation_id JOIN app.accounts a ON a.id = o.account_id WHERE a.email = '${AFTER_TARGET_EMAIL}'")"
[[ "$accounts_after_target" == "1" && "$ink_after_target" == "500" ]] ||
	fail "the exercise wrote an account after the target and the primary does not hold it: the boundary below would be asserted against nothing"
log "the target is ${TARGET_TIME}: ${jobs_after_target} job(s) and one account with ${ink_after_target} INK were committed after it"

# ---------------------------------------------------------------------------
# 6. The loss, and the point-in-time recovery of the deployment's own script
# ---------------------------------------------------------------------------
DISASTER_EPOCH="$(sql "${PROJECT}-primary" 'SELECT floor(extract(epoch FROM now()))::bigint')"
DISASTER_AT="$(sql "${PROJECT}-primary" "SELECT to_char(now(), 'YYYY-MM-DD HH24:MI:SS.US')")"
restore_started=$SECONDS
docker rm --force "${PROJECT}-primary" >/dev/null || fail "the primary could not be removed"
docker run --rm --mount "type=bind,src=${WORK_DIR},dst=/cleanup" "$POSTGRES_IMAGE" \
	rm -rf /cleanup/primary-data || fail "the primary's data directory could not be destroyed"
log "the primary and its data directory are gone: what remains is the store"

mkdir -p "$WORK_DIR/restored"
chmod 0700 "$WORK_DIR/restored"
docker run --detach --name "${PROJECT}-restored" --network "$NETWORK" \
	--publish 127.0.0.1::5432 \
	"${container_env[@]}" "${mounts[@]}" \
	--mount "type=bind,src=${WORK_DIR}/restored,dst=/var/lib/postgresql/restored" \
	"$POSTGRES_IMAGE" sleep infinity >/dev/null || fail "the restore container did not start"

docker exec "${PROJECT}-restored" /opt/backup/restore.sh \
	--pgdata /var/lib/postgresql/restored \
	--base "$BASE_LABEL" \
	--target-time "$TARGET_TIME" \
	--superuser "$DB_USER" \
	--start --wait-promotion --timeout 300 >"$WORK_DIR/restore.out" 2>"$WORK_DIR/restore.err" || {
	tail -30 "$WORK_DIR/restore.err" >&2
	fail "the restore did not reach its target"
}
wait_ready "${PROJECT}-restored"
RTO_WRITABLE=$((SECONDS - restore_started))
PG_PORT_RESTORED="$(docker port "${PROJECT}-restored" 5432/tcp | head -1 | sed 's/.*://')"
RESTORED_DSN="postgres://${DB_USER}:${DB_PASSWORD}@127.0.0.1:${PG_PORT_RESTORED}/${DB_NAME}?sslmode=disable"
in_recovery="$(sql "${PROJECT}-restored" 'SELECT pg_is_in_recovery()')"
[[ "$in_recovery" == "f" ]] || fail "the restored server is still in recovery: a restore that has not promoted is a database nobody can write to"
log "the restored server answers on 127.0.0.1:${PG_PORT_RESTORED} after ${RTO_WRITABLE}s"

# ---------------------------------------------------------------------------
# 7. The financial assertion: what came back, and what did not
# ---------------------------------------------------------------------------
if ! "$WORK_DIR/bin/drillaudit" verify -dsn "$RESTORED_DSN" -baseline "$WORK_DIR/baseline.json" \
	-out "$WORK_DIR/comparison.json" >"$WORK_DIR/verify.out" 2>&1; then
	cat "$WORK_DIR/verify.out" >&2
	fail "the restored ledger is not the ledger that was there: the comparison refused it"
fi
log "$(tail -1 "$WORK_DIR/verify.out")"

restored_after_target_account="$(sql "${PROJECT}-restored" "SELECT count(*) FROM app.accounts WHERE email = '${AFTER_TARGET_EMAIL}'")"
restored_after_target_ink="$(sql "${PROJECT}-restored" "SELECT COALESCE(sum(t.amount), 0) FROM app.wallet_transactions t JOIN app.wallet_operations o ON o.id = t.operation_id JOIN app.accounts a ON a.id = o.account_id WHERE a.email = '${AFTER_TARGET_EMAIL}'")"
restored_jobs_after_target="$(sql "${PROJECT}-restored" "SELECT count(*) FROM app.jobs WHERE created_at > '${TARGET_TIME}'")"
[[ "$restored_after_target_ink" == "0" ]] ||
	fail "the restored cluster holds ${restored_after_target_ink} INK that was credited after the target"
ROWS_AFTER_TARGET=$((restored_after_target_account + restored_jobs_after_target))
migrations_restored="$(sql "${PROJECT}-restored" 'SELECT count(*) FROM app.schema_metadata WHERE is_applied')"
[[ "$migrations_restored" -ge 1 ]] || fail "the restored cluster records no applied migration: the schema did not come back with the rows"
log "the restored cluster holds ${migrations_restored} applied migration(s), none of the ${jobs_after_target} job(s) written after the target, and not the account credited after it"

# RPO is what could have been lost: the distance between the newest commit that
# came back and the instant the primary was destroyed, both read from a cluster's
# own clock.
NEWEST_RECOVERED_EPOCH="$(sql "${PROJECT}-restored" 'SELECT floor(extract(epoch FROM max(created_at)))::bigint FROM app.wallet_transactions')"
NEWEST_RECOVERED_AT="$(sql "${PROJECT}-restored" "SELECT to_char(max(created_at), 'YYYY-MM-DD HH24:MI:SS.US') FROM app.wallet_transactions")"
[[ -n "$NEWEST_RECOVERED_EPOCH" && "$NEWEST_RECOVERED_EPOCH" != "" ]] ||
	fail "the restored cluster holds no ledger row, so the recovery point cannot be stated"
RPO_OBSERVED=$((DISASTER_EPOCH - NEWEST_RECOVERED_EPOCH))
ARCHIVE_BOUND="$(jq -r '.ledger.archive_timeout_seconds' "$WORK_DIR/baseline.json")"
ARCHIVE_FAILED="$(jq -r '.archive_failed' "$WORK_DIR/baseline.json")"

# ---------------------------------------------------------------------------
# 8. The application, brought up on what came back
# ---------------------------------------------------------------------------
SERVER_PORT="$(node -e 'const s=require("net").createServer();s.listen(0,"127.0.0.1",()=>{process.stdout.write(String(s.address().port));s.close()})')"
BASE_URL="http://127.0.0.1:${SERVER_PORT}"
SINK_DIR="$WORK_DIR/email-sink"
mkdir -p "$SINK_DIR"
CURSOR_SECRET="$(node -e 'process.stdout.write(require("node:crypto").randomBytes(48).toString("hex"))')"
SERVER_LOG="$WORK_DIR/server.log"

app_started=$SECONDS
scrubbed ARENA_ENV=test \
	ARENA_ADDR="127.0.0.1:${SERVER_PORT}" \
	ARENA_DATABASE_URL="$RESTORED_DSN" \
	ARENA_ASSETS_DIR="$ASSETS_DIR" \
	ARENA_EMAIL_SINK_DIR="$SINK_DIR" \
	ARENA_CURSOR_SECRET="$CURSOR_SECRET" \
	ARENA_LOG_LEVEL=info \
	"$WORK_DIR/bin/arena" server >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

ready=0
for _ in $(seq 1 300); do
	if ! kill -0 "$SERVER_PID" 2>/dev/null; then
		tail -n 40 "$SERVER_LOG" >&2
		fail "the application exited before it reported itself ready on the restored data"
	fi
	if curl --silent --fail --max-time 2 "$BASE_URL/health/ready" >/dev/null; then
		ready=1
		break
	fi
	sleep 0.2
done
[[ "$ready" -eq 1 ]] || { tail -n 40 "$SERVER_LOG" >&2; fail "the application did not become ready on the restored data"; }
RTO_APP=$((SECONDS - restore_started))
log "the application is serving the restored data on ${BASE_URL} after ${RTO_APP}s from the loss"

smoke=()
probe() {
	local what="$1" method="$2" path="$3" want="$4"
	local status
	status="$(curl --silent --output "$WORK_DIR/probe.out" --write-out '%{http_code}' --max-time 10 \
		-X "$method" "${BASE_URL}${path}")"
	smoke+=("$(jq -nc --arg what "$what" --arg method "$method" --arg path "$path" \
		--argjson status "$status" --argjson want "$want" \
		'{what:$what, method:$method, path:$path, status:$status, want:$want}')")
	[[ "$status" == "$want" ]] || fail "the restored instance answered ${status} on ${method} ${path} and the drill asked for ${want}"
	printf 'disaster-drill: %s %s %s → %s\n' "$method" "$path" "${what}" "$status" >&2
}
probe "readiness on the restored data" GET "/health/ready" 200
probe "the sign-in page" GET "/login" 200
probe "the participation page of the Arena" GET "/arenas/${ARENA_SLUG}" 200

# The pages are not enough on their own: an account that came back has to be able
# to authenticate against the restored cluster. It signs in through the product's
# own form — the double-submit token included — and a wrong password is driven
# right after it, because "the session was opened" only means something next to
# "and it is not opened for a credential the drill made up".
COOKIE_JAR="$WORK_DIR/session.jar"
html_login() {
	local email="$1" password="$2" jar="$3"
	curl --silent --output /dev/null -c "$jar" "${BASE_URL}/login"
	# The jar is the Netscape format: domain, flag, path, secure, expiry, name,
	# value. The name is read from the file instead of being assumed, so a renamed
	# cookie cannot make the drill drive a form that never receives the token.
	local token
	token="$(awk 'NF >= 7 {print $7}' "$jar" | tail -1)"
	[[ -n "$token" ]] || { cp "$jar" "$WORK_DIR/failed.jar" 2>/dev/null || true; fail "the sign-in form issued no CSRF cookie"; }
	curl --silent --output "$WORK_DIR/login-body.html" -D "$WORK_DIR/login.headers" -c "$jar" -b "$jar" \
		--write-out '%{http_code}' --max-time 10 \
		-H "X-CSRF-Token: ${token}" -H "Origin: ${BASE_URL}" \
		--data-urlencode "csrf_token=${token}" \
		--data-urlencode "email=${email}" \
		--data-urlencode "password=${password}" \
		"${BASE_URL}/login"
}
LOGIN_STATUS="$(html_login "$PARTICIPANT_EMAIL" "$PASSWORD" "$COOKIE_JAR")"
# A successful browser sign-in answers with the redirect the form documents
# (303): the page a person lands on is read next, not the answer to the POST.
smoke+=("$(jq -nc --argjson status "$LOGIN_STATUS" '{what:"the participant signs in on the restored data", method:"POST", path:"/login", status:$status, want:303}')")
[[ "$LOGIN_STATUS" == "303" ]] || fail "the participant could not sign in on the restored data: ${LOGIN_STATUS}"
grep -qi '^set-cookie: arena_session=' "$WORK_DIR/login.headers" ||
	fail "the sign-in answered ${LOGIN_STATUS} with the right password and opened no session: the restored account authenticated into nothing"
WRONG_STATUS="$(html_login "$PARTICIPANT_EMAIL" "not-the-password" "$WORK_DIR/wrong.jar")"
if grep -qi '^set-cookie: arena_session=' "$WORK_DIR/login.headers" 2>/dev/null; then
	fail "a wrong password opened a session"
fi
# The refusal is a status of its own, which is what makes the assertion above a
# statement about the credential and not about the form answering at all.
smoke+=("$(jq -nc --argjson status "$WRONG_STATUS" '{what:"a wrong password is refused", method:"POST", path:"/login", status:$status, want:401}')")
[[ "$WRONG_STATUS" == "401" ]] || fail "the refusal of a wrong password answered ${WRONG_STATUS}"

# The signed-in view of the restored Arena, which is what a person sees after the
# recovery: the page has to answer, and it has to be the page of a session.
SIGNED_STATUS="$(curl --silent --output "$WORK_DIR/arena-signed.html" --write-out '%{http_code}' --max-time 10 \
	-b "$COOKIE_JAR" "${BASE_URL}/arenas/${ARENA_SLUG}")"
smoke+=("$(jq -nc --argjson status "$SIGNED_STATUS" "{what:\"the Arena as the signed-in participant reads it\", method:\"GET\", path:\"/arenas/${ARENA_SLUG}\", status:\$status, want:200}")")
[[ "$SIGNED_STATUS" == "200" ]] || fail "the participation page answered ${SIGNED_STATUS} to the signed-in participant on the restored data"
[[ "$(wc -c <"$WORK_DIR/arena-signed.html")" -gt 0 ]] ||
	fail "the participation page answered ${SIGNED_STATUS} with an empty body: the restored data came back and nothing rendered"

# ---------------------------------------------------------------------------
# 9. The versioned load baseline, against the restored instance
# ---------------------------------------------------------------------------
K6_SUMMARY="$WORK_DIR/k6-summary.json"
K6_OUTPUT="$WORK_DIR/k6.out"
k6_started=$SECONDS
set +e
scrubbed K6_BASE_URL="$BASE_URL" \
	K6_DATASET_SEED="drill-${SEED}" \
	K6_ARENA_ID="$ARENA_ID" \
	K6_ACCOUNT_EMAIL="$PARTICIPANT_EMAIL" \
	K6_ACCOUNT_PASSWORD="$PASSWORD" \
	k6 run --summary-export "$K6_SUMMARY" tests/load/smoke.js >"$K6_OUTPUT" 2>&1
K6_STATUS=$?
set -e
K6_DURATION=$((SECONDS - k6_started))
[[ -f "$K6_SUMMARY" ]] || { tail -20 "$K6_OUTPUT" >&2; fail "the load workload produced no summary"; }

thresholds="$WORK_DIR/thresholds.json"
node - "$ROOT/tests/load/smoke.js" >"$thresholds" <<'NODE'
const fs = require('fs');
const source = fs.readFileSync(process.argv[2], 'utf8');
const open = source.indexOf('thresholds: {');
if (open < 0) { process.stderr.write('the workload declares no thresholds\n'); process.exit(2); }
const close = source.indexOf('};', open);
const block = source.slice(open, close);
const rows = [];
// The keys are read with their quotes when they carry one, because several of
// them name a tagged metric (`http_req_duration{workload:login}`) and a split on
// the first colon would read half a metric name as the whole of it.
for (const line of block.split('\n')) {
  const match = line.match(/^\s*(?:'([^']+)'|([A-Za-z0-9_]+))\s*:\s*\[\s*'([^']+)'\s*\]/);
  if (match) rows.push({ metric: match[1] || match[2], bound: match[3] });
}
process.stdout.write(JSON.stringify(rows, null, 2));
NODE
[[ "$(jq 'length' "$thresholds")" -ge 1 ]] || fail "the versioned workload declares no threshold, so there is nothing to register"

# The measured side is read out of the summary k6 itself wrote, and a crossed
# bound is what k6 says crossed: the drill never re-derives a budget. A rate
# metric and a trend metric carry their figures differently — the rate has one
# `value`, the trend has a percentile per key — so the bound decides where to
# look, and anything the summary does not carry stays `unknown` and is refused
# below rather than registered as if it had been measured.
measured_for() {
	local metric="$1" bound="$2"
	case "$bound" in
	rate\<*)
		jq -r --arg m "$metric" '(.metrics[$m].value // "unknown") | tostring' "$K6_SUMMARY"
		;;
	p\(*\)*)
		local percentile
		percentile="$(printf '%s' "$bound" | sed -nE 's/^p\(([0-9]+)\).*/\1/p')"
		jq -r --arg m "$metric" --arg p "p(${percentile})" '(.metrics[$m][$p] // "unknown") | tostring' "$K6_SUMMARY"
		;;
	*)
		echo "unknown"
		;;
	esac
}

# One JSON object per line, collected below: this file is the accumulator, and
# the array is built from it in one `jq -s`, not appended as if it were JSON.
thresholds_with_measures="$WORK_DIR/thresholds-measured.json"
: >"$thresholds_with_measures"
jq -c '.[]' "$thresholds" | while IFS= read -r row; do
	metric="$(jq -r '.metric' <<<"$row")"
	bound="$(jq -r '.bound' <<<"$row")"
	measured="$(measured_for "$metric" "$bound")"
	jq -nc --arg metric "$metric" --arg bound "$bound" --arg measured "$measured" \
		'{metric:$metric, bound:$bound, measured:$measured}' >>"$thresholds_with_measures"
done

breaches="$WORK_DIR/breaches.json"
# The default is the empty list and not an empty file: the gate reads this as
# JSON, and "nothing crossed" has to be a value the report can carry. What
# counts as crossed is what k6 recorded for each declared bound, which is its
# own verdict and not the drill's arithmetic.
printf '[]\n' >"$breaches"
jq -r '.metrics | to_entries[] | select(.value.thresholds != null) | .key as $metric | .value.thresholds | to_entries[] | select(.value == true) | "\($metric): \(.key) was crossed"' \
	"$K6_SUMMARY" | jq -R -s 'split("\n") | map(select(length > 0))' >"$breaches"
if [[ "$(jq 'length' "$breaches")" -eq 0 && "$K6_STATUS" -ne 0 ]]; then
	# A workload that failed without naming a bound is not a pass: the failure
	# is registered as a breach so the report cannot carry it as a green run.
	jq -nc --argjson status "$K6_STATUS" \
		'["the workload exited " + ($status|tostring) + " without naming a crossed threshold"]' >"$breaches"
fi

if [[ "$(jq 'length' "$breaches")" -eq 0 ]]; then
	log "the load baseline ran green against the restored instance: no declared threshold was crossed"
else
	log "the load workload crossed $(jq 'length' "$breaches") declared threshold(s)"
fi
printf '%s' "$(jq -s '.' "$thresholds_with_measures")" >"$WORK_DIR/load-thresholds.json"
if jq -e '.[] | select(.measured == "unknown")' "$WORK_DIR/load-thresholds.json" >/dev/null 2>&1; then
	jq -c '.[] | select(.measured == "unknown")' "$WORK_DIR/load-thresholds.json" >&2
	fail "the workload declared a bound the summary carries no measurement for: a threshold registered as unknown is a budget nobody stood in front of"
fi

# ---------------------------------------------------------------------------
# 10. The versioned browser journeys, against the restored cluster
# ---------------------------------------------------------------------------
E2E_OK=false
E2E_DETAIL="the browser journeys did not run"
if ARENA_DATABASE_URL="$RESTORED_DSN" ARENA_E2E_ASSETS_DIR="$ASSETS_DIR" \
	"$TOOL_DIR/../e2e/harness.sh" >"$WORK_DIR/e2e.out" 2>&1; then
	E2E_OK=true
	E2E_DETAIL="the versioned harness drove its journeys green against the restored cluster (it provisions its own database inside it, because a journey's fixtures are not a customer's data: what the drill reads out of the restored data is the smoke probes and the ledger comparison above)"
	log "the browser journeys ran green against the restored cluster"
else
	E2E_DETAIL="the versioned harness failed against the restored cluster: $(tail -3 "$WORK_DIR/e2e.out" | tr '\n' ' ')"
	log "$E2E_DETAIL"
fi

# ---------------------------------------------------------------------------
# 11. The email provider unavailable, and back
# ---------------------------------------------------------------------------
# The delivery path in production is the one the phase is about: the journey hands
# the message to the outbox inside its transaction, the queue holds it as durable
# work, and the worker renders and delivers it. The drill drives that path with the
# product's own form — there is no private producer here — while the provider is
# unreachable, and then again once the provider answers.
PROD_PORT="$(node -e 'const s=require("net").createServer();s.listen(0,"127.0.0.1",()=>{process.stdout.write(String(s.address().port));s.close()})')"
PROD_URL="http://127.0.0.1:${PROD_PORT}"
PROD_LOG="$WORK_DIR/server-production.log"
# The worker's environment is the production composition minus the address: it
# mounts no page and owns the provider credential. It is an array and not a
# function because `timeout` bounds a command, and the bound is part of the
# exercise: a worker that outlives the drill would deliver into a process nobody
# is watching.
worker_env=(
	ARENA_ENV=production
	ARENA_DATABASE_URL="$RESTORED_DSN"
	ARENA_STRIPE_SECRET_KEY="sk_live_drill_synthetic"
	ARENA_RESEND_API_KEY="re_drill_synthetic_key"
	ARENA_EMAIL_FROM="Arena <no-reply@arena.invalid>"
	ARENA_CURSOR_SECRET="$CURSOR_SECRET"
)
run_worker() {
	timeout -s TERM "$1" env -i PATH="$PATH" HOME="${HOME:-$WORK_DIR}" TMPDIR="${TMPDIR:-/tmp}" \
		"${worker_env[@]}" "${@:2}" "$WORK_DIR/bin/arena" worker
}

scrubbed ARENA_ENV=production \
	ARENA_ADDR="127.0.0.1:${PROD_PORT}" \
	ARENA_DATABASE_URL="$RESTORED_DSN" \
	ARENA_ASSETS_DIR="$ASSETS_DIR" \
	ARENA_STRIPE_SECRET_KEY="sk_live_drill_synthetic" \
	ARENA_RESEND_API_KEY="re_drill_synthetic_key" \
	ARENA_EMAIL_FROM="Arena <no-reply@arena.invalid>" \
	ARENA_CURSOR_SECRET="$CURSOR_SECRET" \
	ARENA_LOG_LEVEL=info \
	"$WORK_DIR/bin/arena" server >"$PROD_LOG" 2>&1 &
PROD_SERVER_PID=$!
prod_ready=0
for _ in $(seq 1 300); do
	if ! kill -0 "$PROD_SERVER_PID" 2>/dev/null; then
		tail -n 40 "$PROD_LOG" >&2
		fail "the production composition did not stay up"
	fi
	if curl --silent --fail --max-time 2 "$PROD_URL/health/ready" >/dev/null; then
		prod_ready=1
		break
	fi
	sleep 0.2
done
[[ "$prod_ready" -eq 1 ]] || { tail -n 40 "$PROD_LOG" >&2; fail "the production composition never became ready"; }

# A registration through the product's own form, with the double-submit token the
# form issues: this is the request a person makes, and it is what puts the
# delivery on the queue.
CSRF_HEADERS="$WORK_DIR/register.headers"
curl --silent --output "$WORK_DIR/register.html" -D "$CSRF_HEADERS" -c "$WORK_DIR/csrf.jar" \
	"${PROD_URL}/register"
csrf_cookie_line="$(grep -i '^set-cookie:' "$CSRF_HEADERS" | head -1 | tr -d '\r')"
[[ -n "$csrf_cookie_line" ]] || fail "the registration form issued no CSRF cookie, so the drill cannot drive the form"
csrf_name="${csrf_cookie_line#*: }"
csrf_name="${csrf_name%%=*}"
csrf_value="${csrf_cookie_line#*=}"
csrf_value="${csrf_value%%;*}"
REGISTER_EMAIL="drill-outage-${SEED}@example.test"
REGISTER_STATUS="$(curl --silent --output "$WORK_DIR/register-result.html" --write-out '%{http_code}' \
	-b "$WORK_DIR/csrf.jar" -c "$WORK_DIR/csrf.jar" \
	-H "X-CSRF-Token: ${csrf_value}" -H "Origin: ${PROD_URL}" \
	--data-urlencode "csrf_token=${csrf_value}" \
	--data-urlencode "email=${REGISTER_EMAIL}" \
	--data-urlencode "password=${PASSWORD}" \
	"${PROD_URL}/register")"
[[ "$REGISTER_STATUS" == "200" ]] ||
	fail "the registration was refused with ${REGISTER_STATUS} while the provider was unreachable: a registration whose message is durable must not depend on the provider being up"
log "a registration went through the product's own form while the provider was unreachable (${csrf_name})"

EMAIL_JOB_SQL="SELECT state, attempts, COALESCE(last_error_code, ''), available_at, created_at FROM app.jobs WHERE type = 'email_delivery' ORDER BY created_at DESC LIMIT 1"
job_row() { sql "${PROJECT}-restored" "$EMAIL_JOB_SQL"; }
IFS='|' read -r job_state job_attempts job_error job_available job_created <<<"$(job_row)"
[[ "$job_attempts" == "0" ]] || fail "the delivery was attempted before the worker ran: the queue belongs to the worker"
wallet_rows_before="$(sql "${PROJECT}-restored" 'SELECT count(*) FROM app.wallet_transactions')"

# Direction one: the provider cannot be reached. The transport is pointed at a
# closed port, which is a real unreachable provider and not an injected error.
# Six seconds and no more: the queue's own policy backs off by seconds, so a
# longer window would consume the attempt budget and park the row, and what this
# direction of the exercise is about is the retry that follows an outage — the
# row has to survive it still deliverable.
run_worker 6 HTTPS_PROXY="http://127.0.0.1:1" >"$WORK_DIR/worker-offline.out" 2>&1 || true
IFS='|' read -r job_state job_attempts job_error job_available job_created <<<"$(job_row)"
EMAIL_ATTEMPTS_BEFORE="$job_attempts"
EMAIL_ERROR_CODE="$job_error"
EMAIL_JOB_RETAINED=false
if [[ "$job_state" != "dead" && "$job_attempts" -ge 1 && "$job_attempts" -lt 5 ]]; then EMAIL_JOB_RETAINED=true; fi
[[ "$EMAIL_ATTEMPTS_BEFORE" -ge 1 ]] ||
	{ tail -20 "$WORK_DIR/worker-offline.out" >&2; fail "the worker recorded no attempt while the provider was unreachable"; }
[[ -n "$EMAIL_ERROR_CODE" ]] ||
	{ tail -20 "$WORK_DIR/worker-offline.out" >&2; fail "the failed delivery was recorded with no error code, so an operator would not know why the message did not leave"; }
[[ "$EMAIL_JOB_RETAINED" == "true" ]] ||
	fail "the delivery was dropped (state ${job_state}, ${job_attempts} attempt(s)) instead of being retried: a message lost to an outage is a message the product dropped in silence"
log "the provider was unreachable: attempt ${EMAIL_ATTEMPTS_BEFORE} recorded the code ${EMAIL_ERROR_CODE} and the job is still ${job_state}"

wallet_rows_offline="$(sql "${PROJECT}-restored" 'SELECT count(*) FROM app.wallet_transactions')"
[[ "$wallet_rows_offline" == "$wallet_rows_before" ]] ||
	fail "the email outage wrote $(($wallet_rows_offline - $wallet_rows_before)) financial row(s): a provider that cannot be reached may not move INK"

# Direction two: the provider answers. The stand-in is the provider for the
# length of the exercise, and the worker still verifies its certificate: the
# trust root is handed over explicitly instead of being turned off.
VENDOR_DIR="$WORK_DIR/vendor"
mkdir -p "$VENDOR_DIR"
"$WORK_DIR/bin/vendorsim" -dir "$VENDOR_DIR" -log "$WORK_DIR/vendor.log" >"$WORK_DIR/vendor.out" 2>"$WORK_DIR/vendor.err" &
VENDOR_PID=$!
VENDOR_PORT=""
for _ in $(seq 1 100); do
	VENDOR_PORT="$(sed -nE 's/^vendorsim port=([0-9]+).*/\1/p' "$WORK_DIR/vendor.out" | head -1)"
	[[ -n "$VENDOR_PORT" ]] && break
	sleep 0.1
done
[[ -n "$VENDOR_PORT" ]] || { cat "$WORK_DIR/vendor.err" >&2; fail "the provider stand-in never reported its port"; }

# The retry is scheduled by the queue's own backoff, so the drill waits for the
# moment the row says it is due instead of editing the row.
available_epoch="$(sql "${PROJECT}-restored" "SELECT floor(extract(epoch FROM available_at))::bigint FROM app.jobs WHERE type = 'email_delivery' ORDER BY created_at DESC LIMIT 1")"
while (( $(date +%s) < available_epoch )); do sleep 1; done
delivery_started=$(date +%s)
run_worker 60 HTTPS_PROXY="http://127.0.0.1:${VENDOR_PORT}" \
	SSL_CERT_FILE="$VENDOR_DIR/vendorsim.crt" >"$WORK_DIR/worker-online.out" 2>&1 || true
IFS='|' read -r job_state job_attempts job_error job_available job_created <<<"$(job_row)"
EMAIL_DELIVERED_AFTER=false
EMAIL_DELIVERED_SECONDS=0
if [[ "$job_state" == "succeeded" ]]; then EMAIL_DELIVERED_AFTER=true; fi
EMAIL_DELIVERED_SECONDS=$(($(date +%s) - DISASTER_EPOCH))
grep -q "receipt=" "$WORK_DIR/vendor.log" || {
	tail -20 "$WORK_DIR/worker-online.out" >&2
	fail "the provider stand-in answered no delivery once it was reachable, so the retry path did not run"
}
[[ "$EMAIL_DELIVERED_AFTER" == "true" ]] || {
	tail -20 "$WORK_DIR/worker-online.out" >&2
	fail "the delivery is ${job_state} with ${job_attempts} attempt(s) after the provider answered: the retry path did not deliver"
}
log "the provider answered: the same message was delivered on attempt ${job_attempts} in ${EMAIL_DELIVERED_SECONDS}s from the loss, and the queue holds $(grep -c 'receipt=' "$WORK_DIR/vendor.log") receipt(s)"

# ---------------------------------------------------------------------------
# 12. The payment provider unavailable
# ---------------------------------------------------------------------------
# The provider surface is probed while it cannot be reached: the same key the
# deployment would hold, a synthetic credential the provider refuses, and an
# authenticated product call that reaches for it. What matters is not the status
# alone but that nothing moved.
stripe_routes=()
stripe_probe() {
	local what="$1" method="$2" path="$3" want="$4" auth="$5" body="$6"
	local status
	if [[ "$auth" == "session" ]]; then
		status="$(curl --silent --output "$WORK_DIR/stripe.out" --write-out '%{http_code}' --max-time 15 \
			-b "$PROD_JAR" -X "$method" -H 'Content-Type: application/json' \
			${body:+-d "$body"} "${PROD_URL}${path}")"
	else
		status="$(curl --silent --output "$WORK_DIR/stripe.out" --write-out '%{http_code}' --max-time 15 \
			-X "$method" -H 'Content-Type: application/json' \
			-H 'Stripe-Signature: synthetic-invalid-signature' \
			${body:+-d "$body"} "${PROD_URL}${path}")"
	fi
	stripe_routes+=("$(jq -nc --arg what "$what" --arg method "$method" --arg path "$path" \
		--argjson status "$status" --argjson want "$want" \
		'{what:$what, method:$method, path:$path, status:$status, want:$want}')")
	printf 'disaster-drill: stripe %s %s → %s (expected %s)\n' "$method" "$path" "$status" "$want" >&2
}
wallet_rows_before_stripe="$(sql "${PROJECT}-restored" 'SELECT count(*) FROM app.wallet_transactions')"
stripe_probe "the provider webhook refuses an unverified event" POST "/api/v1/webhooks/stripe" 400 "" \
	'{"id":"evt_drill_synthetic","type":"checkout.session.completed","livemode":false}'
stripe_probe "the checkout surface answers an unauthenticated caller" POST "/api/v1/me/billing/checkout" 401 "" \
	'{"price":"price_drill_synthetic"}'
stripe_probe "the billing surface is mounted" GET "/api/v1/me/billing/subscription" 401 "" ""
stripe_probe "the wallet surface is mounted" GET "/api/v1/me/wallet" 401 "" ""
wallet_rows_after_stripe="$(sql "${PROJECT}-restored" 'SELECT count(*) FROM app.wallet_transactions')"
STRIPE_LEDGER_ROWS=$((wallet_rows_after_stripe - wallet_rows_before_stripe))
[[ "$STRIPE_LEDGER_ROWS" -eq 0 ]] ||
	fail "${STRIPE_LEDGER_ROWS} financial row(s) appeared while the payment provider was unreachable"
STRIPE_VERDICT="refused"
STRIPE_OWNER=""
STRIPE_PLAN=""
LOAD_NOTE=""
limitations=()
absent_routes="$(printf '%s\n' "${stripe_routes[@]}" | jq -s '[.[] | select(.status == 404) | .path] | unique')"
if [[ "$(jq 'length' <<<"$absent_routes")" -gt 0 ]]; then
	# A surface the contract declares and the delivered process does not mount is
	# a finding, and it is registered as one instead of being read as a provider
	# that happened to be away.
	STRIPE_VERDICT="surface_absent"
	STRIPE_OWNER="bootstrap/rel"
	STRIPE_PLAN="compose in 'arena server' the JSON surfaces the contract declares (the drill probed $(jq -r 'join(", ")' <<<"$absent_routes")) and register the Stripe webhook route; until then the payment boundary (and the wallet read of the restored data) is not reachable by the product"
	limitations+=("a superfície JSON que a composição entregue monta não é a do contrato: $(jq -r 'join(", ")' <<<"$absent_routes") responderam **404** em 'arena server', então o limite de pagamento e a leitura da carteira pela API são um achado (dono ${STRIPE_OWNER}) e o baseline de carga mede as latências dessas respostas tratadas, não as do caminho de pagamento;")
	LOAD_NOTE="as latências acima são as das respostas que a composição entregue dá aos caminhos do workload — $(jq -r 'join(", ")' <<<"$absent_routes") responderam 404 —, e não as de um caminho de pagamento ou de carteira montados: enquanto essas superfícies não estiverem compostas, o baseline mede o que existe, e o que existe não é o caminho que a fase pede para medir."
fi
log "the payment provider was unreachable: zero financial rows, verdict ${STRIPE_VERDICT}"

# ---------------------------------------------------------------------------
# 13. The facts, the report and the gate
# ---------------------------------------------------------------------------
kill "$PROD_SERVER_PID" >/dev/null 2>&1 || true
kill "$VENDOR_PID" >/dev/null 2>&1 || true
kill "$SERVER_PID" >/dev/null 2>&1 || true

ARCHIVE_LAG_RESTORED="$(jq -r '.archive_lag_seconds' "$WORK_DIR/baseline.json")"
DOCKER_VERSION="$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)"
HOST_KERNEL="$(uname -sr)"
HOST_CPUS="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo unknown)"
COMMIT="$(git rev-parse --short HEAD)"

jq -n \
	--argjson version 1 \
	--arg run_at "$(date -u +%Y-%m-%d)" \
	--arg commit "$COMMIT" \
	--arg kernel "$HOST_KERNEL" --arg cpus "$HOST_CPUS" --arg docker "$DOCKER_VERSION" \
	--arg seed "$SEED" --argjson accounts 3 --argjson ink "$DATASET_INK" --arg arena "$ARENA_SLUG" \
	--arg baseline_at "$BASELINE_AT" --arg target_at "$TARGET_TIME" --arg disaster_at "$DISASTER_AT" \
	--argjson rows_after "$ROWS_AFTER_TARGET" --arg restore_command "deploy/backup/restore.sh --base ${BASE_LABEL} --target-time ${TARGET_TIME}" \
	--argjson bound "$ARCHIVE_BOUND" --argjson observed "$RPO_OBSERVED" --argjson lag "$ARCHIVE_LAG_RESTORED" \
	--arg newest "$NEWEST_RECOVERED_AT" \
	--argjson rto_target "$RTO_TARGET_SECONDS" --argjson rto_writable "$RTO_WRITABLE" --argjson rto_app "$RTO_APP" \
	--argjson attempts "$EMAIL_ATTEMPTS_BEFORE" --arg error_code "$EMAIL_ERROR_CODE" \
	--argjson retained "$EMAIL_JOB_RETAINED" --argjson delivered "$EMAIL_DELIVERED_AFTER" \
	--argjson delivered_seconds "$EMAIL_DELIVERED_SECONDS" \
	--argjson stripe_routes "$(printf '%s\n' "${stripe_routes[@]}" | jq -s '.')" \
	--arg stripe_verdict "$STRIPE_VERDICT" --arg stripe_owner "$STRIPE_OWNER" --arg stripe_plan "$STRIPE_PLAN" \
	--argjson stripe_rows "$STRIPE_LEDGER_ROWS" \
	--argjson smoke "$(printf '%s\n' "${smoke[@]}" | jq -s '.')" \
	--argjson e2e "$E2E_OK" --arg e2e_detail "$E2E_DETAIL" \
	--argjson thresholds "$(cat "$WORK_DIR/load-thresholds.json")" \
	--argjson breaches "$(cat "$breaches")" \
	--argjson duration "$K6_DURATION" \
	--argjson limitations "$(printf '%s\n' "${limitations[@]:-}" | jq -R -s 'split("\n") | map(select(length > 0))')" \
	--arg load_note "$LOAD_NOTE" \
	--argjson archive_failed "$ARCHIVE_FAILED" \
	'{
		version: $version,
		run_at: $run_at,
		commit: $commit,
		host: {kernel: $kernel, cpus: $cpus, docker: $docker},
		dataset: {seed: $seed, accounts: $accounts, ink: $ink, arena: $arena},
		timeline: {
			baseline_at: $baseline_at, target_at: $target_at, disaster_at: $disaster_at,
			rows_after_target: $rows_after, restore_command: $restore_command, primary_destroyed: true
		},
		rpo: {
			bound_seconds: $bound, observed_seconds: $observed,
			archive_lag_seconds: $lag, newest_recovered_commit: $newest
		},
		rto: {target_seconds: $rto_target, to_writable_seconds: $rto_writable, to_app_seconds: $rto_app},
		outage: {
			email: {
				attempts_before: $attempts, error_code: $error_code,
				job_retained: $retained, delivered_after: $delivered, delivered_in_seconds: $delivered_seconds
			},
			stripe: {
				routes: $stripe_routes, verdict: $stripe_verdict, ledger_rows_created: $stripe_rows,
				owner: $stripe_owner, plan: $stripe_plan
			}
		},
		load: {
			tool: "k6", script: "tests/load/smoke.js", dataset: $seed,
			duration_seconds: $duration, thresholds: $thresholds, breaches: $breaches,
			note: $load_note
		},
		journeys: {smoke: $smoke, e2e: $e2e, e2e_detail: $e2e_detail},
		limitations: $limitations
	}' >"$WORK_DIR/facts.json"

"$WORK_DIR/bin/drillaudit" report -facts "$WORK_DIR/facts.json" -comparison "$WORK_DIR/comparison.json" \
	-out "$REPORT" || fail "the report could not be written"
"$WORK_DIR/bin/drillaudit" check -file "$REPORT" ||
	fail "the drill's own gate refused the report it just wrote: the exercise is not evidence while a number is missing, a ceiling was crossed or the ledger did not come back equal"

log "ok — the backup was restored in isolation, the ledger and the projection came back equal with nothing created after the target, RPO ${RPO_OBSERVED}s against a ${ARCHIVE_BOUND}s bound, RTO ${RTO_WRITABLE}s to writable and ${RTO_APP}s to the application against a ${RTO_TARGET_SECONDS}s target, the load baseline registered without a crossed threshold, and both providers were exercised with a provider unavailable"
