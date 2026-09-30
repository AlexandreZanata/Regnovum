#!/usr/bin/env bash
#
# Verification of the PostgreSQL backup pipeline (P19-T04).
#
# Why the gate is an exercise and not a set of assertions about files: the
# questions this file answers are questions about a database that was lost. Is
# the WAL actually leaving the server? Does the object in the store hold the
# bytes the backup uploaded, sealed? Does a restore into an *empty* cluster
# reach a chosen point in time, with the migrations and the rows committed
# before it and without the rows committed after? How long did that take, and
# how much history could have been lost? None of that can be read out of a
# configuration file.
#
# What one run owns, and therefore tears down on every exit path: a throwaway
# S3-compatible store, a throwaway primary cluster, the container the restore
# runs in, the network they share, the generated credentials and the sealing
# key.
#
# It is fail-closed: a missing command, a Compose file whose database would not
# archive, a segment that never reached the store, an object the store holds in
# the clear, a key that opens what it should not, a restore that stops short of
# its target or overshoots it, a migration that did not come back, a row that
# came back changed, or a retention run that removes what a restore needs all
# abort with a non-zero status.
#
# Requirements: Docker with a reachable daemon, Go, curl, and the application
# image (ARENA_IMAGE, default goyim-arena:local) for the migrations step.
#
# Environment:
#   ARENA_IMAGE             the application image that applies the migrations
#   ARENA_BACKUP_S3_IMAGE   the S3-compatible store to run. Default: the MinIO
#                           digest this repository verified against, pulled from
#                           quay.io (the registry MinIO publishes to; the Docker
#                           Hub repository no longer exists).
set -euo pipefail

TOOL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$TOOL_DIR/../.." && pwd)"
cd "$ROOT"

COMPOSE_FILE="compose.production.yaml"
PROJECT="arena-backupaudit-$$"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/arena-backupaudit-XXXXXX")"
NETWORK="${PROJECT}-net"
APP_IMAGE="${ARENA_IMAGE:-goyim-arena:local}"
S3_IMAGE="${ARENA_BACKUP_S3_IMAGE:-quay.io/minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e}"
BUCKET="arena-backups"
PREFIX="arena"
MARKER="GAEB-EXERCISE-MARKER-must-not-be-readable-at-rest"
S3_USER="arena-backup"
S3_PASSWORD="backup-exercise-secret"
DB_USER="arena"
DB_PASSWORD="exercise-db-password"
DB_NAME="arena"

log() { printf 'backup-verify: %s\n' "$*" >&2; }
fail() { printf 'backup-verify: %s\n' "$*" >&2; exit 1; }

cleanup() {
	# The clutter this exercise owns is removed on every exit path, and none of
	# these steps may change the exit status: by the time the trap runs, the
	# verdict is already the status the script is leaving with.
	docker rm --force "${PROJECT}-primary" "${PROJECT}-restored" "${PROJECT}-s3" >/dev/null 2>&1 || true
	docker network rm "$NETWORK" >/dev/null 2>&1 || true
	# The store's and the server's data directories are written by root inside
	# the containers, so the host user cannot unlink what is inside them; one
	# last container with the image that is already local removes them, and the
	# host removes the rest. The image is named only if it was read out of the
	# Compose file: a run that aborted before that has no container clutter, and
	# naming an empty image here would make the trap itself the error.
	if [[ -n "${POSTGRES_IMAGE:-}" ]]; then
		docker run --rm --mount "type=bind,src=${WORK_DIR},dst=/cleanup" "$POSTGRES_IMAGE" \
			sh -c 'rm -rf /cleanup/* /cleanup/.[!.]* /cleanup/..?*' >/dev/null 2>&1 || true
	fi
	rm -rf "$WORK_DIR" >/dev/null 2>&1 || true
	true
}
trap cleanup EXIT

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required and was not found in PATH"
}
require_command docker
require_command go
require_command curl

