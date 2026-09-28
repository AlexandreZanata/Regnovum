-- +goose Up
-- 00035 records monetary intentions with their persisted responses
-- (P32-T05): app.economy_intentions binds one (intention key, actor,
-- operation) triple to a payload hash and the transfer that settled it.
--
-- Invariants (docs/reino/LEDGER_CONTRACT.md §2, THR-ECON-22/30):
-- 1. One intention settles at most once: the triple is unique, so a retry
--    after a post-commit timeout resolves the stored response instead of
--    writing new legs (cardinality 0 or 1, never 2).
-- 2. A different payload under the same triple is a conflict, not a
--    replay: the stored hash tells them apart, and reuse is refused.
-- 3. The recorded response is exact: the transfer id and millis stored are
--    the legs committed in the same transaction, never reconstructed.
-- 4. Intentions are append-only: the trigger refuses UPDATE and DELETE for
--    every role including the owner.
-- 5. Least privilege: arena_app reads and appends intentions, never
--    rewrites them.

CREATE TABLE IF NOT EXISTS app.economy_intentions (
    intention_key text NOT NULL,
    actor text NOT NULL,
    operation text NOT NULL,
    payload_hash text NOT NULL,
    transfer_id uuid NOT NULL,
    amount_milli bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT economy_intentions_key_check CHECK (trim(intention_key) <> ''),
    CONSTRAINT economy_intentions_actor_check CHECK (trim(actor) <> ''),
    CONSTRAINT economy_intentions_operation_check CHECK (trim(operation) <> ''),
    CONSTRAINT economy_intentions_payload_check CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT economy_intentions_amount_range_check CHECK (amount_milli BETWEEN 1 AND 2100000000000),
    CONSTRAINT economy_intentions_transfer_unique UNIQUE (transfer_id),
    CONSTRAINT economy_intentions_triple_unique UNIQUE (intention_key, actor, operation)
);

CREATE INDEX IF NOT EXISTS economy_intentions_actor_idx ON app.economy_intentions (actor, created_at DESC);

COMMENT ON TABLE app.economy_intentions IS 'Exactly-once registry of monetary intentions: one triple, one payload hash, one settled transfer with its persisted response';
COMMENT ON COLUMN app.economy_intentions.payload_hash IS 'SHA-256 hex of the canonical transfer payload: a different payload under the same triple is a conflict';
COMMENT ON COLUMN app.economy_intentions.transfer_id IS 'Transfer settled by this intention; unique so one transfer answers exactly one intention';

-- +goose StatementBegin
-- A recorded intention is the proof that an uncertain outcome was
-- decided: rewriting it would let one intention settle twice.
CREATE OR REPLACE FUNCTION app.economy_intentions_forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'recorded intentions are final'
        USING ERRCODE = '23514', CONSTRAINT = 'economy_intentions_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS economy_intentions_immutable ON app.economy_intentions;
CREATE TRIGGER economy_intentions_immutable
    BEFORE UPDATE OR DELETE ON app.economy_intentions
    FOR EACH ROW EXECUTE FUNCTION app.economy_intentions_forbid_mutation();

ALTER TABLE app.economy_intentions OWNER TO arena_owner;
ALTER FUNCTION app.economy_intentions_forbid_mutation() OWNER TO arena_owner;

-- Runtime surface. The application resolves retries and appends settled
-- intentions; history is never rewritten.
GRANT SELECT, INSERT ON app.economy_intentions TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS economy_intentions_immutable ON app.economy_intentions;
DROP FUNCTION IF EXISTS app.economy_intentions_forbid_mutation();
DROP TABLE IF EXISTS app.economy_intentions;
