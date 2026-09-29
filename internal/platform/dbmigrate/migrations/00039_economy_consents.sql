-- +goose Up
-- 00039 records charter consent and conversion opt-in intents (P33-T03):
-- app.economy_charter_consents versions every acceptance and refusal per
-- account and charter, and app.economy_optins records the explicit
-- economic opt-in with its quantity, rate and validity. Nothing here
-- moves value: conversion itself arrives in a later task, and refusal
-- preserves history, export, recourse and settlement by leaving every
-- book untouched.
--
-- Invariants (docs/reino/RESPOSTAS.md item 5, Q05 proposta):
-- 1. One decision per account and charter version: the unique guard
--    refuses a second verdict, so acceptance can never be rewritten
--    into refusal or the reverse.
-- 2. No consent without an account: the foreign key ties every record
--    to a real holder, so orphan intents cannot exist.
-- 3. Opt-in carries its exact terms: positive quantity and rate pair
--    with a future validity bound. Terms are matched verbatim at
--    conversion time, never rounded or reinterpreted.
-- 4. Both registries are append-only: triggers refuse UPDATE and DELETE
--    for every role including the owner. A changed mind is a new row
--    under a new charter version, never an edit.
-- 5. Least privilege: arena_app reads and appends consents and intents,
--    never rewrites them.

CREATE TABLE IF NOT EXISTS app.economy_charter_consents (
    account_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    charter_version text NOT NULL,
    decision text NOT NULL,
    decided_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT economy_charter_consents_version_check CHECK (charter_version ~ '^v[1-9][0-9]*$'),
    CONSTRAINT economy_charter_consents_decision_check CHECK (decision IN ('accepted', 'refused')),
    CONSTRAINT economy_charter_consents_account_version_unique UNIQUE (account_id, charter_version)
);

COMMENT ON TABLE app.economy_charter_consents IS 'Versioned charter verdicts per account: acceptance unlocks new activities, refusal preserves history, export, recourse and settlement';
COMMENT ON COLUMN app.economy_charter_consents.charter_version IS 'Charter version as vN: acceptances never carry across versions silently';

CREATE TABLE IF NOT EXISTS app.economy_optins (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    account_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    charter_version text NOT NULL,
    quantity_milli bigint NOT NULL,
    rate_num bigint NOT NULL,
    rate_den bigint NOT NULL,
    valid_until timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT economy_optins_version_check CHECK (charter_version ~ '^v[1-9][0-9]*$'),
    CONSTRAINT economy_optins_quantity_check CHECK (quantity_milli BETWEEN 1 AND 2100000000000),
    CONSTRAINT economy_optins_rate_check CHECK (rate_num > 0 AND rate_den > 0),
    CONSTRAINT economy_optins_account_version_unique UNIQUE (account_id, charter_version)
);

CREATE INDEX IF NOT EXISTS economy_optins_account_idx ON app.economy_optins (account_id, created_at DESC);

COMMENT ON TABLE app.economy_optins IS 'Explicit economic opt-in intents: quantity, conversion rate and validity recorded verbatim for exact matching at conversion; one intent per account and charter version';
COMMENT ON COLUMN app.economy_optins.rate_num IS 'Conversion rate numerator in milliINK per legacy INK unit, with rate_den: exact rational, never rounded';
COMMENT ON COLUMN app.economy_optins.rate_den IS 'Conversion rate denominator in legacy INK units';

-- +goose StatementBegin
-- Consent and intent are speech acts with a timestamp: editing one would
-- rewrite what the holder said, so every mutation dies here for every
-- role including the owner.
CREATE OR REPLACE FUNCTION app.economy_consents_forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'charter consent and opt-in records are final'
        USING ERRCODE = '23514', CONSTRAINT = 'economy_consents_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS economy_charter_consents_immutable ON app.economy_charter_consents;
CREATE TRIGGER economy_charter_consents_immutable
    BEFORE UPDATE OR DELETE ON app.economy_charter_consents
    FOR EACH ROW EXECUTE FUNCTION app.economy_consents_forbid_mutation();

DROP TRIGGER IF EXISTS economy_optins_immutable ON app.economy_optins;
CREATE TRIGGER economy_optins_immutable
    BEFORE UPDATE OR DELETE ON app.economy_optins
    FOR EACH ROW EXECUTE FUNCTION app.economy_consents_forbid_mutation();

ALTER TABLE app.economy_charter_consents OWNER TO arena_owner;
ALTER TABLE app.economy_optins OWNER TO arena_owner;
ALTER FUNCTION app.economy_consents_forbid_mutation() OWNER TO arena_owner;

-- Runtime surface. Consents and intents are recorded and read by the
-- application; history is never rewritten.
GRANT SELECT, INSERT ON app.economy_charter_consents TO arena_app;
GRANT SELECT, INSERT ON app.economy_optins TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS economy_optins_immutable ON app.economy_optins;
DROP TRIGGER IF EXISTS economy_charter_consents_immutable ON app.economy_charter_consents;
DROP FUNCTION IF EXISTS app.economy_consents_forbid_mutation();
DROP TABLE IF EXISTS app.economy_optins;
DROP TABLE IF EXISTS app.economy_charter_consents;
