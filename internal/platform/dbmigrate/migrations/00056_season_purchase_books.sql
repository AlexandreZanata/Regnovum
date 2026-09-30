-- +goose Up
-- 00056 binds purchase quotes, intents and settlements to season books
-- without moving money (P46-T06): pricing snapshots, INK purchase
-- intents and their paid settlements carry an explicit season_key
-- anchored to app.seasons. Quote expiry and settlement deadlines are
-- capped by the book end at the application layer; the schema only
-- names the book. Pre-season rows bind the explicitly inactive
-- compat-legacy namespace by column default, without rewriting
-- history; ledgers keep their counts and hashes byte for byte.
--
-- Invariants (docs/reino/TEMPORADAS_SUCESSAO.md §4–§5):
-- 1. Additive only: three columns on tables this family owns. No
--    quote, intent, settlement, leg, custody or hold is rewritten;
--    the old diary keeps its counts and hashes, so S never moves
--    and no silent conversion or new mint appears.
-- 2. Book identity per row: each quote, intent and settlement names
--    its book. The same intent key in another book is another
--    purchase; replays resolve per (account, key) with the stored
--    season, never redirecting another book outcome.
-- 3. No mixed books: intent and settlement legs/holds already carry
--    their book via the economy keys (00055); the new columns only
--    name the purchase book beside them, so a cross-book settlement
--    dies on the passphrase check in the adapter before any leg.
-- 4. Compatibility namespace: pre-season rows bind compat-legacy
--    by default, explicitly inactive and never activated. Future
--    quotes and purchases name their book explicitly at the port;
--    making the season mandatory there is the application layer of
--    this same task.
-- 5. Least privilege: no grant changes. arena_app keeps the exact
--    DML it already holds on these tables, never more.
--
-- Consumer tables outside purchase (metering, commerce, crumbs,
-- wallet, charter) keep their shape here; their season references
-- land in the family migrations of P46-T07–T08 with per-family
-- tests.

ALTER TABLE app.pricing_quotes
    ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT 'compat-legacy' REFERENCES app.seasons(season_key) ON DELETE RESTRICT;

COMMENT ON COLUMN app.pricing_quotes.season_key IS 'Book identity of this quotation: expiry is capped by the book end at acceptance; pre-season rows bind the explicitly inactive compat-legacy namespace';

ALTER TABLE app.billing_ink_intents
    ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT 'compat-legacy' REFERENCES app.seasons(season_key) ON DELETE RESTRICT;

COMMENT ON COLUMN app.billing_ink_intents.season_key IS 'Book identity of this purchase: vault, hold and legs settle in the same book; pre-season rows bind the explicitly inactive compat-legacy namespace';

ALTER TABLE app.billing_ink_settlements
    ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT 'compat-legacy' REFERENCES app.seasons(season_key) ON DELETE RESTRICT;

COMMENT ON COLUMN app.billing_ink_settlements.season_key IS 'Book identity of this liquidation: the receipt never leaves its book; pre-season rows bind the explicitly inactive compat-legacy namespace';

-- +goose Down
ALTER TABLE app.billing_ink_settlements DROP COLUMN IF EXISTS season_key;
ALTER TABLE app.billing_ink_intents DROP COLUMN IF EXISTS season_key;
ALTER TABLE app.pricing_quotes DROP COLUMN IF EXISTS season_key;
