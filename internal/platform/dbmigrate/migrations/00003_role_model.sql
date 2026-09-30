-- +goose Up
-- 00003 establishes the least-privilege role model of Regnovum
-- (P03-T03) inside a single forward migration, so the database ships its
-- security policy atomically with the schema it protects. Full role
-- documentation lives in docs/ROLE_MODEL.md.
--
-- Role model:
--   arena_owner     owns every object; DDL authority; NOLOGIN. The
--                   bootstrap superuser relinquishes ownership after the
--                   schema is provisioned and never serves application
--                   traffic.
--   arena_migrator  runs the forward-only migration workflow; membership
--                   in arena_owner (SET role) is transient and exits with
--                   the migration session.
--   arena_app       the runtime of the application: data DML inside the
--                   app schema only. It cannot create or alter schema,
--                   cannot touch the migration history and is never a
--                   superuser.
--
-- Credentials are managed by the operator (ALTER ROLE ... PASSWORD), or
-- through scripts/dev-role-cycle.sh; passwords are never hardcoded here.
--
-- Idempotency: DO blocks guard the CREATE ROLE statements so a database
-- restored from a snapshot that already has the roles re-runs safely.
-- goose needs StatementBegin/StatementEnd around the multi-line DO blocks
-- so the dollar-quoted bodies parse as single statements.
--
-- Concurrency: roles are CLUSTER-global (they live in the shared catalog,
-- not in the database being migrated) while a migration runs per database, so
-- the IF NOT EXISTS check above is a check-then-act on state another session
-- can change in between. Two sessions migrating the same cluster at once —
-- two instances running migrations at boot, or the test harness provisioning
-- scratch databases in parallel — both pass the check and one of them aborts
-- with:
--
--   duplicate key value violates unique constraint "pg_authid_rolname_index"
--
-- (reproduced against a fresh cluster; the failing statement is the CREATE
-- ROLE inside this block).
--
-- A PostgreSQL advisory lock cannot serialize that, and it is worth recording
-- why so nobody tries it again: advisory locks are scoped to the database
-- that takes them. Verified directly — a lock taken on a key in one database
-- is granted immediately to another database of the same cluster. So the
-- guard is made atomic instead of mutually exclusive: the presence check
-- stays as the fast path, and each CREATE ROLE catches the duplicate that a
-- concurrent session may have committed in the meantime. The effect is
-- CREATE ROLE IF NOT EXISTS, which PostgreSQL does not provide.
--
-- A future migration that touches a cluster-global object must be written the
-- same way: idempotent under concurrent execution, not merely guarded.

-- +goose StatementBegin
DO $$
BEGIN
    -- arena_owner owns every object; the bootstrap superuser relinquishes
    -- ownership below and never serves application traffic.
    BEGIN
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'arena_owner') THEN
            CREATE ROLE arena_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
        END IF;
    EXCEPTION WHEN duplicate_object OR unique_violation THEN
        -- Another session in this cluster created it between the check and
        -- the create. The role exists, which is all this migration needs.
        NULL;
    END;

    -- arena_app is the ONLY login role the migration creates: the runtime
    -- authenticates with credentials the operator manages (scripts/
    -- dev-role-cycle.sh in development, the secret store in production).
    BEGIN
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'arena_app') THEN
            CREATE ROLE arena_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
        END IF;
    EXCEPTION WHEN duplicate_object OR unique_violation THEN
        NULL;
    END;

    BEGIN
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'arena_migrator') THEN
            CREATE ROLE arena_migrator NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION
                IN ROLE arena_owner;
        END IF;
    EXCEPTION WHEN duplicate_object OR unique_violation THEN
        NULL;
    END;
END
$$;
-- +goose StatementEnd

-- Ownership of the existing objects moves from the bootstrap superuser to
-- the NOLOGIN owner; membership grants migrator its transient DDL powers.
ALTER SCHEMA app OWNER TO arena_owner;
ALTER TABLE app.schema_metadata OWNER TO arena_owner;

GRANT USAGE ON SCHEMA app TO arena_app;
GRANT SELECT ON app.schema_metadata TO arena_app;

-- +goose Down
-- The Down section restores the pre-00003 shape: ownership back to the
-- bootstrap role and the role model removed. It is not part of the
-- production path (migrations are forward-only); it exists so a migration
-- can be tested cleanly on a scratch database.
--
-- Dropping is cluster-global for the same reason creating is, so each drop
-- catches the case where a concurrent session removed the role between the
-- check and the drop. Only that case is swallowed: a role that still owns
-- objects must keep failing loudly, because that is a real problem and not a
-- race (dependent_objects_still_exist is deliberately not caught).
-- +goose StatementBegin
DO $$
BEGIN
    BEGIN
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'arena_migrator') THEN
            DROP ROLE arena_migrator;
        END IF;
    EXCEPTION WHEN undefined_object THEN
        NULL;
    END;

    BEGIN
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'arena_app') THEN
            DROP ROLE arena_app;
        END IF;
    EXCEPTION WHEN undefined_object THEN
        NULL;
    END;

    BEGIN
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'arena_owner') THEN
            DROP ROLE arena_owner;
        END IF;
    EXCEPTION WHEN undefined_object THEN
        NULL;
    END;
END
$$;
-- +goose StatementEnd

ALTER TABLE app.schema_metadata OWNER TO current_user;
ALTER SCHEMA app OWNER TO current_user;
