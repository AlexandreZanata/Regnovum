-- +goose Up
-- 00060 indexes rebuildable seasonal wealth and records outbox revisions
-- without moving money (P47-T02): app.seasonal_wealth_projections indexes
-- net beneficial wealth W(b) with (season_id, wealth DESC, attained_revision, subject_id),
-- app.seasonal_wealth_events provides the linearizable revision outbox,
-- and app.seasonal_wealth_checkpoints tracks rebuild freshness. The diary
-- remains authoritative; projections are rebuildable derived states.
--
-- Invariants (docs/reino/TEMPORADAS_SUCESSAO.md §7–§8):
-- 1. Additive only: four new tables for projection, outbox, obligations
--    and checkpoints. No journal leg, custody, intention or season row
--    is rewritten; S never moves and no balance is manually replaced.
-- 2. Ranking index: (season_id, wealth DESC, attained_revision, subject_id)
--    allows top-1 and leader queries to execute via index scan without
--    scanning all participant accounts on each transfer.
-- 3. Checkpoints and outbox: revisions advance monotonically per season;
--    events carry the exact deltas committed in the same financial
--    transaction. Uncommitted transactions leave no trace.
-- 4. Invalidation: freeze, debt, and disqualification invalidate or
--    fence projections; gaps and stale revisions halt royal act evaluation.
-- 5. Least privilege: arena_owner owns the objects; arena_app reads,
--    inserts and updates projection rows, but cannot delete immutable
--    outbox events.
--

CREATE TABLE IF NOT EXISTS app.seasonal_wealth_projections (
    season_id text NOT NULL REFERENCES app.seasons(season_key) ON DELETE RESTRICT,
    subject_id text NOT NULL,
    beneficiary_kind text NOT NULL DEFAULT 'participante',
    assets_milli bigint NOT NULL DEFAULT 0,
    liabilities_milli bigint NOT NULL DEFAULT 0,
    wealth bigint NOT NULL DEFAULT 0,
    attained_revision bigint NOT NULL DEFAULT 0,
    frozen boolean NOT NULL DEFAULT false,
    eligible boolean NOT NULL DEFAULT true,
    source_watermark timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (season_id, subject_id),
    CONSTRAINT seasonal_wealth_projections_assets_check CHECK (assets_milli >= 0),
    CONSTRAINT seasonal_wealth_projections_liabilities_check CHECK (liabilities_milli >= 0),
    CONSTRAINT seasonal_wealth_projections_wealth_check CHECK (wealth >= 0),
    CONSTRAINT seasonal_wealth_projections_attained_check CHECK (attained_revision >= 0),
    CONSTRAINT seasonal_wealth_projections_kind_check CHECK (beneficiary_kind IN ('participante', 'institucional', 'tecnica'))
);

CREATE INDEX IF NOT EXISTS seasonal_wealth_ranking_idx
    ON app.seasonal_wealth_projections (season_id, wealth DESC, attained_revision, subject_id);

COMMENT ON TABLE app.seasonal_wealth_projections IS 'Rebuildable seasonal wealth projections: indexes W(b) without replacing the authoritative journal';
COMMENT ON COLUMN app.seasonal_wealth_projections.season_id IS 'Season book identity';
COMMENT ON COLUMN app.seasonal_wealth_projections.subject_id IS 'Beneficiary subject account identifier';
COMMENT ON COLUMN app.seasonal_wealth_projections.wealth IS 'Derived beneficial wealth W(b) = max(0, assets - liabilities)';
COMMENT ON COLUMN app.seasonal_wealth_projections.attained_revision IS 'Revision when current wealth level was attained; breaks ties by seniority';

CREATE TABLE IF NOT EXISTS app.seasonal_wealth_events (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    season_id text NOT NULL REFERENCES app.seasons(season_key) ON DELETE RESTRICT,
    revision bigint NOT NULL,
    subject_id text NOT NULL,
    beneficiary_kind text NOT NULL DEFAULT 'participante',
    event_kind text NOT NULL,
    delta_assets bigint NOT NULL DEFAULT 0,
    delta_liabilities bigint NOT NULL DEFAULT 0,
    source_ref text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT seasonal_wealth_events_revision_check CHECK (revision >= 1),
    CONSTRAINT seasonal_wealth_events_revision_unique UNIQUE (season_id, revision),
    CONSTRAINT seasonal_wealth_events_kind_check CHECK (event_kind IN ('genesis', 'transfer', 'debt', 'reserve', 'refund', 'freeze', 'eligibility'))
);

CREATE INDEX IF NOT EXISTS seasonal_wealth_events_subject_idx
    ON app.seasonal_wealth_events (season_id, subject_id, revision);

COMMENT ON TABLE app.seasonal_wealth_events IS 'Linearizable outbox of committed wealth changes, updated within financial transactions';

CREATE TABLE IF NOT EXISTS app.seasonal_wealth_obligations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    season_id text NOT NULL REFERENCES app.seasons(season_key) ON DELETE RESTRICT,
    debtor_subject text NOT NULL,
    obligation_kind text NOT NULL,
    amount_milli bigint NOT NULL,
    already_deducted boolean NOT NULL DEFAULT false,
    source_ref text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT seasonal_wealth_obligations_amount_check CHECK (amount_milli >= 0),
    CONSTRAINT seasonal_wealth_obligations_kind_check CHECK (obligation_kind IN ('registered-loan', 'encumbrance', 'third-party-custody', 'internal-reserve'))
);

CREATE INDEX IF NOT EXISTS seasonal_wealth_obligations_debtor_idx
    ON app.seasonal_wealth_obligations (season_id, debtor_subject);

COMMENT ON TABLE app.seasonal_wealth_obligations IS 'Confirmed registered seasonal debts and obligations contributing to L(b)';

CREATE TABLE IF NOT EXISTS app.seasonal_wealth_checkpoints (
    season_id text PRIMARY KEY REFERENCES app.seasons(season_key) ON DELETE RESTRICT,
    last_revision bigint NOT NULL DEFAULT 0,
    checkpoint_hash text NOT NULL DEFAULT '',
    frozen boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT seasonal_wealth_checkpoints_rev_check CHECK (last_revision >= 0)
);

COMMENT ON TABLE app.seasonal_wealth_checkpoints IS 'Checkpoint tracking last processed revision and integrity hash per season book';

ALTER TABLE app.seasonal_wealth_projections OWNER TO arena_owner;
ALTER TABLE app.seasonal_wealth_events OWNER TO arena_owner;
ALTER TABLE app.seasonal_wealth_obligations OWNER TO arena_owner;
ALTER TABLE app.seasonal_wealth_checkpoints OWNER TO arena_owner;

GRANT SELECT, INSERT, UPDATE, DELETE ON app.seasonal_wealth_projections TO arena_app;
GRANT SELECT, INSERT ON app.seasonal_wealth_events TO arena_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON app.seasonal_wealth_obligations TO arena_app;
GRANT SELECT, INSERT, UPDATE ON app.seasonal_wealth_checkpoints TO arena_app;

-- +goose Down
DROP TABLE IF EXISTS app.seasonal_wealth_checkpoints;
DROP TABLE IF EXISTS app.seasonal_wealth_obligations;
DROP TABLE IF EXISTS app.seasonal_wealth_events;
DROP TABLE IF EXISTS app.seasonal_wealth_projections;
