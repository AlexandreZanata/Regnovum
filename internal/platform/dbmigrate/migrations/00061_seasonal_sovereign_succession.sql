-- +goose Up
-- 00061 records seasonal sovereign successions, reigns, and notification outbox (P47-T04)
-- Enforces exactly one active reign per season, linearizable evaluator checkpoints,
-- and atomic outbox buffering without moving money.
--
-- Invariants (docs/reino/TEMPORADAS_SUCESSAO.md §8):
-- 1. Single active reign: partial unique index seasonal_reigns_active_unique
--    ensures at most one active reign per season book.
-- 2. Monotonic reign versions: unique (season_id, reign_version) ensures
--    reign versions strictly increment and never rewrite history.
-- 3. Atomic transition: reign update/insertion and outbox event are committed
--    in the same transaction as evaluator progress.
-- 4. Checkpoints & leases: seasonal_succession_evaluators tracks last_evaluated_revision
--    and fences concurrent workers with timed leases and CAS semantics.

CREATE TABLE IF NOT EXISTS app.seasonal_reigns (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    season_id text NOT NULL REFERENCES app.seasons(season_key) ON DELETE RESTRICT,
    reign_version integer NOT NULL,
    holder_subject text NOT NULL,
    is_regent boolean NOT NULL DEFAULT false,
    reason text NOT NULL,
    institutional_c bigint NOT NULL DEFAULT 0,
    winning_wealth bigint NOT NULL DEFAULT 0,
    attained_revision bigint NOT NULL DEFAULT 0,
    transition_revision bigint NOT NULL DEFAULT 0,
    predecessor text NOT NULL DEFAULT '',
    is_active boolean NOT NULL DEFAULT true,
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT seasonal_reigns_reign_version_check CHECK (reign_version >= 1),
    CONSTRAINT seasonal_reigns_version_unique UNIQUE (season_id, reign_version),
    CONSTRAINT seasonal_reigns_reason_check CHECK (reason IN ('conquest', 'incumbent-retained', 'regency')),
    CONSTRAINT seasonal_reigns_c_check CHECK (institutional_c >= 0),
    CONSTRAINT seasonal_reigns_winning_wealth_check CHECK (winning_wealth >= 0),
    CONSTRAINT seasonal_reigns_attained_check CHECK (attained_revision >= 0),
    CONSTRAINT seasonal_reigns_transition_check CHECK (transition_revision >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS seasonal_reigns_active_unique
    ON app.seasonal_reigns (season_id)
    WHERE (is_active = true);

CREATE INDEX IF NOT EXISTS seasonal_reigns_holder_idx
    ON app.seasonal_reigns (season_id, holder_subject);

COMMENT ON TABLE app.seasonal_reigns IS 'Sovereign reigns: records active and historical monarchs/regents per season with strict single-active constraint';
COMMENT ON COLUMN app.seasonal_reigns.season_id IS 'Season book identity';
COMMENT ON COLUMN app.seasonal_reigns.reign_version IS 'Investiture sequence number within the season (starts at 1)';
COMMENT ON COLUMN app.seasonal_reigns.holder_subject IS 'Account identifier of the reigning monarch or regent';
COMMENT ON COLUMN app.seasonal_reigns.is_active IS 'True if this reign is the currently invested authority in this season';

CREATE TABLE IF NOT EXISTS app.seasonal_succession_events (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    season_id text NOT NULL REFERENCES app.seasons(season_key) ON DELETE RESTRICT,
    reign_version integer NOT NULL,
    event_kind text NOT NULL,
    successor_subject text NOT NULL,
    predecessor_subject text NOT NULL DEFAULT '',
    institutional_c bigint NOT NULL DEFAULT 0,
    winning_wealth bigint NOT NULL DEFAULT 0,
    transition_revision bigint NOT NULL DEFAULT 0,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT seasonal_succession_events_reign_version_check CHECK (reign_version >= 1),
    CONSTRAINT seasonal_succession_events_kind_check CHECK (event_kind IN ('conquest', 'incumbent-retained', 'regency'))
);

CREATE INDEX IF NOT EXISTS seasonal_succession_events_season_idx
    ON app.seasonal_succession_events (season_id, reign_version);

COMMENT ON TABLE app.seasonal_succession_events IS 'Linearizable notification outbox for sovereign successions, written atomically with reign transitions';

CREATE TABLE IF NOT EXISTS app.seasonal_succession_evaluators (
    season_id text PRIMARY KEY REFERENCES app.seasons(season_key) ON DELETE RESTRICT,
    last_evaluated_revision bigint NOT NULL DEFAULT 0,
    active_worker_id text NOT NULL DEFAULT '',
    lease_expires_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00Z',
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT seasonal_succession_evaluators_rev_check CHECK (last_evaluated_revision >= 0)
);

COMMENT ON TABLE app.seasonal_succession_evaluators IS 'Evaluator checkpoint and worker lease tracking for linearizable succession processing';

ALTER TABLE app.seasonal_reigns OWNER TO arena_owner;
ALTER TABLE app.seasonal_succession_events OWNER TO arena_owner;
ALTER TABLE app.seasonal_succession_evaluators OWNER TO arena_owner;

GRANT SELECT, INSERT, UPDATE, DELETE ON app.seasonal_reigns TO arena_app;
GRANT SELECT, INSERT, UPDATE ON app.seasonal_succession_events TO arena_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON app.seasonal_succession_evaluators TO arena_app;

-- +goose Down
DROP TABLE IF EXISTS app.seasonal_succession_evaluators;
DROP TABLE IF EXISTS app.seasonal_succession_events;
DROP TABLE IF EXISTS app.seasonal_reigns;
