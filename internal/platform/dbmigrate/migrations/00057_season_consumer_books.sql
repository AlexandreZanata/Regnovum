-- +goose Up
-- 00057 binds seasonal publication and escrow obligations without
-- moving money (P46-T07): metering publications, commerce transfers
-- and commerce contracts carry an explicit season_key anchored to
-- app.seasons, and contracts additionally carry the terminal clause
-- of section 5.1 (policy reference with hash and both accept
-- evidences) beside the seal. Pre-season rows bind the explicitly
-- inactive compat-legacy namespace by column default, without
-- rewriting history; ledgers keep their counts and hashes byte for
-- byte. Refunds, settlements, tithe reversals and obligations derive
-- their book from the original publication or contract via the
-- existing original_id and contract_id references and need no new
-- column: the adapters refuse cross-book reuse and writes after the
-- seal by reading the parent book in the same transaction.
--
-- Invariants (docs/reino/TEMPORADAS_SUCESSAO.md §4–§5.1):
-- 1. Additive only: three season columns plus three terminal term
--    columns on tables this family owns. No publication, transfer,
--    contract, leg, custody or hold is rewritten; the old diary
--    keeps its counts and hashes, so S never moves and no silent
--    conversion or new mint appears.
-- 2. Book identity per row: each publication, transfer and contract
--    names its book. The same key in another book settles its own
--    outcome instead of redirecting a replay; replays resolve per
--    (account, key) with the stored season, never redirecting
--    another book outcome.
-- 3. No mixed books: publication and transfer legs, holds and escrow
--    custodies already carry their book via the economy keys
--    (00055); the new columns only name the consumer book beside
--    them, so a cross-book settlement dies on the season check in
--    the adapter before any leg.
-- 4. Terminal clause beside the seal: the seal still covers object,
--    parties, amount and expiry; the book, the policy reference
--    with its hash and both accept evidences travel beside it and
--    are matched before any escrow opens. Absence or divergence
--    refuses new seasonal funding; the migration never completes
--    old accepts, and no new INK performance is admitted past the
--    book end. Legacy contracts keep empty terms and their rules.
-- 5. Derived books need no column: metering refunds link to their
--    publication, settlements to their contract, service refunds to
--    their liquidated payment and obligations to their refund, so
--    the book is read from the parent in the writing transaction.
-- 6. Compatibility namespace: pre-season rows bind compat-legacy
--    by default, explicitly inactive and never activated. Future
--    publications, transfers and fundings name their book
--    explicitly at the port; making the season mandatory there is
--    the application layer of this same task.
-- 7. Least privilege: no grant changes. arena_app keeps the exact
--    DML it already holds on these tables, never more.
--
-- Consumer tables outside publication and escrow (crumbs) keep
-- their shape here; their season references land in the family
-- migration of P46-T08 with per-family tests.

ALTER TABLE app.metering_publications
    ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT 'compat-legacy' REFERENCES app.seasons(season_key) ON DELETE RESTRICT;

COMMENT ON COLUMN app.metering_publications.season_key IS 'Book identity of this publication: the legs settle in the same book; pre-season rows bind the explicitly inactive compat-legacy namespace';

ALTER TABLE app.commerce_transfers
    ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT 'compat-legacy' REFERENCES app.seasons(season_key) ON DELETE RESTRICT;

COMMENT ON COLUMN app.commerce_transfers.season_key IS 'Book identity of this transfer: the legs settle in the same book; pre-season rows bind the explicitly inactive compat-legacy namespace';

ALTER TABLE app.commerce_contracts
    ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT 'compat-legacy' REFERENCES app.seasons(season_key) ON DELETE RESTRICT;

COMMENT ON COLUMN app.commerce_contracts.season_key IS 'Book identity of this escrow: the locked legs settle in the same book; pre-season rows bind the explicitly inactive compat-legacy namespace';

ALTER TABLE app.commerce_contracts
    ADD COLUMN IF NOT EXISTS terminal_policy_ref text NOT NULL DEFAULT '';

COMMENT ON COLUMN app.commerce_contracts.terminal_policy_ref IS 'Terminal clause reference of section 5.1: versioned policy accepted by both parties before funding; empty on legacy contracts without the clause';

ALTER TABLE app.commerce_contracts
    ADD COLUMN IF NOT EXISTS terminal_policy_hash text NOT NULL DEFAULT '';

COMMENT ON COLUMN app.commerce_contracts.terminal_policy_hash IS 'Digest of the terminal clause (64 lowercase hex) bound at funding; empty on legacy contracts without the clause';

ALTER TABLE app.commerce_contracts
    ADD COLUMN IF NOT EXISTS buyer_accept_ref text NOT NULL DEFAULT '';

COMMENT ON COLUMN app.commerce_contracts.buyer_accept_ref IS 'Buyer accept evidence recorded before funding; empty on legacy contracts without the clause';

ALTER TABLE app.commerce_contracts
    ADD COLUMN IF NOT EXISTS provider_accept_ref text NOT NULL DEFAULT '';

COMMENT ON COLUMN app.commerce_contracts.provider_accept_ref IS 'Provider accept evidence recorded before funding; empty on legacy contracts without the clause';

-- +goose Down
ALTER TABLE app.commerce_contracts DROP COLUMN IF EXISTS provider_accept_ref;
ALTER TABLE app.commerce_contracts DROP COLUMN IF EXISTS buyer_accept_ref;
ALTER TABLE app.commerce_contracts DROP COLUMN IF EXISTS terminal_policy_hash;
ALTER TABLE app.commerce_contracts DROP COLUMN IF EXISTS terminal_policy_ref;
ALTER TABLE app.commerce_contracts DROP COLUMN IF EXISTS season_key;
ALTER TABLE app.commerce_transfers DROP COLUMN IF EXISTS season_key;
ALTER TABLE app.metering_publications DROP COLUMN IF EXISTS season_key;