# ---------------------------------------------------------------------------
# 1. The tool, and the deployment's own audit of itself
# ---------------------------------------------------------------------------
CGO_ENABLED=0 go build -o "$WORK_DIR/backupctl" ./tools/backupctl || fail "the backup tool did not build"

# A database that recycles WAL unwritten makes every assertion below pass while
# the store quietly stops receiving anything, so the deployment is judged first.
if ! "$WORK_DIR/backupctl" check-compose "$COMPOSE_FILE" 2>"$WORK_DIR/compose.err"; then
	tail -10 "$WORK_DIR/compose.err" >&2
	fail "the committed Compose file would not archive"
fi
log "the committed topology declares the archive and passes its own rules"

# The database's flags come from the file, not from this script: the server the
# exercise starts is the server the deployment declares.
mapfile -t DB_COMMAND < <("$WORK_DIR/backupctl" print-compose-command "$COMPOSE_FILE")
[[ "${#DB_COMMAND[@]}" -gt 1 ]] || fail "the Compose file declares no command for the db service"
POSTGRES_IMAGE="$(sed -nE 's/^[[:space:]]*image:[[:space:]]+(postgres:[^[:space:]]+).*/\1/p' "$COMPOSE_FILE" | head -1)"
[[ -n "$POSTGRES_IMAGE" ]] || fail "compose.production.yaml pins no PostgreSQL image"
log "running ${POSTGRES_IMAGE} with the ${#DB_COMMAND[@]} arguments the deployment declares"

# ---------------------------------------------------------------------------
# 2. A store, a key, and the credentials the database is given
# ---------------------------------------------------------------------------
docker network create "$NETWORK" >/dev/null || fail "the throwaway network was not created"

# The bucket is prepared as a directory in the store's data volume. That is the
# harness's shortcut and it is deliberate: the credential this pipeline carries
# is the credential that archives, and one that could also create and delete
# buckets would be one privilege more than the pipeline needs — so the tool has
# no bucket command at all, and the harness creates the bucket where the store
# keeps it.
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

"$WORK_DIR/backupctl" keygen "$WORK_DIR/backup.key" >/dev/null || fail "the sealing key was not generated"
chmod 0600 "$WORK_DIR/backup.key"
"$WORK_DIR/backupctl" keygen "$WORK_DIR/other.key" >/dev/null || fail "the second key was not generated"

# The key the *server* reads is a second file with the same bytes, because the
# owner each side needs is different: the archive_command runs as the postgres
# user inside the container, and that user cannot read a file the operator owns.
# The tool refuses any key that is readable beyond its owner, and it is right to:
# widening the mode so that a second user can read it would make the key every
# process able to read it. The answer is the one a deployment gives — the server
# gets the key as *its own* file, owned by it, mode 0600 — and this exercise sets
# that up the same way instead of loosening the host's copy.
mkdir -p "$WORK_DIR/server-key"
docker run --rm --mount "type=bind,src=${WORK_DIR},dst=/work" "$POSTGRES_IMAGE" \
	sh -c 'install -o postgres -g postgres -m 0600 /work/backup.key /work/server-key/backup.key' ||
	fail "the postgres user's copy of the sealing key was not prepared"

# The container-side environment: the endpoint is the store's *service* name, and
# the sealing key is a mounted file. The key is never an environment variable,
# because a key in the environment is a key in `docker inspect`.
cat >"$WORK_DIR/backup.env" <<EOF
BACKUP_S3_ENDPOINT=http://minio:9000
BACKUP_S3_BUCKET=${BUCKET}
BACKUP_S3_ACCESS_KEY=${S3_USER}
BACKUP_S3_SECRET_KEY=${S3_PASSWORD}
BACKUP_S3_PREFIX=${PREFIX}
BACKUP_ENCRYPTION_KEY_FILE=/run/secrets/backup_key
BACKUP_BIN=/opt/backup-tool/backupctl
BACKUP_LOCK_DIR=/tmp
# The scripts talk to the server over the local socket with the standard libpq
# variables, which is what the deployment's database environment file sets too.
PGUSER=${DB_USER}
EOF
chmod 0600 "$WORK_DIR/backup.env"

