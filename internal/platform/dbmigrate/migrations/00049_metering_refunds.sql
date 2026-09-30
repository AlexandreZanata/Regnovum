-- +goose Up
-- 00049 records INK publication compensations (P36-T07): one row per
-- refunded publication with the refund key, the owning account, the
-- compensated publication, the reversed amount, the compensating
-- journal transfer and the database posted instant. A correction is a
-- new current entry linked to its cause, never a rewrite and never a
-- backdate: the original publication row stays immutable with its own
-- posted instant, so closed periods are never reopened.
--
-- Invariants:
-- 1. Additive only: one new table plus one trigger function. No
--    existing row is touched. The INK itself moves back in economy
--    legs the same transaction writes under a new transfer id, so the
--    compensation links to the original without editing it.
-- 2. One refund key settles once per account: (account_label,
--    refund_key) is UNIQUE, so a retried refund replays instead of
--    paying twice.
-- 3. One error settles one compensation: original_id is UNIQUE, so a
--    second refund of the same publication refuses instead of paying
--    twice. Partial compensation is out of scope in this phase: the
--    amount always equals the original charge.
-- 4. Causal link: original_id references the settled publication
--    with ON DELETE RESTRICT; history can never lose its cause.
-- 5. Current clock: posted_at defaults to now() inside the writing
--    transaction. No caller field chooses the posting time, so a
--    correction always lands in the current period.
-- 6. History is retained and immutable: the trigger function refuses
--    UPDATE and DELETE for every role including the owner.
-- 7. Least privilege: arena_owner owns the objects; arena_app may
--    read and append refund rows, never rewrite them.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.metering_refund_forbid() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the refund registry is append-only'
        USING ERRCODE = '23514', CONSTRAINT = 'metering_refund_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS app.metering_refunds (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    refund_key text NOT NULL,
    account_label text NOT NULL,
    original_id uuid NOT NULL REFERENCES app.metering_publications(id) ON DELETE RESTRICT,
    amount_milli bigint NOT NULL,
    transfer_id uuid NOT NULL,
    reason text NOT NULL,
    posted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT metering_refunds_key_check CHECK (trim(refund_key) <> ''),
    CONSTRAINT metering_refunds_account_check CHECK (trim(account_label) <> ''),
    CONSTRAINT metering_refunds_amount_check CHECK (amount_milli BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT metering_refunds_reason_check CHECK (trim(reason) <> ''),
    CONSTRAINT metering_refunds_account_key_unique UNIQUE (account_label, refund_key),
    CONSTRAINT metering_refunds_original_unique UNIQUE (original_id),
    CONSTRAINT metering_refunds_transfer_unique UNIQUE (transfer_id)
);

COMMENT ON TABLE app.metering_refunds IS 'Publication compensations: refund key, owner, compensated publication, reversed amount, compensating transfer and database posted instant, append-only';
COMMENT ON COLUMN app.metering_refunds.original_id IS 'Compensated publication: the cause the correction links to without editing';
COMMENT ON COLUMN app.metering_refunds.posted_at IS 'Database clock at write, always current: corrections never backdate into closed periods';

CREATE INDEX IF NOT EXISTS metering_refunds_account_idx ON app.metering_refunds (account_label, posted_at DESC);

DROP TRIGGER IF EXISTS metering_refunds_immutable ON app.metering_refunds;
CREATE TRIGGER metering_refunds_immutable
    BEFORE UPDATE OR DELETE ON app.metering_refunds
    FOR EACH ROW EXECUTE FUNCTION app.metering_refund_forbid();

ALTER FUNCTION app.metering_refund_forbid() OWNER TO arena_owner;
ALTER TABLE app.metering_refunds OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.metering_refunds TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS metering_refunds_immutable ON app.metering_refunds;
DROP TABLE IF EXISTS app.metering_refunds;
DROP FUNCTION IF EXISTS app.metering_refund_forbid();
