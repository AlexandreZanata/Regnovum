-- +goose Up
-- 00051 records formal trade contracts in exclusive escrow
-- (P37-T03): one row per funded contract with the idempotency key,
-- the service object, both parties, the exact amount, the expiry,
-- the sealed terms hash and the escrow lifecycle status, plus the
-- journal transfer that locked the buyer amount. Delivery
-- acceptance, provider payment, buyer cancellation, lapse marking
-- and competent resolution advance the status without ever
-- rewriting history: every mutation is a new settlement row or a
-- guarded status step in one transaction.
--
-- Invariants:
-- 1. Additive only: one new table plus one trigger function. No
--    existing row is touched. Locked funds live in exclusive
--    per-contract escrow custodies; releases and refunds move them
--    under new transfers that name the contract.
-- 2. One key funds once per buyer: (buyer_id, contract_key) is
--    UNIQUE, so a retried funding replays instead of locking twice.
--    Another buyer reusing a key opens its own contract.
-- 3. Trade only: kind admits exactly 'trade'. Gifts never lock,
--    refunds flow through linked entries and treasury movements are
--    not payments.
-- 4. Closed machine: status admits exactly the six lifecycle
--    states; terminal contracts never reopen, and expiry marks
--    without moving any leg.
-- 5. History is retained and immutable as rows: the trigger
--    function refuses UPDATE and DELETE for every role including
--    the owner. Status advances through new settlement rows that
--    reference their contract, never by rewriting it.
-- 6. Least privilege: arena_owner owns the objects; arena_app may
--    read and append contract and settlement rows, never rewrite
--    them.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.commerce_contract_forbid() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the contract registry is append-only'
        USING ERRCODE = '23514', CONSTRAINT = 'commerce_contract_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS app.commerce_contracts (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    contract_key text NOT NULL,
    kind text NOT NULL,
    buyer_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    provider_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    object text NOT NULL,
    amount_milli bigint NOT NULL,
    terms_hash text NOT NULL,
    escrow_transfer_id uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    posted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT commerce_contracts_key_check CHECK (trim(contract_key) <> ''),
    CONSTRAINT commerce_contracts_kind_check CHECK (kind = 'trade'),
    CONSTRAINT commerce_contracts_object_check CHECK (trim(object) <> ''),
    CONSTRAINT commerce_contracts_amount_check CHECK (amount_milli BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT commerce_contracts_hash_check CHECK (trim(terms_hash) <> ''),
    CONSTRAINT commerce_contracts_buyer_key_unique UNIQUE (buyer_id, contract_key),
    CONSTRAINT commerce_contracts_escrow_transfer_unique UNIQUE (escrow_transfer_id)
);

COMMENT ON TABLE app.commerce_contracts IS 'Funded formal trade contracts: idempotency key, service object, parties, exact amount, sealed terms and locking transfer, append-only';
COMMENT ON COLUMN app.commerce_contracts.contract_key IS 'Caller operation token scoped by buyer: a retry replays instead of locking twice';
COMMENT ON COLUMN app.commerce_contracts.escrow_transfer_id IS 'Journal transfer that locked the buyer amount in exclusive escrow';

CREATE TABLE IF NOT EXISTS app.commerce_settlements (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    contract_id uuid NOT NULL REFERENCES app.commerce_contracts(id) ON DELETE RESTRICT,
    action text NOT NULL,
    transfer_id uuid NULL,
    decided_by text NULL,
    posted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT commerce_settlements_action_check CHECK (action IN ('accept', 'release', 'refund', 'expire', 'resolve-release', 'resolve-refund')),
    CONSTRAINT commerce_settlements_transfer_unique UNIQUE (transfer_id)
);

COMMENT ON TABLE app.commerce_settlements IS 'Escrow lifecycle steps: acceptance, provider payment, buyer refund, lapse marking and competent resolutions, append-only';
COMMENT ON COLUMN app.commerce_settlements.action IS 'Lifecycle step: accept and expire move no legs; release, refund and resolutions move under new transfers';

CREATE INDEX IF NOT EXISTS commerce_contracts_buyer_idx ON app.commerce_contracts (buyer_id, posted_at DESC);
CREATE INDEX IF NOT EXISTS commerce_settlements_contract_idx ON app.commerce_settlements (contract_id, posted_at);

-- One contract settles value exactly once: terminal money-moving
-- steps are unique per contract, so two conflicting release orders
-- never pay twice even under concurrency. Acceptance and lapse
-- marking stay repeatable reads: replays resolve without writing.
CREATE UNIQUE INDEX IF NOT EXISTS commerce_settlements_terminal_unique
    ON app.commerce_settlements (contract_id)
    WHERE action IN ('release', 'refund', 'resolve-release', 'resolve-refund');

DROP TRIGGER IF EXISTS commerce_contracts_immutable ON app.commerce_contracts;
CREATE TRIGGER commerce_contracts_immutable
    BEFORE UPDATE OR DELETE ON app.commerce_contracts
    FOR EACH ROW EXECUTE FUNCTION app.commerce_contract_forbid();

DROP TRIGGER IF EXISTS commerce_settlements_immutable ON app.commerce_settlements;
CREATE TRIGGER commerce_settlements_immutable
    BEFORE UPDATE OR DELETE ON app.commerce_settlements
    FOR EACH ROW EXECUTE FUNCTION app.commerce_contract_forbid();

ALTER FUNCTION app.commerce_contract_forbid() OWNER TO arena_owner;
ALTER TABLE app.commerce_contracts OWNER TO arena_owner;
ALTER TABLE app.commerce_settlements OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.commerce_contracts TO arena_app;
GRANT SELECT, INSERT ON app.commerce_settlements TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS commerce_settlements_immutable ON app.commerce_settlements;
DROP TRIGGER IF EXISTS commerce_contracts_immutable ON app.commerce_contracts;
DROP TABLE IF EXISTS app.commerce_settlements;
DROP TABLE IF EXISTS app.commerce_contracts;
DROP FUNCTION IF EXISTS app.commerce_contract_forbid();
