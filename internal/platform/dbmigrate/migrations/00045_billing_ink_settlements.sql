-- +goose Up
-- 00045 records INK purchase settlements (P35-T06): one row per
-- liquidated intent with the settling provider event. The webhook
-- carries intent key, account, amount, currency and status under HMAC:
-- only a paid event matching the sealed intent moves the committed
-- INK to the buyer, exactly once. Statuses settle in later tasks with
-- their own migration; this registry only records paid liquidations.
--
-- Invariants:
-- 1. Additive only: one new table plus one trigger function. No
--    existing row is touched. The intent row itself never changes
--    here: settlement is a new fact beside it.
-- 2. One liquidation per intent: intent_id is UNIQUE, so a second
--    event for a settled intent resolves without effect instead of
--    paying twice, whatever its order.
-- 3. One record per provider event: event_id is UNIQUE, so a
--    redelivered webhook replays instead of settling again.
-- 4. History is retained and immutable: the trigger function refuses
--    UPDATE and DELETE for every role including the owner.
-- 5. Least privilege: arena_owner owns the objects; arena_app may
--    read and append settlement rows, never rewrite them.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.billing_ink_settlement_forbid() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the purchase settlement registry is append-only'
        USING ERRCODE = '23514', CONSTRAINT = 'billing_ink_settlement_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS app.billing_ink_settlements (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    intent_id uuid NOT NULL REFERENCES app.billing_ink_intents(id) ON DELETE RESTRICT,
    event_id text NOT NULL,
    amount_minor bigint NOT NULL,
    currency text NOT NULL,
    settled_at timestamptz NOT NULL,
    CONSTRAINT billing_ink_settlements_intent_unique UNIQUE (intent_id),
    CONSTRAINT billing_ink_settlements_event_unique UNIQUE (event_id),
    CONSTRAINT billing_ink_settlements_event_check CHECK (trim(event_id) <> ''),
    CONSTRAINT billing_ink_settlements_amount_check CHECK (amount_minor BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT billing_ink_settlements_currency_check CHECK (currency = 'BRL')
);

COMMENT ON TABLE app.billing_ink_settlements IS 'Paid INK purchase liquidations: one row per settled intent with its provider event, append-only';
COMMENT ON COLUMN app.billing_ink_settlements.event_id IS 'Provider event identifier: redeliveries replay instead of settling again';

CREATE INDEX IF NOT EXISTS billing_ink_settlements_intent_idx ON app.billing_ink_settlements (intent_id, settled_at DESC);

DROP TRIGGER IF EXISTS billing_ink_settlements_immutable ON app.billing_ink_settlements;
CREATE TRIGGER billing_ink_settlements_immutable
    BEFORE UPDATE OR DELETE ON app.billing_ink_settlements
    FOR EACH ROW EXECUTE FUNCTION app.billing_ink_settlement_forbid();

ALTER FUNCTION app.billing_ink_settlement_forbid() OWNER TO arena_owner;
ALTER TABLE app.billing_ink_settlements OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.billing_ink_settlements TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS billing_ink_settlements_immutable ON app.billing_ink_settlements;
DROP TABLE IF EXISTS app.billing_ink_settlements;
DROP FUNCTION IF EXISTS app.billing_ink_settlement_forbid();
