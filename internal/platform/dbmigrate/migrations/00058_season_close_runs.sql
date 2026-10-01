-- +goose Up
-- 00058 records the seasonal closing barrier without moving money
-- (P46-T09): one close run per book with monotonic generation,
-- lease ownership, checkpoint cursors, progress hints and a
-- conservation snapshot. The barrier itself stays in
-- app.season_lifecycle (the closing row refuses new admissions in
-- every family adapter inside the writing transaction); this table
-- fences which worker may drain and proves what the cutoff saw.
--
-- Invariants (docs/reino/TEMPORADAS_SUCESSAO.md §5–§5.1):
-- 1. One run per book: PRIMARY KEY(season_key) lets two closers race
--    with exactly one winner; the loser replays the stored run in the
--    same generation instead of opening a second barrier.
-- 2. Generation fencing: writers carry (season, generation, owner);
--    every mutation checks the stored generation in-transaction, so a
--    stale worker after a newer generation moves nothing. Takeover
--    needs an expired lease and bumps the generation by exactly one.
-- 3. Lease before work: the owner holds the book only until
--    leased_until on the database clock; a held lease refuses
--    takeover, an expired one allows it, and lock order is always the
--    run row first, then item rows, with no network under lock.
-- 4. Checkpoints are cursors, not truth: last_hold_key and
--    last_escrow_key advance idempotently (keyset, never sequence or
--    timestamp order); drained/blocked are progress hints while Seal
--    decides by live scan, so a crash before commit replays the same
--    batch and a crash after resumes past it.
-- 5. Conservation snapshot: cutoff_at (database clock), snapshot_milli
--    (the book Genesis S), snapshot_legs and snapshot_intentions are
--    captured at Begin. S must read equal at Seal (no mint, no second
--    Genesis); legs and intentions may only grow (admitted in-flight
--    work completes once in the old book, never twice, never crossed).
-- 6. Failure is durable: RecordFailure keeps the cursor, stores the
--    machine code and detail, and parks the run in failed; a resumed
--    worker replays from the cursor, and terminal settlement guards in
--    each family still allow each effect exactly once.
-- 7. Least privilege: arena_app reads, inserts and advances runs, never
--    deletes them; arena_owner owns the objects. No money moves here:
--    legs are written only by the family drain paths under a valid
--    lease, and liquidation never enters the competitive ranking.
--
-- T10 owns the archive seal and the successor opening; this table
-- never activates a season and never carries PII.

CREATE TABLE IF NOT EXISTS app.season_close_runs (
    season_key text PRIMARY KEY REFERENCES app.seasons(season_key) ON DELETE RESTRICT,
    generation bigint NOT NULL DEFAULT 1,
    lease_owner text NOT NULL,
    leased_until timestamptz NOT NULL,
    state text NOT NULL DEFAULT 'closing',
    cutoff_at timestamptz NOT NULL,
    last_hold_key text NOT NULL DEFAULT '',
    last_escrow_key text NOT NULL DEFAULT '',
    drained bigint NOT NULL DEFAULT 0,
    blocked bigint NOT NULL DEFAULT 0,
    snapshot_milli bigint NOT NULL,
    snapshot_legs bigint NOT NULL DEFAULT 0,
    snapshot_intentions bigint NOT NULL DEFAULT 0,
    error_code text NOT NULL DEFAULT '',
    error_detail text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT season_close_runs_generation_check CHECK (generation >= 1),
    CONSTRAINT season_close_runs_owner_check CHECK (trim(lease_owner) <> ''),
    CONSTRAINT season_close_runs_state_check CHECK (state IN ('closing', 'sealed', 'failed')),
    CONSTRAINT season_close_runs_counters_check CHECK (drained >= 0 AND blocked >= 0),
    CONSTRAINT season_close_runs_snapshot_check CHECK (snapshot_milli >= 0 AND snapshot_legs >= 0 AND snapshot_intentions >= 0)
);

COMMENT ON TABLE app.season_close_runs IS 'Seasonal closing barrier runs: one fenced run per book with generation, lease, checkpoint cursors and the cutoff conservation snapshot; money never moves here';
COMMENT ON COLUMN app.season_close_runs.season_key IS 'Book under closing: one run per book, so two closers open exactly one barrier';
COMMENT ON COLUMN app.season_close_runs.generation IS 'Monotonic fence: every drain write carries it, stale workers move nothing, takeover bumps it by one past an expired lease';
COMMENT ON COLUMN app.season_close_runs.lease_owner IS 'Opaque worker holding the book until leased_until on the database clock';
COMMENT ON COLUMN app.season_close_runs.cutoff_at IS 'Barrier instant on the database clock: admissions checked after it refuse, admitted in-flight work still completes once';
COMMENT ON COLUMN app.season_close_runs.last_hold_key IS 'Checkpoint cursor over economy holds in key order: crash before commit replays, crash after resumes past it';
COMMENT ON COLUMN app.season_close_runs.last_escrow_key IS 'Checkpoint cursor over commerce escrows in key order, with the same replay promise';
COMMENT ON COLUMN app.season_close_runs.snapshot_milli IS 'Book Genesis S at the barrier: Seal requires it byte-equal, so nothing mints and nothing crosses';

ALTER TABLE app.season_close_runs OWNER TO arena_owner;

GRANT SELECT, INSERT, UPDATE ON app.season_close_runs TO arena_app;

-- +goose Down
DROP TABLE IF EXISTS app.season_close_runs;
