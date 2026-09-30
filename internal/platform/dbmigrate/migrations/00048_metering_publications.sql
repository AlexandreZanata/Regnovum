-- +goose Up
-- 00048 records INK publication charges (P36-T04): one row per settled
-- publication with the idempotency key, the publishing account, the
-- sealed price terms, the canonical content and quote hashes, the
-- journal transfer moving the exact quoted cost and the database
-- posted instant. The caller names only key, account, content, price
-- and quote: cost, version and hashes resolve from the sealed quote,
-- so a forged request can never invent a price or move another
-- holder's funds.
--
-- Invariants:
-- 1. Additive only: one new table plus one trigger function. No
--    existing row is touched. The INK itself moves only in economy
--    legs the same transaction writes under the same transfer id, so
--    statement and content always point at one intention.
-- 2. One key settles once per account: (account_label, intention_key)
--    is UNIQUE, so a retried publication replays instead of charging
--    twice. Another account reusing a key opens its own settlement.
-- 3. Server-side terms only: units, amount, versions and hashes are
--    CHECKed positive/non-empty, and the transfer id is UNIQUE, so a
--    retried leg pair can never fan out.
-- 4. Database clock: posted_at defaults to now() inside the writing
--    transaction and is only visible after commit; no request, browser
--    or acceptance instant is ever stored as the posting time.
-- 5. History is retained and immutable: the trigger function refuses
--    UPDATE and DELETE for every role including the owner.
-- 6. Least privilege: arena_owner owns the objects; arena_app may
--    read and append publication rows, never rewrite them.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.metering_publication_forbid() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the publication registry is append-only'
        USING ERRCODE = '23514', CONSTRAINT = 'metering_publication_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS app.metering_publications (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    intention_key text NOT NULL,
    account_label text NOT NULL,
    service text NOT NULL,
    price_version int NOT NULL,
    units int NOT NULL,
    amount_milli bigint NOT NULL,
    content_hash text NOT NULL,
    quote_hash text NOT NULL,
    payload_hash text NOT NULL,
    transfer_id uuid NOT NULL,
    posted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT metering_publications_key_check CHECK (trim(intention_key) <> ''),
    CONSTRAINT metering_publications_account_check CHECK (trim(account_label) <> ''),
    CONSTRAINT metering_publications_service_check CHECK (trim(service) <> ''),
    CONSTRAINT metering_publications_version_check CHECK (price_version > 0),
    CONSTRAINT metering_publications_units_check CHECK (units > 0),
    CONSTRAINT metering_publications_amount_check CHECK (amount_milli BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT metering_publications_content_check CHECK (trim(content_hash) <> ''),
    CONSTRAINT metering_publications_quote_check CHECK (trim(quote_hash) <> ''),
    CONSTRAINT metering_publications_payload_check CHECK (trim(payload_hash) <> ''),
    CONSTRAINT metering_publications_account_key_unique UNIQUE (account_label, intention_key),
    CONSTRAINT metering_publications_transfer_unique UNIQUE (transfer_id)
);

COMMENT ON TABLE app.metering_publications IS 'Settled INK publication charges: idempotency key, publisher, sealed price terms, canonical hashes, journal transfer and database posted instant, append-only';
COMMENT ON COLUMN app.metering_publications.intention_key IS 'Caller operation token scoped by account: a retry replays instead of charging twice';
COMMENT ON COLUMN app.metering_publications.transfer_id IS 'Journal transfer moving the exact quoted cost: statement and content share one intention';
COMMENT ON COLUMN app.metering_publications.posted_at IS 'Database clock at write, visible only after commit: never a request or browser instant';

CREATE INDEX IF NOT EXISTS metering_publications_account_idx ON app.metering_publications (account_label, posted_at DESC);

DROP TRIGGER IF EXISTS metering_publications_immutable ON app.metering_publications;
CREATE TRIGGER metering_publications_immutable
    BEFORE UPDATE OR DELETE ON app.metering_publications
    FOR EACH ROW EXECUTE FUNCTION app.metering_publication_forbid();

ALTER FUNCTION app.metering_publication_forbid() OWNER TO arena_owner;
ALTER TABLE app.metering_publications OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.metering_publications TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS metering_publications_immutable ON app.metering_publications;
DROP TABLE IF EXISTS app.metering_publications;
DROP FUNCTION IF EXISTS app.metering_publication_forbid();