mounts=(
	--mount "type=bind,src=${ROOT}/deploy/backup,dst=/opt/backup,readonly"
	--mount "type=bind,src=${WORK_DIR}/backupctl,dst=/opt/backup-tool/backupctl,readonly"
	--mount "type=bind,src=${WORK_DIR}/server-key/backup.key,dst=/run/secrets/backup_key,readonly"
)

# Every container-side invocation of the tool reads its settings from the file
# the deployment would use; only the host-side ones below name the port this
# machine sees. There is deliberately no second copy of the S3 settings.
container_env=(--env-file "$WORK_DIR/backup.env")

host_ctl() {
	env BACKUP_S3_ENDPOINT="http://127.0.0.1:${S3_PORT}" BACKUP_S3_BUCKET="$BUCKET" \
		BACKUP_S3_ACCESS_KEY="$S3_USER" BACKUP_S3_SECRET_KEY="$S3_PASSWORD" BACKUP_S3_PREFIX="$PREFIX" \
		BACKUP_ENCRYPTION_KEY_FILE="$WORK_DIR/backup.key" "$WORK_DIR/backupctl" "$@"
}

sql() {
	# sql <container> <statement> — one value, no formatting.
	docker exec "$1" psql --no-psqlrc --tuples-only --no-align --username "$DB_USER" --dbname "$DB_NAME" -c "$2"
}

psql_script() {
	docker exec -i "$1" psql --no-psqlrc --quiet --username "$DB_USER" --dbname "$DB_NAME"
}

