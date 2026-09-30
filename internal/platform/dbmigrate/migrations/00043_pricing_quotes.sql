-- +goose Up
-- 00043 records versioned BTC/BRL purchase quotes (P35-T03): one row per
-- accepted quotation with its integer price, observed/accepted/expiry
-- instants, content hash and one row per source sighting. The row
-- freezes the accepted terms: later webhooks judge the persisted
-- expiry instead of recomputing it, and a withdrawn source never
-- rewrites history.
--
-- Invariants (docs/reino/RESPOSTAS.md item 22, Q22 proposta):
-- 1. Additive only: two new tables plus one trigger function. No
--    existing row is touched. Q22 is PENDENTE, so no ratified TTL,
--    spread or provider lives here: every window arrives per
--    acceptance and is stored beside the price it judged.
-- 2. Terms are sealed at acceptance: accepted_at and expires_at are
--    plain timestamptz written once by the acceptance port, never
--    now() defaults the reader could mistake for policy.
-- 3. Expiry orders acceptance: expires_at is strictly after
--    accepted_at, so a quote with no lifetime dies at 23514 before
--    any sale can read it.
-- 4. One sighting per source and quote: (quote_id, source) is UNIQUE,
--    so a duplicated source dies at 23505 instead of weighing twice.
-- 5. History is retained and immutable: the trigger function refuses
--    UPDATE and DELETE for every role including the owner.
-- 6. Least privilege: arena_owner owns the objects; arena_app may
--    read and append quote rows, never rewrite them.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.pricing_forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'the pricing registry is append-only'
        USING ERRCODE = '23514', CONSTRAINT = 'pricing_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS app.pricing_quotes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    price_minor bigint NOT NULL,
    observed_at timestamptz NOT NULL,
    accepted_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    quote_hash text NOT NULL,
    CONSTRAINT pricing_quotes_price_check CHECK (price_minor BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT pricing_quotes_hash_check CHECK (trim(quote_hash) <> ''),
    CONSTRAINT pricing_quotes_lifetime_check CHECK (expires_at > accepted_at)
);

COMMENT ON TABLE app.pricing_quotes IS 'Accepted BTC/BRL quotation snapshots: integer price, observed/accepted/expiry instants and content hash, append-only';
COMMENT ON COLUMN app.pricing_quotes.price_minor IS 'Guarded median in integer minor units (centavos per BTC): never float';
COMMENT ON COLUMN app.pricing_quotes.quote_hash IS 'Canonical hash binding price, ordered sources and instants: tampering is detectable by recomputation';

CREATE TABLE IF NOT EXISTS app.pricing_quote_sources (
    quote_id uuid NOT NULL REFERENCES app.pricing_quotes(id) ON DELETE RESTRICT,
    source text NOT NULL,
    price_minor bigint NOT NULL,
    observed_at timestamptz NOT NULL,
    payload_hash text NOT NULL,
    CONSTRAINT pricing_quote_sources_key_check CHECK (trim(source) <> ''),
    CONSTRAINT pricing_quote_sources_price_check CHECK (price_minor BETWEEN 1 AND 9223372036854775807),
    CONSTRAINT pricing_quote_sources_hash_check CHECK (trim(payload_hash) <> ''),
    CONSTRAINT pricing_quote_sources_unique UNIQUE (quote_id, source)
);

COMMENT ON TABLE app.pricing_quote_sources IS 'One sighting per source and quote: the frozen source set the acceptance judged';

CREATE INDEX IF NOT EXISTS pricing_quotes_expiry_idx ON app.pricing_quotes (expires_at);

DROP TRIGGER IF EXISTS pricing_quotes_immutable ON app.pricing_quotes;
CREATE TRIGGER pricing_quotes_immutable
    BEFORE UPDATE OR DELETE ON app.pricing_quotes
    FOR EACH ROW EXECUTE FUNCTION app.pricing_forbid_mutation();

DROP TRIGGER IF EXISTS pricing_quote_sources_immutable ON app.pricing_quote_sources;
CREATE TRIGGER pricing_quote_sources_immutable
    BEFORE UPDATE OR DELETE ON app.pricing_quote_sources
    FOR EACH ROW EXECUTE FUNCTION app.pricing_forbid_mutation();

ALTER FUNCTION app.pricing_forbid_mutation() OWNER TO arena_owner;
ALTER TABLE app.pricing_quotes OWNER TO arena_owner;
ALTER TABLE app.pricing_quote_sources OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.pricing_quotes TO arena_app;
GRANT SELECT, INSERT ON app.pricing_quote_sources TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS pricing_quote_sources_immutable ON app.pricing_quote_sources;
DROP TRIGGER IF EXISTS pricing_quotes_immutable ON app.pricing_quotes;
DROP TABLE IF EXISTS app.pricing_quote_sources;
DROP TABLE IF EXISTS app.pricing_quotes;
DROP FUNCTION IF EXISTS app.pricing_forbid_mutation();
