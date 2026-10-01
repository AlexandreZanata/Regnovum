-- +goose Up
-- 00054 opens isolated season books without touching live money
-- (P46-T03): the immutable season registry, the append-only
-- lifecycle ledger with a single ACTIVE season at the constraint
-- level, and the per-season Genesis key replacing the global
-- singleton. Pre-season technical data receives the explicitly
-- inactive 'compat-legacy' namespace; no season is activated and no
-- credit is emitted here.
--
-- Invariants (docs/reino/TEMPORADAS_SUCESSAO.md §3–§4):
-- 1. Additive only: two new tables plus one new Genesis column. No
--    journal leg, custody, intention or hold is rewritten; the old
--    diary keeps its counts and hashes byte for byte, so S never
--    moves and no silent conversion or new mint appears.
-- 2. One Genesis per book: UNIQUE(season_key) replaces the global
--    singleton unique. A second Genesis in the same book dies on a
--    unique violation even under concurrency (THR-ECON-20); another
--    book keeps its own Genesis. Amount, Treasury destination and
--    immutability guards stay exactly as ratified.
-- 3. Single ACTIVE season: lifecycle state derives from append-only
--    events, never by rewriting a row; the partial unique index on
--    to_state = 'active' refuses a second active season, and
--    UNIQUE(season_key, to_state) refuses a repeated stage.
-- 4. No mixed books: lifecycle and Genesis rows reference
--    app.seasons(season_key) with ON DELETE RESTRICT, so a cross
--    reference to an unknown book dies on the foreign key.
-- 5. Exact ninety days: CHECK pins ends_at to starts_at plus
--    7.776.000 seconds; civil months, browser clocks and DST never
--    enter the calendar. The window is [starts_at, ends_at).
-- 6. Compatibility namespace: the pre-season Genesis row is bound
--    to 'compat-legacy' (ordinal 0, reserved) by column default,
--    without UPDATE of the historic row; the namespace carries no
--    lifecycle event and is never activated.
-- 7. History is retained and immutable as rows: the season registry
--    trigger refuses UPDATE and DELETE for every role including the
--    owner. Least privilege: arena_owner owns the objects;
--    arena_app reads seasons and lifecycle rows, never writes them.
--
-- Consumer inventory for P46-T05–T08 (NOT altered here): season
-- references for app.economy_custodies, app.economy_entries,
-- app.economy_intentions, app.economy_holds,
-- app.economy_charter_consents, app.economy_optins and
-- app.economy_partitions, plus the billing, pricing, metering,
-- commerce and crumbs consumer tables, land in their own additive
-- migrations with per-family tests.

CREATE TABLE IF NOT EXISTS app.seasons (
    season_key text PRIMARY KEY,
    ordinal integer NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    charter_version text NOT NULL,
    policy_ref text NOT NULL,
    initial_monarch text NOT NULL,
    regent text NOT NULL,
    manifest_hash text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT seasons_key_check CHECK (trim(season_key) <> ''),
    CONSTRAINT seasons_ordinal_check CHECK (ordinal >= 0),
    CONSTRAINT seasons_ordinal_unique UNIQUE (ordinal),
    CONSTRAINT seasons_window_check CHECK (ends_at = starts_at + make_interval(secs => 7776000)),
    CONSTRAINT seasons_charter_check CHECK (charter_version ~ '^v[0-9]+$'),
    CONSTRAINT seasons_policy_check CHECK (trim(policy_ref) <> ''),
    CONSTRAINT seasons_monarch_check CHECK (trim(initial_monarch) <> ''),
    CONSTRAINT seasons_regent_check CHECK (trim(regent) <> ''),
    CONSTRAINT seasons_manifest_check CHECK (manifest_hash ~ '^[0-9a-f]{64}$')
);

COMMENT ON TABLE app.seasons IS 'Immutable season plan registry: key, ordinal, exact ninety-day UTC window, charter, policy, initial holder data and sealed manifesto; never activated here';
COMMENT ON COLUMN app.seasons.season_key IS 'Immutable book identity (INK@season_id): pre-season technical data uses the explicitly inactive compat-legacy namespace';
COMMENT ON COLUMN app.seasons.ordinal IS 'Calendar order from 1; 0 is reserved for the explicitly inactive pre-season compatibility namespace';
COMMENT ON COLUMN app.seasons.ends_at IS 'Exclusive end, always starts_at plus 7776000 seconds: civil months and DST never enter the calendar';
COMMENT ON COLUMN app.seasons.manifest_hash IS 'Sealed manifesto digest (64 lowercase hex): altered manifests never open a season';

