-- +goose Up
-- 00050 records voluntary commerce transfers and sanction blocks
-- (P37-T02): one row per settled transfer with the idempotency key,
-- the immutable business kind, the payer, the payee, the exact
-- amount, the explicit consent reference and the journal transfer
-- moving it, plus one row per sanctioned account. The caller names
-- only key, kind, parties, amount and consent: eligibility, limits,
-- sanctions, balances and custodies resolve server-side in one
-- transaction, so a forged request can neither spend another
-- holder's funds nor bypass a block.
--
-- Invariants:
-- 1. Additive only: two new tables plus one trigger function. No
--    existing row is touched. The INK itself moves only in economy
--    legs the same transaction writes under the same transfer id.
-- 2. One key settles once per payer: (payer_id, intention_key) is
--    UNIQUE, so a retried transfer replays instead of paying twice.
--    Another payer reusing a key opens its own settlement.
-- 3. Parties are known accounts: payer and payee reference
--    app.accounts with ON DELETE RESTRICT, and only 'active'
--    accounts transact, enforced in the settling transaction.
-- 4. Sanctions block both directions: payer and payee are checked
--    against the sanction registry in the same transaction.
--    Sanction rows carry no trigger on purpose: lifting a block is
--    governance with its own future migration, never a silent edit
--    through this phase.
-- 5. History is retained and immutable: the trigger function refuses
--    UPDATE and DELETE on transfers for every role including the
--    owner.
-- 6. Least privilege: arena_owner owns the objects; arena_app may
--    read and append transfer rows and read sanctions, never rewrite
--    them.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.commerce_transfer_forbid() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the transfer registry is append-only'
        USING ERRCODE = '23514', CONSTRAINT = 'commerce_transfer_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS app.commerce_transfers (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    intention_key text NOT NULL,
    kind text NOT NULL,
    payer_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    payee_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    amount_milli bigint NOT NULL,
    consent_ref text NOT NULL,
    payload_hash text NOT NULL,
    transfer_id uuid NOT NULL,
    posted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT commerce_transfers_key_check CHECK (trim(intention_key) <> ''),
    CONSTRAINT commerce_transfers_kind_check CHECK (kind IN ('gift', 'trade', 'refund', 'treasury')),
    CONSTRAINT commerce_transfers_amount_check CHECK (amount_milli BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT commerce_transfers_consent_check CHECK (trim(consent_ref) <> ''),
    CONSTRAINT commerce_transfers_hash_check CHECK (trim(payload_hash) <> ''),
    CONSTRAINT commerce_transfers_payer_key_unique UNIQUE (payer_id, intention_key),
    CONSTRAINT commerce_transfers_transfer_unique UNIQUE (transfer_id)
);

COMMENT ON TABLE app.commerce_transfers IS 'Settled voluntary transfers: idempotency key, immutable kind, payer, payee, exact amount, consent reference and journal transfer, append-only';
COMMENT ON COLUMN app.commerce_transfers.intention_key IS 'Caller operation token scoped by payer: a retry replays instead of paying twice';
COMMENT ON COLUMN app.commerce_transfers.kind IS 'Business kind sealed at acceptance: gifts, formal trades, refunds and treasury movements never interchange';
COMMENT ON COLUMN app.commerce_transfers.transfer_id IS 'Journal transfer moving the exact amount: row and legs share one intention';

CREATE TABLE IF NOT EXISTS app.commerce_sanctions (
    account_id uuid PRIMARY KEY REFERENCES app.accounts(id) ON DELETE RESTRICT,
    reason text NOT NULL,
    decided_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT commerce_sanctions_reason_check CHECK (trim(reason) <> '')
);

COMMENT ON TABLE app.commerce_sanctions IS 'Sanctioned accounts blocked from paying and receiving: rows are managed by governance, read by settlement';
COMMENT ON COLUMN app.commerce_sanctions.account_id IS 'Blocked account: payer and payee checks run in the settling transaction';

CREATE INDEX IF NOT EXISTS commerce_transfers_payer_idx ON app.commerce_transfers (payer_id, posted_at DESC);

DROP TRIGGER IF EXISTS commerce_transfers_immutable ON app.commerce_transfers;
CREATE TRIGGER commerce_transfers_immutable
    BEFORE UPDATE OR DELETE ON app.commerce_transfers
    FOR EACH ROW EXECUTE FUNCTION app.commerce_transfer_forbid();

ALTER FUNCTION app.commerce_transfer_forbid() OWNER TO arena_owner;
ALTER TABLE app.commerce_transfers OWNER TO arena_owner;
ALTER TABLE app.commerce_sanctions OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.commerce_transfers TO arena_app;
GRANT SELECT ON app.commerce_sanctions TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS commerce_transfers_immutable ON app.commerce_transfers;
DROP TABLE IF EXISTS app.commerce_transfers;
DROP TABLE IF EXISTS app.commerce_sanctions;
DROP FUNCTION IF EXISTS app.commerce_transfer_forbid();
