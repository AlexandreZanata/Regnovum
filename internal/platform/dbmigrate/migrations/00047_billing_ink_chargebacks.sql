-- +goose Up
-- 00047 records INK purchase chargebacks (P35-T08): one row per
-- reversed liquidation with the disputing provider event. A
-- chargeback revokes the buyer INK up to their balance and covers
-- the remainder from commercial stock in the same transaction: the
-- defrauding holder answers for the legitimate debt, the operator
-- covers the immediate difference, and good-faith third parties are
-- never debited. Short treasuries refuse instead of minting, and no
-- balance ever implies a negative.
--
-- Invariants:
-- 1. Additive only: one new table plus one trigger function. No
--    existing row is touched. Settlement and intent rows never change
--    here: the chargeback is a new compensating fact beside them.
-- 2. One chargeback per liquidation and per provider event: both
--    UNIQUEs make redeliveries and second disputes replay instead of
--    revoking twice.
-- 3. History is retained and immutable: the trigger function refuses
--    UPDATE and DELETE for every role including the owner.
-- 4. Least privilege: arena_owner owns the objects; arena_app may
--    read and append chargeback rows, never rewrite them.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.billing_ink_chargeback_forbid() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the purchase chargeback registry is append-only'
        USING ERRCODE = '23514', CONSTRAINT = 'billing_ink_chargeback_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS app.billing_ink_chargebacks (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    settlement_id uuid NOT NULL UNIQUE REFERENCES app.billing_ink_settlements(id) ON DELETE RESTRICT,
    event_id text NOT NULL UNIQUE,
    disputed_minor bigint NOT NULL,
    ink_revoked bigint NOT NULL,
    treasury_covered bigint NOT NULL,
    received_at timestamptz NOT NULL,
    CONSTRAINT billing_ink_chargebacks_event_check CHECK (trim(event_id) <> ''),
    CONSTRAINT billing_ink_chargebacks_disputed_check CHECK (disputed_minor BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT billing_ink_chargebacks_revoked_check CHECK (ink_revoked BETWEEN 0 AND 9223372036854775807),
    CONSTRAINT billing_ink_chargebacks_covered_check CHECK (treasury_covered BETWEEN 0 AND 9223372036854775807)
);

COMMENT ON TABLE app.billing_ink_chargebacks IS 'Reversed INK liquidations: holder revocation plus treasury cover per provider dispute, append-only';
COMMENT ON COLUMN app.billing_ink_chargebacks.ink_revoked IS 'MilliINK taken back from the defrauding holder balance, never below zero';
COMMENT ON COLUMN app.billing_ink_chargebacks.treasury_covered IS 'MilliINK the operator covered from commercial stock when the holder balance fell short';

DROP TRIGGER IF EXISTS billing_ink_chargebacks_immutable ON app.billing_ink_chargebacks;
CREATE TRIGGER billing_ink_chargebacks_immutable
    BEFORE UPDATE OR DELETE ON app.billing_ink_chargebacks
    FOR EACH ROW EXECUTE FUNCTION app.billing_ink_chargeback_forbid();

ALTER FUNCTION app.billing_ink_chargeback_forbid() OWNER TO arena_owner;
ALTER TABLE app.billing_ink_chargebacks OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.billing_ink_chargebacks TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS billing_ink_chargebacks_immutable ON app.billing_ink_chargebacks;
DROP TABLE IF EXISTS app.billing_ink_chargebacks;
DROP FUNCTION IF EXISTS app.billing_ink_chargeback_forbid();
