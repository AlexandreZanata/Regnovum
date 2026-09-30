#!/usr/bin/env bash
# Dev role lifecycle for Regnovum (P03-T03, development only).
#
# Provisions or rotates the login credential of the application runtime
# role (arena_app) and the operational roles (arena_backup,
# arena_observability), which are provisioned outside migrations because
# they belong to operations, not to the schema.
#
# Usage:
#   scripts/dev-role-cycle.sh provision   # first setup (or after wipe)
#   scripts/dev-role-cycle.sh rotate      # new random password for arena_app
#
# Environment:
#   ARENA_DEV_DB_URL   admin connection to the dev database, as seen from
#                      INSIDE the compose service (default: the bootstrap
#                      user against localhost of the container network)
#
# Passwords are generated with openssl rand, echoed once for the operator
# and never written to disk by this script.
set -euo pipefail

DB_URL="${ARENA_DEV_DB_URL:-postgres://arena:arena-local-dev@localhost:5432/arena?sslmode=disable}"

psql_exec() {
	docker compose exec -T db psql "$DB_URL" "$@"
}

generate_password() {
	local password
	password="$(openssl rand -base64 24 | tr -d '/+=' | cut -c1-24)"
	printf '%s' "$password"
}

role_exists() {
	psql_exec -t -A -c "SELECT 1 FROM pg_roles WHERE rolname = '$1';" | grep -q 1
}

create_login_role() {
	local role="$1"
	if role_exists "$role"; then
		echo "role $role already exists; skipping creation" >&2
		return
	fi
	psql_exec -v ON_ERROR_STOP=1 -c \
		"CREATE ROLE $role LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;"
}

command="${1:-}"
case "$command" in
provision)
	create_login_role arena_app
	create_login_role arena_backup
	create_login_role arena_observability
	# Operational grants, mirroring docs/ROLE_MODEL.md. Backup connects
	# (implicit on the database) and reads the whole app schema;
	# observability reads catalogs (pg_stat_* views).
	psql_exec -v ON_ERROR_STOP=1 \
		-c "GRANT USAGE ON SCHEMA app TO arena_backup;" \
		-c "GRANT USAGE ON SCHEMA app TO arena_observability;" \
		-c "GRANT SELECT ON ALL TABLES IN SCHEMA app TO arena_backup;" \
		-c "GRANT SELECT ON ALL TABLES IN SCHEMA app TO arena_observability;"
	password="$(generate_password)"
	psql_exec -v ON_ERROR_STOP=1 -c \
		"ALTER ROLE arena_app PASSWORD '$password';" >/dev/null
	echo "arena_app password (dev): $password"
	echo "roles provisioned: arena_app (login), arena_backup, arena_observability"
	;;
rotate)
	if ! role_exists arena_app; then
		echo "role arena_app does not exist; run provision first" >&2
		exit 1
	fi
	password="$(generate_password)"
	psql_exec -v ON_ERROR_STOP=1 -c \
		"ALTER ROLE arena_app PASSWORD '$password';" >/dev/null
	echo "arena_app rotated password (dev): $password"
	;;
*)
	echo "usage: scripts/dev-role-cycle.sh {provision|rotate}" >&2
	exit 64
	;;
esac