# ---------------------------------------------------------------------------
# 3. A primary that archives, migrated, with rows committed before a target
# ---------------------------------------------------------------------------
start_primary() {
	mkdir -p "$WORK_DIR/primary-data"
	chmod 0777 "$WORK_DIR/primary-data"
	docker run --detach --name "${PROJECT}-primary" --network "$NETWORK" \
		--env "POSTGRES_PASSWORD=${DB_PASSWORD}" --env "POSTGRES_USER=${DB_USER}" --env "POSTGRES_DB=${DB_NAME}" \
		--env "POSTGRES_INITDB_ARGS=--encoding=UTF8 --locale=C" \
		"${container_env[@]}" "${mounts[@]}" \
		--mount "type=bind,src=${WORK_DIR}/primary-data,dst=/var/lib/postgresql" \
		"$POSTGRES_IMAGE" "${DB_COMMAND[@]}" >/dev/null
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

start_primary
wait_ready "${PROJECT}-primary"
log "the primary is up with the deployment's archive settings"

DSN="postgres://${DB_USER}:${DB_PASSWORD}@${PROJECT}-primary:5432/${DB_NAME}?sslmode=disable"
if ! docker run --rm --network "$NETWORK" --env "ARENA_DATABASE_URL=${DSN}" "$APP_IMAGE" migrate up \
	>"$WORK_DIR/migrate.out" 2>"$WORK_DIR/migrate.err"; then
	tail -20 "$WORK_DIR/migrate.err" >&2 || true
	fail "the migrations did not apply (is ARENA_IMAGE=$APP_IMAGE built?)"
fi
log "the migrations applied through the application's own runner"

# The exercise writes into a real table: app.jobs is what the queue stores, it
# has a check constraint on its type, and losing or reordering its rows is the
# kind of damage a point-in-time restore exists to undo.
insert_job() {
	psql_script "${PROJECT}-primary" <<SQL
INSERT INTO app.jobs (type, version, parameters, idempotency_key, available_at)
VALUES ('retention_run', 1, jsonb_build_object('marker', '${MARKER}', 'sequence', $1), 'exercise-$1', now());
SQL
}
for sequence in 1 2 3 4 5; do insert_job "$sequence" >/dev/null; done
jobs_committed="$(sql "${PROJECT}-primary" 'SELECT count(*) FROM app.jobs')"
[[ "$jobs_committed" == "5" ]] || fail "the primary holds ${jobs_committed} jobs, want the 5 the exercise committed"

# ---------------------------------------------------------------------------
# 4. Continuous archiving: is the WAL leaving the server?
# ---------------------------------------------------------------------------
archived=""
deadline=$((SECONDS + 180))
while (( SECONDS < deadline )); do
	failed="$(sql "${PROJECT}-primary" 'SELECT failed_count FROM pg_stat_archiver')"
	if [[ "$failed" != "0" ]]; then
		sql "${PROJECT}-primary" 'SELECT last_failed_wal, last_failed_time FROM pg_stat_archiver' >&2 || true
		docker logs "${PROJECT}-primary" 2>&1 | tail -5 >&2 || true
		fail "the server failed to archive a segment ${failed} time(s): a broken archive is the failure that hides behind a running database"
	fi
	archived="$(sql "${PROJECT}-primary" 'SELECT archived_count FROM pg_stat_archiver')"
	if [[ -n "$archived" && "$archived" -ge 1 ]]; then
		break
	fi
	sql "${PROJECT}-primary" 'SELECT pg_switch_wal()' >/dev/null
	sleep 2
done
[[ "${archived:-0}" -ge 1 ]] || fail "no WAL segment reached the archive: archive_mode is on and nothing is being shipped"
log "the archive received ${archived} segment(s) with no failure"

wal_objects="$(host_ctl list wal/ | wc -l | tr -d ' ')"
[[ "$wal_objects" -ge 1 ]] || fail "the store lists no WAL segment"
log "the store holds ${wal_objects} WAL segment(s)"

# ---------------------------------------------------------------------------
# 5. A base backup, and what the store actually holds
# ---------------------------------------------------------------------------
BASE_LABEL="exercise-$(date -u +%Y-%m-%dT%H-%M-%SZ)"
docker exec "${PROJECT}-primary" /opt/backup/base-backup.sh --label "$BASE_LABEL" >"$WORK_DIR/base.out" 2>"$WORK_DIR/base.err" ||
	{ tail -20 "$WORK_DIR/base.err" >&2; fail "the base backup failed"; }
log "the base backup is stored under the label ${BASE_LABEL}"

host_ctl get "base/${BASE_LABEL}.json.enc" "$WORK_DIR/manifest.json" >/dev/null || fail "the stored manifest could not be read"
grep -q '"start_lsn"' "$WORK_DIR/manifest.json" || fail "the stored manifest quotes no start LSN: the retention floor would have nothing to stand on"
host_ctl list base/ | grep -q "${BASE_LABEL}.tar.gz.enc" || fail "the base backup tar is not in the store"

# The encryption is measured, not assumed: the object is downloaded exactly as
# the store holds it and the exercise's marker must not be in those bytes.
host_ctl get "base/${BASE_LABEL}.tar.gz.enc" "$WORK_DIR/sealed.tar.gz.enc" --raw >/dev/null
if grep -qa "$MARKER" "$WORK_DIR/sealed.tar.gz.enc"; then
	fail "the exercise's marker is readable in the object the store holds: the backup left this host in the clear"
fi
log "the stored base backup does not contain the row data in the clear"

# The control that makes the assertion above mean something: the marker *is* in
# the cluster the backup came from.
marked="$(sql "${PROJECT}-primary" "SELECT count(*) FROM app.jobs WHERE parameters->>'marker' = '${MARKER}'")"
[[ "$marked" == "5" ]] || fail "the primary holds ${marked} marked rows and the exercise wrote 5, so the assertion above measured nothing"
log "the marker is in the primary (${marked} rows) and not in the object"

# And the refusal in the other direction: another key does not open it.
if env BACKUP_S3_ENDPOINT="http://127.0.0.1:${S3_PORT}" BACKUP_S3_BUCKET="$BUCKET" \
	BACKUP_S3_ACCESS_KEY="$S3_USER" BACKUP_S3_SECRET_KEY="$S3_PASSWORD" BACKUP_S3_PREFIX="$PREFIX" \
	BACKUP_ENCRYPTION_KEY_FILE="$WORK_DIR/other.key" "$WORK_DIR/backupctl" get "base/${BASE_LABEL}.tar.gz.enc" "$WORK_DIR/wrong-key.tar.gz" >/dev/null 2>&1; then
	fail "a different key opened the base backup"
fi
log "another key does not open the object"

# ---------------------------------------------------------------------------
# 6. The target: rows committed before it, and rows committed after it
# ---------------------------------------------------------------------------
# The target is taken between two writes, so the exercise proves both directions
# of the boundary rather than only that a recovery finished.
sleep 1
TARGET_TIME="$(sql "${PROJECT}-primary" "SELECT to_char(now(), 'YYYY-MM-DD HH24:MI:SS.US')")"
sleep 1
for sequence in 6 7; do insert_job "$sequence" >/dev/null; done
sql "${PROJECT}-primary" 'SELECT pg_switch_wal()' >/dev/null

jobs_before="$(sql "${PROJECT}-primary" "SELECT count(*) FROM app.jobs WHERE created_at <= '${TARGET_TIME}'")"
jobs_after="$(sql "${PROJECT}-primary" "SELECT count(*) FROM app.jobs WHERE created_at > '${TARGET_TIME}'")"
[[ "$jobs_before" == "5" ]] || fail "the primary counts ${jobs_before} jobs at or before the target, want 5"
[[ "$jobs_after" == "2" ]] || fail "the primary counts ${jobs_after} jobs after the target, want 2"
checksum_at_target="$(sql "${PROJECT}-primary" "SELECT md5(string_agg(id || ':' || type || ':' || parameters::text, '|' ORDER BY id)) FROM app.jobs WHERE created_at <= '${TARGET_TIME}'")"
# The migration record is compared against the primary's own numbers and not
# against a count of files: the runner records a row for the baseline it writes
# when it creates the table, so "one row per file" is not what the table holds.
# What the files do prove is that every file left its row, which is asserted on
# its own line below; what the restore has to prove is that the same rows came
# back, which is what the digest is for.
migration_files="$(ls internal/platform/dbmigrate/migrations/*.sql | wc -l | tr -d ' ')"
migrations_applied="$(sql "${PROJECT}-primary" 'SELECT count(*) FROM app.schema_metadata WHERE is_applied')"
migrations_from_files="$(sql "${PROJECT}-primary" 'SELECT count(*) FROM app.schema_metadata WHERE is_applied AND version_id >= 1')"
[[ "$migrations_from_files" == "$migration_files" ]] ||
	fail "the primary records ${migrations_from_files} applied migrations from the files and the tree holds ${migration_files}"
migrations_digest="$(sql "${PROJECT}-primary" "SELECT md5(string_agg(version_id::text, ',' ORDER BY version_id)) FROM app.schema_metadata WHERE is_applied")"
# The schema object set: every table the migrations created must be in the
# restored cluster. The rows are covered by their own checksum above.
schema_expected="$(sql "${PROJECT}-primary" "SELECT md5(string_agg(tablename, ',' ORDER BY tablename)) FROM pg_tables WHERE schemaname = 'app'")"
log "the target is ${TARGET_TIME}: ${jobs_before} jobs at or before it, ${jobs_after} after, ${migration_files} migration files and ${migrations_applied} applied record(s)"

# ---------------------------------------------------------------------------
# 7. The loss, and the restore into an empty directory
# ---------------------------------------------------------------------------
# The mount point is /var/lib/postgresql and not /var/lib/postgresql/data: from
# 18 on, the image stores the cluster in a versioned subdirectory of that path
# and refuses to start when it finds data at the old one. The deployment mounts
# the same path, so the exercise runs the layout the deployment runs.
restore_started=$SECONDS
docker rm --force "${PROJECT}-primary" >/dev/null || fail "the primary could not be removed"
# The data directory is root-owned inside the container, so the host cannot
# unlink what is inside it; the loss is completed with the image that is already
# local. The store is what must remain afterwards, and it is untouched.
docker run --rm --mount "type=bind,src=${WORK_DIR},dst=/cleanup" "$POSTGRES_IMAGE" \
	rm -rf /cleanup/primary-data || fail "the primary's data directory could not be destroyed"
log "the primary and its data directory are gone: what remains is the store"

# The container the restore runs in is not a database yet: it exists so that the
# restore path can be driven the way an operator drives it, from inside the
# deployment's own image, with an empty directory to restore into.
# 0700 and not 0777: this directory becomes a cluster, and PostgreSQL refuses to
# start on a data directory it considers too open. The primary's directory above
# is looser on purpose — the image's entrypoint has to create the cluster inside
# it — but the restore target is handed to the server as it will run.
mkdir -p "$WORK_DIR/restored"
chmod 0700 "$WORK_DIR/restored"
docker run --detach --name "${PROJECT}-restored" --network "$NETWORK" \
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
restore_seconds=$((SECONDS - restore_started))
wait_ready "${PROJECT}-restored"
log "the restored server is answering: the restore took ${restore_seconds}s from the loss"

in_recovery="$(sql "${PROJECT}-restored" 'SELECT pg_is_in_recovery()')"
[[ "$in_recovery" == "f" ]] || fail "the restored server is still in recovery: a restore that has not promoted is a database nobody can write to"

# ---------------------------------------------------------------------------
# 8. What came back, and what did not
# ---------------------------------------------------------------------------
restored_before="$(sql "${PROJECT}-restored" "SELECT count(*) FROM app.jobs WHERE created_at <= '${TARGET_TIME}'")"
restored_after="$(sql "${PROJECT}-restored" "SELECT count(*) FROM app.jobs WHERE created_at > '${TARGET_TIME}'")"
restored_checksum="$(sql "${PROJECT}-restored" "SELECT md5(string_agg(id || ':' || type || ':' || parameters::text, '|' ORDER BY id)) FROM app.jobs WHERE created_at <= '${TARGET_TIME}'")"
migrations_restored="$(sql "${PROJECT}-restored" 'SELECT count(*) FROM app.schema_metadata WHERE is_applied')"
migrations_restored_digest="$(sql "${PROJECT}-restored" "SELECT md5(string_agg(version_id::text, ',' ORDER BY version_id)) FROM app.schema_metadata WHERE is_applied")"
schema_restored="$(sql "${PROJECT}-restored" "SELECT md5(string_agg(tablename, ',' ORDER BY tablename)) FROM pg_tables WHERE schemaname = 'app'")"

expect_equal() {
	local what="$1" want="$2" got="$3"
	[[ "$want" == "$got" ]] || fail "${what}: got ${got:-<nothing>}, want ${want}"
}
expect_equal "the jobs committed at or before the target" "5" "$restored_before"
expect_equal "the jobs committed after the target" "0" "$restored_after"
expect_equal "the checksum of the jobs that came back" "$checksum_at_target" "$restored_checksum"
expect_equal "the applied migration records" "$migrations_applied" "$migrations_restored"
expect_equal "the applied migration versions" "$migrations_digest" "$migrations_restored_digest"
expect_equal "the schema object set" "$schema_expected" "$schema_restored"
log "the restore carried ${restored_before} jobs (checksum ${restored_checksum}), ${migrations_restored} migration record(s) with their versions, and the schema, and none of the ${jobs_after} jobs committed after the target"

# The archive the restored server reads is the archive the primary wrote: the
# recovered cluster is a reader of the same objects, not a copy of a database
# that happened to be running.
log "the store holds $(host_ctl list wal/ | wc -l | tr -d ' ') WAL segment(s) and $(host_ctl list base/ | wc -l | tr -d ' ') base object(s) for both clusters"

# ---------------------------------------------------------------------------
# 9. Retention: what it keeps, what it lets go, and what it refuses
# ---------------------------------------------------------------------------
# An older base backup, so that the policy has something to remove. It is a real
# manifest with an older date rather than a flag that would not exist in
# production: the policy reads the manifest, so the fixture has to be one.
host_ctl get "base/${BASE_LABEL}.json.enc" "$WORK_DIR/old-manifest.json" >/dev/null
sed -E 's/"created_at":"[^"]*"/"created_at":"2000-01-01T00:00:00Z"/; s/"name":"[^"]*"/"name":"old-exercise"/; s/"label":"[^"]*"/"label":"old-exercise"/' \
	"$WORK_DIR/old-manifest.json" >"$WORK_DIR/old.json"
printf 'not a real tar, and retention never reads it\n' >"$WORK_DIR/old.tar.gz"
host_ctl put base/old-exercise.json.enc "$WORK_DIR/old.json" --if-absent >/dev/null
host_ctl put base/old-exercise.tar.gz.enc "$WORK_DIR/old.tar.gz" --if-absent >/dev/null

# The operator's own entry point is what the gate drives, not the tool's
# subcommands: retention.sh owns the window and the floor
# (BACKUP_KEEP_DAYS/BACKUP_KEEP_MIN), the order of the two prefixes and the lock,
# and the tool underneath owns the policy. A gate that called `prune` twice would
# leave the script that production actually invokes unexercised.
retention_env=(--env BACKUP_KEEP_DAYS=30 --env BACKUP_KEEP_MIN=1)
dry_run="$(docker exec "${retention_env[@]}" "${PROJECT}-restored" /opt/backup/retention.sh 2>"$WORK_DIR/retention.err")" || {
	tail -5 "$WORK_DIR/retention.err" >&2
	fail "retention.sh failed in reporting mode"
}
# The name the policy prints is the name it deletes and the name `delete` takes,
# which is the object's path inside the store's own prefix — the same address
# `list` shows with the prefix in front of it.
printf '%s\n' "$dry_run" | grep -q "would delete base/old-exercise.tar.gz.enc" ||
	fail "retention does not want to remove the older backup: the window is not being applied"
printf '%s\n' "$dry_run" | grep -q "would delete base/old-exercise.json.enc" ||
	fail "retention would remove only one of the two objects a base backup owns: a tar without its manifest is a backup no restore selects, and a manifest without its tar is one that restores nothing"
# Only the base selections are counted here: the same run also reports what the
# WAL pass would remove, and that is asserted in its own section below.
expect_equal "the objects a dry run selects" "2" "$(printf '%s\n' "$dry_run" | grep -c 'would delete base/')"
if printf '%s\n' "$dry_run" | grep -q "${BASE_LABEL}"; then
	fail "retention wants to remove the only usable backup"
fi
expect_equal "the objects after a dry run" "4" "$(host_ctl list base/ | wc -l | tr -d ' ')"
log "a dry run selects the older backup and keeps the one a restore needs, without deleting anything"

# --apply is the only path that deletes, and it is asked for explicitly.
wal_before="$(host_ctl list wal/ | wc -l | tr -d ' ')"
# The archive holds more than segments — the server also archives the backup
# history file it writes when a base backup finishes — and the policy removes
# only the names it can compare against its floor. Whatever is not a segment is
# recorded here so that leaving it alone is an assertion and not a hope.
wal_others="$(host_ctl list wal/ | awk '{print $1}' | grep -vE '^.*/wal/[0-9A-F]{24}\.enc$' | sort || true)"
apply_output="$(docker exec "${retention_env[@]}" "${PROJECT}-restored" /opt/backup/retention.sh --apply 2>"$WORK_DIR/retention-apply.err")" || {
	tail -5 "$WORK_DIR/retention-apply.err" >&2
	fail "retention.sh failed while applying"
}
expect_equal "the objects after applying retention" "2" "$(host_ctl list base/ | wc -l | tr -d ' ')"
# A base backup is two objects and retention treats them as one: the pair of the
# backup a restore would select has to be there, and the pair it selected has to
# be gone. Counting objects would accept either half surviving alone.
host_ctl list base/ | grep -q "base/${BASE_LABEL}.tar.gz.enc" ||
	fail "applying retention removed the tar of the backup the exercise restored from"
host_ctl list base/ | grep -q "base/${BASE_LABEL}.json.enc" ||
	fail "applying retention removed the manifest of the backup the exercise restored from"
if host_ctl list base/ | grep -q "old-exercise"; then
	fail "applying retention kept the older backup it had selected for removal"
fi
log "applying retention removed the older backup and kept the usable one"

# The WAL floor: retention must not remove the segments the kept backup needs,
# and the floor it publishes has to be the one it acted on. The policy names the
# segment it keeps, so the two readings — what it said it removed and what the
# store no longer lists — are compared instead of trusting either alone.
wal_after="$(host_ctl list wal/ | wc -l | tr -d ' ')"
floor="$(printf '%s\n' "$apply_output" | sed -nE 's/.*WAL floor is ([0-9A-F]{24});.*/\1/p' | head -1)"
[[ -n "$floor" ]] || fail "retention published no WAL floor, so the segment it keeps cannot be checked"
selected="$(printf '%s\n' "$apply_output" | sed -nE 's/.*; ([0-9]+) segment\(s\) selected for removal.*/\1/p' | head -1)"
expect_equal "the segments retention said it removed" "$((wal_before - wal_after))" "$selected"
host_ctl list wal/ | grep -q "wal/${floor}.enc" ||
	fail "retention removed ${floor}, the first segment the kept base backup needs, which makes the kept backup unrestorable"
[[ "$wal_after" -ge 1 ]] || fail "retention removed every WAL segment, leaving the kept base backup unrestorable"
while IFS= read -r object; do
	[[ -n "$object" ]] || continue
	host_ctl list wal/ | grep -qF "$object" ||
		fail "retention removed ${object}, which is not a segment and which it had to leave alone"
done <<<"$wal_others"
log "the WAL floor is ${floor}: retention removed ${selected} of ${wal_before} object(s), kept the segment that floor names, and left $(printf '%s\n' "$wal_others" | grep -c . ) object(s) whose name is not a segment alone"

# ---------------------------------------------------------------------------
# 10. The numbers this exercise exists to produce
# ---------------------------------------------------------------------------
# RPO is bounded by archive_timeout (declared in the Compose file and asserted
# above) and observed here as how long ago the archive last succeeded. RTO is
# what the restore just took. Neither is published as a guarantee: the targets in
# docs/DEPLOYMENT.md §7 stand until exercises accumulate.
archive_lag="$(sql "${PROJECT}-restored" "SELECT COALESCE(now() - last_archived_time, interval '-1') FROM pg_stat_archiver")"
[[ -n "$archive_lag" ]] || fail "the restored server reports nothing about its archive, so the observed RPO cannot be stated"
printf 'backup-verify: RPO bound (archive_timeout declared): 300s\n'
printf 'backup-verify: RPO observed (archive lag at the end of the exercise): %s\n' "${archive_lag:-unknown}"
printf 'backup-verify: RTO observed (loss of the primary to a server accepting writes at the target): %ss\n' "$restore_seconds"

log "ok — the deployment archives, the store holds sealed objects, an empty cluster was restored to a chosen point in time with its migrations and its rows and without the rows after it, and retention keeps what a restore would need"
