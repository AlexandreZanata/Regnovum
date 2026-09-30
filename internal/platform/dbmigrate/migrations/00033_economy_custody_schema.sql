-- +goose Up
-- 00033 establishes the economy custody schema (P32-T02):
-- app.economy_custodies, app.economy_partitions and app.economy_entries.
--
-- Invariants (docs/reino/LEDGER_CONTRACT.md §1–§2, docs/THREAT_MODEL.md §5.8):
-- 1. Every custody holds Genesis INK in exactly one place: custodies are
--    (kind, label) unique and partitions are (custody, name) unique, so the
--    same unit can never be counted twice.
-- 2. Every amount is milliINK bigint: legs carry 1..S subunits, never zero,
--    negative, float, numeric or money. A single leg can never exceed the
--    fixed supply S = 2100000000000 milliINK.
-- 3. Double entry travels as legs sharing one transfer_id: each leg names
--    its custody and direction, and (transfer_id, custody_id, direction) is
--    unique, so a transfer cannot debit the same custody twice.
-- 4. The registry and the journal are strictly append-only: triggers refuse
--    UPDATE and DELETE for every role including the owner (THR-ECON-20/23),
--    and arena_app is never granted them either.
-- 5. Financial history is retained: entries reference custodies with
--    ON DELETE RESTRICT, and custodial rows are never removed.
-- 6. Least privilege: arena_owner owns the objects; arena_app gets the
--    minimum DML (SELECT and INSERT only, no UPDATE or DELETE anywhere).

CREATE TABLE IF NOT EXISTS app.economy_custodies (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    kind text NOT NULL,
    label text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT economy_custodies_kind_check CHECK (kind IN (
        'treasury',
        'user',
        'escrow',
        'contract',
        'title'
    )),
    CONSTRAINT economy_custodies_label_check CHECK (trim(label) <> ''),
    CONSTRAINT economy_custodies_kind_label_unique UNIQUE (kind, label)
);

COMMENT ON TABLE app.economy_custodies IS 'Genesis custody accounts: exactly one home per unit of INK, never renamed or removed';
COMMENT ON COLUMN app.economy_custodies.kind IS 'Custody class: treasury, user, escrow, contract or title; closed vocabulary';
COMMENT ON COLUMN app.economy_custodies.label IS 'Stable custody name within its kind; unique per kind';

CREATE TABLE IF NOT EXISTS app.economy_partitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    custody_id uuid NOT NULL REFERENCES app.economy_custodies(id) ON DELETE RESTRICT,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT economy_partitions_name_check CHECK (name IN (
        'sovereign_reserve',
        'commercial_stock',
        'operating_cash',
        'obligations',
        'encumbrances',
        'available'
    )),
    CONSTRAINT economy_partitions_custody_name_unique UNIQUE (custody_id, name)
);

COMMENT ON TABLE app.economy_partitions IS 'Exclusive sub-partitions of one custody: reserve, stock, cash, obligations and encumbrances never double-count';
COMMENT ON COLUMN app.economy_partitions.custody_id IS 'Owning custody; restricted from deletion while partitions exist';

CREATE TABLE IF NOT EXISTS app.economy_entries (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    transfer_id uuid NOT NULL,
    custody_id uuid NOT NULL REFERENCES app.economy_custodies(id) ON DELETE RESTRICT,
    direction text NOT NULL,
    amount_milli bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT economy_entries_direction_check CHECK (direction IN ('debit', 'credit')),
    CONSTRAINT economy_entries_amount_range_check CHECK (amount_milli BETWEEN 1 AND 2100000000000),
    CONSTRAINT economy_entries_transfer_leg_unique UNIQUE (transfer_id, custody_id, direction)
);

CREATE INDEX IF NOT EXISTS economy_entries_transfer_idx ON app.economy_entries (transfer_id);
CREATE INDEX IF NOT EXISTS economy_entries_custody_idx ON app.economy_entries (custody_id, created_at DESC);

COMMENT ON TABLE app.economy_entries IS 'Double-entry legs of Genesis transfers: debit/credit pairs sharing one transfer_id, append-only';
COMMENT ON COLUMN app.economy_entries.transfer_id IS 'Business intention carried by every leg of the transfer; replayed intentions reuse it instead of duplicating legs';
COMMENT ON COLUMN app.economy_entries.amount_milli IS 'Leg amount in milliINK subunits: 1..S, never zero, negative or beyond supply';

-- +goose StatementBegin
-- Custodies, partitions and entries are facts of money at rest and in
-- motion: nothing about them is ever edited or erased. The trigger fires
-- for every role including the owner; the 23514 code surfaces the refusal
-- as a constraint violation, like the billing refund guard.
CREATE OR REPLACE FUNCTION app.economy_forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the economy ledger is append-only'
        USING ERRCODE = '23514', CONSTRAINT = 'economy_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS economy_custodies_immutable ON app.economy_custodies;
CREATE TRIGGER economy_custodies_immutable
    BEFORE UPDATE OR DELETE ON app.economy_custodies
    FOR EACH ROW EXECUTE FUNCTION app.economy_forbid_mutation();

DROP TRIGGER IF EXISTS economy_partitions_immutable ON app.economy_partitions;
CREATE TRIGGER economy_partitions_immutable
    BEFORE UPDATE OR DELETE ON app.economy_partitions
    FOR EACH ROW EXECUTE FUNCTION app.economy_forbid_mutation();

DROP TRIGGER IF EXISTS economy_entries_immutable ON app.economy_entries;
CREATE TRIGGER economy_entries_immutable
    BEFORE UPDATE OR DELETE ON app.economy_entries
    FOR EACH ROW EXECUTE FUNCTION app.economy_forbid_mutation();

ALTER TABLE app.economy_custodies OWNER TO arena_owner;
ALTER TABLE app.economy_partitions OWNER TO arena_owner;
ALTER TABLE app.economy_entries OWNER TO arena_owner;
ALTER FUNCTION app.economy_forbid_mutation() OWNER TO arena_owner;

-- Runtime surface. Custodies, partitions and entries are created and read
-- by the application; history is never rewritten, so arena_app holds no
-- UPDATE or DELETE anywhere (THR-ECON-11).
GRANT SELECT, INSERT ON app.economy_custodies TO arena_app;
GRANT SELECT, INSERT ON app.economy_partitions TO arena_app;
GRANT SELECT, INSERT ON app.economy_entries TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS economy_entries_immutable ON app.economy_entries;
DROP TRIGGER IF EXISTS economy_partitions_immutable ON app.economy_partitions;
DROP TRIGGER IF EXISTS economy_custodies_immutable ON app.economy_custodies;
DROP FUNCTION IF EXISTS app.economy_forbid_mutation();
DROP TABLE IF EXISTS app.economy_entries;
DROP TABLE IF EXISTS app.economy_partitions;
DROP TABLE IF EXISTS app.economy_custodies;
