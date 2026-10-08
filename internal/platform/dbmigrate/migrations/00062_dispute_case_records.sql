-- +goose Up
-- 00062 stores staged private case records (P56-T03).
-- The disputes lifecycle (P39-T08) kept its records in memory; the
-- staged harness needs the filed evidence to survive a reload, so
-- this table persists the sealed CaseRecord envelope per
-- negotiation key. It creates no tribunal, rule or power: the
-- domain structs stay the authority, JSONB is only the envelope,
-- and the writers are the same use cases the memory store served.
--
-- One row per negotiation key: Put overwrites the envelope, Get
-- reads it back. Unknown keys read as absent through the domain
-- error, so strangers learn nothing.

CREATE TABLE IF NOT EXISTS app.dispute_cases (
    key text PRIMARY KEY,
    record jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT dispute_cases_key_check CHECK (char_length(key) BETWEEN 1 AND 128)
);

COMMENT ON TABLE app.dispute_cases IS 'Staged private case records: one sealed CaseRecord envelope per negotiation key (P56-T03)';
COMMENT ON COLUMN app.dispute_cases.key IS 'Negotiation key the parties address, at most 128 characters';
COMMENT ON COLUMN app.dispute_cases.record IS 'Sealed CaseRecord envelope: proposal, entry, hearing and decision as filed';

ALTER TABLE app.dispute_cases OWNER TO arena_owner;

GRANT SELECT, INSERT, UPDATE ON app.dispute_cases TO arena_app;

-- +goose Down
DROP TABLE IF EXISTS app.dispute_cases;
