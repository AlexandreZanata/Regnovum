-- +goose Up
-- 00044 records INK purchase intents (P35-T05): one row per accepted
-- purchase with the buyer, the sealed quotation, the server-derived
-- fiat and INK amounts, the terms hash and the commercial-stock hold
-- that backs it. The buyer names only key, account, quotation and
-- fiat ticket: price, quantity and stock resolve on the server, so a
-- forged request can never mint INK or move another holder's funds.
--
-- Invariants:
-- 1. Additive only: one new table plus one trigger function. No
--    existing row is touched. Fiat and INK books stay separate: this
--    row names amounts, the INK itself moves only in economy legs.
-- 2. One key settles once per account: (account_id, intent_key) is
--    UNIQUE, so a retried acceptance replays instead of provisioning
--    twice. Another account reusing a key opens its own intent.
-- 3. Server-side terms only: quote_id references the sealed snapshot
--    with ON DELETE RESTRICT, and hold_id references the backing hold
--    the same transaction wrote. No client field chooses price,
--    quantity or stock.
-- 4. Pending only: status admits exactly 'pending' in this phase. The
--    settlement transitions arrive in later tasks with their own
--    migration; nothing here pre-opens them.
-- 5. History is retained and immutable: the trigger function refuses
--    UPDATE and DELETE for every role including the owner.
-- 6. Least privilege: arena_owner owns the objects; arena_app may
--    read and append intent rows, never rewrite them.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.billing_ink_intent_forbid() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the purchase intent registry is append-only'
        USING ERRCODE = '23514', CONSTRAINT = 'billing_ink_intent_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS app.billing_ink_intents (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    intent_key text NOT NULL,
    account_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    quote_id uuid NOT NULL REFERENCES app.pricing_quotes(id) ON DELETE RESTRICT,
    fiat_minor bigint NOT NULL,
    ink_milli bigint NOT NULL,
    terms_hash text NOT NULL,
    status text NOT NULL,
    hold_id uuid NULL REFERENCES app.economy_holds(id) ON DELETE RESTRICT,
    decided_at timestamptz NOT NULL,
    CONSTRAINT billing_ink_intents_key_check CHECK (trim(intent_key) <> ''),
    CONSTRAINT billing_ink_intents_account_key_unique UNIQUE (account_id, intent_key),
    CONSTRAINT billing_ink_intents_fiat_check CHECK (fiat_minor BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT billing_ink_intents_ink_check CHECK (ink_milli BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT billing_ink_intents_hash_check CHECK (trim(terms_hash) <> ''),
    CONSTRAINT billing_ink_intents_status_check CHECK (status = 'pending')
);

COMMENT ON TABLE app.billing_ink_intents IS 'Accepted INK purchase intents: buyer, sealed quotation, server-derived amounts, terms hash and backing hold, append-only';
COMMENT ON COLUMN app.billing_ink_intents.intent_key IS 'Caller operation token scoped by account: a retry replays instead of provisioning twice';
COMMENT ON COLUMN app.billing_ink_intents.hold_id IS 'Commercial-stock hold backing the intent, written by the same transaction';

CREATE INDEX IF NOT EXISTS billing_ink_intents_account_idx ON app.billing_ink_intents (account_id, decided_at DESC);

DROP TRIGGER IF EXISTS billing_ink_intents_immutable ON app.billing_ink_intents;
CREATE TRIGGER billing_ink_intents_immutable
    BEFORE UPDATE OR DELETE ON app.billing_ink_intents
    FOR EACH ROW EXECUTE FUNCTION app.billing_ink_intent_forbid();

ALTER FUNCTION app.billing_ink_intent_forbid() OWNER TO arena_owner;
ALTER TABLE app.billing_ink_intents OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.billing_ink_intents TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS billing_ink_intents_immutable ON app.billing_ink_intents;
DROP TABLE IF EXISTS app.billing_ink_intents;
DROP FUNCTION IF EXISTS app.billing_ink_intent_forbid();
