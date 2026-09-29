-- +goose Up
-- 00034 records the single Genesis attestation (P32-T03):
-- app.economy_genesis binds one event key to the Treasury custody and the
-- fixed supply, and refuses every second event at the constraint level.
--
-- Invariants (docs/reino/LEDGER_CONTRACT.md §3, docs/THREAT_MODEL.md §5.8):
-- 1. Exactly one Genesis ever exists: the singleton column admits a single
--    row, so a second event with any other key dies on a unique violation
--    even under concurrency (THR-ECON-20).
-- 2. The attested amount is always S: the CHECK pins 2100000000000
--    milliINK, so no partial or short Genesis can be recorded.
-- 3. The destination is always the Treasury: the foreign key points at an
--    economy custody created by the guarded initializer, never at a user.
-- 4. Attestation is append-only: the trigger refuses UPDATE and DELETE for
--    every role including the owner.
-- 5. Least privilege: arena_app can read the attestation (SELECT) but can
--    never write it; only the guarded initializer running as the schema
--    owner inserts the single row. Production startup never runs it.

CREATE TABLE IF NOT EXISTS app.economy_genesis (
    genesis_key text PRIMARY KEY,
    treasury_custody_id uuid NOT NULL REFERENCES app.economy_custodies(id) ON DELETE RESTRICT,
    amount_milli bigint NOT NULL,
    singleton boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT economy_genesis_amount_supply_check CHECK (amount_milli = 2100000000000),
    CONSTRAINT economy_genesis_singleton_check CHECK (singleton),
    CONSTRAINT economy_genesis_singleton_unique UNIQUE (singleton),
    CONSTRAINT economy_genesis_key_check CHECK (trim(genesis_key) <> '')
);

COMMENT ON TABLE app.economy_genesis IS 'Single Genesis attestation: one event key, the Treasury custody and exactly S milliINK; every second event is refused';
COMMENT ON COLUMN app.economy_genesis.genesis_key IS 'Idempotency key of the creation event: replays resolve to this row instead of minting again';
COMMENT ON COLUMN app.economy_genesis.singleton IS 'Always true and unique: the constraint that makes a second Genesis impossible';

-- +goose StatementBegin
-- The attestation is the birth certificate of the supply: it is written
-- once and never edited or erased.
CREATE OR REPLACE FUNCTION app.economy_genesis_forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the genesis attestation is final'
        USING ERRCODE = '23514', CONSTRAINT = 'economy_genesis_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS economy_genesis_immutable ON app.economy_genesis;
CREATE TRIGGER economy_genesis_immutable
    BEFORE UPDATE OR DELETE ON app.economy_genesis
    FOR EACH ROW EXECUTE FUNCTION app.economy_genesis_forbid_mutation();

ALTER TABLE app.economy_genesis OWNER TO arena_owner;
ALTER FUNCTION app.economy_genesis_forbid_mutation() OWNER TO arena_owner;

-- Runtime surface. The application reads the attestation to prove Genesis
-- happened; it never writes it (THR-ECON-20).
GRANT SELECT ON app.economy_genesis TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS economy_genesis_immutable ON app.economy_genesis;
DROP FUNCTION IF EXISTS app.economy_genesis_forbid_mutation();
DROP TABLE IF EXISTS app.economy_genesis;
