-- +goose Up
-- 00059 archives sealed books and lets successors open one at a time
-- (P46-T10): the immutable archive receipt plus a current-active guard
-- that replaces the one-ever partial unique index. No diary leg,
-- custody, intention or hold is rewritten; old books keep their counts
-- and hashes byte for byte, so S never moves and no carry-over appears.
--
-- Invariants (docs/reino/TEMPORADAS_SUCESSAO.md §3–§4, §10):
-- 1. Additive only: one new receipt table plus one guard trigger. No
--    journal leg, custody, intention, hold, season or lifecycle row is
--    rewritten; the old diary keeps its counts and hashes, so S never
--    moves and no silent conversion or new mint appears.
-- 2. Archive is a receipt, not a rewrite: PRIMARY KEY(season_key) keeps
--    one receipt per sealed book; the row carries cutoff_at (database
--    clock), sealed_at, the Genesis S, leg and intention counts and the
--    64-hex manifesto digest. Adulterated checksums and diverged S never
--    archive: the opener verifies before writing.
-- 3. One current ACTIVE, not one ever: the 00054 partial unique index
--    allowed a single active row in table history, so a sealed
--    predecessor blocked every successor forever. It is replaced by a
--    BEFORE INSERT trigger that refuses a new active only while another
--    book's latest stage is still active. Sequential actives after a
--    seal proceed; concurrent actives still die with 23505, and the
--    per-book UNIQUE(season_key, to_state) still refuses repeated
--    stages. The trigger locks the lifecycle table to serialize the
--    race: two openers record exactly one ACTIVE.
-- 4. Fresh economy, never a copy: the successor Genesis runs once per
--    book in PREPARED; user balances are never copied. The opener
--    verifies Treasury == S and others == 0 before activating.
-- 5. History stays immutable: the receipt trigger reuses the season
--    append-only guard and refuses UPDATE and DELETE for every role
--    including the owner. Least privilege: arena_owner owns the
--    objects; arena_app reads receipts and inserts them once, never
--    updates or deletes them.
--
-- T11 owns the read contract; T12 owns the oracle. This migration never
-- activates a season and never carries PII.

DROP INDEX IF EXISTS app.season_lifecycle_single_active;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.season_lifecycle_guard_single_active() RETURNS trigger AS $$
BEGIN
    IF NEW.to_state = 'active' THEN
        LOCK TABLE app.season_lifecycle IN SHARE ROW EXCLUSIVE MODE;
        IF EXISTS (
            SELECT 1 FROM (
                SELECT DISTINCT ON (season_key) season_key, to_state
                FROM app.season_lifecycle
                ORDER BY season_key, recorded_at DESC, id DESC
            ) latest
            WHERE latest.to_state = 'active' AND latest.season_key <> NEW.season_key
        ) THEN
            RAISE EXCEPTION 'only one season may be active at a time'
                USING ERRCODE = '23505', CONSTRAINT = 'season_lifecycle_single_active_current';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS season_lifecycle_single_active_current ON app.season_lifecycle;
CREATE TRIGGER season_lifecycle_single_active_current
    BEFORE INSERT ON app.season_lifecycle
    FOR EACH ROW EXECUTE FUNCTION app.season_lifecycle_guard_single_active();

CREATE TABLE IF NOT EXISTS app.season_archives (
    season_key text PRIMARY KEY REFERENCES app.seasons(season_key) ON DELETE RESTRICT,
    cutoff_at timestamptz NOT NULL,
    sealed_at timestamptz NOT NULL,
    snapshot_milli bigint NOT NULL,
    snapshot_legs bigint NOT NULL DEFAULT 0,
    snapshot_intentions bigint NOT NULL DEFAULT 0,
    manifest_hash text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT season_archives_cutoff_check CHECK (cutoff_at <= sealed_at),
    CONSTRAINT season_archives_snapshot_check CHECK (snapshot_milli >= 0 AND snapshot_legs >= 0 AND snapshot_intentions >= 0),
    CONSTRAINT season_archives_manifest_check CHECK (manifest_hash ~ '^[0-9a-f]{64}$')
);

COMMENT ON TABLE app.season_archives IS 'Sealed book receipts: cutoff, seal instant, Genesis S, leg and intention counts and manifesto digest; one row per archived book, never rewritten';
COMMENT ON COLUMN app.season_archives.season_key IS 'Archived book: one receipt per sealed book, so two openers archive exactly once';
COMMENT ON COLUMN app.season_archives.cutoff_at IS 'Barrier instant on the database clock carried from the close run';
COMMENT ON COLUMN app.season_archives.sealed_at IS 'Seal instant from the lifecycle sealed stage';
COMMENT ON COLUMN app.season_archives.snapshot_milli IS 'Book Genesis S at the seal: opening requires it byte-equal, so nothing mints and nothing crosses';
COMMENT ON COLUMN app.season_archives.manifest_hash IS 'Sealed manifesto digest (64 lowercase hex): adulterated manifests never open a successor';

DROP TRIGGER IF EXISTS season_archives_immutable ON app.season_archives;
CREATE TRIGGER season_archives_immutable
    BEFORE UPDATE OR DELETE ON app.season_archives
    FOR EACH ROW EXECUTE FUNCTION app.seasons_forbid_mutation();

ALTER TABLE app.season_archives OWNER TO arena_owner;
ALTER FUNCTION app.season_lifecycle_guard_single_active() OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.season_archives TO arena_app;

-- +goose Down
REVOKE ALL ON app.season_archives FROM arena_app;
DROP TRIGGER IF EXISTS season_archives_immutable ON app.season_archives;
DROP TABLE IF EXISTS app.season_archives;
DROP TRIGGER IF EXISTS season_lifecycle_single_active_current ON app.season_lifecycle;
DROP FUNCTION IF EXISTS app.season_lifecycle_guard_single_active();
CREATE UNIQUE INDEX IF NOT EXISTS season_lifecycle_single_active
    ON app.season_lifecycle (to_state)
    WHERE to_state = 'active';