CREATE TABLE IF NOT EXISTS app.season_lifecycle (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    season_key text NOT NULL REFERENCES app.seasons(season_key) ON DELETE RESTRICT,
    from_state text NULL,
    to_state text NOT NULL,
    decided_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT season_lifecycle_from_check CHECK (from_state IS NULL OR from_state IN ('prepared', 'active', 'closing', 'sealed', 'archived')),
    CONSTRAINT season_lifecycle_to_check CHECK (to_state IN ('prepared', 'active', 'closing', 'sealed', 'archived')),
    CONSTRAINT season_lifecycle_stage_unique UNIQUE (season_key, to_state)
);

COMMENT ON TABLE app.season_lifecycle IS 'Append-only season stage events: state derives from the latest event, never by rewriting; at most one active season exists';
COMMENT ON COLUMN app.season_lifecycle.from_state IS 'Previous stage, NULL on the opening prepared event';
COMMENT ON COLUMN app.season_lifecycle.to_state IS 'New stage: a second active season dies on the partial unique index';

CREATE UNIQUE INDEX IF NOT EXISTS season_lifecycle_single_active
    ON app.season_lifecycle (to_state)
    WHERE to_state = 'active';

CREATE INDEX IF NOT EXISTS season_lifecycle_season_idx ON app.season_lifecycle (season_key, recorded_at);

-- +goose StatementBegin
-- The registry and its lifecycle are facts of the calendar: nothing
-- about them is ever edited or erased. The trigger fires for every
-- role including the owner; the 23514 code surfaces the refusal as
-- a constraint violation, like the economy ledger guard.
CREATE OR REPLACE FUNCTION app.seasons_forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the season registry is append-only'
        USING ERRCODE = '23514', CONSTRAINT = 'seasons_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS seasons_immutable ON app.seasons;
CREATE TRIGGER seasons_immutable
    BEFORE UPDATE OR DELETE ON app.seasons
    FOR EACH ROW EXECUTE FUNCTION app.seasons_forbid_mutation();

DROP TRIGGER IF EXISTS season_lifecycle_immutable ON app.season_lifecycle;
CREATE TRIGGER season_lifecycle_immutable
    BEFORE UPDATE OR DELETE ON app.season_lifecycle
    FOR EACH ROW EXECUTE FUNCTION app.seasons_forbid_mutation();

-- The explicitly inactive compatibility namespace for pre-season
-- technical data: ordinal 0, carrying no lifecycle event, never
-- activated. The existing Genesis row binds to it by column default
-- below, without rewriting history.
INSERT INTO app.seasons
    (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
VALUES
    ('compat-legacy', 0, '2026-01-01T00:00:00Z', '2026-04-01T00:00:00Z', 'v0', 'compat-inactive', 'compat-none', 'compat-none',
     '0000000000000000000000000000000000000000000000000000000000000000')
ON CONFLICT (season_key) DO NOTHING;

-- One Genesis per book: the new key defaults to the compatibility
-- namespace so the historic row is preserved untouched and legacy
-- writers keep binding the inactive book. The global singleton
-- unique is replaced by per-season uniqueness; amount, Treasury
-- and immutability guards stay exactly as ratified. Per-season
-- Genesises name their book explicitly; making the season
-- mandatory at the port is P46-T04, which also keeps every
-- existing suite green without touching it.
ALTER TABLE app.economy_genesis
    ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT 'compat-legacy' REFERENCES app.seasons(season_key) ON DELETE RESTRICT;

ALTER TABLE app.economy_genesis DROP CONSTRAINT IF EXISTS economy_genesis_singleton_unique;

ALTER TABLE app.economy_genesis
    ADD CONSTRAINT economy_genesis_season_unique UNIQUE (season_key);

COMMENT ON COLUMN app.economy_genesis.season_key IS 'Book identity of this Genesis: one Genesis per season, never twice in one book';

ALTER TABLE app.seasons OWNER TO arena_owner;
ALTER TABLE app.season_lifecycle OWNER TO arena_owner;
ALTER FUNCTION app.seasons_forbid_mutation() OWNER TO arena_owner;

GRANT SELECT ON app.seasons TO arena_app;
GRANT SELECT ON app.season_lifecycle TO arena_app;

-- +goose Down
ALTER TABLE app.economy_genesis DROP CONSTRAINT IF EXISTS economy_genesis_season_unique;
ALTER TABLE app.economy_genesis ADD CONSTRAINT economy_genesis_singleton_unique UNIQUE (singleton);
ALTER TABLE app.economy_genesis DROP COLUMN IF EXISTS season_key;
DROP TRIGGER IF EXISTS season_lifecycle_immutable ON app.season_lifecycle;
DROP TRIGGER IF EXISTS seasons_immutable ON app.seasons;
DROP FUNCTION IF EXISTS app.seasons_forbid_mutation();
DROP TABLE IF EXISTS app.season_lifecycle;
DELETE FROM app.seasons WHERE season_key = 'compat-legacy';
DROP TABLE IF EXISTS app.seasons;
