-- +goose Up
-- 00037 records generic value holds (P32-T07): app.economy_holds locks
-- funds of one owner custody inside a dedicated hold custody until an
-- explicit release or capture moves them. No Treasury logic lives here.
--
-- Invariants (docs/reino/LEDGER_CONTRACT.md §4, THR-ECON-28):
-- 1. Reserved funds are not spendable: they leave the owner custody in
--    the same transaction that opens the hold, so journal balances
--    already exclude them.
-- 2. Closed transitions: active settles to released, captured or
--    expired; expired still settles to released or captured. Every other
--    rewrite dies in the trigger, for every role including the owner.
-- 3. Expiry moves nothing: marking a hold expired writes no leg. Funds
--    stay locked until an explicit release or capture.
-- 4. One hold owns its hold custody: the unique guard stops two holds
--    from sharing one locked purse.
-- 5. Least privilege: arena_app reads and appends holds and settles
--    them, never deletes them.

CREATE TABLE IF NOT EXISTS app.economy_holds (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    owner_custody_id uuid NOT NULL REFERENCES app.economy_custodies(id) ON DELETE RESTRICT,
    hold_custody_id uuid NOT NULL REFERENCES app.economy_custodies(id) ON DELETE RESTRICT,
    amount_milli bigint NOT NULL,
    purpose text NOT NULL,
    expires_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz,
    CONSTRAINT economy_holds_amount_range_check CHECK (amount_milli BETWEEN 1 AND 2100000000000),
    CONSTRAINT economy_holds_purpose_check CHECK (trim(purpose) <> ''),
    CONSTRAINT economy_holds_status_check CHECK (status IN ('active', 'released', 'captured', 'expired')),
    CONSTRAINT economy_holds_closed_check CHECK (
        (status = 'active' AND closed_at IS NULL) OR
        (status <> 'active' AND closed_at IS NOT NULL)
    ),
    CONSTRAINT economy_holds_hold_custody_unique UNIQUE (hold_custody_id)
);

CREATE INDEX IF NOT EXISTS economy_holds_owner_idx ON app.economy_holds (owner_custody_id, status);

COMMENT ON TABLE app.economy_holds IS 'Generic value holds: owner funds locked in a dedicated hold custody until an explicit release or capture; expiry alone moves nothing';
COMMENT ON COLUMN app.economy_holds.owner_custody_id IS 'Custody the funds were reserved from; its spendable balance excludes active and expired-unsettled holds';
COMMENT ON COLUMN app.economy_holds.hold_custody_id IS 'Dedicated escrow-kind custody holding the locked amount; one hold per custody';
COMMENT ON COLUMN app.economy_holds.status IS 'Closed machine: active settles to released, captured or expired; expired still settles to released or captured';

-- +goose StatementBegin
-- A hold settles only along its closed edges: active to released,
-- captured or expired, and expired to released or captured. Anything
-- else — a reopened settlement, a rewritten amount, a deleted hold —
-- is refused for every role including the owner.
CREATE OR REPLACE FUNCTION app.economy_holds_protect() RETURNS trigger AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
        OR NEW.owner_custody_id IS DISTINCT FROM OLD.owner_custody_id
        OR NEW.hold_custody_id IS DISTINCT FROM OLD.hold_custody_id
        OR NEW.amount_milli IS DISTINCT FROM OLD.amount_milli
        OR NEW.purpose IS DISTINCT FROM OLD.purpose
        OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
        OR NEW.created_at IS DISTINCT FROM OLD.created_at
    THEN
        RAISE EXCEPTION 'a hold record is preserved'
            USING ERRCODE = '23514', CONSTRAINT = 'economy_holds_immutable';
    END IF;

    IF NOT (
        (OLD.status = 'active' AND NEW.status IN ('released', 'captured', 'expired'))
        OR (OLD.status = 'expired' AND NEW.status IN ('released', 'captured'))
    ) THEN
        RAISE EXCEPTION 'hold transition is not an edge of the closed machine'
            USING ERRCODE = '23514', CONSTRAINT = 'economy_holds_transition';
    END IF;

    IF (NEW.status = 'active' AND NEW.closed_at IS NOT NULL)
        OR (NEW.status <> 'active' AND NEW.closed_at IS NULL)
    THEN
        RAISE EXCEPTION 'closed holds carry their settlement instant'
            USING ERRCODE = '23514', CONSTRAINT = 'economy_holds_closed_check';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS economy_holds_protect ON app.economy_holds;
CREATE TRIGGER economy_holds_protect
    BEFORE UPDATE ON app.economy_holds
    FOR EACH ROW EXECUTE FUNCTION app.economy_holds_protect();

DROP TRIGGER IF EXISTS economy_holds_retained ON app.economy_holds;
CREATE TRIGGER economy_holds_retained
    BEFORE DELETE ON app.economy_holds
    FOR EACH ROW EXECUTE FUNCTION app.economy_forbid_mutation();

ALTER TABLE app.economy_holds OWNER TO arena_owner;
ALTER FUNCTION app.economy_holds_protect() OWNER TO arena_owner;

-- Runtime surface. Holds are opened and settled by the application;
-- history is never deleted (THR-ECON-28).
GRANT SELECT, INSERT, UPDATE ON app.economy_holds TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS economy_holds_retained ON app.economy_holds;
DROP TRIGGER IF EXISTS economy_holds_protect ON app.economy_holds;
DROP FUNCTION IF EXISTS app.economy_holds_protect();
DROP TABLE IF EXISTS app.economy_holds;
