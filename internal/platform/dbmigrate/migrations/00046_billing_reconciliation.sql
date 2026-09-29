-- +goose Up
-- 00046 reconciles INK purchases without silent fixes (P35-T07): the
-- gateway charge events beside the intents they settle for, and the
-- pending intent lifecycle with closed transitions. A job compares
-- gateway, fiat, intent and ledger: captured payments without
-- transfer and transfers without capture raise alerts, and holds
-- release only on a proven terminal failure. Nothing here rewrites
-- history: alerts never write, and every transition is reported.
--
-- Invariants:
-- 1. Additive plus one governed evolution: one new table, and the
--    intent status CHECK widens from pending-only to the closed
--    PENDING|SETTLED|FAILED|REVIEW vocabulary with a transition
--    trigger replacing the total freeze. No existing row is touched.
-- 2. Gateway facts are append-only: one row per provider event with
--    UNIQUE event identity, so redeliveries replay. Only gateway
--    verdicts (paid|failed) record: navigations never do.
-- 3. Closed transitions: pending settles, fails or awaits review;
--    review settles or fails; settled and failed never reopen. Every
--    other write dies at 23514, and DELETE stays refused.
-- 4. Least privilege: arena_owner owns the objects; arena_app may
--    read and append event rows and settle intents through the
--    transition trigger, never rewrite history.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.billing_ink_intent_transition() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'the purchase intent registry is append-only'
            USING ERRCODE = '23514', CONSTRAINT = 'billing_ink_intent_immutable';
    END IF;
    IF OLD.status = 'pending' AND NEW.status IN ('settled', 'failed', 'review') THEN
        RETURN NEW;
    END IF;
    IF OLD.status = 'review' AND NEW.status IN ('settled', 'failed') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'purchase intent leaves its state only along closed edges'
        USING ERRCODE = '23514', CONSTRAINT = 'billing_ink_intent_transition';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS app.billing_ink_charge_events (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    event_id text NOT NULL UNIQUE,
    intent_key text NOT NULL,
    account_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    amount_minor bigint NOT NULL,
    currency text NOT NULL,
    status text NOT NULL,
    received_at timestamptz NOT NULL,
    CONSTRAINT billing_ink_charge_events_event_check CHECK (trim(event_id) <> ''),
    CONSTRAINT billing_ink_charge_events_key_check CHECK (trim(intent_key) <> ''),
    CONSTRAINT billing_ink_charge_events_amount_check CHECK (amount_minor BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT billing_ink_charge_events_currency_check CHECK (currency = 'BRL'),
    CONSTRAINT billing_ink_charge_events_status_check CHECK (status IN ('paid', 'failed'))
);

COMMENT ON TABLE app.billing_ink_charge_events IS 'Gateway charge verdicts per purchase intent: paid or failed facts the reconciliation compares, append-only';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.billing_ink_event_forbid() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the charge event registry is append-only'
        USING ERRCODE = '23514', CONSTRAINT = 'billing_ink_event_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS billing_ink_charge_events_immutable ON app.billing_ink_charge_events;
CREATE TRIGGER billing_ink_charge_events_immutable
    BEFORE UPDATE OR DELETE ON app.billing_ink_charge_events
    FOR EACH ROW EXECUTE FUNCTION app.billing_ink_event_forbid();

ALTER TABLE app.billing_ink_intents DROP CONSTRAINT IF EXISTS billing_ink_intents_status_check;
ALTER TABLE app.billing_ink_intents ADD CONSTRAINT billing_ink_intents_status_check CHECK (status IN ('pending', 'settled', 'failed', 'review'));

DROP TRIGGER IF EXISTS billing_ink_intents_immutable ON app.billing_ink_intents;
CREATE TRIGGER billing_ink_intents_transition
    BEFORE UPDATE OR DELETE ON app.billing_ink_intents
    FOR EACH ROW EXECUTE FUNCTION app.billing_ink_intent_transition();

ALTER FUNCTION app.billing_ink_intent_transition() OWNER TO arena_owner;
ALTER FUNCTION app.billing_ink_event_forbid() OWNER TO arena_owner;
ALTER TABLE app.billing_ink_charge_events OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.billing_ink_charge_events TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS billing_ink_charge_events_immutable ON app.billing_ink_charge_events;
DROP TABLE IF EXISTS app.billing_ink_charge_events;
DROP FUNCTION IF EXISTS app.billing_ink_event_forbid();
DROP TRIGGER IF EXISTS billing_ink_intents_transition ON app.billing_ink_intents;
CREATE TRIGGER billing_ink_intents_immutable
    BEFORE UPDATE OR DELETE ON app.billing_ink_intents
    FOR EACH ROW EXECUTE FUNCTION app.billing_ink_intent_forbid();
ALTER TABLE app.billing_ink_intents DROP CONSTRAINT IF EXISTS billing_ink_intents_status_check;
ALTER TABLE app.billing_ink_intents ADD CONSTRAINT billing_ink_intents_status_check CHECK (status = 'pending');
DROP FUNCTION IF EXISTS app.billing_ink_intent_transition();
