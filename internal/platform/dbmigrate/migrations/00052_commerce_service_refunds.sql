-- +goose Up
-- 00052 records proportional service refunds linked to their
-- liquidated payment (P37-T05): one row per partial refund with the
-- idempotency key, the total returned to the buyer, the floor(10%)
-- tithe reversal, the provider share and the explicit shortfall
-- obligation, plus at most one obligation row naming the debtor,
-- the creditor and the amount the provider could not cover. A
-- refund never reopens its cause: the released contract stays
-- terminal and every compensation links to the untouched original
-- in the same transaction as the legs it moves, or nothing is
-- stored at all.
--
-- Invariants:
-- 1. Additive only: two new tables plus two triggers. No existing
--    row is touched. Refunded value returns under a new transfer
--    that names the refund; the release transfer stays intact.
-- 2. One key refunds once per contract: (contract_id, refund_key)
--    is UNIQUE, so a retried refund replays instead of compensating
--    twice. Divergent terms under one key conflict.
-- 3. Proportional split: tithe_reversal = amount / 10 (integer
--    floor, the same function as the settlement tithe) and
--    provider_share = amount - tithe_reversal, so the two outputs
--    always sum to the refunded value and S never moves.
-- 4. No hidden negative balance: obligation_milli stays within
--    [0, provider_share] and names the shortfall the provider
--    could not cover; the provider custody is debited only up to
--    its balance, never below zero. At most one obligation names
--    each refund.
-- 5. History is retained and immutable as rows: the contract
--    registry trigger function refuses UPDATE and DELETE for every
--    role including the owner. No refund row is ever rewritten.
-- 6. Least privilege: arena_owner owns the objects; arena_app may
--    read and append refund and obligation rows, never rewrite
--    them.

CREATE TABLE IF NOT EXISTS app.commerce_service_refunds (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    contract_id uuid NOT NULL REFERENCES app.commerce_contracts(id) ON DELETE RESTRICT,
    refund_key text NOT NULL,
    amount_milli bigint NOT NULL,
    tithe_reversal_milli bigint NOT NULL,
    provider_share_milli bigint NOT NULL,
    obligation_milli bigint NOT NULL DEFAULT 0,
    transfer_id uuid NULL,
    posted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT commerce_service_refunds_key_check CHECK (trim(refund_key) <> ''),
    CONSTRAINT commerce_service_refunds_amount_check CHECK (amount_milli BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT commerce_service_refunds_tithe_check CHECK (tithe_reversal_milli = amount_milli / 10),
    CONSTRAINT commerce_service_refunds_provider_check CHECK (provider_share_milli = amount_milli - tithe_reversal_milli),
    CONSTRAINT commerce_service_refunds_obligation_check CHECK (obligation_milli BETWEEN 0 AND provider_share_milli),
    CONSTRAINT commerce_service_refunds_contract_key_unique UNIQUE (contract_id, refund_key),
    CONSTRAINT commerce_service_refunds_transfer_unique UNIQUE (transfer_id)
);

COMMENT ON TABLE app.commerce_service_refunds IS 'Proportional service refunds linked to their liquidated payment: idempotency key, total returned, tithe reversal, provider share, explicit shortfall and moving transfer, append-only';
COMMENT ON COLUMN app.commerce_service_refunds.refund_key IS 'Caller operation token scoped by contract: a retry replays instead of compensating twice';
COMMENT ON COLUMN app.commerce_service_refunds.transfer_id IS 'Journal transfer moving provider share and tithe reversal to the buyer; NULL only when dust plus a broke provider moves nothing and the full share becomes obligation';
COMMENT ON COLUMN app.commerce_service_refunds.obligation_milli IS 'Shortfall the provider could not cover: explicit debt to the buyer, never a hidden negative balance';

CREATE TABLE IF NOT EXISTS app.commerce_refund_obligations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    refund_id uuid NOT NULL REFERENCES app.commerce_service_refunds(id) ON DELETE RESTRICT,
    debtor_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    creditor_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    amount_milli bigint NOT NULL,
    posted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT commerce_refund_obligations_amount_check CHECK (amount_milli BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT commerce_refund_obligations_parties_check CHECK (debtor_id <> creditor_id),
    CONSTRAINT commerce_refund_obligations_refund_unique UNIQUE (refund_id)
);

COMMENT ON TABLE app.commerce_refund_obligations IS 'Explicit shortfall debts: at most one per service refund, naming the provider debtor, the buyer creditor and the uncovered share, append-only';

CREATE INDEX IF NOT EXISTS commerce_service_refunds_contract_idx ON app.commerce_service_refunds (contract_id, posted_at);
CREATE INDEX IF NOT EXISTS commerce_refund_obligations_debtor_idx ON app.commerce_refund_obligations (debtor_id, posted_at);

DROP TRIGGER IF EXISTS commerce_service_refunds_immutable ON app.commerce_service_refunds;
CREATE TRIGGER commerce_service_refunds_immutable
    BEFORE UPDATE OR DELETE ON app.commerce_service_refunds
    FOR EACH ROW EXECUTE FUNCTION app.commerce_contract_forbid();

DROP TRIGGER IF EXISTS commerce_refund_obligations_immutable ON app.commerce_refund_obligations;
CREATE TRIGGER commerce_refund_obligations_immutable
    BEFORE UPDATE OR DELETE ON app.commerce_refund_obligations
    FOR EACH ROW EXECUTE FUNCTION app.commerce_contract_forbid();

ALTER TABLE app.commerce_service_refunds OWNER TO arena_owner;
ALTER TABLE app.commerce_refund_obligations OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.commerce_service_refunds TO arena_app;
GRANT SELECT, INSERT ON app.commerce_refund_obligations TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS commerce_refund_obligations_immutable ON app.commerce_refund_obligations;
DROP TRIGGER IF EXISTS commerce_service_refunds_immutable ON app.commerce_service_refunds;
DROP TABLE IF EXISTS app.commerce_refund_obligations;
DROP TABLE IF EXISTS app.commerce_service_refunds;
